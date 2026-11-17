#!/usr/bin/env python3
"""Tests for prior-check.py, as the check job will run it on a push to main."""

import contextlib
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("prior-check.py").resolve()
SPEC = importlib.util.spec_from_file_location("prior_check", SCRIPT)
prior_check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(prior_check)


def check_run(run_id, conclusion="success", status="completed", check_id=1, html_url=None):
    return {
        "id": check_id,
        "name": "check (ubuntu-latest)",
        "conclusion": conclusion,
        "status": status,
        "html_url": html_url or f"https://github.com/dayamjz/assistant/actions/runs/{run_id}/job/{check_id}",
    }


class RunIdFromURLTest(unittest.TestCase):
    def test_job_url(self):
        self.assertEqual(
            prior_check.run_id_from_url(
                "https://github.com/dayamjz/assistant/actions/runs/40738084737/job/122991952329"
            ),
            40738084737,
        )

    def test_missing(self):
        self.assertIsNone(prior_check.run_id_from_url(""))
        self.assertIsNone(prior_check.run_id_from_url("https://example.com/nope"))


class DecideTest(unittest.TestCase):
    def test_pull_request_success_skips(self):
        skip, reason = prior_check.decide(
            [check_run(100, check_id=11)],
            {100: "pull_request"},
            {11: True},
            current_run_id=999,
        )
        self.assertTrue(skip)
        self.assertIn("100", reason)
        self.assertIn("pull_request", reason)

    def test_current_run_does_not_count(self):
        skip, reason = prior_check.decide(
            [check_run(100)],
            {100: "pull_request"},
            {100: True},
            current_run_id=100,
        )
        self.assertFalse(skip)
        self.assertIn("no successful", reason)

    def test_push_success_does_not_skip(self):
        skip, _ = prior_check.decide(
            [check_run(100)],
            {100: "push"},
            {100: True},
            current_run_id=999,
        )
        self.assertFalse(skip)

    def test_failed_check_does_not_skip(self):
        skip, _ = prior_check.decide(
            [check_run(100, conclusion="failure")],
            {100: "pull_request"},
            {100: True},
            current_run_id=999,
        )
        self.assertFalse(skip)

    def test_push_and_pull_request_prefers_pull_request(self):
        skip, reason = prior_check.decide(
            [check_run(50, check_id=1), check_run(60, check_id=2)],
            {50: "push", 60: "pull_request"},
            {1: True, 2: True},
            current_run_id=999,
        )
        self.assertTrue(skip)
        self.assertIn("60", reason)

    def test_empty_runs_do_not_skip(self):
        skip, _ = prior_check.decide([], {}, {}, current_run_id=1)
        self.assertFalse(skip)

    def test_pull_request_without_suite_does_not_skip(self):
        skip, reason = prior_check.decide(
            [check_run(100, check_id=11)],
            {100: "pull_request"},
            {11: False},
            current_run_id=999,
        )
        self.assertFalse(skip)
        self.assertIn("suite", reason)


class FakeHTTPResponse:
    def __init__(self, body, status=200):
        self.body = body if isinstance(body, bytes) else body.encode()
        self.status = status

    def read(self):
        return self.body

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


def job_with_steps(*steps):
    return {"steps": [{"name": name, "conclusion": conclusion} for name, conclusion in steps]}


