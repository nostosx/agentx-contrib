#!/bin/sh
# Point every module at a released core, dropping its sibling replacement.
# Run after the core is available from the Go module proxy and before contrib
# releases; see RELEASING.md.
#
#	scripts/use-core-version.sh v1.0.0
set -eu

if [ "$#" -ne 1 ]; then
	printf '%s\n' 'usage: use-core-version.sh core-version' >&2
	exit 1
fi
core=$1
# These module paths have no /vN suffix, so only major versions 0 and 1 fit.
prerelease='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
version_re="^v(0|1)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-$prerelease(\\.$prerelease)*)?$"
check_version() {
	if ! printf '%s\n' "$1" | grep -Eq "$version_re" ||
		printf '%s\n' "$1" | grep -Eq -- '[.-][0-9]{14}-[0-9a-f]{12}$'; then
		printf 'expected a release version, got %s\n' "$1" >&2
		exit 1
	fi
}
check_version "$core"

cd "$(dirname "$0")/.."
export GOWORK=off
core_path=github.com/nostosx/agentx
# Resolve the requested release before changing any go.mod. An unavailable
# core must not remove local replacements.
go mod download "$core_path@$core"

for mod in */go.mod; do
	dir=${mod%/go.mod}
	printf '== %s\n' "$dir"
	(
		cd "$dir"
		go mod edit -dropreplace="$core_path" -require="$core_path@$core"

		go mod tidy
		resolved=$(go list -m -f '{{.Version}}' "$core_path")
		if [ "$resolved" != "$core" ]; then
			printf '%s requires core %s after tidy, expected %s\n' "$dir" "$resolved" "$core" >&2
			exit 1
		fi
	)
done
