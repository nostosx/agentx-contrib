//go:build integration

package retrieval

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	pg "github.com/nostosx/servex-contrib/database/postgres"
	textpg "github.com/nostosx/servex-contrib/search/postgres"
	vectorpg "github.com/nostosx/servex-contrib/vector/pgvector"

	"github.com/nostosx/agentx/rag"
)

func TestIntegrationServexPostgres(t *testing.T) {
	dsn := os.Getenv("AGENTX_IT_SERVEX_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AGENTX_IT_SERVEX_POSTGRES_DSN to an owned vector-enabled database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	schema := "ax_it_" + strings.ToLower(rand.Text())
	ident, err := pg.Ident(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	v, err := vectorpg.New(db, vectorpg.Config{Table: pg.Table{Schema: schema, Name: "vectors"}, Namespace: "tenant", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	text, err := textpg.New(db, textpg.Config{Table: pg.Table{Schema: schema, Name: "documents"}, Namespace: "tenant", Engine: textpg.Native})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := text.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := &rag.Chunk{ID: "chunk", Source: "docs/postgres", Content: "PostgreSQL retrieval", Embedding: []float32{1, 0}, Metadata: map[string]any{"kind": "guide"}}
	record, err := VectorRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := TextDocument(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := text.Upsert(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	vr, err := NewServexVector(ServexOptions{Vectors: v, Embedder: fixtureEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewServexText(ServexOptions{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []rag.Retriever{vr, tr} {
		q := rag.Query{Text: "PostgreSQL", Limit: 2, Filter: rag.Filter{Sources: []string{"docs/postgres"}, Metadata: map[string]any{"kind": "guide"}}}
		hits, err := r.Retrieve(t.Context(), q)
		if err != nil || len(hits) != 1 || hits[0].Chunk.ID != c.ID || hits[0].Chunk.Content != c.Content {
			t.Fatalf("%+v: %v", hits, err)
		}
		q.Filter.Sources = []string{"private"}
		hits, err = r.Retrieve(t.Context(), q)
		if err != nil || len(hits) != 0 {
			t.Fatalf("source isolation: %+v: %v", hits, err)
		}
	}
	other, err := vectorpg.New(db, vectorpg.Config{Table: pg.Table{Schema: schema, Name: "vectors"}, Namespace: "other", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := NewServexVector(ServexOptions{Vectors: other, Embedder: fixtureEmbedder{}})
	if err != nil {
		t.Fatal(err)
	}
	if hits, err := isolated.Retrieve(t.Context(), rag.Query{Text: "PostgreSQL", Limit: 2}); err != nil || len(hits) != 0 {
		t.Fatalf("namespace isolation: %+v: %v", hits, err)
	}
}
