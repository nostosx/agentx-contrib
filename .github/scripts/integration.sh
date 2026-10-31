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
    : "${AGENTX_IT_TESSERACT:?document requires an explicit tesseract executable}"
    : "${AGENTX_IT_PDFTOPPM:?document requires an explicit pdftoppm executable}"
    test -x "$AGENTX_IT_PDFTOTEXT"
    test -x "$AGENTX_IT_TESSERACT"
    test -x "$AGENTX_IT_PDFTOPPM"
    pattern='^TestIntegration' ;;
  retrieval)
    : "${AGENTX_IT_QDRANT_URL:?retrieval requires a disposable Qdrant endpoint}"
    : "${AGENTX_IT_ELASTICSEARCH_URL:?retrieval requires a disposable Elasticsearch endpoint}"
    pattern='^TestIntegration' ;;
  browser)
    : "${AGENTX_IT_BROWSER_BINARY:?browser requires an explicit Chromium executable}"
    test -x "$AGENTX_IT_BROWSER_BINARY"
    pattern='^TestIntegrationChromium$' ;;
  debug)
    : "${AGENTX_IT_NODE:?debug requires an explicit Node executable}"
    : "${AGENTX_IT_PLAYWRIGHT:?debug requires the installed Playwright module entry}"
    test -x "$AGENTX_IT_NODE"
    test -f "$AGENTX_IT_PLAYWRIGHT"
    pattern='^TestIntegrationBrowser$' ;;
  *) echo 'Select sqlstore, pgvector, document, retrieval, debug or browser for integration acceptance' >&2; exit 1 ;;
esac
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
result="$(mktemp)"
trap 'rm -f "$result"' EXIT
cd "$root/$module"
GOWORK=off "${GO:-go}" test -race -shuffle=on -count=1 -timeout=15m -tags integration -run "$pattern" -json ./... | tee "$result"
GOWORK=off "${GO:-go}" run "$root/.github/scripts/integrationcheck/main.go" "$result" "$module"
