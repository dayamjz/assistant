#!/usr/bin/env python3
"""Count every failure in recent CI runs, by test, platform and branch.

Run: python3 scripts/flake-ledger.py [--limit 60] [--json ledger.json]
Parse one log: python3 scripts/flake-ledger.py parse <log>... (a job log as
GitHub serves it, or raw `go test -v` output with --raw)

The ledger reads every attempt of every completed run of a workflow (ci.yml
by default), not only the latest: `gh run list` reports a run's last attempt,
so a test that failed in attempt 1 and passed in attempt 2 is invisible to
anything that reads conclusions. A failure in an earlier attempt that the
run's final attempt does not show is marked hidden by a re-run, whatever the
final attempt concluded: the final attempt is all a run's checks show. Each
failure is timed by the job it failed in, since a re-run can come hours after
its run was created.

What it reads, and from where:
- Go tests, from the Test step's `full test output` group, which ci.yml
  prints only when that step fails. `--- FAIL` lines name tests and
  subtests; `panic: test timed out` names the tests still running; a
  `FAIL <package>` line with no failing test under it is a package failure
  (a build failure, a panic outside a test, or a per-package leak).
- The leak check's leftovers, from the same group: `LEAK package=...` is a
  per-package leak, a `LEAK` line without `package=` a whole-run leftover.
  The self-test's planted leftovers print outside the group and are not
  read.
- Python unittest failures (`FAIL: test_x (...)`) anywhere in the log.
- A job cancelled at its timeout-minutes bound, from the job's annotations.
  A cancelled job's log is read like a failed one's, and what failed in it
  before the cancel counts; a cancelled job with nothing failed in it
  (superseded by a newer run of the same ref, or cancelled otherwise) is not
  a failure and is only counted.
- Any other failed step (vet, gofmt, lint) as a step failure naming the step
  and its last error line, in a failed or a cancelled job.

It is loud where it cannot see: a log or API read that fails makes it exit 2
(a failed issue read leaves each failure's issues unknown, never "none"),
and a failed job whose log shows tests ran but in which nothing was named
makes it exit 3 (listed under "Unattributed"). Both still print the ledger.

Every gh it starts runs under a deadline (API_DEADLINE, LOG_DEADLINE) in a
process group of its own, watched by a small watchdog that ends the group at
the deadline or as soon as the ledger exits, however it exits (start_gh).

Issues are matched by text: an issue whose title or body contains a record's
match token (a test's top-level name, a leaked command's base name, or the
record key itself) is that record's issue. The markdown prints each token,
so an issue can be linked by writing it into the issue.
"""

import argparse
import concurrent.futures
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import time

pause = time.sleep  # between tries of a read GitHub failed with a server error
SCRIPT_DIGEST = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()[:16]

ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
STAMP = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ")
GROUP_START = "##[group]full test output"
GROUP_END = "##[endgroup]"
STEP_START = "##[group]Run "
GO_FAIL = re.compile(r"^(\s*)--- FAIL: (\S+)")
GO_PACKAGE_END = re.compile(r"^(ok|FAIL)\s+(\S+)(?:\s+(\[[^\]]+\]|\S+))?")
GO_CONTEXT = re.compile(r"^=== (?:RUN|CONT|NAME)\s+(\S+)")
GO_MESSAGE = re.compile(r"^\s+(\S+\.go:\d+: .*)")
# internal/principles.Cite logs this before a test's own messages; it never
# says why a test failed.
CITE_MESSAGE = re.compile(r"^\S+\.go:\d+: claims PRD principle ")
# A t.TempDir component: the test's name with its slashes dropped, then digits.
TEST_TEMPDIR = re.compile(r"^(Test[A-Za-z0-9_]+?)\d+$")
GO_TIMEOUT = re.compile(r"^panic: test timed out after (\S+)")
GO_RUNNING = re.compile(r"^\s+(Test\S*|Example\S*|Fuzz\S*|Benchmark\S*) \(")
GO_PANIC = re.compile(r"^panic: (.*)")
# Every line the leak check prints for a package starts so; the processes it
# names carry a command, and a process it saw too late to read does not.
LEAK_PACKAGE = re.compile(r"^LEAK package=(\S+) (.*)$")
LEAK_PACKAGE_PROCESS = re.compile(r"^process pid=\d+ start=\S+ command=(.*)$")
LEAK_PROCESS = re.compile(r"^LEAK process pid=\d+ start=\S+ command=(.*)$")
LEAK_ENTRY = re.compile(r"^LEAK (directory|file|socket|symlink|fifo|entry) (.+)$")
LEAK_TOTAL = re.compile(r"^leak-check: (\d+) leftover\(s\); command exit=")
UNITTEST = re.compile(r"^(FAIL|ERROR): (\w+) \(([\w.]+?)\)(?: \((.*)\))?\s*$")
UNITTEST_SUMMARY = re.compile(r"^FAILED \((?:failures|errors)=")
ERROR_LINE = re.compile(r"^##\[error\](.*)")
GUARD_ERROR = "fewer packages ran through the per-package check than passed"
TIMEOUT_ANNOTATION = "exceeded the maximum execution time"
SUPERSEDED_ANNOTATION = "higher priority waiting request"
# The error line the runner prints in the step a cancel stops.
CANCEL_ERROR = "The operation was canceled"
MODULE_ROOT = re.compile(r"^/(?:Users|home)/runner/work/[^/]+/[^/]+")


