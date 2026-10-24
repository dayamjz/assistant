#!/usr/bin/env python3
"""Count every failure in recent CI runs, by test, platform and branch.

Run: GH_TOKEN=$(gh auth token) python3 scripts/flake-ledger.py --repo owner/name [--limit 60] [--json ledger.json]
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

This is the first version, kept small on purpose: it names what it can read
plainly and reports everything else in a failed job as not attributed. From
a failed or cancelled job it reads:
- A Go test that failed, from a `--- FAIL:` line at column 0 in the Test
  step's `full test output` group (scripts/ci-test.sh prints the group only
  when the step fails). go test -v prints a top-level test's result line at
  column 0, and what a test writes through t.Log or t.Output is indented, so
  output a test logs, another go test's included, is not read as a failure;
  text a test writes to stdout itself, unindented, can be. Subtests are not
  read.
- The top-level tests a `panic: test timed out` lists as running.
- A package that failed with no test named: `[setup failed]` or
  `[build failed]` as a build failure, otherwise a package failure, with its
  first `panic:` line when it printed one.
- Leftovers the leak check reported in the group (any `LEAK` line, or a
  `leak-check: N leftover(s)` count above zero): one record for the job; the
  leftovers themselves are not read.
- An error the Test step reported (an `##[error]` line in it other than its
  exit line), by its message. The Test step is told by its command
  (TEST_COMMANDS).
- Any failed step other than Test, by its name.
- A job cancelled at its timeout-minutes bound, from its annotations. A
  cancelled job with nothing failed in it (superseded by a newer run of the
  same ref, or cancelled otherwise) is only counted.

It is loud where it cannot see: a read that fails makes it exit 2 (a failed
issue read leaves each failure's issues unknown, never "none"), and a failed
or cancelled job whose Test step failed, or that names no failed step, with
none of the above read in it makes it exit 3 (listed as not attributed). Both
still print the ledger.

It reads the GitHub REST API over HTTPS in this process (API_URL, which is
GITHUB_API_URL in GitHub Actions), with the token from GH_TOKEN or
GITHUB_TOKEN, and starts no other program. Each read waits at most
READ_TIMEOUT for any one piece of data, and at most API_DEADLINE in all (a
job's log LOG_DEADLINE), enforced by an interval timer; so the ledger runs
in one thread, on a platform with SIGALRM (Linux and macOS). A job's log is
served from a signed address GitHub redirects to; the token is sent only to
the API's own address.

Issues are matched by text: an issue whose title or body contains a record's
match token is that record's issue. A test's token is its top-level name,
matched as a word and not after a slash, where it would be another test's
subtest; any other record's token is its key. The markdown prints each
token, so an issue can be linked by writing it into the issue.
"""

import argparse
import contextlib
import datetime
import http.client
import json
import os
from pathlib import Path
import re
import signal
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

pause = time.sleep  # between tries of a read GitHub failed with a server error

ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
STAMP = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ")
GROUP_START = "##[group]full test output"
GROUP_END = "##[endgroup]"
STEP_START = "##[group]Run "
# The step that runs the suite, by name in the jobs API, and in the log by
# its command: scripts/ci-test.sh from 2026-10-24 (#574), before that a
# script of ci.yml's own whose first line no other step's shares.
TEST_STEP = "Test"
TEST_COMMANDS = ("bash scripts/ci-test.sh", "set -o pipefail")
GO_FAIL = re.compile(r"^--- FAIL: (\S+)")
GO_PACKAGE_END = re.compile(r"^(ok|FAIL)\s+(\S+)(?:\s+(\[[^\]]+\]|\S+))?")
GO_TIMEOUT = re.compile(r"^panic: test timed out after (\S+)")
GO_RUNNING = re.compile(r"^\s+(Test\S*|Example\S*|Fuzz\S*|Benchmark\S*) \(")
GO_PANIC = re.compile(r"^panic: (.*)")
LEAK_LINE = re.compile(r"^LEAK ")
LEAK_TOTAL = re.compile(r"^leak-check: (\d+) leftover\(s\); command exit=")
ERROR_LINE = re.compile(r"^##\[error\](.*)")
TIMEOUT_ANNOTATION = "exceeded the maximum execution time"
SUPERSEDED_ANNOTATION = "higher priority waiting request"
# The error lines a step's end prints: a run step that failed, and a cancel.
STEP_END_ERRORS = ("Process completed with exit code", "The operation was canceled")

