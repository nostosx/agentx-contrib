# S3 document loader

This module implements `rag.Loader` using an injected AWS SDK for
Go v2 S3 client. It lists a bucket prefix with pagination, downloads supported
objects, and passes their bytes to the existing `rag.Parser` contract. It does
not load credentials, create a bucket, upload objects or install a parser.

```go
loader := &s3loader.Loader{
    Client: client, // an application-configured *s3.Client from AWS SDK v2
    Bucket: "knowledge",
    Prefix: "manuals/",
    MaxObjectSize: 10 << 20,
    MaxTotalSize: 64 << 20,
    MaxObjects: 1000,
}
docs, err := loader.Load(ctx)
```

Import this package as `s3loader`. The application configures its SDK client
with credentials, region, TLS, retry policy and, for S3-compatible backends,
the endpoint and path-style options. The same loader works with those clients;
compatibility with each backend still requires integration verification.

`Parsers` defaults to `rag.DefaultParsers()`. To ingest PDF or DOCX, copy that
map and register parsers from the separate `document` module in the application.
Unsupported extensions and directory markers are skipped. Sources are stable
`s3://bucket/key` URLs, optionally prefixed by `SourcePrefix`. Documents carry
`s3_bucket`, `s3_key` and `s3_etag` metadata.

Any read, listing, parsing or resource-limit error returns no documents. This
prevents an incomplete load from making `rag.Index.Sync` remove missing sources.
ETags from listing are sent as `If-Match` on reads. An S3 listing across pages
is not an atomic snapshot; subsequent syncs must reconcile concurrent changes.
Repeated pagination tokens and duplicate keys fail rather than returning an
ambiguous snapshot. Downloaded bodies are closed, including failure paths.

The defaults are 10 MiB per object, 64 MiB downloaded total, and 1000 listed
objects (including skipped extensions). Exceeding a limit fails the load.
Use dedicated prefixes and explicit caller deadlines for larger collections.

## Verification

From the repository root, run `MODULES=s3 task check lint vulncheck`.
Tests include pagination, limits, cancellation, failure atomicity and a real
AWS SDK client against a local S3 HTTP protocol fixture. They never access
cloud credentials or a remote bucket.

Licensed under MIT. Go 1.27.3 or later is required.
