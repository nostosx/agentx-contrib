package utils_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func TestHTTPClientPostJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/test" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Setting") != "override" || r.Header.Get("X-Base") != "base" {
			t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
		}
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL + "/v1/", Client: srv.Client(), Header: http.Header{"X-Setting": {"default"}, "X-Base": {"base"}}}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.PostJSON(t.Context(), "/test", []byte(`{}`), http.Header{"X-Setting": {"override"}}, &out); err != nil || !out.OK {
		t.Fatalf("PostJSON = %+v, %v", out, err)
	}
	if c.Header.Get("X-Setting") != "default" {
		t.Fatal("per-request override modified client defaults")
	}
}

func TestParseAPIError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, kind, code, message string }{
		{name: "openai", body: `{"error":{"type":"rate_limit","message":"slow"}}`, kind: "rate_limit", message: "slow"},
		{name: "gemini", body: `{"error":{"status":"RESOURCE_EXHAUSTED","code":429,"message":"slow"}}`, kind: "RESOURCE_EXHAUSTED", message: "slow"},
		{name: "code", body: `{"error":{"code":"quota","message":"slow"}}`, kind: "quota", code: "quota", message: "slow"},
		{name: "type and code", body: `{"error":{"type":"invalid_request_error","code":"insufficient_quota","message":"slow"}}`, kind: "invalid_request_error", code: "insufficient_quota", message: "slow"},
		{name: "string", body: `{"error":"slow"}`, message: "slow"},
		{name: "flat", body: `{"type":"busy","message":"slow"}`, kind: "busy", message: "slow"},
		{name: "plain", body: " unavailable ", message: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"2"}}}
			err := utils.ParseAPIError("test", resp, []byte(tc.body))
			if err.Provider != "test" || err.Type != tc.kind || err.Code != tc.code || err.Message != tc.message || err.RetryAfter != 2*time.Second || err.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("API error = %+v", err)
			}
		})
	}
}

func TestHTTPClientErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
		api        bool
	}{
		{name: "status", body: `{"error":{"message":"slow"}}`, status: http.StatusTooManyRequests, api: true},
		{name: "invalid json", body: `broken`, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL}
			var out any
			err := c.PostJSON(t.Context(), "/", nil, nil, &out)
			if err == nil {
				t.Fatal("expected error")
			}
			var api *model.APIError
			if errors.As(err, &api) != tc.api {
				t.Fatalf("error type = %T: %v", err, err)
			}
			// A response that does not decode fails again on retry.
			if !tc.api && model.IsRetryable(err) {
				t.Fatalf("decode error retryable: %v", err)
			}
		})
	}
	c := utils.HTTPClient{Provider: "test", BaseURL: "http://bad host"}
	resp, err := c.Post(t.Context(), "/", nil, nil)
	if err == nil {
		resp.Body.Close()
	}
	if err == nil || model.IsRetryable(err) {
		t.Fatalf("invalid URL error = %v", err)
	}
}

// TestHTTPClientBodyReadErrors checks that a response cut off mid-body is
// a retryable transport failure, while an undecodable one is not.
func TestHTTPClientBodyReadErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"partial":`))
		w.(http.Flusher).Flush()
		// Over HTTP/2 this resets the stream after the headers were sent.
		panic(http.ErrAbortHandler)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL, Client: srv.Client()}
	var out any
	err := c.PostJSON(t.Context(), "/", nil, nil, &out)
	if _, ok := errors.AsType[*url.Error](err); !ok || !model.IsRetryable(err) {
		t.Fatalf("PostJSON error = %T %v, want retryable *url.Error", err, err)
	}
	resp, err := c.Post(t.Context(), "/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); !model.IsRetryable(err) {
		t.Fatalf("read error = %v, want retryable", err)
	}
}

func TestHTTPClientInvalidBaseURL(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"api.openai.com/v1", "ftp://example.com", "http:///v1"} {
		c := utils.HTTPClient{Provider: "test", BaseURL: base}
		resp, err := c.Post(t.Context(), "/", nil, nil)
		if err == nil {
			resp.Body.Close()
		}
		if _, ok := errors.AsType[*model.PermanentError](err); !ok {
			t.Errorf("base URL %q: error = %v, want permanent", base, err)
		}
	}
}

func TestQuotaCodeNotRetried(t *testing.T) {
	t.Parallel()
	resp := &http.Response{StatusCode: http.StatusTooManyRequests}
	for body, want := range map[string]bool{
		`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`:       false,
		`{"error":{"type":"requests","code":"organization_spend_limit_exceeded"}}`:  false,
		`{"error":{"type":"tokens","code":"rate_limit_exceeded","message":"slow"}}`: true,
	} {
		if got := utils.ParseAPIError("test", resp, []byte(body)).Retryable(); got != want {
			t.Errorf("%s: Retryable = %v, want %v", body, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{name: "milliseconds take precedence", header: http.Header{"Retry-After-Ms": {"125"}, "Retry-After": {"2"}}, want: 125 * time.Millisecond},
		{name: "fractional seconds", header: http.Header{"Retry-After": {"1.5"}}, want: 1500 * time.Millisecond},
		{name: "invalid", header: http.Header{"Retry-After": {"invalid"}}},
		{name: "past", header: http.Header{"Retry-After": {time.Unix(0, 0).UTC().Format(http.TimeFormat)}}},
		{name: "negative", header: http.Header{"Retry-After": {"-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := utils.ParseRetryAfter(tc.header); got != tc.want {
				t.Errorf("delay = %v, want %v", got, tc.want)
			}
		})
	}
	h := http.Header{"Retry-After": {time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}}
	if got := utils.ParseRetryAfter(h); got < 59*time.Minute || got > time.Hour {
		t.Errorf("HTTP date delay = %v", got)
	}
	if err := utils.ParseAPIError("test", &http.Response{StatusCode: http.StatusInternalServerError}, []byte(strings.Repeat("x", 600))); len(err.Message) > 503 {
		t.Fatal("unbounded error body")
	}
}

func TestServerOf(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]struct {
		host string
		port int
	}{
		"https://api.example.com/v1": {"api.example.com", 443},
		"http://localhost:11434/v1":  {"localhost", 11434},
		"http://[::1]/v1":            {"::1", 80},
		"not a url":                  {"", 0},
	} {
		if host, port := utils.ServerOf(in); host != want.host || port != want.port {
			t.Errorf("ServerOf(%q) = %q, %d", in, host, port)
		}
	}
}

// A stream request answered with something other than an event stream,
// such as a gateway's JSON error with status 200, fails with that error
// instead of an empty stream that looks like a dropped connection.
func TestHTTPClientPostStream(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contentType, body, want string
		ok                            bool
	}{
		{"event stream", "text/event-stream; charset=utf-8", "data: {}\n\n", "", true},
		{"no content type", "", "data: {}\n\n", "", true},
		{"json error", "application/json", `{"error":{"message":"quota exhausted","type":"insufficient_quota"}}`, "quota exhausted", false},
		{"html page", "text/html", "<html>maintenance</html>", "maintenance", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = []string{tc.contentType}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL, Client: srv.Client()}
			resp, err := c.PostStream(t.Context(), "/stream", []byte("{}"), nil)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || model.IsRetryable(err) {
				t.Fatalf("PostStream = %v, retryable %v", err, model.IsRetryable(err))
			}
			if _, ok := errors.AsType[*model.APIError](err); !ok {
				t.Fatalf("error %T is not an APIError", err)
			}
		})
	}
}