class EvaluateTest(unittest.TestCase):
    def _opener(self, test_conclusion):
        def opener(request, timeout=30):
            path = request.full_url.split("https://api.github.com/", 1)[1]
            if path.startswith("repos/dayamjz/assistant/commits/abc/check-runs?"):
                return FakeHTTPResponse(json.dumps({"check_runs": [check_run(100, check_id=11)]}))
            if path == "repos/dayamjz/assistant/actions/runs/100":
                return FakeHTTPResponse(json.dumps({"event": "pull_request"}))
            if path == "repos/dayamjz/assistant/actions/jobs/11":
                return FakeHTTPResponse(json.dumps(job_with_steps(
                    ("Prior check", "success"),
                    ("Test", test_conclusion),
                )))
            raise urllib.error.HTTPError(request.full_url, 404, "nope", hdrs=None, fp=io.BytesIO(b"{}"))
        return opener

    def test_evaluate_reads_event_and_skips(self):
        """Positive control: PR success whose Test step ran still skips."""
        client = prior_check.Client("tok", opener=self._opener("success"))
        skip, reason = prior_check.evaluate(
            client, "dayamjz/assistant", "abc", "check (ubuntu-latest)", 999
        )
        self.assertTrue(skip)
        self.assertIn("pull_request", reason)
        self.assertIn("Test", reason)

    def test_docs_only_job_success_does_not_skip(self):
        """A green check whose Test step was skipped (docs-only) must not
        authorize skipping the suite on main (#708 round 4)."""
        client = prior_check.Client("tok", opener=self._opener("skipped"))
        skip, reason = prior_check.evaluate(
            client, "dayamjz/assistant", "abc", "check (ubuntu-latest)", 999
        )
        self.assertFalse(skip)
        self.assertIn("suite", reason.lower())

    def test_evaluate_api_error_surfaces(self):
        def opener(request, timeout=30):
            raise urllib.error.HTTPError(
                request.full_url, 502, "bad", hdrs=None, fp=io.BytesIO(b"oops")
            )

        client = prior_check.Client("tok", opener=opener)
        with self.assertRaises(prior_check.APIError) as caught:
            prior_check.evaluate(client, "dayamjz/assistant", "abc", "check (ubuntu-latest)", 1)
        self.assertIn("502", str(caught.exception))


class ProbeTest(unittest.TestCase):
    def test_probe_requires_both_paths(self):
        calls = {"n": 0}

        class FakeClient:
            def get(self, path):
                if "workflows/ci.yml/runs" in path:
                    return {"workflow_runs": [{"head_sha": "testedsha"}]}
                if path.startswith("repos/dayamjz/assistant/commits?"):
                    return [{"sha": "testedsha"}, {"sha": "neversha"}]
                raise AssertionError(path)

        def fake_evaluate(client, repo, sha, check_name, current_run_id):
            calls["n"] += 1
            if sha == "testedsha":
                return True, "already"
            return False, "never"

        with mock.patch.object(prior_check, "evaluate", side_effect=fake_evaluate):
            prior_check.probe(FakeClient(), "dayamjz/assistant", "check (ubuntu-latest)")
        self.assertGreaterEqual(calls["n"], 2)

    def test_probe_healthy_history_does_not_fail_the_job(self):
        """When every recent commit already has a PR check, the probe still
        evaluated the skip path and shows run via empty check runs (#708)."""
        commits = [{"sha": f"{i:040x}"} for i in range(30)]

        class FakeClient:
            def get(self, path):
                if "workflows/ci.yml/runs" in path:
                    return {"workflow_runs": [{"head_sha": commits[0]["sha"]}]}
                if path.startswith("repos/dayamjz/assistant/commits?"):
                    return commits
                raise AssertionError(path)

        with mock.patch.object(prior_check, "Client", lambda token, **k: FakeClient()):
            with mock.patch.object(
                prior_check, "evaluate", side_effect=lambda *a, **k: (True, "already")
            ):
                with mock.patch.dict(os.environ, {"GH_TOKEN": "tok"}):
                    code = prior_check.main(
                        [
                            "--repo", "dayamjz/assistant",
                            "--check-name", "check (ubuntu-latest)",
                            "--probe",
                        ]
                    )
        self.assertEqual(code, 0)

    def test_probe_first_api_503_exits_nonzero(self):
        """A probe that never evaluates a commit must fail loudly (#708)."""

        def failing_opener(request, timeout=30):
            raise urllib.error.HTTPError(
                request.full_url, 503, "unavailable", hdrs=None, fp=io.BytesIO(b"{}")
            )

        real = prior_check.Client

        def factory(token, api_url=prior_check.API_URL, opener=None):
            return real(token, api_url=api_url, opener=failing_opener)

        with mock.patch.object(prior_check, "Client", factory):
            with mock.patch.dict(os.environ, {"GH_TOKEN": "tok"}):
                code = prior_check.main(
                    [
                        "--repo", "dayamjz/assistant",
                        "--check-name", "check (ubuntu-latest)",
                        "--probe",
                    ]
                )
        self.assertEqual(code, 1)

    def test_probe_exits_zero_when_both_paths_evaluate(self):
        """Positive control: a working API that yields both paths exits 0."""

        class FakeClient:
            def get(self, path):
                if "workflows/ci.yml/runs" in path:
                    return {"workflow_runs": [{"head_sha": "testedsha"}]}
                if path.startswith("repos/dayamjz/assistant/commits?"):
                    return [{"sha": "testedsha"}, {"sha": "neversha"}]
                raise AssertionError(path)

        def fake_evaluate(client, repo, sha, check_name, current_run_id):
            if sha == "testedsha":
                return True, "already"
            return False, "never"

        with mock.patch.object(prior_check, "Client", lambda token, **k: FakeClient()):
            with mock.patch.object(prior_check, "evaluate", side_effect=fake_evaluate):
                with mock.patch.dict(os.environ, {"GH_TOKEN": "tok"}):
                    code = prior_check.main(
                        [
                            "--repo", "dayamjz/assistant",
                            "--check-name", "check (ubuntu-latest)",
                            "--probe",
                        ]
                    )
        self.assertEqual(code, 0)


