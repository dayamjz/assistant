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
        return self.run_script_with(None, **values)

    def run_script_with(self, mode, **values):
        env = {**self.env, **values}
        result = subprocess.run(["bash", str(SCRIPT)] + ([mode] if mode else []), cwd=self.folder, env=env,
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

    def test_a_run_filter_with_a_flag_or_space_is_refused(self):
        # A flag or a list of names is not a pattern of test names: as
        # -run's value it would select no test, and the run would pass.
        self.assert_refused("CI_TEST_RUN", "-race=false")
        self.assert_refused("CI_TEST_RUN", "TestA TestB")
        self.assert_refused("CI_TEST_RUN", "^(TestA)$ -count=1")

    def test_the_unfinished_mode_refuses_the_same_packages(self):
        # bash ci-test.sh unfinished lists packages with go list; a flag in
        # its packages is refused there too, before go runs.
        (self.folder / "test.log").write_text("ok  \tgithub.com/dayamjz/assistant/internal/agents\t1.0s\n")
        result, calls = self.run_script_with("unfinished", CI_TEST_PACKAGES="-json ./...")
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn("::error::CI_TEST_PACKAGES", result.stdout)
        self.assertEqual(calls, [])

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
        self.assertNotIn("-run", go_test, "no CI_TEST_RUN, so every test runs")

    def test_a_run_filter_reaches_go_test_before_the_packages(self):
        _, calls = self.run_script(CI_TEST_PACKAGES="./internal/service",
                                   CI_TEST_COUNT="3", CI_TEST_TIMEOUT="55m",
                                   CI_TEST_RUN="^(TestA|TestB)$")
        runs = [c for c in calls if c.startswith("python3 scripts/leak-check.py run ")]
        self.assertEqual(len(runs), 1, calls)
        words = runs[0].split()
        go_test = words[words.index("go"):]
        self.assertIn("-run", go_test)
        self.assertEqual(go_test[go_test.index("-run") + 1], "^(TestA|TestB)$")
        self.assertLess(go_test.index("-run"), go_test.index("./internal/service"))
        self.assertLess(go_test.index("-race"), go_test.index("-run"))

    def test_linux_wrapper_forwards_leak_check_record(self):
        # #902: Ubuntu 26 ignores bare sudo --preserve-env. Stub is dash-safe.
        # Record every KEY=VAL and also apply only those pairs (env -i), then
        # run the payload so a child that prints LEAK_CHECK_RECORD proves the
        # -exec recorder would see the file after preserve-env is dropped.
        # Without that forward, CI reports 0 per-package checks against N ok.
        seen = self.folder / "child-leak-check-record"
        sudo = self.folder / "bin" / "sudo"
        sudo.write_text("\n".join([
            "#!/bin/sh",
            'printf \'%s\\n\' "sudo $*" >> "$CI_TEST_CALLS"',
            "while [ $# -gt 0 ]; do",
            "  case $1 in",
            "    --preserve-env|--preserve-env=*) shift ;;",
            "    env) shift; break ;;",
            "    *) shift ;;",
            "  esac",
            "done",
            "env_args=",
            "while [ $# -gt 0 ]; do",
            "  case $1 in",
            '    *=*) printf \'%s\\n\' "env $1" >> "$CI_TEST_CALLS"; env_args="$env_args $1"; shift ;;',
            "    *) break ;;",
            "  esac",
            "done",
            # Drop unshare/tini/-- and the setpriv bash -c wrapper; run payload.
            "if [ \"$1\" = unshare ]; then",
            "  shift",
            "  while [ $# -gt 0 ]; do",
            "    case $1 in",
            "      --pid|--fork|--mount-proc) shift ;;",
            "      tini) shift; [ \"$1\" = -- ] && shift; break ;;",
            "      *) break ;;",
            "    esac",
            "  done",
            "fi",
            "if [ \"$1\" = bash ] && [ \"$2\" = -c ]; then",
            "  shift 2; shift; shift; shift; shift",
            "fi",
            f'seen="{seen}"',
            'exec env -i HOME="$HOME" $env_args sh -c \''
            'printf "%s\\n" "${LEAK_CHECK_RECORD-}" > "$0"; exit 0\' "$seen"',
            "",
        ]))
        sudo.chmod(0o755)
        _, calls = self.run_script_with(
            None, RUNNER_OS="Linux", CI_TEST_PACKAGES="./internal/agents")
        env_lines = [c for c in calls if c.startswith("env ")]
        self.assertTrue(env_lines, calls)
        joined = " ".join(env_lines)
        self.assertIn("LEAK_CHECK_RECORD=", joined)
        want = str(self.folder / "runner-temp" / "per-package-checked.txt")
        self.assertIn(want, joined)
        self.assertTrue(seen.exists(), "ubuntu-26-like sudo never ran the child")
        self.assertEqual(seen.read_text().strip(), want,
                         "LEAK_CHECK_RECORD must reach the child without preserve-env")


if __name__ == "__main__":
    unittest.main()
