# Releasing

Each library module has its own version and tag, `<module>/vA.B.C`.
Go 1.27.3 is the toolchain requirement, independent of module versions.
All five modules are preparing their first release as v1.0.0. Their
module-specific notes are prepared in `CHANGELOG.md`; no module is published
yet. Record subsequent changes under `Unreleased`, identifying the module.
Runnable programs are maintained in agentx-examples.
The library modules require a released agentx, so release
[agentx first](https://github.com/nostosx/agentx/blob/main/RELEASING.md) and wait
until that version is available from the Go module proxy.

## Prepare the modules

1. Start from a clean checkout on `main`, with Go 1.27.3 or later. Run
   `scripts/use-core-version.sh v1.0.0` after agentx v1.0.0 is published. The
   script downloads requested releases before editing any module, removes
   the core replacements, updates the requirements and tidies each module.
   It rejects placeholder and pseudo-versions, invalid versions and major
   versions that need a `/vN` module path. Review all `go.mod` and `go.sum`
   changes; a later tidy or version conflict can still leave partial edits.
2. Review the selected module's prepared `## <module>/v1.0.0` notes in
   `CHANGELOG.md` and add its release date when publishing. For later
   releases, move the module's entries from `Unreleased` into the matching
   `## <module>/vA.B.C` section.
   Keep a copy of the root MIT `LICENSE` in each module directory, so each
   module contains its own license when archived. Go's
   VCS downloader can copy a root license into a nested module zip, but
   explicit module licenses also cover archives made directly from a module
   directory. The release workflow checks that the selected module's license
   matches the root license.
3. Run `make check`, `make lint` and `make vulncheck`. Use `MODULES=sqlstore`
   (or another module name) to limit the module gates to the selected release.
   For SQL storage, start disposable
   PostgreSQL 17 and MySQL 8.4 databases, set both `AGENTX_IT_POSTGRES_DSN`
   and `AGENTX_IT_MYSQL_DSN` as described in the README, and run
   `make integration`. This gate requires both DSNs and verifies all eight
   suite/backend combinations without skips; directly running tagged Go tests
   can skip an unset backend and is not equivalent acceptance.
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
module passes `make check` on Linux, macOS and Windows. Modernizer, vet,
build, lint and vulnerability checks cover integration builds too.
Non-SQL modules also run race tests with `-tags integration`.

For `sqlstore`, a separate required job starts real PostgreSQL 17 and MySQL
8.4 services and runs `go test -count=1 -race -shuffle=on -tags integration -json ./...`.
Both DSNs are mandatory. The job rejects skipped tests and requires passing
session, loop-checkpoint, workflow-checkpoint and memory conformance suites on
both servers. Failed or skipped SQL acceptance prevents publication. Other
modules do not need those services.

Only after every applicable job passes does the publishing job create
`<module>/vA.B.C` at the checked commit and warm the public Go module proxy.
After publication, verify installation from a clean temporary module with
`GOWORK=off` and no replacements, using the published module version.
Verification jobs have read-only repository permissions; only publishing has
`contents: write`. Releases for the same module are serialized.

Never push release tags by hand: a version the Go module proxy has seen cannot
be withdrawn. A failure while warming the proxy happens after publication;
inspect the existing tag and release before taking further action.

## Update examples

Update the independent [agentx-examples](https://github.com/nostosx/agentx-examples)
project after the core, provider and filestore versions are available. Its README
explains how to replace local dependencies with independently released versions.
The contrib version-switching script only updates this repository's libraries.