class MainTest(unittest.TestCase):
    def test_non_push_event_runs_without_api(self):
        with tempfile.NamedTemporaryFile("r+", suffix=".txt") as out:
            code = prior_check.main(
                [
                    "--repo", "dayamjz/assistant",
                    "--sha", "abc",
                    "--check-name", "check (ubuntu-latest)",
                    "--run-id", "1",
                    "--event-name", "pull_request",
                    "--github-output", out.name,
                ]
            )
            self.assertEqual(code, 0)
            text = Path(out.name).read_text()
            self.assertIn("run=true", text)
            self.assertIn("pull_request", text)

    def test_expect_skip_fails_when_running(self):
        code = prior_check.main(
            [
                "--repo", "dayamjz/assistant",
                "--sha", "abc",
                "--check-name", "check (ubuntu-latest)",
                "--run-id", "1",
                "--event-name", "pull_request",
                "--expect", "skip",
            ]
        )
        self.assertEqual(code, 1)

    def test_api_failure_fails_closed_to_run(self):
        with mock.patch.object(prior_check, "Client", side_effect=prior_check.APIError("boom")):
            with tempfile.NamedTemporaryFile("r+", suffix=".txt") as out:
                code = prior_check.main(
                    [
                        "--repo", "dayamjz/assistant",
                        "--sha", "abc",
                        "--check-name", "check (ubuntu-latest)",
                        "--run-id", "1",
                        "--event-name", "push",
                        "--ref", "refs/heads/main",
                        "--github-output", out.name,
                    ]
                )
                self.assertEqual(code, 0)
                self.assertIn("run=true", Path(out.name).read_text())

    def test_incomplete_read_fails_closed_to_run(self):
        """A truncated API body must set run=true, not escape (#708 review)."""

        class Truncated:
            def read(self):
                raise http.client.IncompleteRead(partial=b'{"check_runs":')

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

        real = prior_check.Client

        def factory(token, api_url=prior_check.API_URL, opener=None):
            return real(token, api_url=api_url, opener=lambda *a, **k: Truncated())

        with mock.patch.object(prior_check, "Client", factory):
            with mock.patch.dict(os.environ, {"GH_TOKEN": "tok"}):
                with tempfile.NamedTemporaryFile("r+", suffix=".txt") as out:
                    code = prior_check.main(
                        [
                            "--repo", "dayamjz/assistant",
                            "--sha", "abc",
                            "--check-name", "check (ubuntu-latest)",
                            "--run-id", "1",
                            "--event-name", "push",
                            "--ref", "refs/heads/main",
                            "--github-output", out.name,
                        ]
                    )
                    text = Path(out.name).read_text()
        self.assertEqual(code, 0)
        self.assertIn("run=true", text)
        self.assertIn("not read", text)


