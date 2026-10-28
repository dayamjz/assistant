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
still report SUCCESS under the expanded check name. Fail closed: any API
or parse problem sets run=true. Permissions: checks:read, actions:read.
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
# IncompleteRead is an HTTPException, not an OSError; both must become
# APIError so main can fail closed to run=true (#708 review).
READ_ERRORS = (OSError, TimeoutError, http.client.HTTPException)


class APIError(Exception):
    """A GitHub API read that cannot be used to decide."""


def run_id_from_url(html_url: str) -> int | None:
    match = RUN_URL.search(html_url or "")
    return int(match.group(1)) if match else None


def decide(check_runs, workflow_event, current_run_id: int):
    """Return (skip, reason). skip is True only for a successful check run
    from a pull_request workflow run that is not the current one."""
    for run in check_runs:
        if run.get("conclusion") != "success":
            continue
        if run.get("status") not in (None, "completed"):
            continue
        run_id = run_id_from_url(run.get("html_url") or "")
        if run_id is None or run_id == current_run_id:
            continue
        if workflow_event.get(run_id) == "pull_request":
            return True, (
                f"check run {run.get('id')} on workflow run {run_id} "
                f"already succeeded on a pull_request for this commit"
            )
    return False, "no successful pull_request check run for this commit and name"


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
        body = self.get(f"repos/{repo}/commits/{sha}/check-runs?{query}")
        if not isinstance(body, dict):
            raise APIError(f"repos/{repo}/commits/{sha}/check-runs: expected an object")
        return body.get("check_runs") or []

    def workflow_event(self, repo: str, run_id: int) -> str:
        event = self.get(f"repos/{repo}/actions/runs/{run_id}").get("event")
        if not isinstance(event, str) or not event:
            raise APIError(f"repos/{repo}/actions/runs/{run_id}: missing event")
        return event


def evaluate(client: Client, repo: str, sha: str, check_name: str, current_run_id: int):
    runs = client.check_runs(repo, sha, check_name)
    events = {}
    for run in runs:
        if run.get("conclusion") != "success":
            continue
        run_id = run_id_from_url(run.get("html_url") or "")
        if run_id is None or run_id == current_run_id or run_id in events:
            continue
        events[run_id] = client.workflow_event(repo, run_id)
    return decide(runs, events, current_run_id)


def write_output(path: str, run: bool, reason: str) -> None:
    with open(path, "a", encoding="utf-8") as out:
        out.write(f"run={str(run).lower()}\n")
        out.write(f"reason={reason}\n")


def latest_pull_request_head(client: Client, repo: str) -> str:
    body = client.get(
        f"repos/{repo}/actions/workflows/ci.yml/runs?"
        + urllib.parse.urlencode({"event": "pull_request", "status": "success", "per_page": 1})
    )
    runs = body.get("workflow_runs") or []
    if not runs or not runs[0].get("head_sha"):
        raise APIError("no successful pull_request CI run to probe against")
    return runs[0]["head_sha"]


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


def probe(client: Client, repo: str, check_name: str) -> None:
    tested = latest_pull_request_head(client, repo)
    skip, reason = evaluate(client, repo, tested, check_name, current_run_id=0)
    print(f"prior-check probe: already-tested {tested}: skip={skip} ({reason})")
    if not skip:
        raise SystemExit(f"prior-check probe: expected skip for {tested}")
    never = untested_commit(client, repo, check_name)
    if never is None:
        # Healthy history: every recent commit was PR-tested. Still show
        # the run path (empty check runs → do not skip) and exit 0 so the
        # suite is not blocked (#708 review).
        skip, reason = decide([], {}, current_run_id=0)
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
                        help="show one already-tested commit skips and one never-tested runs")
    parser.add_argument("--event-name", default="")
    parser.add_argument("--ref", default="")
    args = parser.parse_args(argv)

    if args.probe:
        token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or ""
        try:
            probe(Client(token), args.repo, args.check_name)
        except APIError as error:
            # An unreadable API must not fail the Ubuntu job before the
            # suite; fall back so the suite runs (#708 review).
            print(f"prior-check probe: {error}; continuing so the suite runs", file=sys.stderr)
            return 0
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
