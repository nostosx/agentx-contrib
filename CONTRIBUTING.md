# Contributing to agentx-contrib

Thanks for helping. The
[agentx contributing guide](https://github.com/nostosx/agentx/blob/main/CONTRIBUTING.md)
applies here too: open an issue before a new feature or API change, keep
pull requests to one change with tests, and report security issues
privately (see [SECURITY.md](SECURITY.md)).

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

## Development

Use Go 1.27.3, GNU Make, Bash and Python 3. A standalone checkout resolves
agentx and kitx v1.0.0 through the Go module proxy. Run Go
commands inside a module directory with `GOWORK=off`, or use the `Makefile`, which
does so for every module. For linting, install golangci-lint v2.15.0 built
with Go 1.27.3. Install both check tools outside the modules and put the Go
bin directory on `PATH`:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
```

Do not add these tools to library `go.mod` files. `make help` lists all targets.

```sh
make check        # format, modernizer and tidy checks, vet, build, shuffled race tests
make lint         # golangci-lint, including integration builds
make vulncheck    # reachable vulnerability scans, including integration builds
make test         # unit tests without the race detector
make race         # race tests with shuffled execution order
make integration  # sqlstore acceptance; requires both PostgreSQL and MySQL DSNs
make check MODULES=provider  # select one module, or a quoted space-separated list
```

`make check` rejects formatting, modernizer and tidy differences without
applying them. Use `make fmt`, `make fix` or `make tidy` explicitly, then
review the changes. Modernizer, vet, build, lint and vulnerability checks
cover both default and integration builds. Unit race tests do not require
external services. `make tooling-check` runs integration-gate regression
fixtures without external services and is included in `make check`.
`GO`, `GOFMT`, `GOLANGCI_LINT` and `GOVULNCHECK` can select installed executables.
Direct tagged Go tests may skip a backend whose DSN is unset. The
`make integration` gate requires both DSNs and rejects skipped or missing
conformance suites; a skipped backend is not a successful acceptance run.

CI runs `make check` per module on Linux, macOS and Windows, with independent
lint and vulnerability jobs and a separate PostgreSQL/MySQL integration job.
Run `make check`, `make lint` and `make vulncheck` before requesting review.
For service-dependent changes, run the applicable integration tests against
disposable databases as described in the README. Never point them at shared
or production data.

Add a line to the "Unreleased" section of [CHANGELOG.md](CHANGELOG.md),
naming the module, for changes users notice.

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).
