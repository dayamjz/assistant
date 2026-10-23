#!/usr/bin/env python3
"""Tests for the CI flake ledger, flake-ledger.py.

The logs under testdata/flake-ledger are real GitHub job logs, trimmed to the
lines that matter with every kept line verbatim, and api.json holds real API
responses trimmed to the fields the ledger reads. The Go shapes the real logs
do not cover (a timeout, a build failure, a panic outside a test, a package
that fails with no test failing) come from running the real `go test -v` on a
module written here, inside a real log's frame.
"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "flake-ledger.py"
DATA = HERE / "testdata" / "flake-ledger"
SPEC = importlib.util.spec_from_file_location("flake_ledger", SCRIPT)
ledger = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ledger)


def parse(name):
    with open(DATA / name, encoding="utf-8") as handle:
        records, unattributed, _ = ledger.parse_lines(handle)
    return {r.key: r for r in records}, unattributed


def framed(go_output, frame="judge-test-failure.log"):
    """go_output as the Test step's full test output group of a real log."""
    lines = (DATA / frame).read_text(encoding="utf-8").split("\n")
    start = next(i for i, l in enumerate(lines) if l.endswith(ledger.GROUP_START))
    end = next(i for i, l in enumerate(lines) if i > start and l.endswith(ledger.GROUP_END))
    stamp = re.match(r"^﻿?(\S+Z) ", lines[start]).group(1)
    return lines[:start + 1] + [f"{stamp} {line}" for line in go_output] + lines[end:]


class RealLogTest(unittest.TestCase):
    def test_a_failing_test_is_named_and_the_self_tests_planted_leaks_are_not(self):
        records, unattributed = parse("judge-test-failure.log")
        self.assertFalse(unattributed)
        self.assertEqual(list(records), ["test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient"])
        record = records["test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient"]
        # The first message, not principles.Cite's line before it.
        self.assertTrue(record.detail.startswith("judge_test.go:141: no connection went idle"), record.detail)
        self.assertIn("LEAK process pid=", (DATA / "judge-test-failure.log").read_text(),
                      "the frame must carry the self-test's planted leaks for this to prove anything")

    def test_a_per_package_leak_names_the_test_whose_temp_dir_it_ran_from(self):
        records, unattributed = parse("per-package-leak.log")
        self.assertFalse(unattributed)
        leak = records["leak: internal/service child.test from TestAHeldProcessEndsWithItsTestOnEveryPathtimed_out"]
        self.assertTrue(leak.prefix)
        self.assertIn("test: internal/service TestADispatchersTrustedReadIsItsOwn", records)
        self.assertEqual(len(records), 2)

    def test_one_leftover_printed_twice_is_one_record(self):
        text = (DATA / "whole-run-leak-process.log").read_text()
        # The self-test's planted one, then two mdworkers in the summary and again in the group.
        self.assertEqual(text.count("LEAK process"), 5)
        records, unattributed = parse("whole-run-leak-process.log")
        self.assertFalse(unattributed)
        self.assertEqual(list(records), ["run-leak: (whole run) process mdworker_shared"])

    def test_leftover_directories_fold_their_contents_and_their_random_names(self):
        records, _ = parse("whole-run-leak-directories.log")
        self.assertEqual(sorted(records), [
            "run-leak: (whole run) directory /private/tmp/atinternal_1journey-*-*-h*",
            "run-leak: (whole run) directory /private/tmp/atinternal_1service-*-*-gitconfig*",
            "run-leak: (whole run) process CacheDeleteExtension",
            "run-leak: (whole run) process MailCacheDelete",
        ])

    def test_a_unittest_failure_names_its_script_and_method(self):
        records, unattributed = parse("unittest-failure.log")
        self.assertFalse(unattributed)
        name = "unittest: scripts/leak-check-test.py PerPackageTest.test_a_process_started_as_its_parent_exits_mid_reading_is_found"
        self.assertEqual(list(records), [name])
        self.assertEqual(records[name].token, "test_a_process_started_as_its_parent_exits_mid_reading_is_found")


