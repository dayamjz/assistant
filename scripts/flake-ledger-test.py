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
import signal
import subprocess
import sys
import tempfile
import threading
import time
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
        self.pull_list = self.data["pulls"]  # pull request objects, as the pulls API returns them

    def runs(self, workflow, limit, branch=None, since=None):
        return self.data["runs"][:limit]

    def run(self, run_id):
        return next(r for r in self.data["runs"] if r["id"] == run_id)

    def attempt(self, run_id, attempt):
        return {"conclusion": self.data["attempts"][f"{run_id}/{attempt}"]["conclusion"]}

    def attempt_jobs(self, run_id, attempt):
        return self.data["attempts"][f"{run_id}/{attempt}"]["jobs"]

    def annotations(self, job_id):
        if "annotations" in self.broken:
            raise ledger.GitHubError(f"gh api repos/dayamjz/assistant/check-runs/{job_id}/annotations: HTTP 502")
        return self.data["annotations"][str(job_id)]

    def issues(self):
        if "issues" in self.broken:
            raise ledger.GitHubError("gh api repos/dayamjz/assistant/issues: HTTP 503")
        return self.data["issues"]

    def pulls(self):
        if "pulls" in self.broken:
            raise ledger.GitHubError("gh api repos/dayamjz/assistant/pulls: HTTP 503")
        return self.pull_list

    def job_log(self, job_id):
        self.read.append(job_id)
        jobs = [j for a in self.data["attempts"].values() for j in a["jobs"] if j["id"] == job_id]
        if jobs and not jobs[0].get("steps"):
            # As GitHub answers for a job cancelled before a runner took it
            # (jobs 120513403886 and 120517498243): no steps, so no log.
            raise ledger.GitHubError(f"log of job {job_id}: gh: HTTP 404")
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


def log_lines(name, first=1, last=None):
    """Lines first..last (1-based, inclusive) of a recorded log, verbatim."""
    lines = (DATA / name).read_text(encoding="utf-8").split("\n")
    return lines[first - 1:last]


def set_steps(github, job_id, conclusions):
    """Gives the recorded job's named steps these conclusions."""
    job = next(j for a in github.data["attempts"].values() for j in a["jobs"] if j["id"] == job_id)
    for step in job["steps"]:
        step["conclusion"] = conclusions.get(step["name"], step["conclusion"])
    return job


