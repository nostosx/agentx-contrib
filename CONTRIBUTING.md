# Contributing to agentx-contrib

Thanks for helping. The
[agentx contributing guide](https://github.com/nostosx/agentx/blob/main/CONTRIBUTING.md)
applies here too: open an issue before a new feature or API change, keep
pull requests to one change with tests, and report security issues
privately (see [SECURITY.md](SECURITY.md)).

Install [Task](https://taskfile.dev/docs/installation/) v3.53.1 outside the module,
matching CI, and put its binary on `PATH`:

```sh
go install github.com/go-task/task/v3/cmd/task@v3.53.1
```

Public tasks are `check`, `test`, `build`, `lint`, `vulncheck`, `fmt`, `fix`
and `tidy`. Bare `task` runs `check`. Validation helpers are internal; use
`task check` to run them. Run `task --list` to discover the public tasks. Tool overrides accept environment variables or
Task arguments, for example `task check GO="/path with spaces/go"`.
Checks force `GOWORK=off`; `check`, `lint` and `vulncheck` do not apply source fixes.

## Modules

Each top-level directory is a module, `github.com/nostosx/agentx-contrib/<dir>`.
Modules never import each other. Runnable applications live in the separate
[agentx-examples](https://github.com/nostosx/agentx-examples) project, which
can compose the core and multiple contrib modules. A new library module:

- implements agentx contracts with the public API of agentx only; helpers
  go in the module's own `internal/` directory;
- passes the conformance suite of each store contract it implements
  (`session/sessiontest`, `loop/checkpointtest`, `workflow/checkpointtest`,
  `memory/memorytest`, `rag/ragtest`);
- puts tests that need a server in `*_integration_test.go` files with
  `//go:build integration`, reading the address from an `AGENTX_IT_*`
  variable and skipping when it is unset;
- is added to the module list of the CI and Release workflows and to both
  READMEs.

Foundation adapters may explicitly depend on servex contracts and shared
servex-contrib implementations. Keep that graph directed: artifact/S3 reuses
`servex-contrib/storage/s3`; embedding cache reuses `servex/storage/cache`,
`servex-contrib/cache/loader` and an injected codec. Applications select Redis
or memory caches, SDK credentials, logging and telemetry providers. Do not
reimplement these facilities in agentx, import agentx-contrib from servex,
or couple independent agentx-contrib modules to each other.

Artifact/image/search contracts are published in agentx v1.3.0. Artifact storage
uses the independently published servex S3 v1.0.0 foundation. Release consumers
must resolve public dependencies without source replacements. External temporary
modfiles/workspaces remain an explicit development option for future unreleased
contracts, never publication evidence.

## Development

The pgvector backend has a separate real-service gate:
`AGENTX_IT_PGVECTOR_DSN=... task test INTEGRATION=true MODULES=pgvector`. Enable the vector
extension in a disposable database first. For the document module use
`AGENTX_IT_PDFTOTEXT=/absolute/path/to/pdftotext task test INTEGRATION=true MODULES=document`.
The complete document gate also requires `AGENTX_IT_PDFTOPPM` and
`AGENTX_IT_TESSERACT`, with all three tools executable. Retrieval acceptance
requires disposable endpoints `AGENTX_IT_QDRANT_URL` and
`AGENTX_IT_ELASTICSEARCH_URL`, then `task test INTEGRATION=true MODULES=retrieval`.
All reject missing configuration and skipped tests. Provider acceptance is
separate from debug browser acceptance, which uses explicit `AGENTX_IT_NODE`
and `AGENTX_IT_PLAYWRIGHT` paths and both Chromium/WebKit engines through
`task test INTEGRATION=true MODULES=debug`. Provider live acceptance is
explicitly opt-in: `go run ./cmd/providercheck -live -provider=... -model=...` from provider/.
Ordinary tests and CI do not enable live provider calls.

Use Go 1.27.3, Task v3.53.1 and Bash. Released modules resolve their published
agentx and kitx requirements through the Go module proxy; unreleased APIs need
matching development sources through the external procedure above. Run Go
commands inside a module directory with `GOWORK=off`, or use the `Taskfile.yml`, which
does so for every module. For linting, install golangci-lint v2.15.0 built
with Go 1.27.3. Install both check tools outside the modules and put the Go
bin directory on `PATH`:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
```

Do not add these tools to library `go.mod` files. `task --list` lists all targets.

```sh
task check        # format, modernizer and tidy checks, vet, build, shuffled race tests
task lint         # golangci-lint, including integration builds
task vulncheck    # reachable vulnerability scans, including integration builds
task test         # module tests and tooling regression tests
task test RACE=true  # include the race detector
task test COVER=true  # race tests plus per-module coverage.out
task test INTEGRATION=true MODULES=sqlstore  # requires PostgreSQL and MySQL DSNs
task check MODULES=provider  # select one module, or a quoted space-separated list
```

`task check` rejects formatting, modernizer and tidy differences without
applying them. Use `task fmt`, `task fix` or `task tidy` explicitly, then
review the changes. Modernizer, vet, build, lint and vulnerability checks
cover both default and integration builds. Unit race tests do not require
external services. `task test` also runs integration-gate regression
fixtures; `task check` reuses `task test RACE=true`.
The validators and tooling regressions use only the Go standard library.
`GO`, `GOFMT`, `GOLANGCI_LINT` and `GOVULNCHECK` can select installed executables.
Direct tagged Go tests may skip a backend whose DSN is unset. The
`task test INTEGRATION=true MODULES=sqlstore` gate requires both DSNs and rejects skipped or missing
conformance suites; a skipped backend is not a successful acceptance run.

CI runs `task check` per module on Linux, macOS and Windows, with independent
lint and vulnerability jobs and a separate PostgreSQL/MySQL integration job.
Run `task check`, `task lint` and `task vulncheck` before requesting review.
For service-dependent changes, run the applicable integration tests against
disposable databases as described in the README. Never point them at shared
or production data.

Add a line to the "Unreleased" section of [CHANGELOG.md](CHANGELOG.md),
naming the module, for changes users notice.

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).