def clean(line):
    """Strip the BOM, GitHub's timestamp prefix and terminal escapes."""
    line = line.rstrip("\r\n").lstrip("\ufeff")
    return ANSI.sub("", STAMP.sub("", line, count=1))


def normalize_path(path):
    """Fold the parts of a leftover path that differ on every run."""
    path = re.sub(r"/_temp/tmp\.[A-Za-z0-9]+", "/_temp/tmp.*", path)
    path = re.sub(r"/var/folders/[^/]+/[^/]+/T", "/var/folders/*/*/T", path)
    # internal/agents/standin's TempDir names: at<tag>-<pid36>-<start36>-<pattern><random>.
    path = re.sub(r"/(at[a-z0-9_]+)-[0-9a-z]+-[0-9a-z]+-([^/]*?)\d+(?=/|$)", r"/\1-*-*-\2*", path)
    return re.sub(r"\d{4,}", "N", path)


def command_name(command):
    """A leaked command's stable name: its program's base name."""
    words = command.split()
    if not words:
        return "(no command)"
    return re.sub(r"\d{4,}", "N", os.path.basename(words[0]) or words[0])


def tempdir_test(command):
    """The test whose t.TempDir a leaked program ran from, or None. Go drops
    the slash from a subtest's name there, so this is a prefix of nothing
    shorter than the top-level test's name plus the subtest's."""
    words = command.split()
    for part in (words[0].split("/") if words else []):
        match = TEST_TEMPDIR.match(part)
        if match:
            return match.group(1)
    return None


def package_of(path):
    """A runner's absolute package directory as a module-relative one."""
    relative = MODULE_ROOT.sub("", path).lstrip("/")
    return relative or "."


def short_package(import_path):
    """An import path under this module as a module-relative directory."""
    parts = import_path.split("/")
    if len(parts) > 3 and parts[0] in ("github.com", "gitlab.com"):
        return "/".join(parts[3:])
    if len(parts) == 3 and parts[0] in ("github.com", "gitlab.com"):
        return "."
    return import_path


class Record:
    """One failure seen in one job: what failed, and the match token for issues."""

    def __init__(self, kind, package, name, detail="", subtests=(), token=None, prefix=False):
        self.kind = kind
        self.package = package
        self.name = name
        self.detail = detail
        self.subtests = sorted(set(subtests))
        self.token = token or self.key
        # A prefix token is matched by any test name in an issue that it starts with.
        self.prefix = prefix

    @property
    def key(self):
        return f"{self.kind}: {self.package} {self.name}".rstrip()

    def to_dict(self):
        return {"kind": self.kind, "package": self.package, "name": self.name,
                "detail": self.detail, "subtests": self.subtests, "token": self.token,
                "prefix": self.prefix}

    @classmethod
    def from_dict(cls, data):
        return cls(data["kind"], data["package"], data["name"], data.get("detail", ""),
                   data.get("subtests", ()), data.get("token"), data.get("prefix", False))


