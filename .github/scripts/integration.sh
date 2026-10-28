#!/usr/bin/env bash
set -euo pipefail

module="${1:-sqlstore}"
case "$module" in
  sqlstore)
    : "${AGENTX_IT_POSTGRES_DSN:?PostgreSQL is required for integration acceptance}"
    : "${AGENTX_IT_MYSQL_DSN:?MySQL is required for integration acceptance}"
    pattern='.' ;;
  pgvector)
    : "${AGENTX_IT_PGVECTOR_DSN:?pgvector requires a disposable database DSN}"
    pattern='^TestIntegration' ;;
  document)
    : "${AGENTX_IT_PDFTOTEXT:?document requires an explicit pdftotext executable}"
    test -x "$AGENTX_IT_PDFTOTEXT"
    pattern='^TestIntegration' ;;
  *) echo 'Select sqlstore, pgvector or document for integration acceptance' >&2; exit 1 ;;
esac
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
result="$(mktemp)"
trap 'rm -f "$result"' EXIT
cd "$root/$module"
GOWORK=off "${GO:-go}" test -race -shuffle=on -count=1 -timeout=15m -tags integration -run "$pattern" -json ./... | tee "$result"
GOWORK=off "${GO:-go}" run "$root/.github/scripts/integrationcheck/main.go" "$result" "$module"
