#!/usr/bin/env python3
"""Compare runner-user processes and temp entries around a command, and fail
a Go package for every process it leaves running.

Run locally: python3 scripts/leak-check.py run -- go test -race -p 2 ./...
Per package, on Linux:
  python3 scripts/leak-check.py run -- \
    go test -exec "python3 $PWD/scripts/leak-check.py package --" ./...
Prove both reject planted leftovers: python3 scripts/leak-check.py self-test
Only Linux and macOS are supported. Inspection errors fail closed.

run compares one snapshot taken before the command with one taken after it,
so it reports what is still there when the command ends: for go test, the
end of the whole run. A process that outlives its package but exits before
go test ends is not there, and neither is a temp entry removed by then.

package is P15's per-package check (CI fails when any process a test started
outlives its package): go test runs each package's test binary through it,
and it fails that package for every process the binary's tree still has
running once the binary exits (package_run says how, and what it misses).
It needs Linux's child subreaper and refuses anywhere else, so on macOS
nothing checks per package but standin.RunOwned's own check, in the packages
whose TestMain uses it, which sees only processes of that package's binary
and processes carrying its owner identity.

The only things set aside are macOS's own, each listed below with why: launchd
jobs labelled com.apple.*, Apple XPC services launchd started from /System, and
a fixed set of Apple entries in the per-user temp dir when TMPDIR points
elsewhere. On macOS run with a TMPDIR of its own, as CI
does; without one, those temp entries are reported rather than set aside.
"""

import argparse
import ctypes
import errno
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile


class BSDInfo(ctypes.Structure):
    """Darwin's proc_bsdinfo from sys/proc_info.h, including microsecond start."""

    _fields_ = [
        ("fields", ctypes.c_uint32 * 12),
        ("comm", ctypes.c_char * 16),
        ("name", ctypes.c_char * 32),
        ("more", ctypes.c_uint32 * 6),
        ("start_sec", ctypes.c_uint64),
        ("start_usec", ctypes.c_uint64),
    ]


def process_start(pid):
    """Read native start identity, retaining precision lost by ps lstart."""
    if sys.platform == "linux":
        try:
            # comm is parenthesized and can itself contain spaces or ')'.
            fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
            return fields[19]  # Field 22: starttime in clock ticks since boot.
        except (FileNotFoundError, ProcessLookupError):
            return None
    if sys.platform == "darwin":
        lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        info = BSDInfo()
        size = ctypes.sizeof(info)
        lib.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64,
                                     ctypes.c_void_p, ctypes.c_int]
        lib.proc_pidinfo.restype = ctypes.c_int
        count = lib.proc_pidinfo(pid, 3, 0, ctypes.byref(info), size)
        if count != size:
            code = ctypes.get_errno()
            if count == 0 and code == errno.ESRCH:
                return None
            raise OSError(code, f"proc_pidinfo({pid}) returned {count}, expected {size}")
        return f"{info.start_sec}.{info.start_usec:06d}"
    raise RuntimeError(f"unsupported platform: {sys.platform}")


def processes():
    """Collect the effective user's processes, excluding only this inspector."""
    output = subprocess.check_output(
        ["ps", "-axo", "uid=,pid=,command="], text=True, errors="replace"
    )
    found = {}
    for line in output.splitlines():
        uid, pid, *command = line.split(None, 2)
        pid = int(pid)
        if int(uid) != os.geteuid() or pid == os.getpid():
            continue
        start = process_start(pid)
        # ps itself has exited by this read. Other processes may also exit
        # during enumeration; only a native 'gone' result is set aside.
        if start is not None:
            found[(pid, start)] = command[0] if command else ""
    return found


def apple_launchd_jobs():
    """Map the pid of each running com.apple.* job in this user's launchd domain
    to its label.

    macOS's launchd starts Apple's agents and XPC services in the runner's
    session on its own schedule and as a side effect of the toolchain (xcrun
    starts CoreSimulator), and they stay running; a run on a fresh macOS runner
    saw over a hundred. They are the runner's services, not the command's.
    launchctl reports a pid only for a process launchd itself started as that
    job, so an orphan a test leaves, which launchd also adopts, is not one of
    these and is still reported. A test that asks launchd to start an Apple
    job is not reported; stopping one is launchd's.
    """
    if sys.platform != "darwin":
        return {}
    output = subprocess.check_output(["launchctl", "list"], text=True, errors="replace")
    jobs = {}
    for line in output.splitlines()[1:]:
        pid, _, label = line.split("\t", 2)
        if pid != "-" and label.startswith("com.apple."):
            jobs[int(pid)] = label
    return jobs


