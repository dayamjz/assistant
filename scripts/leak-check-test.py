#!/usr/bin/env python3
"""Executable and model tests for the CI leftover guard, leak-check.py."""

import contextlib
import ctypes
import importlib.util
import io
import os
from pathlib import Path
import socket
import stat
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
            root / "TemporaryItems" / "NSIRD_xcodebuild_yh1fJd",
            root / "TestLeak123", root / "gitconfig9",
            other / "com.apple.devicecheckd", other / "xcrun_db",
            other / "TemporaryItems",
        ])}
        messages, aside = check.differences(({}, {}), ({}, new), {}, root)
        self.assertEqual(sorted(messages), sorted([
            f"LEAK directory {root / 'TestLeak123'}",
            f"LEAK directory {root / 'gitconfig9'}",
            f"LEAK directory {other / 'com.apple.devicecheckd'}",
            f"LEAK directory {other / 'xcrun_db'}",
            f"LEAK directory {other / 'TemporaryItems'}",
        ]))
        self.assertEqual(len(aside), 5)
        # With no root, as when TMPDIR is the per-user temp dir, none is aside.
        messages, aside = check.differences(({}, {}), ({}, new), {}, None)
        self.assertEqual((len(messages), aside), (10, []))

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

    def test_launchd_jobs_are_read_only_on_macos(self):
        if sys.platform != "darwin":
            self.assertEqual(check.apple_launchd_jobs({1}), {})

    @unittest.skipUnless(sys.platform == "darwin", "launchd is macOS's")
    def test_an_apple_label_alone_does_not_set_a_job_aside(self):
        def listed():
            jobs = {}
            for line in subprocess.check_output(["launchctl", "list"], text=True).splitlines()[1:]:
                pid, _, label = line.split("\t", 2)
                if pid != "-" and label.startswith("com.apple."):
                    jobs[int(pid)] = label
            return jobs
        # Positive control: Apple's own running jobs are set aside.
        running = listed()
        jobs = check.apple_launchd_jobs(set(running))
        self.assertTrue(jobs, "no running com.apple job was set aside")
        self.assertTrue(all(running[pid] == label for pid, label in jobs.items()))
        # A job a test submits under an Apple label, running sleep.
        label = f"com.apple.leak-check-test-{os.getpid()}"
        subprocess.run(["launchctl", "submit", "-l", label, "--", "/bin/sleep", "300"],
                       check=True)
        try:
            pid = None
            while pid is None:
                pid = next((p for p, l in listed().items() if l == label), None)
            self.assertNotIn(pid, check.apple_launchd_jobs({pid}))
            self.assertEqual(check.apple_owned({pid}), {})
        finally:
            subprocess.run(["launchctl", "remove", label], check=False)
        # A job a test bootstraps under an Apple label from a plist of its
        # own, which it then swaps for a link to one of Apple's: launchd
        # still records the test's path, and the link there is the test's.
        # The plist is directly in /tmp, so the link is the one entry this
        # user can replace on the way to it.
        label = f"com.apple.leak-check-test-linked-{os.getpid()}"
        plist = Path("/tmp").resolve() / f"{label}.plist"
        plist.write_text(LAUNCHD_SLEEP.format(label=label))
        domain = f"gui/{os.getuid()}"
        try:
            subprocess.run(["launchctl", "bootstrap", domain, str(plist)], check=True)
            try:
                pid = None
                while pid is None:
                    pid = next((p for p, l in listed().items() if l == label), None)
                plist.unlink()
                plist.symlink_to("/System/Library/LaunchAgents/com.apple.Finder.plist")
                self.assertEqual(check.launchd_definition(label, pid), str(plist))
                self.assertNotIn(pid, check.apple_launchd_jobs({pid}))
            finally:
                subprocess.run(["launchctl", "bootout", f"{domain}/{label}"], check=False)
        finally:
            plist.unlink(missing_ok=True)
        # An orphan is no job at all.
        with subprocess.Popen(["sleep", "300"], start_new_session=True) as child:
            try:
                self.assertNotIn(child.pid, check.apple_launchd_jobs({child.pid}))
            finally:
                child.kill()
                child.wait()

    def test_only_root_can_change_a_definition_under_roots_directories(self):
        if sys.platform == "darwin":
            agents = Path("/System/Library/LaunchAgents")
            owned = next(agents.glob("*.plist"))
        else:
            owned = Path("/usr/bin/env")
        self.assertTrue(check.only_root_changes(owned), owned)
        with tempfile.TemporaryDirectory() as folder:
            mine = Path(folder) / "com.apple.mine.plist"
            mine.write_text("")
            self.assertFalse(check.only_root_changes(mine))
            self.assertFalse(check.only_root_changes(Path(folder) / "absent.plist"))
            # A link of this user's to a file only root can change, directly
            # in /tmp, which root owns and everyone can write: every entry
            # above the link is root's, and the link can be replaced, so
            # what it names now proves nothing.
            link = Path("/tmp") / f"com.apple.leak-check-test-{os.getpid()}.plist"
            link.symlink_to(owned)
            try:
                self.assertFalse(check.only_root_changes(link))
            finally:
                link.unlink()
            # A link only root can replace is followed: /etc is one at / on
            # macOS, and on Linux /etc/hosts is the file itself.
            self.assertTrue(check.only_root_changes("/etc/hosts"))
            if sys.platform == "darwin":
                # A root-owned file this user cannot change, linked into a
                # directory this user can: it could be swapped for another.
                linked = Path(folder) / "hosts"
                os.link("/private/etc/hosts", linked)
                self.assertEqual(linked.stat().st_uid, 0)
                self.assertFalse(check.only_root_changes(linked))
        cryptex = Path("/System/Volumes/Preboot/Cryptexes/App/System/Library/LaunchAgents")
        if sys.platform == "darwin" and cryptex.is_dir():
            # Mode 777 above it, but System Integrity Protection refuses this
            # user writes there and the entry below is flagged against removal.
            for plist in cryptex.glob("*.plist"):
                self.assertTrue(check.only_root_changes(plist), plist)

    @unittest.skipUnless(sys.platform == "darwin", "the per-user temp dir is macOS's")
    def test_an_apple_name_in_the_user_temp_dir_is_set_aside_only_per_package(self):
        # Without --per-package, run reads everything in the per-user temp
        # dir, so where a directory there cannot be listed (Apple's
        # TemporaryItems folders on macOS 27) it fails naming one, before it
        # runs the command, and never sets the new entry aside.
        user_temp = Path(subprocess.check_output(["getconf", "DARWIN_USER_TEMP_DIR"],
                                                 text=True).strip()).resolve()
        try:
            check.temp_entries([user_temp])
            unlistable = None
        except PermissionError as error:
            unlistable = error.filename
        with tempfile.TemporaryDirectory(dir="/tmp") as folder:
            env = dict(os.environ, TMPDIR=folder)
            for flag, expected in (([], "LEAK directory {}"),
                                   (["--per-package"], "set aside: Apple-named temp directory {}")):
                made = user_temp / f"com.apple.leak-check-test-{os.getpid()}-{len(flag)}"
                try:
                    result = subprocess.run(
                        [sys.executable, str(SCRIPT), "run", *flag, "--", "mkdir", str(made)],
                        env=env, text=True, capture_output=True, check=False, timeout=300)
                    if not flag and unlistable is not None:
                        # It refuses at its first reading, before the command.
                        self.assertEqual(result.returncode, 2, result)
                        self.assertIn("this user cannot list it", result.stderr)
                        self.assertNotIn("set aside", result.stdout)
                        self.assertFalse(made.exists(), result)
                        continue
                    self.assertTrue(made.is_dir(), result)
                    self.assertIn(expected.format(made), result.stdout)
                    self.assertNotIn("inspection failed", result.stderr)
                finally:
                    if made.is_dir():
                        made.rmdir()

    def test_an_unreadable_directory_fails_the_walk_only_where_it_is_watched(self):
        # Real directories this user owns and cannot list (mode 000): one
        # under an Apple-named entry, which run --per-package sets aside with
        # everything under it and so does not read, and one under a watched
        # entry, which it must read. A root of the test's own stands for the
        # per-user temp dir.
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            aside = root / "com.apple.leak-check-test" / "sealed"
            watched = root / "watched" / "sealed"
            for sealed in (aside, watched):
                sealed.mkdir(parents=True)
                sealed.chmod(0)
            try:
                self.assertFalse(os.access(aside, os.X_OK),
                                 "a mode 000 directory reads as enterable; "
                                 "this user (root?) cannot be refused one")

                def unread(path):
                    return check.apple_temp_entry(path, root)

                with self.assertRaises(PermissionError) as caught:
                    check.temp_entries([root], unread)
                self.assertEqual(caught.exception.filename, str(watched))
                self.assertIn("this user cannot list it", str(caught.exception))
                watched.chmod(0o700)
                entries = check.temp_entries([root], unread)
                self.assertIn(str(aside.parent), entries)
                self.assertNotIn(str(aside), entries)
                self.assertIn(str(watched), entries)
                # Read everywhere, as run reads without --per-package.
                with self.assertRaises(PermissionError) as caught:
                    check.temp_entries([root])
                self.assertEqual(caught.exception.filename, str(aside))
            finally:
                for sealed in (aside, watched):
                    sealed.chmod(0o700)

    @unittest.skipUnless(sys.platform == "darwin", "the per-user temp dir is macOS's")
    def test_the_real_check_reads_nothing_it_sets_aside_and_all_it_watches(self):
        # The real CLI as CI runs it (run --per-package, TMPDIR elsewhere),
        # over the real per-user temp dir: a mode 000 directory under an
        # Apple-named entry there does not stop it, and one in the watched
        # TMPDIR fails it naming that directory. Other runs on this machine
        # may leave entries of their own, so the first asserts no
        # inspection failure, not a clean exit.
        user_temp = Path(subprocess.check_output(["getconf", "DARWIN_USER_TEMP_DIR"],
                                                 text=True).strip()).resolve()
        with tempfile.TemporaryDirectory(dir="/tmp") as folder:
            env = dict(os.environ, TMPDIR=folder)
            aside = user_temp / f"com.apple.leak-check-test-{os.getpid()}" / "sealed"
            watched = Path(folder).resolve() / "sealed"
            aside.mkdir(parents=True)
            watched.mkdir()
            try:
                aside.chmod(0)

                def run():
                    return subprocess.run(
                        [sys.executable, str(SCRIPT), "run", "--per-package", "--", "true"],
                        env=env, text=True, capture_output=True, check=False, timeout=300)

                result = run()
                self.assertNotIn("inspection failed", result.stderr, result)
                self.assertIn(result.returncode, (0, 1), result)
                watched.chmod(0)
                result = run()
                self.assertEqual(result.returncode, 2, result)
                self.assertIn(f"this user cannot list it, so nothing under it can be checked: "
                              f"'{watched}'", result.stderr)
            finally:
                for sealed in (aside, watched):
                    sealed.chmod(0o700)
                    sealed.rmdir()
                aside.parent.rmdir()

    @unittest.skipUnless(sys.platform == "darwin", "SF_NOUNLINK is set aside on macOS")
    def test_only_an_entry_flagged_against_removal_is_set_aside_by_its_flag(self):
        # /private/var/folders, where every per-user temp dir lives, is
        # flagged SF_NOUNLINK and /private/var/tmp is not: with /private/var
        # standing for the per-user temp dir, an entry under the first is
        # set aside whatever its name, and one under the second is not.
        var = Path("/private/var")
        self.assertTrue(os.lstat(var / "folders").st_flags & check.SF_NOUNLINK,
                        "/private/var/folders is not flagged here, so this shows nothing")
        self.assertFalse(os.lstat(var / "tmp").st_flags & check.SF_NOUNLINK)
        self.assertTrue(check.apple_temp_entry(var / "folders" / "anything", var))
        self.assertFalse(check.apple_temp_entry(var / "tmp" / "anything", var))
        self.assertFalse(check.apple_temp_entry(var / "gone-leak-check-test" / "x", var))
        # This user cannot set the flag, so no test can make an entry that
        # carries it.
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(PermissionError):
                os.chflags(folder, check.SF_NOUNLINK)
            self.assertFalse(os.lstat(folder).st_flags & check.SF_NOUNLINK)

    def test_an_orphan_is_not_an_apple_bundle_service(self):
        with subprocess.Popen(["sleep", "300"], start_new_session=True) as child:
            try:
                self.assertIsNone(check.apple_bundle_service(child.pid))
                self.assertEqual(check.apple_owned({child.pid}), {})
            finally:
                child.kill()
                child.wait()
        if sys.platform != "darwin":
            return
        # Positive control: running Apple XPC services and app extensions
        # launchd started are set aside, each by its bundle.
        output = subprocess.check_output(["ps", "-axo", "pid=,command="], text=True)
        for kind in (".xpc", ".appex"):
            with self.subTest(kind=kind):
                running = [int(line.split(None, 1)[0]) for line in output.splitlines()
                           if f"{kind}/Contents/MacOS/" in line and line.split(None, 1)[1].startswith("/System/")]
                found = [pid for pid in running if check.apple_bundle_service(pid)]
                self.assertTrue(found, f"no running /System {kind} bundle read as launchd's")
        # A bundle launchd records that this user can change is not set
        # aside: /Applications is writable by the admin group.
        with self.subTest(kind="changeable"):
            changeable = [int(line.split(None, 1)[0]) for line in output.splitlines()
                          if "/Contents/MacOS/" in line
                          and line.split(None, 1)[1].startswith("/Applications/")
                          and (".xpc/" in line or ".appex/" in line)]
            if not changeable:
                self.skipTest("no XPC service or app extension from /Applications runs here")
            self.assertEqual([pid for pid in changeable if check.apple_bundle_service(pid)], [])
        # /System/Volumes/Data is the writable data volume, so a bundle there
        # is no Apple bundle whatever its path starts with.
        self.assertFalse(check.only_root_changes(f"/System/Volumes/Data{Path.home()}"))

    def test_missing_root_is_an_error(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(FileNotFoundError):
                check.temp_entries([Path(folder) / "missing"])

    def test_owned_unreadable_tree_is_an_error(self):
        # A real subdirectory this user owns and cannot list or enter: not a
        # root, so not required, and not enterable, so only its owner being
        # this user makes the walk fail rather than set it aside.
        with tempfile.TemporaryDirectory() as folder:
            owned = Path(folder) / "owned"
            owned.mkdir()
            owned.chmod(0)
            try:
                self.assertEqual(owned.stat().st_uid, os.geteuid())
                self.assertFalse(os.access(owned, os.X_OK),
                                 "a mode 000 directory reads as enterable; "
                                 "this user (root?) cannot be refused one")
                with self.assertRaises(PermissionError):
                    list(owned.iterdir())
                with self.assertRaises(PermissionError):
                    check.temp_entries([Path(folder)])
            finally:
                owned.chmod(0o700)

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


EARLY_TEST = """package a

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// With LEAK set, it leaves a detached sleep running and passes. The sleep
// holds an exclusive flock on the file LOCK names until it exits, and its
// pid is written to RECORD once it holds it.
func TestMaybeLeavesADetachedProcess(t *testing.T) {
	if os.Getenv("LEAK") == "" {
		return
	}
	lock, err := os.OpenFile(os.Getenv("LOCK"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "2")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{lock}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := []byte(strconv.Itoa(cmd.Process.Pid))
	if err := os.WriteFile(os.Getenv("RECORD")+".new", pid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(os.Getenv("RECORD")+".new", os.Getenv("RECORD")); err != nil {
		t.Fatal(err)
	}
}
"""

LATE_TEST = """package b

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// With LEAK set, it ends only once package a's sleep has exited: it waits
// for a to record the sleep, then for the sleep's lock. So the sleep
// outlives a and has exited before go test ends.
func TestOutlastsTheEarlyPackagesProcess(t *testing.T) {
	if os.Getenv("LEAK") == "" {
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := os.Stat(os.Getenv("RECORD")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("package a never recorded its sleep")
		}
		time.Sleep(10 * time.Millisecond)
	}
	lock, err := os.Open(os.Getenv("LOCK"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
}
"""


# A launchd job definition running sleep, for a label of the test's own.
LAUNCHD_SLEEP = """<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>{label}</string>
<key>ProgramArguments</key><array><string>/bin/sleep</string><string>300</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
"""

USER_TEMP_TEST = """package a

import (
	"os"
	"testing"
)

// It makes the directory MAKE names and passes.
func TestMakesADirectory(t *testing.T) {
	if err := os.Mkdir(os.Getenv("MAKE"), 0o700); err != nil {
		t.Fatalf("making %s: %v", os.Getenv("MAKE"), err)
	}
}
"""

PS_TEST = """package a

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// It starts ps writing more than a pipe holds into the FIFO FIFO names,
// writes ps's pid to RECORD and passes, leaving ps blocked on that write
// for as long as nobody reads the FIFO.
func TestLeavesPsBlockedOnItsOutput(t *testing.T) {
	out, err := os.OpenFile(os.Getenv("FIFO"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command("/bin/ps", "-A", "-ww", "-E", "-o", "args=", "-o", "args=",
		"-o", "args=", "-o", "args=", "-o", "args=", "-o", "args=", "-o", "args=", "-o", "args=")
	cmd.Stdout = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting ps: %v", err)
	}
	if err := os.WriteFile(os.Getenv("RECORD"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}
"""

SANDBOX_FILTER_PATH = 1  # sandbox_check's filter for a path argument
READ_EXTENSION = "com.apple.app-sandbox.read"  # the class a read grant is issued under
MACH_EXTENSION = "com.apple.app-sandbox.mach"  # the class a mach-lookup grant is issued under


def issue_extension(kind, target):
    """A sandbox extension token granting kind for target, a path for
    READ_EXTENSION and a global name for MACH_EXTENSION, issued by this
    process, which no sandbox confines."""
    libc = ctypes.CDLL("/usr/lib/libSystem.dylib", use_errno=True)
    issue = (libc.sandbox_extension_issue_file if kind == READ_EXTENSION
             else libc.sandbox_extension_issue_mach)
    issue.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint32]
    issue.restype = ctypes.c_void_p
    token = issue(kind.encode(), str(target).encode(), 0)
    if not token:
        raise OSError(ctypes.get_errno(), f"issuing {kind} for {target}")
    try:
        return ctypes.string_at(token).decode()
    finally:
        libc.free(ctypes.c_void_p(token))


