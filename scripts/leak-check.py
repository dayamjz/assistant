#!/usr/bin/env python3
"""Compare runner-user processes and temp entries around a command, and fail
a Go package for every process it leaves running.

Run locally: python3 scripts/leak-check.py run -- go test -race -p 2 ./...
Per package as well:
  python3 scripts/leak-check.py run -- \
    go test -exec "python3 $PWD/scripts/leak-check.py package --" ./...
Prove both reject planted leftovers: python3 scripts/leak-check.py self-test
Only Linux and macOS are supported. Inspection errors fail closed. On macOS
27, where Apple's agents keep folders in the per-user temp dir that this user
cannot list, run passes only as CI runs it, with a TMPDIR of its own and
--per-package, which reads nothing under them (guarded_run).

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
entries are reported rather than set aside, and run reads what is under
each, so one this user cannot list fails the run (temp_entries).
"""

import argparse
import ctypes
import errno
import os
from pathlib import Path
import secrets
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


MAX_SYMLINKS = 32  # links only_root_changes follows before refusing, as MAXSYMLINKS


def only_root_changes(path):
    """Whether no user but root can have put path where it is, or changed it.

    path must be absolute and is resolved one entry at a time from /, never
    in one step, since a link this user can replace makes the entry it names
    theirs whatever it points at. Each entry on the way must be
    irreplaceable: its directory unchangeable, or the entry flagged so that
    it cannot be renamed or removed. A symbolic link is followed only once it
    has passed that, and what it names is then held to the same. The entry
    reached last must also be unchangeable itself. Unchangeable is on a
    read-only volume, or flagged immutable or restricted, or owned by root and
    writable by neither its group nor others. Mode bits alone do not decide
    it: System Integrity Protection refuses this user writes in
    /System/Volumes/Preboot/Cryptexes, which is mode 777, and the entry
    below it there is flagged against removal.
    """
    path = str(path)
    if not os.path.isabs(path):
        return False

    def unchangeable(node, info):
        try:
            readonly = os.statvfs(node).f_flag & os.ST_RDONLY
        except OSError:
            return False
        return bool(readonly
                    or getattr(info, "st_flags", 0) & (SF_IMMUTABLE | SF_RESTRICTED)
                    or (info.st_uid == 0 and not info.st_mode & 0o022))

    current = Path("/")
    try:
        current_info = os.lstat(current)
    except OSError:
        return False
    remaining = [part for part in path.split("/") if part]
    followed = 0
    while remaining:
        name = remaining.pop(0)
        if name == ".":
            continue
        if name == "..":
            current = current.parent
            try:
                current_info = os.lstat(current)
            except OSError:
                return False
            continue
        node = current / name
        try:
            info = os.lstat(node)
        except OSError:
            return False
        flags = getattr(info, "st_flags", 0)
        if not (unchangeable(current, current_info)
                or flags & (SF_IMMUTABLE | SF_RESTRICTED | SF_NOUNLINK)):
            return False
        if stat.S_ISLNK(info.st_mode):
            followed += 1
            if followed > MAX_SYMLINKS:
                return False
            try:
                target = os.readlink(node)
            except OSError:
                return False
            if os.path.isabs(target):
                current, current_info = Path("/"), os.lstat("/")
            remaining = [part for part in target.split("/") if part] + remaining
            continue
        current, current_info = node, info
    return unchangeable(current, current_info)


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
    its own records that plist, which stays the test's if it is then swapped
    for a link to one of Apple's, since the path is resolved one entry at a
    time and the link is an entry the test can replace. A test that asks launchd to start one of
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
# (guarded_run). So is an entry there flagged SF_NOUNLINK, whatever its name
# (apple_temp_entry). Any other new name there is still reported.
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
    the names Apple's tools use there (APPLE_TEMP_NAMES, APPLE_TEMP_PREFIX),
    or one flagged SF_NOUNLINK, which only the superuser can set (the guard's
    tests check that this user cannot). On macOS 27 Apple's agents keep a
    folder flagged so there, some without an Apple name (gamed, heard), with
    a TemporaryItems inside that this user cannot list. It reads the name and
    the flags of that entry, not their contents; an entry that is gone by
    then is not Apple's."""
    if root is None:
        return False
    try:
        top = Path(path).relative_to(root).parts[0]
    except (ValueError, IndexError):
        return False
    if top in APPLE_TEMP_NAMES or top.startswith(APPLE_TEMP_PREFIX):
        return True
    try:
        return bool(getattr(os.lstat(Path(root) / top), "st_flags", 0) & SF_NOUNLINK)
    except FileNotFoundError:
        return False


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


