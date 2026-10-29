#!/usr/bin/env python3
"""Tests for the repeat gate's plan, repeat-plan.py, run as CI runs it."""

import importlib.util
import json
import os
from pathlib import Path
import re
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


# Room a bound around --shard-run leaves above discovery_bound for the
# interpreter to start, import the planner and print.
SHARD_RUN_MARGIN = 30.0
# Discovery's timeout, scaled down, for a listing that stalls.
STALL_TIMEOUT = 5.0


def shard_run(package, shard, shards, discovery_timeout=None, **kwargs):
    """--shard-run as the workflow's Shard filter step runs it, under a bound
    above discovery_bound(discovery_timeout), so a stalled listing ends with
    discovery's own error and its group ended, not with this bound killing
    the planner while go's group runs on (#727). discovery_timeout defaults
    to DISCOVERY_TIMEOUT."""
    if discovery_timeout is None:
        discovery_timeout = repeat_plan.DISCOVERY_TIMEOUT
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--shard-run", package, str(shard), str(shards),
         "--discovery-timeout", str(discovery_timeout)],
        capture_output=True, text=True,
        timeout=repeat_plan.discovery_bound(discovery_timeout) + SHARD_RUN_MARGIN, **kwargs)


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


def listing_with_leftover(test, exit_code=0, diagnostic=None):
    """A command that starts sleep 60 in its process group, optionally prints
    diagnostic on stderr, prints TestProbe on stdout, and exits with
    exit_code. Records the sleep's pid in leftover.pid and the leader's pid
    (the process group) in leader.pid."""
    root = Path(tempfile.mkdtemp(prefix="repeat-plan-leftover-"))
    test.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
    leftover = shlex.quote(str(root / "leftover.pid"))
    leader = shlex.quote(str(root / "leader.pid"))
    lines = [
        "#!/bin/sh",
        f"echo $$ > {leader}",
        f"sleep 60 </dev/null >/dev/null 2>&1 &",
        f"echo $! > {leftover}",
        f"while [ ! -s {leftover} ]; do sleep 0.05; done",
    ]
    if diagnostic is not None:
        lines.append(f"echo {shlex.quote(diagnostic)} >&2")
    lines += ["echo TestProbe", f"exit {int(exit_code)}"]
    script = root / "list.sh"
    script.write_text("\n".join(lines) + "\n")
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
            text = Path(out.name).read_text()
            self.assertTrue(text.startswith("run=true\npackages=./internal/service\nleak_check_tests=false\nmatrix="), text)
            matrix = json.loads(text.split("matrix=", 1)[1])
            self.assertEqual(len(matrix["include"]), 2 * repeat_plan.SERVICE_SHARDS)

    def test_a_service_change_shards_on_both_oses(self):
        # internal/service alone outgrew one macOS budget (#726); the matrix
        # splits it so each job keeps -race, REPEAT_COUNT, and the leak check.
        plan = run("internal/service/dispatch_internal_test.go")
        include = plan["matrix"]["include"]
        self.assertEqual({e["os"] for e in include}, set(repeat_plan.OSES))
        by_os = {}
        for entry in include:
            by_os.setdefault(entry["os"], []).append(entry)
        for os_name, entries in by_os.items():
            self.assertEqual([e["name"] for e in entries],
                             [f"service-{i}" for i in range(repeat_plan.SERVICE_SHARDS)], os_name)
            self.assertEqual({e["packages"] for e in entries}, {"./internal/service"})
            self.assertEqual([e["shard"] for e in entries],
                             [str(i) for i in range(repeat_plan.SERVICE_SHARDS)])
            self.assertEqual({e["shards"] for e in entries}, {str(repeat_plan.SERVICE_SHARDS)})
            self.assertEqual([e["leak_check_tests"] for e in entries],
                             ["false"] * repeat_plan.SERVICE_SHARDS)

    def test_other_packages_stay_one_job_per_os(self):
        plan = run("internal/agents/sweep_darwin.go")
        include = plan["matrix"]["include"]
        self.assertEqual(len(include), len(repeat_plan.OSES))
        for entry in include:
            self.assertEqual(entry["name"], "other")
            self.assertEqual(entry["packages"], "./internal/agents")
            self.assertEqual(entry["shard"], "")

    def test_service_and_others_split_leak_onto_the_first_service_shard(self):
        plan = run(".github/workflows/repeat.yml")
        include = plan["matrix"]["include"]
        for os_name in repeat_plan.OSES:
            rows = [e for e in include if e["os"] == os_name]
            service = [e for e in rows if e["packages"] == "./internal/service"]
            other = [e for e in rows if e["name"] == "other"]
            self.assertEqual(len(service), repeat_plan.SERVICE_SHARDS)
            self.assertEqual(len(other), 1)
            self.assertIn("./internal/agents", other[0]["packages"].split())
            self.assertNotIn("./internal/service", other[0]["packages"].split())
            self.assertEqual(service[0]["leak_check_tests"], "true")
            self.assertTrue(all(e["leak_check_tests"] == "false" for e in service[1:] + other))

    def test_leak_only_still_runs_on_both_oses(self):
        plan = run("scripts/leak-check.py")
        include = plan["matrix"]["include"]
        self.assertEqual(len(include), len(repeat_plan.OSES))
        for entry in include:
            self.assertEqual(entry["name"], "leak")
            self.assertEqual(entry["packages"], "")
            self.assertEqual(entry["leak_check_tests"], "true")

    def test_partition_round_robins_and_covers_every_name(self):
        names = [f"Test{c}" for c in "ABCDEF"]
        parts = repeat_plan.partition_tests(names, 3)
        self.assertEqual(parts, [["TestA", "TestD"], ["TestB", "TestE"], ["TestC", "TestF"]])
        self.assertEqual(sorted(n for part in parts for n in part), names)

    def test_run_pattern_selects_exactly_the_named_tests(self):
        self.assertEqual(repeat_plan.run_pattern(["TestA", "TestB_C"]), "^(TestA|TestB_C)$")

    def test_every_covered_package_exists(self):
        root = SCRIPT.parent.parent
        for package in repeat_plan.RISKY:
            self.assertTrue(list((root / package).glob("*.go")), f"{package} has no Go files")

    def test_with_shard_runs_records_per_os_listing_in_why(self):
        # Patterns are not baked on Ubuntu: each OS job lists (#727).
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--with-shard-runs"],
            input="internal/service/dispatch_internal_test.go\n",
            capture_output=True, text=True, check=True, timeout=60,
        )
        plan = json.loads(result.stdout)
        self.assertTrue(any("each OS lists" in line for line in plan["why"]), plan["why"])
        for entry in plan["matrix"]["include"]:
            self.assertEqual(entry["shards"], str(repeat_plan.SERVICE_SHARDS))
            self.assertRegex(entry["shard"], r"^[0-2]$")

    def test_ubuntu_discovered_patterns_omit_a_darwin_only_name(self):
        # Fail before: baking Ubuntu's list into macOS shards drops Darwin-only
        # tests (#727 review). Each OS must partition its own list.
        linux = ["TestLinuxOnly", "TestShared"]
        darwin = ["TestDarwinOnly", "TestShared"]
        linux_patterns = [repeat_plan.run_pattern(part)
                          for part in repeat_plan.partition_tests(linux, 2)]
        darwin_patterns = [repeat_plan.run_pattern(part)
                           for part in repeat_plan.partition_tests(darwin, 2)]

        def covered(name, patterns):
            return any(re.fullmatch(pat, name) for pat in patterns)

        self.assertFalse(covered("TestDarwinOnly", linux_patterns))
        self.assertTrue(covered("TestDarwinOnly", darwin_patterns))
        self.assertTrue(covered("TestShared", linux_patterns))
        self.assertTrue(covered("TestShared", darwin_patterns))

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
        # Discovery without -race omits //go:build race tests; shards would
        # then pass while the unfiltered race suite failed (#727 review).
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
        # Fail before (2371a6d): the error and docstring claimed discovery
        # ended its whole tree, while a process the stalled wrapper started
        # with setsid ran on, and the error read "(still present)" for a
        # group with nothing left in it but its unreaped leader. Discovery
        # ends its process group, a member that ignores SIGTERM included, and
        # says that a process outside the group is not ended (#727, P15).
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
        # Once reaped, the leader's pid may name another group, so
        # end_process_group must not signal (procgroup's rule / P15).
        proc = subprocess.Popen(["true"], start_new_session=True)
        proc.wait()
        self.assertTrue(repeat_plan.end_process_group(proc).startswith("not signalled"))

    def test_discovery_signals_only_while_the_leader_is_unreaped(self):
        # Fail before (c6d89e7): communicate reaped the leader, then SIGKILL
        # after a grace could target a recycled pgid (#744 round 3). Now every
        # killpg happens while returncode is still None, and none after reap.
        events = []
        real_end = repeat_plan.end_process_group

        def wrapped(proc, grace=repeat_plan.DISCOVERY_KILL_GRACE):
            real_killpg = os.killpg

            def killpg(pgid, sig):
                # Presence polls use signal 0 after the reap; only real
                # termination signals must see an unreaped leader.
                if sig != 0:
                    events.append(("signal", sig, proc.returncode))
                    try:
                        os.kill(proc.pid, 0)
                    except ProcessLookupError:
                        events.append(("leader_gone", sig))
                return real_killpg(pgid, sig)

            os.killpg = killpg
            try:
                events.append(("enter_end", proc.returncode))
                result = real_end(proc, grace)
                events.append(("after_end", proc.returncode))
                return result
            finally:
                os.killpg = real_killpg

        repeat_plan.end_process_group = wrapped
        self.addCleanup(lambda: setattr(repeat_plan, "end_process_group", real_end))
        root, cmd = listing_with_leftover(self, exit_code=0)
        with self.assertRaises(RuntimeError):
            repeat_plan.run_discovery(cmd, timeout=STALL_TIMEOUT)
        leftover = recorded_pid(root / "leftover.pid")
        leader = recorded_pid(root / "leader.pid")
        self.addCleanup(end_survivor, leftover)
        self.assertIsNotNone(leader)
        self.assertTrue(group_empty(leader), f"process group {leader} still had members")
        self.assertEqual(events[0], ("enter_end", None), events)
        signals = [e for e in events if e[0] == "signal"]
        self.assertEqual(
            [sig for _, sig, _ in signals],
            [signal.SIGTERM, signal.SIGKILL],
            events,
        )
        for _, sig, code in signals:
            self.assertIsNone(code, f"signal {sig} saw a reaped leader: {events}")
        self.assertNotIn("leader_gone", [e[0] for e in events], events)
        self.assertIsNotNone(events[-1][1], "leader was not reaped after the last signal")

    def test_discovery_ends_its_group_after_exit_0_with_a_leftover(self):
        # Fail before (62ec5dd): run_discovery returned success and left
        # sleep running. Now the group is empty afterwards and the leftover
        # fails the listing loudly so the later leak-check baseline cannot
        # hide it (#744 / #727 review, P15). Round 2: the error keeps stderr.
        self._assert_leftover_after_exit(
            exit_code=0, diagnostic="listing processes: permission denied")

    def test_discovery_ends_its_group_after_nonzero_exit_with_a_leftover(self):
        # Fail before (c8bbfee): cleanup ended the child but the error omitted
        # Go's exit code and stderr (#744 round 2).
        self._assert_leftover_after_exit(
            exit_code=7, diagnostic="listing processes: permission denied")

    def _assert_leftover_after_exit(self, exit_code, diagnostic=None):
        root, cmd = listing_with_leftover(self, exit_code=exit_code, diagnostic=diagnostic)
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
        self.assertIn(f"exited {exit_code}", message)
        self.assertIn(f"still had members after the listing exited {exit_code}", message)
        self.assertIn("nothing in it is left", message)
        if diagnostic is not None:
            self.assertIn(diagnostic, message)

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

    def test_cleanup_defers_sigterm_until_the_group_is_ended(self):
        # Fail before (c8bbfee): a SIGTERM during end_process_group's grace
        # after timeout cut cleanup short before SIGKILL; the listing (which
        # ignores SIGTERM) stayed alive while the planner exited 143 (#744).
        root, env = stalled_listing(self)
        started = time.monotonic()
        child = subprocess.Popen(
            [
                sys.executable, "-c",
                "import importlib.util, os;"
                f"spec = importlib.util.spec_from_file_location('rp', {str(SCRIPT)!r});"
                "rp = importlib.util.module_from_spec(spec); spec.loader.exec_module(rp);"
                "rp.install_termination_signals();"
                f"rp.run_discovery(['go', 'test', '-list', '^Test', '.'], "
                f"timeout={STALL_TIMEOUT}, cwd={str(root)!r}, env=dict(os.environ))",
            ],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env,
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
            stall = recorded_pid(root / "stall.pid")
            if stall is not None:
                end_survivor(stall)

        self.addCleanup(_reap_child)
        deadline = started + STALL_TIMEOUT + 1
        stall = None
        while time.monotonic() < deadline:
            stall = recorded_pid(root / "stall.pid")
            if stall is not None:
                break
            time.sleep(0.05)
        self.assertIsNotNone(stall, "discovery never reached its stalled wrapper")
        # Into the grace after discovery's timeout: cleanup has sent SIGTERM
        # to the group and is sleeping before SIGKILL.
        into_grace = started + STALL_TIMEOUT + 0.3
        delay = into_grace - time.monotonic()
        if delay > 0:
            time.sleep(delay)
        child.send_signal(signal.SIGTERM)
        try:
            child.wait(timeout=repeat_plan.discovery_bound(STALL_TIMEOUT) + 5)
        except subprocess.TimeoutExpired:
            self.fail("planner did not exit after deferred SIGTERM")
        self.assertEqual(
            child.returncode, 128 + signal.SIGTERM,
            f"want deferred SIGTERM status, got {child.returncode}",
        )
        self.assertTrue(group_empty(stall), f"process group {stall} still had members")
        self.assertFalse(running(stall), f"stalled wrapper {stall} outlived deferred-SIGTERM cleanup")

    def test_a_bound_around_shard_run_outlasts_discovery_and_its_cleanup(self):
        # Fail before (2371a6d): the bound around --shard-run equalled
        # discovery's deadline (300s each), so on a stalled listing it killed
        # the planner first and go's group ran on. With discovery's timeout
        # scaled down, shard_run's bound, derived from discovery_bound, lets
        # discovery's own timeout end the group (#727, P15).
        root, env = stalled_listing(self)
        try:
            result = shard_run(".", 0, 1, discovery_timeout=STALL_TIMEOUT, cwd=root, env=env)
        except subprocess.TimeoutExpired as expired:
            result = expired
        finally:
            stall = recorded_pid(root / "stall.pid")
            survived = end_survivor(stall)
        self.assertIsNotNone(stall, "the listing never reached its stalled wrapper")
        self.assertFalse(survived, f"stalled wrapper {stall} outlived the bound around --shard-run")
        self.assertIsInstance(result, subprocess.CompletedProcess,
                              "the bound around --shard-run fired before discovery's own timeout")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("discovery timed out", result.stderr)
        self.assertEqual(result.stdout, "")

    def test_shard_run_partitions_real_service_tests(self):
        # --shard-run partitions list_tests on this OS; --with-shard-runs only
        # records that split in why (it does not embed a partition).
        listed = sorted(repeat_plan.list_tests("./internal/service"))
        self.assertGreater(len(listed), repeat_plan.SERVICE_SHARDS)
        patterns = []
        for i in range(repeat_plan.SERVICE_SHARDS):
            result = shard_run("./internal/service", i, repeat_plan.SERVICE_SHARDS)
            self.assertEqual(result.returncode, 0, result.stderr)
            patterns.append(result.stdout)
        for name in listed:
            expect = listed.index(name) % repeat_plan.SERVICE_SHARDS
            hits = [i for i, pat in enumerate(patterns) if re.fullmatch(pat, name)]
            self.assertEqual(hits, [expect], name)
        # A listing error must fail the shard step, never yield an empty pattern.
        failed = shard_run("./internal/no-such-package", 0, 3)
        self.assertNotEqual(failed.returncode, 0)
        self.assertTrue(failed.stderr.strip())
        self.assertEqual(failed.stdout, "")


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
