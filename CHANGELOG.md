# Changelog

Modules are versioned independently with tags of the form `<module>/vX.Y.Z`.
The initial release of each module is v1.0.0. Patch releases preserve the
public API; minor releases may contain breaking changes, each marked with its
migration. Record subsequent changes under `Unreleased`, identifying the
module.

All modules require Go 1.27.3 or later. Released modules use published agentx
and kitx versions without replacements; unreleased capabilities may require
matching development sources. See [RELEASING.md](RELEASING.md) for release gates.

## Unreleased

## retrieval/v1.2.0

Release date: 2026-10-31. Requires Agentx v1.4.0 and Servex v1.5.0.

- Add read-only Servex vector/text retrievers and explicit chunk encoders, with
  exact source/string metadata filters, cosine score conversion and payload identity checks.
- Validate cancellation, finite nonzero query embeddings and backend result bounds.
- Gate release on real PostgreSQL vector/text retrieval and namespace isolation,
  alongside the existing Qdrant/Elasticsearch read/write acceptance.
- Preserve existing pgvector Memory/VectorStore schemas; ingestion transactions
  and backend selection remain owned by applications.

## loader/v1.0.2

Release date: 2026-10-31. Requires agentx v1.4.0 and httpx v1.1.0.

- Percent-encode GitHub file path segments in source links so files containing #, spaces or percent signs keep valid citations. Preserve repository/source identity and branch encoding.
- Verify dotted repositories, slash-containing refs and special file names together in the snapshot/delta regression.

## loader/v1.0.1

Release date: 2026-10-31. Requires agentx v1.4.0 and httpx v1.1.0.

- Accept valid GitHub repository names containing periods, including .github, while rejecting dot/dot-dot path segments and keeping owner validation separate.
- Cover repository identity in authenticated snapshot/delta HTTP fixtures and verify the public organization profile repository through the released consumer.

## web/v1.0.0

Release date: 2026-10-31. Requires agentx v1.4.0 and httpx v1.1.0.

- Add Readability/Markdown extraction, absolute source links, bounded HTTP/charset loading and an optional borrowed Servex cache Loader, with Agent tool and RAG snapshot examples.

## browser/v1.0.0

Release date: 2026-10-31. Requires agentx v1.4.0.

- Add isolated Chromium sessions with dynamic DOM/control snapshots, fill/click, PNG screenshots and bounded temporary downloads. Deny automatic downloads and close interrupted sessions.
- Add mandatory real-Chromium CI/release acceptance, separate from debug viewer browser tests.

## loader/v1.0.0

Release date: 2026-10-31. Requires agentx v1.4.0 and httpx v1.1.0.

- Add scoped GitHub, Notion and Feishu read-only connectors using Servex HTTP and injected Cache/Codec/token facilities. Fail incomplete snapshots and pagination/permission errors; expose expiring, scoped snapshot-comparison delta cursors with explicit deletions.
- Cover official API shapes with HTTP fixtures; no live cloud account acceptance is implied.

## retrieval/v1.1.0

Release date: 2026-10-31. Requires agentx v1.4.0 and httpx v1.1.0.

- Add Qdrant/Elasticsearch SourceWriters for Index.Add/Sync/ApplyChanges. Persist pending manifests before replacement and commit source hashes after all batches succeed; retain non-atomic semantics and retry incomplete sources.
- Exclude manifests from retrieval, namespace stable identities, paginate source directories, check every bulk item/deletion result and reuse Servex HTTP. Add failure/retry fixtures and mandatory real-backend write lifecycle acceptance.


## artifact/v1.0.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add scoped artifact.Store persistence over the existing servex S3 Store, bounded single-object envelopes, atomic replacement and standard error identity. Applications own SDK clients, authorization and lifetimes.
- Require published agentx v1.3.0 and servex-contrib/storage/s3 v1.0.0; use real public checksums without source replacements.

## debug/v1.0.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add a bounded telemetry collector and an explicitly authorized read-only trace/evaluation viewer. Preserve parent relationships, events and error types while omitting prompts and evaluation payloads.
- Compare exact case identities, reject oversized names, and require Chromium/WebKit browser acceptance before publication.

## document/v1.1.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add XLSX worksheet extraction with cached raw values and bounded ZIP/text/row/column resources.
- Add local Tesseract image OCR and pdftoppm-to-OCR PDF parsing with cancellation, page metadata, limits and private temporary cleanup.
- Pin verified Excelize security-fix commit c5fd73bde8f6 to resolve four reachable advisories. Require real PDF/OCR acceptance before publication.

## embedding/v1.0.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Cache vectors per text while retaining miss batching, duplicate order, namespace isolation and independent copies. Reuse servex Cache, contrib Loader and an explicitly injected Codec.
- Preserve owner cancellation/Close and valid model output when cache encoding fails.

## filestore/v1.1.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Implement core artifact.Store using the existing atomic JSON file engine and cross-process locks; preserve scoped keys, replacement, copies and persistence across reopen.

## middleware/v1.2.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add explicit model fallback targets with optional model-name overrides, retryable error classification, cancellation and telemetry.
- Preserve all error chains, classify the terminal target correctly and never switch after the first stream event.

## provider/v1.3.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add OpenAI independent image generation/editing/streaming and Gemini/Vertex generation/reference editing/streaming through core imagegen. Preserve authentication, usage, signatures, previews, final images and request IDs.
- Reject unsupported typed options, malformed or incomplete results and excessive payloads; do not automatically retry image requests.
- Add explicit providercheck image acceptance. Ordinary tests use local fixtures and make no paid requests.

