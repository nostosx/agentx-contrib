package cohere

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/rag"
)

func TestNewReranker(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "")
	if _, err := NewReranker(); err == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("COHERE_API_KEY", "env-key")
	r, err := NewReranker()
	if err != nil || r.http.Header.Get("Authorization") != "Bearer env-key" {
		t.Fatalf("reranker=%+v err=%v", r, err)
	}
	for _, tc := range []struct {
		name string
		opt  Option
	}{
		{name: "negative top n", opt: WithTopN(-1)},
		{name: "negative truncation", opt: WithMaxTokensPerDoc(-1)},
		{name: "invalid score", opt: WithMinScore(1.1)},
		{name: "empty model", opt: WithModel("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewReranker(tc.opt); err == nil {
				t.Fatal("invalid option accepted")
			}
		})
	}
}

func TestRerank(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v2/rerank" || req.Header.Get("Authorization") != "Bearer refreshed" {
			t.Errorf("request=%s auth=%s", req.URL.Path, req.Header.Get("Authorization"))
		}
		var body struct {
			Model           string   `json:"model"`
			Query           string   `json:"query"`
			Documents       []string `json:"documents"`
			TopN            int      `json:"top_n"`
			MaxTokensPerDoc int      `json:"max_tokens_per_doc"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "custom" || body.Query != "question" || len(body.Documents) != 2 || body.Documents[0] != "header\n\nfirst" || body.TopN != 2 || body.MaxTokensPerDoc != 128 {
			t.Errorf("body=%+v", body)
		}
		fmt.Fprint(w, `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.2}]}`)
	}))
	defer srv.Close()
	r, err := NewReranker(WithTokenSource(func(context.Context) (string, error) { return "refreshed", nil }), WithModel("custom"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithTopN(3), WithMaxTokensPerDoc(128), WithMinScore(0.3))
	if err != nil {
		t.Fatal(err)
	}
	hits := []rag.Hit{{Chunk: &rag.Chunk{ID: "a", Context: "header", Content: "first"}, Score: 10}, {Chunk: &rag.Chunk{ID: "b", Content: "second"}, Score: 11}}
	got, err := r.Rerank(t.Context(), "question", hits)
	if err != nil || len(got) != 1 || got[0].Chunk.ID != "b" || got[0].Score != 0.9 || hits[0].Score != 10 || hits[1].Score != 11 {
		t.Fatalf("got=%+v input=%+v err=%v", got, hits, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Rerank(ctx, "question", hits); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if got, err := r.Rerank(t.Context(), "question", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty hits=%v err=%v", got, err)
	}
}

func TestRerankErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, payload string
		status        int
		retryable     bool
	}{
		{name: "missing results", payload: `{}`, status: http.StatusOK},
		{name: "missing index", payload: `{"results":[{"relevance_score":0.5}]}`, status: http.StatusOK},
		{name: "missing score", payload: `{"results":[{"index":0}]}`, status: http.StatusOK},
		{name: "out of range", payload: `{"results":[{"index":2,"relevance_score":0.5}]}`, status: http.StatusOK},
		{name: "duplicate", payload: `{"results":[{"index":0,"relevance_score":0.5},{"index":0,"relevance_score":0.1}]}`, status: http.StatusOK},
		{name: "invalid score", payload: `{"results":[{"index":0,"relevance_score":2}]}`, status: http.StatusOK},
		{name: "retryable", payload: `{"message":"busy"}`, status: http.StatusServiceUnavailable, retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.payload)
			}))
			defer srv.Close()
			r, err := NewReranker(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Rerank(t.Context(), "q", []rag.Hit{{Chunk: &rag.Chunk{Content: "doc"}}}); err == nil || model.IsRetryable(err) != tc.retryable {
				t.Fatalf("error=%v", err)
			}
		})
	}
	r, err := NewReranker(WithAPIKey("fixture"), WithBaseURL("http://unused.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rerank(t.Context(), "q", []rag.Hit{{}}); err == nil || model.IsRetryable(err) {
		t.Fatalf("invalid candidate error=%v", err)
	}
}