def stopped_after_attempt_2(github):
    """The recorded three-attempt run as it stood before attempt 3."""
    run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
    run["run_attempt"], run["conclusion"] = 2, "failure"
    del github.data["attempts"]["39905430594/3"]
    return github


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

    def rerun_check_failed_in_attempt_2(self, folder):
        # Run 39905430594 with its attempt 2 holding a failed re-runs job, as
        # ci.yml's job fails: the step that runs this check exits 4 after
        # printing the ledger's own ::error:: line.
        stamp = "2026-10-23T06:20:01.0000000Z"
        log = Path(folder) / "rerun-check-failed.log"
        log.write_text("\n".join([
            f"{stamp} ##[group]Run set -o pipefail",
            f"{stamp} ##[endgroup]",
            f"{stamp} ##[error]run 39905430594 attempt 1 (check (macos-latest)) failed leak: internal/supervisor "
            "mdworker_shared, and no open issue names it; file one containing mdworker_shared before re-running past it",
            f"{stamp} ##[error]Process completed with exit code 4."]) + "\n")
        github = FakeGitHub({**LOGS, 99000000001: log})
        github.data["attempts"]["39905430594/2"]["jobs"].append({
            "id": 99000000001, "run_attempt": 2, "name": "re-runs", "status": "completed", "conclusion": "failure",
            "started_at": "2026-10-23T06:19:50Z", "completed_at": "2026-10-23T06:20:02Z", "labels": ["ubuntu-latest"],
            "html_url": "https://github.com/dayamjz/assistant/actions/runs/39905430594/job/99000000001",
            "steps": [{"name": "Set up job", "conclusion": "success"},
                      {"name": "Run actions/checkout@v4", "conclusion": "success"},
                      {"name": "Every earlier attempt's failure has an open issue", "conclusion": "failure"},
                      {"name": "Complete job", "conclusion": "success"}]})
        # The issue the check asked for is filed: every earlier failure is covered.
        for issue in github.data["issues"]:
            issue["state"], issue["closed_at"] = "open", None
        return github

    def test_the_rerun_checks_own_earlier_failure_needs_no_issue(self):
        with tempfile.TemporaryDirectory() as folder:
            github = self.rerun_check_failed_in_attempt_2(folder)
            status, data, text = run_report(github, "--run", "39905430594", "--require-open-issues",
                                            "--own-job", "re-runs")
        self.assertEqual(status, 0, text)
        self.assertEqual(data["rerun_without_issue"], [])

    def test_another_jobs_earlier_failure_still_needs_an_issue(self):
        # The positive control: --own-job sets aside only that job.
        with tempfile.TemporaryDirectory() as folder:
            github = self.rerun_check_failed_in_attempt_2(folder)
            for issue in github.data["issues"]:
                if issue["number"] == 552:
                    issue["state"] = "closed"
            status, data, _ = run_report(github, "--run", "39905430594", "--require-open-issues",
                                         "--own-job", "re-runs")
        self.assertEqual(status, 4)
        self.assertEqual([r["key"] for r in data["rerun_without_issue"]],
                         ["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"])

    def test_an_earlier_attempts_cancel_does_not_make_a_run_incomplete(self):
        # Run 39932040317's attempt 1 timed out (a failure, with #559 open);
        # attempt 2 finished every job.
        status, data, _ = run_report(FakeGitHub(LOGS), "--run", "39932040317", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual([f["key"] for f in data["failures"]],
                         ["job-timeout: check (macos-latest) exceeded timeout-minutes"])

    def test_one_run_with_a_job_that_timed_out_is_incomplete(self):
        # Run 39932040317 as it stood after attempt 1: its macOS job ran into
        # timeout-minutes. The timeout is a failure record, and the suite
        # never finished, so the run's summary cannot pass.
        github = FakeGitHub(LOGS)
        run = github.run(39932040317)
        run["run_attempt"], run["conclusion"] = 1, "cancelled"
        del github.data["attempts"]["39932040317/2"]
        status, data, _ = run_report(github, "--run", "39932040317")
        self.assertEqual(status, 2)
        self.assertEqual([(c["job_id"], c["reason"]) for c in data["cancelled_jobs"]], [(120342424178, "timeout")])
        self.assertIn("job-timeout: check (macos-latest) exceeded timeout-minutes", [f["key"] for f in data["failures"]])

    def test_one_run_with_a_job_cancelled_after_a_failure_is_incomplete(self):
        # Run 39916130926 with its macOS job's log holding the recorded judge
        # failure, and its ubuntu job finished: the cancel still cut the
        # macOS suite short.
        github = FakeGitHub({**LOGS, 120290002840: "judge-test-failure.log"})
        set_steps(github, 120290002917, {})["conclusion"] = "success"
        status, data, _ = run_report(github, "--run", "39916130926")
        self.assertEqual(status, 2)
        self.assertEqual([(c["job_id"], c["reason"]) for c in data["cancelled_jobs"]], [(120290002840, "superseded")])

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
        self.assertIn("hidden: the final attempt does not show it", text)

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

    def test_a_failure_the_final_attempt_does_not_show_is_hidden_though_the_run_failed(self):
        # The recorded run, stopped after attempt 2: the supervisor leak of
        # attempt 1 is gone from attempt 2, the run's face, which fails
        # internal/service instead.
        _, data, text = run_report(stopped_after_attempt_2(FakeGitHub(LOGS)))
        failures = {f["key"]: f for f in data["failures"]}
        self.assertEqual(failures["leak: internal/supervisor mdworker_shared"]["hidden_by_rerun"], 1)
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual(dispatch["hidden_by_rerun"], 0)
        self.assertIn("attempt 1, check (macos-latest): leak: internal/supervisor mdworker_shared "
                      "(hidden: the final attempt does not show it)", text)

    def test_a_failure_the_final_attempt_shows_again_is_not_hidden(self):
        github = stopped_after_attempt_2(FakeGitHub({**LOGS, 120265317855: "rerun-attempt-1.log"}))
        _, data, _ = run_report(github)
        leak = next(f for f in data["failures"] if f["key"] == "leak: internal/supervisor mdworker_shared")
        self.assertEqual((leak["count"], leak["hidden_by_rerun"]), (2, 0))

    def test_each_failure_is_timed_by_the_job_it_failed_in(self):
        # Attempt 2's job ended at 06:18:38Z, nearly an hour after the run
        # was created (05:22:50Z).
        _, data, _ = run_report(FakeGitHub(LOGS))
        failures = {f["key"]: f for f in data["failures"]}
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual((dispatch["first_seen"], dispatch["last_seen"]),
                         ("2026-10-23T06:18:38Z", "2026-10-23T06:18:38Z"))
        leak = failures["leak: internal/supervisor mdworker_shared"]
        self.assertEqual(leak["last_seen"], "2026-10-23T05:52:16Z")

    def test_a_step_that_failed_before_a_cancel_is_counted(self):
        # The recorded superseded job, as the runner would report it had
        # Verify formatting failed: the steps after it skip, the always()
        # step is cancelled, and the cancel becomes the job's conclusion.
        github = FakeGitHub(LOGS)
        job = next(j for a in github.data["attempts"].values() for j in a["jobs"] if j["id"] == 120290002840)
        after = False
        for step in job["steps"]:
            if step["name"] == "Verify formatting":
                step["conclusion"], after = "failure", True
            elif after and step["name"] == "Skipped tests":
                step["conclusion"] = "cancelled"
            elif after and step["name"] not in ("Post Run actions/checkout@v4", "Complete job"):
                step["conclusion"] = "skipped"
        stamp = "2026-10-23T07:12:01.0000000Z"
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "gofmt-then-cancel.log"
            log.write_text("\n".join([
                f"{stamp} ##[group]Run unformatted=$(gofmt -l .)", f"{stamp} ##[endgroup]",
                f"{stamp} These files are not gofmt-clean:", f"{stamp} internal/graph/graph.go",
                f"{stamp} ##[error]Process completed with exit code 1.",
                "2026-10-23T07:19:57.7514310Z ##[error]The operation was canceled."]) + "\n")
            github.logs = {**LOGS, 120290002840: log}
            status, data, _ = run_report(github)
        self.assertEqual(status, 0)
        failures = {f["key"]: f for f in data["failures"]}
        step = failures["step: check (macos-latest) step Verify formatting"]
        self.assertEqual([o["job_id"] for o in step["occurrences"]], [120290002840])
        self.assertNotIn("The operation was canceled.", step["details"])
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_the_headline_counts_every_job_whose_failures_are_counted(self):
        github = FakeGitHub({**LOGS, 120290002840: "judge-test-failure.log"})
        _, data, text = run_report(github)
        jobs = {o["job_id"] for f in data["failures"] for o in f["occurrences"]}
        self.assertEqual(len(jobs), 4)
        self.assertEqual(data["failed_jobs"], 4)
        self.assertIn("4 failed job(s)", text)

    def test_a_cancel_with_no_reason_recorded_is_not_said_to_be_by_hand(self):
        # No annotation and no steps: cancelled before a runner took it.
        github = FakeGitHub(LOGS)
        github.data["annotations"]["120290002840"] = []
        job = next(j for a in github.data["attempts"].values() for j in a["jobs"] if j["id"] == 120290002840)
        job["steps"] = []
        status, data, text = run_report(github)
        self.assertEqual(status, 0, text)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 1})
        self.assertIn("1 cancelled for another reason (by hand, or before it started).", text)
        self.assertNotIn("by hand.", text)

    def test_a_job_whose_annotations_cannot_be_read_is_not_said_to_have_an_unreadable_log(self):
        with mock.patch.object(ledger, "pause"):
            status, data, _ = run_report(FakeGitHub(LOGS, broken={"annotations"}))
        self.assertEqual(status, 2)
        reasons = {u["job_id"]: u["reason"] for u in data["unattributed"]}
        self.assertEqual(sorted(reasons), [120290002840, 120290002917, 120342424178])
        for reason in reasons.values():
            self.assertTrue(reason.startswith("not read: gh api repos/dayamjz/assistant/check-runs/"), reason)

    def test_a_run_is_matched_to_its_own_pull_request_not_a_forks_of_the_same_branch_name(self):
        github = FakeGitHub(LOGS)
        run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
        run["head_branch"], run["head_repository"] = "main", {"full_name": "someone/assistant"}
        github.pull_list = [
            {"number": 600, "head": {"ref": "main", "repo": {"full_name": "other/assistant"}}},
            {"number": 601, "head": {"ref": "main", "repo": {"full_name": "someone/assistant"}}},
            {"number": 602, "head": {"ref": "main", "repo": None}},  # a deleted fork
        ]
        _, data, _ = run_report(github)
        prs = {o["pr"] for f in data["failures"] for o in f["occurrences"] if o["run_id"] == 39905430594}
        self.assertEqual(prs, {601})

    def two_failed_steps(self, folder, failed_second=True):
        # The recorded judge failure's job through its Test step (verbatim,
        # ending in that step's exit line), then its Skipped tests step,
        # which runs always(), failing too.
        stamp = "2026-10-23T13:54:03.2321910Z"
        lines = log_lines("judge-test-failure.log", 1, 137) + [
            f"{stamp} No test skipped on macos-latest.",
            f"{stamp} ##[error]grep: test.log: Input/output error"]
        if failed_second:
            lines.append(f"{stamp} ##[error]Process completed with exit code 2.")
        log = Path(folder) / "two-failed-steps.log"
        log.write_text("\n".join(lines) + "\n")
        github = FakeGitHub({**LOGS, 120256178246: log})
        set_steps(github, 120256178246, {"Test": "failure", "Skipped tests": "failure"})
        return github

    def test_every_failed_step_counts_though_another_named_a_failure(self):
        with tempfile.TemporaryDirectory() as folder:
            status, data, text = run_report(self.two_failed_steps(folder))
        self.assertEqual(status, 0, text)
        failures = {f["key"]: f for f in data["failures"]}
        attempt_1 = {k for k, f in failures.items() if any(o["job_id"] == 120256178246 for o in f["occurrences"])}
        self.assertIn("step: check (macos-latest) step Skipped tests", attempt_1)
        self.assertIn("test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient", attempt_1)
        # The Test step's failure is the named test, not a step row too.
        self.assertNotIn("step: check (macos-latest) step Test", attempt_1)
        self.assertEqual(failures["step: check (macos-latest) step Skipped tests"]["details"],
                         ["grep: test.log: Input/output error"])

    def test_failed_steps_that_cannot_be_told_apart_are_all_counted(self):
        # Two failed steps, one exit line: which step printed the named test
        # is unknown, so both steps are counted rather than one assumed.
        with tempfile.TemporaryDirectory() as folder:
            _, data, _ = run_report(self.two_failed_steps(folder, failed_second=False))
        keys = {f["key"] for f in data["failures"]
                if any(o["job_id"] == 120256178246 for o in f["occurrences"])}
        self.assertTrue({"step: check (macos-latest) step Test", "step: check (macos-latest) step Skipped tests",
                         "test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient"} <= keys, keys)

    def test_a_test_whose_package_never_reported_is_counted(self):
        # The superseded job, cancelled while cat test.log printed: one
        # complete failing package (the recorded judge failure), then
        # another package's failing test (the recorded dispatch failure),
        # and the cancel before that package's summary line.
        lines = (log_lines("judge-test-failure.log", 1, 109)
                 + log_lines("rerun-attempt-2.log", 116, 120)
                 + ["2026-10-23T07:19:57.7514310Z ##[error]The operation was canceled."])
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "cut-off.log"
            log.write_text("\n".join(lines) + "\n")
            github = FakeGitHub({**LOGS, 120290002840: log})
            status, data, text = run_report(github)
        self.assertEqual(status, 0, text)
        keys = {f["key"] for f in data["failures"]
                if any(o["job_id"] == 120290002840 for o in f["occurrences"])}
        self.assertEqual(keys, {"test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient",
                                "test: (output cut off) TestConcurrentDispatchOfOneTaskClaimsOneAttempt"})

    def pr_of(self, created_at, named=None):
        # The recorded run, moved onto the branch #154 and then #257 used.
        github = FakeGitHub(LOGS)
        run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
        run["head_branch"], run["created_at"] = "claude/prd-owner-decisions", created_at
        if named:
            run["pull_requests"] = [{"number": named}]
        _, data, _ = run_report(github)
        return {o["pr"] for f in data["failures"] for o in f["occurrences"] if o["run_id"] == 39905430594}

    def test_a_reused_branchs_run_is_its_own_pull_requests(self):
        # 38522563333 ran at 06:30:04 on 10-14, while #154 was open.
        self.assertEqual(self.pr_of("2026-10-14T06:30:04Z"), {154})
        self.assertEqual(self.pr_of("2026-10-15T14:33:28Z"), {257})
        # Between the two, neither was open: unknown, not a guess.
        self.assertEqual(self.pr_of("2026-10-15T00:00:00Z"), {None})

    def test_the_pull_request_a_run_names_is_its_own(self):
        self.assertEqual(self.pr_of("2026-10-15T14:33:28Z", named=154), {154})

    def test_a_cancelled_job_whose_tests_name_nothing_fails_the_ledger(self):
        # The superseded job's Test step printed its output and named no
        # failure: a cancel later in the job does not make that complete.
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "empty-group.log"
            log.write_text("\n".join(framed(["FAIL"])))
            github = FakeGitHub({**LOGS, 120290002840: log})
            status, data, _ = run_report(github)
        self.assertEqual(status, 3)
        self.assertEqual([(u["job_id"], u["reason"]) for u in data["unattributed"]],
                         [(120290002840, "tests ran but no failure was named")])
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_a_job_cancelled_before_it_started_is_set_aside_without_a_log(self):
        github = FakeGitHub(LOGS)
        for attempt in github.data["attempts"].values():
            for job in attempt["jobs"]:
                if job["id"] == 120290002840:
                    job["steps"] = []
        status, data, text = run_report(github)
        self.assertEqual(status, 0, text)
        self.assertNotIn(120290002840, github.read)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 2, "cancelled": 0})

    def test_a_timeout_known_from_its_annotation_survives_an_unreadable_log(self):
        status, data, text = run_report(FakeGitHub(LOGS, unreadable={120342424178}))
        self.assertEqual(status, 2)
        self.assertIn("HTTP 410", text)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120342424178])
        timeout = next(f for f in data["failures"]
                       if f["key"] == "job-timeout: check (macos-latest) exceeded timeout-minutes")
        self.assertEqual([o["job_id"] for o in timeout["occurrences"]], [120342424178])

    def test_an_unreadable_log_is_not_cached(self):
        # A timed-out job's partial result is not its answer: the next run
        # reads its log again.
        with tempfile.TemporaryDirectory() as folder:
            first, second = FakeGitHub(LOGS, unreadable={120342424178}), FakeGitHub(LOGS)
            statuses = []
            for github in (first, second):
                with contextlib.redirect_stdout(io.StringIO()):
                    statuses.append(ledger.main(["--cache", folder], github=github))
        self.assertEqual(statuses, [2, 0])
        self.assertEqual(second.read, [120342424178])

    def test_the_cache_reads_a_finished_job_once(self):
        with tempfile.TemporaryDirectory() as folder:
            first, second = FakeGitHub(LOGS), FakeGitHub(LOGS)
            for github in (first, second):
                with contextlib.redirect_stdout(io.StringIO()):
                    ledger.main(["--cache", folder], github=github)
        self.assertEqual(sorted(first.read), sorted(LOGS))
        self.assertEqual(second.read, [])