def apple_xpc_service(pid):
    """The Apple XPC bundle launchd started pid from, or None.

    launchctl list does not show an XPC service a framework starts for its
    client, but launchd records the bundle such a process was started from as
    the originator of the process's own domain (launchctl print pid/<pid>),
    which a process a test starts has none of. Only a bundle under /System,
    the read-only system volume, counts. A process that exits before the read
    is not set aside.
    """
    if sys.platform != "darwin":
        return None
    result = subprocess.run(["launchctl", "print", f"pid/{pid}"], text=True,
                            errors="replace", capture_output=True, check=False)
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        name, _, value = line.strip().partition(" = ")
        if (name == "originator" and value.startswith("/System/")
                and value.endswith(".xpc")):
            return value
    return None


def apple_owned(new_pids):
    """Describe each of new_pids that is macOS's own: a com.apple.* launchd job
    or an Apple XPC service launchd started (apple_xpc_service)."""
    jobs = {pid: f"launchd job {label}" for pid, label in apple_launchd_jobs().items()
            if pid in new_pids}
    for pid in new_pids:
        if pid not in jobs:
            bundle = apple_xpc_service(pid)
            if bundle is not None:
                jobs[pid] = f"launchd XPC service {bundle}"
    return jobs


# Entries Apple's own tools make directly in the per-user temp dir
# (getconf DARWIN_USER_TEMP_DIR), which they find through confstr and not
# TMPDIR: xcrun's cache, and the directories launchd's Apple agents keep there.
# Each is set aside with everything under it, only in that directory, and only
# when TMPDIR is another directory, so the temporary files of go, git, tmux and
# the tests (which follow TMPDIR) can never land under one. Any other new name
# there is still reported.
APPLE_TEMP_NAMES = {
    "xcrun_db",             # xcrun's lookup cache, written by the cgo toolchain
    ".LINKS",               # Apple framework hard-link staging
    "betaenrollmentagent",  # the beta enrollment agent's scratch directory
}
APPLE_TEMP_PREFIX = "com.apple."  # one directory per Apple agent, by bundle id


def apple_temp_root():
    """The per-user temp dir whose Apple entries are set aside, or None.

    None off macOS, and None when TMPDIR resolves to that directory, since the
    command's own temporary files would then share it.
    """
    if sys.platform != "darwin":
        return None
    root = Path(subprocess.check_output(
        ["getconf", "DARWIN_USER_TEMP_DIR"], text=True
    ).strip()).resolve()
    if Path(tempfile.gettempdir()).resolve() == root:
        return None
    return root


def apple_temp_entry(path, root):
    """Whether path is, or is under, an Apple-made entry directly in root."""
    if root is None:
        return False
    try:
        top = Path(path).relative_to(root).parts[0]
    except (ValueError, IndexError):
        return False
    return top in APPLE_TEMP_NAMES or top.startswith(APPLE_TEMP_PREFIX)


def temp_roots():
    """Watch the system temp, user temp, and explicit TMPDIR without aliases."""
    roots = {Path("/tmp").resolve(), Path(tempfile.gettempdir()).resolve()}
    if os.environ.get("TMPDIR"):
        roots.add(Path(os.environ["TMPDIR"]).resolve())
    if sys.platform == "darwin":
        roots.add(Path(subprocess.check_output(
            ["getconf", "DARWIN_USER_TEMP_DIR"], text=True
        ).strip()).resolve())
    return sorted(roots)


def temp_entries(roots):
    """Walk temp directories without following symlinks, including nested sockets."""
    found = {}
    visited = set()

    def visit(path, required=False):
        if path in visited:
            return
        visited.add(path)
        try:
            children = list(path.iterdir())
        except FileNotFoundError:
            if required:
                raise
            return
        except PermissionError:
            # A foreign private directory this user cannot enter cannot
            # receive this user's leaks. Execute-only trees can conceal a
            # writable descendant, so those and owned trees fail closed.
            if (required or path.stat().st_uid == os.geteuid() or
                    os.access(path, os.X_OK)):
                raise
            return
        for child in children:
            try:
                info = child.lstat()
            except FileNotFoundError:
                continue
            kind = ("directory" if stat.S_ISDIR(info.st_mode) else
                    "socket" if stat.S_ISSOCK(info.st_mode) else
                    "symlink" if stat.S_ISLNK(info.st_mode) else "file")
            found[str(child)] = (info.st_dev, info.st_ino, kind)
            if kind == "directory":
                visit(child)

    for root in roots:
        visit(root, required=True)
    return found


def snapshot(roots):
    return processes(), temp_entries(roots)