def temp_entries(roots, unread=None):
    """Walk temp directories without following symlinks, including nested sockets.

    unread, given a path, says whether the walk records that entry but reads
    nothing under it: guarded_run passes the Apple entries it sets aside
    (apple_temp_entry), since everything under one is set aside with it.
    Everything else is read. A directory under a root that this user cannot
    list is an error when this user owns it or can enter it, whatever
    refused the listing: an Apple folder seen in the per-user temp dir on
    macOS 27 refused this user listing it and still let this user create
    entries in it, so a leftover inside would go unseen.
    """
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
        except PermissionError as error:
            # A foreign private directory this user cannot enter cannot
            # receive this user's leaks. Execute-only trees can conceal a
            # writable descendant, so those and owned trees fail closed.
            if (required or path.stat().st_uid == os.geteuid() or
                    os.access(path, os.X_OK)):
                raise PermissionError(
                    error.errno, f"{error.strerror}; this user cannot list it, "
                    "so nothing under it can be checked", str(path)) from error
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
            if kind == "directory" and not (unread and unread(child)):
                visit(child)

    for root in roots:
        visit(root, required=True)
    return found


def snapshot(roots, unread=None):
    return processes(), temp_entries(roots, unread)


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
    test reported ok went through package. Nothing under an entry set aside
    is read (temp_entries), so one this user cannot list does not stop the
    run. Without per_package, every new entry there is reported and
    everything under each is read, so one that cannot be listed fails the
    run. What the sandbox does not cover is an entry made
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

    def unread(path):
        return apple_temp_entry(path, apple_root)

    before = snapshot(roots, unread)
    result = subprocess.run(command, check=False)
    after = snapshot(roots, unread)
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
    """The guard's positive control: the real CLI, run as CI runs it (run
    --per-package, with a TMPDIR of its own), must reject and name both leaks.
    Run that way it reads nothing under the Apple entries of the per-user
    temp dir (guarded_run), as CI's own run does not, so a folder there this
    user cannot list does not fail the control when it would not fail CI."""
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
                [sys.executable, str(Path(__file__).resolve()), "run", "--per-package", "--",
                 sys.executable, "-c", code, str(pid_file), str(leaked)],
                env=dict(os.environ, TMPDIR=str(root)), text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
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
E_PPID_OFFSET = 560  # kinfo_proc.kp_eproc.e_ppid, 64-bit; the guard's tests check it on known parents
LAUNCHD = 1  # the pid an orphan is reparented to on macOS
SZOMB = 5  # extern_proc.p_stat of a process that has exited, from sys/proc.h
P_WEXIT = 0x00002000  # extern_proc.p_flag of a process working on exiting, from sys/proc.h
SANDBOX_FILTER_GLOBAL_NAME = 2  # sandbox_check's filter for a Mach service name; the guard's tests fail if wrong
SANDBOX_CHECK_NO_REPORT = 0x40000000  # sandbox_check: do not log a denial
SETTLE_READINGS = 64

# A process's state in a kinfo_proc record (darwin_records).
RUNNING, EXITING, EXITED = "running", "exiting", "exited"


