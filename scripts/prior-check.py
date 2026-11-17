#!/usr/bin/env python3
"""Whether a push-to-main check job can skip because a pull request already
ran the same check successfully on this commit.

The fleet lands through a batch pull request whose CI ran on exactly the
commit that main is then fast-forwarded to. Re-running the suite on that
push duplicates work the required checks already passed. This script reads
the commit's check runs through the GitHub API and says whether a completed
successful run of the named check came from a pull_request event other than
the current workflow run.

  python3 scripts/prior-check.py \\
    --repo "$GITHUB_REPOSITORY" --sha "$GITHUB_SHA" \\
    --check-name "check (${{ matrix.os }})" --run-id "$GITHUB_RUN_ID" \\
    --event-name "$EVENT" --ref "$REF" --github-output "$GITHUB_OUTPUT"

Outputs run=true to run the job's steps, or run=false to skip them and
still report SUCCESS under the expanded check name. A prior job success
counts only when its Test step concluded success (the suite actually
ran); a docs-only skip leaves Test skipped and must not authorize
skipping on main. Fail open: any API or parse problem sets run=true.
Permissions: checks:read, actions:read.
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

API_URL = os.environ.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
RUN_URL = re.compile(r"/actions/runs/(\d+)(?:/|$)")
JOB_URL = re.compile(r"/job/(\d+)(?:/|$)")
TEST_STEP = "Test"
# IncompleteRead is an HTTPException, not an OSError; both must become
# APIError so main can fail closed to run=true (#708 review).
READ_ERRORS = (OSError, TimeoutError, http.client.HTTPException)


class APIError(Exception):
    """A GitHub API read that cannot be used to decide."""


def field(body, key: str, kind, call: str):
    """body[key] when body is an object whose key holds a kind (a str must
    be non-empty), else APIError naming call: an absent field is never read
    as an empty answer."""
    value = body.get(key) if isinstance(body, dict) else None
    if not isinstance(value, kind) or (kind is str and not value):
        raise APIError(f"{call}: expected an object with {kind.__name__} {key}")
    return value


def conclusion(item, call: str):
    """item's conclusion: a str, or None for one not finished. A missing key
    or any other value is an APIError naming call, so an incomplete answer
    never reads as "not success"."""
    value = item.get("conclusion", ...) if isinstance(item, dict) else ...
    if value is not None and not isinstance(value, str):
        raise APIError(f"{call}: expected an object with a conclusion (str or null)")
    return value


def run_id_from_url(html_url: str) -> int | None:
    match = RUN_URL.search(html_url or "")
    return int(match.group(1)) if match else None


def job_id_from_url(html_url: str) -> int | None:
    match = JOB_URL.search(html_url or "")
    return int(match.group(1)) if match else None


def suite_ran(steps) -> bool:
    """True when the job's Test step concluded success (suite executed)."""
    for step in steps or []:
        if step.get("name") == TEST_STEP and step.get("conclusion") == "success":
            return True
    return False


def decide(check_runs, workflow_event, suite_ran_jobs, current_run_id: int):
    """Return (skip, reason). skip is True only for a successful check run
    from a pull_request workflow run that is not the current one and whose
    Test step ran successfully (not skipped for docs-only)."""
    for run in check_runs:
        if run.get("conclusion") != "success":
            continue
        if run.get("status") not in (None, "completed"):
            continue
        html_url = run.get("html_url") or ""
        run_id = run_id_from_url(html_url)
        if run_id is None or run_id == current_run_id:
            continue
        if workflow_event.get(run_id) != "pull_request":
            continue
        job_id = job_id_from_url(html_url) or run.get("id")
        if not suite_ran_jobs.get(job_id):
            continue
        return True, (
            f"check run {run.get('id')} on workflow run {run_id} "
            f"already succeeded on a pull_request for this commit "
            f"and its {TEST_STEP} step ran"
        )
    return False, (
        "no successful pull_request check run that executed the suite "
        "for this commit and name"
    )


