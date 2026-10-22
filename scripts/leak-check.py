#!/usr/bin/env python3
"""Compare runner-user processes and temp entries around a command, and fail
a Go package for every process it leaves running.

Run locally: python3 scripts/leak-check.py run -- go test -race -p 2 ./...
Per package as well:
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
and it fails that package for every process the binary started that is
still running once the binary exits, with other packages running beside it.
Linux ties a process to its package with the child subreaper, macOS with a
sandbox profile the package's processes inherit; package_run says how, and
what neither sees.

The only things set aside are taken for macOS's, each printed, and each only
on evidence that rules out the tests' own processes: a com.apple.* launchd job
whose definition only root can have made (apple_launchd_jobs), an XPC service
or app extension launchd started from a bundle only root can have made
(apple_bundle_service), and, with
run --per-package, entries with Apple's names in the per-user temp dir, where
the per-package check denies every package's processes writes (guarded_run).
On macOS run with a TMPDIR of its own, as CI does; without one, those temp
entries are reported rather than set aside.
"""

import argparse
import ctypes
import errno
import os
from pathlib import Path
import signal
import stat
import struct
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


SF_IMMUTABLE = 0x00020000  # sys/stat.h: no change, rename or removal, even by root
SF_RESTRICTED = 0x00080000  # sys/stat.h: System Integrity Protection's restricted flag
SF_NOUNLINK = 0x00100000  # sys/stat.h: no rename or removal, even by root


def only_root_changes(path):
    """Whether no user but root can have put path where it is, or changed it.

    path, resolved, must exist, and must be unchangeable: on a read-only
    volume, or flagged immutable or restricted, or owned by root and writable
    by neither its group nor others. Each entry from it up to / must also be
    irreplaceable: its directory unchangeable in the same sense, or the entry
    itself flagged so that it cannot be renamed or removed. Mode bits alone
    do not decide it: System Integrity Protection refuses this user writes in
    /System/Volumes/Preboot/Cryptexes, which is mode 777, and the entry
    below it there is flagged against removal.
    """
    try:
        target = Path(os.path.realpath(path))
        chain = [target, *target.parents]
        infos = {node: os.lstat(node) for node in chain}
        volumes = {node: os.statvfs(node) for node in chain}
    except OSError:
        return False

    def unchangeable(node):
        info = infos[node]
        return bool(volumes[node].f_flag & os.ST_RDONLY
                    or getattr(info, "st_flags", 0) & (SF_IMMUTABLE | SF_RESTRICTED)
                    or (info.st_uid == 0 and not info.st_mode & 0o022))

    def irreplaceable(node):
        flags = getattr(infos[node], "st_flags", 0)
        return (node == node.parent or unchangeable(node.parent)
                or bool(flags & (SF_IMMUTABLE | SF_RESTRICTED | SF_NOUNLINK)))

    return unchangeable(target) and all(irreplaceable(node) for node in chain)


def apple_launchd_jobs(new_pids):
    """Map each of new_pids that a com.apple.* job in this user's launchd
    domains runs to its label, when launchd loaded that job from a definition
    no user but root can have made.

    macOS's launchd starts Apple's agents and XPC services in the runner's
    session on its own schedule and as a side effect of the toolchain (xcrun
    starts CoreSimulator), and they stay running; a run on a fresh macOS runner
    saw over a hundred. They are the runner's services, not the command's.
    launchctl reports a pid only for a process launchd itself started as that
    job, so an orphan a test leaves, which launchd also adopts, is not one of
    these and is still reported.

    A label is not proof: a test can submit a job under any label. So the job
    is read again by its label (launchctl print), and set aside only when it
    still runs as that pid and the definition launchd recorded for it, a plist
    or an XPC bundle, is one only root can have put there (only_root_changes).
    A submitted job records none, and one a test bootstraps from a plist of
    its own records that plist. A test that asks launchd to start one of
    Apple's own jobs from its own definition is not reported; stopping it is
    launchd's.
    """
    if sys.platform != "darwin":
        return {}
    output = subprocess.check_output(["launchctl", "list"], text=True, errors="replace")
    jobs = {}
    for line in output.splitlines()[1:]:
        pid, _, label = line.split("\t", 2)
        if pid == "-" or int(pid) not in new_pids or not label.startswith("com.apple."):
            continue
        definition = launchd_definition(label, int(pid))
        if definition is not None and only_root_changes(definition):
            jobs[int(pid)] = label
    return jobs


