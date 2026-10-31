# Releasing

Each library module has its own version and tag, `<module>/vA.B.C`.
Go 1.27.3 is the toolchain requirement, independent of module versions.
Module-specific release notes live in `CHANGELOG.md`. The initial version
of each module is v1.0.0. Record subsequent changes under `Unreleased`,
identifying the module.
Runnable programs are maintained in agentx-examples.
The library modules require a released agentx, so release
[agentx first](https://github.com/nostosx/agentx/blob/main/RELEASING.md) and wait
until that version is available from the Go module proxy.

## Prepare the modules

Artifact requires agentx v1.3.0 and servex-contrib/storage/s3 v1.0.0.
Verify every nostosx dependency is published before releasing an adapter;
development placeholders and local replacements are not publishable.
Embedding uses independently released servex/cache/codec foundations.
Search requires agentx v1.3.0 or later. Debug and retrieval
reuse existing core contracts. Document pins an actual public Excelize
security-fix pseudo-version until a fixed stable tag is published; the release
ban on development pseudo-versions applies to nostosx dependencies, not this
verified upstream security pin.
External workspaces and temporary modfiles are development tools, not release
acceptance. No core or foundation repository may depend back on these adapters.

1. Start from a clean checkout on `main`, with Go 1.27.3 or later. When
   upgrading the core, run `scripts/use-core-version.sh <released-version>`
   after the chosen agentx version is available from the public Go proxy. The
   script downloads requested releases before editing any module, removes
   the core replacements, updates the requirements and tidies each module.
   It rejects placeholder and pseudo-versions, invalid versions and major
   versions that need a `/vN` module path. Review all `go.mod` and `go.sum`
   changes; a later tidy or version conflict can still leave partial edits.
2. Review the selected module's notes and release date in `CHANGELOG.md`.
   Move its entries from `Unreleased` into the matching
   `## <module>/vA.B.C` section.
   Keep a copy of the root MIT `LICENSE` in each module directory, so each
   module contains its own license when archived. Go's
   VCS downloader can copy a root license into a nested module zip, but
   explicit module licenses also cover archives made directly from a module
   directory. The release workflow checks that the selected module's license
   matches the root license.
3. Run `task check`, `task lint` and `task vulncheck`. Use `MODULES=sqlstore`
   (or another module name) to limit the module gates to the selected release.
   For SQL storage, start disposable
   PostgreSQL 17 and MySQL 8.4 databases, set both `AGENTX_IT_POSTGRES_DSN`
   and `AGENTX_IT_MYSQL_DSN` as described in the README, and run
   `task test INTEGRATION=true MODULES=sqlstore`. This gate requires both DSNs and verifies all ten
   suite/backend combinations without skips; directly running tagged Go tests
   can skip an unset backend and is not equivalent acceptance.
   Document releases require executable `AGENTX_IT_PDFTOTEXT`,
   `AGENTX_IT_PDFTOPPM` and `AGENTX_IT_TESSERACT` and the complete document gate.
   Retrieval releases require both disposable `AGENTX_IT_QDRANT_URL` and
   `AGENTX_IT_ELASTICSEARCH_URL` and its real-backend Task gate. Their workflow
   jobs run before publication and reject skipped suites.
   Debug releases also require the committed Chromium/WebKit browser gate,
   with explicit Node/Playwright paths; the reusable workflow installs test
   tools outside the library module and uses an isolated HTTP fixture.
4. Review and merge the changes before starting **Release** from `main`.
   Do not substitute a sibling checkout for the released core during
   release acceptance.

## Shared kitx dependency

All modules resolve the published kitx v1.0.0 without local replacements.
Provider and filestore import kitx directly; the other modules use it through
agentx. Keep these requirements aligned when adopting another kitx release,
then tidy and run the module gates. The `use-core-version.sh` script updates
agentx only and preserves the selected kitx dependency.
The release workflow rejects remaining replacements and requires both agentx and
kitx to have explicit non-pseudo release versions. Release verification must work
without sibling checkouts.

## Release each library

Run the **Release** workflow once per module, choosing the module and version.
It validates the changelog, module license and versions; rejects replacements
and unreleased core/kitx requirements; and runs the same formatting, tidy, vet, modernizer,
build, shuffled race-test, lint and vulnerability gates as CI. The selected
module passes `task check` on Linux, macOS and Windows. Modernizer, vet,
build, lint and vulnerability checks cover integration builds too.
Modules without service fixtures also run race tests with integration tags.
pgvector requires its own real-backend gate, described below.

For `sqlstore`, a separate required job starts real PostgreSQL 17 and MySQL
8.4 services and runs `go test -count=1 -race -shuffle=on -tags integration -json ./...`.
Both DSNs are mandatory. The job rejects skipped tests and requires passing
session, loop-checkpoint, workflow-checkpoint, memory and MCP token suites on
both servers. Failed or skipped SQL acceptance prevents publication. Other
modules do not need those services. For pgvector, a required job enables the
vector extension in a disposable PostgreSQL service and runs
`task test INTEGRATION=true MODULES=pgvector`, rejecting missing, failed or skipped suites.
The pgvector, document and s3 modules have independent versions; each release
must pass its applicable checks before publication.

For document, another required job installs Poppler in its disposable runner
and executes `task test INTEGRATION=true MODULES=document` with an explicit pdftotext
path. A skipped converter test does not permit publication. Live provider
acceptance is an independent explicit operation; local fixture success does
not establish compatibility with a particular live model.

Only after every applicable job passes does the publishing job create
`<module>/vA.B.C` at the checked commit and warm the public Go module proxy.
After publication, verify installation from a clean temporary module with
`GOWORK=off` and no replacements, using the published module version.
Verification jobs have read-only repository permissions; only publishing has
`contents: write`. Releases for the same module are serialized.

Web/browser/loader v1.0.0 and retrieval v1.1.0 require published agentx v1.4.0.
Web, loader and retrieval also require httpx v1.1.0; publish and independently
verify both foundations before selecting the contrib releases. Browser has a
required real-Chromium job using AGENTX_IT_BROWSER_BINARY. Retrieval acceptance
requires its new SourceWriter lifecycle suites on both real backends. Ordinary
connector tests verify official API shapes and failure semantics with fixtures;
they do not establish live cloud-account permissions.

Never push release tags by hand: a version the Go module proxy has seen cannot
be withdrawn. A failure while warming the proxy happens after publication;
inspect the existing tag and release before taking further action.

## Update examples

Update the independent [agentx-examples](https://github.com/nostosx/agentx-examples)
project after the core, provider and filestore versions are available. Its README
records the selected published dependencies and standalone setup.
The contrib version-switching script only updates this repository's libraries.

## Servex retrieval v1.2.0

Select Servex v1.5.0 and database/postgres, vector/pgvector and search/postgres
v1.0.0 before publishing retrieval. The PostgreSQL dependency imports are test-only;
production adapters consume generic contracts. Release requires the same real
PostgreSQL acceptance as ordinary CI in addition to Qdrant/Elasticsearch writers.
The integration gate requires AGENTX_IT_SERVEX_POSTGRES_DSN and rejects skipped suites.
