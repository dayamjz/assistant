#!/usr/bin/env python3
"""Tests for prior-check.py, as the check job will run it on a push to main."""

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
            current_run_id=999,
        )
        self.assertTrue(skip)
        self.assertIn("100", reason)
        self.assertIn("pull_request", reason)

    def test_current_run_does_not_count(self):
        skip, reason = prior_check.decide(
            [check_run(100)],
            {100: "pull_request"},
            current_run_id=100,
        )
        self.assertFalse(skip)
        self.assertIn("no successful", reason)

    def test_push_success_does_not_skip(self):
        skip, _ = prior_check.decide(
            [check_run(100)],
            {100: "push"},
            current_run_id=999,
        )
        self.assertFalse(skip)

    def test_failed_check_does_not_skip(self):
        skip, _ = prior_check.decide(
            [check_run(100, conclusion="failure")],
            {100: "pull_request"},
            current_run_id=999,
        )
        self.assertFalse(skip)

    def test_push_and_pull_request_prefers_pull_request(self):
        skip, reason = prior_check.decide(
            [check_run(50, check_id=1), check_run(60, check_id=2)],
            {50: "push", 60: "pull_request"},
            current_run_id=999,
        )
        self.assertTrue(skip)
        self.assertIn("60", reason)

    def test_empty_runs_do_not_skip(self):
        skip, _ = prior_check.decide([], {}, current_run_id=1)
        self.assertFalse(skip)


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


class EvaluateTest(unittest.TestCase):
    def test_evaluate_reads_event_and_skips(self):
        def opener(request, timeout=30):
            path = request.full_url.split("https://api.github.com/", 1)[1]
            if path.startswith("repos/dayamjz/assistant/commits/abc/check-runs?"):
                return FakeHTTPResponse(json.dumps({"check_runs": [check_run(100, check_id=11)]}))
            if path == "repos/dayamjz/assistant/actions/runs/100":
                return FakeHTTPResponse(json.dumps({"event": "pull_request"}))
            raise urllib.error.HTTPError(request.full_url, 404, "nope", hdrs=None, fp=io.BytesIO(b"{}"))

        client = prior_check.Client("tok", opener=opener)
        skip, reason = prior_check.evaluate(
            client, "dayamjz/assistant", "abc", "check (ubuntu-latest)", 999
        )
        self.assertTrue(skip)
        self.assertIn("pull_request", reason)

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
        """When every recent commit already has a PR check, the probe must
        still exit 0 so the Ubuntu suite runs (#708 review)."""
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


if __name__ == "__main__":
    unittest.main()