def darwin_records(mib):
    """The kinfo_proc records sysctl returns for mib, as (pid, start, state,
    parent): parent is the record's parent pid (eproc.e_ppid), and start is
    the kernel's start time in the form process_start uses; state is
    EXITED for a zombie (p_stat SZOMB), EXITING for a process that is not one
    yet but whose p_flag has P_WEXIT, the flag ps shows as E, and RUNNING
    otherwise. A process held in its exit reads EXITING, not yet a zombie,
    for as long as it is held, and sandbox_check answers for it as for a pid
    that is gone (the guard's tests hold one there)."""
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
        # extern_proc: p_starttime at 0, p_flag at 32, p_stat at 36, p_pid at
        # 40; eproc, which follows it, puts e_ppid at 560 of the record.
        sec, usec = struct.unpack_from("<qi", buffer.raw, offset)
        flag, = struct.unpack_from("<i", buffer.raw, offset + 32)
        pid, = struct.unpack_from("<i", buffer.raw, offset + 40)
        parent, = struct.unpack_from("<i", buffer.raw, offset + E_PPID_OFFSET)
        if buffer.raw[offset + 36] == SZOMB:
            state = EXITED
        elif flag & P_WEXIT:
            state = EXITING
        else:
            state = RUNNING
        records.append((pid, f"{sec}.{usec:06d}", state, parent))
    return records


def darwin_table():
    """Map (pid, start) to (state, parent pid) (darwin_records), for every
    process in the kernel's table (sysctl kern.proc.all), whatever its user."""
    return {(pid, start): (state, parent)
            for pid, start, state, parent in darwin_records((1, 14, 0))}


def darwin_process(pid):
    """(start, state) for the process pid now is (darwin_records), or None
    once it is gone (sysctl kern.proc.pid)."""
    records = darwin_records((1, 14, 1, pid))
    return (records[0][1], records[0][2]) if records else None


class Sandbox:
    """A sandbox profile that marks a package's processes, and the reads that
    tell them apart.

    The mark is two Mach service names, inside and outside, each
    "leak-check." and 128 random bits in hex, new for each package: the
    profile allows everything except looking up inside, and the writes
    below, so a process in it is denied inside and allowed outside. A
    process in no sandbox is allowed both, and one in a sandbox that denies
    the lookups it was not granted is denied both. Reading as marked takes
    a rule that tells these two names apart, or a grant of outside alone:
    inside is named only in this profile, and outside is in no profile and
    leaves this process in no argument, so no other process can name it in
    a rule or be issued a grant for it, and a rule written without either
    name tells two random names apart only by chance. The mark was once a
    pair of files in TMPDIR, and Spotlight's mdworker_shared, whose sandbox
    at times allowed reading one of them and not the other, read as marked
    and was failed and killed as a package's process (#551). The check makes
    no files now: the guard's tests fail if Sandbox makes any under TMPDIR,
    and check that a process in another sandbox granted a read of one file
    is not marked. The profile has no rule a sandbox extension lifts, so a
    process in it that consumes a grant to inside is still marked.
    A process started under the profile is in it after a new session, its
    parent's exit and exec, and sandbox-exec run inside it fails before
    starting its command, so a process the package starts cannot leave the
    mark that way; the guard's tests check each of these on the machine they
    run on.

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
    sandbox_check reads the mark of any pid; for a pid that is gone, and for
    a process that has begun exiting (darwin_records), it answers denied for
    both names, which is not the mark.
    """

    def __init__(self, unwritable=None):
        self.inside = f"leak-check.{secrets.token_hex(16)}"
        self.outside = f"leak-check.{secrets.token_hex(16)}"
        if unwritable is not None and ('"' in str(unwritable) or "\\" in str(unwritable)):
            raise RuntimeError(f"cannot quote {unwritable} in a sandbox profile")
        self.profile = ("(version 1)(allow default)"
                        f'(deny mach-lookup (global-name "{self.inside}"))')
        if unwritable is not None:
            self.profile += f'(deny file-write* (subpath "{unwritable}"))'
        self.check = ctypes.CDLL("/usr/lib/libSystem.dylib", use_errno=True).sandbox_check
        self.check.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int]
        self.check.restype = ctypes.c_int

    def denies(self, pid, name):
        return self.check(pid, b"mach-lookup",
                          SANDBOX_FILTER_GLOBAL_NAME | SANDBOX_CHECK_NO_REPORT, name.encode())

    def marks(self, pid):
        """True when pid is in this sandbox, False when it is not or is gone,
        None when sandbox_check failed."""
        inside, outside = self.denies(pid, self.inside), self.denies(pid, self.outside)
        if inside < 0 or outside < 0:
            return None
        return inside == 1 and outside == 0


