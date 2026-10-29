#!/usr/bin/env python3
"""Tests for the repeat gate's plan, repeat-plan.py, run as CI runs it."""

import importlib.util
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time
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


# Discovery's timeout, scaled down, for a listing that stalls.
STALL_TIMEOUT = 5.0


def stalled_listing(test, escapee=False):
    """A module whose `go test -list` stalls in a -toolexec wrapper, and the
    environment that selects it. The wrapper ignores SIGTERM, so only SIGKILL
    to its group ends it, and records its pid in stall.pid; with escapee it
    first starts a process in a session of its own, outside that group,
    which records its pid in escapee.pid."""
    root = Path(tempfile.mkdtemp(prefix="repeat-plan-stall-"))
    test.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
    (root / "go.mod").write_text("module example.com/stalllist\n\ngo 1.25.0\n")
    (root / "x_test.go").write_text(
        'package stalllist\n\nimport "testing"\n\nfunc TestA(t *testing.T) {}\n'
    )
    lines = ["#!/bin/sh", "trap '' TERM"]
    if escapee:
        record = shlex.quote(str(root / "escapee.pid"))
        code = ("import os, pathlib, sys, time; os.setsid(); "
                "pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(600)")
        lines += [f"{shlex.quote(sys.executable)} -c {shlex.quote(code)} {record} "
                  "</dev/null >/dev/null 2>&1 &",
                  f"while [ ! -s {record} ]; do sleep 0.1; done"]
    lines += [f"echo $$ > {shlex.quote(str(root / 'stall.pid'))}", "exec sleep 600"]
    wrapper = root / "stall-toolexec"
    wrapper.write_text("\n".join(lines) + "\n")
    wrapper.chmod(0o755)
    return root, dict(os.environ, GOFLAGS=f"-toolexec={wrapper}")


def recorded_pid(path):
    """The pid a stalled listing's process wrote to path, or None if it never
    got that far."""
    return int(path.read_text().strip()) if path.exists() else None


def running(pid):
    """Whether pid is still there; signal 0 signals nothing."""
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


def end_survivor(pid):
    """SIGKILL pid if it is still running, and say whether it was."""
    if pid is None or not running(pid):
        return False
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    return True


def group_empty(pgid):
    """Whether killpg(pgid, 0) raises ProcessLookupError."""
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def listing_with_leftover(test, exit_code=0):
    """A command that starts sleep 60 in its process group, prints TestProbe,
    and exits with exit_code. Records the sleep's pid in leftover.pid and the
    leader's pid (the process group) in leader.pid."""
    root = Path(tempfile.mkdtemp(prefix="repeat-plan-leftover-"))
    test.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
    leftover = shlex.quote(str(root / "leftover.pid"))
    leader = shlex.quote(str(root / "leader.pid"))
    script = root / "list.sh"
    script.write_text(
        "#!/bin/sh\n"
        f"echo $$ > {leader}\n"
        f"sleep 60 </dev/null >/dev/null 2>&1 &\n"
        f"echo $! > {leftover}\n"
        f"while [ ! -s {leftover} ]; do sleep 0.05; done\n"
        "echo TestProbe\n"
        f"exit {int(exit_code)}\n"
    )
    script.chmod(0o755)
    return root, [str(script)]