## retrieval/v1.0.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Add read-only Qdrant and Elasticsearch BM25/cosine rag.Retriever adapters. Enforce trusted namespaces and supported filters server-side, validate returned scope and reject partial searches.
- Preserve cosine score semantics, bounded limits and application-owned indexing. Require real Qdrant/Elasticsearch acceptance without skips.

## search/v1.0.0

Release date: 2026-10-31. Requires agentx v1.3.0.

- Implement core search.Searcher with Tavily and Serper, explicit credentials, attributed URLs/snippets, bounded responses and cancellation.
- Refuse credential-bearing redirects and automatic paid retries; compose the core bounded Agent tool.

## provider/v1.2.0

Release date: 2026-10-28. Requires agentx v1.2.1.

- Add Vertex AI predict embeddings, Bedrock Titan embeddings and a Cohere
  reranker. Preserve ordinary constructor injection and local fixture tests.
- Add an explicit live acceptance command with bounded requests, required model
  selection and structured verified/unverified reports. Ordinary CI does not
  establish live model compatibility or make paid provider requests.

## sqlstore/v1.1.0

Release date: 2026-10-28. Requires agentx v1.2.1.

- Add encrypted MCP TokenStore with explicit user/trust-domain namespace,
  injected authenticated encryption and atomic credential replacement.
- Migrate creates the mcp_credentials table. OAuth refresh coordination remains
  application-owned; operations join a transaction only when one is supplied.
- Require all ten SQL conformance suite/backend combinations before publication.

## document/v1.0.0

Release date: 2026-10-28. Requires agentx v1.2.1.

- Add bounded DOCX parsing and explicit Poppler pdftotext-backed PDF parsing,
  with cancellation, source/page metadata and a caller-supplied OCR fallback.
- Keep textless pages visible and return ErrNoText for wholly textless inputs.
  A real converter integration gate is required before publication.

## pgvector/v1.0.0

Release date: 2026-10-28. Requires agentx v1.2.1.

- Add PostgreSQL RAG source replacement, namespace-filtered exact cosine search,
  memory storage and database-side retrieval. Own tables and migrations remain
  separate from other contrib modules.
- Require real PostgreSQL/vector-extension conformance before publication.

## s3/v1.0.0

Release date: 2026-10-28. Requires agentx v1.2.1.

- Add an AWS SDK v2 document loader with an injected client and parsers,
  pagination, conditional reads, bounded downloads and all-or-nothing failures.
- Tests use local fixtures; no live bucket or model access is required.

## provider/v1.1.0

Release date: 2026-10-27.

- Adopt agentx v1.1.0. Adapters read their own entry of
  `model.Request.ProviderOptions` (`"openai"`, `"anthropic"`, `"gemini"`,
  `"bedrock"`) instead of the removed `Extra`, so options for one provider no
  longer reach another. OpenAI Chat Completions sends `Request.Seed` as `seed`
  and Gemini as `generationConfig.seed`; the Responses API, Anthropic and
  Bedrock have no seed and ignore it. Reasoning effort and display use the
  typed `model.Effort` and `model.ReasoningDisplay`. Breaking: requires agentx
  v1.1.0; pass provider fields with `agent.WithProviderOptions`.

## otel/v1.1.0

Release date: 2026-10-27.

- Adopt agentx v1.1.0. `otel.Tracer` and `otel.Meter` return
  `telemetry.Tracer` and `telemetry.Meter`, whose interfaces moved from
  agentx's `middleware` package. Breaking: requires agentx v1.1.0.

## middleware/v1.1.0

Release date: 2026-10-27.

- Adopt agentx v1.1.0. Retry records its `agentx.retry` events
  through the `telemetry` package. Breaking: requires agentx v1.1.0.

## provider/v1.0.0

Release date: 2026-10-23.

- Add OpenAI Responses and Chat Completions, Anthropic, Gemini and Amazon
  Bedrock adapters, plus OpenAI and Gemini embedders. Adapters call the APIs
  over `net/http` and reuse kitx for data URLs and UTF-8 truncation.
- Restrict redirects to the original scheme, hostname and effective port,
  including requests made through custom HTTP clients.
- Test request and response handling against local API fixtures. Live provider
  compatibility has not been verified for the initial release.

## filestore/v1.0.0

Release date: 2026-10-23.

- Add JSON-backed sessions, run and workflow checkpoints, memory, vectors and
  MCP OAuth token storage. Reuse kitx atomic writes while preserving private
  file permissions and OAuth token permission compatibility.
- Reload memory under a process-shared OS lock on every operation, including
  scoped deletion. Report read errors instead of serving a stale snapshot;
  retain the `.lock` sidecar while stores are active.
- Coordinate session and checkpoint file reads, replacements and deletes
  through persistent OS lock sidecars, including across store instances.
  Honor cancellation while waiting for a file lock and prevent Windows
  sharing violations during concurrent checkpoint access. Session revision
  checks remain serialized only within one store instance.

## sqlstore/v1.0.0

Release date: 2026-10-23.

- Add sessions, run and workflow checkpoints and memory on SQLite, PostgreSQL
  and MySQL through `database/sql`, without importing a driver in production.
- Verify store contracts with the core conformance suites; use dedicated
  PostgreSQL and MySQL fixtures for service integration tests.

## middleware/v1.0.0

Release date: 2026-10-23.

- Add structured logging for model and tool calls, plus retries for model
  calls and embedders with retry classification, server delay hints and
  pre-output stream retries.

## otel/v1.0.0

Release date: 2026-10-23.

- Add OpenTelemetry tracing, metrics and opt-in GenAI content events for the
  core middleware interfaces.