def read_denied(pid, path):
    """sandbox_check's answer for pid reading path's data: 1 denied, 0 allowed."""
    libc = ctypes.CDLL("/usr/lib/libSystem.dylib", use_errno=True)
    libc.sandbox_check.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int]
    libc.sandbox_check.restype = ctypes.c_int
    return libc.sandbox_check(pid, b"file-read-data",
                              SANDBOX_FILTER_PATH | check.SANDBOX_CHECK_NO_REPORT,
                              str(path).encode())


# Consumes the sandbox extension token in argv[1], prints "consumed" once it
# has, and waits to be killed.
CONSUME_AND_WAIT = """
import ctypes, sys, time
libc = ctypes.CDLL('/usr/lib/libSystem.dylib', use_errno=True)
libc.sandbox_extension_consume.argtypes = [ctypes.c_char_p]
libc.sandbox_extension_consume.restype = ctypes.c_int64
if libc.sandbox_extension_consume(sys.argv[1].encode()) < 0:
    sys.exit(f'consuming the extension failed: errno {ctypes.get_errno()}')
print('consumed', flush=True)
time.sleep(300)
"""


def running(pid):
    """Whether pid is a running process, an unreaped exited one counting as
    gone. Read with ps, not with the guard's own reads."""
    result = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], text=True,
                            capture_output=True, check=False)
    state = result.stdout.strip()
    return result.returncode == 0 and state != "" and not state.startswith("Z")