def differences(before, after, jobs=None, apple_root=None):
    """Return every new process identity and new or replaced filesystem entry,
    and, separately, the new ones set aside as macOS's own.

    jobs maps a pid to a description of it as macOS's own (apple_owned) and
    apple_root is the directory apple_temp_entry applies to.
    """
    jobs = jobs or {}
    old_processes, old_entries = before
    new_processes, new_entries = after
    messages, aside = [], []
    for (pid, start), command in sorted(new_processes.items()):
        if (pid, start) in old_processes:
            continue
        if pid in jobs:
            aside.append(f"{jobs[pid]} pid={pid}")
        else:
            messages.append(f"LEAK process pid={pid} start={start} command={command}")
    for path, entry in sorted(new_entries.items()):
        if old_entries.get(path) == entry:
            continue
        if apple_temp_entry(path, apple_root):
            aside.append(f"Apple temp {entry[2]} {path}")
        else:
            messages.append(f"LEAK {entry[2]} {path}")
    return messages, aside


def guarded_run(command):
    roots = temp_roots()
    print("leak-check: watching " + ", ".join(map(str, roots)), flush=True)
    apple_root = apple_temp_root()
    before = snapshot(roots)
    result = subprocess.run(command, check=False)
    after = snapshot(roots)
    new_pids = {pid for pid, start in after[0] if (pid, start) not in before[0]}
    messages, aside = differences(before, after, apple_owned(new_pids), apple_root)
    for message in aside:
        print(f"leak-check: set aside as macOS's own: {message}", flush=True)
    for message in messages:
        print(message, flush=True)
    print(f"leak-check: {len(messages)} leftover(s); command exit={result.returncode}",
          flush=True)
    return 1 if messages else (result.returncode if result.returncode >= 0 else 1)


def self_test():
    """The guard's positive control: the real CLI must reject and name both leaks."""
    with tempfile.TemporaryDirectory(prefix="leak-check-self-test-") as folder:
        root = Path(folder).resolve()
        pid_file = root / "pid"
        pid_file.touch()  # Exists before the baseline; only content will change.
        leaked = root / "planted-directory"
        code = """
import pathlib, subprocess, sys
p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     start_new_session=True)
pathlib.Path(sys.argv[1]).write_text(str(p.pid))
pathlib.Path(sys.argv[2]).mkdir()
"""
        pid = None
        try:
            result = subprocess.run(
                [sys.executable, str(Path(__file__).resolve()), "run", "--",
                 sys.executable, "-c", code, str(pid_file), str(leaked)],
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                check=False, timeout=60,
            )
            print(result.stdout, end="", flush=True)
            pid = int(pid_file.read_text())
            if (result.returncode != 1 or
                    f"LEAK process pid={pid} " not in result.stdout or
                    f"LEAK directory {leaked}" not in result.stdout):
                raise RuntimeError("self-test did not reject and name both planted leaks")
        finally:
            # Even a broken inspector must not leave its positive control alive.
            if pid is None and pid_file.read_text():
                pid = int(pid_file.read_text())
            if pid is not None:
                os.kill(pid, 9)
        print("leak-check self-test: expected failure observed; planted leaks removed")
    if sys.platform == "linux":
        package_self_test()
    return 0


def package_self_test():
    """The per-package check's positive control: package_run must fail a
    command that leaves a detached process behind, name it, and end it."""
    code = """
import subprocess
p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     start_new_session=True)
print(f'planted {p.pid}', flush=True)
"""
    result = subprocess.run(
        [sys.executable, str(Path(__file__).resolve()), "package", "--",
         sys.executable, "-c", code],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        check=False, timeout=60,
    )
    print(result.stdout, end="", flush=True)
    planted = [line.split()[1] for line in result.stdout.splitlines()
               if line.startswith("planted ")]
    if not planted:
        raise RuntimeError("the per-package self-test planted nothing")
    pid = int(planted[0])
    named = f"LEAK package={os.getcwd()} process pid={pid} " in result.stdout
    running = process_start(pid) is not None
    if running:
        os.kill(pid, 9)
    if result.returncode != 1 or not named or running:
        raise RuntimeError("per-package self-test did not fail, name and end the planted process")
    print("leak-check self-test: per-package check failed, named and ended its planted process")


PR_SET_CHILD_SUBREAPER = 36  # from linux/prctl.h


