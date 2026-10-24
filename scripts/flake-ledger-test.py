#!/usr/bin/env python3
"""Tests for the CI flake ledger, flake-ledger.py. They start no process.

The logs under testdata/flake-ledger are real GitHub job logs, trimmed to the
lines that matter with every kept line verbatim, and api.json holds real API
responses trimmed to the fields the ledger reads; GitHubServer serves both
over HTTP in this process, at the addresses GitHub serves them from. The Go
shapes the real logs do not cover (a timeout, a build failure, a panic
outside a test, a package that fails with no test failing, output a test
logs) are in go-test-v.txt: the real output of `go test -v` over the module
in GO_PACKAGES, recorded once. After changing GO_PACKAGES, record it again:

    python3 scripts/flake-ledger-test.py write-go-module DIR
    cd DIR && GOFLAGS=-trimpath GOWORK=off go test -v -count=1 -p 2 -timeout 5s ./... \\
        > scripts/testdata/flake-ledger/go-test-v.txt 2>&1

write-go-module adds a test that logs the module's digest, so the recording
carries the digest only when go test ran over that very module; the tests
look for it, and a module changed without recording again fails them.
"""

import contextlib
import hashlib
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock
import urllib.parse

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "flake-ledger.py"
DATA = HERE / "testdata" / "flake-ledger"
SPEC = importlib.util.spec_from_file_location("flake_ledger", SCRIPT)
ledger = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ledger)


def parse(name):
    with open(DATA / name, encoding="utf-8") as handle:
        records, printed_nothing = ledger.parse_lines(handle)
    return {r.key: r for r in records}, printed_nothing


def framed(go_output, frame="judge-test-failure.log"):
    """go_output as the Test step's full test output group of a real log."""
    lines = (DATA / frame).read_text(encoding="utf-8").split("\n")
    start = next(i for i, l in enumerate(lines) if l.endswith(ledger.GROUP_START))
    end = next(i for i, l in enumerate(lines) if i > start and l.endswith(ledger.GROUP_END))
    stamp = re.match(r"^﻿?(\S+Z) ", lines[start]).group(1)
    return lines[:start + 1] + [f"{stamp} {line}" for line in go_output] + lines[end:]


class RealLogTest(unittest.TestCase):
    def test_a_failing_test_is_named_and_the_self_tests_planted_leaks_are_not(self):
        records, printed_nothing = parse("judge-test-failure.log")
        self.assertFalse(printed_nothing)
        self.assertEqual(sorted(records), ["test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient"])
        self.assertIn("LEAK process pid=", (DATA / "judge-test-failure.log").read_text(),
                      "the frame must carry the self-test's planted leaks for this to prove anything")

    def test_a_per_package_leak_is_counted_once_for_the_job_without_being_read(self):
        records, printed_nothing = parse("per-package-leak.log")
        self.assertFalse(printed_nothing)
        self.assertEqual(sorted(records), [
            "leak: (leak check) leftovers, not read by this version",
            "test: internal/service TestADispatchersTrustedReadIsItsOwn"])
        leak = records["leak: (leak check) leftovers, not read by this version"]
        self.assertTrue(leak.detail.startswith("LEAK package=/Users/runner/work/assistant/assistant/internal/service "),
                        leak.detail)

    def test_a_package_the_leak_check_alone_failed_is_a_package_failure(self):
        records, _ = parse("rerun-attempt-1.log")
        self.assertEqual(sorted(records), [
            "leak: (leak check) leftovers, not read by this version",
            "package: internal/supervisor (package)"])
        self.assertEqual(records["package: internal/supervisor (package)"].detail, "the leak check failed it")

    def test_whole_run_leftovers_are_counted_without_being_read(self):
        for name in ("whole-run-leak-process.log", "whole-run-leak-directories.log"):
            with self.subTest(log=name):
                records, printed_nothing = parse(name)
                self.assertFalse(printed_nothing)
                self.assertIn("leak: (leak check) leftovers, not read by this version", records)


