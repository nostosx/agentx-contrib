# Changelog

Modules are versioned independently with tags of the form `<module>/vX.Y.Z`.
The initial release of each module is v1.0.0. Patch releases preserve the
public API; minor releases may contain breaking changes, each marked with its
migration. Record subsequent changes under `Unreleased`, identifying the
module.

All modules require Go 1.27.3 or later and use published agentx and kitx
versions without replacements. See [RELEASING.md](RELEASING.md) for release gates.

## Unreleased

- Normalize CRLF output from Windows module discovery before validating Task
  selections; preserve rejection of empty, unknown and outside module paths.

- Development tooling: expose only check, test, build, lint, vulncheck, fmt,
  fix and tidy; make check the default and keep validation details internal.
  Remove redundant all/help/fix-check/clean task wrappers.

- Development tooling: consolidate unit, race, coverage, tooling regression and
  service tests under `task test`; select variants with `RACE`, `COVER` and
  `INTEGRATION`. `check` reuses the race variant. Live provider acceptance uses
  its explicit CLI instead of an additional Task command.

- Development tooling: replace Python integration validators and regression
  fixtures with Go standard-library tools; no Python runtime is required.

- Development tooling (all modules): replace Make entry points with Task v3.53.1 in local
  checks, CI and contribution/release instructions. Use `task check`, `task lint`
  and `task vulncheck`; check coverage, build tags and failure gates are preserved.

- Document standalone module installation and a runnable file-store quick start.
- Clarify filestore revision-check, snapshot and lock-file ownership boundaries
  when multiple instances or processes access the same paths.
- Correct sqlstore transaction guidance: store operations can execute multiple
  statements and join a transaction only when the caller provides one.

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
