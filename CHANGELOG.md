# Changelog

Modules are versioned independently with tags of the form `<module>/vX.Y.Z`.
The initial release of each module is v1.0.0. Published v1 releases preserve
public API compatibility. Record subsequent changes under `Unreleased`,
identifying the module.

All modules require Go 1.27.3 or later and use published agentx and kitx
v1.0.0 without replacements. See [RELEASING.md](RELEASING.md) for release gates.

## Unreleased

- Document standalone module installation and a runnable file-store quick start.
- Clarify filestore revision-check, snapshot and lock-file ownership boundaries
  when multiple instances or processes access the same paths.
- Correct sqlstore transaction guidance: store operations can execute multiple
  statements and join a transaction only when the caller provides one.

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
