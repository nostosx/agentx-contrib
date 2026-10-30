# Cached embeddings (unreleased)

`Cached` implements the existing `agentx/memory.Embedder` contract. It reuses
`servex/storage/cache.Cache` and the lifecycle-owned
`servex-contrib/cache/loader.Loader`; there is no agentx-specific Redis client,
TTL cache, codec backend or singleflight engine.

```go
cached, err := embedding.NewCached(owner, embedder, cacheBackend,
    embedding.CacheOptions{
        Namespace: "tenant-42/openai/text-embedding-3-small/document/1536/v1",
        TTL: time.Hour, LoadTimeout: 30 * time.Second,
        Dimensions: 1536, Codec: jsoncodec.Codec,
    })
if err != nil { return err }
defer cached.Close()
vectors, err := cached.Embed(ctx, []string{"first document", "second document"})
if err != nil { return err }
_ = vectors
```

This fragment imports this module as `embedding`, `time`, and
`servex-contrib/encoding/json` as `jsoncodec`. `embedder` is any core Embedder;
`cacheBackend` may be a servex-contrib memory or Redis cache. `owner` controls
shared-load lifetime and `ctx` controls this caller's wait. Close stops shared
loads and joins them, without closing the cache or model. Cache backends and
model callbacks must honor their contexts.

Namespace must contain trusted tenant/access scope and vector-space identity:
model/version, task, dimensions and any normalization options. Query and
document embedders may require distinct namespaces. Text is hashed into keys;
it is not stored as a key, but vectors are still potentially sensitive data.

Hits are reused per text. Duplicate misses are removed, unique misses are sent
to the model in one batch, and results return in original input order. Identical
concurrent miss batches share Loader work; partially overlapping batches need
not coalesce. A shared batch record and per-text records use the same TTL.
The app selects backend capacity/eviction. Returned vectors are copied, so
callers cannot mutate cached/shared values.

Cache read/write failures are best-effort misses; model failures, vector count
mismatches, non-finite values and inconsistent/configured dimensions are errors.
Failed model batches are never cached. Missing/corrupt per-text entries reload.
TTL <= 0 means no expiry, according to the servex Cache contract. LoadTimeout
defaults to 30 seconds. There is no automatic model retry; compose the existing
retry.Embedder deliberately if desired.

Use matching source workspaces/temporary modfiles during this unreleased
development stage. Public servex/cache foundations are versioned independently;
do not add local replacements to this module. Tests compose the real servex
memory cache, JSON codec and Loader, including reuse, namespace isolation,
batching, copy isolation, invalid vectors, cache failure and cancellation.
