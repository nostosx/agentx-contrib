package retrieval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nostosx/agentx/rag"
)

func TestQdrantPartialWriteInvalidatesHashAndRetries(t *testing.T) {
	points := map[string]map[string]any{}
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/scroll"):
			var manifests []any
			for id, payload := range points {
				if payload["agentx_record"] == "manifest" {
					manifests = append(manifests, map[string]any{"id": id, "payload": payload})
				}
			}
			if manifests == nil {
				manifests = []any{}
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"points": manifests, "next_page_offset": nil}}); err != nil {
				t.Error(err)
			}
			return
		case r.Method == http.MethodPut:
			var batch []struct {
				ID      string         `json:"id"`
				Payload map[string]any `json:"payload"`
			}
			if err := json.Unmarshal(body["points"], &batch); err != nil {
				t.Error(err)
				return
			}
			for _, p := range batch {
				points[p.ID] = p.Payload
				if fail && p.Payload["agentx_record"] == "chunk" {
					http.Error(w, "injected interruption", http.StatusInternalServerError)
					return
				}
			}
		case strings.HasSuffix(r.URL.Path, "/delete"):
			if body["filter"] != nil {
				for id, p := range points {
					if p["agentx_record"] == "chunk" {
						delete(points, id)
					}
				}
			} else {
				var ids []string
				if err := json.Unmarshal(body["points"], &ids); err != nil {
					t.Error(err)
					return
				}
				for _, id := range ids {
					delete(points, id)
				}
			}
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
		fmt.Fprint(w, `{"result":{"status":"completed"}}`)
	}))
	defer server.Close()
	w, err := NewQdrantWriter(WriterOptions{Endpoint: server.URL, Index: "fixture", Namespace: "tenant", Dimensions: 3, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	src := rag.SourceChunks{Info: rag.SourceInfo{Source: "source", Hash: "one", Chunks: 1}, Chunks: []*rag.Chunk{{ID: "one", Source: "source", Content: "first", Embedding: []float32{1, 0, 0}}}}
	if err := w.Write(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	fail = true
	src.Info.Hash = "two"
	src.Chunks[0].ID = "two"
	if err := w.Write(t.Context(), src); err == nil {
		t.Fatal("partial failure accepted")
	}
	infos, err := w.Sources(t.Context())
	if err != nil || len(infos) != 1 || infos[0].Hash != "" {
		t.Fatalf("failed source still reported unchanged: %+v %v", infos, err)
	}
	fail = false
	if err := w.Write(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	infos, err = w.Sources(t.Context())
	if err != nil || infos[0].Hash != "two" || len(points) != 2 {
		t.Fatalf("retry left stale chunks: %+v %v %d", infos, err, len(points))
	}
	if err := w.Delete(t.Context(), "source"); err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatal("delete left manifest/chunks")
	}
}

func TestSourceDirectoryPagination(t *testing.T) {
	for _, provider := range []string{"qdrant", "elasticsearch"} {
		t.Run(provider, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				start := 0
				if provider == "qdrant" {
					if body["offset"] != nil {
						if err := json.Unmarshal(body["offset"], &start); err != nil {
							t.Error(err)
						}
					}
				} else {
					if body["search_after"] != nil {
						var after []string
						if err := json.Unmarshal(body["search_after"], &after); err != nil {
							t.Error(err)
						}
						start, _ = strconv.Atoi(after[0])
						start++
					}
				}
				var records []any
				for i := start; i < min(start+100, 205); i++ {
					source := fmt.Sprintf("source-%03d", i)
					p := map[string]any{"namespace": "tenant", "agentx_record": "manifest", "source_info": rag.SourceInfo{Source: source, Hash: "complete"}}
					if provider == "qdrant" {
						records = append(records, map[string]any{"payload": p})
					} else {
						records = append(records, map[string]any{"_source": p, "sort": []string{strconv.Itoa(i)}})
					}
				}
				var reply any
				if provider == "qdrant" {
					var next any
					if start+100 < 205 {
						next = start + 100
					}
					reply = map[string]any{"result": map[string]any{"points": records, "next_page_offset": next}}
				} else {
					reply = map[string]any{"hits": map[string]any{"hits": records}}
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(reply); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			w, err := newWriter(WriterOptions{Endpoint: server.URL, Index: "fixture", Namespace: "tenant", Dimensions: 3}, provider)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			infos, err := w.Sources(t.Context())
			if err != nil || len(infos) != 205 || requests != 3 || infos[204].Source != "source-204" {
				t.Fatalf("directory silently truncated: %d %d %v", len(infos), requests, err)
			}
			w.o.MaxSources = 10
			if _, err := w.Sources(t.Context()); err == nil {
				t.Fatal("bounded directory returned partial success")
			}
		})
	}
}

func TestElasticsearchBulkAndDeletionFailures(t *testing.T) {
	mode := "bulk"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_bulk") {
			if mode == "bulk" {
				fmt.Fprint(w, `{"errors":false,"items":[{"index":{"status":429}}]}`)
			} else {
				fmt.Fprint(w, `{"errors":false,"items":[{"index":{"status":201}}]}`)
			}
			return
		}
		fmt.Fprint(w, `{"timed_out":false,"version_conflicts":1,"failures":[],"deleted":0}`)
	}))
	defer server.Close()
	w, err := NewElasticsearchWriter(WriterOptions{Endpoint: server.URL, Index: "fixture", Namespace: "tenant"})
	if err != nil {
		t.Fatal(err)
	}
	src := rag.SourceChunks{Info: rag.SourceInfo{Source: "source", Hash: "hash", Chunks: 1}, Chunks: []*rag.Chunk{{ID: "c", Source: "source", Content: "body"}}}
	if err := w.Write(t.Context(), src); err == nil || requests != 1 {
		t.Fatal("HTTP 200 bulk item failure accepted", err, requests)
	}
	mode = "delete"
	if err := w.Write(t.Context(), src); err == nil {
		t.Fatal("delete_by_query conflicts accepted")
	}
	before := requests
	src.Chunks[0].Source = "other"
	if err := w.Write(t.Context(), src); err == nil || requests != before {
		t.Fatal("invalid chunk mutated backend")
	}
}