# The module whose real `go test -v` output is go-test-v.txt (see the module
# docstring for how to record it again).
GO_PACKAGES = {
    "go.mod": "module github.com/example/m\n\ngo 1.21\n",
    "pass/pass_test.go": """package pass
import "testing"
func TestPasses(t *testing.T) {}
""",
    "fail/fail_test.go": """package fail
import ("fmt"; "strings"; "testing")
func TestTop(t *testing.T) {
	t.Errorf("the first reason")
}
func TestParent(t *testing.T) {
	t.Run("sub a", func(t *testing.T) { t.Fatal("sub a broke") })
	t.Run("sub b", func(t *testing.T) {})
}
// A test may log another go test's output, failures included, naming this
// package's own passing test. Written through t.Log or t.Output, as is or
// each line trimmed, it is indented, so it is not this package's.
func TestFixture(t *testing.T) {}
func TestLogsAFixturesOutput(t *testing.T) {
	out := "=== RUN   TestFixture\\n--- FAIL: TestFixture (0.00s)\\nFAIL\\tgithub.com/x/y\\t0.1s\\n"
	t.Log(out)
	fmt.Fprint(t.Output(), out)
	for _, line := range strings.Split(out, "\\n") {
		fmt.Fprintln(t.Output(), strings.TrimSpace(line))
	}
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
GO_COMMAND = "GOFLAGS=-trimpath GOWORK=off go test -v -count=1 -p 2 -timeout 5s ./..."
GO_OUTPUT = DATA / "go-test-v.txt"
PROVENANCE = "recorded from GO_PACKAGES "


def go_module_digest():
    return hashlib.sha256(json.dumps([GO_PACKAGES, GO_COMMAND], sort_keys=True).encode()).hexdigest()


def write_go_module(folder):
    """Writes GO_PACKAGES, and a test logging their digest, into folder."""
    root = Path(folder)
    files = {**GO_PACKAGES, "provenance/provenance_test.go": (
        'package provenance\nimport "testing"\n'
        f'func TestRecordedFrom(t *testing.T) {{ t.Log("{PROVENANCE}{go_module_digest()}") }}\n')}
    for name, source in files.items():
        (root / name).parent.mkdir(parents=True, exist_ok=True)
        (root / name).write_text(source)
    print(f"wrote the module to {root}; now run, in it:\n  {GO_COMMAND} > {GO_OUTPUT} 2>&1")


def recording_is_current():
    """Whether go-test-v.txt is go test's output over GO_PACKAGES as they are:
    only that run logs their digest (write_go_module)."""
    return PROVENANCE + go_module_digest() in GO_OUTPUT.read_text(encoding="utf-8")


class RecordedGoOutputTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.output = GO_OUTPUT.read_text(encoding="utf-8").split("\n")

    def records(self, lines, raw=False):
        records, printed_nothing = ledger.parse_lines(lines, raw=raw)
        return {r.key: r for r in records}, printed_nothing

    def test_the_recording_is_of_the_module_as_it_now_is(self):
        self.assertTrue(recording_is_current(),
                        "GO_PACKAGES or GO_COMMAND changed: record go-test-v.txt again (module docstring)")

    def test_a_module_written_but_not_recorded_does_not_pass_for_recorded(self):
        # write-go-module is the first half of recording; until go test has
        # run over what it wrote, the recording is not of that module.
        changed = {**GO_PACKAGES, "unrecorded/unrecorded_test.go":
                   'package unrecorded\nimport "testing"\nfunc TestUnrecorded(t *testing.T) { t.Fail() }\n'}
        with tempfile.TemporaryDirectory() as folder, mock.patch.dict(GO_PACKAGES, changed), \
                contextlib.redirect_stdout(io.StringIO()):
            write_go_module(folder)
            self.assertFalse(recording_is_current())

    def test_each_kind_of_go_failure_is_one_record(self):
        records, printed_nothing = self.records(framed(self.output))
        self.assertFalse(printed_nothing)
        self.assertEqual(sorted(records), [
            "build: build setup failed",
            "package: exits (package)",
            "package: panics (package)",
            "test: fail TestParent",
            "test: fail TestTop",
            "timeout: timeout TestSlow",
        ], "\n".join(self.output))
        self.assertEqual(records["package: panics (package)"].detail, "panic: boom at init")
        self.assertEqual(records["package: exits (package)"].detail, "no failing test was named")
        self.assertEqual(records["timeout: timeout TestSlow"].detail, "test timed out after 5s")
        self.assertEqual(records["test: fail TestTop"].token, "TestTop")

    def test_output_a_test_writes_is_not_read_however_it_is_laid_out(self):
        # The recording holds the logged fixture's --- FAIL line as t.Log and
        # t.Output put it, never at column 0.
        written = [l for l in self.output if "--- FAIL: TestFixture" in l]
        self.assertGreaterEqual(len(written), 3, "\n".join(self.output))
        self.assertTrue(all(l.startswith("    ") for l in written), written)
        records, _ = self.records(framed(self.output))
        self.assertNotIn("test: fail TestFixture", records)

    def test_raw_output_parses_as_the_group_does(self):
        raw, _ = self.records(self.output, raw=True)
        in_group, _ = self.records(framed(self.output))
        self.assertEqual(sorted(raw), sorted(in_group))


class LoudTest(unittest.TestCase):
    def test_a_group_with_nothing_named_in_it_is_printed_nothing(self):
        records, printed_nothing = ledger.parse_lines(framed(["something went wrong", "FAIL"]))
        self.assertEqual(records, [])
        self.assertTrue(printed_nothing)

    def test_leftovers_counted_but_not_listed_are_still_a_record(self):
        lines = framed(["ok  \tgithub.com/example/m/pass\t0.1s", "leak-check: 1 leftover(s); command exit=0"])
        records, printed_nothing = ledger.parse_lines(lines)
        self.assertEqual([r.key for r in records], ["leak: (leak check) leftovers, not read by this version"])
        self.assertFalse(printed_nothing)

    def test_an_error_the_test_step_reports_is_a_record_beside_the_tests_it_named(self):
        # A log from before ci-test.sh (its Test step is ci.yml's own script):
        # a lost per-package check reported with ::error::, which the runner
        # prints as ##[error] in the Test step, here after the group.
        lines = framed(["--- FAIL: TestX (0.00s)", "FAIL\tgithub.com/example/m/x\t0.1s"])
        start = next(i for i, l in enumerate(lines) if l.endswith(ledger.GROUP_START))
        end = next(i for i, l in enumerate(lines) if i > start and l.endswith(ledger.GROUP_END))
        stamp = lines[end].split(" ", 1)[0]
        lines.insert(end + 1, f"{stamp} ##[error]fewer packages ran through the per-package check than passed")
        records, _ = ledger.parse_lines(lines)
        self.assertEqual(sorted(r.key for r in records), [
            "error: (Test step) fewer packages ran through the per-package check than passed",
            "test: x TestX"])

    def test_an_error_ci_test_reports_before_any_test_ran_is_a_record(self):
        # ci-test.sh refuses a value before go test starts: no group, only
        # its ::error:: in the step that runs it.
        stamp = "2026-10-24T09:00:00.0000000Z"
        records, printed_nothing = ledger.parse_lines([
            f"{stamp} ##[group]Run bash scripts/ci-test.sh", f"{stamp} ##[endgroup]",
            f"{stamp} ##[error]CI_TEST_COUNT must be a positive integer, not 'x'",
            f"{stamp} ##[error]Process completed with exit code 2."])
        self.assertEqual([r.key for r in records], ["error: (Test step) CI_TEST_COUNT must be a positive integer, not 'x'"])
        self.assertFalse(printed_nothing)

    def test_an_error_line_outside_the_test_step_is_not_read(self):
        # The recorded judge failure, with an error reported in the step
        # after Test (Skipped tests); that step's failure is the jobs API's
        # to report (read_job), not an error of the Test step.
        lines = (DATA / "judge-test-failure.log").read_text(encoding="utf-8").split("\n")
        step = next(i for i, l in enumerate(lines) if l.endswith("##[group]Run if [ ! -f test.log ]; then"))
        stamp = lines[step].split(" ", 1)[0]
        lines.insert(step + 2, f"{stamp} ##[error]grep: test.log: Input/output error")
        records, _ = ledger.parse_lines(lines)
        self.assertEqual([r.kind for r in records], ["test"])


# The recorded jobs' logs, by job id.
LOGS = {120256178246: "rerun-attempt-1.log", 120265317855: "rerun-attempt-2.log",
        120342424178: "job-timeout.log", 120290002840: "superseded-macos.log",
        120290002917: "superseded-ubuntu.log"}
TOKEN = "test-token"


class GitHubServer:
    """GitHub's REST API as the ledger reads it, served over HTTP in this
    process from api.json and the fixture logs. A job's log is a redirect to
    a second server, as GitHub's is to a signed address elsewhere; that
    server records the Authorization header it receives."""

    PAGE = 2  # items per page, so every listing the ledger reads has a next page
    SLOW_LIMIT = 15  # seconds

    def __init__(self, logs=LOGS, unreadable=(), flaky=None, broken=()):
        self.data = json.loads((DATA / "api.json").read_text())
        self.logs = dict(logs)
        self.unreadable = set(unreadable)
        self.flaky = dict(flaky or {})  # job id -> server errors before its log is served
        self.broken = set(broken)  # "issues", "annotations": reads that fail for good
        self.read = []  # job ids whose log was asked for
        self.log_authorizations = []  # the Authorization header each log read brought
        # Path prefixes that do not answer, and job ids whose log sends a line
        # every 0.2 s, until release or SLOW_LIMIT, so a ledger without its
        # deadline fails a test instead of hanging it.
        self.stalled = set()
        self.trickled = set()
        # job id -> "length" or "chunked": its log is cut off half way, after
        # a Content-Length promising all of it, or with no closing chunk.
        self.cut_logs = {}
        self.cut_once = set()  # job ids in cut_logs cut only on their first read
        self.cut_issues = False  # the issues answer promises 50 bytes and sends 2
        self.release = threading.Event()
        self.api = self._serve(self._answer_api)
        self.blobs = self._serve(self._answer_blob)

    def _serve(self, answer):
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                status, headers, body = answer(self)
                self.close_connection = True
                if headers.get("Transfer-Encoding") == "chunked":
                    self.protocol_version = "HTTP/1.1"
                try:
                    self.send_response(status)
                    for name, value in headers.items():
                        self.send_header(name, value)
                    if isinstance(body, bytes):
                        if "Content-Length" not in headers:  # one given promises more than is sent
                            self.send_header("Content-Length", str(len(body)))
                        self.end_headers()
                        self.wfile.write(body)
                        return
                    self.end_headers()
                    for chunk in body:  # streamed until release
                        self.wfile.write(chunk)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # the ledger gave up on this read

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.daemon_threads = True
        # A short poll, so close() does not wait half a second per server.
        threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()
        server.owner = owner
        return server

    def url(self, server=None):
        return f"http://127.0.0.1:{(server or self.api).server_address[1]}"

    def close(self):
        self.release.set()
        for server in (self.api, self.blobs):
            server.shutdown()
            server.server_close()

    def run(self, run_id):
        return next(r for r in self.data["runs"] if r["id"] == run_id)

    def job(self, job_id):
        return next(j for a in self.data["attempts"].values() for j in a["jobs"] if j["id"] == job_id)

    def _paged(self, request, items, key=None):
        split = urllib.parse.urlsplit(request.path)
        query = urllib.parse.parse_qs(split.query)
        page = int(query.get("page", ["1"])[0])
        chunk = items[(page - 1) * self.PAGE:page * self.PAGE]
        headers = {"Content-Type": "application/json"}
        if page * self.PAGE < len(items):
            query["page"] = [str(page + 1)]
            nxt = f"{self.url()}{split.path}?{urllib.parse.urlencode(query, doseq=True)}"
            headers["Link"] = f'<{nxt}>; rel="next", <{self.url()}{split.path}>; rel="first"'
        body = {key: chunk} if key else chunk
        return 200, headers, json.dumps(body).encode()

    def _answer_api(self, request):
        path = urllib.parse.urlsplit(request.path).path
        if any(path.startswith(p) for p in self.stalled):
            self.release.wait(self.SLOW_LIMIT)
        if request.headers.get("Authorization") != f"Bearer {TOKEN}":
            return 401, {}, b'{"message": "Requires authentication"}'
        repo = "/repos/dayamjz/assistant"
        if re.fullmatch(rf"{repo}/actions/workflows/[^/]+/runs", path):
            # Decoded as a server decodes a query string: %XX, and + as a space.
            query = urllib.parse.parse_qs(urllib.parse.urlsplit(request.path).query)
            runs = [r for r in self.data["runs"] if r["head_branch"] == query.get("branch", [r["head_branch"]])[0]]
            return self._paged(request, runs, "workflow_runs")
        match = re.fullmatch(rf"{repo}/actions/runs/(\d+)", path)
        if match:
            return 200, {}, json.dumps(self.run(int(match[1]))).encode()
        match = re.fullmatch(rf"{repo}/actions/runs/(\d+)/attempts/(\d+)(/jobs)?", path)
        if match:
            attempt = self.data["attempts"][f"{match[1]}/{match[2]}"]
            if match[3]:
                return self._paged(request, attempt["jobs"], "jobs")
            return 200, {}, json.dumps({"conclusion": attempt["conclusion"]}).encode()
        match = re.fullmatch(rf"{repo}/check-runs/(\d+)/annotations", path)
        if match:
            if "annotations" in self.broken:
                return 502, {}, b"{}"
            return 200, {}, json.dumps(self.data["annotations"][match[1]]).encode()
        if path == f"{repo}/issues":
            if "issues" in self.broken:
                return 503, {}, b"{}"
            if self.cut_issues:
                return 200, {"Content-Type": "application/json", "Content-Length": "50"}, b"[{"
            return self._paged(request, self.data["issues"])
        match = re.fullmatch(rf"{repo}/actions/jobs/(\d+)/logs", path)
        if match:
            job_id = int(match[1])
            self.read.append(job_id)
            if not self.job(job_id).get("steps"):
                # As GitHub answers for a job cancelled before a runner took
                # it (jobs 120513403886 and 120517498243): no steps, no log.
                return 404, {}, b'{"message": "Not Found"}'
            if job_id in self.unreadable:
                return 410, {}, b'{"message": "Gone"}'
            if self.flaky.get(job_id):
                self.flaky[job_id] -= 1
                return 503, {}, b"{}"
            return 302, {"Location": f"{self.url(self.blobs)}/logs/{job_id}?sig=signed"}, b""
        return 404, {}, b'{"message": "Not Found"}'

    def _trickle(self):
        ends = time.monotonic() + self.SLOW_LIMIT
        while not self.release.wait(0.2) and time.monotonic() < ends:
            yield b"a line\n"

    def _answer_blob(self, request):
        self.log_authorizations.append(request.headers.get("Authorization"))
        job_id = int(re.match(r"/logs/(\d+)", request.path)[1])
        if job_id in self.trickled:
            return 200, {"Content-Type": "text/plain"}, self._trickle()
        if job_id in self.cut_logs:
            framing = self.cut_logs[job_id]
            if job_id in self.cut_once:
                del self.cut_logs[job_id]
            body = (Path(self.logs[job_id]) if Path(self.logs[job_id]).is_absolute()
                    else DATA / self.logs[job_id]).read_bytes()
            half = body[:len(body) // 2]
            if framing == "length":
                return 200, {"Content-Type": "text/plain", "Content-Length": str(len(body))}, half
            return 200, {"Content-Type": "text/plain", "Transfer-Encoding": "chunked"}, \
                iter([b"%x\r\n" % len(half) + half + b"\r\n"])  # and no closing 0-size chunk
        log = self.logs[job_id]
        return 200, {"Content-Type": "text/plain"}, (Path(log) if Path(log).is_absolute() else DATA / log).read_bytes()


def log_lines(name, first=1, last=None):
    """Lines first..last (1-based, inclusive) of a recorded log, verbatim."""
    return (DATA / name).read_text(encoding="utf-8").split("\n")[first - 1:last]


def set_steps(github, job_id, conclusions):
    """Gives the recorded job's named steps these conclusions."""
    job = github.job(job_id)
    for step in job["steps"]:
        step["conclusion"] = conclusions.get(step["name"], step["conclusion"])
    return job


def stopped_after_attempt_2(github):
    """The recorded three-attempt run as it stood before attempt 3."""
    run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
    run["run_attempt"], run["conclusion"] = 2, "failure"
    del github.data["attempts"]["39905430594/3"]
    return github


class ServerTest(unittest.TestCase):
    def github(self, **options):
        server = GitHubServer(**options)
        self.addCleanup(server.close)
        return server

    def run_report(self, github, *extra, token=TOKEN):
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / "ledger.json"
            stdout, stderr = io.StringIO(), io.StringIO()
            env = {k: v for k, v in os.environ.items() if k not in ("GH_TOKEN", "GITHUB_TOKEN")}
            if token:
                env["GH_TOKEN"] = token
            with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(ledger, "pause"), \
                    contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                status = ledger.main(["--repo", "dayamjz/assistant", "--tz", "America/Los_Angeles",
                                      "--json", str(out), *extra], api_url=github.url())
            data = json.loads(out.read_text()) if out.exists() else None
        self.stderr = stderr.getvalue()
        return status, data, stdout.getvalue()


class LedgerTest(ServerTest):
    def test_an_issue_read_that_fails_still_prints_and_writes_the_ledger(self):
        status, data, text = self.run_report(self.github(broken={"issues"}))
        self.assertEqual(status, 2)
        self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt",
                      [f["key"] for f in data["failures"]])
        self.assertTrue(any("issues" in p and "HTTP 503" in p for p in data["problems"]), data["problems"])
        # Unknown, not "none": a failed read is no evidence that no issue exists.
        self.assertTrue(all(f["issues"] is None for f in data["failures"]))
        self.assertIn("unknown", text)
        self.assertNotIn("**none**", text)

    def test_a_cancelled_jobs_failed_tests_are_counted(self):
        # A Test step that failed and printed its output, then a newer push
        # cancelled the job in a later step: the cancel is the job's result,
        # the failure is still in its log.
        github = self.github(logs={**LOGS, 120290002840: "judge-test-failure.log"})
        _, data, _ = self.run_report(github)
        failure = next(f for f in data["failures"]
                       if f["key"] == "test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient")
        self.assertEqual(failure["occurrences"][0]["job_id"], 120290002840)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_failures_a_green_rerun_passed_over_are_counted_and_marked(self):
        status, data, text = self.run_report(self.github())
        self.assertEqual(status, 0, text)
        failures = {f["key"]: f for f in data["failures"]}
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual((dispatch["count"], dispatch["hidden_by_rerun"], dispatch["by_platform"]),
                         (1, 1, {"macos": 1}))
        self.assertEqual(dispatch["occurrences"][0]["attempt"], 2)
        self.assertEqual([i["number"] for i in dispatch["issues"]["closed"]], [552])
        supervisor = failures["package: internal/supervisor (package)"]
        self.assertEqual((supervisor["hidden_by_rerun"], supervisor["occurrences"][0]["attempt"]), (1, 1))
        timeout = failures["job-timeout: check (macos-latest) exceeded timeout-minutes"]
        self.assertEqual(timeout["hidden_by_rerun"], 1)
        self.assertEqual([i["number"] for i in timeout["issues"]["open"]], [559])
        self.assertEqual(sorted(failures), [
            "job-timeout: check (macos-latest) exceeded timeout-minutes",
            "leak: (leak check) leftovers, not read by this version",
            "package: internal/supervisor (package)",
            "test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"])

    def test_a_run_superseded_by_a_newer_one_is_counted_but_not_a_failure(self):
        _, data, _ = self.run_report(self.github())
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 2, "cancelled": 0})
        self.assertEqual(data["failed_jobs"], 3)

    def test_every_attempt_of_a_rerun_is_reported(self):
        _, data, text = self.run_report(self.github())
        rerun = next(r for r in data["reruns"] if r["run_id"] == 39905430594)
        self.assertEqual([a["conclusion"] for a in rerun["attempts"]], ["failure", "failure", "success"])
        # Attempt 2's only LEAK lines are the self-test's, printed before the group.
        self.assertEqual([(f["attempt"], f["key"]) for f in rerun["earlier_failures"]], [
            (1, "leak: (leak check) leftovers, not read by this version"),
            (1, "package: internal/supervisor (package)"),
            (2, "test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt")])
        self.assertIn("hidden: the final attempt does not show it", text)

    def test_an_unreadable_log_is_reported_and_fails_the_ledger(self):
        status, data, text = self.run_report(self.github(unreadable={120265317855}))
        self.assertEqual(status, 2)
        self.assertIn("HTTP 410", text)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120265317855])

    def test_a_server_error_is_read_again_and_a_lasting_one_is_still_loud(self):
        status, data, _ = self.run_report(self.github(flaky={120265317855: 2}))
        self.assertEqual(status, 0)
        self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt",
                      [f["key"] for f in data["failures"]])
        status, data, _ = self.run_report(self.github(flaky={120265317855: 3}))
        self.assertEqual(status, 2)
        self.assertIn("HTTP 503", data["problems"][0])

    def test_a_log_cut_off_part_way_is_a_read_that_failed(self):
        # The cut comes after the log's first half, so what came before it
        # names nothing wrong; only the missing rest says the read failed.
        for framing in ("length", "chunked"):
            with self.subTest(framing=framing):
                github = self.github()
                github.cut_logs[120265317855] = framing
                status, data, _ = self.run_report(github)
                self.assertEqual(status, 2)
                self.assertEqual([u["job_id"] for u in data["unattributed"]], [120265317855])
                self.assertRegex(data["unattributed"][0]["reason"], r"^not read: log of job 120265317855: incomplete read")

    def test_a_log_cut_off_once_is_read_again(self):
        github = self.github()
        github.cut_logs[120265317855] = "length"
        github.cut_once.add(120265317855)
        status, data, _ = self.run_report(github)
        self.assertEqual(status, 0)
        self.assertEqual(github.read.count(120265317855), 2)
        self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt",
                      [f["key"] for f in data["failures"]])

    def test_an_api_answer_cut_off_part_way_still_prints_and_writes_the_ledger(self):
        github = self.github()
        github.cut_issues = True
        status, data, text = self.run_report(github)
        self.assertEqual(status, 2)
        self.assertIsNotNone(data, self.stderr)
        self.assertTrue(any(p.startswith("issues: ") and "incomplete read" in p for p in data["problems"]),
                        data["problems"])
        self.assertTrue(all(f["issues"] is None for f in data["failures"]))
        self.assertIn("unknown", text)

    def test_a_failed_test_step_that_names_nothing_fails_the_ledger(self):
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "empty-group.log"
            log.write_text("\n".join(framed(["FAIL"])))
            status, data, _ = self.run_report(self.github(logs={**LOGS, 120256178246: log}))
        self.assertEqual(status, 3)
        self.assertEqual([(u["job_id"], u["reason"]) for u in data["unattributed"]],
                         [(120256178246, "the Test step failed and nothing in its log was named")])

    def test_a_failed_test_step_whose_output_was_never_printed_fails_the_ledger(self):
        # The step failed before the group (a value ci-test.sh refused, say)
        # and reported nothing this version reads.
        stamp = "2026-10-23T07:12:01.0000000Z"
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "no-group.log"
            log.write_text("\n".join([f"{stamp} ##[group]Run bash scripts/ci-test.sh", f"{stamp} ##[endgroup]",
                                      f"{stamp} ##[error]Process completed with exit code 1."]) + "\n")
            status, data, _ = self.run_report(self.github(logs={**LOGS, 120256178246: log}))
        self.assertEqual(status, 3)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120256178246])

    def test_a_failure_the_final_attempt_does_not_show_is_hidden_though_the_run_failed(self):
        # The recorded run, stopped after attempt 2: attempt 1's supervisor
        # failure is gone from attempt 2, the run's face, which fails
        # internal/service instead.
        _, data, text = self.run_report(stopped_after_attempt_2(self.github()))
        failures = {f["key"]: f for f in data["failures"]}
        self.assertEqual(failures["package: internal/supervisor (package)"]["hidden_by_rerun"], 1)
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual(dispatch["hidden_by_rerun"], 0)
        self.assertIn("attempt 1, check (macos-latest): package: internal/supervisor (package) "
                      "(hidden: the final attempt does not show it)", text)

    def test_a_failure_the_final_attempt_shows_again_is_not_hidden(self):
        github = stopped_after_attempt_2(self.github(logs={**LOGS, 120265317855: "rerun-attempt-1.log"}))
        _, data, _ = self.run_report(github)
        supervisor = next(f for f in data["failures"] if f["key"] == "package: internal/supervisor (package)")
        self.assertEqual((supervisor["count"], supervisor["hidden_by_rerun"]), (2, 0))

    def test_each_failure_is_timed_by_the_job_it_failed_in(self):
        # Attempt 2's job ended at 06:18:38Z, nearly an hour after the run
        # was created (05:22:50Z).
        _, data, _ = self.run_report(self.github())
        failures = {f["key"]: f for f in data["failures"]}
        dispatch = failures["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"]
        self.assertEqual((dispatch["first_seen"], dispatch["last_seen"]),
                         ("2026-10-23T06:18:38Z", "2026-10-23T06:18:38Z"))
        self.assertEqual(failures["package: internal/supervisor (package)"]["last_seen"], "2026-10-23T05:52:16Z")

    def test_a_step_that_failed_before_a_cancel_is_counted(self):
        # The recorded superseded job, as the runner would report it had
        # Verify formatting failed: the steps after it skip, the always()
        # step is cancelled, and the cancel becomes the job's conclusion.
        github = self.github()
        job = github.job(120290002840)
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
            github.logs[120290002840] = log
            status, data, _ = self.run_report(github)
        self.assertEqual(status, 0)
        failures = {f["key"]: f for f in data["failures"]}
        step = failures["step: check (macos-latest) step Verify formatting"]
        self.assertEqual([o["job_id"] for o in step["occurrences"]], [120290002840])
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_every_failed_step_counts_though_the_test_step_named_a_failure(self):
        github = self.github(logs={**LOGS, 120256178246: "judge-test-failure.log"})
        set_steps(github, 120256178246, {"Test": "failure", "Skipped tests": "failure"})
        status, data, text = self.run_report(github)
        self.assertEqual(status, 0, text)
        keys = {f["key"] for f in data["failures"] if any(o["job_id"] == 120256178246 for o in f["occurrences"])}
        self.assertEqual(keys, {"step: check (macos-latest) step Skipped tests",
                                "test: internal/judge TestAnotherProviderStoppingDoesNotReachAClient"})

    def test_the_headline_counts_every_job_whose_failures_are_counted(self):
        _, data, text = self.run_report(self.github(logs={**LOGS, 120290002840: "judge-test-failure.log"}))
        jobs = {o["job_id"] for f in data["failures"] for o in f["occurrences"]}
        self.assertEqual(len(jobs), 4)
        self.assertEqual(data["failed_jobs"], 4)
        self.assertIn("4 failed job(s)", text)

    def test_a_cancel_with_no_reason_recorded_is_not_said_to_be_by_hand(self):
        # No annotation and no steps: cancelled before a runner took it.
        github = self.github()
        github.data["annotations"]["120290002840"] = []
        github.job(120290002840)["steps"] = []
        status, data, text = self.run_report(github)
        self.assertEqual(status, 0, text)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 1})
        self.assertIn("1 cancelled for another reason (by hand, or before it started).", text)

    def test_a_job_whose_annotations_cannot_be_read_is_not_said_to_have_an_unreadable_log(self):
        status, data, _ = self.run_report(self.github(broken={"annotations"}))
        self.assertEqual(status, 2)
        reasons = {u["job_id"]: u["reason"] for u in data["unattributed"]}
        self.assertEqual(sorted(reasons), [120290002840, 120290002917, 120342424178])
        for reason in reasons.values():
            self.assertRegex(reason, r"^not read: repos/dayamjz/assistant/check-runs/\d+/annotations\S*: HTTP 502$")

    def test_a_cancelled_job_whose_tests_name_nothing_fails_the_ledger(self):
        # The superseded job's Test step printed its output and named no
        # failure: a cancel later in the job does not make that complete.
        with tempfile.TemporaryDirectory() as folder:
            log = Path(folder) / "empty-group.log"
            log.write_text("\n".join(framed(["FAIL"])))
            status, data, _ = self.run_report(self.github(logs={**LOGS, 120290002840: log}))
        self.assertEqual(status, 3)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120290002840])
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 1, "cancelled": 0})

    def test_a_job_cancelled_before_it_started_is_set_aside_without_a_log(self):
        github = self.github()
        github.job(120290002840)["steps"] = []
        status, data, text = self.run_report(github)
        self.assertEqual(status, 0, text)
        self.assertNotIn(120290002840, github.read)
        self.assertEqual(data["cancelled_jobs_not_counted"], {"superseded": 2, "cancelled": 0})

    def test_a_timeout_known_from_its_annotation_survives_an_unreadable_log(self):
        status, data, text = self.run_report(self.github(unreadable={120342424178}))
        self.assertEqual(status, 2)
        self.assertIn("HTTP 410", text)
        self.assertEqual([u["job_id"] for u in data["unattributed"]], [120342424178])
        timeout = next(f for f in data["failures"]
                       if f["key"] == "job-timeout: check (macos-latest) exceeded timeout-minutes")
        self.assertEqual([o["job_id"] for o in timeout["occurrences"]], [120342424178])

    def test_a_forks_branch_carries_its_owner(self):
        github = self.github()
        run = next(r for r in github.data["runs"] if r["id"] == 39905430594)
        run["head_branch"], run["head_repository"] = "main", {"full_name": "someone/assistant"}
        _, data, _ = self.run_report(github)
        branches = {o["branch"] for f in data["failures"] for o in f["occurrences"] if o["run_id"] == 39905430594}
        self.assertEqual(branches, {"someone:main"})

    def test_an_issue_about_another_tests_subtest_of_the_same_name_is_not_the_failures(self):
        github = self.github()
        github.data["issues"] = [{"number": 900, "state": "open", "body": "", "closed_at": None,
                                  "title": "service: TestOther/TestConcurrentDispatchOfOneTaskClaimsOneAttempt flakes"}]
        _, data, _ = self.run_report(github)
        entry = next(f for f in data["failures"] if f["name"] == "TestConcurrentDispatchOfOneTaskClaimsOneAttempt")
        self.assertEqual(entry["issues"], {"open": [], "closed": []})


