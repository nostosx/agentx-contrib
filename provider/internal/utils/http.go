package utils

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nostosx/kitx/utf8text"

	"github.com/nostosx/agentx/model"
)

// HTTPClient sends JSON requests to a provider API.
type HTTPClient struct {
	Provider string
	BaseURL  string
	Client   *http.Client
	Header   http.Header
	// Query is added to the URL of every request, as Azure OpenAI's
	// api-version.
	Query url.Values
	// Token, when set, is called for every request; its result is sent as
	// a bearer token in the Authorization header.
	Token func(context.Context) (string, error)
}

// Post sends body as JSON to path and returns the response. Responses with
// an error status are converted to *model.APIError and their body closed.
// Errors reading the returned body, other than io.EOF, are reported as
// *url.Error, like the errors of http.Client.Do, so that model.IsRetryable
// treats a connection lost mid-response as a transport failure.
func (c *HTTPClient) Post(ctx context.Context, path string, body []byte, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err == nil && ((req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "") {
		err = &url.Error{Op: "parse", URL: req.URL.Redacted(), Err: errors.New("base URL needs an http or https scheme and a host")}
	}
	if err != nil {
		// An invalid URL fails the same way on every attempt.
		return nil, model.Permanent(fmt.Errorf("%s: %w", c.Provider, err))
	}
	req.Header.Set("Content-Type", "application/json")
	AddHeaders(req.Header, c.Header)
	for k := range header {
		req.Header.Del(k)
	}
	AddHeaders(req.Header, header)
	if err := c.prepare(ctx, req); err != nil {
		return nil, err
	}
	resp, err := Do(c.Client, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.Provider, err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, ParseAPIError(c.Provider, resp, b)
	}
	resp.Body = &bodyReader{ReadCloser: resp.Body, url: req.URL.Redacted()}
	return resp, nil
}

// bodyReader reports read errors of a response body as *url.Error.
type bodyReader struct {
	io.ReadCloser
	url string
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF { //nolint:errorlint // Readers return io.EOF itself.
		err = &url.Error{Op: "read", URL: b.url, Err: err}
	}
	return n, err
}

// AddHeaders adds every value of src to dst.
func AddHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// ClientOr returns c, or http.DefaultClient when c is nil.
func ClientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

// Do sends req while keeping redirects inside its original origin. The
// caller's client is copied so its redirect policy remains unchanged.
func Do(c *http.Client, req *http.Request) (*http.Response, error) {
	client := *ClientOr(c)
	policy := client.CheckRedirect
	scheme, host := req.URL.Scheme, req.URL.Hostname()
	_, port := ServerOf(req.URL.String())
	checkOrigin := func(next *http.Request) error {
		if next.URL != nil {
			_, nextPort := ServerOf(next.URL.String())
			if next.URL.Scheme == scheme && strings.EqualFold(next.URL.Hostname(), host) && nextPort == port {
				return nil
			}
		}
		return model.Permanent(errors.New("provider redirect crosses the configured origin"))
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if err := checkOrigin(next); err != nil {
			return err
		}
		if policy != nil {
			if err := policy(next, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return model.Permanent(errors.New("stopped after 10 redirects"))
		}
		// A custom policy can change the redirect URL. Check its result too.
		return checkOrigin(next)
	}
	return client.Do(req)
}

// PostJSON sends body and decodes a JSON response into out.
func (c *HTTPClient) PostJSON(ctx context.Context, path string, body []byte, header http.Header, out any) error {
	resp, err := c.Post(ctx, path, body, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode response: %w", c.Provider, err)
	}
	return nil
}

// ParseAPIError builds an APIError from an error response. It understands
// the error envelopes of OpenAI, Anthropic and Gemini.
func ParseAPIError(provider string, resp *http.Response, body []byte) *model.APIError {
	e := &model.APIError{Provider: provider, StatusCode: resp.StatusCode, RetryAfter: ParseRetryAfter(resp.Header)}
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(body, &env) == nil {
		var obj struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Status  string          `json:"status"`
		}
		var str string
		switch {
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &obj) == nil:
			e.Message = obj.Message
			// Gemini sends a numeric code and the status name.
			_ = json.Unmarshal(obj.Code, &e.Code)
			e.Type = cmp.Or(obj.Type, obj.Status, e.Code)
		case len(env.Error) > 0 && json.Unmarshal(env.Error, &str) == nil:
			e.Message = str
		default:
			e.Message, e.Type = env.Message, env.Type
		}
	}
	if e.Message == "" {
		e.Message = utf8text.Truncate(strings.TrimSpace(string(body)), 500, "...")
	}
	return e
}

// ParseRetryAfter reads Retry-After (seconds or HTTP date) and the
// retry-after-ms extension.
func ParseRetryAfter(h http.Header) time.Duration {
	if ms := h.Get("Retry-After-Ms"); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil && v > 0 {
			return time.Duration(v * float64(time.Millisecond))
		}
	}
	ra := h.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(ra, 64); err == nil && v > 0 {
		return time.Duration(v * float64(time.Second))
	}
	if t, err := http.ParseTime(ra); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// ServerOf returns the host and port of baseURL, with the scheme's default
// port when none is given. It returns zero values for an unparsable URL.
func ServerOf(baseURL string) (string, int) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "", 0
	}
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return u.Hostname(), p
	}
	if u.Scheme == "http" {
		return u.Hostname(), 80
	}
	return u.Hostname(), 443
}

// PostStream is Post for a response streamed as Server-Sent Events. A
// response of another media type, such as the JSON error or HTML page a
// gateway may send with status 200, is returned as a permanent
// *model.APIError holding its body, rather than read as a stream without
// events. A response without a Content-Type is read as events.
func (c *HTTPClient) PostStream(ctx context.Context, path string, body []byte, header http.Header) (*http.Response, error) {
	resp, err := c.Post(ctx, path, body, header)
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); ct == "" || err == nil && mt == "text/event-stream" {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	e := ParseAPIError(c.Provider, resp, b)
	e.Message = fmt.Sprintf("expected an event stream, got %s: %s", ct, e.Message)
	return nil, model.Permanent(e)
}