GO_PACKAGES = {
    "pass/pass_test.go": """package pass
import "testing"
func TestPasses(t *testing.T) {}
""",
    "fail/fail_test.go": """package fail
import "testing"
func TestTop(t *testing.T) {
	t.Logf("claims PRD principle P6")
	t.Errorf("the first reason")
	t.Errorf("a second reason")
}
func TestParent(t *testing.T) {
	t.Run("sub a", func(t *testing.T) { t.Fatal("sub a broke") })
	t.Run("sub b", func(t *testing.T) {})
}
// A test may log another go test's output. Indented, it is not this package's.
func TestLogsAFixturesOutput(t *testing.T) {
	t.Log("fixture output:\\n--- FAIL: TestFixture (0.00s)\\nFAIL\\tgithub.com/x/y\\t0.1s")
}
""",
    "timeout/timeout_test.go": """package timeout
import ("testing"; "time")
func TestQuick(t *testing.T) {}
func TestSlow(t *testing.T) {
	t.Run("inner", func(t *testing.T) { time.Sleep(time.Minute) })
}
""",
    "exits/exits_test.go": """package exits
import ("os"; "testing")
func TestMain(m *testing.M) { m.Run(); os.Exit(3) }
func TestPasses(t *testing.T) {}
""",
    "panics/panics_test.go": """package panics
import "testing"
func init() { panic("boom at init") }
func TestNever(t *testing.T) {}
""",
    "build/build_test.go": """package build
import "testing"
func TestNever(t *testing.T) { this is not go }
""",
}


@unittest.skipUnless(shutil.which("go"), "go is not on PATH")
class RealGoTestOutputTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / "go.mod").write_text("module github.com/example/m\n\ngo 1.21\n")
            for name, source in GO_PACKAGES.items():
                (root / name).parent.mkdir(parents=True, exist_ok=True)
                (root / name).write_text(source)
            env = {**os.environ, "GOFLAGS": "", "GOWORK": "off"}
            result = subprocess.run(["go", "test", "-v", "-count=1", "-timeout", "5s", "./..."],
                                    cwd=root, env=env, capture_output=True, text=True, timeout=300)
        cls.output = (result.stdout + result.stderr).split("\n")
        cls.status = result.returncode

    def records(self, lines):
        records, unattributed, _ = ledger.parse_lines(lines)
        return {r.key: r for r in records}, unattributed

    def test_each_kind_of_go_failure_is_one_record(self):
        self.assertNotEqual(self.status, 0)
        records, unattributed = self.records(framed(self.output))
        self.assertFalse(unattributed)
        self.assertEqual(sorted(records), [
            "build: build setup failed",
            "package: exits (package)",
            "package: panics (package)",
            "test: fail TestParent",
            "test: fail TestTop",
            "timeout: timeout TestSlow",
        ], "\n".join(self.output))
        self.assertEqual(records["test: fail TestTop"].detail.split(": ", 1)[1], "the first reason")
        self.assertEqual(records["test: fail TestParent"].subtests, ["TestParent/sub_a"])
        self.assertEqual(records["timeout: timeout TestSlow"].subtests, ["TestSlow/inner"])
        self.assertEqual(records["package: panics (package)"].detail, "panic: boom at init")
        self.assertEqual(records["package: exits (package)"].detail, "no failing test was named")

    def test_raw_output_parses_as_the_group_does(self):
        raw, _ = self.records_raw()
        framed_records, _ = self.records(framed(self.output))
        self.assertEqual(sorted(raw), sorted(framed_records))

    def records_raw(self):
        records, unattributed, _ = ledger.parse_lines(self.output, raw=True)
        return {r.key: r for r in records}, unattributed


class LoudTest(unittest.TestCase):
    def test_every_per_package_leak_shape_the_leak_check_prints_is_read(self):
        # scripts/leak-check.py prints both on Linux (linux_package_run).
        package = "/home/runner/work/assistant/assistant/internal/service"
        lines = framed([
            f"LEAK package={package} process pid=4242 start=12345 command=/tmp/x/child.test --flag",
            f"LEAK package={package} a process was running after the binary exited "
            "and exited before the check read it",
            "FAIL\tgithub.com/dayamjz/assistant/internal/service\t3.2s"])
        records, unattributed, _ = ledger.parse_lines(lines)
        self.assertEqual(sorted(r.key for r in records), [
            "leak: internal/service a process was running after the binary exited and exited before "
            "the check read it",
            "leak: internal/service child.test"])
        self.assertFalse(unattributed)

    def test_a_leak_line_of_a_shape_never_seen_is_still_a_record(self):
        lines = framed(["LEAK package=/home/runner/work/assistant/assistant/internal/vcs something new 77",
                        "LEAK socket-ish /tmp/x9999", "ok  \tgithub.com/dayamjz/assistant/internal/vcs\t1s"])
        records, _, _ = ledger.parse_lines(lines)
        self.assertEqual(sorted(r.key for r in records), [
            "leak: internal/vcs something new 77",
            "run-leak: (whole run) unparsed LEAK socket-ish /tmp/xN"])

    def test_a_group_with_nothing_named_in_it_is_unattributed(self):
        records, unattributed, _ = ledger.parse_lines(framed(["something went wrong", "FAIL"]))
        self.assertEqual(records, [])
        self.assertTrue(unattributed)

    def test_leftovers_the_parser_cannot_read_are_still_a_failure(self):
        lines = framed(["ok  \tgithub.com/example/m/pass\t0.1s",
                        "LEFTOVER widget, named some way this parser has not seen",
                        "leak-check: 1 leftover(s); command exit=0"])
        records, unattributed, _ = ledger.parse_lines(lines)
        self.assertEqual([r.key for r in records], ["run-leak: (whole run) (unparsed leftovers)"])
        self.assertFalse(unattributed)


