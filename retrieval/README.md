# Server-side retrieval

Qdrant and Elasticsearch implementations of `rag.Retriever`.
Applications own schema, collection/index lifecycle and vector-space compatibility.
Query adapters are read-only; the separate SourceWriter implementations add ingestion. They do not
implement `rag.VectorStore`, whose atomic source replacement requires a
transaction neither adapter supplies.

```go
r, err := retrieval.NewQdrant(retrieval.Options{
    Endpoint: "http://127.0.0.1:6333", Index: "knowledge",
    Namespace: tenantID, Embedder: embedder, APIKey: apiKey,
})
if err != nil { return err }
hits, err := r.Retrieve(ctx, rag.Query{Text: "question", Limit: 10})
```

Both options and namespace come from trusted application configuration, never
from model arguments. Namespace is enforced in the server query and checked
again in each result. A namespace is a query boundary, not a database ACL;
applications still provision credentials and index access. An injected HTTP
client supplies transport settings; redirects are disabled. Responses are
limited to 16 MiB, and transport/HTTP errors omit credential-bearing URLs and
server response bodies. No request is automatically retried.

## Index schemas and scores

Qdrant requires a collection with `Cosine` distance and the configured embedder's
dimension. Each point's payload has `namespace` and `chunk`, where `chunk` is
the JSON representation of `rag.Chunk`. For externally ingested points the application assigns IDs; SourceWriter assigns
stable namespace/source/chunk IDs. `VectorName` selects an optional named vector. Search uses
`POST /collections/{index}/points/query`. Exact source and scalar metadata
filters run on the server. `SourcePrefix` is explicitly unsupported, rather
than silently weakening filtering. Payload indexes improve filter performance.

Elasticsearch documents have `{namespace, chunk, embedding}`. Map `namespace`,
`chunk.source` and string metadata filter fields as `keyword`, `chunk.content`
as `text`; map `embedding` as `dense_vector` for vector mode. Without an
Embedder, `NewElasticsearch` performs BM25 search on `chunk.content`. With an
Embedder, it performs exact `script_score` cosine search over all eligible
documents; it converts Elasticsearch's `cosine + 1` score back to cosine.
This requires expensive queries to be enabled. It is not an ANN implementation.
BM25 and cosine scores have different meanings; use core `rag.Hybrid` for RRF
fusion rather than directly comparing them. Exact sources, prefixes and
scalar metadata filters are supported. Partial shard failures or timeouts fail
the query instead of returning an apparently complete result.

Metadata keys must be simple field names, and values scalar strings, booleans
or numbers. Arrays/objects are rejected. Limit must be positive and at most
MaxResults (default 100, configurable up to 10000); no unbounded scans or
silent truncation are promised. MinScore must be finite. Query embeddings must
contain one finite, nonzero vector. Result chunks require an ID and source.

## Acceptance

Unit tests use local HTTP fixtures. Real acceptance creates unique disposable
collections/indexes, verifies tenant isolation, filters and scores, then deletes
only those test resources with an independent cleanup context:

```sh
AGENTX_IT_QDRANT_URL=http://127.0.0.1:6333 \
AGENTX_IT_ELASTICSEARCH_URL=http://127.0.0.1:9200 \
task test INTEGRATION=true MODULES=retrieval
```