class OneRunTest(ServerTest):
    """--run: one run, finished or not, read as its own workflow's check; and
    --require-open-issues, which fails a re-run past a failure no open issue
    names."""

    def test_one_run_is_read_alone_with_its_finished_jobs_counted(self):
        status, data, text = self.run_report(self.github(), "--run", "39905430594")
        self.assertEqual(status, 0, text)
        self.assertEqual((data["runs_scanned"], data["jobs_read"], data["failed_jobs"]), (1, 9, 2))
        self.assertIn("9 finished job(s), 2 failed job(s)", text)

    def test_a_running_attempts_unfinished_jobs_are_not_read(self):
        # The run as its re-runs job sees it: attempt 3 still running, its
        # macOS check job not finished.
        github = self.github()
        run = github.run(39905430594)
        run["status"], run["conclusion"] = "in_progress", None
        job = github.data["attempts"]["39905430594/3"]["jobs"][0]
        job["status"], job["conclusion"] = "in_progress", None
        status, data, _ = self.run_report(github, "--run", "39905430594")
        self.assertEqual(status, 0)
        self.assertEqual(data["jobs_read"], 8)
        self.assertNotIn(job["id"], github.read)

    def test_one_run_with_a_cancelled_job_is_incomplete(self):
        # Run 39916130926's two check jobs were cancelled (superseded): read
        # as one run, its check cannot pass, whatever the cancel's reason.
        status, data, _ = self.run_report(self.github(), "--run", "39916130926")
        self.assertEqual(status, 2)
        self.assertEqual(data["failures"], [])
        self.assertIn("latest attempt did not finish", self.stderr)
        status, _, _ = self.run_report(self.github())
        self.assertEqual(status, 0, "over many runs a superseded job is only counted")

    def test_one_run_with_a_job_that_timed_out_is_incomplete(self):
        # Run 39932040317 as it stood after attempt 1, its macOS job cut off
        # at timeout-minutes: a failure, and a suite that never finished.
        github = self.github()
        run = github.run(39932040317)
        run["run_attempt"], run["conclusion"] = 1, "cancelled"
        del github.data["attempts"]["39932040317/2"]
        status, data, _ = self.run_report(github, "--run", "39932040317")
        self.assertEqual(status, 2)
        self.assertEqual([(c["job_id"], c["reason"]) for c in data["cancelled_jobs"]], [(120342424178, "timeout")])
        self.assertIn("job-timeout: check (macos-latest) exceeded timeout-minutes", [f["key"] for f in data["failures"]])

    def test_one_run_with_a_job_cancelled_after_a_failure_is_incomplete(self):
        github = self.github(logs={**LOGS, 120290002840: "judge-test-failure.log"})
        set_steps(github, 120290002917, {})["conclusion"] = "success"
        status, data, _ = self.run_report(github, "--run", "39916130926")
        self.assertEqual(status, 2)
        self.assertEqual([(c["job_id"], c["reason"]) for c in data["cancelled_jobs"]], [(120290002840, "superseded")])

    def test_an_earlier_attempts_cancel_does_not_make_a_run_incomplete(self):
        # Run 39932040317's attempt 1 timed out (#559 is open); attempt 2
        # finished every job.
        status, data, _ = self.run_report(self.github(), "--run", "39932040317", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual([f["key"] for f in data["failures"]],
                         ["job-timeout: check (macos-latest) exceeded timeout-minutes"])

    def test_a_rerun_past_a_failure_needs_an_open_issue_for_it(self):
        # #551 and #552 are closed, and nothing names this version's leak or
        # package rows, so none of run 39905430594's earlier failures has an
        # open issue.
        status, data, text = self.run_report(self.github(), "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 4)
        self.assertEqual([(r["attempt"], r["key"]) for r in data["rerun_without_issue"]], [
            (1, "leak: (leak check) leftovers, not read by this version"),
            (1, "package: internal/supervisor (package)"),
            (2, "test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt")])
        self.assertIn("## Re-run past with no open issue", text)
        self.assertIn("::error::run 39905430594 attempt 2 (check (macos-latest)) failed test: internal/service "
                      "TestConcurrentDispatchOfOneTaskClaimsOneAttempt, and no open issue names it", self.stderr)

    def test_a_rerun_check_without_the_issues_does_not_pass(self):
        status, _, _ = self.run_report(self.github(broken={"issues"}), "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 2)

    def open_issues_naming_every_earlier_failure(self, github):
        for issue in github.data["issues"]:
            issue["state"], issue["closed_at"] = "open", None
        github.data["issues"].append({
            "number": 901, "state": "open", "closed_at": None, "title": "supervisor leaks under the leak check",
            "body": "Rows: leak: (leak check) leftovers, not read by this version; package: internal/supervisor (package)"})

    def test_open_issues_let_a_rerun_pass(self):
        github = self.github()
        self.open_issues_naming_every_earlier_failure(github)
        status, data, _ = self.run_report(github, "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual(data["rerun_without_issue"], [])

    def test_a_failure_of_the_latest_attempt_needs_no_issue_to_pass_this_check(self):
        # Run 39905430594 as it stood after attempt 2: attempt 2's failure
        # (#552 closed) is the run's own red result, not one a re-run passed
        # over; attempt 1's are covered by open issues.
        github = stopped_after_attempt_2(self.github())
        self.open_issues_naming_every_earlier_failure(github)
        next(i for i in github.data["issues"] if i["number"] == 552)["state"] = "closed"
        status, data, _ = self.run_report(github, "--run", "39905430594", "--require-open-issues")
        self.assertEqual(status, 0)
        self.assertEqual(data["rerun_without_issue"], [])
        self.assertIn("test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt",
                      [f["key"] for f in data["failures"]])

    def rerun_check_failed_in_attempt_2(self, folder):
        # Run 39905430594 with its attempt 2 holding a failed re-runs job, as
        # ci.yml's job fails: its step exits 4 after the ledger's ::error::.
        stamp = "2026-10-23T06:20:01.0000000Z"
        log = Path(folder) / "rerun-check-failed.log"
        log.write_text("\n".join([
            f"{stamp} ##[group]Run python3 scripts/flake-ledger.py --repo \"$GITHUB_REPOSITORY\" --run \"$GITHUB_RUN_ID\" \\",
            f"{stamp} ##[endgroup]",
            f"{stamp} ##[error]run 39905430594 attempt 1 (check (macos-latest)) failed package: internal/supervisor "
            "(package), and no open issue names it; file one containing package: internal/supervisor (package) "
            "before re-running past it",
            f"{stamp} ##[error]Process completed with exit code 4."]) + "\n")
        github = self.github(logs={**LOGS, 99000000001: log})
        github.data["attempts"]["39905430594/2"]["jobs"].append({
            "id": 99000000001, "run_attempt": 2, "name": "re-runs", "status": "completed", "conclusion": "failure",
            "started_at": "2026-10-23T06:19:50Z", "completed_at": "2026-10-23T06:20:02Z", "labels": ["ubuntu-latest"],
            "html_url": "https://github.com/dayamjz/assistant/actions/runs/39905430594/job/99000000001",
            "steps": [{"name": "Set up job", "conclusion": "success"},
                      {"name": "Run actions/checkout@v4", "conclusion": "success"},
                      {"name": "Every earlier attempt's failure has an open issue", "conclusion": "failure"},
                      {"name": "Complete job", "conclusion": "success"}]})
        self.open_issues_naming_every_earlier_failure(github)
        return github

    def test_the_rerun_checks_own_earlier_failure_needs_no_issue(self):
        with tempfile.TemporaryDirectory() as folder:
            github = self.rerun_check_failed_in_attempt_2(folder)
            status, data, text = self.run_report(github, "--run", "39905430594", "--require-open-issues",
                                                 "--own-job", "re-runs")
        self.assertEqual(status, 0, text)
        self.assertEqual(data["rerun_without_issue"], [])
        # Its failed step is still a row, and its error lines are not read as a Test step's.
        keys = {f["key"] for f in data["failures"]}
        self.assertIn("step: re-runs step Every earlier attempt's failure has an open issue", keys)
        self.assertFalse(any(k.startswith("error:") for k in keys), keys)

    def test_another_jobs_earlier_failure_still_needs_an_issue(self):
        # The positive control: --own-job sets aside only that job.
        with tempfile.TemporaryDirectory() as folder:
            github = self.rerun_check_failed_in_attempt_2(folder)
            next(i for i in github.data["issues"] if i["number"] == 552)["state"] = "closed"
            status, data, _ = self.run_report(github, "--run", "39905430594", "--require-open-issues",
                                              "--own-job", "re-runs")
        self.assertEqual(status, 4)
        self.assertEqual([r["key"] for r in data["rerun_without_issue"]],
                         ["test: internal/service TestConcurrentDispatchOfOneTaskClaimsOneAttempt"])


class WireTest(ServerTest):
    """How the ledger talks to GitHub: what it sends where, and its bounds."""

    def test_without_a_token_it_refuses_before_reading_anything(self):
        github = self.github()
        status, data, _ = self.run_report(github, token=None)
        self.assertEqual(status, 2)
        self.assertIsNone(data)
        self.assertIn("set GH_TOKEN or GITHUB_TOKEN", self.stderr)
        self.assertEqual(github.read, [])

    def test_a_token_github_refuses_is_loud(self):
        status, data, _ = self.run_report(self.github(), token="wrong")
        self.assertEqual(status, 2)
        self.assertIn("HTTP 401", self.stderr)

    def test_a_logs_redirect_is_read_without_the_token(self):
        github = self.github()
        status, _, _ = self.run_report(github)
        self.assertEqual(status, 0)
        self.assertEqual(sorted(github.read), sorted(j for j in LOGS if github.job(j).get("steps")))
        self.assertTrue(github.log_authorizations)
        self.assertEqual(set(github.log_authorizations), {None})

    def test_every_page_of_a_listing_is_read(self):
        # GitHubServer serves two items a page, so the runs, each attempt's
        # jobs and the issues all take more than one page, each found by
        # following the Link header.
        github = self.github()
        client = ledger.GitHub("dayamjz/assistant", TOKEN, github.url())
        self.assertEqual(len(client.runs("ci.yml", 60)), len(github.data["runs"]))
        self.assertEqual(len(client.issues()), len(github.data["issues"]))
        jobs = github.data["attempts"]["39905430594/1"]["jobs"]
        self.assertGreater(len(jobs), GitHubServer.PAGE)
        self.assertEqual([j["id"] for j in client.attempt_jobs(39905430594, 1)], [j["id"] for j in jobs])

    def test_a_next_page_elsewhere_is_not_sent_the_token(self):
        github = self.github()
        original = github._paged

        def elsewhere(request, items, key=None):
            status, headers, body = original(request, items, key)
            if "Link" in headers:
                headers["Link"] = headers["Link"].replace(github.url(), github.url(github.blobs))
            return status, headers, body
        github._paged = elsewhere
        client = ledger.GitHub("dayamjz/assistant", TOKEN, github.url())
        with self.assertRaisesRegex(ledger.GitHubError, "not the API's address, so the token is not sent there"):
            client.issues()
        self.assertEqual(github.log_authorizations, [])

    def test_a_branch_is_asked_for_by_its_own_name(self):
        # A name git accepts (git check-ref-format --branch). Unencoded in a
        # query, a server reads + as a space, # as the query's end and & as
        # the next parameter.
        github = self.github()
        branch = github.data["runs"][0]["head_branch"] = "fix/c++#2&x=1"
        runs = ledger.GitHub("dayamjz/assistant", TOKEN, github.url()).runs("ci.yml", 10, branch=branch,
                                                                            since="2026-10-01")
        self.assertEqual([r["id"] for r in runs], [github.data["runs"][0]["id"]])

    def test_a_read_that_never_answers_ends_at_its_deadline(self):
        github = self.github()
        github.stalled.add("/repos/dayamjz/assistant/issues")
        client = ledger.GitHub("dayamjz/assistant", TOKEN, github.url())
        begun = time.monotonic()
        with mock.patch.object(ledger, "API_DEADLINE", 1), mock.patch.object(ledger, "pause"):
            with self.assertRaisesRegex(ledger.GitHubError, "timed out after 1s"):
                client.issues()
        # Three tries, each ended at its deadline.
        self.assertLess(time.monotonic() - begun, 10)

    def test_a_log_that_trickles_ends_at_its_deadline(self):
        # Each line comes well inside READ_TIMEOUT; the whole log never ends.
        github = self.github()
        github.trickled.add(120265317855)
        client = ledger.GitHub("dayamjz/assistant", TOKEN, github.url())
        begun = time.monotonic()
        with mock.patch.object(ledger, "LOG_DEADLINE", 1), mock.patch.object(ledger, "pause"):
            result = ledger.read_job(client, github.job(120265317855))
        self.assertEqual(result["error"], "log of job 120265317855: timed out after 1s")
        # Three tries, each ended at its deadline.
        self.assertLess(time.monotonic() - begun, 10)


class MatchTest(unittest.TestCase):
    def test_a_test_name_matches_only_as_a_whole_word(self):
        find = ledger.matcher([{"number": 1, "state": "open", "title": "TestFooBar flakes", "body": ""}])
        self.assertEqual([i["number"] for i in find("TestFooBar", test=True)["open"]], [1])
        self.assertEqual(find("TestFoo", test=True)["open"], [])

    def test_a_failed_test_is_not_another_tests_subtest_of_its_name(self):
        titles = {1: "service.TestTop flakes", 2: "TestTop/sub flakes", 3: "TestOther/TestTop flakes"}
        find = ledger.matcher([{"number": n, "state": "open", "title": t, "body": ""} for n, t in titles.items()])
        self.assertEqual([i["number"] for i in find("TestTop", test=True)["open"]], [1, 2])
        # Any other token may follow a path.
        find = ledger.matcher([{"number": 4, "state": "open", "title": "/usr/libexec/mdworker_shared stays", "body": ""}])
        self.assertEqual([i["number"] for i in find("mdworker_shared")["open"]], [4])


if __name__ == "__main__":
    if sys.argv[1:2] == ["write-go-module"]:
        write_go_module(sys.argv[2])
        sys.exit(0)
    unittest.main()
