// Package search adapts Tavily and Serper to agentx web search.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	core "github.com/nostosx/agentx/search"
)

// Options contains trusted application configuration. No environment is read.
type Options struct {
	APIKey string
	// Endpoint overrides the complete search endpoint, for proxies or tests.
	Endpoint string
	Client   *http.Client
}

// Client implements core.Searcher. It never follows result URLs or retries.
type Client struct {
	endpoint, key, provider string
	http                    *http.Client
}

// NewTavily constructs a basic Tavily web search client.
func NewTavily(o Options) (*Client, error) {
	return newClient(o, "tavily", "https://api.tavily.com/search")
}

// NewSerper constructs a Serper Google organic search client.
func NewSerper(o Options) (*Client, error) {
	return newClient(o, "serper", "https://google.serper.dev/search")
}

func newClient(o Options, provider, endpoint string) (*Client, error) {
	if strings.TrimSpace(o.APIKey) == "" || strings.ContainsAny(o.APIKey, "\r\n") {
		return nil, errors.New("search: API key is required")
	}
	if o.Endpoint != "" {
		endpoint = o.Endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("search: invalid endpoint")
	}
	h := &http.Client{Timeout: 30 * time.Second}
	if o.Client != nil {
		*h = *o.Client
	}
	// Credentials must not be forwarded by a provider redirect.
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: endpoint, key: o.APIKey, provider: provider, http: h}, nil
}

// Search performs a bounded search, preserving the provider's result order.
func (c *Client) Search(ctx context.Context, r core.Request) (*core.Response, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	n := r.Limit
	if n == 0 {
		n = 5
	}
	maxResults := 20
	if c.provider == "serper" {
		maxResults = 100
	}
	if n > maxResults {
		return nil, fmt.Errorf("search: %s limit exceeds %d", c.provider, maxResults)
	}
	body := map[string]any{"query": r.Query, "max_results": n, "search_depth": "basic", "include_answer": false, "include_raw_content": false}
	if c.provider == "serper" {
		body = map[string]any{"q": r.Query, "num": n}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("search: create request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.provider == "tavily" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else {
		req.Header.Set("X-API-KEY", c.key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("search: transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search: %s HTTP %d", c.provider, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("search: read response: %w", err)
	}
	if len(data) > 4<<20 {
		return nil, errors.New("search: response exceeds 4 MiB")
	}
	var wire struct {
		Results   []core.Result `json:"results"`
		RequestID string        `json:"request_id"`
		Organic   []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, errors.New("search: invalid JSON response")
	}
	out := &core.Response{Results: wire.Results, RequestID: wire.RequestID}
	if c.provider == "serper" {
		out.Results = nil
		for _, v := range wire.Organic {
			out.Results = append(out.Results, core.Result{Title: v.Title, URL: v.Link, Content: v.Snippet})
		}
	}
	if len(out.Results) > n {
		out.Results = out.Results[:n]
	}
	for _, v := range out.Results {
		u, err := url.Parse(v.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return nil, errors.New("search: provider returned an invalid source URL")
		}
	}
	return out, nil
}

var _ core.Searcher = (*Client)(nil)
