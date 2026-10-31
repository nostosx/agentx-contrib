package loader

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nostosx/agentx/rag"
)

// NotionOptions limits loading to explicit page IDs. Child pages are not
// followed; add them to PageIDs. Block children within each page are recursive.
type NotionOptions struct {
	Options
	PageIDs    []string
	APIVersion string
	MaxDepth   int
}

// NewNotion loads page titles and recursive block content. It does not query
// databases or fetch attachments. The default API version is 2025-09-03.
func NewNotion(o NotionOptions) (*Connector, error) {
	o.PageIDs = slices.Clone(o.PageIDs)
	if len(o.PageIDs) == 0 {
		return nil, errors.New("loader: Notion page IDs required")
	}
	for _, id := range o.PageIDs {
		if !identityPattern.MatchString(id) {
			return nil, errors.New("loader: invalid Notion page ID")
		}
	}
	if o.APIVersion == "" {
		o.APIVersion = "2025-09-03"
	}
	if strings.ContainsAny(o.APIVersion, "\r\n") {
		return nil, errors.New("loader: invalid Notion API version")
	}
	if o.MaxDepth == 0 {
		o.MaxDepth = 32
	}
	if o.MaxDepth < 1 || o.MaxDepth > 100 {
		return nil, errors.New("loader: invalid block depth")
	}
	c, err := newConnector(o.Options, "https://api.notion.com", []any{"notion", o.PageIDs, o.APIVersion, o.MaxDepth})
	if err != nil {
		return nil, err
	}
	c.headers = []string{"Notion-Version", o.APIVersion}
	c.load = func(ctx context.Context) ([]*rag.Document, error) {
		var docs []*rag.Document
		pages := 0
		total := 0
		for _, id := range o.PageIDs {
			var page struct {
				ID         string `json:"id"`
				URL        string `json:"url"`
				Archived   bool   `json:"archived"`
				InTrash    bool   `json:"in_trash"`
				LastEdited string `json:"last_edited_time"`
				Properties map[string]struct {
					Type  string     `json:"type"`
					Title []richText `json:"title"`
				} `json:"properties"`
			}
			if err := c.get(ctx, "/v1/pages/{id}", map[string]any{"id": id}, &page); err != nil {
				return nil, err
			}
			if page.ID == "" || strings.ReplaceAll(page.ID, "-", "") != strings.ReplaceAll(id, "-", "") {
				return nil, errors.New("loader: missing Notion page identity")
			}
			if page.Archived || page.InTrash {
				continue
			}
			title := ""
			for _, p := range page.Properties {
				if p.Type == "title" {
					title = rich(p.Title)
				}
			}
			visited := map[string]bool{}
			var walk func(string, int) (string, error)
			walk = func(blockID string, depth int) (string, error) {
				if depth > o.MaxDepth || visited[blockID] {
					return "", limitError()
				}
				visited[blockID] = true
				var out strings.Builder
				cursor := ""
				seen := map[string]bool{}
				for {
					pages++
					if pages > c.o.MaxPages {
						return "", limitError()
					}
					query := map[string]any{"id": blockID, "page_size": 100}
					if cursor != "" {
						query["start_cursor"] = cursor
					}
					var result struct {
						Results    []notionBlock `json:"results"`
						HasMore    bool          `json:"has_more"`
						NextCursor string        `json:"next_cursor"`
					}
					if err := c.get(ctx, "/v1/blocks/{id}/children", query, &result); err != nil {
						return "", err
					}
					if result.Results == nil {
						return "", errors.New("loader: missing Notion block listing")
					}
					for _, block := range result.Results {
						if block.ID == "" || !identityPattern.MatchString(block.ID) {
							return "", errors.New("loader: invalid Notion block")
						}
						text, err := renderNotion(block)
						if err != nil {
							return "", err
						}
						out.WriteString(text)
						out.WriteString("\n")
						total += len(text) + 1
						if total > c.o.MaxBytes {
							return "", limitError()
						}
						if block.HasChildren && block.Type != "child_page" {
							child, err := walk(block.ID, depth+1)
							if err != nil {
								return "", err
							}
							out.WriteString(child)
						}
					}
					if !result.HasMore {
						break
					}
					if result.NextCursor == "" || seen[result.NextCursor] {
						return "", errors.New("loader: invalid Notion pagination")
					}
					cursor = result.NextCursor
					seen[cursor] = true
				}
				return out.String(), nil
			}
			content, err := walk(id, 0)
			if err != nil {
				return nil, err
			}
			doc := document("notion://"+strings.ReplaceAll(page.ID, "-", ""), title, content, page.URL)
			doc.Metadata["last_edited_time"] = page.LastEdited
			docs = append(docs, doc)
			if len(docs) > c.o.MaxDocuments {
				return nil, limitError()
			}
		}
		return docs, nil
	}
	return c, nil
}