class LogParser:
    """Turns a job log, line by line, into failure records.

    raw=True reads plain `go test -v` output, as if all of it were the full
    test output group.
    """

    def __init__(self, raw=False):
        self.raw = raw
        self.in_group = raw
        self.saw_group = False
        self.saw_unittest_failure = False
        self.step = ""
        self.errors = []
        self.records = []
        # Per Go package block, reset at each ok/FAIL package line.
        self.failed_tops, self.failed_subs = set(), set()
        self.first = {}  # top-level test -> the first message logged under it
        self.messages = {}
        self.context = ""
        self.block_leaks = []
        self.block_panic = ""
        self.timeout = None  # (after, [running tests]) once a timeout panic is seen
        self.reading_running = False
        self.run_leaks = []
        self.run_leak_total = None
        self.group_lines = 0

    def feed(self, line):
        line = clean(line)
        if not self.raw:
            if line.startswith(GROUP_START):
                self.in_group = True
                self.saw_group = True
                return
            if self.in_group and line.startswith(GROUP_END):
                self.in_group = False
                return
            if line.startswith(STEP_START):
                self.step = line[len(STEP_START):].strip()
        if self.in_group:
            self.group_lines += 1
            self._go_line(line)
            return
        error = ERROR_LINE.match(line)
        if error:
            message = error.group(1).strip()
            self.errors.append(message)
            if GUARD_ERROR in message:
                self.records.append(Record("guard", "(per-package check)", GUARD_ERROR))
            return
        self._unittest_line(line)

    def _unittest_line(self, line):
        if UNITTEST_SUMMARY.match(line):
            self.saw_unittest_failure = True
            return
        match = UNITTEST.match(line)
        if not match:
            return
        method, where, params = match.group(2), match.group(3), match.group(4)
        # Python 3.11+ prints module.Class.method, earlier ones module.Class.
        parts = where.split(".")
        if parts[-1] == method:
            parts = parts[:-1]
        name = f"{parts[-1]}.{method}" if parts and parts[-1] != "__main__" else method
        script = next((w for w in self.step.split() if w.endswith(".py")), self.step or "(unittest)")
        detail = f"{match.group(1)}" + (f" ({params})" if params else "")
        self.records.append(Record("unittest", script, name, detail, token=method))

    def _go_line(self, line):
        if self.reading_running:
            running = GO_RUNNING.match(line)
            if running:
                self.timeout[1].append(running.group(1))
                return
            if line.strip() in ("", "running tests:"):
                return
            self.reading_running = False
        context = GO_CONTEXT.match(line)
        if context:
            self.context = context.group(1)
            return
        message = GO_MESSAGE.match(line)
        if message and self.context and self.context not in self.messages \
                and not CITE_MESSAGE.match(message.group(1)):
            self.messages[self.context] = message.group(1)[:200]
        failed = GO_FAIL.match(line)
        if failed:
            indent, name = failed.group(1), failed.group(2)
            if not indent:
                self.failed_tops.add(name)
                self.first.setdefault(name, self.messages.get(name, ""))
            elif "/" in name:
                # A subtest. An indented name with no slash is output a test
                # logged (a fixture's own go test), not a test of this package.
                self.failed_subs.add(name)
                self.first.setdefault(name.split("/", 1)[0], self.messages.get(name, ""))
            return
        timeout = GO_TIMEOUT.match(line)
        if timeout:
            self.timeout = (timeout.group(1), [])
            self.reading_running = True
            return
        panic = GO_PANIC.match(line)
        if panic and not self.block_panic:
            self.block_panic = panic.group(1)[:200]
            return
        leak = LEAK_PACKAGE.match(line)
        if leak:
            # Recorded where it is read, from the line's own package, so a
            # leak is counted whatever its package's verdict line says.
            self._package_leak(package_of(leak.group(1)), leak.group(2))
            return
        process = LEAK_PROCESS.match(line)
        if process:
            self.run_leaks.append(("process", command_name(process.group(1)), process.group(1)))
            return
        entry = LEAK_ENTRY.match(line)
        if entry:
            self.run_leaks.append((entry.group(1), normalize_path(entry.group(2)), entry.group(2)))
            return
        if line.startswith("LEAK "):
            # A shape this parser does not know is still a leftover.
            self.run_leaks.append(("unparsed", re.sub(r"\d{4,}", "N", line), line))
            return
        total = LEAK_TOTAL.match(line)
        if total:
            self.run_leak_total = int(total.group(1))
            return
        end = GO_PACKAGE_END.match(line)
        if end and "/" in end.group(2):
            self._end_package(end.group(1), short_package(end.group(2)), end.group(3) or "")

    def _package_leak(self, package, rest):
        self.block_leaks.append(rest)
        process = LEAK_PACKAGE_PROCESS.match(rest)
        if not process:
            name = re.sub(r"\d{4,}", "N", rest)
            self.records.append(Record("leak", package, name, rest[:200]))
            return
        command = process.group(1)
        name, test = command_name(command), tempdir_test(command)
        if test:
            self.records.append(Record("leak", package, f"{name} from {test}", command[:200],
                                       token=test, prefix=True))
        else:
            self.records.append(Record("leak", package, name, command[:200], token=name))

    def _end_package(self, verdict, package, suffix):
        if verdict == "FAIL":
            self._fail_package(package, suffix)
        self.failed_tops, self.failed_subs, self.first = set(), set(), {}
        self.messages = {}
        self.context = ""
        self.block_leaks = []
        self.block_panic = ""
        self.timeout = None
        self.reading_running = False

    def _fail_package(self, package, suffix):
        before = len(self.records)
        running = self.timeout[1] if self.timeout else []
        running_tops = {t.split("/", 1)[0] for t in running}
        for top in sorted(self.failed_tops | running_tops):
            subtests = [s for s in self.failed_subs | set(running) if s.startswith(top + "/")]
            if top in running_tops:
                detail = f"test timed out after {self.timeout[0]}"
                self.records.append(Record("timeout", package, top, detail, subtests, token=top))
            else:
                self.records.append(Record("test", package, top, self.first.get(top, ""), subtests, token=top))
        if self.timeout and not running:
            self.records.append(Record("timeout", package, "(package)",
                                       f"test timed out after {self.timeout[0]}"))
        if len(self.records) == before and not self.block_leaks:
            if suffix.startswith("["):
                self.records.append(Record("build", package, suffix.strip("[]")))
            else:
                detail = f"panic: {self.block_panic}" if self.block_panic else "no failing test was named"
                self.records.append(Record("package", package, "(package)", detail))

    def finish(self):
        """The records, plus whether tests visibly ran but nothing was named."""
        if self.run_leaks:
            directories = [full for kind, _, full in self.run_leaks if kind == "directory"]
            for kind, stable, full in self.run_leaks:
                if any(full.startswith(d + "/") for d in directories):
                    continue  # inside a leftover directory already reported
                token = stable if kind in ("process", "unparsed") else os.path.basename(stable.rstrip("/"))
                self.records.append(Record("run-leak", "(whole run)", f"{kind} {stable}",
                                           full[:200], token=token))
        if self.run_leak_total and not self.run_leaks:
            self.records.append(Record("run-leak", "(whole run)", "(unparsed leftovers)",
                                       f"leak-check reported {self.run_leak_total} leftover(s) "
                                       "that this parser did not read"))
        # One record per failure per job: the summary and the leak check can
        # print one failure more than once.
        unique = {}
        for record in self.records:
            if record.key in unique:
                unique[record.key].subtests = sorted(set(unique[record.key].subtests) | set(record.subtests))
            else:
                unique[record.key] = record
        self.records = list(unique.values())
        tests_ran = self.saw_group or self.saw_unittest_failure or (self.raw and self.group_lines)
        return self.records, bool(tests_ran) and not self.records


