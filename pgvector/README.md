# pgvector

`github.com/nostosx/agentx-contrib/pgvector` persists agentx RAG chunks and
long-term memories in PostgreSQL with the `vector` extension. It implements:

- `rag.VectorStore` through `Vectors`;
- `memory.Store`, `memory.ScopedDeleter` and `memory.Retriever` through `Memory`.

Library code uses `database/sql` without importing a driver. The integration
tests use pgx. Requires Go 1.27.3, PostgreSQL 12 or later and pgvector 0.7 or
later. The backend was tested against PostgreSQL 17 with pgvector 0.8.7.

## Usage

Open the database with the application's chosen driver, configure its pool,
and retain ownership of its lifetime. Enable the extension separately with
`CREATE EXTENSION vector` before migrating. `Migrate` does not install an
extension or require extension-administration privileges.

```go
package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nostosx/agentx-contrib/pgvector"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/rag"
	"github.com/nostosx/agentx/rag/ragtest"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	// ragtest.Embedder is deterministic and needs no model service.
	// Replace it with the application's embedding model for real retrieval.
	embedder := ragtest.Embedder{}
	opts := []pgvector.Option{
		pgvector.WithNamespace("project-example"),
		pgvector.WithEmbedder(embedder),
		pgvector.WithDimensions(64),
	}
	if err := pgvector.Migrate(ctx, db, opts...); err != nil {
		log.Fatal(err)
	}
	store, err := pgvector.Memory(db, opts...)
	if err != nil {
		log.Fatal(err)
	}
	mem := memory.New(store, memory.WithEmbedder(embedder), memory.WithRetriever(store))
	if _, err := mem.Remember(ctx, "The user prefers Go", memory.WithScope("user-42")); err != nil {
		log.Fatal(err)
	}
	if hits, err := mem.Recall(ctx, "Go", memory.InScope("user-42")); err != nil {
		log.Fatal(err)
	} else {
		log.Printf("memory hits: %d", len(hits))
	}
	vectors, err := pgvector.Vectors(db, opts...)
	if err != nil {
		log.Fatal(err)
	}
	index := rag.NewIndex(rag.Config{Store: vectors, Embedder: embedder})
	_ = index // Compose with FSLoader, URLLoader or the application's Loader.
}
```

Use the same query embedding model and dimensions as those used for stored
embeddings. For asymmetric embedding models, inject the query embedder with
`pgvector.WithEmbedder` and the document embedder with `memory.WithEmbedder`.
`MemoryStore.Put` never calls a model automatically.

## Schema and isolation

`WithPrefix` changes the default `agentx_pg_` table prefix; `Migrate` creates
`sources`, `chunks` and `memories` tables beneath it. A store's own `Migrate`
creates only its tables. Migrations are idempotent for this initial schema;
run them from one process. They do not upgrade an incompatible existing
schema or change its configured dimensions.

Every read and write includes `WithNamespace` (default `default`). Different
namespaces can reuse the same source, chunk and memory IDs without sharing
records. Select namespaces from trusted application identity. They provide
logical isolation, not database access control. Memory scopes remain an
additional filter inside a namespace.

The schema belongs to this module. It does not share `sqlstore` tables and
does not convert or migrate existing `sqlstore` memories. Memory IDs, scopes
and namespaces use exact comparisons, including case and trailing spaces.
Source and chunk keys are hashed for fixed-size primary keys; their original
identifiers are retained in the stored documents.

Source replacement uses a transaction and locks the source row before
replacing chunks, so concurrent instances cannot produce a mixed source.
The whole `Replace` call commits or rolls back together. No `rag.Revisioner`
is exposed, since an in-process counter cannot represent external writers;
keyword retrievers rebuild from current data rather than trusting such a
counter. Constructors create no goroutines, start no services and do not
close the caller's `*sql.DB`.

## Search behavior

- Vector similarity is computed in PostgreSQL. RAG source names, literal
  source prefixes and metadata key existence are filtered in SQL. Metadata
  values are matched on decoded chunks with the core `rag.Filter.Match`,
  preserving its numeric, slice and nested-value semantics. This matters
  for integers beyond float64's exact range: PostgreSQL JSONB numeric
  equality is not equivalent to the core matcher. With metadata filters,
  search reads candidates in database score order and applies `limit`
  after matching, rather than limiting away valid results. Large candidate
  sets with metadata filters therefore require additional row transfer.
  Memory filters apply exact scope and all requested tags in SQL.
- Search is exact. Materialized candidates prevent externally-created ANN
  indexes from silently changing recall. This module creates no HNSW or
  IVFFlat index and does not expose an approximate-search option.
- RAG search returns all matching embedded chunks when `limit <= 0`.
  Memory retrieval returns only positive similarities meeting `MinScore`,
  like `memory.Vector`.
- Zero vectors and dimension mismatches have similarity zero. Missing
  embeddings are excluded. `WithDimensions(n)` optionally rejects
  non-empty vectors whose dimension differs; the default accepts mixed
  dimensions. Finite vectors up to 16000 dimensions are accepted.
- JSON text retains memory timestamps to nanoseconds and original float32
  values, including negative zero. JSONB is used separately for metadata
  and tag filtering. PostgreSQL JSONB restrictions apply to those fields,
  including rejection of escaped NUL. Text content may contain NUL because
  it stays inside escaped JSON text.
- RAG metadata round-trips through JSON: generic numbers decode as float64,
  slices as `[]any`, and objects as `map[string]any`. Metadata matching uses
  those returned values, so calling `Filter.Match` on a listed chunk agrees
  with applying that filter through `List` or `Search`.

## Tests

Unit tests require no external service. Use an owned disposable database
with the extension already enabled for integration tests:

```sh
export AGENTX_IT_PGVECTOR_DSN='postgres://postgres@127.0.0.1:5432/agentx_test?sslmode=disable'
GOWORK=off go test -race -tags integration -shuffle=on -count=1 ./...
```

Integration tests create and remove uniquely prefixed tables, run the
`rag/ragtest` and `memory/memorytest` conformance suites, and verify namespace
isolation, metadata/tag filtering, cancellation, transaction rollback and
concurrent replacement. Missing DSN makes direct Go tests skip; a skipped
test is not backend acceptance.

## License

[MIT](LICENSE).
