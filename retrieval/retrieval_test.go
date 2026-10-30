package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/rag"
)

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1, 0, 0}}, nil
}

var _ memory.Embedder = fixedEmbedder{}

func TestRetrieveProvidersAndCosineScores(t *testing.T) {
	for _, provider := range []string{"qdrant", "elasticsearch", "elasticsearch-vector"} {
		t.Run(provider, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				b, _ := json.Marshal(body)
				if !strings.Contains(string(b), "tenant-a") {
					t.Fatal("missing server-side namespace")
				}
				if provider == "qdrant" {
					if r.URL.Path != "/collections/docs/points/query" || r.Header.Get("api-key") != "private" {
						t.Error("request path/auth")
					}
					_, _ = w.Write([]byte(`{"result":{"points":[{"score":0.8,"payload":{"namespace":"tenant-a","chunk":{"id":"c","source":"doc","content":"text","metadata":{"lang":"en"}}}}]}}`))
				} else {
					if r.URL.Path != "/docs/_search" || r.Header.Get("Authorization") != "ApiKey private" {
						t.Error("request path/auth")
					}
					score := 0.8
					if provider == "elasticsearch-vector" {
						score = 1.8
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_score": score, "_source": map[string]any{"namespace": "tenant-a", "chunk": map[string]any{"id": "c", "source": "doc", "content": "text", "metadata": map[string]any{"lang": "en"}}}}}}})
				}
			}))
			defer s.Close()
			o := Options{Endpoint: s.URL, Index: "docs", Namespace: "tenant-a", APIKey: "private"}
			var r *Retriever
			var err error
			if provider == "qdrant" {
				o.Embedder = fixedEmbedder{}
				r, err = NewQdrant(o)
			} else {
				if provider == "elasticsearch-vector" {
					o.Embedder = fixedEmbedder{}
				}
				r, err = NewElasticsearch(o)
			}
			if err != nil {
				t.Fatal(err)
			}
			hits, err := r.Retrieve(t.Context(), rag.Query{Text: "text", Limit: 2, Filter: rag.Filter{Sources: []string{"doc"}, Metadata: map[string]any{"lang": "en"}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 || hits[0].Chunk.ID != "c" || hits[0].Score < 0.7999 || hits[0].Score > 0.8001 {
				t.Fatalf("hits: %+v", hits)
			}
		})
	}
}

func TestRetrievalRejectsWrongScopeAndPartialResponses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"scope", `{"hits":{"hits":[{"_score":1,"_source":{"namespace":"other","chunk":{"id":"c","source":"doc"}}}]}}`},
		{"timeout", `{"timed_out":true,"hits":{"hits":[]}}`},
		{"shard failure", `{"_shards":{"failed":1}}`},
		{"missing chunk", `{"hits":{"hits":[{"_score":1,"_source":{"namespace":"tenant-a"}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer s.Close()
			r, err := NewElasticsearch(Options{Endpoint: s.URL, Index: "docs", Namespace: "tenant-a"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Retrieve(t.Context(), rag.Query{Text: "x", Limit: 1}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestRetrievalPreflightAndCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network call") }))
	defer s.Close()
	r, err := NewQdrant(Options{Endpoint: s.URL, Index: "docs", Namespace: "a", Embedder: fixedEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []rag.Query{{Text: "x"}, {Text: "x", Limit: 1, Filter: rag.Filter{SourcePrefix: "doc"}}, {Text: "x", Limit: 1, Filter: rag.Filter{Metadata: map[string]any{"array": []string{"a"}}}}} {
		if _, err := r.Retrieve(t.Context(), q); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Retrieve(ctx, rag.Query{Text: "x", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