class Client:
    def __init__(self, token: str, api_url: str = API_URL, opener=None):
        if not token:
            raise APIError("set GH_TOKEN or GITHUB_TOKEN")
        self.token = token
        self.api_url = api_url.rstrip("/")
        self.opener = opener or urllib.request.urlopen

    def get(self, path: str):
        url = f"{self.api_url}/{path.lstrip('/')}"
        request = urllib.request.Request(
            url,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {self.token}",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "assistant-prior-check",
            },
        )
        try:
            with self.opener(request, timeout=30) as response:
                body = response.read()
        except urllib.error.HTTPError as error:
            detail = error.read().decode("utf-8", "replace")[:200]
            raise APIError(f"{path}: HTTP {error.code}: {detail}") from None
        except urllib.error.URLError as error:
            raise APIError(f"{path}: {error.reason}") from None
        except READ_ERRORS as error:
            raise APIError(f"{path}: {error}") from None
        try:
            return json.loads(body)
        except ValueError as error:
            raise APIError(f"{path}: the answer is not JSON") from error

    def check_runs(self, repo: str, sha: str, check_name: str):
        query = urllib.parse.urlencode(
            {"check_name": check_name, "status": "completed", "filter": "all", "per_page": "100"}
        )
        call = f"repos/{repo}/commits/{sha}/check-runs"
        runs = field(self.get(f"{call}?{query}"), "check_runs", list, call)
        for run in runs:
            conclusion(run, call)
        return runs

    def workflow_event(self, repo: str, run_id: int) -> str:
        call = f"repos/{repo}/actions/runs/{run_id}"
        return field(self.get(call), "event", str, call)

    def job_steps(self, repo: str, job_id: int):
        call = f"repos/{repo}/actions/jobs/{job_id}"
        steps = field(self.get(call), "steps", list, call)
        for step in steps:
            conclusion(step, call)
        return steps


def evaluate(client: Client, repo: str, sha: str, check_name: str, current_run_id: int):
    runs = client.check_runs(repo, sha, check_name)
    events = {}
    suite = {}
    for run in runs:
        if run.get("conclusion") != "success":
            continue
        html_url = run.get("html_url") or ""
        run_id = run_id_from_url(html_url)
        if run_id is None or run_id == current_run_id:
            continue
        if run_id not in events:
            events[run_id] = client.workflow_event(repo, run_id)
        if events[run_id] != "pull_request":
            continue
        job_id = job_id_from_url(html_url) or run.get("id")
        if job_id is None or job_id in suite:
            continue
        suite[job_id] = suite_ran(client.job_steps(repo, job_id))
    return decide(runs, events, suite, current_run_id)


def write_output(path: str, run: bool, reason: str) -> None:
    with open(path, "a", encoding="utf-8") as out:
        out.write(f"run={str(run).lower()}\n")
        out.write(f"reason={reason}\n")


def latest_pull_request_head(client: Client, repo: str) -> str | None:
    """The head of ci.yml's newest successful pull_request run, or None when
    the readable listing holds none. None is not a first-run state on its
    own, since a filter that matched nothing would read the same; only
    first_run may conclude that."""
    call = f"repos/{repo}/actions/workflows/ci.yml/runs"
    query = urllib.parse.urlencode({"event": "pull_request", "status": "success", "per_page": 1})
    runs = field(client.get(f"{call}?{query}"), "workflow_runs", list, call)
    if not runs:
        return None
    return field(runs[0], "head_sha", str, f"{call}: newest successful pull_request run")


FIRST_RUN_PAGE = 100


def first_run(client: Client, repo: str, run_id: int | None) -> str:
    """Proof that the repository is in its true first-run state, as the line
    to print, or APIError naming what could not be shown.

    The true first-run state: no pull_request run of ci.yml has ever
    concluded success, shown by one read of ci.yml's pull_request runs that
    is complete (total_count runs, all on this page), lists this run, and
    holds no run that concluded success. Requiring this run in the listing
    means an empty answer to a filter that matched nothing cannot pass:
    the read has to reach at least this workflow run. Earlier runs that all
    failed, at this probe or anywhere else, are inside the state: none of
    them is a candidate. Every shortfall is an APIError, so an unreadable
    API, an absent field, or a listing longer than a page never reaches
    the state."""
    call = f"repos/{repo}/actions/workflows/ci.yml/runs"
    query = urllib.parse.urlencode({"event": "pull_request", "per_page": FIRST_RUN_PAGE})
    body = client.get(f"{call}?{query}")
    runs = field(body, "workflow_runs", list, call)
    total = field(body, "total_count", int, call)
    if total != len(runs):
        raise APIError(f"{call}: lists {len(runs)} of {total} pull_request runs, not all of them")
    ids = [field(run, "id", int, f"{call}: a pull_request run") for run in runs]
    if run_id not in ids:
        raise APIError(f"{call}: this run ({run_id}, from --run-id) is not among its {total} pull_request runs")
    for run in runs:
        if conclusion(run, call) == "success":
            raise APIError(f"{call}: run {run['id']} succeeded but the success listing was empty")
    return (
        f"ci.yml has {total} pull_request run(s), this run {run_id} among them, "
        "and none concluded success"
    )