def linux_processes():
    """Map every running pid in /proc to (parent pid, start, command line).

    A process in state Z or X has exited and is left out. A process that
    exits during the walk is left out too; the caller decides from its own
    children, not from this map.
    """
    found = {}
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            fields = (entry / "stat").read_text().rsplit(")", 1)[1].split()
            command = (entry / "cmdline").read_bytes()
        except (FileNotFoundError, ProcessLookupError):
            continue
        if fields[0] in ("Z", "X"):
            continue
        found[int(entry.name)] = (int(fields[1]), fields[19],
                                  command.rstrip(b"\0").replace(b"\0", b" ")
                                  .decode(errors="replace"))
    return found


def descendants(table, root):
    """Every pid in table whose chain of parents reaches root."""
    children = {}
    for pid, (parent, _, _) in table.items():
        children.setdefault(parent, []).append(pid)
    found, frontier = [], [root]
    while frontier:
        for child in children.get(frontier.pop(), []):
            found.append(child)
            frontier.append(child)
    return sorted(found)


def children_running():
    """Reap every child of this process that has exited, and report whether
    any child is still running. No child at all is False."""
    while True:
        try:
            pid, _ = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return False
        if pid == 0:
            return True


def package_run(command):
    """Run one package's test binary and fail it for every process it left.

    go test runs this as -exec, once per package, in that package's
    directory. It makes this process Linux's child subreaper for its
    descendants first, so a process the binary or anything under it starts
    is reparented here, never to init, when its parent exits, whatever
    session, group or environment it runs in. While the binary runs, it reaps
    every such process that exits. Once the binary has exited, any child
    still running is a process the package started that outlived it (P15):
    each of them and everything under it is printed as a LEAK line naming the
    package, and the package fails. That question is asked of the kernel's
    own record of this process's children (waitpid), so a process that
    starts another and exits while this looks still leaves a child here.
    Each generation is then killed: only this process's own unreaped
    children are signalled, so no pid can have been reused, and what each
    leaves is reparented here for the next round, until no child remains.

    When LEAK_CHECK_RECORD names a file, the package's directory is appended
    to it before the binary starts, so a caller can tell every package went
    through this check rather than trusting the -exec flag reached go test.

    The binary's exit status is passed on, or 1 when it was 0 and something
    was left; a binary ended by a signal ends this process with the same
    signal. A process the package had started through a service manager or
    another process outside its tree (launchd, systemd, a running daemon) is
    not its descendant and is not seen here; the comparison around the whole
    run (run) still covers this user's processes and the temp roots. Only
    Linux has the subreaper, and elsewhere this refuses rather than pass.
    """
    if sys.platform != "linux":
        raise RuntimeError("the per-package check needs Linux's child subreaper "
                           f"(prctl PR_SET_CHILD_SUBREAPER), and this is {sys.platform}")
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
        code = ctypes.get_errno()
        raise OSError(code, "prctl(PR_SET_CHILD_SUBREAPER)")
    record = os.environ.get("LEAK_CHECK_RECORD")
    if record:
        with open(record, "a", encoding="utf-8") as out:
            out.write(os.getcwd() + "\n")
    child = os.posix_spawnp(command[0], command, os.environ)

    def forward(signum, _frame):
        try:
            os.kill(child, signum)
        except ProcessLookupError:
            pass

    for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP, signal.SIGQUIT):
        signal.signal(signum, forward)
    while True:
        pid, status = os.waitpid(-1, 0)
        if pid == child:
            break
    package, me = os.getcwd(), os.getpid()
    named = set()
    while children_running():
        table = linux_processes()
        for pid in descendants(table, me):
            parent, start, line = table[pid]
            if (pid, start) not in named:
                named.add((pid, start))
                print(f"LEAK package={package} process pid={pid} start={start} command={line}",
                      flush=True)
        for pid, (parent, _, _) in table.items():
            if parent == me:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        try:
            os.waitpid(-1, 0)
        except ChildProcessError:
            break
    if named:
        print(f"leak-check: package {package} left {len(named)} process(es) running", flush=True)
    if os.WIFSIGNALED(status):
        signal.signal(os.WTERMSIG(status), signal.SIG_DFL)
        os.kill(me, os.WTERMSIG(status))
    code = os.waitstatus_to_exitcode(status)
    return 1 if code == 0 and named else code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["run", "self-test", "package"])
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        if args.mode == "self-test":
            if args.command:
                parser.error("self-test takes no command")
            return self_test()
        command = args.command
        if command[:1] == ["--"]:
            command = command[1:]
        if not command:
            parser.error(f"{args.mode} requires a command")
        if args.mode == "package":
            return package_run(command)
        return guarded_run(command)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"leak-check: inspection failed: {error}", file=sys.stderr, flush=True)
        return 2


if __name__ == "__main__":
    sys.exit(main())
