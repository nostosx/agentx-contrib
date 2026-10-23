package utils_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/model"
)

func TestHTTPClientRedirectBoundary(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"other host", "other port", "https downgrade"} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/custom=%v", kind, custom), func(t *testing.T) {
				t.Parallel()
				var received atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = io.WriteString(w, `{}`)
				}))
				defer target.Close()
				location := target.URL
				if kind == "other host" {
					location = strings.Replace(location, "127.0.0.1", "localhost", 1)
				}
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for _, key := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
						if r.Header.Get(key) != "test-secret" {
							t.Errorf("initial request lost %s", key)
						}
					}
					http.Redirect(w, r, location, http.StatusTemporaryRedirect)
				})
				var origin *httptest.Server
				if kind == "https downgrade" {
					origin = httptest.NewTLSServer(handler)
				} else {
					origin = httptest.NewServer(handler)
				}
				defer origin.Close()
				var client *http.Client
				if custom || kind == "https downgrade" {
					client = origin.Client()
				}
				if custom {
					client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
				}
				header := make(http.Header)
				for _, key := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
					header.Set(key, "test-secret")
				}
				c := utils.HTTPClient{Provider: "test", BaseURL: origin.URL, Client: client, Header: header}
				resp, err := c.Post(t.Context(), "/", []byte(`{"prompt":"private prompt"}`), nil)
				if resp != nil {
					resp.Body.Close()
				}
				if _, ok := errors.AsType[*model.PermanentError](err); !ok || model.IsRetryable(err) {
					t.Fatalf("redirect error = %v, want permanent error", err)
				}
				if got := received.Load(); got != 0 {
					t.Fatalf("redirect target received %d private requests", got)
				}
				if custom && client.CheckRedirect(nil, nil) != nil {
					t.Fatal("provider changed caller's redirect policy")
				}
				if !custom && client != nil && client.CheckRedirect != nil {
					t.Fatal("provider installed a policy on caller's client")
				}
			})
		}
	}
}

func TestHTTPClientSameOriginRedirect(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "follow", true: "custom stop"}[stop], func(t *testing.T) {
			t.Parallel()
			var received, policies atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					http.Redirect(w, r, "/final", http.StatusPermanentRedirect)
					return
				}
				received.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || string(body) != "private prompt" || r.Header.Get("X-Api-Key") != "test-secret" {
					t.Errorf("same-origin redirect lost request: %s %q %v", r.Method, body, r.Header)
				}
				_, _ = io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			client := srv.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error {
				policies.Add(1)
				if stop {
					return http.ErrUseLastResponse
				}
				return nil
			}
			c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL, Client: client, Header: http.Header{"X-Api-Key": {"test-secret"}}}
			resp, err := c.Post(t.Context(), "/", []byte("private prompt"), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			want := int32(1)
			if stop {
				want = 0
			}
			if received.Load() != want || policies.Load() != 1 {
				t.Fatalf("received = %d, policy calls = %d", received.Load(), policies.Load())
			}
		})
	}
}

func TestHTTPClientRedirectPolicyCannotChangeOrigin(t *testing.T) {
	t.Parallel()
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/same-origin", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		req.URL, _ = url.Parse(target.URL)
		return nil
	}
	c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL, Client: client}
	resp, err := c.Post(t.Context(), "/", []byte("private prompt"), nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || received.Load() != 0 {
		t.Fatalf("policy bypassed origin: received = %d, error = %v", received.Load(), err)
	}
}

func TestHTTPClientDefaultRedirectLimit(t *testing.T) {
	t.Parallel()
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := utils.HTTPClient{Provider: "test", BaseURL: srv.URL}
	resp, err := c.Post(t.Context(), "/", []byte("private prompt"), nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || received.Load() != 10 || model.IsRetryable(err) {
		t.Fatalf("redirect loop: received = %d, error = %v", received.Load(), err)
	}
}