FAKE_GH = """#!/bin/sh
# Blocks like a gh whose server never answers, with a child of its own
# that holds none of gh's pipes, so only ending the group ends it.
echo $$ >> "$FAKE_GH_PIDS"
sleep 600 </dev/null >/dev/null 2>&1 &
echo $! >> "$FAKE_GH_PIDS"
echo "a first line, then nothing"
wait
"""


# A gh that answers the API from the recorded responses, as GitHub would,
# and blocks on a job log with a child of its own, as a stalled download.
FAKE_GH_API = """
import json, os, re, subprocess, sys, time
args = [a for a in sys.argv[1:] if a not in ("-H", "Accept: application/vnd.github+json")]
if args[:2] == ["api", "--help"]:
    print("--allow-escape-sequences"); sys.exit(0)
data = json.load(open(os.environ["FAKE_GH_DATA"]))
path = args[-1]
if path.endswith("/logs"):
    with open(os.environ["FAKE_GH_PIDS"], "a") as pids:
        child = subprocess.Popen(["sleep", "600"], stdin=subprocess.DEVNULL,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        pids.write(f"{os.getpid()}\\n{child.pid}\\n")
    time.sleep(600)
elif "/workflows/" in path:
    page = int(re.search(r"[?&]page=(\\d+)", path).group(1))
    print(json.dumps({"workflow_runs": data["runs"] if page == 1 else []}))
elif m := re.search(r"runs/(\\d+)/attempts/(\\d+)/jobs", path):
    print(json.dumps([{"jobs": data["attempts"][f"{m[1]}/{m[2]}"]["jobs"]}]))
elif m := re.search(r"runs/(\\d+)/attempts/(\\d+)", path):
    print(json.dumps(data["attempts"][f"{m[1]}/{m[2]}"]))
elif m := re.search(r"check-runs/(\\d+)/annotations", path):
    print(json.dumps(data["annotations"][m[1]]))
else:
    sys.stderr.write(f"fake gh: no answer for {path}\\n"); sys.exit(1)
"""


