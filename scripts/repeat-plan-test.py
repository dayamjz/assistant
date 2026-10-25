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
        plan = run("internal/service/README.md", "internal/vcs/git.go", "docs/prd.md", "")
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


def git(repo, *args):
    """git in a test repository, with automatic maintenance off so no
    background git outlives the test (#119)."""
    return subprocess.run(["git", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "user.name=test",
                           "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", *args],
                          cwd=repo, check=True, capture_output=True, text=True).stdout.strip()


class ChangedPathsTest(unittest.TestCase):
    """The plan of a real change between two commits, as the workflow asks
    for it: repeat-plan.py --base BASE --head HEAD in the checkout."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="repeat-plan-"))
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", str(self.repo)], check=False))
        git(self.repo, "init", "-q", "-b", "main")
        source = next(p for p in sorted((SCRIPT.parent.parent / "internal/service").glob("*.go"))
                      if not p.name.endswith("_test.go"))
        (self.repo / "internal/service").mkdir(parents=True)
        (self.repo / "internal/journey").mkdir(parents=True)
        (self.repo / "internal/service/move.go").write_text(source.read_text())
        (self.repo / "internal/journey/doc.go").write_text("package journey\n")
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-q", "-m", "base")
        self.base = git(self.repo, "rev-parse", "HEAD")

    def plan(self):
        head = git(self.repo, "rev-parse", "HEAD")
        result = subprocess.run([sys.executable, str(SCRIPT), "--base", self.base, "--head", head],
                                cwd=self.repo, capture_output=True, text=True, check=True)
        return json.loads(result.stdout)

    def test_a_file_moved_out_of_a_covered_package_runs_that_package(self):
        # The file leaves internal/service for internal/journey, its package
        # line changed: git would call it a rename and name only where it
        # went, which the gate does not cover.
        git(self.repo, "mv", "internal/service/move.go", "internal/journey/move.go")
        moved = self.repo / "internal/journey/move.go"
        moved.write_text(moved.read_text().replace("package service", "package journey", 1))
        git(self.repo, "commit", "-q", "-am", "move")
        self.assertIn("./internal/service", self.plan()["packages"].split())

    def test_a_change_outside_the_covered_packages_runs_nothing(self):
        # The positive control: the same setup with a change the gate does
        # not cover plans no run.
        (self.repo / "internal/journey/doc.go").write_text("package journey\n\n// Doc.\n")
        git(self.repo, "commit", "-q", "-am", "doc")
        self.assertEqual(self.plan()["run"], False)


if __name__ == "__main__":
    unittest.main()
