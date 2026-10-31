//go:build integration

package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nostosx/agentx/rag"
)

func requestJSON(t *testing.T, ctx context.Context, method, endpoint string, body any) {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		t.Fatalf("%s %s: %d %s", method, endpoint, res.StatusCode, b)
	}
}

func testPayload(namespace, id, source, lang string) map[string]any {
	return map[string]any{"namespace": namespace, "chunk": map[string]any{"id": id, "document_id": source, "source": source, "content": "golang evidence", "metadata": map[string]any{"lang": lang}}}
}

func TestIntegrationQdrant(t *testing.T) {
	endpoint := os.Getenv("AGENTX_IT_QDRANT_URL")
	if endpoint == "" {
		t.Skip("set AGENTX_IT_QDRANT_URL to a disposable Qdrant")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	index := "agentx_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	requestJSON(t, t.Context(), http.MethodPut, endpoint+"/collections/"+index, map[string]any{"vectors": map[string]any{"size": 3, "distance": "Cosine"}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		requestJSON(t, ctx, http.MethodDelete, endpoint+"/collections/"+index, nil)
	})
	points := []any{}
	for i, ns := range []string{"tenant-a", "tenant-a", "tenant-b"} {
		lang := "en"
		if i == 1 {
			lang = "zh"
		}
		v := []float32{1, 0, 0}
		if i == 1 {
			v = []float32{0.8, 0.6, 0}
		}
		points = append(points, map[string]any{"id": i + 1, "vector": v, "payload": testPayload(ns, fmt.Sprint(i), "doc/"+fmt.Sprint(i), lang)})
	}
	requestJSON(t, t.Context(), http.MethodPut, endpoint+"/collections/"+index+"/points?wait=true", map[string]any{"points": points})
	r, err := NewQdrant(Options{Endpoint: endpoint, Index: index, Namespace: "tenant-a", Embedder: fixedEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := r.Retrieve(t.Context(), rag.Query{Text: "golang", Limit: 10, MinScore: 0.7})
	if err != nil || len(hits) != 2 || hits[0].Score < 0.999 || hits[1].Score < 0.799 || hits[1].Score > 0.801 {
		t.Fatalf("cosine and namespace: %+v %v", hits, err)
	}
	hits, err = r.Retrieve(t.Context(), rag.Query{Text: "golang", Limit: 10, Filter: rag.Filter{Sources: []string{"doc/0"}, Metadata: map[string]any{"lang": "en"}}})
	if err != nil || len(hits) != 1 || hits[0].Chunk.Source != "doc/0" {
		t.Fatalf("filter: %+v %v", hits, err)
	}
}

func TestIntegrationElasticsearch(t *testing.T) {
	endpoint := os.Getenv("AGENTX_IT_ELASTICSEARCH_URL")
	if endpoint == "" {
		t.Skip("set AGENTX_IT_ELASTICSEARCH_URL to a disposable Elasticsearch")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	index := "agentx_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	mapping := map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": map[string]any{"properties": map[string]any{
		"namespace": map[string]any{"type": "keyword"}, "embedding": map[string]any{"type": "dense_vector", "dims": 3, "similarity": "cosine"},
		"chunk": map[string]any{"properties": map[string]any{"source": map[string]any{"type": "keyword"}, "content": map[string]any{"type": "text"}, "metadata": map[string]any{"properties": map[string]any{"lang": map[string]any{"type": "keyword"}}}}},
	}}}
	requestJSON(t, t.Context(), http.MethodPut, endpoint+"/"+index, mapping)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		requestJSON(t, ctx, http.MethodDelete, endpoint+"/"+index, nil)
	})
	for i, ns := range []string{"tenant-a", "tenant-a", "tenant-b"} {
		lang := "en"
		if i == 1 {
			lang = "zh"
		}
		payload := testPayload(ns, fmt.Sprint(i), "doc/"+fmt.Sprint(i), lang)
		v := []float32{1, 0, 0}
		if i == 1 {
			v = []float32{0.8, 0.6, 0}
		}
		payload["embedding"] = v
		requestJSON(t, t.Context(), http.MethodPut, fmt.Sprintf("%s/%s/_doc/%d?refresh=true", endpoint, index, i), payload)
	}
	for _, vector := range []bool{false, true} {
		t.Run(fmt.Sprint(vector), func(t *testing.T) {
			o := Options{Endpoint: endpoint, Index: index, Namespace: "tenant-a"}
			if vector {
				o.Embedder = fixedEmbedder{}
			}
			r, err := NewElasticsearch(o)
			if err != nil {
				t.Fatal(err)
			}
			hits, err := r.Retrieve(t.Context(), rag.Query{Text: "golang", Limit: 10})
			if err != nil || len(hits) != 2 {
				t.Fatalf("namespace: %+v %v", hits, err)
			}
			if vector && (hits[0].Score < 0.999 || hits[1].Score < 0.799 || hits[1].Score > 0.801) {
				t.Fatalf("cosine conversion: %+v", hits)
			}
			hits, err = r.Retrieve(t.Context(), rag.Query{Text: "golang", Limit: 10, Filter: rag.Filter{SourcePrefix: "doc/", Sources: []string{"doc/0"}, Metadata: map[string]any{"lang": "en"}}})
			if err != nil || len(hits) != 1 {
				t.Fatalf("filter: %+v %v", hits, err)
			}
		})
	}
}

