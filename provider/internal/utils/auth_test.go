package utils_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func TestHTTPClientTokenAndQuery(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-2" || r.URL.Query().Get("api-version") != "v1" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("request = %s %v", r.URL, r.Header)
		}
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	var calls atomic.Int32
	c := utils.HTTPClient{
		Provider: "test", BaseURL: srv.URL, Client: srv.Client(),
		Header: http.Header{"Authorization": {"Bearer static"}},
		Query:  url.Values{"api-version": {"v1"}},
		Token: func(ctx context.Context) (string, error) {
			if ctx == nil {
				t.Error("no context")
			}
			// A token is fetched for every request.
			if calls.Add(1) == 1 {
				return "token-1", nil
			}
			return "token-2", nil
		},
	}
	if _, err := c.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	var out struct{}
	if err := c.PostJSON(t.Context(), "/x?alt=sse", []byte(`{}`), nil, &out); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPClientTokenErrors(t *testing.T) {
	t.Parallel()
	transient := &url.Error{Op: "Post", URL: "https://oauth2.invalid", Err: errors.New("connection reset")}
	for _, tc := range []struct {
		name      string
		token     string
		err       error
		retryable bool
	}{
		{name: "transport failure", err: transient, retryable: true},
		{name: "permanent failure", err: model.Permanent(errors.New("invalid_grant"))},
		{name: "empty token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := utils.HTTPClient{Provider: "test", BaseURL: "http://127.0.0.1:1", Token: func(context.Context) (string, error) { return tc.token, tc.err }}
			resp, err := c.Post(t.Context(), "/x", nil, nil)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error = %v, retryable = %v", err, model.IsRetryable(err))
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("error chain lost: %v", err)
			}
		})
	}
}

func TestVertexBaseURL(t *testing.T) {
	t.Parallel()
	for loc, want := range map[string]string{
		"global":   "https://aiplatform.googleapis.com/v1/projects/p/locations/global/publishers/google",
		"us":       "https://aiplatform.us.rep.googleapis.com/v1/projects/p/locations/us/publishers/google",
		"eu":       "https://aiplatform.eu.rep.googleapis.com/v1/projects/p/locations/eu/publishers/google",
		"us-east5": "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/google",
	} {
		if got := utils.VertexBaseURL("p", loc, "google"); got != want {
			t.Errorf("VertexBaseURL(%q) = %s, want %s", loc, got, want)
		}
	}
}

func TestHasAuth(t *testing.T) {
	t.Parallel()
	if utils.HasAuth(http.Header{}, "x-api-key") || !utils.HasAuth(http.Header{"X-Api-Key": {"k"}}, "x-api-key") || !utils.HasAuth(http.Header{"Authorization": {"Bearer k"}}) {
		t.Fatal("HasAuth mismatch")
	}
}