class PerPackageTest(unittest.TestCase):
    """The per-package check, run by go test -exec over a real two-package
    module: package a leaves a detached sleep 2 and passes, and package b
    ends only once that sleep has exited, so the sleep outlives a and has
    exited before go test ends, where a comparison at the end of the run
    cannot see it."""

    def go_test(self, module, record, leak, wrapped):
        env = dict(os.environ, RECORD=str(record), LOCK=str(record) + ".lock")
        env.pop("LEAK", None)
        if leak:
            env["LEAK"] = "1"
        command = ["go", "test", "-count=1", "-p", "2"]
        if wrapped:
            command += ["-exec", f"{sys.executable} {SCRIPT} package --"]
        return subprocess.run(command + ["./..."], cwd=module, env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                              check=False, timeout=300)

    def module(self, folder):
        module = Path(folder).resolve() / "module"
        (module / "a").mkdir(parents=True)
        (module / "b").mkdir()
        (module / "go.mod").write_text("module example.com/leaks\n\ngo 1.21\n")
        (module / "a" / "a_test.go").write_text(EARLY_TEST)
        (module / "b" / "b_test.go").write_text(LATE_TEST)
        return module

    def test_an_early_packages_detached_process_fails_that_package(self):
        with tempfile.TemporaryDirectory() as folder:
            module, record = self.module(folder), Path(folder) / "pid"
            # Unwrapped, go test passes, and the sleep has exited by its end:
            # the leak a suite-end comparison misses.
            plain = self.go_test(module, record, leak=True, wrapped=False)
            pid = int(record.read_text())
            self.assertEqual(plain.returncode, 0, plain.stdout)
            self.assertFalse(running(pid), "the sleep outlived go test, so this shows nothing")
            record.unlink()

            leaked = self.go_test(module, record, leak=True, wrapped=True)
            self.assertTrue(record.exists(), f"package a never ran under the check:\n{leaked.stdout}")
            pid = int(record.read_text())
            try:
                self.assertNotEqual(leaked.returncode, 0, leaked.stdout)
                self.assertIn(f"LEAK package={module / 'a'} process pid={pid} ", leaked.stdout)
                self.assertIn("command=sleep 2", leaked.stdout)
                self.assertRegex(leaked.stdout, r"(?m)^FAIL\texample.com/leaks/a\b")
                self.assertRegex(leaked.stdout, r"(?m)^ok  \texample.com/leaks/b\b")
                self.assertFalse(running(pid), "the check left the leaked sleep running")
            finally:
                if running(pid):
                    os.kill(pid, 9)

            # Control: the same module leaving nothing passes the check.
            clean = self.go_test(module, record, leak=False, wrapped=True)
            self.assertEqual(clean.returncode, 0, clean.stdout)
            self.assertNotIn("LEAK", clean.stdout)

    @unittest.skipUnless(sys.platform == "linux", "the subreaper is Linux's")
    def test_a_child_that_exits_before_it_is_read_still_fails_the_package(self):
        # The real check, with the one leftover's exit ordered between the
        # check seeing a child running and its read of the process table:
        # the leftover is killed and waited for, unreaped, as that read
        # starts. Before, nothing was named and the package passed.
        with tempfile.TemporaryDirectory() as folder:
            record = Path(folder) / "pid"
            result = subprocess.run(
                [sys.executable, "-B", "-c", EXIT_BEFORE_READ, str(SCRIPT), str(record), "--",
                 sys.executable, "-c", PLANT_DETACHED, str(record)],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                check=False, timeout=60)
            self.assertIn("exited before the read", result.stdout,
                          "the leftover's exit never fell between the two, so this shows nothing")
            self.assertEqual(result.returncode, 1, result.stdout)
            self.assertIn(f"LEAK package={os.getcwd()} ", result.stdout)

    @unittest.skipUnless(sys.platform == "darwin", "the sandbox mark is macOS's")
    def test_a_process_started_as_its_parent_exits_mid_reading_is_found(self):
        # The real check, with a process in the package's sandbox made to
        # start a detached sleep and exit after the reading listed it and
        # before its mark is read: the sleep is in no listing so far, and the
        # parent reads as unmarked from the moment its exit begins. Its exit
        # is held open on a connection it lingers on, and the order says when
        # the check lets it finish:
        # - exited: before its mark is read, so it is gone or a zombie by then.
        # - exiting: only after the check has read it again after its mark,
        #   so both reads find it exiting and not yet a zombie. Before, the
        #   check took it for a running process outside the sandbox, settled
        #   the reading and passed with the sleep running (CI on macOS 26).
        # - held: never during the reading, which must then fail rather than
        #   settle without the sleep.
        # Only listing again, once the parent has exited, finds the sleep.
        for order, said in (("exited", "exited before its mark was read"),
                            ("exiting", "was still exiting when the check read it again"),
                            ("held", "was still exiting when the check ended")):
            with self.subTest(order=order), tempfile.TemporaryDirectory() as folder:
                parent, child = Path(folder) / "parent", Path(folder) / "child"
                result = subprocess.run(
                    [sys.executable, "-B", "-c", HOLD_IN_EXIT, str(SCRIPT), order, str(parent),
                     str(child), "--", sys.executable, "-c", FORK_ON_SIGNAL, str(parent),
                     str(child)],
                    text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                    check=False, timeout=60)
                self.assertTrue(child.exists(), result.stdout)
                pid = int(child.read_text())
                try:
                    self.assertIn(f"started {pid} and {said}", result.stdout,
                                  "the parent's exit never fell where this order needs it, "
                                  "so this shows nothing")
                    if order == "held":
                        self.assertNotEqual(result.returncode, 0, result.stdout)
                        self.assertIn("no reading of the process table settled", result.stdout)
                        self.assertNotIn("LEAK", result.stdout)
                        continue
                    self.assertEqual(result.returncode, 1, result.stdout)
                    self.assertIn(f"LEAK package={os.getcwd()} process pid={pid} ", result.stdout)
                    self.assertFalse(running(pid), "the check left the sleep running")
                finally:
                    if running(pid):
                        os.kill(pid, 9)

    @unittest.skipUnless(sys.platform == "darwin", "the sandbox mark is macOS's")
    def test_only_the_packages_own_sandbox_marks_a_process(self):
        # Real processes: one in the package's sandbox is marked, and so is
        # one there holding a grant to look up the name its profile denies,
        # which no extension lifts; one outside any sandbox, one in a sandbox
        # that denies both names, and a pid that is gone are not.
        sandbox = check.Sandbox()
        started = []
        try:
            def sleep(*prefix):
                started.append(subprocess.Popen([*prefix, "sleep", "300"]))
                return started[-1]
            inside = sleep("/usr/bin/sandbox-exec", "-p", sandbox.profile)
            outside = sleep()
            both = sleep("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)"
                         f'(deny mach-lookup (global-name "{sandbox.inside}")'
                         f' (global-name "{sandbox.outside}"))')
            granted = subprocess.Popen(
                ["/usr/bin/sandbox-exec", "-p", sandbox.profile, sys.executable, "-c",
                 CONSUME_AND_WAIT, issue_extension(MACH_EXTENSION, sandbox.inside)],
                stdout=subprocess.PIPE, text=True)
            started.append(granted)
            self.assertEqual(granted.stdout.readline().strip(), "consumed")
            gone = subprocess.Popen(["true"])
            gone.wait()
            # sandbox-exec applies the profile before it execs sleep, so a
            # process read as sleep is in its sandbox.
            for process in (inside, both):
                while check.darwin_command(process.pid).split()[:1] != ["sleep"]:
                    pass
            self.assertIs(sandbox.marks(inside.pid), True)
            self.assertIs(sandbox.marks(granted.pid), True)
            self.assertIs(sandbox.marks(outside.pid), False)
            self.assertIs(sandbox.marks(both.pid), False)
            self.assertIs(sandbox.marks(gone.pid), False)
            # A process in the package's sandbox cannot move to another one:
            # sandbox-exec run inside it fails before it starts its command.
            nested = subprocess.run(
                ["/usr/bin/sandbox-exec", "-p", sandbox.profile,
                 "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "true"],
                capture_output=True, text=True, check=False, timeout=30)
            self.assertNotEqual(nested.returncode, 0, nested)
            self.assertIn("sandbox_apply", nested.stderr)
            # A sandboxed process cannot exec a set-user-ID program, and the
            # profile lets none run outside it, where it would carry no mark.
            for program in ("/bin/ps", "/usr/bin/sudo"):
                self.assertTrue(os.stat(program).st_mode & stat.S_ISUID,
                                f"{program} is not set-user-ID here, so this shows nothing")
                refused = subprocess.run(["/usr/bin/sandbox-exec", "-p", sandbox.profile,
                                          program, "-n" if "sudo" in program else "-p1"],
                                         capture_output=True, text=True, check=False,
                                         timeout=30)
                self.assertNotEqual(refused.returncode, 0, refused)
                self.assertIn("Operation not permitted", refused.stderr)
        finally:
            for process in started:
                process.kill()
                process.wait()
                if process.stdout:
                    process.stdout.close()

    @unittest.skipUnless(sys.platform == "darwin", "the sandbox mark is macOS's")
    def test_a_grant_to_one_file_does_not_mark_another_sandboxs_process(self):
        # #551: Spotlight's importer, mdworker_shared, runs in a sandbox that
        # denies it reads and is granted, by a sandbox extension, the file it
        # is importing. Before, the mark was a pair of files the check made
        # in TMPDIR, which Spotlight indexes, so a worker importing the second
        # of them read as in the package's sandbox, and the check failed the
        # package naming the worker and killed it. Here real processes in
        # such a sandbox each hold a real read grant to one file under a
        # TMPDIR of the test's own: each file the check made there, and a
        # control file of the test's, so there is always one. None is marked.
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder).resolve()
            control, unread = folder / "control", folder / "unread"
            control.write_text("granted")
            unread.write_text("never granted")
            with mock.patch.object(tempfile, "tempdir", str(folder)):
                sandbox = check.Sandbox()
            granted = sorted(path for path in folder.rglob("*")
                             if path.is_file() and path != unread)
            self.assertIn(control, granted)
            profile = (f'(version 1)(allow default)(deny file-read-data (subpath "{folder}"))'
                       '(allow file-read-data (extension "com.apple.app-sandbox.read"))')
            for path in granted:
                with self.subTest(granted=path.name):
                    token = issue_extension(READ_EXTENSION, path)
                    process = subprocess.Popen(
                        ["/usr/bin/sandbox-exec", "-p", profile, sys.executable, "-c",
                         CONSUME_AND_WAIT, token],
                        stdout=subprocess.PIPE, text=True)
                    try:
                        self.assertEqual(process.stdout.readline().strip(), "consumed")
                        # The grant is in force: the process may read that
                        # file, and not another beside it.
                        self.assertEqual(read_denied(process.pid, path), 0)
                        self.assertEqual(read_denied(process.pid, unread), 1)
                        self.assertIs(sandbox.marks(process.pid), False)
                    finally:
                        process.kill()
                        process.wait()
                        process.stdout.close()

    @unittest.skipUnless(sys.platform == "darwin", "the sandbox mark is macOS's")
    def test_a_ps_a_package_leaves_blocked_on_its_output_cannot_pass(self):
        # A real package starts /bin/ps writing into a pipe nobody drains
        # (a FIFO this test holds open and never reads), records its pid and
        # passes, leaving ps blocked. ps is set-user-ID here, so the
        # package's sandbox refuses to start it. Before, the profile let it
        # run outside the sandbox, unmarked, and the package passed with it
        # still running.
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder).resolve()
            module, record, fifo = folder / "module", folder / "pid", folder / "fifo"
            (module / "a").mkdir(parents=True)
            (module / "go.mod").write_text("module example.com/leaks\n\ngo 1.21\n")
            (module / "a" / "a_test.go").write_text(PS_TEST)
            os.mkfifo(fifo)
            reader = os.open(fifo, os.O_RDONLY | os.O_NONBLOCK)
            env = dict(os.environ, RECORD=str(record), FIFO=str(fifo))

            def go_test(*wrap):
                return subprocess.run(["go", "test", "-count=1", *wrap, "./..."], cwd=module,
                                      env=env, text=True, stdout=subprocess.PIPE,
                                      stderr=subprocess.STDOUT, check=False, timeout=300)

            def end(pid):
                if running(pid):
                    os.kill(pid, 9)
            try:
                # Control: unwrapped, the package passes with ps still
                # running, blocked on the undrained pipe.
                plain = go_test()
                self.assertEqual(plain.returncode, 0, plain.stdout)
                pid = int(record.read_text())
                try:
                    self.assertTrue(running(pid), "ps finished, so this shows nothing")
                finally:
                    end(pid)
                record.unlink()

                wrapped = go_test("-exec", f"{sys.executable} {SCRIPT} package --")
                if record.exists():
                    end(int(record.read_text()))
                self.assertRegex(wrapped.stdout, r"(?m)^FAIL\texample.com/leaks/a\b")
                self.assertIn("starting ps: fork/exec /bin/ps: operation not permitted",
                              wrapped.stdout)
                self.assertFalse(record.exists(), "ps started under the check")
            finally:
                os.close(reader)

    @unittest.skipUnless(sys.platform == "darwin", "the per-user temp dir is macOS's")
    def test_a_package_cannot_write_in_the_user_temp_dir(self):
        # A real package makes com.apple.<name> in the per-user temp dir,
        # where run --per-package sets Apple-named entries aside. Unwrapped,
        # it passes and the entry stands. Under the check, with TMPDIR
        # elsewhere as in CI, the write is refused and the package fails.
        # Before, the check allowed the write, and the whole-run comparison
        # set the entry aside as Apple's by its name.
        user_temp = Path(subprocess.check_output(["getconf", "DARWIN_USER_TEMP_DIR"],
                                                 text=True).strip()).resolve()
        made = user_temp / f"com.apple.leak-check-test-{os.getpid()}"
        with tempfile.TemporaryDirectory(dir="/tmp") as folder:
            module = Path(folder).resolve() / "module"
            (module / "a").mkdir(parents=True)
            (module / "go.mod").write_text("module example.com/leaks\n\ngo 1.21\n")
            (module / "a" / "a_test.go").write_text(USER_TEMP_TEST)
            env = dict(os.environ, TMPDIR=folder, MAKE=str(made))

            def go_test(*wrap):
                return subprocess.run(["go", "test", "-count=1", *wrap, "./..."], cwd=module,
                                      env=env, text=True, stdout=subprocess.PIPE,
                                      stderr=subprocess.STDOUT, check=False, timeout=300)
            try:
                plain = go_test()
                self.assertEqual(plain.returncode, 0, plain.stdout)
                self.assertTrue(made.is_dir(), "the package made nothing, so this shows nothing")
                made.rmdir()

                wrapped = go_test("-exec", f"{sys.executable} {SCRIPT} package --")
                self.assertRegex(wrapped.stdout, r"(?m)^FAIL\texample.com/leaks/a\b")
                self.assertIn(f"making {made}: mkdir {made}: operation not permitted",
                              wrapped.stdout)
                self.assertFalse(made.exists(), "the package wrote in the per-user temp dir")
            finally:
                if made.is_dir():
                    made.rmdir()

    @unittest.skipIf(sys.platform in ("linux", "darwin"), "the per-package check runs here")
    def test_the_per_package_check_refuses_elsewhere(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "package", "--", "true"],
                                text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 2, result)
        self.assertIn("per-package", result.stderr)


