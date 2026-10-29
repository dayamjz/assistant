#!/usr/bin/env python3
"""Tests for docs-only.py, as the macOS check job will run it on a pull request."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("docs-only.py").resolve()
SPEC = importlib.util.spec_from_file_location("docs_only", SCRIPT)
docs_only = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docs_only)


def run(*paths, output=None):
    args = [sys.executable, str(SCRIPT)] + (["--github-output", output] if output else [])
    result = subprocess.run(
        args, input="\n".join(paths) + "\n", capture_output=True, text=True, check=True
    )
    return json.loads(result.stdout)


class DocsOnlyTest(unittest.TestCase):
    def test_docs_tree_alone_is_docs_only(self):
        self.assertTrue(run("docs/prd.md")["docs_only"])

    def test_markdown_anywhere_is_docs_only(self):
        self.assertTrue(run("AGENTS.md", "internal/service/README.md")["docs_only"])

    def test_mixed_with_go_is_not(self):
        self.assertFalse(run("docs/prd.md", "internal/service/x.go")["docs_only"])

    def test_workflow_change_is_not(self):
        self.assertFalse(run(".github/workflows/ci.yml")["docs_only"])

    def test_empty_is_not_docs_only(self):
        self.assertFalse(run("")["docs_only"])
        self.assertFalse(docs_only.docs_only([]))

    def test_step_outputs(self):
        with tempfile.NamedTemporaryFile("r", suffix=".txt") as out:
            run("docs/prd.md", output=out.name)
            text = Path(out.name).read_text()
            self.assertIn("docs_only=true", text)

    def test_is_docs_path(self):
        self.assertTrue(docs_only.is_docs_path("docs/a/b.txt"))
        self.assertTrue(docs_only.is_docs_path("foo.md"))
        self.assertFalse(docs_only.is_docs_path("docs"))
        self.assertFalse(docs_only.is_docs_path("foo.mdx"))
        self.assertFalse(docs_only.is_docs_path("internal/x.go"))


def git(repo, *args):
    return subprocess.run(
        [
            "git", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
            "-c", "user.name=test", "-c", "user.email=test@example.com",
            "-c", "commit.gpgsign=false", *args,
        ],
        cwd=repo, check=True, capture_output=True, text=True,
    ).stdout.strip()


class ChangedPathsTest(unittest.TestCase):
    def test_base_head_docs_only(self):
        with tempfile.TemporaryDirectory() as repo:
            git(repo, "init")
            (Path(repo) / "docs").mkdir()
            (Path(repo) / "docs" / "a.md").write_text("a\n")
            git(repo, "add", "docs/a.md")
            git(repo, "commit", "-m", "base")
            base = git(repo, "rev-parse", "HEAD")
            (Path(repo) / "docs" / "b.md").write_text("b\n")
            git(repo, "add", "docs/b.md")
            git(repo, "commit", "-m", "head")
            head = git(repo, "rev-parse", "HEAD")
            result = subprocess.run(
                [sys.executable, str(SCRIPT), "--base", base, "--head", head],
                cwd=repo, capture_output=True, text=True, check=True,
            )
            self.assertTrue(json.loads(result.stdout)["docs_only"])

    def test_unreadable_diff_fails_closed(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--base", "nope", "--head", "nope"],
            capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0)
        self.assertFalse(json.loads(result.stdout)["docs_only"])


if __name__ == "__main__":
    unittest.main()