API_URL = os.environ.get("GITHUB_API_URL") or "https://api.github.com"
# Seconds one read may wait for any one piece of data, and for a whole API
# response or job log. A read that runs out counts as a server error, so
# retried() may make it twice more.
READ_TIMEOUT = 30
API_DEADLINE = 120
LOG_DEADLINE = 300
REDIRECTS = (301, 302, 303, 307, 308)
# What reading a response can raise: the socket's errors, and http.client's
# for a malformed or cut-off answer (IncompleteRead, which is not an OSError).
READ_ERRORS = (OSError, http.client.HTTPException)


def clean(line):
    """Strip the BOM, GitHub's timestamp prefix and terminal escapes."""
    line = line.rstrip("\r\n").lstrip("\ufeff")
    return ANSI.sub("", STAMP.sub("", line, count=1))


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

    def __init__(self, kind, package, name, detail="", token=None):
        self.kind = kind
        self.package = package
        self.name = name
        self.detail = detail
        self.token = token or self.key

    @property
    def key(self):
        return f"{self.kind}: {self.package} {self.name}".rstrip()

    def to_dict(self):
        return {"kind": self.kind, "package": self.package, "name": self.name,
                "detail": self.detail, "token": self.token}

    @classmethod
    def from_dict(cls, data):
        return cls(data["kind"], data["package"], data["name"], data.get("detail", ""), data.get("token"))


class LogParser:
    """Turns a job log, line by line, into failure records.

    raw=True reads plain `go test -v` output, as if all of it were the full
    test output group.
    """

    def __init__(self, raw=False):
        self.raw = raw
        self.in_group = raw
        self.in_test_step = False
        self.saw_group = False
        self.group_lines = 0
        self.records = []
        self.errors = []  # what the Test step reported on ##[error] lines
        self.leftovers = ""  # the first leftover line the leak check printed
        self._new_package()

    def _new_package(self):
        # Per Go package's output, ended by its ok or FAIL line.
        self.failed = []  # top-level tests a column-0 --- FAIL line named
        self.timeout = None  # (after, [running tests]) once a timeout panic is seen
        self.reading_running = False
        self.panic = ""
        self.leaked = False

    def feed(self, line):
        line = clean(line)
        if not self.raw:
            if line.startswith(GROUP_START):
                self.in_group = self.saw_group = True
                return
            if self.in_group and line.startswith(GROUP_END):
                self.in_group = False
                return
            if line.startswith(STEP_START):
                self.in_test_step = line[len(STEP_START):].strip() in TEST_COMMANDS
                return
        if self.in_group:
            self.group_lines += 1
            self._go_line(line)
            return
        error = ERROR_LINE.match(line)
        if error and self.in_test_step and not error.group(1).startswith(STEP_END_ERRORS):
            self.errors.append(error.group(1).strip())

    def _go_line(self, line):
        if self.reading_running:
            running = GO_RUNNING.match(line)
            if running:
                self.timeout[1].append(running.group(1))
                return
            if line.strip() in ("", "running tests:"):
                return
            self.reading_running = False
        failed = GO_FAIL.match(line)
        if failed:
            if failed.group(1) not in self.failed:
                self.failed.append(failed.group(1))
            return
        timeout = GO_TIMEOUT.match(line)
        if timeout:
            self.timeout = (timeout.group(1), [])
            self.reading_running = True
            return
        panic = GO_PANIC.match(line)
        if panic:
            self.panic = self.panic or panic.group(1)[:200]
            return
        total = LEAK_TOTAL.match(line)
        if LEAK_LINE.match(line) or (total and int(total.group(1)) > 0):
            self.leftovers = self.leftovers or line[:200]
            self.leaked = True
            return
        end = GO_PACKAGE_END.match(line)
        if end and "/" in end.group(2):
            if end.group(1) == "FAIL":
                self._fail_package(short_package(end.group(2)), end.group(3) or "")
            self._new_package()

    def _fail_package(self, package, suffix):
        before = len(self.records)
        tops = []
        if self.timeout:
            after, running = self.timeout
            tops = list(dict.fromkeys(t.split("/", 1)[0] for t in running))
            for top in tops:
                self.records.append(Record("timeout", package, top, f"test timed out after {after}", token=top))
            if not tops:
                self.records.append(Record("timeout", package, "(package)", f"test timed out after {after}"))
        for name in self.failed:
            if name not in tops:
                self.records.append(Record("test", package, name, token=name))
        if len(self.records) > before:
            return
        if suffix.startswith("["):
            self.records.append(Record("build", package, suffix.strip("[]")))
        elif self.panic:
            self.records.append(Record("package", package, "(package)", f"panic: {self.panic}"))
        elif self.leaked:
            self.records.append(Record("package", package, "(package)", "the leak check failed it"))
        else:
            self.records.append(Record("package", package, "(package)", "no failing test was named"))

    def finish(self):
        """The records, and whether the test output was printed but named nothing."""
        if self.leftovers:
            self.records.append(Record("leak", "(leak check)", "leftovers, not read by this version",
                                       self.leftovers))
        for message in self.errors:
            self.records.append(Record("error", "(Test step)", message[:120], message[:200]))
        unique = {}
        for record in self.records:
            unique.setdefault(record.key, record)
        self.records = list(unique.values())
        printed = self.saw_group or (self.raw and self.group_lines)
        return self.records, bool(printed) and not self.records