def mid_discovery_script(test):
    """A listing command that records its pgid and sleeps, so a signal can
    reach the planner while discovery is still waiting on it."""
    root = Path(tempfile.mkdtemp(prefix="repeat-plan-mid-"))
    test.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
    leader = shlex.quote(str(root / "leader.pid"))
    script = root / "list.sh"
    script.write_text(
        "#!/bin/sh\n"
        f"echo $$ > {leader}\n"
        "exec sleep 600\n"
    )
    script.chmod(0o755)
    return root, [str(script)]


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

    def test_list_tests_failure_keeps_go_stderr_and_exit_code(self):
        # A silent CalledProcessError hid the runner's process-table error (#727).
        class result:
            returncode = 1
            stderr = "listing processes: permission denied\n"
            stdout = ""

        def fake_run(cmd, **kwargs):
            self.assertIn("-race", cmd)
            return result

        with self.assertRaises(RuntimeError) as ctx:
            repeat_plan.list_tests("./internal/service", runner=fake_run)
        message = str(ctx.exception)
        self.assertIn("exited 1", message)
        self.assertIn("permission denied", message)
        self.assertIn("-race", message)

    def test_list_tests_includes_a_race_only_test(self):
        # Discovery without -race omits //go:build race tests, so a list taken
        # that way misses tests scripts/ci-test.sh runs (#727 review).
        root = Path(tempfile.mkdtemp(prefix="repeat-plan-race-"))
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
        (root / "go.mod").write_text("module example.com/racelist\n\ngo 1.25.0\n")
        (root / "plain_test.go").write_text(
            'package racelist\n\nimport "testing"\n\nfunc TestPlain(t *testing.T) {}\n'
        )
        (root / "race_test.go").write_text(
            '//go:build race\n\npackage racelist\n\nimport "testing"\n\n'
            "func TestRaceOnly(t *testing.T) {}\n"
        )
        without = repeat_plan.run_discovery(["go", "test", "-list", "^Test", "."], cwd=root)
        self.assertEqual(without.returncode, 0, without.stderr)
        without_names = {line for line in without.stdout.splitlines() if line.startswith("Test")}
        self.assertIn("TestPlain", without_names)
        self.assertNotIn("TestRaceOnly", without_names)

        names = set(repeat_plan.list_tests(".", cwd=root))
        self.assertIn("TestPlain", names)
        self.assertIn("TestRaceOnly", names)

    def test_a_discovery_timeout_ends_its_group_and_says_what_it_does_not(self):
        # Discovery ends its process group, a member that ignores SIGTERM
        # included, and says nothing in it is left only once the group is
        # gone. A process the stalled wrapper started with setsid is outside
        # the group: it runs on, and the error says so, so the error claims
        # no more than the group end does (#727 review, P15).
        root, env = stalled_listing(self, escapee=True)
        with self.assertRaises(RuntimeError) as ctx:
            repeat_plan.list_tests(".", cwd=root, timeout=STALL_TIMEOUT, env=env)
        escapee = recorded_pid(root / "escapee.pid")
        if escapee is not None:
            self.addCleanup(end_survivor, escapee)
        stall = recorded_pid(root / "stall.pid")
        self.assertIsNotNone(stall, "the listing never reached its stalled wrapper")
        self.assertFalse(end_survivor(stall), f"stalled wrapper {stall} outlived discovery's timeout")
        self.assertIsNotNone(escapee, "the wrapper never started its setsid process")
        self.assertTrue(running(escapee), "the setsid process the error says is not ended has ended")
        message = str(ctx.exception)
        self.assertIn("discovery timed out", message)
        self.assertIn("nothing in it is left", message)
        self.assertIn("(setsid, setpgid) is outside that group and is not ended", message)

    def test_a_reaped_leader_is_not_signalled(self):
        # Once reaped and the group is empty, the leader's pid may name
        # another group, so end_process_group must not signal (P15).
        proc = subprocess.Popen(["true"], start_new_session=True)
        proc.wait()
        self.assertTrue(repeat_plan.end_process_group(proc).startswith("not signalled"))

    def test_discovery_ends_its_group_after_exit_0_with_a_leftover(self):
        # Fail before (62ec5dd): run_discovery returned success and left
        # sleep running. Now the group is empty afterwards and the leftover
        # fails the listing loudly so the later leak-check baseline cannot
        # hide it (#744 / #727 review, P15).
        self._assert_leftover_after_exit(exit_code=0)

    def test_discovery_ends_its_group_after_nonzero_exit_with_a_leftover(self):
        self._assert_leftover_after_exit(exit_code=7)

    def _assert_leftover_after_exit(self, exit_code):
        root, cmd = listing_with_leftover(self, exit_code=exit_code)
        err = None
        try:
            repeat_plan.run_discovery(cmd, timeout=STALL_TIMEOUT)
        except RuntimeError as exc:
            err = exc
        leftover = recorded_pid(root / "leftover.pid")
        leader = recorded_pid(root / "leader.pid")
        self.addCleanup(end_survivor, leftover)
        self.assertIsNotNone(leftover, "the listing never started its leftover sleep")
        self.assertIsNotNone(leader, "the listing never recorded its process group")
        self.assertIsNotNone(err, f"run_discovery returned while leftover {leftover} could still run")
        self.assertTrue(group_empty(leader), f"process group {leader} still had members")
        self.assertFalse(running(leftover), f"leftover sleep {leftover} outlived discovery")
        message = str(err)
        self.assertIn(f"still had members after the listing exited {exit_code}", message)
        self.assertIn("nothing in it is left", message)

    def test_discovery_ends_its_group_when_the_planner_gets_sigint(self):
        self._assert_signal_ends_discovery_group(signal.SIGINT)

    def test_discovery_ends_its_group_when_the_planner_gets_sigterm(self):
        self._assert_signal_ends_discovery_group(signal.SIGTERM)

    def _assert_signal_ends_discovery_group(self, sig):
        # Fail before (62ec5dd): interrupting the planner left discovery's
        # child running. A real planner subprocess installs the SIGTERM
        # handler (when present) and runs run_discovery; after the signal,
        # the group is empty (#744 / #727 review, P15).
        root, cmd = mid_discovery_script(self)
        child = subprocess.Popen(
            [
                sys.executable, "-c",
                "import importlib.util;"
                f"spec = importlib.util.spec_from_file_location('rp', {str(SCRIPT)!r});"
                "rp = importlib.util.module_from_spec(spec); spec.loader.exec_module(rp);"
                "getattr(rp, 'install_termination_signals', lambda: None)();"
                f"rp.run_discovery({cmd!r}, timeout=60)",
            ],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )

        def _reap_child():
            if child.poll() is None:
                child.kill()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
            for pipe in (child.stdout, child.stderr):
                if pipe is not None:
                    pipe.close()
            # Old planners leave the listing; end it if still around.
            leader = recorded_pid(root / "leader.pid")
            if leader is not None:
                try:
                    os.killpg(leader, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass

        self.addCleanup(_reap_child)
        deadline = time.monotonic() + 10
        leader = None
        while time.monotonic() < deadline:
            leader = recorded_pid(root / "leader.pid")
            if leader is not None:
                break
            time.sleep(0.05)
        self.assertIsNotNone(leader, "discovery never reached its listing command")
        child.send_signal(sig)
        try:
            child.wait(timeout=repeat_plan.discovery_bound(STALL_TIMEOUT))
        except subprocess.TimeoutExpired:
            self.fail(f"planner did not exit after {sig!r}")
        self.assertTrue(group_empty(leader), f"process group {leader} still had members after {sig!r}")

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