def launchd_definition(label, pid):
    """The definition path launchd recorded for the job label in this user's
    gui or user domain while it runs as pid, or None."""
    for domain in (f"gui/{os.getuid()}", f"user/{os.getuid()}"):
        result = subprocess.run(["launchctl", "print", f"{domain}/{label}"], text=True,
                                errors="replace", capture_output=True, check=False)
        if result.returncode != 0:
            continue
        fields = {}
        for line in result.stdout.splitlines():
            if line.startswith("\t") and not line.startswith("\t\t"):
                name, _, value = line.strip().partition(" = ")
                fields.setdefault(name, value)
        if fields.get("pid") == str(pid) and fields.get("path", "").startswith("/"):
            return fields["path"]
    return None


def apple_bundle_service(pid):
    """The Apple XPC service or app extension bundle launchd started pid
    from, or None.

    launchctl list shows neither an XPC service a framework starts for its
    client nor an app extension the system starts for its host, but launchd
    records the bundle such a process was started from as the originator of
    the process's own domain (launchctl print pid/<pid>), which a process a
    test starts has none of. The bundle counts only when it is an .xpc or
    .appex that no user but root can have made or replaced
    (only_root_changes): a path under /System is not enough, since
    /System/Volumes/Data is the writable data volume. A process that exits
    before the read is not set aside.
    """
    if sys.platform != "darwin":
        return None
    result = subprocess.run(["launchctl", "print", f"pid/{pid}"], text=True,
                            errors="replace", capture_output=True, check=False)
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        name, _, value = line.strip().partition(" = ")
        if (name == "originator" and value.endswith((".xpc", ".appex"))
                and only_root_changes(value)):
            return value
    return None


def apple_owned(new_pids):
    """Describe each of new_pids that is macOS's own: a com.apple.* launchd job
    (apple_launchd_jobs) or an Apple XPC service or app extension launchd
    started (apple_bundle_service)."""
    jobs = {pid: f"launchd job {label}" for pid, label in apple_launchd_jobs(new_pids).items()}
    for pid in new_pids:
        if pid not in jobs:
            bundle = apple_bundle_service(pid)
            if bundle is not None:
                jobs[pid] = f"launchd {'app extension' if bundle.endswith('.appex') else 'XPC service'} {bundle}"
    return jobs


