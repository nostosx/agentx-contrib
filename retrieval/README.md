# Server-side retrieval

Unreleased Qdrant and Elasticsearch implementations of `rag.Retriever`.
These are read-only query adapters: applications own schema, ingestion,
collection/index lifecycle and vector-space compatibility. They do not
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
the JSON representation of `rag.Chunk`. The point ID is independently assigned
by the application. `VectorName` selects an optional named vector. Search uses
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