class FakeGitHub:
    """Serves the recorded responses in api.json and the fixture logs."""

    def __init__(self, logs, unreadable=(), flaky=None, broken=()):
        self.data = json.loads((DATA / "api.json").read_text())
        self.broken = set(broken)  # names of reads that fail for good
        self.logs = logs
        self.unreadable = set(unreadable)
        self.flaky = dict(flaky or {})  # job id -> server errors before it answers
        self.repo = "dayamjz/assistant"
        self.read = []

    def runs(self, workflow, limit, branch=None, since=None):
        return self.data["runs"][:limit]

    def run(self, run_id):
        return next(r for r in self.data["runs"] if r["id"] == run_id)

    def attempt(self, run_id, attempt):
        return {"conclusion": self.data["attempts"][f"{run_id}/{attempt}"]["conclusion"]}

    def attempt_jobs(self, run_id, attempt):
        return self.data["attempts"][f"{run_id}/{attempt}"]["jobs"]

    def annotations(self, job_id):
        return self.data["annotations"][str(job_id)]

    def issues(self):
        if "issues" in self.broken:
            raise ledger.GitHubError("gh api repos/dayamjz/assistant/issues: HTTP 503")
        return self.data["issues"]

    def pulls(self):
        if "pulls" in self.broken:
            raise ledger.GitHubError("gh api repos/dayamjz/assistant/pulls: HTTP 503")
        return []

    def job_log(self, job_id):
        self.read.append(job_id)
        if job_id in self.unreadable:
            raise ledger.GitHubError(f"log of job {job_id}: HTTP 410")
        if self.flaky.get(job_id):
            self.flaky[job_id] -= 1
            yield "a line read before the connection dropped\n"
            raise ledger.GitHubError(f"log of job {job_id}: HTTP 503")
        with open(DATA / self.logs[job_id], encoding="utf-8") as handle:
            yield from handle


LOGS = {120256178246: "rerun-attempt-1.log", 120265317855: "rerun-attempt-2.log",
        120342424178: "job-timeout.log", 120290002840: "superseded-macos.log",
        120290002917: "superseded-ubuntu.log"}


def run_report(github, *extra):
    with tempfile.TemporaryDirectory() as folder:
        out = Path(folder) / "ledger.json"
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            status = ledger.main(["--no-cache", "--tz", "America/Los_Angeles", "--json", str(out), *extra],
                                 github=github)
        return status, json.loads(out.read_text()), stdout.getvalue()