# Names Apple's own tools give entries directly in the per-user temp dir
# (getconf DARWIN_USER_TEMP_DIR), which they find through confstr and not
# TMPDIR: xcrun's cache, the directories launchd's Apple agents keep there,
# and the one Foundation makes item-replacement directories in.
# A name proves nothing about who made the entry, so one is set aside, with
# everything under it, only in that directory, only when TMPDIR is another
# directory, and only by run --per-package, whose packages cannot write there
# (guarded_run). Any other new name there is still reported.
APPLE_TEMP_NAMES = {
    "xcrun_db",             # xcrun's lookup cache, written by the cgo toolchain
    ".LINKS",               # Apple framework hard-link staging
    "betaenrollmentagent",  # the beta enrollment agent's scratch directory
    "TemporaryItems",       # Foundation's NSIRD_<program>_* directories; CI's
                            # macOS runner left xcodebuild's plugin cache in one
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
    """Whether path is, or is under, an entry directly in root with one of
    the names Apple's tools use there (APPLE_TEMP_NAMES, APPLE_TEMP_PREFIX).
    The name is all it reads."""
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
    and, separately, the new ones set aside as taken for macOS's.

    jobs maps a pid to a description of it as macOS's own (apple_owned) and
    apple_root is the directory apple_temp_entry applies to, None for none.
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
            aside.append(f"Apple-named temp {entry[2]} {path}")
        else:
            messages.append(f"LEAK {entry[2]} {path}")
    return messages, aside


def guarded_run(command, per_package=False):
    """Run command and fail for every runner-user process and temp entry it
    left: new when it ends, and not set aside as macOS's own (differences).

    A name in the per-user temp dir is no proof of who made the entry, since
    a test can make com.apple.anything there as easily as an Apple agent. So
    run sets aside the Apple entries there (apple_temp_entry) only when
    per_package says the command runs every test binary through package,
    whose sandbox denies all of the package's processes writes there
    (Sandbox), and only when TMPDIR is elsewhere (apple_temp_root). run does
    not check that claim; CI's step does, by failing unless every package go
    test reported ok went through package. Without per_package, every new
    entry there is reported. What the sandbox does not cover is an entry made
    there by a process outside every package's tree, such as a launchd job a
    test submitted (package_run), whose entry is set aside when its name is
    one of Apple's.
    """
    roots = temp_roots()
    print("leak-check: watching " + ", ".join(map(str, roots)), flush=True)
    apple_root = apple_temp_root() if per_package else None
    if per_package and apple_root is not None:
        print(f"leak-check: Apple entries in {apple_root} are set aside; the per-package "
              "check denies every package's processes writes there", flush=True)
    before = snapshot(roots)
    result = subprocess.run(command, check=False)
    after = snapshot(roots)
    new_pids = {pid for pid, start in after[0] if (pid, start) not in before[0]}
    messages, aside = differences(before, after, apple_owned(new_pids), apple_root)
    for message in aside:
        print(f"leak-check: set aside: {message}", flush=True)
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
    package_self_test()
    return 0


def package_self_test():
    """The per-package check's controls: package_run must fail a command that
    leaves a detached process behind, name it and end it, and must neither
    name nor end a process started outside the command. Where run sets aside
    Apple-named entries in the per-user temp dir (apple_temp_root), the
    command must also be refused making one there."""
    code = """
import os, subprocess, sys
p = subprocess.Popen(['sleep', '300'], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     start_new_session=True)
print(f'planted {p.pid}', flush=True)
if len(sys.argv) > 1:
    target = os.path.join(sys.argv[1], f'com.apple.leak-check-self-test-{os.getpid()}')
    try:
        os.mkdir(target)
        print(f'wrote {target}', flush=True)
    except PermissionError as error:
        print(f'refused {target}: {error.strerror}', flush=True)
"""
    user_temp = apple_temp_root()
    bystander = subprocess.Popen(["sleep", "300"], stdin=subprocess.DEVNULL,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                 start_new_session=True)
    try:
        result = subprocess.run(
            [sys.executable, str(Path(__file__).resolve()), "package", "--",
             sys.executable, "-c", code] + ([str(user_temp)] if user_temp else []),
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
        wrote = [line.split(None, 1)[1] for line in result.stdout.splitlines()
                 if line.startswith("wrote ")]
        for path in wrote:
            os.rmdir(path)
        if result.returncode != 1 or not named or running:
            raise RuntimeError("per-package self-test did not fail, name and end the planted process")
        if f"pid={bystander.pid} " in result.stdout or bystander.poll() is not None:
            raise RuntimeError("per-package self-test named or ended a process outside the package")
        if user_temp is not None and (wrote or "refused " not in result.stdout):
            raise RuntimeError(f"the per-package check let a package write in {user_temp}, "
                               "where run sets Apple-named entries aside")
    finally:
        bystander.kill()
        bystander.wait()
    print("leak-check self-test: per-package check failed, named and ended its planted process, "
          "and left a process outside the package alone")
    if user_temp is not None:
        print(f"leak-check self-test: per-package check refused a write in {user_temp}")
    elif sys.platform == "darwin":
        print("leak-check self-test: TMPDIR is the per-user temp dir, so the per-package check "
              "does not deny writes there and run sets nothing aside there; neither was checked")


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


def linux_leftovers(package):
    """The Linux half of package_run, once the binary has exited and been
    reaped: report and end every process the package left, and return how
    many rounds found one running.

    This process is the subreaper of everything the binary started, so a
    process of the package's still running is this process's child, or a
    descendant of one. Each round asks waitpid whether a child is running;
    once one is, the package has failed, whatever the reads after it find.
    The round then prints a LEAK line for each descendant /proc still shows
    running, or, when it shows none because they exited after waitpid
    answered, one line saying so. It kills this process's own children: only
    unreaped children are signalled, so no pid can have been reused. What
    they leave is reparented here for the next round, until no child remains.
    A process that exits between the binary's exit and the first round's
    waitpid is reaped there and is not seen.
    """
    me, named, rounds = os.getpid(), set(), 0
    while children_running():
        rounds += 1
        table = linux_processes()
        seen = descendants(table, me)
        for pid in seen:
            _, start, line = table[pid]
            if (pid, start) not in named:
                named.add((pid, start))
                print(f"LEAK package={package} process pid={pid} start={start} command={line}",
                      flush=True)
        if not seen:
            print(f"LEAK package={package} a process was running after the binary exited "
                  "and exited before the check read it", flush=True)
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
    return rounds


KINFO_PROC_SIZE = 648  # sizeof(struct kinfo_proc) from sys/sysctl.h, 64-bit
SZOMB = 5  # extern_proc.p_stat of a process that has exited, from sys/proc.h
SANDBOX_FILTER_PATH = 1  # sandbox_check's filter for a path argument
SANDBOX_CHECK_NO_REPORT = 0x40000000  # sandbox_check: do not log a denial
SETTLE_READINGS = 64


def darwin_records(mib):
    """The kinfo_proc records sysctl returns for mib, as (pid, start, zombie):
    start is the kernel's start time in the form process_start uses."""
    libc = ctypes.CDLL(None, use_errno=True)
    name = (ctypes.c_int * len(mib))(*mib)
    for _ in range(SETTLE_READINGS):
        size = ctypes.c_size_t(0)
        if libc.sysctl(name, len(mib), None, ctypes.byref(size), None, 0) != 0:
            raise OSError(ctypes.get_errno(), f"sysctl {mib} size")
        size.value += size.value // 4 + KINFO_PROC_SIZE
        buffer = ctypes.create_string_buffer(size.value)
        if libc.sysctl(name, len(mib), buffer, ctypes.byref(size), None, 0) == 0:
            break
        code = ctypes.get_errno()
        if code != errno.ENOMEM:  # ENOMEM: the table grew past the buffer
            raise OSError(code, f"sysctl {mib}")
    else:
        raise RuntimeError(f"sysctl {mib} outgrew its buffer {SETTLE_READINGS} times")
    if size.value % KINFO_PROC_SIZE:
        raise RuntimeError(f"sysctl {mib} returned {size.value} bytes, "
                           f"not whole {KINFO_PROC_SIZE}-byte records")
    records = []
    for offset in range(0, size.value, KINFO_PROC_SIZE):
        # extern_proc: p_starttime at 0, p_stat at 36, p_pid at 40.
        sec, usec = struct.unpack_from("<qi", buffer.raw, offset)
        pid, = struct.unpack_from("<i", buffer.raw, offset + 40)
        records.append((pid, f"{sec}.{usec:06d}", buffer.raw[offset + 36] == SZOMB))
    return records


def darwin_table():
    """Map (pid, start) to whether it has exited, for every process in the
    kernel's table (sysctl kern.proc.all), whatever its user."""
    return {(pid, start): zombie for pid, start, zombie in darwin_records((1, 14, 0))}


def darwin_process(pid):
    """(start, exited) for the process pid now is, or None once it is gone
    (sysctl kern.proc.pid)."""
    records = darwin_records((1, 14, 1, pid))
    return (records[0][1], records[0][2]) if records else None


class Sandbox:
    """A sandbox profile that marks a package's processes, and the reads that
    tell them apart.

    The profile allows everything except reading one file, inside, and the
    writes below, so a process in it denies inside and allows outside, two empty files in a
    directory of their own. A process outside it allows both, and one in
    another sandbox that denies both is not marked either. A process started
    under the profile is in it after a new session, its parent's exit and
    exec, and sandbox-exec run inside it fails before starting its command,
    so a process the package starts cannot leave the mark that way; the
    guard's tests check each of these on the machine they run on.

    Given unwritable, a directory, the profile also denies every write under
    it: package_run passes the per-user temp dir when TMPDIR is elsewhere
    (apple_temp_root), so no process of the package can make an entry there,
    whatever its name, and an entry run sets aside there by name is not one
    of the package's (guarded_run).

    A sandboxed process cannot exec a set-user-ID program (/bin/ps and sudo
    among them): the start fails with EPERM. The profile lets none run
    outside it, since what it started there would carry no mark, so a test
    that runs one fails under this check rather than leaving a process it
    cannot see.
    sandbox_check reads the mark of any pid; for a pid that is gone it
    answers denied for both files, which is not the mark.
    """

    def __init__(self, unwritable=None):
        self.folder = Path(tempfile.mkdtemp(prefix="leak-check-package-")).resolve()
        self.inside = self.folder / "inside"
        self.outside = self.folder / "outside"
        self.inside.touch()
        self.outside.touch()
        for path in (self.inside, unwritable):
            if path is not None and ('"' in str(path) or "\\" in str(path)):
                raise RuntimeError(f"cannot quote {path} in a sandbox profile")
        self.profile = ("(version 1)(allow default)"
                        f'(deny file-read-data (literal "{self.inside}"))')
        if unwritable is not None:
            self.profile += f'(deny file-write* (subpath "{unwritable}"))'
        self.check = ctypes.CDLL("/usr/lib/libSystem.dylib", use_errno=True).sandbox_check
        self.check.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int]
        self.check.restype = ctypes.c_int

    def denies(self, pid, path):
        return self.check(pid, b"file-read-data",
                          SANDBOX_FILTER_PATH | SANDBOX_CHECK_NO_REPORT, str(path).encode())

    def marks(self, pid):
        """True when pid is in this sandbox, False when it is not or is gone,
        None when sandbox_check failed."""
        inside, outside = self.denies(pid, self.inside), self.denies(pid, self.outside)
        if inside < 0 or outside < 0:
            return None
        return inside == 1 and outside == 0

    def remove(self):
        for path in (self.inside, self.outside):
            path.unlink(missing_ok=True)
        self.folder.rmdir()


def darwin_marked(sandbox):
    """Every (pid, start) a settled reading finds running in sandbox.

    A reading lists the kernel's table and, for each process running in it
    that it has not settled, reads the mark and then the process again. A
    process still the same and running at that second read was running when
    its mark was read, so the mark settles it either way; a marked one is
    found even if it has exited by then. One that has exited by the second
    read and is not marked may have been read after it was gone, and may have
    started a process before it went that the listing could not hold, so the
    table is listed again, and only a listing in which every process settled
    ends the reading. A process that exits before its mark is read, and
    started nothing, is not found. SETTLE_READINGS listings without one that
    settles are an error.
    """
    found, settled = set(), set()
    for _ in range(SETTLE_READINGS):
        unsettled = False
        # Newest first: a process the package left is among the last started.
        table = darwin_table()
        for key in sorted(table, key=lambda k: tuple(map(int, k[1].split("."))), reverse=True):
            if table[key] or key in settled:
                continue
            mark = sandbox.marks(key[0])
            running = darwin_process(key[0]) == (key[1], False)
            if mark:
                found.add(key)
            if mark or (running and mark is not None):
                settled.add(key)
            if not running or mark is None:
                unsettled = True
        if not unsettled:
            return found
    raise RuntimeError(f"no reading of the process table settled in {SETTLE_READINGS} listings")


def darwin_command(pid):
    result = subprocess.run(["ps", "-o", "command=", "-p", str(pid)], text=True,
                            errors="replace", capture_output=True, check=False)
    return result.stdout.strip()


def darwin_leftovers(package, sandbox):
    """The macOS half of package_run, once the binary has exited and been
    reaped: report and end every process in the package's sandbox, and return
    how many readings found one.

    The binary ran in sandbox, so every process the package started is in it
    and nothing else is (Sandbox). Each settled reading (darwin_marked) that
    finds a process fails the package, prints a LEAK line for each process
    not already named, and kills each that is still the same process when
    read again just before the signal; a process that exited and whose pid
    was reused in that moment would be signalled instead. Readings repeat
    until one finds nothing, SETTLE_READINGS at most, after which this is an
    error.
    """
    named, rounds = set(), 0
    for _ in range(SETTLE_READINGS):
        found = darwin_marked(sandbox)
        if not found:
            return rounds
        rounds += 1
        for pid, start in sorted(found - named):
            print(f"LEAK package={package} process pid={pid} start={start} "
                  f"command={darwin_command(pid)}", flush=True)
        named |= found
        for pid, start in found:
            if darwin_process(pid) == (start, False):
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
    raise RuntimeError(f"processes in the package's sandbox still ran after "
                       f"{SETTLE_READINGS} rounds of ending them")


def package_run(command):
    """Run one package's test binary and fail it for every process it left.

    This is P15's per-package check (CI fails when any process a test started
    outlives its package). go test runs this as -exec, once per package, in
    that package's directory, and other packages run beside it. Once the
    binary has exited, a process the package started that is still running
    fails the package: it is printed as a LEAK line naming the package, and
    ended. How a process is tied to its package:

    - Linux: this process makes itself the child subreaper of its
      descendants, so a process anything under the binary starts is
      reparented here, never to init, when its parent exits, whatever
      session, group or environment it runs in (linux_leftovers).
    - macOS: the binary runs under sandbox-exec with a profile of this run's
      own, which every process it starts inherits (Sandbox, darwin_leftovers).

    Neither sees a process the package had started through a service manager
    or another process outside its tree (launchd, systemd, a running daemon),
    since that process is not its descendant, nor one that exits between the
    binary's exit and the check's first read of it. On macOS a package
    cannot start a set-user-ID program under the check (Sandbox). The
    comparison around the whole run (run) still covers this user's processes
    and the temp roots at the end.

    When LEAK_CHECK_RECORD names a file, the package's directory is appended
    to it before the binary starts, so a caller can tell every package went
    through this check rather than trusting the -exec flag reached go test.

    The binary's exit status is passed on, or 1 when it was 0 and something
    was left; a binary ended by a signal ends this process with the same
    signal. Anywhere else this refuses rather than pass.
    """
    sandbox = None
    if sys.platform == "linux":
        libc = ctypes.CDLL(None, use_errno=True)
        if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != 0:
            code = ctypes.get_errno()
            raise OSError(code, "prctl(PR_SET_CHILD_SUBREAPER)")
    elif sys.platform == "darwin":
        sandbox = Sandbox(unwritable=apple_temp_root())
        command = ["/usr/bin/sandbox-exec", "-p", sandbox.profile] + command
    else:
        raise RuntimeError("the per-package check runs on Linux and macOS, "
                           f"and this is {sys.platform}")
    try:
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
            pid, status = os.waitpid(-1 if sandbox is None else child, 0)
            if pid == child:
                break
        package = os.getcwd()
        if sandbox is None:
            left = linux_leftovers(package)
        else:
            left = darwin_leftovers(package, sandbox)
    finally:
        if sandbox is not None:
            sandbox.remove()
    if left:
        print(f"leak-check: package {package} left processes running", flush=True)
    if os.WIFSIGNALED(status):
        signal.signal(os.WTERMSIG(status), signal.SIG_DFL)
        os.kill(os.getpid(), os.WTERMSIG(status))
    code = os.waitstatus_to_exitcode(status)
    return 1 if code == 0 and left else code


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
        per_package = args.mode == "run" and command[:1] == ["--per-package"]
        if per_package:
            command = command[1:]
        if command[:1] == ["--"]:
            command = command[1:]
        if not command:
            parser.error(f"{args.mode} requires a command")
        if args.mode == "package":
            return package_run(command)
        return guarded_run(command, per_package)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"leak-check: inspection failed: {error}", file=sys.stderr, flush=True)
        return 2


if __name__ == "__main__":
    sys.exit(main())
