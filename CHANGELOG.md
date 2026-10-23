# Changelog

Modules are versioned independently with tags of the form `<module>/vX.Y.Z`.
All five modules are preparing their first release as v1.0.0; none has been
published yet. Published v1 releases preserve public API compatibility.
All modules require Go 1.27.3 or later and resolve the published kitx v1.0.0;
the core dependency still uses a local replacement. See [RELEASING.md](RELEASING.md) for publication gates.

## Unreleased

## provider/v1.0.0

Prepared first release; not yet published.

- Add OpenAI Responses and Chat Completions, Anthropic, Gemini and Amazon
  Bedrock adapters, plus OpenAI and Gemini embedders. Adapters call the APIs
  over `net/http` and reuse kitx for data URLs and UTF-8 truncation.
- Restrict redirects to the original scheme, hostname and effective port,
  including requests made through custom HTTP clients.
- Test request and response handling against local API fixtures. Live provider
  compatibility has not been verified for the initial release.

## filestore/v1.0.0

Prepared first release; not yet published.

- Add JSON-backed sessions, run and workflow checkpoints, memory, vectors and
  MCP OAuth token storage. Reuse kitx atomic writes while preserving private
  file permissions and OAuth token permission compatibility.
- Reload memory under a process-shared OS lock on every operation, including
  scoped deletion. Report read errors instead of serving a stale snapshot;
  retain the `.lock` sidecar while stores are active.

## sqlstore/v1.0.0

Prepared first release; not yet published.

- Add sessions, run and workflow checkpoints and memory on SQLite, PostgreSQL
  and MySQL through `database/sql`, without importing a driver in production.
- Verify store contracts with the core conformance suites; use dedicated
  PostgreSQL and MySQL fixtures for service integration tests.

## middleware/v1.0.0

Prepared first release; not yet published.

- Add structured logging for model and tool calls, plus retries for model
  calls and embedders with retry classification, server delay hints and
  pre-output stream retries.

## otel/v1.0.0

Prepared first release; not yet published.

- Add OpenTelemetry tracing, metrics and opt-in GenAI content events for the
  core middleware interfaces.
