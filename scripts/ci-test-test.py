#!/usr/bin/env python3
"""Tests for scripts/ci-test.sh's inputs.

The script runs as CI runs it, from a scratch directory, with python3 and go
first on PATH as programs that record their arguments and exit 0 (nothing
else of the suite runs). A value the script cannot use must be refused
before either is started; a value it can use must reach go test after
-race, which the script promises is never optional.
"""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "ci-test.sh"

RECORDER = """#!/bin/sh
printf '%s\\n' "$(basename "$0") $*" >> "$CI_TEST_CALLS"
exit 0
"""


class InputTest(unittest.TestCase):
    def setUp(self):
        self.folder = Path(tempfile.mkdtemp(prefix="ci-test-test-"))
        self.addCleanup(shutil.rmtree, self.folder, ignore_errors=True)
        bin_dir = self.folder / "bin"
        bin_dir.mkdir()
        for name in ("python3", "go"):
            program = bin_dir / name
            program.write_text(RECORDER)
            program.chmod(0o755)
        (self.folder / "runner-temp").mkdir()
        self.calls = self.folder / "calls"
        self.env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                    "CI_TEST_CALLS": str(self.calls), "RUNNER_OS": "macOS",
                    "RUNNER_TEMP": str(self.folder / "runner-temp")}

    def run_script(self, **values):
        env = {**self.env, **values}
        result = subprocess.run(["bash", str(SCRIPT)], cwd=self.folder, env=env,
                                capture_output=True, text=True, timeout=60)
        calls = self.calls.read_text().splitlines() if self.calls.exists() else []
        return result, calls

    def assert_refused(self, name, value):
        result, calls = self.run_script(**{name: value})
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn(f"::error::{name}", result.stdout)
        self.assertEqual(calls, [], "something ran before the value was refused")

    def test_a_flag_in_the_packages_is_refused(self):
        # Each would turn the race detector off or select no test at all,
        # and the run would still pass.
        for value in ("-race=false ./...", "-run=^$ ./...", "./... -race=false", "-count=0 ./internal/agents",
                      "-exec=true ./..."):
            with self.subTest(value=value):
                self.assert_refused("CI_TEST_PACKAGES", value)

    def test_a_package_outside_the_module_root_is_refused(self):
        # (An empty value is the default, ./...; spaces alone name nothing.)
        for value in ("internal/agents", "/tmp", "../other", "./../other", "./internal/x;rm", "   "):
            with self.subTest(value=value):
                self.assert_refused("CI_TEST_PACKAGES", value)

    def test_a_bad_count_or_timeout_is_refused(self):
        self.assert_refused("CI_TEST_COUNT", "0")
        self.assert_refused("CI_TEST_COUNT", "3 -race=false")
        self.assert_refused("CI_TEST_TIMEOUT", "30m -race=false")

    def test_package_patterns_reach_go_test_after_the_race_flag(self):
        # The positive control: valid values pass the checks, and go test
        # (run through the leak check, here the python3 recorder) gets -race
        # first and the packages as given.
        _, calls = self.run_script(CI_TEST_PACKAGES="./internal/agents/... ./internal/service",
                                   CI_TEST_COUNT="3", CI_TEST_TIMEOUT="55m")
        runs = [c for c in calls if c.startswith("python3 scripts/leak-check.py run ")]
        self.assertEqual(len(runs), 1, calls)
        words = runs[0].split()
        go_test = words[words.index("go"):]
        self.assertEqual(go_test[:2], ["go", "test"])
        self.assertLess(go_test.index("-race"), go_test.index("./internal/agents/..."))
        self.assertIn("-count=3", go_test)
        self.assertEqual(go_test[-2:], ["./internal/agents/...", "./internal/service"])
        self.assertEqual(go_test.index("-race"), go_test.index("-count=3") - 1)


if __name__ == "__main__":
    unittest.main()