def parse_lines(lines, raw=False):
    parser = LogParser(raw=raw)
    for line in lines:
        parser.feed(line)
    records, unattributed = parser.finish()
    return records, unattributed, parser.errors


class GitHubError(Exception):
    """A gh call failed; the ledger is incomplete without its answer."""

    @property
    def transient(self):
        return bool(re.search(r"HTTP 5\d\d|HTTP 429|timed out|connection reset", str(self)))


def retried(read, tries=3):
    """read(), again after a pause when GitHub answers a server error. A read
    that still fails after the last try raises, so the ledger stays loud."""
    for attempt in range(tries):
        try:
            return read()
        except GitHubError as error:
            if not error.transient or attempt == tries - 1:
                raise
            pause(2 ** attempt)


# Seconds one gh call may take before it is ended: an API read (a paginated
# one included), and one job's log, which can run to megabytes.
API_DEADLINE = 120
LOG_DEADLINE = 300

# Runs one gh in a process group of its own and ends that group, gh and what
# gh started, when gh exits, when the deadline passes, or when the ledger's end
# of stdin closes, which the kernel does however the ledger exits, killed
# included. The watchdog runs in a session of its own, so a signal meant for
# the ledger's terminal does not stop it before it has ended gh.
WATCHDOG = r"""
import os, signal, subprocess, sys, threading
deadline, command = float(sys.argv[1]), sys.argv[2:]
child = subprocess.Popen(command, stdin=subprocess.DEVNULL, start_new_session=True)

def end(status, message=None):
    try:
        os.killpg(child.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    if message:
        sys.stderr.write(message + "\n")
        sys.stderr.flush()
    os._exit(status)

def owner_gone():
    sys.stdin.buffer.read()
    end(125, "the flake ledger exited")

threading.Thread(target=owner_gone, daemon=True).start()
try:
    status = child.wait(timeout=deadline)
except subprocess.TimeoutExpired:
    end(124, f"timed out after {deadline:g}s")
end(status if status >= 0 else 128 - status)
"""


def start_gh(args, deadline):
    """A running gh under the watchdog: stdout a pipe, stderr a file, and
    stdin the pipe whose closing ends it. A gh call ends with everything it
    started at its deadline or when the ledger exits; a program gh starts
    that leaves gh's process group is not followed."""
    errors = tempfile.TemporaryFile(mode="w+", encoding="utf-8", errors="replace")
    process = subprocess.Popen(
        [sys.executable, "-I", "-c", WATCHDOG, str(deadline), "gh", *args],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors,
        text=True, encoding="utf-8", errors="replace", start_new_session=True)
    return process, errors


def finish_gh(process, errors):
    """gh's exit status and what it printed on stderr, once it has ended."""
    status = process.wait()
    process.stdin.close()
    errors.seek(0)
    message = errors.read().strip()
    errors.close()
    return status, message


def gh_output(args, deadline=None):
    """(status, stdout, stderr) of one bounded gh call."""
    process, errors = start_gh(args, API_DEADLINE if deadline is None else deadline)
    try:
        output = process.stdout.read()
    finally:
        process.stdout.close()
        status, message = finish_gh(process, errors)
    return status, output, message


class GitHub:
    """The few GitHub reads the ledger makes, all through the gh CLI, each
    under a deadline (start_gh)."""

    def __init__(self, repo):
        self.repo = repo
        self._escape_flag = None

    def _gh(self, args):
        def read():
            status, output, message = gh_output(args)
            if status != 0:
                raise GitHubError(f"gh {' '.join(args)}: {message or status}")
            return output
        return retried(read)

    def api(self, path, paginate=False):
        args = ["api", "-H", "Accept: application/vnd.github+json", path]
        if paginate:
            args[1:1] = ["--paginate", "--slurp"]
        data = json.loads(self._gh(args))
        return data

    def runs(self, workflow, limit, branch=None, since=None):
        runs, page = [], 1
        while len(runs) < limit:
            query = f"per_page=100&page={page}&status=completed"
            if branch:
                query += f"&branch={branch}"
            if since:
                query += f"&created=>={since}"
            batch = self.api(f"repos/{self.repo}/actions/workflows/{workflow}/runs?{query}")["workflow_runs"]
            if not batch:
                break
            runs.extend(batch)
            page += 1
        return runs[:limit]

    def attempt(self, run_id, attempt):
        return self.api(f"repos/{self.repo}/actions/runs/{run_id}/attempts/{attempt}")

    def attempt_jobs(self, run_id, attempt):
        pages = self.api(f"repos/{self.repo}/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100",
                         paginate=True)
        return [job for page in pages for job in page["jobs"]]

    def annotations(self, job_id):
        return self.api(f"repos/{self.repo}/check-runs/{job_id}/annotations?per_page=100")

    def issues(self):
        pages = self.api(f"repos/{self.repo}/issues?state=all&per_page=100", paginate=True)
        return [i for page in pages for i in page if "pull_request" not in i]

    def pulls(self):
        pages = self.api(f"repos/{self.repo}/pulls?state=all&per_page=100", paginate=True)
        return [p for page in pages for p in page]

    def job_log(self, job_id):
        """Yields the job's log lines as gh streams them."""
        if self._escape_flag is None:
            status, help_text, message = gh_output(["api", "--help"])
            if status != 0:
                raise GitHubError(f"gh api --help: {message or status}")
            self._escape_flag = "--allow-escape-sequences" in help_text
        args = ["api"] + (["--allow-escape-sequences"] if self._escape_flag else [])
        args.append(f"repos/{self.repo}/actions/jobs/{job_id}/logs")
        process, errors = start_gh(args, LOG_DEADLINE)
        read_to_end = False
        try:
            for line in process.stdout:
                yield line
            read_to_end = True
        finally:
            process.stdout.close()
            if not read_to_end:
                # The reader stopped early: end gh now rather than at its
                # deadline. After a full read, stdin stays open until gh has
                # exited, so its own status is the one reported.
                process.stdin.close()
            status, message = finish_gh(process, errors)
        if status != 0:
            raise GitHubError(f"log of job {job_id}: {message or status}")