def start_order(key):
    """Sort key for a (pid, start): the start time as numbers."""
    return tuple(map(int, key[1].split(".")))


def started_outside(table, pending, outside):
    """Add to outside each process of pending (a key to (description,
    unmarked)) that read as unmarked and whose parent in table settled
    outside the sandbox, started no later than it, and is not launchd
    (darwin_marked), until no more can be added: a parent added here counts
    for its own children."""
    current = {key[0]: key for key in table}
    added = True
    while added:
        added = False
        for key, (_, unmarked) in pending.items():
            if key in outside or not unmarked:
                continue
            parent = current.get(table[key][1])
            if (parent is not None and parent[0] != LAUNCHD and parent in outside
                    and start_order(parent) <= start_order(key)):
                outside.add(key)
                added = True


def darwin_marked(sandbox):
    """Every (pid, start) a settled reading finds running in sandbox.

    A reading lists the kernel's table and, for each process in it that has
    not exited and that it has not settled, reads the mark and then the
    process again. A process still the same and running, not exiting, at that
    second read was running when its mark was read, so the mark settles it
    either way; a marked one is found even if it has exited by then. One that
    is exiting, has exited or is gone by the second read and is not marked
    may have been read once its exit had begun, when it reads as unmarked
    whatever its sandbox (Sandbox), and may have started a process before
    then that the listing could not hold, so the table is listed again, and
    only a listing in which every process settled ends the reading. Such a
    process stays unsettled in each listing that finds it exiting, so one
    held in its exit through SETTLE_READINGS listings makes this an error
    rather than a reading without what it started. A process that exits
    before its mark is read, and started nothing, is not found.

    Except: an unmarked process that could not be settled so is settled
    outside the sandbox when its parent in the same listing (its record's
    e_ppid) settled outside it, started no later than it did, and is not
    launchd. A process is in the sandbox only if the process that started it
    was, since the sandbox is inherited and cannot be left (Sandbox). The
    rule takes a recorded parent other than launchd, which adopts orphans,
    to be the process that started this one; a record naming some other
    process, as when a debugger outside the sandbox traces one of the
    package's processes as it exits, would settle that process outside, and
    that is the rule's gap. It matters because any process of any user that
    starts and exits while a listing is read would otherwise force another
    listing, and steady churn on a busy machine then never let one settle
    (#553: 66 of 100 runs of a clean package failed so beside three loops
    of /usr/bin/true). Settled this way, a process's own children follow by
    the same rule from it, in the same listing.

    SETTLE_READINGS listings without one that settles are an error naming
    what the last left unsettled.
    """
    found, settled, outside = set(), set(), set()
    for _ in range(SETTLE_READINGS):
        pending = {}
        # Newest first: a process the package left is among the last started.
        table = darwin_table()
        for key in sorted(table, key=start_order, reverse=True):
            if table[key][0] == EXITED or key in settled:
                continue
            mark = sandbox.marks(key[0])
            reading = darwin_process(key[0])
            running = reading == (key[1], RUNNING)
            if mark:
                found.add(key)
            if mark or (running and mark is not None):
                settled.add(key)
                if not mark:
                    outside.add(key)
            if not running or mark is None:
                state = "gone" if reading is None or reading[0] != key[1] else reading[1]
                pending[key] = (f"pid={key[0]} start={key[1]} parent={table[key][1]} {state}"
                                + (", its mark unread" if mark is None else ""), mark is False)
        started_outside(table, pending, outside)
        unsettled = [said for key, (said, _) in pending.items() if key not in outside]
        if not unsettled:
            return found
    raise RuntimeError(f"no reading of the process table settled in {SETTLE_READINGS} "
                       f"listings; the last left unsettled: {'; '.join(unsettled)}")


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
            if darwin_process(pid) in ((start, RUNNING), (start, EXITING)):
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
