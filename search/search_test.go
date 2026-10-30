package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/nostosx/agentx/search"
)

func TestSearchProviders(t *testing.T) {
	for _, name := range []string{"tavily", "serper"} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error(r.Method)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if name == "tavily" {
					if r.Header.Get("Authorization") != "Bearer private" || body["query"] != "golang" || body["max_results"] != float64(2) {
						t.Errorf("bad request: %+v", body)
					}
					_, _ = w.Write([]byte(`{"request_id":"r1","results":[{"title":"Go","url":"https://go.dev","content":"language","score":0.9}]}`))
				} else {
					if r.Header.Get("X-API-KEY") != "private" || body["q"] != "golang" || body["num"] != float64(2) {
						t.Errorf("bad request: %+v", body)
					}
					_, _ = w.Write([]byte(`{"organic":[{"title":"Go","link":"https://go.dev","snippet":"language"}]}`))
				}
			}))
			defer s.Close()
			constructor := NewTavily
			if name == "serper" {
				constructor = NewSerper
			}
			client, err := constructor(Options{APIKey: "private", Endpoint: s.URL})
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Search(t.Context(), core.Request{Query: "golang", Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Results) != 1 || out.Results[0].Title != "Go" || out.Results[0].URL != "https://go.dev" || out.Results[0].Content != "language" {
				t.Fatalf("%+v", out)
			}
		})
	}
}

func TestSearchFailuresAndNoRedirect(t *testing.T) {
	var contacted atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Add(1) }))
	defer destination.Close()
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"private error", "private upstream document", http.StatusTooManyRequests},
		{"invalid JSON", "{", http.StatusOK},
		{"unsafe URL", `{"results":[{"url":"file:///private"}]}`, http.StatusOK},
		{"oversize", strings.Repeat("x", (4<<20)+1), http.StatusOK},
		{"redirect", "", http.StatusTemporaryRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "redirect" {
					w.Header().Set("Location", destination.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			client, err := NewTavily(Options{APIKey: "secret", Endpoint: s.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Search(t.Context(), core.Request{Query: "query"})
			if err == nil || strings.Contains(err.Error(), "private upstream") {
				t.Fatalf("error: %v", err)
			}
		})
	}
	if contacted.Load() != 0 {
		t.Fatal("credential-bearing redirect followed")
	}
}

func TestSearchCancellationAndLimits(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer s.Close()
	c, err := NewTavily(Options{APIKey: "key", Endpoint: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Search(ctx, core.Request{Query: "query"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := c.Search(t.Context(), core.Request{Query: "query", Limit: 21}); err == nil {
		t.Fatal("unsupported limit accepted")
	}
}