type richText struct {
	PlainText string `json:"plain_text"`
	Href      string `json:"href"`
	Text      struct {
		Content string `json:"content"`
		Link    *struct {
			URL string `json:"url"`
		} `json:"link"`
	} `json:"text"`
}

func rich(items []richText) string {
	var b strings.Builder
	for _, r := range items {
		s := r.PlainText
		if s == "" {
			s = r.Text.Content
		}
		href := r.Href
		if href == "" && r.Text.Link != nil {
			href = r.Text.Link.URL
		}
		if href != "" {
			b.WriteString("[" + s + "](" + href + ")")
		} else {
			b.WriteString(s)
		}
	}
	return b.String()
}

type notionBlock struct {
	ID          string                     `json:"id"`
	Type        string                     `json:"type"`
	HasChildren bool                       `json:"has_children"`
	Raw         map[string]json.RawMessage `json:"-"`
}

func (b *notionBlock) UnmarshalJSON(data []byte) error {
	type plain notionBlock
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = notionBlock(v)
	return json.Unmarshal(data, &b.Raw)
}
func renderNotion(b notionBlock) (string, error) {
	var content struct {
		RichText             []richText   `json:"rich_text"`
		Cells                [][]richText `json:"cells"`
		Title, URL, Language string
		Checked              bool
		Type                 string
		External             struct{ URL string }
		File                 struct{ URL string }
	}
	if err := json.Unmarshal(b.Raw[b.Type], &content); err != nil {
		return "", errors.New("loader: invalid Notion block content")
	}
	text := rich(content.RichText)
	switch b.Type {
	case "paragraph", "quote", "callout", "toggle":
		return text, nil
	case "heading_1":
		return "# " + text, nil
	case "heading_2":
		return "## " + text, nil
	case "heading_3":
		return "### " + text, nil
	case "bulleted_list_item":
		return "- " + text, nil
	case "numbered_list_item":
		return "1. " + text, nil
	case "to_do":
		if content.Checked {
			return "- [x] " + text, nil
		}
		return "- [ ] " + text, nil
	case "code":
		return "```" + content.Language + "\n" + text + "\n```", nil
	case "divider":
		return "---", nil
	case "table_row":
		var cells []string
		for _, cell := range content.Cells {
			cells = append(cells, rich(cell))
		}
		return "| " + strings.Join(cells, " | ") + " |", nil
	case "table", "column", "column_list", "synced_block":
		return "", nil
	case "child_page":
		return "[Child page: " + content.Title + "](notion://" + strings.ReplaceAll(b.ID, "-", "") + ")", nil
	case "bookmark", "embed", "link_preview":
		return content.URL, nil
	case "image", "file", "pdf", "video", "audio":
		link := content.External.URL
		if content.Type == "file" {
			// Expiring provider URLs must not change an unchanged snapshot hash.
			link = "notion://block/" + b.ID + "/" + b.Type
		}
		return "[" + b.Type + "](" + link + ")", nil
	default:
		return "", errors.New("loader: unsupported Notion block type: " + b.Type)
	}
}