CURRENT = 4242
SUCCESS_RUNS = "workflows/ci.yml/runs?event=pull_request&status=success&per_page=1"
ALL_RUNS = "workflows/ci.yml/runs?event=pull_request&per_page=100"
RUNS_CALL = "repos/dayamjz/assistant/actions/workflows/ci.yml/runs"
TESTED, NEVER = "a" * 40, "b" * 40


def pr_run(run_id, conclusion=None, status="completed"):
    return {"id": run_id, "event": "pull_request", "status": status,
            "conclusion": conclusion, "head_sha": f"{run_id:040x}"}


def listing(*runs, total=None):
    return {"total_count": len(runs) if total is None else total, "workflow_runs": list(runs)}


FIRST_RUN = {SUCCESS_RUNS: listing(), ALL_RUNS: listing(pr_run(CURRENT, status="in_progress"))}
CANDIDATE = {
    SUCCESS_RUNS: listing(dict(pr_run(100, "success"), head_sha=TESTED)),
    f"commits/{TESTED}/check-runs?": {"total_count": 1, "check_runs": [check_run(100, check_id=11)]},
    "actions/runs/100": {"event": "pull_request"},
    "actions/jobs/11": job_with_steps(("Prior check", "success"), ("Test", "success")),
    "commits?per_page=30": [{"sha": TESTED}, {"sha": NEVER}],
    f"commits/{NEVER}/check-runs?": {"total_count": 0, "check_runs": []},
}


def run_probe(routes, run_id=CURRENT):
    """The exit code and output of `prior-check.py --probe` against an API
    whose answer to a URL containing a route key is that route's value:
    JSON for a dict or list, a raw body for bytes, an HTTP status for an
    int, or an exception to raise. Any other URL fails the test."""

    def opener(request, timeout=30):
        for key, answer in routes.items():
            if key in request.full_url:
                if isinstance(answer, int):
                    raise urllib.error.HTTPError(request.full_url, answer, "x", hdrs=None,
                                                 fp=io.BytesIO(b'{"message":"x"}'))
                if isinstance(answer, BaseException):
                    raise answer
                return FakeHTTPResponse(answer if isinstance(answer, bytes) else json.dumps(answer))
        raise AssertionError(f"unexpected request {request.full_url}")

    real = prior_check.Client
    out = io.StringIO()
    argv = ["--repo", "dayamjz/assistant", "--check-name", "check (ubuntu-latest)", "--probe"]
    if run_id is not None:
        argv += ["--run-id", str(run_id)]
    with mock.patch.object(prior_check, "Client", lambda token, **k: real(token, opener=opener)), \
            mock.patch.dict(os.environ, {"GH_TOKEN": "tok"}), \
            contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
        try:
            code = prior_check.main(argv)
        except SystemExit as exit_:  # as the interpreter would exit
            code = exit_.code if isinstance(exit_.code, int) else 1
            print(exit_.code, file=out)
    return code, out.getvalue()