def parse_lines(lines, raw=False):
    parser = LogParser(raw=raw)
    for line in lines:
        parser.feed(line)
    return parser.finish()


class GitHubError(Exception):
    """A read of GitHub failed; the ledger is incomplete without its answer."""

    @property
    def transient(self):
        return bool(re.search(r"HTTP 5\d\d|HTTP 429|timed out|connection reset|incomplete read", str(self)))


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


@contextlib.contextmanager
def deadline(seconds, what):
    """Raises GitHubError in the code it wraps once seconds have passed,
    whatever that code waits on: an interval timer, so only in the main
    thread."""
    def expire(signum, frame):
        raise GitHubError(f"{what}: timed out after {seconds:g}s")
    previous = signal.signal(signal.SIGALRM, expire)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """A redirect is the caller's to follow, so the token never goes along."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_error(what, error):
    """A GitHubError for an exception reading a response."""
    if isinstance(error, http.client.IncompleteRead):
        return GitHubError(f"{what}: incomplete read ({len(error.partial)} bytes, {error.expected} more expected)")
    return GitHubError(f"{what}: {getattr(error, 'reason', error)}")


def next_link(header):
    """The rel="next" address of a Link header, or None."""
    for part in (header or "").split(","):
        match = re.match(r'\s*<([^>]+)>\s*;\s*rel="next"', part)
        if match:
            return match.group(1)
    return None


class GitHub:
    """The few GitHub reads the ledger makes, each bounded (READ_TIMEOUT and
    API_DEADLINE or LOG_DEADLINE)."""

    def __init__(self, repo, token, api_url=None):
        self.repo = repo
        self.token = token
        self.api_url = (api_url or API_URL).rstrip("/")
        self.origin = urllib.parse.urlsplit(self.api_url)[:2]
        self.opener = urllib.request.build_opener(_NoRedirect)

    def _open(self, url, what, auth):
        """The response to a GET of url; a redirect is returned, not followed."""
        headers = {"Accept": "application/vnd.github+json", "User-Agent": "assistant-flake-ledger",
                   "X-GitHub-Api-Version": "2022-11-28"}
        if auth:
            if urllib.parse.urlsplit(url)[:2] != self.origin:
                raise GitHubError(f"{what}: {url} is not the API's address, so the token is not sent there")
            headers["Authorization"] = f"Bearer {self.token}"
        try:
            return self.opener.open(urllib.request.Request(url, headers=headers), timeout=READ_TIMEOUT)
        except urllib.error.HTTPError as error:
            if error.code in REDIRECTS:
                return error
            error.close()
            raise GitHubError(f"{what}: HTTP {error.code}") from None
        except (urllib.error.URLError, *READ_ERRORS) as error:
            raise read_error(what, error) from None

    def _json(self, url):
        """(body, next page's address) of one API response, read again on a
        server error."""
        what = url[len(self.api_url) + 1:] if url.startswith(self.api_url) else url

        def read():
            with deadline(API_DEADLINE, what):
                response = self._open(url, what, auth=True)
                try:
                    if response.status in REDIRECTS:
                        raise GitHubError(f"{what}: HTTP {response.status}")
                    try:
                        body = response.read()  # raises IncompleteRead on a body cut short
                    except READ_ERRORS as error:
                        raise read_error(what, error) from None
                    link = response.headers.get("Link")
                finally:
                    response.close()
            try:
                return json.loads(body), next_link(link)
            except ValueError:
                raise GitHubError(f"{what}: the answer is not JSON") from None
        return retried(read)

    def api(self, path):
        return self._json(f"{self.api_url}/{path}")[0]

    def pages(self, path):
        """Every page of a listing, following GitHub's Link header."""
        items, url = [], f"{self.api_url}/{path}"
        while url:
            body, url = self._json(url)
            items.append(body)
        return items

    def runs(self, workflow, limit, branch=None, since=None):
        # Encoded, so a branch name with + # & or % in it is asked for as it is.
        query = {"per_page": 100, "status": "completed"}
        if branch:
            query["branch"] = branch
        if since:
            query["created"] = f">={since}"
        runs = []
        url = (f"{self.api_url}/repos/{self.repo}/actions/workflows/{urllib.parse.quote(workflow, safe='')}/runs?"
               f"{urllib.parse.urlencode(query)}")
        while url and len(runs) < limit:
            body, url = self._json(url)
            runs.extend(body["workflow_runs"])
        return runs[:limit]

    def attempt(self, run_id, attempt):
        return self.api(f"repos/{self.repo}/actions/runs/{run_id}/attempts/{attempt}")

    def attempt_jobs(self, run_id, attempt):
        pages = self.pages(f"repos/{self.repo}/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100")
        return [job for page in pages for job in page["jobs"]]

    def annotations(self, job_id):
        return self.api(f"repos/{self.repo}/check-runs/{job_id}/annotations?per_page=100")

    def issues(self):
        pages = self.pages(f"repos/{self.repo}/issues?state=all&per_page=100")
        return [i for page in pages for i in page if "pull_request" not in i]

    def job_log(self, job_id):
        """Yields the job's log lines as they arrive. GitHub answers with a
        redirect to the log's own signed address, which is read without the
        token. A log cut off part way raises once its end is reached (a
        chunked one when its closing chunk never comes, one with a
        Content-Length when bytes it promised are missing), so the caller
        never takes part of a log for all of it. The caller bounds the whole
        read (read_job)."""
        what = f"log of job {job_id}"
        response = self._open(f"{self.api_url}/repos/{self.repo}/actions/jobs/{job_id}/logs", what, auth=True)
        try:
            if response.status in REDIRECTS:
                location = response.headers.get("Location")
                response.close()
                if not location:
                    raise GitHubError(f"{what}: HTTP {response.status} with no Location")
                response = self._open(urllib.parse.urljoin(self.api_url + "/", location), what, auth=False)
                if response.status in REDIRECTS:
                    raise GitHubError(f"{what}: HTTP {response.status} from the log's own address")
            pending = b""
            while True:
                # read(), not readline(): readline ends quietly at a cut-off
                # chunked body, where read() raises.
                try:
                    chunk = response.read(65536)
                except READ_ERRORS as error:
                    raise read_error(what, error) from None
                if not chunk:
                    break
                *lines, pending = (pending + chunk).split(b"\n")
                for raw in lines:
                    yield raw.decode("utf-8", "replace") + "\n"
            if response.length:
                # A Content-Length's remainder: read() ends quietly there.
                raise GitHubError(f"{what}: incomplete read ({response.length} more bytes expected)")
            if pending:
                yield pending.decode("utf-8", "replace")
        finally:
            response.close()


def platform_of(job):
    labels = " ".join(job.get("labels") or []) + " " + job.get("name", "")
    for family in ("macos", "ubuntu", "windows"):
        if family in labels:
            return family
    return (job.get("labels") or ["unknown"])[0]


def failed_steps(job):
    return [s["name"] for s in job.get("steps") or [] if s.get("conclusion") == "failure"]


def read_job(github, job):
    """Records for one failed or cancelled job: {"records": [...], "unattributed": bool,
    "cancel": None | "timeout" | "superseded" | "cancelled", "error": None | str}."""
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
            return result
    failed = failed_steps(job)
    for name in failed:
        if name != TEST_STEP:
            result["records"].append(Record("step", job["name"], f"step {name}").to_dict())

    def read():
        # A log that fails part way is read again from its start.
        with deadline(LOG_DEADLINE, f"log of job {job['id']}"):
            return parse_lines(github.job_log(job["id"]))
    try:
        records, printed_nothing = retried(read)
    except GitHubError as error:
        # What the annotations and steps already proved stays.
        result["error"] = str(error)
        return result
    result["records"] += [r.to_dict() for r in records]
    # The Test step failed, or the job failed naming no step, and the log
    # explained none of it.
    test_failed = TEST_STEP in failed or (not failed and result["cancel"] is None)
    result["unattributed"] = printed_nothing or (test_failed and not records)
    return result


def run_jobs(github, run):
    """Every failed or cancelled job of every attempt of one run."""
    final = run.get("run_attempt") or 1
    seen, jobs, attempts, problems = set(), [], [], []
    for attempt in range(1, final + 1):
        try:
            listed = github.attempt_jobs(run["id"], attempt)
            conclusion = run["conclusion"] if attempt == final else github.attempt(run["id"], attempt).get("conclusion")
        except GitHubError as error:
            problems.append(f"run {run['id']} attempt {attempt}: {error}")
            continue
        attempts.append({"attempt": attempt, "conclusion": conclusion})
        for job in listed:
            if job["id"] in seen:
                continue  # a job listed under more than one attempt is read once
            seen.add(job["id"])
            if job.get("status") != "completed" or job.get("conclusion") in ("success", "skipped", "neutral"):
                continue
            jobs.append((run, job))
    rerun = {"run": run, "attempts": attempts} if final > 1 else None
    return jobs, rerun, problems


def collect(github, runs):
    """Every failed job of every attempt of each run, with its records."""
    jobs, reruns, problems, results = [], [], [], {}
    for run in runs:
        found, rerun, trouble = run_jobs(github, run)
        jobs.extend(found)
        problems.extend(trouble)
        if rerun:
            reruns.append(rerun)
    for _, job in jobs:
        try:
            results[job["id"]] = read_job(github, job)
        except GitHubError as error:
            results[job["id"]] = {"records": [], "unattributed": False, "cancel": None, "error": str(error)}
        if results[job["id"]].get("error"):
            problems.append(results[job["id"]]["error"])
    return jobs, results, reruns, problems


def matcher(issues):
    """Returns find(token, test) -> the issues naming token. A test's token
    (test=True) is not matched after a slash, where it names another test's
    subtest; any other token may follow one, as a program's name follows a
    path."""
    texts = [(i["number"], i["state"], i.get("title") or "", (i.get("title") or "") + "\n" + (i.get("body") or ""),
              i.get("closed_at")) for i in issues]

    def find(token, test=False):
        before = r"(?<![A-Za-z0-9_/])" if test else r"(?<![A-Za-z0-9_])"
        pattern = re.compile(before + re.escape(token) + r"(?![A-Za-z0-9_])")
        found = {"open": [], "closed": []}
        for number, state, title, text, closed_at in texts:
            if pattern.search(text):
                item = {"number": number, "title": title}
                if state != "open":
                    item["closed_at"] = closed_at
                found["open" if state == "open" else "closed"].append(item)
        return found
    return find


def branch_of(run, repo):
    """The run's branch, with its owner when it is a fork's (someone:main)."""
    branch = run.get("head_branch") or ""
    head = (run.get("head_repository") or {}).get("full_name")
    if head and head.lower() != repo.lower():  # GitHub's names ignore case
        return f"{head.split('/')[0]}:{branch}"
    return branch


def build_ledger(repo, workflow, runs, jobs, results, reruns, problems, issues):
    # Without the issues, which issue names a failure is unknown, not "none".
    find = matcher(issues) if issues is not None else (lambda token, test=False: None)
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
        counted += 1
        final = run.get("run_attempt") or 1
        occurrence = {
            "run_id": run["id"], "attempt": job.get("run_attempt", 1), "final_attempt": final,
            "job_id": job["id"], "job": job["name"], "platform": platform_of(job),
            "branch": branch_of(run, repo), "event": run.get("event"),
            "sha": run.get("head_sha"), "created_at": run.get("created_at"),
            # When the job that failed ended; a completed job always has it.
            "failed_at": job.get("completed_at") or job.get("started_at") or run.get("created_at"),
            "url": job.get("html_url") or run.get("html_url"),
            "run_conclusion": run["conclusion"],
        }
        if result.get("error"):
            unattributed.append({**occurrence, "reason": f"not read: {result['error']}"})
        elif result.get("unattributed"):
            unattributed.append({**occurrence, "reason": "the Test step failed and nothing in its log was named"})
        for data in result["records"]:
            record = Record.from_dict(data)
            entry = entries.setdefault(record.key, {
                "key": record.key, "kind": record.kind, "package": record.package,
                "name": record.name, "token": record.token, "details": [], "occurrences": []})
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
            "count": len(occ),
            "by_platform": by_platform,
            "by_branch": by_branch,
            "hidden_by_rerun": sum(1 for o in occ if o["hidden_by_rerun"]),
            "first_seen": min(o["failed_at"] for o in occ),
            "last_seen": max(o["failed_at"] for o in occ),
            "issues": find(entry["token"], entry["kind"] in ("test", "timeout")),
        })
        entry["occurrences"].sort(key=lambda o: o["failed_at"], reverse=True)
        failures.append(entry)
    failures.sort(key=lambda e: (-e["count"], e["key"]))
    rerun_rows = []
    for item in reruns:
        run = item["run"]
        earlier = [{"attempt": o["attempt"], "job": o["job"], "key": e["key"], "hidden": o["hidden_by_rerun"]}
                   for e in failures for o in e["occurrences"]
                   if o["run_id"] == run["id"] and o["attempt"] < o["final_attempt"]]
        rerun_rows.append({
            "run_id": run["id"], "branch": branch_of(run, repo), "url": run.get("html_url"),
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
    parts = [f"#{i['number']}" for i in found["open"]] + [f"~~#{i['number']}~~" for i in found["closed"]]
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
               f"newer run, {ledger['cancelled_jobs_not_counted']['cancelled']} cancelled for another reason "
               "(by hand, or before it started).")
    out.append("")
    if ledger["problems"] or ledger["unattributed"]:
        out.append("## Incomplete: read these first")
        out.append("")
        for problem in ledger["problems"]:
            out.append(f"- read failed: {cell(problem)}")
        for item in ledger["unattributed"]:
            out.append(f"- not attributed: run {item['run_id']} attempt {item['attempt']} {item['job']} "
                       f"({item['branch']}): {item['reason']} {item['url']}")
        out.append("")
    out.append("## Failures")
    out.append("")
    out.append("One row per failure, most frequent first; each count is failed jobs. *Hidden* counts failures "
               "from an earlier attempt that the run's final attempt does not show; *last seen* is when the "
               "job that failed ended. Struck-through issues are closed.")
    out.append("")
    out.append("| Count | Failure | Kind | ubuntu | macos | hidden | branches | last seen | issues |")
    out.append("|---:|---|---|---:|---:|---:|---|---|---|")
    for entry in ledger["failures"]:
        branches = sorted(((n, b) for b, n in entry["by_branch"].items()), reverse=True)
        shown = ", ".join(f"{b} ({n})" for n, b in branches[:3])
        if len(branches) > 3:
            shown += f", +{len(branches) - 3}"
        out.append("| " + " | ".join(cell(c) for c in [
            entry["count"], f"{entry['package']} `{entry['name']}`", entry["kind"],
            entry["by_platform"].get("ubuntu", 0), entry["by_platform"].get("macos", 0),
            entry["hidden_by_rerun"] or "-", shown or "-", local_time(entry["last_seen"], zone),
            issue_text(entry)]) + " |")
    if not ledger["failures"]:
        out.append("| 0 | no failures in this window | | | | | | | |")
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


def zone_of(name):
    if not name:
        return datetime.datetime.now().astimezone().tzinfo
    from zoneinfo import ZoneInfo
    return ZoneInfo(name)


def report(args, github):
    runs = github.runs(args.workflow, args.limit, branch=args.branch, since=args.since)
    if not runs:
        print(f"flake-ledger: no completed runs of {args.workflow} in {github.repo}", file=sys.stderr)
        return 2
    jobs, results, reruns, problems = collect(github, runs)
    issues = None
    try:
        issues = github.issues()
    except GitHubError as error:
        problems.append(f"issues: {error}")
    ledger = build_ledger(github.repo, args.workflow, runs, jobs, results, reruns, problems, issues)
    if args.json:
        Path(args.json).write_text(json.dumps(ledger, indent=2) + "\n")
    if args.format == "json":
        print(json.dumps(ledger, indent=2))
    else:
        print(markdown(ledger, zone_of(args.tz)))
    if ledger["problems"]:
        return 2
    if ledger["unattributed"]:
        return 3
    return 0


def parse_command(args):
    status = 0
    for name in args.logs:
        with open(name, encoding="utf-8", errors="replace") as handle:
            records, printed_nothing = parse_lines(handle, raw=args.raw)
        for record in records:
            print(json.dumps({"log": name, **record.to_dict()}))
        if printed_nothing:
            print(f"flake-ledger: {name}: the test output named no failure", file=sys.stderr)
            status = 3
    return status


def main(argv=None, api_url=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command")
    parse = sub.add_parser("parse", help="print the failure records of local log files as JSON lines")
    parse.add_argument("--raw", action="store_true", help="the logs are plain `go test -v` output")
    parse.add_argument("logs", nargs="+")
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"),
                        help="owner/name (default: GITHUB_REPOSITORY)")
    parser.add_argument("--workflow", default="ci.yml")
    parser.add_argument("--limit", type=int, default=60, help="most recent completed runs to read")
    parser.add_argument("--branch", help="only runs of this branch")
    parser.add_argument("--since", help="only runs created on or after this date (YYYY-MM-DD)")
    parser.add_argument("--format", choices=["markdown", "json"], default="markdown")
    parser.add_argument("--json", metavar="PATH", help="also write the JSON ledger here")
    parser.add_argument("--tz", help="time zone for the markdown (default: this machine's)")
    args = parser.parse_args(argv)
    if args.command == "parse":
        return parse_command(args)
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if not token:
        print("flake-ledger: set GH_TOKEN or GITHUB_TOKEN to a token that can read the repository "
              "(for example GH_TOKEN=$(gh auth token))", file=sys.stderr)
        return 2
    if not args.repo:
        print("flake-ledger: pass --repo owner/name (GITHUB_REPOSITORY is not set)", file=sys.stderr)
        return 2
    try:
        return report(args, GitHub(args.repo, token, api_url))
    except GitHubError as error:
        print(f"flake-ledger: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("flake-ledger: interrupted", file=sys.stderr)
        sys.exit(130)
