#!/usr/bin/env python3
"""Executable and model tests for the CI leftover guard (P15)."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("leak-check.py").resolve()
SPEC = importlib.util.spec_from_file_location("leak_check", SCRIPT)
check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check)


class LeakCheckTest(unittest.TestCase):
    def test_pid_reuse_is_a_new_process(self):
        before = ({(42, "1.000001"): "old"}, {})
        after = ({(42, "1.000002"): "new"}, {})
        self.assertEqual(check.differences(before, after), ([
            "LEAK process pid=42 start=1.000002 command=new"
        ], []))
        self.assertEqual(check.differences(after, after), ([], []))

    def test_recursive_socket_symlink_and_replaced_entry(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            existing = root / "tmux-existing"
            existing.mkdir()
            replaced = root / "replaced"
            replaced.write_text("before")
            before = ({}, check.temp_entries([root, existing]))
            # Keep the old inode allocated so replacement is unambiguous.
            replaced.rename(root / "old")
            replaced.write_text("after")
            with socket.socket(socket.AF_UNIX) as server:
                server.bind(str(existing / "socket"))
                (existing / "link").symlink_to(existing, target_is_directory=True)
                after = ({}, check.temp_entries([root, existing]))
                messages, _ = check.differences(before, after)
                self.assertIn(f"LEAK socket {existing / 'socket'}", messages)
                self.assertIn(f"LEAK symlink {existing / 'link'}", messages)
                self.assertIn(f"LEAK file {replaced}", messages)
                self.assertEqual(len(messages), 4)

    def test_only_an_apple_launchd_job_is_set_aside(self):
        before = ({}, {})
        after = ({(7, "1.0"): "/System/x/agentd", (8, "1.0"): "sleep 300"}, {})
        # pid 8 shares launchd as parent with pid 7 once orphaned, but launchd
        # reports no job for it, so it is still a leak.
        messages, aside = check.differences(before, after,
                                            {7: "launchd job com.apple.agentd"})
        self.assertEqual(messages, ["LEAK process pid=8 start=1.0 command=sleep 300"])
        self.assertEqual(aside, ["launchd job com.apple.agentd pid=7"])

    def test_apple_temp_entries_are_set_aside_only_in_their_root(self):
        root, other = Path("/darwin/T"), Path("/runner/tmp")
        new = {str(p): (1, i, "directory") for i, p in enumerate([
            root / "com.apple.devicecheckd", root / "com.apple.x" / "inner",
            root / "xcrun_db", root / ".LINKS" / "ABC",
            root / "TestLeak123", root / "gitconfig9",
            other / "com.apple.devicecheckd", other / "xcrun_db",
        ])}
        messages, aside = check.differences(({}, {}), ({}, new), {}, root)
        self.assertEqual(sorted(messages), sorted([
            f"LEAK directory {root / 'TestLeak123'}",
            f"LEAK directory {root / 'gitconfig9'}",
            f"LEAK directory {other / 'com.apple.devicecheckd'}",
            f"LEAK directory {other / 'xcrun_db'}",
        ]))
        self.assertEqual(len(aside), 4)
        # With no root, as when TMPDIR is the per-user temp dir, none is aside.
        messages, aside = check.differences(({}, {}), ({}, new), {}, None)
        self.assertEqual((len(messages), aside), (8, []))

    def test_apple_temp_root_is_off_when_tmpdir_shares_it(self):
        if sys.platform != "darwin":
            self.assertIsNone(check.apple_temp_root())
            return
        darwin = subprocess.check_output(["getconf", "DARWIN_USER_TEMP_DIR"],
                                         text=True).strip()
        with mock.patch.object(tempfile, "gettempdir", return_value=darwin):
            self.assertIsNone(check.apple_temp_root())
        with tempfile.TemporaryDirectory(dir="/tmp") as folder, \
                mock.patch.object(tempfile, "gettempdir", return_value=folder):
            self.assertEqual(check.apple_temp_root(), Path(darwin).resolve())

    def test_launchd_jobs_are_read_from_launchctl(self):
        jobs = check.apple_launchd_jobs()
        if sys.platform != "darwin":
            self.assertEqual(jobs, {})
            return
        self.assertTrue(jobs, "launchctl listed no running com.apple job")
        self.assertTrue(all(label.startswith("com.apple.") for label in jobs.values()))
        with subprocess.Popen(["sleep", "300"], start_new_session=True) as child:
            try:
                self.assertNotIn(child.pid, check.apple_launchd_jobs())
            finally:
                child.kill()
                child.wait()

    def test_an_orphan_is_not_an_apple_xpc_service(self):
        with subprocess.Popen(["sleep", "300"], start_new_session=True) as child:
            try:
                self.assertIsNone(check.apple_xpc_service(child.pid))
                self.assertEqual(check.apple_owned({child.pid}), {})
            finally:
                child.kill()
                child.wait()
        if sys.platform != "darwin":
            return
        # Positive control: a running Apple XPC service launchd started.
        output = subprocess.check_output(["ps", "-axo", "pid=,command="], text=True)
        services = [int(line.split(None, 1)[0]) for line in output.splitlines()
                    if ".xpc/Contents/MacOS/" in line and "/System/" in line]
        found = [pid for pid in services if check.apple_xpc_service(pid)]
        self.assertTrue(found, "no running /System XPC service read as launchd's")

    def test_missing_root_is_an_error(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(FileNotFoundError):
                check.temp_entries([Path(folder) / "missing"])

    def test_owned_unreadable_tree_is_an_error(self):
        with tempfile.TemporaryDirectory() as folder:
            with mock.patch.object(Path, "iterdir", side_effect=PermissionError):
                with self.assertRaises(PermissionError):
                    check.temp_entries([Path(folder)])

    def test_only_foreign_unenterable_subtrees_can_be_set_aside(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            private = root / "private"
            private.mkdir()
            original_iter = Path.iterdir
            original_stat = Path.stat

            def unreadable(path):
                if path == private:
                    raise PermissionError(str(path))
                return original_iter(path)

            def foreign(path, *args, **kwargs):
                info = list(original_stat(path, *args, **kwargs))
                if path == private:
                    info[4] = os.geteuid() + 1
                return os.stat_result(info)

            with mock.patch.object(Path, "iterdir", unreadable), \
                    mock.patch.object(Path, "stat", foreign):
                with mock.patch.object(os, "access", return_value=False):
                    self.assertIn(str(private), check.temp_entries([root]))
                with mock.patch.object(os, "access", return_value=True):
                    with self.assertRaises(PermissionError):
                        check.temp_entries([root])

    def test_native_identity_and_effective_user(self):
        observed = check.process_start(os.getpid())
        self.assertIsNotNone(observed)
        self.assertEqual(observed, check.process_start(os.getpid()))
        with subprocess.Popen(["sleep", "300"]) as child:
            try:
                self.assertIn((child.pid, check.process_start(child.pid)), check.processes())
                self.assertFalse(any(pid == os.getpid() for pid, _ in check.processes()))
            finally:
                child.kill()
                child.wait()

    def test_real_tmux_server_and_socket_in_existing_directory(self):
        with tempfile.TemporaryDirectory(prefix="lc-", dir="/tmp") as folder:
            root = Path(folder).resolve()
            address = root / "server"
            before = check.snapshot([root])
            try:
                subprocess.run(["tmux", "-f", "/dev/null", "-S", str(address),
                                "new-session", "-d", "-s", "leak-check", "sleep 300"],
                               check=True)
                messages, _ = check.differences(before, check.snapshot([root]))
                self.assertIn(f"LEAK socket {address}", messages)
                self.assertTrue(any(message.startswith("LEAK process ") and
                                    "tmux" in message for message in messages), messages)
            finally:
                subprocess.run(["tmux", "-S", str(address), "kill-server"], check=True)

    def test_all_temp_roots_are_included_and_canonical(self):
        with tempfile.TemporaryDirectory() as folder:
            with mock.patch.dict(os.environ, {"TMPDIR": folder}):
                roots = check.temp_roots()
                self.assertIn(Path(folder).resolve(), roots)
                self.assertIn(Path("/tmp").resolve(), roots)
                self.assertEqual(len(roots), len(set(roots)))

    def test_command_failure_survives_clean_comparison(self):
        with mock.patch.object(check, "temp_roots", return_value=[]), \
                mock.patch.object(check, "snapshot", return_value=({}, {})), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(check.guarded_run([sys.executable, "-c", "exit(7)"]), 7)

    def test_inspection_failure_cannot_pass(self):
        with mock.patch.object(sys, "argv", [str(SCRIPT), "run", "true"]), \
                mock.patch.object(check, "snapshot", side_effect=PermissionError("denied")), \
                contextlib.redirect_stdout(io.StringIO()), \
                contextlib.redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(check.main(), 2)
            self.assertIn("inspection failed", errors.getvalue())

    def test_self_test_requires_the_real_cli_to_fail(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "self-test"],
                                capture_output=True, text=True, check=False, timeout=90)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("LEAK process pid=", result.stdout)
        self.assertIn("LEAK directory ", result.stdout)
        self.assertIn("expected failure observed", result.stdout)


if __name__ == "__main__":
    unittest.main()