class LedgerTest(unittest.TestCase):
    def test_an_issue_read_that_fails_still_prints_and_writes_the_ledger(self):
        with mock.patch.object(ledger, "pause"):
            status, data, text = run_report(FakeGitHub(LOGS, broken={"issues", "pulls"}))
        self.assertEqual(status, 2)
        self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt",
                      [f["key"] for f in data["failures"]])
        self.assertTrue(any("issues: HTTP 503" in p for p in data["problems"]), data["problems"])
        # Unknown, not "none": a failed read is no evidence that no issue exists.
        self.assertTrue(all(f["issues"] is None for f in data["failures"]))
        self.assertIn("unknown", text)
        self.assertNotIn("**none**", text)

    def test_a_cancelled_jobs_failed_tests_are_counted(self):
        # A Test step that failed and printed its output, then a newer push
        # cancelled the job in a later step: the cancel is the job's result,
        # the failure is still in its log.
        github = FakeGitHub({**LOGS, 120290002840: "judge-test-failure.log"})
        _, data, _ = run_report(github)
        failure = next(f for f in data["failures"]
                       if f["key"] == "test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient")
        self.assertEqual(failure["occurrences"][0]["job_id"], 120290002840)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_failures_a_green_rerun_passed_over_are_counted_and_marked(self):
        status, data, text = run_report(FakeGitHub(LOGS))
        self.assertEqual(status, 0, text)
        failures = {f["key"]: f for f in data["failures"]}
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual((dispatch["count"], dispatch["hidden_by_rerun"], dispatch["by_platform"]),
                         (1, 1, {"macos": 1}))
        self.assertEqual(dispatch["occurrences"][0]["attempt"], 2)
        leak = failures["leak: internal/supervisor mdworker_shared"]
        self.assertEqual((leak["hidden_by_rerun"], leak["occurrences"][0]["attempt"]), (1, 1))
        self.assertEqual([i["number"] for i in leak["issues"]["closed"]], [551])
        self.assertEqual([i["number"] for i in dispatch["issues"]["closed"]], [552])
        timeout = failures["job-timeout: check (macos-latest) exceeded timeout-minutes"]
        self.assertEqual(timeout["hidden_by_rerun"], 1)
        self.assertEqual([i["number"] for i in timeout["issues"]["open"]], [559])
        self.assertEqual(len(failures), 3)

    def test_one_run_is_read_alone_with_its_finished_jobs_counted(self):
        status, data, text = run_report(FakeGitHub(LOGS), "--run", "39905430594")
        self.assertEqual(status, 0, text)
        self.assertEqual((data["runs_scanned"], data["jobs_read"], data["failed_jobs"]), (1, 9, 2))
        self.assertIn("9 finished job(s), 2 failed job(s)", text)

    def test_one_run_with_a_cancelled_job_is_incomplete(self):
        # Run 39916130926's two check jobs were cancelled; read as one run,
        # its summary cannot pass, whatever the cancel's reason.
        status, data, _ = run_report(FakeGitHub(LOGS), "--run", "39916130926")
        self.assertEqual(status, 2)
        self.assertEqual(data["failures"], [])
        status, _, _ = run_report(FakeGitHub(LOGS))
        self.assertEqual(status, 0, "over many runs a superseded job is only counted")

    def test_a_rerun_past_a_failure_needs_an_open_issue_for_it(self):
        # The recorded issues name both of run 39905430594's earlier failures,
        # but #551 and #552 are closed, so neither has an open issue.
        status, data, text = run_report(FakeGitHub(LOGS), "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 4)
        self.assertEqual([(r["attempt"], r["key"]) for r in data["rerun_without_issue"]], [
            (1, "leak: internal/supervisor mdworker_shared"),
            (2, "test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt")])
        self.assertIn("## Re-run past with no open issue", text)

    def test_a_rerun_check_without_the_issues_does_not_pass(self):
        with mock.patch.object(ledger, "pause"):
            status, _, _ = run_report(FakeGitHub(LOGS, broken={"issues"}), "--run", "39905430594",
                                      "--require-open-issues")
        self.assertEqual(status, 2)

    def test_an_open_issue_lets_a_rerun_pass(self):
        github = FakeGitHub(LOGS)
        for issue in github.data["issues"]:
            issue["state"], issue["closed_at"] = "open", None
        status, data, _ = run_report(github, "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual(data["rerun_without_issue"], [])

    def test_an_earlier_attempts_cancel_does_not_make_a_run_incomplete(self):
        # Run 39932040317's attempt 1 timed out (a failure, with #559 open);
        # attempt 2 finished every job.
        status, data, _ = run_report(FakeGitHub(LOGS), "--run", "39932040317", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual([f["key"] for f in data["failures"]],
                         ["job-timeout: check (macos-latest) exceeded timeout-minutes"])

    def test_a_running_attempts_jobs_are_not_cached(self):
        with tempfile.TemporaryDirectory() as folder:
            github = FakeGitHub(LOGS)
            run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
            run["status"], run["conclusion"] = "in_progress", None
            with contextlib.redirect_stdout(io.StringIO()):
                ledger.main(["--cache", folder, "--run", "39905430594"], github=github)
            cached = sorted(p.name.split("-" + ledger.SCRIPT_DIGEST)[0] for p in Path(folder).iterdir())
        self.assertIn("attempt-39905430594-1", cached)
        self.assertNotIn("attempt-39905430594-3", cached)

    def test_a_run_superseded_by_a_newer_one_is_counted_but_not_a_failure(self):
        _, data, _ = run_report(FakeGitHub(LOGS))
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 2, "cancelled": 0})
        self.assertEqual(data["failed_jobs"], 3)

    def test_every_attempt_of_a_rerun_is_reported(self):
        _, data, text = run_report(FakeGitHub(LOGS))
        rerun = next(r for r in data["reruns"] if r["run_id"] == 39905430594)
        self.assertEqual([a["conclusion"] for a in rerun["attempts"]], ["failure", "failure", "success"])
        self.assertEqual([(f["attempt"], f["key"]) for f in rerun["earlier_failures"]], [
            (1, "leak: internal/supervisor mdworker_shared"),
            (2, "test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt")])
        self.assertIn("hidden: the run ended green", text)

    def test_an_unreadable_log_is_reported_and_fails_the_ledger(self):
        status, data, text = run_report(FakeGitHub(LOGS, unreadable={120265317855}))
        self.assertEqual(status, 2)
        self.assertIn("HTTP 410", text)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120265317855])

    def test_a_server_error_is_read_again_and_a_lasting_one_is_still_loud(self):
        with mock.patch.object(ledger, "pause"):
            status, data, _ = run_report(FakeGitHub(LOGS, flaky={120265317855: 2}))
            self.assertEqual(status, 0)
            failures = [f["key"] for f in data["failures"]]
            self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt", failures)
            status, data, _ = run_report(FakeGitHub(LOGS, flaky={120265317855: 3}))
            self.assertEqual(status, 2)
            self.assertIn("HTTP 503", data["problems"][0])

    def test_a_failed_test_step_that_names_nothing_fails_the_ledger(self):
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "empty-group.log"
            log.write_text("\n".join(framed(["FAIL"])))
            github = FakeGitHub({**LOGS, 120256178246: log})
            status, data, _ = run_report(github)
        self.assertEqual(status, 3)
        self.assertEqual([u["reason"] for u in data["unattributed"]], ["tests ran but no failure was named"])
        failures = {f["key"] for f in data["failures"]}
        self.assertIn("step: check (macos-latest) step Test", failures)

    def test_the_cache_reads_a_finished_job_once(self):
        with tempfile.TemporaryDirectory() as folder:
            first, second = FakeGitHub(LOGS), FakeGitHub(LOGS)
            for github in (first, second):
                with contextlib.redirect_stdout(io.StringIO()):
                    ledger.main(["--cache", folder], github=github)
        self.assertEqual(sorted(first.read), sorted(LOGS))
        self.assertEqual(second.read, [])


