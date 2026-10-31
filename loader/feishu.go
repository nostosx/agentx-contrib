package loader

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nostosx/agentx/rag"
)

// FeishuOptions selects docx document IDs, not a tenant-wide crawl. Token is
// supplied by the application (for example using its Servex OAuth facilities).
type FeishuOptions struct {
	Options
	DocumentIDs []string
	// DocumentBaseURL names the tenant's real /docx URL prefix when known.
	// Empty keeps stable feishu:// sources without inventing a frontend URL.
	DocumentBaseURL string
}

// NewFeishu loads docx text blocks at the document's reported revision. It
// preserves block hierarchy, fails unsupported content, and fetches no binaries.
func NewFeishu(o FeishuOptions) (*Connector, error) {
	o.DocumentIDs = slices.Clone(o.DocumentIDs)
	if o.DocumentBaseURL != "" {
		u, err := url.Parse(o.DocumentBaseURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("loader: invalid Feishu document URL prefix")
		}
	}
	if len(o.DocumentIDs) == 0 {
		return nil, errors.New("loader: Feishu document IDs required")
	}
	for _, id := range o.DocumentIDs {
		if !identityPattern.MatchString(id) {
			return nil, errors.New("loader: invalid Feishu document ID")
		}
	}
	c, err := newConnector(o.Options, "https://open.feishu.cn", []any{"feishu", o.DocumentIDs, o.DocumentBaseURL})
	if err != nil {
		return nil, err
	}
	c.load = func(ctx context.Context) ([]*rag.Document, error) {
		var docs []*rag.Document
		pages := 0
		total := 0
		rawBytes := 0
		for _, id := range o.DocumentIDs {
			var info struct {
				Code int `json:"code"`
				Data *struct {
					Document *struct {
						ID       string `json:"document_id"`
						Title    string `json:"title"`
						Revision int64  `json:"revision_id"`
					} `json:"document"`
				} `json:"data"`
			}
			if err := c.get(ctx, "/open-apis/docx/v1/documents/{id}", map[string]any{"id": id}, &info); err != nil {
				return nil, err
			}
			if info.Code != 0 || info.Data == nil || info.Data.Document == nil || info.Data.Document.ID != id {
				return nil, errors.New("loader: Feishu document unavailable")
			}
			documentInfo := info.Data.Document
			cursor := ""
			seen := map[string]bool{}
			blocks := map[string]feishuBlock{}
			for {
				pages++
				if pages > c.o.MaxPages {
					return nil, limitError()
				}
				query := map[string]any{"id": id, "page_size": 500, "document_revision_id": documentInfo.Revision}
				if cursor != "" {
					query["page_token"] = cursor
				}
				var listing struct {
					Code int `json:"code"`
					Data *struct {
						Items     []feishuBlock `json:"items"`
						HasMore   bool          `json:"has_more"`
						PageToken string        `json:"page_token"`
					} `json:"data"`
				}
				if err := c.get(ctx, "/open-apis/docx/v1/documents/{id}/blocks", query, &listing); err != nil {
					return nil, err
				}
				if listing.Code != 0 || listing.Data == nil || listing.Data.Items == nil {
					return nil, errors.New("loader: incomplete Feishu blocks")
				}
				for _, b := range listing.Data.Items {
					encoded, err := json.Marshal(b.Raw)
					if err != nil {
						return nil, err
					}
					rawBytes += len(encoded)
					if rawBytes > c.o.MaxBytes {
						return nil, limitError()
					}
					if b.ID == "" || blocks[b.ID].ID != "" {
						return nil, errors.New("loader: duplicate or invalid Feishu block")
					}
					blocks[b.ID] = b
					if len(blocks) > c.o.MaxPages*500 {
						return nil, limitError()
					}
				}
				if !listing.Data.HasMore {
					break
				}
				if listing.Data.PageToken == "" || seen[listing.Data.PageToken] {
					return nil, errors.New("loader: invalid Feishu pagination")
				}
				cursor = listing.Data.PageToken
				seen[cursor] = true
			}
			root, ok := blocks[id]
			if !ok || root.Type != 1 {
				return nil, errors.New("loader: missing Feishu document root")
			}
			visited := map[string]bool{}
			var content strings.Builder
			var walk func(string, int) error
			walk = func(blockID string, depth int) error {
				b, ok := blocks[blockID]
				if !ok || visited[blockID] || depth > 100 {
					return errors.New("loader: incomplete or cyclic Feishu hierarchy")
				}
				visited[blockID] = true
				text, err := renderFeishu(b)
				if err != nil {
					return err
				}
				total += len(text) + 1
				if total > c.o.MaxBytes {
					return limitError()
				}
				content.WriteString(text)
				content.WriteByte('\n')
				for _, child := range b.Children {
					if err := walk(child, depth+1); err != nil {
						return err
					}
				}
				return nil
			}
			if err := walk(id, 0); err != nil {
				return nil, err
			}
			if len(visited) != len(blocks) {
				return nil, errors.New("loader: disconnected Feishu blocks")
			}
			webURL := ""
			if o.DocumentBaseURL != "" {
				webURL = strings.TrimRight(o.DocumentBaseURL, "/") + "/" + id
			}
			doc := document("feishu://docx/"+id, documentInfo.Title, content.String(), webURL)
			doc.Metadata["revision_id"] = strconv.FormatInt(documentInfo.Revision, 10)
			docs = append(docs, doc)
			if len(docs) > c.o.MaxDocuments {
				return nil, limitError()
			}
		}
		return docs, nil
	}
	return c, nil
}