def untested_commit(client: Client, repo: str, check_name: str) -> str | None:
    """A recent commit with no successful pull_request check, or None when
    every recent commit already has one (healthy history; the probe then
    demonstrates the run path via fail-closed instead of failing the job)."""
    body = client.get(f"repos/{repo}/commits?{urllib.parse.urlencode({'per_page': 30})}")
    if not isinstance(body, list):
        raise APIError("repos/.../commits: expected a list")
    for commit in body:
        sha = commit.get("sha")
        if sha and not evaluate(client, repo, sha, check_name, current_run_id=0)[0]:
            return sha
    return None


def probe(client: Client, repo: str, check_name: str, run_id: int | None = None) -> None:
    tested = latest_pull_request_head(client, repo)
    if tested is None:
        # Nothing to probe against; first_run refuses unless it can show why.
        print(f"prior-check probe: state: first run: {first_run(client, repo, run_id)}")
        return
    print(f"prior-check probe: state: candidate {tested}")
    skip, reason = evaluate(client, repo, tested, check_name, current_run_id=0)
    print(f"prior-check probe: already-tested {tested}: skip={skip} ({reason})")
    if not skip:
        raise SystemExit(f"prior-check probe: expected skip for {tested}")
    never = untested_commit(client, repo, check_name)
    if never is None:
        # Healthy history: every recent commit was PR-tested. Still show
        # the run path (empty check runs → do not skip) and exit 0 so the
        # suite is not blocked (#708 review).
        skip, reason = decide([], {}, {}, current_run_id=0)
        print(
            f"prior-check probe: never-tested (no recent untested commit; "
            f"empty check runs): skip={skip} ({reason})"
        )
        if skip:
            raise SystemExit("prior-check probe: empty check runs must not skip")
        return
    skip, reason = evaluate(client, repo, never, check_name, current_run_id=0)
    print(f"prior-check probe: never-tested {never}: skip={skip} ({reason})")
    if skip:
        raise SystemExit(f"prior-check probe: expected run for {never}")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--repo", required=True)
    parser.add_argument("--sha")
    parser.add_argument("--check-name", required=True)
    parser.add_argument("--run-id", type=int)
    parser.add_argument("--github-output", metavar="FILE")
    parser.add_argument("--expect", choices=("run", "skip"))
    parser.add_argument("--probe", action="store_true",
                        help="show one already-tested commit skips and one never-tested runs, "
                        "or prove a true first-run state (needs --run-id)")
    parser.add_argument("--event-name", default="")
    parser.add_argument("--ref", default="")
    args = parser.parse_args(argv)

    if args.probe:
        token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or ""
        try:
            probe(Client(token), args.repo, args.check_name, args.run_id)
        except APIError as error:
            # The probe must evaluate a commit or prove a first-run state.
            # An unreadable API fails loudly so the step cannot go green
            # without checking either. The non-probe decision path still
            # fails open to run=true (#708 delta review).
            print(f"prior-check probe: {error}", file=sys.stderr)
            return 1
        return 0

    if not args.sha or args.run_id is None:
        parser.error("--sha and --run-id are required unless --probe")

    if args.event_name and args.event_name != "push":
        skip, reason = False, f"event is {args.event_name}, not a push to main"
    elif args.ref and args.ref != "refs/heads/main":
        skip, reason = False, f"ref is {args.ref}, not refs/heads/main"
    else:
        token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or ""
        try:
            skip, reason = evaluate(
                Client(token), args.repo, args.sha, args.check_name, args.run_id
            )
        except Exception as error:
            # Every API or parse failure falls back to running the suite,
            # including IncompleteRead and other non-APIError escapes.
            skip, reason = False, f"not read ({error}); running the suite"
            print(f"prior-check: {reason}", file=sys.stderr)

    run = not skip
    print(f"prior-check: run={str(run).lower()} ({reason})")
    if args.github_output:
        write_output(args.github_output, run, reason)
    if args.expect == "skip" and not skip:
        print(f"prior-check: expected skip, got run ({reason})", file=sys.stderr)
        return 1
    if args.expect == "run" and skip:
        print(f"prior-check: expected run, got skip ({reason})", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