# Starts a detached sleep, records its pid in argv[1], and exits.
PLANT_DETACHED = """
import pathlib, subprocess, sys
p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     start_new_session=True)
pathlib.Path(sys.argv[1]).write_text(str(p.pid))
"""

# Runs the real package_run over the command after "--", with its read of
# the process table preceded, once, by killing the process recorded in
# argv[2] and waiting, without reaping it, until it has exited. It is this
# process's child by then: the subreaper adopted it when its parent exited.
EXIT_BEFORE_READ = """
import importlib.util, os, signal, sys
from pathlib import Path
spec = importlib.util.spec_from_file_location("leak_check", sys.argv[1])
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)
record, command = Path(sys.argv[2]), sys.argv[sys.argv.index("--") + 1:]
read = check.linux_processes
fired = []

def exit_first():
    if not fired:
        fired.append(True)
        pid = int(record.read_text())
        os.kill(pid, signal.SIGKILL)
        os.waitid(os.P_PID, pid, os.WEXITED | os.WNOWAIT)
        print(f"process {pid} exited before the read", flush=True)
    return read()

check.linux_processes = exit_first
sys.exit(check.package_run(command))
"""

# Leaves a detached process that, on SIGUSR1, starts a detached sleep,
# records its pid in argv[2], and exits; records that process's pid in
# argv[1] once it is ready for the signal, and exits. Before that, the
# process connects to the port in argv[3], gives the connection an 8 MiB
# send buffer and fills it, and sets it to linger on close for up to 120
# seconds; it fills the buffer again just before it exits. The other end
# does not read, so the connection cannot deliver it all, and the exit
# stays under way until the other end reads what it was sent. A 4 KiB
# receive buffer set on the listener did not hold it: macOS grew the
# accepted socket's, which took all the data on a CI run.
FORK_ON_SIGNAL = """
import os, pathlib, signal, socket, struct, subprocess, sys
SO_LINGER_SEC = 0x1080  # sys/socket.h: SO_LINGER, its time in seconds
ready, told = os.pipe()
pid = os.fork()
if pid == 0:
    os.setsid()
    held = socket.create_connection(("127.0.0.1", int(sys.argv[3])))
    held.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 8 << 20)
    def fill():
        held.setblocking(False)
        try:
            while True:
                held.send(b"x" * (1 << 20))
        except BlockingIOError:
            pass
        held.setblocking(True)
    fill()
    held.setsockopt(socket.SOL_SOCKET, SO_LINGER_SEC, struct.pack("ii", 1, 120))
    def start(*_):
        p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             start_new_session=True)
        pathlib.Path(sys.argv[2]).write_text(str(p.pid))
        fill()
        os._exit(0)
    signal.signal(signal.SIGUSR1, start)
    os.write(told, b"r")
    while True:
        signal.pause()
os.read(ready, 1)
pathlib.Path(sys.argv[1]).write_text(str(pid))
"""