class ClosedIssueTest(unittest.TestCase):
    def dispatch(self, branch, created_at):
        github = FakeGitHub(LOGS)
        run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
        run["head_branch"], run["created_at"] = branch, created_at
        _, data, text = run_report(github)
        entry = next(f for f in data["failures"] if f["name"] == "TestConcurrentDispatchOfOneTaskClaimsOneAttempt")
        return entry["failed_on_main_after_close"], text

    def test_a_failure_on_main_after_its_issue_closed_is_flagged(self):
        # #552 closed 2026-10-23T09:27:38Z.
        flagged, text = self.dispatch("main", "2026-10-23T10:00:00Z")
        self.assertEqual(flagged, [552])
        self.assertIn("~~#552~~ **failed on main since it closed**", text)

    def test_a_failure_before_the_close_or_off_main_is_not(self):
        self.assertEqual(self.dispatch("main", "2026-10-23T05:22:50Z")[0], [])
        self.assertEqual(self.dispatch("fix/age-441-handoff-session", "2026-10-23T10:00:00Z")[0], [])


class MatchTest(unittest.TestCase):
    def test_a_test_name_matches_only_as_a_whole_word(self):
        find = ledger.matcher([{"number": 1, "state": "open", "title": "TestFooBar flakes", "body": ""}])
        self.assertEqual([i["number"] for i in find("TestFooBar")["open"]], [1])
        self.assertEqual(find("TestFoo")["open"], [])

    def test_a_tempdir_token_matches_the_subtest_it_came_from(self):
        issue = {"number": 563, "state": "open", "body": "",
                 "title": "service: TestAHeldProcessEndsWithItsTestOnEveryPath/timed_out can leave its child"}
        find = ledger.matcher([issue])
        self.assertEqual([i["number"] for i in find("TestAHeldProcessEndsWithItsTestOnEveryPathtimed_out", True)["open"]],
                         [563])
        self.assertEqual(find("TestAHeldProcessEndsWithItsTestOnEveryPathtimed_out")["open"], [])


if __name__ == "__main__":
    unittest.main()