def platform_of(job):
    labels = " ".join(job.get("labels") or []) + " " + job.get("name", "")
    for family in ("macos", "ubuntu", "windows"):
        if family in labels:
            return family
    return (job.get("labels") or ["unknown"])[0]


class Cache:
    """What a finished job or attempt says, keyed by this script's digest so
    any edit to the parser reads every log again. A finished job's log, and
    the jobs of a finished attempt, never change; a re-run is a new attempt."""

    def __init__(self, directory):
        self.directory = Path(directory) if directory else None
        if self.directory:
            self.directory.mkdir(parents=True, exist_ok=True)

    def _path(self, name):
        return self.directory / f"{name}-{SCRIPT_DIGEST}.json"

    def get(self, name):
        if not self.directory:
            return None
        try:
            return json.loads(self._path(name).read_text())
        except (OSError, ValueError):
            return None

    def put(self, name, value):
        if self.directory:
            path = self._path(name)
            tmp = path.with_name(f"{path.name}.{os.getpid()}.{threading.get_ident()}.tmp")
            tmp.write_text(json.dumps(value))
            tmp.replace(path)


def failed_steps(job):
    return [s["name"] for s in job.get("steps") or [] if s.get("conclusion") == "failure"]


def read_job(github, cache, job):
    """Records for one failed or cancelled job: {"records": [...], "unattributed": bool,
    "cancel": None | "timeout" | "superseded" | "cancelled", "error": None | str}."""
    cached = cache.get(f"job-{job['id']}")
    if cached is not None:
        return cached
    result = {"records": [], "unattributed": False, "cancel": None, "error": None}
    if job["conclusion"] in ("cancelled", "timed_out"):
        if job["conclusion"] == "timed_out":
            result["cancel"] = "timeout"
        else:
            messages = [a.get("message", "") for a in github.annotations(job["id"])]
            if any(TIMEOUT_ANNOTATION in m for m in messages):
                result["cancel"] = "timeout"
            elif any(SUPERSEDED_ANNOTATION in m for m in messages):
                result["cancel"] = "superseded"
            else:
                result["cancel"] = "cancelled"
        if result["cancel"] == "timeout":
            running = [s["name"] for s in job.get("steps") or [] if s.get("conclusion") == "cancelled"]
            result["records"].append(Record(
                "job-timeout", job["name"], "exceeded timeout-minutes",
                f"cancelled in step {running[0]}" if running else "cancelled at its bound",
                token="timeout-minutes").to_dict())
        if not job.get("steps"):
            # Cancelled before a runner took it: no step ran, so there is no
            # output to read and GitHub keeps no log (it answers 404).
            cache.put(f"job-{job['id']}", result)
            return result
    # A log that fails part way is parsed again from its start. A cancelled
    # job's log is read too: a cancel in a later step is the job's result
    # even when its Test step had already failed and printed why.
    try:
        records, unattributed, errors = retried(lambda: parse_lines(github.job_log(job["id"])))
    except GitHubError as error:
        # What the annotations already proved stays; the result is partial,
        # so it is not cached and the next run reads the log again.
        result["error"] = str(error)
        return result
    if not records:
        # A step that failed and named nothing the parser reads (vet, gofmt,
        # lint) is a failure of that step, in a cancelled job too: a cancel
        # in a later step does not undo it. A cancelled job with no failed
        # step is only a cancel.
        steps = failed_steps(job) or ([] if result["cancel"] else ["(no failed step reported)"])
        last = next((e for e in reversed(errors) if not e.startswith(("Process completed", CANCEL_ERROR))),
                    next((e for e in reversed(errors) if e.startswith("Process completed")), ""))
        records = [Record("step", job["name"], f"step {s}", last[:200])
                   for s in steps]
    result["records"] += [r.to_dict() for r in records]
    result["unattributed"] = unattributed
    cache.put(f"job-{job['id']}", result)
    return result