type feishuBlock struct {
	ID       string                     `json:"block_id"`
	Type     int                        `json:"block_type"`
	Children []string                   `json:"children"`
	Raw      map[string]json.RawMessage `json:"-"`
}

func (b *feishuBlock) UnmarshalJSON(data []byte) error {
	type plain feishuBlock
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = feishuBlock(v)
	return json.Unmarshal(data, &b.Raw)
}
func renderFeishu(b feishuBlock) (string, error) {
	if b.Type == 1 || b.Type == 24 || b.Type == 25 || b.Type == 31 || b.Type == 32 {
		return "", nil
	}
	key := "text"
	switch {
	case b.Type >= 3 && b.Type <= 11:
		key = "heading" + strconv.Itoa(b.Type-2)
	case b.Type == 12:
		key = "bullet"
	case b.Type == 13:
		key = "ordered"
	case b.Type == 14:
		key = "code"
	case b.Type == 15:
		key = "quote"
	case b.Type == 17:
		key = "todo"
	case b.Type == 22:
		return "---", nil
	case b.Type != 2:
		return "", errors.New("loader: unsupported Feishu block type: " + strconv.Itoa(b.Type))
	}
	var body struct {
		Style struct {
			Done bool `json:"done"`
		} `json:"style"`
		Elements []struct {
			TextRun *struct {
				Content string `json:"content"`
			} `json:"text_run"`
			Equation *struct {
				Content string `json:"content"`
			} `json:"equation"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(b.Raw[key], &body); err != nil {
		return "", errors.New("loader: malformed Feishu text block")
	}
	var out strings.Builder
	for _, e := range body.Elements {
		switch {
		case e.TextRun != nil:
			out.WriteString(e.TextRun.Content)
		case e.Equation != nil:
			out.WriteString("$" + e.Equation.Content + "$")
		default:
			return "", errors.New("loader: unsupported Feishu text element")
		}
	}
	text := out.String()
	switch {
	case b.Type >= 3 && b.Type <= 11:
		text = strings.Repeat("#", b.Type-2) + " " + text
	case b.Type == 12:
		text = "- " + text
	case b.Type == 13:
		text = "1. " + text
	case b.Type == 14:
		text = "```\n" + text + "\n```"
	case b.Type == 17:
		text = "- [ ] " + text
		if body.Style.Done {
			text = "- [x] " + out.String()
		}
	case b.Type == 15:
		text = "> " + text
	}
	return text, nil
}