class GhProcessTest(unittest.TestCase):
    """P15: every gh the ledger starts runs under a deadline and ends, with
    what it started, when the ledger ends. The gh here is a real program
    that blocks and starts a child, as a hung gh would."""

    def setUp(self):
        folder = tempfile.mkdtemp(prefix="flake-ledger-gh-")
        self.addCleanup(shutil.rmtree, folder, ignore_errors=True)
        self.pids = Path(folder) / "pids"
        gh = Path(folder) / "gh"
        gh.write_text(FAKE_GH)
        gh.chmod(0o755)
        self.env = {**os.environ, "PATH": f"{folder}{os.pathsep}{os.environ['PATH']}",
                    "FAKE_GH_PIDS": str(self.pids)}
        self.addCleanup(self.end_leftovers)

    def started(self, count=2):
        lines = self.pids.read_text().split() if self.pids.exists() else []
        return [int(p) for p in lines] if len(lines) >= count else None

    def end_leftovers(self):
        for pid in self.started(1) or []:
            with contextlib.suppress(ProcessLookupError):
                os.kill(pid, signal.SIGKILL)

    def assert_ended(self, pids, within=10):
        deadline = time.monotonic() + within
        alive = list(pids)
        while alive and time.monotonic() < deadline:
            alive = [p for p in alive if alive_pid(p)]
            time.sleep(0.1)
        self.assertEqual(alive, [], "gh or its child outlived its bound")

    def wait_for_gh(self, ledger_process, count=2):
        deadline = time.monotonic() + 30
        while self.started(count) is None:
            self.assertIsNone(ledger_process.poll(), "the ledger exited before starting gh")
            self.assertLess(time.monotonic(), deadline, "gh never started")
            time.sleep(0.1)
        return self.started(count)

    def test_a_gh_call_past_its_deadline_is_ended_with_its_child(self):
        raised = []

        def call():
            try:
                ledger.GitHub("dayamjz/assistant").api("repos/dayamjz/assistant/issues")
            except ledger.GitHubError as error:
                raised.append(error)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(ledger, "API_DEADLINE", 1), \
                mock.patch.object(ledger, "pause"):
            # On a thread, so a call that never ends fails the test instead
            # of hanging it; ending the fake gh then lets the thread finish.
            caller = threading.Thread(target=call)
            caller.start()
            caller.join(20)
            overran = caller.is_alive()
            if overran:
                self.end_leftovers()
                caller.join(20)
        self.assertFalse(overran, "a gh call outran its deadline")
        self.assertRegex(str(raised[0]), "timed out after 1s")
        pids = self.started()
        self.assertEqual(len(pids), 6, "three tries, each a gh and its child")
        self.assert_ended(pids)

    def test_a_log_read_stopped_early_ends_its_gh_at_once(self):
        github = ledger.GitHub("dayamjz/assistant")
        github._escape_flag = False
        # A short deadline keeps a regression from hanging the test; ending at
        # once is well inside it.
        with mock.patch.dict(os.environ, self.env), mock.patch.object(ledger, "LOG_DEADLINE", 8):
            lines = github.job_log(1)
            self.assertEqual(next(lines), "a first line, then nothing\n")
            begun = time.monotonic()
            lines.close()
        self.assertLess(time.monotonic() - begun, 3, "closing the read waited for gh's deadline")
        self.assert_ended(self.started())

    def ledger(self):
        process = subprocess.Popen([sys.executable, str(SCRIPT), "--repo", "dayamjz/assistant", "--no-cache"],
                                   env=self.env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(lambda: process.poll() is None and (process.kill(), process.wait()))
        return process

    def interrupted_promptly(self, process, pids):
        process.send_signal(signal.SIGINT)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.fail("an interrupted ledger waited for its gh's deadline")
        self.assertEqual(process.returncode, 130)
        self.assert_ended(pids, within=5)

    def test_an_interrupted_ledger_ends_its_gh_at_once(self):
        # Ctrl+C while the run listing blocks, with the real 120 s deadline.
        process = self.ledger()
        self.interrupted_promptly(process, self.wait_for_gh(process))

    def test_an_interrupted_ledger_ends_the_log_reads_its_workers_hold(self):
        gh = Path(self.env["PATH"].split(os.pathsep)[0]) / "gh"
        gh.write_text(f"#!{sys.executable}\n" + FAKE_GH_API)
        self.env["FAKE_GH_DATA"] = str(DATA / "api.json")
        process = self.ledger()
        pids = self.wait_for_gh(process, count=4)  # two log reads on worker threads, each with a child
        self.interrupted_promptly(process, pids)

    def test_a_terminated_ledger_ends_its_gh_and_what_gh_started(self):
        process = self.ledger()
        pids = self.wait_for_gh(process)
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=10)
        self.assert_ended(pids)

    def test_a_killed_ledger_ends_its_gh_and_what_gh_started(self):
        process = self.ledger()
        pids = self.wait_for_gh(process)
        process.kill()
        process.wait(timeout=10)
        self.assert_ended(pids)


