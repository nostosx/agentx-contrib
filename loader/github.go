package loader

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/nostosx/agentx/rag"
)

// GitHubOptions selects a repository ref and file/directory prefixes. Paths is
// required; use ["."] explicitly for the whole repository. Symlinks/submodules
// are ignored. Unsupported parser extensions are outside the indexed scope.
type GitHubOptions struct {
	Options
	Owner, Repo, Ref string
	Paths            []string
	Parsers          map[string]rag.Parser
}

// NewGitHub loads a single immutable tree snapshot using GitHub's Git API.
func NewGitHub(o GitHubOptions) (*Connector, error) {
	o.Paths = slices.Clone(o.Paths)
	o.Parsers = maps.Clone(o.Parsers)
	if !identityPattern.MatchString(o.Owner) || !identityPattern.MatchString(o.Repo) || o.Ref == "" || len(o.Paths) == 0 {
		return nil, errors.New("loader: GitHub owner, repo, ref and paths required")
	}
	for _, p := range o.Paths {
		if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") {
			return nil, errors.New("loader: invalid repository path")
		}
	}
	if o.Parsers == nil {
		o.Parsers = rag.DefaultParsers()
	}
	// Parser/config changes require an application Scope change for delta state.
	c, err := newConnector(o.Options, "https://api.github.com", []any{"github", o.Owner, o.Repo, o.Ref, o.Paths})
	if err != nil {
		return nil, err
	}
	c.headers = []string{"Accept", "application/vnd.github+json", "X-GitHub-Api-Version", "2022-11-28"}
	c.load = func(ctx context.Context) ([]*rag.Document, error) {
		prefix := "/repos/" + o.Owner + "/" + o.Repo
		var tree struct {
			SHA       string `json:"sha"`
			Truncated bool   `json:"truncated"`
			Tree      []struct {
				Path, Type, Mode, SHA string
				Size                  int64
			} `json:"tree"`
		}
		if err := c.get(ctx, prefix+"/git/trees/{ref}", map[string]any{"ref": o.Ref, "recursive": "1"}, &tree); err != nil {
			return nil, err
		}
		if tree.Truncated || tree.SHA == "" || tree.Tree == nil {
			return nil, errors.New("loader: incomplete GitHub tree")
		}
		var docs []*rag.Document
		total := 0
		for _, entry := range tree.Tree {
			if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
				continue
			}
			selected := false
			for _, p := range o.Paths {
				if p == "." || entry.Path == p || strings.HasPrefix(entry.Path, p+"/") {
					selected = true
					break
				}
			}
			if !selected {
				continue
			}
			parser := o.Parsers[strings.ToLower(path.Ext(entry.Path))]
			if parser == nil {
				continue
			}
			if entry.Size > int64(c.o.MaxBytes) || !identityPattern.MatchString(entry.SHA) {
				return nil, errors.New("loader: oversized or invalid GitHub blob")
			}
			var blob struct {
				Encoding, Content, SHA string
				Size                   int64
			}
			if err := c.get(ctx, prefix+"/git/blobs/{sha}", map[string]any{"sha": entry.SHA}, &blob); err != nil {
				return nil, err
			}
			if blob.Encoding != "base64" || blob.SHA != entry.SHA {
				return nil, errors.New("loader: invalid GitHub blob")
			}
			data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
			if err != nil {
				return nil, errors.New("loader: invalid GitHub base64")
			}
			total += len(data)
			if total > c.o.MaxBytes {
				return nil, limitError()
			}
			source := "github://" + o.Owner + "/" + o.Repo + "/" + entry.Path
			parsed, err := parser.Parse(ctx, source, data)
			if err != nil {
				return nil, err
			}
			// Keep one document per file, so the complete snapshot has stable sources.
			var content strings.Builder
			for _, d := range parsed {
				if d == nil {
					return nil, errors.New("loader: parser returned nil document")
				}
				content.WriteString(d.Content)
				content.WriteString("\n\n")
			}
			doc := document(source, entry.Path, content.String(), "https://github.com/"+o.Owner+"/"+o.Repo+"/blob/"+url.PathEscape(o.Ref)+"/"+entry.Path)
			doc.Metadata["blob_sha"] = entry.SHA
			docs = append(docs, doc)
			if len(docs) > c.o.MaxDocuments {
				return nil, limitError()
			}
		}
		return docs, nil
	}
	return c, nil
}
