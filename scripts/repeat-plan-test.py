#!/usr/bin/env python3
"""Tests for the repeat gate's plan, repeat-plan.py, run as CI runs it."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("repeat-plan.py").resolve()
SPEC = importlib.util.spec_from_file_location("repeat_plan", SCRIPT)
repeat_plan = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(repeat_plan)


def run(*paths, output=None):
    args = [sys.executable, str(SCRIPT)] + (["--github-output", output] if output else [])
    result = subprocess.run(args, input="\n".join(paths) + "\n", capture_output=True, text=True, check=True)
    return json.loads(result.stdout)


class PlanTest(unittest.TestCase):
    def test_a_change_to_a_risky_package_runs_that_package_alone(self):
        plan = run("internal/agents/standin/procs_unix.go")
        self.assertEqual(plan["packages"], "./internal/agents/standin")
        self.assertFalse(plan["leak_check_tests"])
        self.assertTrue(plan["run"])
        self.assertIn("#549", plan["why"][0])

    def test_a_parent_package_is_not_its_child(self):
        self.assertEqual(run("internal/agents/sweep_darwin.go")["packages"], "./internal/agents")

    def test_a_new_test_runs_its_package(self):
        self.assertEqual(run("internal/service/dispatch_internal_test.go")["packages"], "./internal/service")

    def test_test_data_belongs_to_the_package_above_it(self):
        plan = run("internal/procgroup/guard/testdata/control/owner/owner.go")
        self.assertEqual(plan["packages"], "./internal/procgroup/guard")

    def test_prose_and_other_packages_run_nothing(self):
        plan = run("internal/service/README.md", "internal/vcs/git.go", "docs/prd.html", "")
        self.assertEqual((plan["run"], plan["packages"], plan["leak_check_tests"]), (False, "", False))

    def test_the_leak_check_repeats_its_own_tests(self):
        plan = run("scripts/leak-check.py")
        self.assertEqual((plan["run"], plan["packages"], plan["leak_check_tests"]), (True, "", True))

    def test_a_change_to_the_gate_runs_all_of_it(self):
        plan = run(".github/workflows/repeat.yml")
        self.assertEqual(plan["packages"].split(), [f"./{p}" for p in sorted(repeat_plan.RISKY)])
        self.assertTrue(plan["leak_check_tests"])

    def test_several_changes_name_each_package_once_in_order(self):
        plan = run("internal/service/a.go", "internal/agents/standin/b.go", "internal/service/c.go")
        self.assertEqual(plan["packages"], "./internal/agents/standin ./internal/service")

    def test_step_outputs(self):
        with tempfile.NamedTemporaryFile("r", suffix=".txt") as out:
            run("internal/service/a.go", output=out.name)
            self.assertEqual(Path(out.name).read_text(), "run=true\npackages=./internal/service\nleak_check_tests=false\n")

    def test_every_covered_package_exists(self):
        root = SCRIPT.parent.parent
        for package in repeat_plan.RISKY:
            self.assertTrue(list((root / package).glob("*.go")), f"{package} has no Go files")


if __name__ == "__main__":
    unittest.main()