class FirstRunTest(unittest.TestCase):
    """#827: the probe passes without a candidate only on a proven first run."""

    def test_fresh_clone_passes(self):
        """#827 Reproducing: a readable listing with no successful
        pull_request run made the probe exit 1 before the suite ran."""
        code, out = run_probe(FIRST_RUN)
        self.assertEqual(code, 0, out)
        self.assertIn("state: first run", out)
        self.assertIn(f"this run {CURRENT}", out)

    def test_earlier_runs_all_failed_passes(self):
        """Case (b): every earlier pull_request run failed, at the probe or
        elsewhere; none is a candidate, so the clone is still on its first run."""
        runs = listing(pr_run(7, "failure"), pr_run(8, "cancelled"), pr_run(CURRENT, status="in_progress"))
        code, out = run_probe(dict(FIRST_RUN, **{ALL_RUNS: runs}))
        self.assertEqual(code, 0, out)
        self.assertIn("3 pull_request run(s)", out)

    def test_first_run_not_shown_stays_loud(self):
        cases = {
            "filter matched nothing": (listing(), CURRENT),
            "this run not listed": (listing(pr_run(7, "failure")), CURRENT),
            "listing incomplete": (listing(pr_run(CURRENT), total=101), CURRENT),
            "a run succeeded": (listing(pr_run(7, "success"), pr_run(CURRENT)), CURRENT),
            "no total_count": ({"workflow_runs": [pr_run(CURRENT)]}, CURRENT),
            "run without id": (listing({"conclusion": "failure"}, pr_run(CURRENT)), CURRENT),
            "no --run-id": (FIRST_RUN[ALL_RUNS], None),
        }
        for name, (answer, run_id) in cases.items():
            with self.subTest(name):
                code, out = run_probe(dict(FIRST_RUN, **{ALL_RUNS: answer}), run_id)
                self.assertEqual(code, 1, out)
                self.assertIn(RUNS_CALL, out)
                self.assertNotIn("state: first run", out)

    def test_unreadable_api_stays_loud(self):
        """A 5xx, auth error, network error, timeout or parse error is never
        a first-run state, on either listing read, even when the other read
        would prove one."""
        answers = {
            "500": 500, "502": 502, "401": 401, "403": 403,
            "network": urllib.error.URLError("connection refused"),
            "timeout": TimeoutError("timed out"),
            "truncated": http.client.IncompleteRead(b'{"total_count":'),
            "malformed JSON": b'{"workflow_runs": [',
            "runs field missing": {"total_count": 0},
            "not an object": [],
        }
        for route in (SUCCESS_RUNS, ALL_RUNS):
            for name, answer in answers.items():
                with self.subTest(route=route, answer=name):
                    code, out = run_probe(dict(FIRST_RUN, **{route: answer}))
                    self.assertEqual(code, 1, out)
                    self.assertIn(RUNS_CALL, out)
                    self.assertNotIn("state: first run", out)

    def test_candidate_probes_both_paths(self):
        """This repository's path: a candidate exists, so both paths are
        probed and the first-run listing is never read."""
        code, out = run_probe(CANDIDATE)
        self.assertEqual(code, 0, out)
        self.assertIn(f"state: candidate {TESTED}", out)
        self.assertIn(f"already-tested {TESTED}: skip=True", out)
        self.assertIn(f"never-tested {NEVER}: skip=False", out)

    def test_candidate_absent_field_stays_loud(self):
        """An absent field is not an empty answer: a never-tested commit
        whose check runs are unread must not stand in for the run path."""
        cases = [
            (f"commits/{NEVER}/check-runs?", {"total_count": 0}, f"commits/{NEVER}/check-runs"),
            ("actions/runs/100", [], "actions/runs/100"),
            ("actions/runs/100", {"event": ""}, "actions/runs/100"),
            ("actions/jobs/11", {"id": 11}, "actions/jobs/11"),
            (SUCCESS_RUNS, listing({"id": 100}), "newest successful pull_request run"),
        ]
        for route, answer, call in cases:
            with self.subTest(route=route, answer=answer):
                code, out = run_probe(dict(CANDIDATE, **{route: answer}))
                self.assertEqual(code, 1, out)
                self.assertIn(call, out)


if __name__ == "__main__":
    unittest.main()
