#!/usr/bin/env bash
set -euo pipefail

# Both backends are mandatory; a skipped backend is not successful acceptance.
: "${AGENTX_IT_POSTGRES_DSN:?PostgreSQL is required for integration acceptance}"
: "${AGENTX_IT_MYSQL_DSN:?MySQL is required for integration acceptance}"
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
result="$(mktemp)"
trap 'rm -f "$result"' EXIT
cd "$root/sqlstore"
GOWORK=off "${GO:-go}" test -race -shuffle=on -count=1 -timeout=15m -tags integration -json ./... | tee "$result"
"${PYTHON:-python3}" - "$result" <<'PYTHON'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    events = [json.loads(line) for line in stream if line.strip()]
if any(event.get("Action") in {"skip", "fail"} for event in events):
    sys.exit("Integration acceptance contains skipped or failed tests")
started = {(event.get("Package"), event["Test"]) for event in events
           if event.get("Action") == "run" and "Test" in event}
finished = {(event.get("Package"), event["Test"]) for event in events
            if event.get("Action") == "pass" and "Test" in event}
if started - finished:
    sys.exit("Integration acceptance contains unfinished tests")
passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
for suite in ("TestSessionStoreConformance", "TestCheckpointStoreConformance",
              "TestWorkflowCheckpointStoreConformance", "TestMemoryStoreConformance"):
    for backend in ("postgresql", "mysql"):
        name = f"{suite}/{backend}"
        if name not in passed:
            sys.exit(f"Missing successful integration suite: {name}")
print("Integration acceptance: all eight SQL suites passed, no skips")
PYTHON
