#!/usr/bin/env python3
"""Exercise the integration gate with synthetic Go events, without servers."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


CONFIG = {"AGENTX_IT_POSTGRES_DSN": "fixture-postgres", "AGENTX_IT_MYSQL_DSN": "fixture-mysql"}
ARGS = []
TEST_NAME = "TestSessionStoreConformance/postgresql"


def bash_executable():
    if os.name != "nt":
        return "bash"
    # System32's bash starts WSL; these fixtures require Git for Windows.
    git = shutil.which("git")
    if git:
        for parent in Path(git).resolve().parents:
            candidate = parent / "bin/bash.exe"
            if candidate.is_file():
                return str(candidate)
    raise RuntimeError("Git Bash is required to test the integration gate")


def valid_events():
    return [dict(Action=action, Test=f"{suite}/{backend}")
            for suite in ("TestSessionStoreConformance", "TestCheckpointStoreConformance",
                          "TestWorkflowCheckpointStoreConformance", "TestMemoryStoreConformance")
            for backend in ("postgresql", "mysql") for action in ("run", "pass")]


class IntegrationGateTest(unittest.TestCase):
    def run_gate(self, events, *, exit_code=0, configured=True):
        with tempfile.TemporaryDirectory(prefix="integration gate ") as directory:
            fake = Path(directory) / "fake-go"
            fake.write_text('#!/bin/bash\nprintf \'%s\\n\' "$TEST_EVENTS"\nexit "$TEST_EXIT"\n', newline="\n")
            fake.chmod(0o700)
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith(("AGENTX_IT_", "SERVEX_IT_"))}
            env.update(GO=fake.as_posix(), PYTHON=Path(os.sys.executable).as_posix(),
                       TEST_EVENTS="\n".join(json.dumps(event) for event in events),
                       TEST_EXIT=str(exit_code))
            if configured:
                env.update(CONFIG)
            return subprocess.run(
                [bash_executable(), (ROOT / ".github/scripts/integration.sh").as_posix(), *ARGS],
                env=env, text=True, capture_output=True, check=False, timeout=20,
            )

    def assert_rejected(self, events, **kwargs):
        result = self.run_gate(events, **kwargs)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_complete_results_pass(self):
        result = self.run_gate(valid_events())
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_missing_environment_fails(self):
        self.assert_rejected(valid_events(), configured=False)

    def test_empty_results_fail(self):
        self.assert_rejected([])

    def test_skip_fails_even_with_other_successes(self):
        self.assert_rejected(valid_events() + [dict(Action="skip", Test=TEST_NAME)])

    def test_test_failure_is_not_hidden_by_pipeline(self):
        self.assert_rejected(valid_events(), exit_code=7)

    def test_failed_event_fails(self):
        self.assert_rejected(valid_events() + [dict(Action="fail", Test=TEST_NAME)])

    def test_unfinished_test_fails(self):
        self.assert_rejected(valid_events() + [dict(Action="run", Test=TEST_NAME + "/unfinished")])

    def test_missing_backend_suite_fails(self):
        self.assert_rejected(valid_events()[:-2])


if __name__ == "__main__":
    unittest.main()