def alive_pid(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


class ClosedIssueTest(unittest.TestCase):
    def dispatch(self, branch, completed_at=None, closed_at=None, event=None, head_repository=None, repo=None):
        # The failure is attempt 2's, in job 120265317855; moving when that
        # job ended moves when it failed. The run itself was created earlier.
        # A run of main is a push; a branch's run is its pull request's.
        github = FakeGitHub(LOGS)
        github.repo = repo or github.repo
        run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
        run["head_branch"] = branch
        run["event"] = event or ("push" if branch == "main" else "pull_request")
        if head_repository:
            run["head_repository"] = {"full_name": head_repository}
        if completed_at:
            job = next(j for j in github.data["attempts"]["39905430594/2"]["jobs"] if j["id"] == 120265317855)
            job["completed_at"] = completed_at
        if closed_at:
            next(i for i in github.data["issues"] if i["number"] == 552)["closed_at"] = closed_at
        _, data, text = run_report(github)
        entry = next(f for f in data["failures"] if f["name"] == "TestConcurrentDispatchOfOneTaskClaimsOneAttempt")
        self.entry = entry
        return entry["failed_on_main_after_close"], text

    def test_a_failure_on_main_after_its_issue_closed_is_flagged(self):
        # #552 closed 2026-10-23T09:27:38Z.
        flagged, text = self.dispatch("main", "2026-10-23T10:00:00Z")
        self.assertEqual(flagged, [552])
        self.assertIn("~~#552~~ **failed on main since it closed**", text)

    def test_a_rerun_failure_after_the_close_of_a_run_created_before_it_is_flagged(self):
        # The run was created at 05:22:50Z; its attempt 2 failed at
        # 06:18:38Z, after an issue closed at 06:00Z.
        self.assertEqual(self.dispatch("main", closed_at="2026-10-23T06:00:00Z")[0], [552])

    def test_a_forks_main_is_not_this_repositorys_main(self):
        # A pull request from a fork whose branch is named main.
        flagged, text = self.dispatch("main", "2026-10-23T10:00:00Z", event="pull_request",
                                      head_repository="someone/assistant")
        self.assertEqual((flagged, self.entry["on_main"]), ([], 0))
        self.assertEqual(self.entry["by_branch"], {"someone:main": 1})
        self.assertIn("someone:main (1)", text)

    def test_a_pull_request_from_this_repositorys_main_is_not_a_run_of_main(self):
        flagged, _ = self.dispatch("main", "2026-10-23T10:00:00Z", event="pull_request")
        self.assertEqual((flagged, self.entry["on_main"]), ([], 0))
        self.assertEqual(self.entry["by_branch"], {"main (pull request)": 1})

    def test_this_repository_named_in_another_case_is_still_its_own(self):
        # GitHub's names are case-insensitive: --repo Dayamjz/Assistant is
        # dayamjz/assistant, which the API's run objects name.
        flagged, _ = self.dispatch("main", "2026-10-23T10:00:00Z", repo="Dayamjz/Assistant")
        self.assertEqual((flagged, self.entry["on_main"]), ([552], 1))

    def test_a_failure_before_the_close_or_off_main_is_not(self):
        self.assertEqual(self.dispatch("main")[0], [])
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