Use disposable services only. The Task gate requires both backends and rejects
skips. CI/release acceptance uses Qdrant 1.18.0 and Elasticsearch 9.2.0.
API references: [Qdrant Query](https://api.qdrant.tech/api-reference/search/query-points),
[Elasticsearch script score](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-script-score-query).

## Source writes and incremental ingestion

Requires agentx v1.4.0 and retrieval v1.1.0. Create a writer and retriever using
identical trusted endpoint/index/namespace/vector settings, then compose:

```go
writer, err := retrieval.NewQdrantWriter(retrieval.WriterOptions{
    Options: options, Dimensions: 1536, BatchSize: 100,
})
if err != nil { return err }
defer writer.Close()
retriever, err := retrieval.NewQdrant(options)
if err != nil { return err }
defer retriever.Close()
index := rag.NewIndex(rag.Config{
    Writer: writer, Retriever: retriever, Embedder: embedder,
})
_, err = index.Sync(ctx, completeSnapshot)
```

Use `NewElasticsearchWriter` for Elasticsearch. Store and Writer are mutually
exclusive. Writer without Retriever supports ingestion only. This is
`rag.SourceWriter`, not the atomic `rag.VectorStore` contract. Each source first
records a pending manifest with empty hash, deletes its previous chunks, writes
bounded batches, and commits its source hash/count only after every batch
succeeds. Failure may leave partial data visible; retry incomplete sources.
Pending manifests are returned by Sources with empty hash, so Add/Sync retries.
Reads exclude manifests, while accepting older externally ingested chunk records.

Successful earlier sources remain committed if a later source fails. One writer
serializes its operations; applications must coordinate independent writers and
avoid concurrent ingestion when listing sources. No distributed transaction or
cross-instance isolation is promised. Explicit Delete is idempotent and clears
both chunks and manifest. Sync requires an authoritative complete snapshot.
Use Index.ApplyChanges for a ChangeLoader delta; commit its cursor only on success.

Elasticsearch additionally requires `agentx_record` and `source_key` keyword
fields. `source_info` holds core SourceInfo. Its hash may be empty during retry.
Qdrant stores manifest points with a valid basis vector of the configured
Dimensions and optional VectorName. Both adapters namespace all source and chunk
identities. Source directories paginate completely and fail beyond MaxSources
(default 100000), on loops or malformed results. Elasticsearch HTTP-200 bulk
item failures, delete conflicts and incomplete shard results are errors.

Default HTTP clients reuse servex-contrib/transport/httpx with private-network
access allowed for these trusted service endpoints. Injected clients are borrowed;
Close releases only owned idle connections. Neither reader nor writer retries
requests automatically. Real acceptance covers Add/Sync/update/delete, manifest
exclusion and namespace isolation on both backends; failure/retry paths use HTTP
fixtures that interrupt a write after the server has partially changed state.

可写索引采用来源目录与失败重试语义，不承诺原子替换。`Sync` 仅接收完整快照，增量使用
`ApplyChanges`；失败后保留旧游标。多个写入实例对同一 namespace 的协调由应用负责。

## Servex vector and document retrieval (v1.2.0)

NewServexVector and NewServexText consume injected servex storage/vector and
storage/search contracts; no database backend is imported. Vector mode requires
cosine distance and an explicit query Embedder. Convert chunks with VectorRecord
or TextDocument before indexing; source plus chunk ID produce the stable object
ID and the payload contains the complete chunk. The store/index owns its trusted
tenant namespace. Existing pgvector Memory and VectorStore schemas/semantics
are unchanged.

Query Limit is positive and bounded by MaxResults (default 100, max 10000).
Supported filters are one exact source and string metadata equality. Multiple
sources, prefixes and non-string metadata are explicitly rejected. Result IDs
and payload filters are checked again; malformed/mismatched results fail the
query. Vector scores are 1-distance and drop nonpositive cosine similarity.
Text scores remain engine-specific. Use rag.Hybrid for rank fusion. ANN is
explicitly selected via Approximate, never enabled by an index alone.

Requires published Servex v1.5.0. Production code imports only the generic
contracts. Applications may inject servex-contrib/vector/pgvector and
servex-contrib/search/postgres v1.0.0; real PostgreSQL acceptance covers both,
including source/metadata filters, payload identity and namespace isolation.
Cancellation and nonfinite/zero embeddings fail before backend I/O.

This adapter is read-only and does not implement rag.SourceWriter or
rag.VectorStore. Applications own chunk ingestion and transactions. The
existing Qdrant/Elasticsearch writers remain independent.