def attempt_of(github, cache, run, attempt):
    """The jobs and conclusion of one finished attempt of a run."""
    name = f"attempt-{run['id']}-{attempt}"
    cached = cache.get(name)
    if cached is not None:
        return cached
    final = run.get("run_attempt") or 1
    jobs = github.attempt_jobs(run["id"], attempt)
    conclusion = run["conclusion"] if attempt == final else github.attempt(run["id"], attempt).get("conclusion")
    value = {"jobs": jobs, "conclusion": conclusion}
    cache.put(name, value)
    return value


def run_jobs(github, cache, run):
    """Every failed or cancelled job of every attempt of one run."""
    final = run.get("run_attempt") or 1
    seen, jobs, attempts, problems = set(), [], [], []
    for attempt in range(1, final + 1):
        try:
            listed = attempt_of(github, cache, run, attempt)
        except GitHubError as error:
            problems.append(f"run {run['id']} attempt {attempt}: {error}")
            continue
        attempts.append({"attempt": attempt, "conclusion": listed["conclusion"]})
        for job in listed["jobs"]:
            if job["id"] in seen:
                continue  # a job listed under more than one attempt is read once
            seen.add(job["id"])
            if job.get("status") != "completed" or job.get("conclusion") in ("success", "skipped", "neutral"):
                continue
            jobs.append((run, job))
    rerun = {"run": run, "attempts": attempts} if final > 1 else None
    return jobs, rerun, problems


def collect(github, cache, runs, workers=4):
    """Every failed job of every attempt of each run, with its records."""
    jobs, reruns, problems = [], [], []
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for found, rerun, trouble in pool.map(lambda run: run_jobs(github, cache, run), runs):
            jobs.extend(found)
            problems.extend(trouble)
            if rerun:
                reruns.append(rerun)
        results = {}
        futures = {pool.submit(read_job, github, cache, job): job["id"] for _, job in jobs}
        for future in concurrent.futures.as_completed(futures):
            job_id = futures[future]
            try:
                results[job_id] = future.result()
            except GitHubError as error:
                results[job_id] = {"records": [], "unattributed": False, "cancel": None, "error": str(error)}
            if results[job_id].get("error"):
                problems.append(results[job_id]["error"])
    return jobs, results, reruns, problems


def matcher(issues):
    """Returns a function from match token to the issues naming it."""
    texts = [(i["number"], i["state"], i.get("title") or "", (i.get("title") or "") + "\n" + (i.get("body") or ""),
              i.get("closed_at")) for i in issues]

    names = [set(re.findall(r"\bTest[A-Za-z0-9_/]{6,}", text)) for _, _, _, text, _ in texts]

    def find(token, prefix=False):
        pattern = re.compile(r"(?<![A-Za-z0-9_])" + re.escape(token) + r"(?![A-Za-z0-9_])")
        found = {"open": [], "closed": []}
        for (number, state, title, text, closed_at), tests in zip(texts, names):
            # Go drops a subtest's slash from its t.TempDir, so an issue naming
            # TestA/sub is found by the token TestAsub.
            near = prefix and any(token.startswith(t.replace("/", "")) for t in tests)
            if near or pattern.search(text):
                item = {"number": number, "title": title}
                if state != "open":
                    item["closed_at"] = closed_at
                found["open" if state == "open" else "closed"].append(item)
        return found
    return find


