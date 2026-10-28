//go:build integration

package pgvector_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nostosx/agentx-contrib/pgvector"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/memory/memorytest"
	"github.com/nostosx/agentx/rag"
	"github.com/nostosx/agentx/rag/ragtest"
)

func fixture(t *testing.T) (*sql.DB, []pgvector.Option) {
	t.Helper()
	dsn := os.Getenv("AGENTX_IT_PGVECTOR_DSN")
	if dsn == "" {
		t.Skip("set AGENTX_IT_PGVECTOR_DSN to a disposable PostgreSQL database with the vector extension")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	db.SetConnMaxLifetime(time.Minute)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	prefix := fmt.Sprintf("axpg_%x_", time.Now().UnixNano())
	opts := []pgvector.Option{pgvector.WithPrefix(prefix)}
	if err := pgvector.Migrate(t.Context(), db, opts...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, name := range []string{"chunks", "sources", "memories"} {
			if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS "`+prefix+name+`"`); err != nil {
				t.Error(err)
			}
		}
	})
	return db, opts
}

func TestIntegrationVectorStore(t *testing.T) {
	db, opts := fixture(t)
	ragtest.Run(t, func(t *testing.T) rag.VectorStore {
		t.Helper()
		s, err := pgvector.Vectors(db, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
	t.Run("FilterMatchParity", func(t *testing.T) { testFilterMatchParity(t, db, opts) })
}

func testFilterMatchParity(t *testing.T, db *sql.DB, opts []pgvector.Option) {
	t.Helper()
	s, err := pgvector.Vectors(db, append(slices.Clone(opts), pgvector.WithNamespace("filter-parity"))...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(t.Context(), rag.SourceChunks{Info: rag.SourceInfo{Source: "types"}, Chunks: []*rag.Chunk{
		{ID: "best", Index: 0, Embedding: []float32{1, 0}, Metadata: map[string]any{
			"large": int64(9007199254740993), "slice": []int{1, 2}, "nested": map[string]any{"n": 1}, "group": "other", "nil": nil,
		}},
		{ID: "second", Index: 1, Embedding: []float32{1, 1}, Metadata: map[string]any{
			"large": int64(9007199254740994), "slice": []int{1, 2}, "nested": map[string]any{"n": 1}, "group": "wanted", "nil": nil,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(t.Context(), rag.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]any{
		{"large": int64(9007199254740992)},
		{"large": int64(9007199254740994)},
		{"slice": []uint16{1, 2}},
		{"slice": []any{float64(1), float64(2)}},
		{"nested": map[string]any{"n": 1}},
		{"nested": map[string]any{"n": float64(1)}},
		{"nil": nil},
		{"missing": nil},
	} {
		filter := rag.Filter{Metadata: metadata}
		var want []string
		for _, ch := range all {
			if filter.Match(ch) {
				want = append(want, ch.ID)
			}
		}
		chunks, err := s.List(t.Context(), filter)
		if err != nil {
			t.Fatal(err)
		}
		var listed []string
		for _, ch := range chunks {
			listed = append(listed, ch.ID)
		}
		if !slices.Equal(listed, want) {
			t.Fatalf("List(%+v) = %v, core Filter.Match wants %v", metadata, listed, want)
		}
		hits, err := s.Search(t.Context(), []float32{1, 0}, 0, filter)
		if err != nil {
			t.Fatal(err)
		}
		var searched []string
		for _, hit := range hits {
			searched = append(searched, hit.Chunk.ID)
		}
		if !slices.Equal(searched, want) {
			t.Fatalf("Search(%+v) = %v, core Filter.Match wants %v", metadata, searched, want)
		}
	}
	// The first SQL candidate does not match: LIMIT must apply only after
	// the core metadata matcher, or a valid lower-ranked hit disappears.
	hits, err := s.Search(t.Context(), []float32{1, 0}, 1, rag.Filter{Metadata: map[string]any{"group": "wanted"}})
	if err != nil || len(hits) != 1 || hits[0].Chunk.ID != "second" {
		t.Fatalf("post-filter top K = %+v, %v", hits, err)
	}
}

func TestIntegrationMemoryStore(t *testing.T) {
	db, opts := fixture(t)
	memorytest.Run(t, func(t *testing.T) memory.Store {
		t.Helper()
		h := sha256.Sum256([]byte(t.Name()))
		s, err := pgvector.Memory(db, append(slices.Clone(opts), pgvector.WithNamespace(fmt.Sprintf("test-%x", h)))...)
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

type embedder struct{ vector []float32 }

func (e embedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{e.vector}, nil
}

func TestIntegrationRetrievalNamespacesAndFilters(t *testing.T) {
	db, opts := fixture(t)
	ctx := t.Context()
	a, err := pgvector.Memory(db, append(slices.Clone(opts), pgvector.WithNamespace("a"), pgvector.WithEmbedder(embedder{[]float32{1, 0}}))...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pgvector.Memory(db, append(slices.Clone(opts), pgvector.WithNamespace("b"), pgvector.WithEmbedder(embedder{[]float32{1, 0}}))...)
	if err != nil {
		t.Fatal(err)
	}
	entries := []*memory.Entry{
		{ID: "best", Scope: "user", Tags: []string{"go", "agent"}, Embedding: []float32{1, 0}},
		{ID: "second", Scope: "user", Tags: []string{"go"}, Embedding: []float32{1, 1}},
		{ID: "other-scope", Scope: "other", Tags: []string{"go"}, Embedding: []float32{1, 0}},
		{ID: "zero", Scope: "user", Embedding: []float32{0, 0}},
		{ID: "wrong-dimensions", Scope: "user", Embedding: []float32{1}},
		{ID: "missing-vector", Scope: "user"},
	}
	for _, e := range entries {
		if err := a.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Put(ctx, &memory.Entry{ID: "best", Content: "private-b", Embedding: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	hits, err := a.Retrieve(ctx, memory.Query{Text: "query", Filter: memory.Filter{Scope: "user", Tags: []string{"go"}}})
	if err != nil || len(hits) != 2 || hits[0].Entry.ID != "best" || hits[1].Entry.ID != "second" || hits[0].Score != 1 {
		t.Fatalf("filtered retrieval = %+v, %v", hits, err)
	}
	hits, err = a.Retrieve(ctx, memory.Query{Text: "query", Filter: memory.Filter{Scope: "user"}, MinScore: .9, Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].Entry.Content == "private-b" {
		t.Fatalf("threshold retrieval = %+v, %v", hits, err)
	}
	if err := a.Delete(ctx, "best"); err != nil {
		t.Fatal(err)
	}
	if e, err := b.Get(ctx, "best"); err != nil || e.Content != "private-b" {
		t.Fatalf("namespace isolation failed: %+v, %v", e, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Retrieve(cancelled, memory.Query{Text: "query"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retrieval = %v", err)
	}
}

func TestIntegrationVectorAtomicReplacement(t *testing.T) {
	db, opts := fixture(t)
	ctx := t.Context()
	s, err := pgvector.Vectors(db, opts...)
	if err != nil {
		t.Fatal(err)
	}
	source := func(value string) rag.SourceChunks {
		return rag.SourceChunks{Info: rag.SourceInfo{Source: "shared", Hash: value}, Chunks: []*rag.Chunk{
			{ID: "first", DocumentID: "doc", Index: 0, Content: value, Embedding: []float32{1, 0}, Metadata: map[string]any{"array": []string{"a", "b"}, "nil": nil}},
			{ID: "second", DocumentID: "doc", Index: 1, Content: value, Embedding: []float32{0, 0}},
		}}
	}
	if err := s.Replace(ctx, source("initial")); err != nil {
		t.Fatal(err)
	}
	// Force an insertion failure after source update and old-chunk deletion.
	bad := source("bad")
	bad.Chunks[1].Metadata = map[string]any{"unsupported": "\x00"}
	if err := s.Replace(ctx, bad); err == nil {
		t.Fatal("PostgreSQL JSONB NUL restriction did not fail")
	}
	infos, err := s.Sources(ctx)
	if err != nil || len(infos) != 1 || infos[0].Hash != "initial" {
		t.Fatalf("failed replacement changed source: %+v, %v", infos, err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			other, err := pgvector.Vectors(db, opts...)
			if err != nil {
				t.Error(err)
				return
			}
			if err := other.Replace(ctx, source(fmt.Sprint(i))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	chunks, err := s.List(ctx, rag.Filter{})
	if err != nil || len(chunks) != 2 || chunks[0].Content != chunks[1].Content {
		t.Fatalf("mixed concurrent replacement: %+v, %v", chunks, err)
	}
	for _, f := range []rag.Filter{
		{Metadata: map[string]any{"array": []any{"a", "b"}, "nil": nil}},
		{SourcePrefix: "sha"},
	} {
		hits, err := s.Search(ctx, []float32{1, 0}, 0, f)
		if err != nil || len(hits) == 0 || hits[0].Score != 1 {
			t.Fatalf("Search(%+v) = %+v, %v", f, hits, err)
		}
	}
	hits, err := s.Search(ctx, []float32{1}, 0, rag.Filter{})
	if err != nil || len(hits) != 2 || hits[0].Score != 0 || hits[1].Score != 0 {
		t.Fatalf("mixed dimensions = %+v, %v", hits, err)
	}
	isolated, err := pgvector.Vectors(db, append(slices.Clone(opts), pgvector.WithNamespace("isolated"))...)
	if err != nil {
		t.Fatal(err)
	}
	if err := isolated.Delete(ctx, "shared"); err != nil {
		t.Fatal(err)
	}
	if err := isolated.Replace(ctx, source("private")); err != nil {
		t.Fatal(err)
	}
	if chunks, err := s.List(ctx, rag.Filter{}); err != nil || len(chunks) != 2 || strings.Contains(chunks[0].Content, "private") {
		t.Fatalf("vector namespace leaked: %+v, %v", chunks, err)
	}
}