func TestIntegrationQdrantWriter(t *testing.T) {
	endpoint := os.Getenv("AGENTX_IT_QDRANT_URL")
	if endpoint == "" {
		t.Skip("set AGENTX_IT_QDRANT_URL")
	}
	index := "agentx_write_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	requestJSON(t, t.Context(), http.MethodPut, endpoint+"/collections/"+index, map[string]any{"vectors": map[string]any{"size": 3, "distance": "Cosine"}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		requestJSON(t, ctx, http.MethodDelete, endpoint+"/collections/"+index, nil)
	})
	options := Options{Endpoint: endpoint, Index: index, Namespace: "tenant-a", Embedder: fixedEmbedder{}}
	writer, err := NewQdrantWriter(WriterOptions{Options: options, Dimensions: 3, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	retriever, err := NewQdrant(options)
	if err != nil {
		t.Fatal(err)
	}
	writerLifecycle(t, writer, retriever, func() rag.SourceWriter {
		options.Namespace = "tenant-b"
		other, err := NewQdrantWriter(WriterOptions{Options: options, Dimensions: 3})
		if err != nil {
			t.Fatal(err)
		}
		return other
	})
}

func TestIntegrationElasticsearchWriter(t *testing.T) {
	endpoint := os.Getenv("AGENTX_IT_ELASTICSEARCH_URL")
	if endpoint == "" {
		t.Skip("set AGENTX_IT_ELASTICSEARCH_URL")
	}
	index := "agentx_write_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	requestJSON(t, t.Context(), http.MethodPut, endpoint+"/"+index, map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": map[string]any{"properties": map[string]any{"namespace": map[string]any{"type": "keyword"}, "agentx_record": map[string]any{"type": "keyword"}, "source_key": map[string]any{"type": "keyword"}, "embedding": map[string]any{"type": "dense_vector", "dims": 3, "similarity": "cosine"}, "chunk": map[string]any{"properties": map[string]any{"source": map[string]any{"type": "keyword"}, "content": map[string]any{"type": "text"}}}}}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		requestJSON(t, ctx, http.MethodDelete, endpoint+"/"+index, nil)
	})
	options := Options{Endpoint: endpoint, Index: index, Namespace: "tenant-a", Embedder: fixedEmbedder{}}
	writer, err := NewElasticsearchWriter(WriterOptions{Options: options, Dimensions: 3, BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	retriever, err := NewElasticsearch(options)
	if err != nil {
		t.Fatal(err)
	}
	writerLifecycle(t, writer, retriever, func() rag.SourceWriter {
		options.Namespace = "tenant-b"
		other, err := NewElasticsearchWriter(WriterOptions{Options: options, Dimensions: 3})
		if err != nil {
			t.Fatal(err)
		}
		return other
	})
}

func writerLifecycle(t *testing.T, w rag.SourceWriter, r rag.Retriever, other func() rag.SourceWriter) {
	t.Helper()
	ctx := t.Context()
	ix := rag.NewIndex(rag.Config{Writer: w, Retriever: r, Embedder: fixedEmbedder{}, BatchSize: 1})
	docs := []*rag.Document{{ID: "a", Source: "a", Content: "golang first"}, {ID: "b", Source: "b", Content: "golang second"}}
	result, err := ix.Sync(ctx, docs)
	if err != nil || len(result.Added) != 2 {
		t.Fatalf("initial Sync: %+v %v", result, err)
	}
	result, err = ix.Add(ctx, docs...)
	if err != nil || len(result.Unchanged) != 2 {
		t.Fatalf("unchanged: %+v %v", result, err)
	}
	hits, err := r.Retrieve(ctx, rag.Query{Text: "golang", Limit: 100})
	if err != nil || len(hits) != 2 {
		t.Fatalf("manifest leaked into retrieval: %+v %v", hits, err)
	}
	docs[0].Content = "golang replacement"
	result, err = ix.Sync(ctx, docs[:1])
	if err != nil || len(result.Deleted) != 1 || result.Deleted[0] != "b" {
		t.Fatalf("replace/delete: %+v %v", result, err)
	}
	hits, err = r.Retrieve(ctx, rag.Query{Text: "golang", Limit: 100})
	if err != nil || len(hits) != 1 || hits[0].Chunk.Content != "golang replacement" {
		t.Fatalf("old chunks retained: %+v %v", hits, err)
	}
	o := other()
	oi := rag.NewIndex(rag.Config{Writer: o, Embedder: fixedEmbedder{}, BatchSize: 1})
	if _, err := oi.Add(ctx, docs[0]); err != nil {
		t.Fatal(err)
	}
	if err := ix.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	infos, err := w.Sources(ctx)
	if err != nil || len(infos) != 0 {
		t.Fatalf("directory not empty: %+v %v", infos, err)
	}
	infos, err = o.Sources(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("namespace crossed: %+v %v", infos, err)
	}
	if err := o.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}