def build_ledger(repo, workflow, runs, jobs, results, reruns, problems, issues, pulls):
    # Without the issues, which issue names a failure is unknown, not "none".
    find = matcher(issues) if issues is not None else (lambda token, prefix=False: None)
    prs = {}
    for pull in sorted(pulls, key=lambda p: p["number"]):
        prs[pull["head"]["ref"]] = pull["number"]
    entries = {}
    unattributed, cancelled, counted = [], {"superseded": 0, "cancelled": 0}, 0
    for run, job in jobs:
        result = results.get(job["id"]) or {"records": [], "error": "not read"}
        # Set aside only a cancel with nothing in it: no failure, no read
        # problem, and no test output that named nothing.
        if (result.get("cancel") in cancelled and not result["records"] and not result.get("error")
                and not result.get("unattributed")):
            cancelled[result["cancel"]] += 1
            continue
        counted += 1  # a failed job: a failure, a read problem, or output that named nothing
        final = run.get("run_attempt") or 1
        occurrence = {
            "run_id": run["id"], "attempt": job.get("run_attempt", 1), "final_attempt": final,
            "job_id": job["id"], "job": job["name"], "platform": platform_of(job),
            "branch": run.get("head_branch") or "", "event": run.get("event"),
            "pr": prs.get(run.get("head_branch")) if run.get("event") == "pull_request" else None,
            "sha": run.get("head_sha"), "created_at": run.get("created_at"),
            # When the job that failed ended; a completed job always has it.
            "failed_at": job.get("completed_at") or job.get("started_at") or run.get("created_at"),
            "url": job.get("html_url") or run.get("html_url"),
            "run_conclusion": run["conclusion"],
        }
        if result.get("error"):
            # Records known without the log (a timeout's annotation) still count.
            unattributed.append({**occurrence, "reason": f"log unreadable: {result['error']}"})
        elif result.get("unattributed"):
            unattributed.append({**occurrence, "reason": "tests ran but no failure was named"})
        for data in result["records"]:
            record = Record.from_dict(data)
            entry = entries.setdefault(record.key, {
                "key": record.key, "kind": record.kind, "package": record.package,
                "name": record.name, "token": record.token, "prefix": record.prefix, "subtests": set(),
                "details": [], "occurrences": []})
            entry["subtests"].update(record.subtests)
            if record.detail and record.detail not in entry["details"] and len(entry["details"]) < 3:
                entry["details"].append(record.detail)
            entry["occurrences"].append(dict(occurrence))
    failures = []
    for entry in entries.values():
        occ = entry["occurrences"]
        # Hidden: failed in an earlier attempt, and the final attempt, which
        # is what the run's checks show, does not have this failure.
        shown = {o["run_id"] for o in occ if o["attempt"] == o["final_attempt"]}
        for o in occ:
            o["hidden_by_rerun"] = o["attempt"] < o["final_attempt"] and o["run_id"] not in shown
        by_platform, by_branch = {}, {}
        for o in occ:
            by_platform[o["platform"]] = by_platform.get(o["platform"], 0) + 1
            by_branch[o["branch"]] = by_branch.get(o["branch"], 0) + 1
        entry.update({
            "subtests": sorted(entry["subtests"]),
            "count": len(occ),
            "by_platform": by_platform,
            "by_branch": by_branch,
            "on_main": by_branch.get("main", 0),
            "hidden_by_rerun": sum(1 for o in occ if o["hidden_by_rerun"]),
            "first_seen": min(o["failed_at"] for o in occ),
            "last_seen": max(o["failed_at"] for o in occ),
            "issues": find(entry["token"], entry["prefix"]),
        })
        # A closed issue's failure seen on main after it closed: the fix did
        # not hold, or the issue named the wrong cause. A branch's run after
        # the close may predate the fix, so only main counts.
        on_main = [o["failed_at"] for o in occ if o["branch"] == "main"]
        entry["failed_on_main_after_close"] = [
            i["number"] for i in (entry["issues"] or {"closed": []})["closed"]
            if i.get("closed_at") and any(stamp > i["closed_at"] for stamp in on_main)]
        entry["occurrences"].sort(key=lambda o: o["failed_at"], reverse=True)
        failures.append(entry)
    failures.sort(key=lambda e: (-e["count"], -e["on_main"], e["key"]))
    rerun_rows = []
    for item in reruns:
        run = item["run"]
        earlier = [{"attempt": o["attempt"], "job": o["job"], "key": e["key"], "hidden": o["hidden_by_rerun"]}
                   for e in failures for o in e["occurrences"]
                   if o["run_id"] == run["id"] and o["attempt"] < o["final_attempt"]]
        rerun_rows.append({
            "run_id": run["id"], "branch": run.get("head_branch"), "url": run.get("html_url"),
            "created_at": run.get("created_at"), "attempts": item["attempts"],
            "final_conclusion": run["conclusion"],
            "earlier_failures": sorted(earlier, key=lambda f: (f["attempt"], f["job"], f["key"]))})
    return {
        "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "repo": repo, "workflow": workflow,
        "runs_scanned": len(runs),
        "window": {"first": min((r["created_at"] for r in runs), default=None),
                   "last": max((r["created_at"] for r in runs), default=None)},
        "failed_jobs": counted,
        "cancelled_jobs_not_counted": cancelled,
        "failures": failures, "unattributed": unattributed, "reruns": rerun_rows, "problems": problems,
    }


def local_time(stamp, zone):
    if not stamp:
        return ""
    moment = datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    return moment.astimezone(zone).strftime("%m-%d %H:%M %Z")


def issue_text(entry):
    found = entry["issues"]
    if found is None:
        return "unknown (the issues could not be read)"
    parts = [f"#{i['number']}" for i in found["open"]]
    parts += [f"~~#{i['number']}~~" + (" **failed on main since it closed**"
                                       if i["number"] in entry["failed_on_main_after_close"] else "")
              for i in found["closed"]]
    if parts:
        return " ".join(parts)
    # A test is matched by its own name; say what else would match.
    return "**none**" if entry["token"] == entry["name"] else f"**none** (matched by `{entry['token']}`)"


def cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def markdown(ledger, zone):
    out = []
    window = ledger["window"]
    out.append(f"# Flake ledger: {ledger['repo']} {ledger['workflow']}")
    out.append("")
    out.append(f"{ledger['runs_scanned']} completed runs, {local_time(window['first'], zone)} to "
               f"{local_time(window['last'], zone)}; every attempt read. {ledger['failed_jobs']} failed "
               f"job(s); not counted: {ledger['cancelled_jobs_not_counted']['superseded']} superseded by a "
               f"newer run, {ledger['cancelled_jobs_not_counted']['cancelled']} cancelled by hand.")
    out.append("")
    if ledger["problems"] or ledger["unattributed"]:
        out.append("## Incomplete: read these first")
        out.append("")
        for problem in ledger["problems"]:
            out.append(f"- read failed: {cell(problem)}")
        for item in ledger["unattributed"]:
            out.append(f"- run {item['run_id']} attempt {item['attempt']} {item['job']} ({item['branch']}): "
                       f"{item['reason']} {item['url']}")
        out.append("")
    out.append("## Failures")
    out.append("")
    out.append("One row per failure, most frequent first; each count is failed jobs. *Hidden* counts failures "
               "from an earlier attempt that the run's final attempt does not show; *last seen* is when the "
               "job that failed ended. Struck-through issues are closed.")
    out.append("")
    out.append("| Count | Failure | Kind | ubuntu | macos | main | hidden | other branches | last seen | issues |")
    out.append("|---:|---|---|---:|---:|---:|---:|---|---|---|")
    for entry in ledger["failures"]:
        others = sorted(((n, b) for b, n in entry["by_branch"].items() if b != "main"), reverse=True)
        other = ", ".join(f"{b} ({n})" for n, b in others[:3]) + (f", +{len(others) - 3}" if len(others) > 3 else "")
        name = f"{entry['package']} `{entry['name']}`"
        if entry["subtests"]:
            name += " (" + ", ".join(f"`{s.split('/', 1)[1]}`" for s in entry["subtests"][:3])
            name += f", +{len(entry['subtests']) - 3})" if len(entry["subtests"]) > 3 else ")"
        out.append("| " + " | ".join(cell(c) for c in [
            entry["count"], name, entry["kind"], entry["by_platform"].get("ubuntu", 0),
            entry["by_platform"].get("macos", 0), entry["on_main"], entry["hidden_by_rerun"] or "-",
            other or "-", local_time(entry["last_seen"], zone), issue_text(entry)]) + " |")
    if not ledger["failures"]:
        out.append("| 0 | no failures in this window | | | | | | | | |")
    out.append("")
    if ledger["reruns"]:
        out.append("## Re-run attempts")
        out.append("")
        for row in ledger["reruns"]:
            attempts = ", ".join(f"{a['attempt']}: {a['conclusion']}" for a in row["attempts"])
            out.append(f"- run [{row['run_id']}]({row['url']}) on {row['branch']}, "
                       f"{local_time(row['created_at'], zone)}: {attempts}")
            for failure in row["earlier_failures"]:
                hidden = " (hidden: the final attempt does not show it)" if failure["hidden"] else ""
                out.append(f"  - attempt {failure['attempt']}, {failure['job']}: {failure['key']}{hidden}")
        out.append("")
    return "\n".join(out)


def default_cache():
    base = os.environ.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")
    return os.path.join(base, "assistant-flake-ledger")


def detect_repo():
    status, output, message = gh_output(["repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"])
    if status != 0 or not output.strip():
        raise GitHubError(f"cannot tell the repository; pass --repo ({message})")
    return output.strip()


def zone_of(name):
    if not name:
        return datetime.datetime.now().astimezone().tzinfo
    from zoneinfo import ZoneInfo
    return ZoneInfo(name)


def report(args, github=None):
    github = github or GitHub(args.repo or detect_repo())
    runs = github.runs(args.workflow, args.limit, branch=args.branch, since=args.since)
    if not runs:
        print(f"flake-ledger: no completed runs of {args.workflow} in {github.repo}", file=sys.stderr)
        return 2
    cache = Cache(None if args.no_cache else args.cache)
    jobs, results, reruns, problems = collect(github, cache, runs)
    issues, pulls = None, []
    try:
        issues = github.issues()
    except GitHubError as error:
        problems.append(f"issues: {error}")
    try:
        pulls = github.pulls()
    except GitHubError as error:
        problems.append(f"pull requests: {error}")
    ledger = build_ledger(github.repo, args.workflow, runs, jobs, results, reruns, problems, issues, pulls)
    if args.json:
        Path(args.json).write_text(json.dumps(ledger, indent=2) + "\n")
    if args.format == "json":
        print(json.dumps(ledger, indent=2))
    else:
        print(markdown(ledger, zone_of(args.tz)))
    if ledger["problems"] or any(u["reason"].startswith("log unreadable") for u in ledger["unattributed"]):
        return 2
    if ledger["unattributed"]:
        return 3
    return 0


def parse_command(args):
    status = 0
    for name in args.logs:
        with open(name, encoding="utf-8", errors="replace") as handle:
            records, unattributed, _ = parse_lines(handle, raw=args.raw)
        for record in records:
            print(json.dumps({"log": name, **record.to_dict()}))
        if unattributed:
            print(f"flake-ledger: {name}: tests ran but no failure was named", file=sys.stderr)
            status = 3
    return status


def main(argv=None, github=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command")
    parse = sub.add_parser("parse", help="print the failure records of local log files as JSON lines")
    parse.add_argument("--raw", action="store_true", help="the logs are plain `go test -v` output")
    parse.add_argument("logs", nargs="+")
    parser.add_argument("--repo", help="owner/name (default: the current checkout's GitHub repository)")
    parser.add_argument("--workflow", default="ci.yml")
    parser.add_argument("--limit", type=int, default=60, help="most recent completed runs to read")
    parser.add_argument("--branch", help="only runs of this branch")
    parser.add_argument("--since", help="only runs created on or after this date (YYYY-MM-DD)")
    parser.add_argument("--format", choices=["markdown", "json"], default="markdown")
    parser.add_argument("--json", metavar="PATH", help="also write the JSON ledger here")
    parser.add_argument("--tz", help="time zone for the markdown (default: this machine's)")
    parser.add_argument("--cache", default=default_cache(), help="parsed-log cache directory")
    parser.add_argument("--no-cache", action="store_true")
    args = parser.parse_args(argv)
    try:
        if args.command == "parse":
            return parse_command(args)
        return report(args, github)
    except GitHubError as error:
        print(f"flake-ledger: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