# Runs the real package_run over the command after "--", passing it the
# port of a connection whose data this process does not read (FORK_ON_SIGNAL)
# as its last argument. The first read of the mark of the process recorded in
# argv[3] is preceded by signalling it to start its sleep and exit, and
# waiting until ps shows it exiting, held there by that connection. argv[2],
# the order, says when the connection is read, which lets the exit finish:
# exited, before the mark is read, once ps no longer shows it exiting;
# exiting, just after the check's next read of that process; held, only once
# package_run has returned or failed. Each prints what ps showed.
HOLD_IN_EXIT = """
import importlib.util, os, signal, socket, subprocess, sys
from pathlib import Path
spec = importlib.util.spec_from_file_location("leak_check", sys.argv[1])
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)
order, parent, child = sys.argv[2], Path(sys.argv[3]), Path(sys.argv[4])
listener = socket.socket()
listener.bind(("127.0.0.1", 0))
listener.listen(1)
command = sys.argv[sys.argv.index("--") + 1:] + [str(listener.getsockname()[1])]
marks, process = check.Sandbox.marks, check.darwin_process
fired, held, again = [], [], []

def state(pid):
    # ps, not the check's own reads: E while exiting, Z once exited, nothing once gone.
    return subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], text=True,
                          capture_output=True, check=False).stdout.strip()

def exiting(pid):
    shown = state(pid)
    return "E" in shown and not shown.startswith("Z")

def tell(pid, what):
    print(f"process {pid} started {child.read_text()} and {what}", flush=True)

def release():
    connection = held.pop()
    while connection.recv(1 << 20):
        pass
    connection.close()

def exit_first(self, pid):
    if not fired and pid == int(parent.read_text()):
        fired.append(True)
        held.append(listener.accept()[0])
        os.kill(pid, signal.SIGUSR1)
        while True:
            shown = state(pid)
            if "E" in shown and not shown.startswith("Z"):
                break
            if shown == "" or shown.startswith("Z"):
                tell(pid, "was not held in its exit")
                break
        if order == "exited":
            release()
            while exiting(pid):
                pass
            tell(pid, "exited before its mark was read")
        elif order == "exiting":
            again.append(pid)
    return marks(self, pid)

def read_then_release(pid):
    reading = process(pid)
    if again and pid == again[0]:
        again.clear()
        if exiting(pid):
            tell(pid, "was still exiting when the check read it again")
        release()
    return reading

check.Sandbox.marks = exit_first
check.darwin_process = read_then_release
try:
    code = check.package_run(command)
finally:
    if held:
        if order == "held" and exiting(int(parent.read_text())):
            tell(int(parent.read_text()), "was still exiting when the check ended")
        release()
sys.exit(code)
"""


if __name__ == "__main__":
    unittest.main()
