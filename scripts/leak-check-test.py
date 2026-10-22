#!/usr/bin/env python3
"""Executable and model tests for the CI leftover guard, leak-check.py."""

import contextlib
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
        user_temp = Path(subprocess.check_output(["getconf", "DARWIN_USER_TEMP_DIR"],
                                                 text=True).strip()).resolve()
        with tempfile.TemporaryDirectory(dir="/tmp") as folder:
            env = dict(os.environ, TMPDIR=folder)
            for flag, expected in (([], "LEAK directory {}"),
                                   (["--per-package"], "set aside: Apple-named temp directory {}")):
                made = user_temp / f"com.apple.leak-check-test-{os.getpid()}-{len(flag)}"
                try:
                    result = subprocess.run(
                        [sys.executable, str(SCRIPT), "run", *flag, "--", "mkdir", str(made)],
                        env=env, text=True, capture_output=True, check=False, timeout=300)
                    self.assertTrue(made.is_dir(), result)
                    self.assertIn(expected.format(made), result.stdout)
                finally:
                    if made.is_dir():
                        made.rmdir()

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
        # parent reads as unmarked once gone. Only listing again finds it.
        with tempfile.TemporaryDirectory() as folder:
            parent, child = Path(folder) / "parent", Path(folder) / "child"
            result = subprocess.run(
                [sys.executable, "-B", "-c", EXIT_BEFORE_MARK, str(SCRIPT), str(parent), str(child),
                 "--", sys.executable, "-c", FORK_ON_SIGNAL, str(parent), str(child)],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                check=False, timeout=60)
            self.assertTrue(child.exists(), result.stdout)
            pid = int(child.read_text())
            try:
                self.assertIn("exited before its mark was read", result.stdout,
                              "the parent's exit never fell inside the reading, so this shows nothing")
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn(f"LEAK package={os.getcwd()} process pid={pid} ", result.stdout)
                self.assertFalse(running(pid), "the check left the sleep running")
            finally:
                if running(pid):
                    os.kill(pid, 9)

    @unittest.skipUnless(sys.platform == "darwin", "the sandbox mark is macOS's")
    def test_only_the_packages_own_sandbox_marks_a_process(self):
        # Real processes: one in the package's sandbox is marked; one outside
        # any sandbox, one in a sandbox that denies both files, and a pid
        # that is gone are not.
        sandbox = check.Sandbox()
        started = []
        try:
            def sleep(*prefix):
                started.append(subprocess.Popen([*prefix, "sleep", "300"]))
                return started[-1]
            inside = sleep("/usr/bin/sandbox-exec", "-p", sandbox.profile)
            outside = sleep()
            both = sleep("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)"
                         f'(deny file-read-data (subpath "{sandbox.folder}"))')
            gone = subprocess.Popen(["true"])
            gone.wait()
            # sandbox-exec applies the profile before it execs sleep, so a
            # process read as sleep is in its sandbox.
            for process in (inside, both):
                while check.darwin_command(process.pid).split()[:1] != ["sleep"]:
                    pass
            self.assertIs(sandbox.marks(inside.pid), True)
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
            sandbox.remove()

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
# argv[1] once it is ready for the signal, and exits.
FORK_ON_SIGNAL = """
import os, pathlib, signal, subprocess, sys
ready, told = os.pipe()
pid = os.fork()
if pid == 0:
    os.setsid()
    def start(*_):
        p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             start_new_session=True)
        pathlib.Path(sys.argv[2]).write_text(str(p.pid))
        os._exit(0)
    signal.signal(signal.SIGUSR1, start)
    os.write(told, b"r")
    while True:
        signal.pause()
os.read(ready, 1)
pathlib.Path(sys.argv[1]).write_text(str(pid))
"""

# Runs the real package_run over the command after "--", with the first read
# of the mark of the process recorded in argv[2] preceded by signalling it to
# start its sleep and exit, and waiting for that exit.
EXIT_BEFORE_MARK = """
import importlib.util, os, select, signal, sys
from pathlib import Path
spec = importlib.util.spec_from_file_location("leak_check", sys.argv[1])
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)
parent, child = Path(sys.argv[2]), Path(sys.argv[3])
command = sys.argv[sys.argv.index("--") + 1:]
read = check.Sandbox.marks
fired = []

def exit_first(self, pid):
    if not fired and pid == int(parent.read_text()):
        fired.append(True)
        queue = select.kqueue()
        queue.control([select.kevent(pid, select.KQ_FILTER_PROC,
                                     select.KQ_EV_ADD | select.KQ_EV_ONESHOT,
                                     select.KQ_NOTE_EXIT)], 0)
        os.kill(pid, signal.SIGUSR1)
        queue.control(None, 1)
        print(f"process {pid} started {child.read_text()} and exited before its mark was read",
              flush=True)
    return read(self, pid)

check.Sandbox.marks = exit_first
sys.exit(check.package_run(command))
"""


if __name__ == "__main__":
    unittest.main()
