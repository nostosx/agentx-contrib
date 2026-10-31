// Package web implements readable HTML extraction and bounded URL loading.
package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	cacheloader "github.com/nostosx/servex-contrib/cache/loader"
	"github.com/nostosx/servex-contrib/transport/httpx"

	core "github.com/nostosx/agentx/web"
)

// ExtractOptions bounds parsing. FullPage keeps the body rather than selecting
// an article, useful for browser forms and documentation navigation.
type ExtractOptions struct {
	FullPage       bool
	MaxHTMLBytes   int
	MaxOutputBytes int
}

// Extractor is an immutable, concurrent-safe core.Extractor.
type Extractor struct{ o ExtractOptions }

// NewExtractor uses Readability and CommonMark conversion without network I/O.
func NewExtractor(o ExtractOptions) (*Extractor, error) {
	if o.MaxHTMLBytes == 0 {
		o.MaxHTMLBytes = 4 << 20
	}
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = 1 << 20
	}
	if o.MaxHTMLBytes < 1 || o.MaxHTMLBytes > 32<<20 || o.MaxOutputBytes < 1 || o.MaxOutputBytes > 16<<20 {
		return nil, errors.New("web: invalid extraction limits")
	}
	return &Extractor{o: o}, nil
}

// Extract implements core.Extractor. Relative links become absolute source URLs.
func (e *Extractor) Extract(ctx context.Context, raw string, data []byte) (*core.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := core.ValidateURL(raw); err != nil {
		return nil, err
	}
	if len(data) > e.o.MaxHTMLBytes {
		return nil, errors.New("web: HTML exceeds limit")
	}
	u, _ := url.Parse(raw)
	parser := readability.NewParser()
	parser.MaxElemsToParse = 50000
	article, err := parser.Parse(bytes.NewReader(data), u)
	if err != nil {
		return nil, fmt.Errorf("web: extract article: %w", err)
	}
	content := article.Content
	if e.o.FullPage || strings.TrimSpace(content) == "" {
		content = string(data)
	}
	markdown, err := htmltomarkdown.ConvertString(content, converter.WithDomain(raw))
	if err != nil {
		return nil, fmt.Errorf("web: convert Markdown: %w", err)
	}
	tree, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("web: parse content: %w", err)
	}
	var links []core.Link
	var text strings.Builder
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			var href string
			for _, a := range n.Attr {
				if a.Key == "href" {
					href = a.Val
				}
			}
			if target, err := u.Parse(href); err == nil && href != "" && core.ValidateURL(target.String()) == nil && !seen[target.String()] {
				seen[target.String()] = true
				var label strings.Builder
				var collect func(*html.Node)
				collect = func(c *html.Node) {
					if c.Type == html.TextNode {
						label.WriteString(c.Data)
					}
					for child := c.FirstChild; child != nil; child = child.NextSibling {
						collect(child)
					}
				}
				collect(n)
				if len(links) < 1000 {
					links = append(links, core.Link{Text: strings.TrimSpace(label.String()), URL: target.String()})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(tree)
	plain := strings.Join(strings.Fields(text.String()), " ")
	if len(plain) > e.o.MaxOutputBytes || len(markdown) > e.o.MaxOutputBytes {
		return nil, errors.New("web: extracted content exceeds limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &core.Document{URL: raw, Title: article.Title, Text: plain, Markdown: markdown, Links: links, Metadata: map[string]string{"byline": article.Byline, "site_name": article.SiteName, "excerpt": article.Excerpt}}, nil
}

// Options configures a Reader. Client is borrowed; when nil a restricted Servex
// HTTP client rejects private/reserved addresses and pins checked DNS results.
// A supplied client owns its network/proxy policy. Cache is borrowed and must
// use an application namespace covering credentials, tenancy and extractor config.
type Options struct {
	Client       *http.Client
	Extractor    core.Extractor
	Cache        *cacheloader.Loader[core.Document]
	Namespace    string
	MaxBodyBytes int64
}

// Reader fetches HTML using application-selected transport and cache facilities.
type Reader struct {
	o      Options
	client *http.Client
	owned  bool
}

// NewReader constructs a Reader without fetching a URL.
func NewReader(o Options) (*Reader, error) {
	if o.MaxBodyBytes == 0 {
		o.MaxBodyBytes = 4 << 20
	}
	if o.MaxBodyBytes < 1 || o.MaxBodyBytes > 32<<20 || (o.Cache != nil && o.Namespace == "") {
		return nil, errors.New("web: invalid body limit or missing cache namespace")
	}
	if o.Extractor == nil {
		var err error
		o.Extractor, err = NewExtractor(ExtractOptions{MaxHTMLBytes: int(o.MaxBodyBytes)})
		if err != nil {
			return nil, err
		}
	}
	r := &Reader{o: o, client: o.Client}
	if r.client == nil {
		var err error
		r.client, err = httpx.NewRestrictedHTTPClient(httpx.HTTPPolicy{MaxResponseBytes: o.MaxBodyBytes})
		if err != nil {
			return nil, err
		}
		r.owned = true
	}
	return r, nil
}

// Read implements core.Reader. No linked resource is fetched and no paid retry
// or cache implementation is added here.
func (r *Reader) Read(ctx context.Context, raw string) (*core.Document, error) {
	if err := core.ValidateURL(raw); err != nil {
		return nil, err
	}
	load := func(ctx context.Context) (core.Document, error) {
		doc, err := r.fetch(ctx, raw)
		if err != nil {
			return core.Document{}, err
		}
		return *doc, nil
	}
	var value core.Document
	var err error
	if r.o.Cache != nil {
		value, err = r.o.Cache.Get(ctx, r.o.Namespace+":"+raw, load)
	} else {
		value, err = load(ctx)
	}
	if err != nil {
		return nil, err
	}
	value.Links = slices.Clone(value.Links)
	value.Metadata = maps.Clone(value.Metadata)
	return &value, nil
}

func (r *Reader) fetch(ctx context.Context, raw string) (*core.Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("web: invalid request")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain")
	res, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("web: fetch failed")
	}
	defer res.Body.Close()
	if err := core.ValidateURL(res.Request.URL.String()); err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, r.o.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("web: read body: %w", err)
	}
	if int64(len(data)) > r.o.MaxBodyBytes {
		return nil, errors.New("web: body exceeds limit")
	}
	ct, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		return nil, errors.New("web: missing or invalid content type")
	}
	if ct != "text/html" && ct != "application/xhtml+xml" && ct != "text/plain" {
		return nil, errors.New("web: unsupported content type")
	}
	decoded, err := charset.NewReader(bytes.NewReader(data), res.Header.Get("Content-Type"))
	if err != nil {
		return nil, errors.New("web: invalid charset")
	}
	utf8data, err := io.ReadAll(io.LimitReader(decoded, r.o.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(utf8data)) > r.o.MaxBodyBytes {
		return nil, errors.New("web: decoded body exceeds limit")
	}
	final := res.Request.URL.String()
	if ct == "text/plain" {
		return &core.Document{URL: final, Text: string(utf8data), Markdown: string(utf8data)}, nil
	}
	return r.o.Extractor.Extract(ctx, final, utf8data)
}

// Close closes idle connections only when Reader constructed the HTTP client.
// Borrowed clients and cache loaders remain owned by the application.
func (r *Reader) Close() error {
	if r.owned {
		r.client.CloseIdleConnections()
	}
	return nil
}

var _ core.Reader = (*Reader)(nil)
var _ core.Extractor = (*Extractor)(nil)
