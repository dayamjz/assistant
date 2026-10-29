#!/usr/bin/env python3
"""What the repeat gate runs for a pull request's change.

Run: python3 scripts/repeat-plan.py --base BASE --head HEAD [--github-output FILE]
(in the checkout), or with changed paths on standard input instead.

It reads the paths a pull request changes since its merge base (changed_paths)
and prints the plan as JSON:
the Go packages to run with -race -count=N on both OSes, whether to repeat
the leak check's own tests, the per-OS matrix the workflow runs (including
internal/service test-name shards), and why. With --github-output it also
writes the plan as step outputs (run, packages, leak_check_tests, matrix).

internal/service alone has outgrown one 55-minute go-test budget on a slow
macOS runner (#726). When the plan includes it, the matrix splits that
package across SERVICE_SHARDS jobs per OS. Each job lists tests on that OS
with `go test -race -list` (--shard-run) so Darwin-only and Linux-only
names are not filtered out by the other platform's discovery (#727).
Other covered packages stay one job per OS. Coverage is the union of the
shards on that OS; each still runs with -race, REPEAT_COUNT, and the
per-package leak check.

`go test -list` still runs TestMain; the list-only skip of the leftover
check lives on standin.RunOwned in internal/agents/standin/owner_unix.go
(P14, #727).

RISKY is the one list of what the gate covers, each package with the
issues that put it there: every package here produced a regression on main
or a flake in the week before the gate existed, almost always in that
package's own tests. A change to a package outside it, or to a file that is
not Go source or test data, runs nothing. A change to the gate itself runs
every package here, so the gate's own pull request shows it working.

Also: python3 scripts/repeat-plan.py --shard-run PACKAGE SHARD SHARDS
[--discovery-timeout SECONDS] prints the -run regexp for one shard from a
list taken on this machine (run_discovery).
"""

import argparse
import json
import os
import re
import signal
import subprocess
import sys
import time

RISKY = {
    "internal/agents/standin": "#549 #558 #561 failed in its own tests after a change to it; #569 #459",
    "internal/agents": "#469 #515 #539",
    "internal/procgroup": "process starts; #547 changed its fork lock to fix #539",
    "internal/procgroup/guard": "the starter guard beside internal/procgroup; #547",
    "internal/service": "#552 (from #540) and #562 (from #557) failed in its own tests; #563 #548 #451",
}
LEAK_CHECK = {
    "scripts/leak-check.py": "#553 #551",
    "scripts/leak-check-test.py": "#553",
}
GATE = {".github/workflows/repeat.yml", "scripts/repeat-plan.py"}

# How many parallel jobs split ./internal/service on each OS. Chosen so a
# slow macOS count (~2500s for the whole package) stays well under the
# 55m go-test budget per job; see #726.
SERVICE = "./internal/service"
SERVICE_SHARDS = 3
OSES = ("ubuntu-latest", "macos-latest")

# Same build mode as scripts/ci-test.sh / the shard jobs: without -race,
# //go:build race tests are absent from every shard and the union no longer
# covers the race suite (#727 review).
LIST_FLAGS = ("-race",)


def package_of(path):
    """The package directory a changed file belongs to: its directory, or
    the directory above testdata for a file under it."""
    parts = path.split("/")
    if "testdata" in parts[:-1]:
        return "/".join(parts[:parts.index("testdata")])
    return os.path.dirname(path)


def changed_paths(base, head):
    """Every path the change from BASE's merge base to HEAD touches. Renames
    are not detected, so a file moved out of a package names that package
    too: with them, git names only where a moved file went."""
    out = subprocess.run(["git", "diff", "--name-only", "--no-renames", "-z", f"{base}...{head}"],
                         capture_output=True, text=True, check=True, timeout=120).stdout
    return [path for path in out.split("\0") if path]


def partition_tests(tests, shards):
    """Split sorted test names across shards by round-robin so neighboring
    names (and serial clusters) do not land in one job."""
    if shards < 1:
        raise ValueError(f"shards must be at least 1, not {shards}")
    ordered = sorted(tests)
    return [[name for i, name in enumerate(ordered) if i % shards == shard]
            for shard in range(shards)]


def run_pattern(tests):
    """A go test -run regexp that selects exactly these top-level tests
    (and their subtests: one slash-free element matches the root only)."""
    if not tests:
        raise ValueError("run_pattern needs at least one test")
    return "^(%s)$" % "|".join(re.escape(name) for name in tests)


# How long discovery's `go test -list` may run before its process group is
# ended, and how long that group gets after SIGTERM before SIGKILL and after
# SIGKILL to be gone. A bound around discovery exceeds discovery_bound.
DISCOVERY_TIMEOUT = 300.0
DISCOVERY_KILL_GRACE = 2.0


def discovery_bound(timeout=DISCOVERY_TIMEOUT):
    """The longest run_discovery waits with this timeout: the timeout, then
    end_process_group's two graces. Polling and the interpreter's own time
    are not in it, so a bound around discovery exceeds this by a margin of
    its own; one that does not can kill the planner first and leave go's
    group running with nothing to end it (#727)."""
    return timeout + 2 * DISCOVERY_KILL_GRACE


def _group_present(pgid):
    """Whether any process is in group pgid. Signal 0 signals nothing; kill(2)
    answers ESRCH when no process is in the group and EPERM when one is there
    that this user may not signal."""
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def end_process_group(proc, grace=DISCOVERY_KILL_GRACE):
    """End the process group proc leads, started with start_new_session, and
    say how far that got.

    Both signals go to the group only while proc is unreaped (its returncode
    unset), when its pid names that group and no other (internal/procgroup's
    package comment says why): SIGTERM, then SIGKILL after grace whether or
    not anything is left. Then proc is reaped and the group polled with
    signal 0 until nothing answers, within one more grace. A process that
    left the group, by setsid or setpgid of its own, is not in it: no signal
    here reaches it, and the answer does not count it.
    """
    pgid = proc.pid
    if proc.returncode is not None:
        return "not signalled; its leader was already reaped, so its id may name another group"
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            os.killpg(pgid, sig)
        except (ProcessLookupError, PermissionError):
            pass  # What is left is read below.
        if sig == signal.SIGTERM:
            time.sleep(grace)
    sent = f"SIGTERM, then SIGKILL {grace:g}s later"
    deadline = time.monotonic() + grace
    try:
        proc.wait(timeout=grace)
    except subprocess.TimeoutExpired:
        return f"{sent}; its leader had not exited {grace:g}s after SIGKILL"
    while _group_present(pgid):
        if time.monotonic() >= deadline:
            return f"{sent}; a process was still in it {grace:g}s after SIGKILL"
        time.sleep(0.05)
    return f"{sent}; nothing in it is left"


def run_discovery(cmd, timeout=DISCOVERY_TIMEOUT, cwd=None, env=None):
    """Run cmd (go test -list) as the leader of a new session and process
    group, and answer as subprocess.run does.

    subprocess.run's timeout ends only `go`, and the compiler, linker or test
    binary it started runs on (#727). Here a timeout ends go's process group
    (end_process_group) before raising, and the error says how far that got.
    That is all it ends: a descendant that started a session or group of its
    own (setsid, setpgid) is outside the group and runs on. Nothing in this
    repository ends or reports it: the workflows run discovery in a step
    before scripts/ci-test.sh's, outside its Linux PID namespace, and its
    leak check reports only what is new once its own run has begun.
    discovery_bound(timeout) is the longest this waits.
    """
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        cwd=cwd,
        env=env,
        start_new_session=True,
    )
    try:
        stdout, stderr = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        ended = end_process_group(proc)
        proc.stdout.close()
        proc.stderr.close()
        raise RuntimeError(
            f"discovery timed out after {timeout:g}s; process group {proc.pid}: {ended}. "
            f"A descendant that started a session or group of its own (setsid, setpgid) "
            f"is outside that group and is not ended: {' '.join(cmd)}"
        ) from None
    return subprocess.CompletedProcess(cmd, proc.returncode, stdout, stderr)


def list_tests(package, runner=None, cwd=None, timeout=DISCOVERY_TIMEOUT, env=None):
    """Top-level Test names in package from `go test -race -list` on this OS.

    Real runs use run_discovery, so a timeout ends go's process group and
    what stayed in it, not only `go` (P15, #727); run_discovery says what
    that leaves. runner is an optional test double that returns a
    CompletedProcess-like object; cwd and env are the listing's directory
    and environment. On failure the error always includes Go's exit code and
    stderr (or stdout if stderr is empty), so a CI log names the cause
    rather than only CalledProcessError (#727).
    """
    cmd = ["go", "test", *LIST_FLAGS, "-list", "^Test", package]
    if runner is not None:
        proc = runner(cmd, capture_output=True, text=True, timeout=timeout, cwd=cwd, env=env)
    else:
        proc = run_discovery(cmd, timeout=timeout, cwd=cwd, env=env)
    if proc.returncode != 0:
        detail = (proc.stderr or "").strip() or (proc.stdout or "").strip() or "(no output)"
        raise RuntimeError(
            f"{' '.join(cmd)} exited {proc.returncode}: {detail}"
        )
    names = [line for line in proc.stdout.splitlines() if line.startswith("Test")]
    if not names:
        raise RuntimeError(f"{' '.join(cmd)} printed no Test names")
    return names


def shard_run_pattern(package, shard, shards, timeout=DISCOVERY_TIMEOUT):
    """The -run regexp for one partition of package's tests on this OS,
    listed within timeout (list_tests)."""
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard {shard} is out of range for {shards} shards")
    parts = partition_tests(list_tests(package, timeout=timeout), shards)
    chosen = parts[shard]
    if not chosen:
        raise ValueError(f"shard {shard}/{shards} is empty for {package}")
    return run_pattern(chosen)


def matrix(packages, leak):
    """The workflow's strategy.matrix.include: one entry per job.

    ./internal/service becomes SERVICE_SHARDS entries per OS with shard
    indexes; each run job lists tests on that OS (--shard-run) so a
    Darwin-only or Linux-only test is not dropped by the other platform's
    discovery. Every other planned package shares one entry per OS. The
    leak check's own tests run on the first entry of each OS that has work.
    """
    pkgs = packages.split() if packages else []
    others = [p for p in pkgs if p != SERVICE]
    has_service = SERVICE in pkgs
    include = []
    for os_name in OSES:
        leak_here = leak
        if has_service:
            for i in range(SERVICE_SHARDS):
                include.append({
                    "os": os_name,
                    "name": f"service-{i}",
                    "packages": SERVICE,
                    "shard": str(i),
                    "shards": str(SERVICE_SHARDS),
                    "leak_check_tests": "true" if leak_here else "false",
                })
                leak_here = False
        if others:
            include.append({
                "os": os_name,
                "name": "other",
                "packages": " ".join(others),
                "shard": "",
                "shards": "",
                "leak_check_tests": "true" if leak_here else "false",
            })
            leak_here = False
        if not pkgs and leak:
            include.append({
                "os": os_name,
                "name": "leak",
                "packages": "",
                "shard": "",
                "shards": "",
                "leak_check_tests": "true",
            })
    return {"include": include}


def plan(paths, with_shard_runs=False):
    packages, why, leak = set(), [], False
    for path in paths:
        path = path.strip()
        if not path:
            continue
        if path in GATE:
            packages.update(RISKY)
            leak = True
            why.append(f"{path}: the gate itself changed, so it runs every package it covers")
            continue
        if path in LEAK_CHECK:
            leak = True
            why.append(f"{path}: the leak check ({LEAK_CHECK[path]})")
            continue
        package = package_of(path)
        in_testdata = package != os.path.dirname(path)
        if package in RISKY and (path.endswith(".go") or in_testdata):
            packages.add(package)
            why.append(f"{path}: {package} ({RISKY[package]})")
    ordered = sorted(packages)
    packages_str = " ".join(f"./{p}" for p in ordered)
    if with_shard_runs and SERVICE in packages_str.split():
        # Listing is per OS job (--shard-run), not here: the plan runs on
        # Ubuntu and cannot discover Darwin-only tests (#727).
        why.append(
            f"{SERVICE}: split across {SERVICE_SHARDS} test-name shards per OS "
            f"(each OS lists its own tests; #726 #727)"
        )
    return {
        "run": bool(ordered or leak),
        "packages": packages_str,
        "leak_check_tests": leak,
        "matrix": matrix(packages_str, leak),
        "why": why,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--github-output", metavar="FILE", help="also append the plan as step outputs here")
    parser.add_argument("--base", help="the pull request's base commit (with --head)")
    parser.add_argument("--head", help="the pull request's head commit (with --base)")
    parser.add_argument("--with-shard-runs", action="store_true",
                        help="record in why that service shards list per OS (matrix always shards)")
    parser.add_argument("--shard-run", nargs=3, metavar=("PACKAGE", "SHARD", "SHARDS"),
                        help="print the -run regexp for one test-name shard on this OS and exit")
    parser.add_argument("--discovery-timeout", type=float, default=DISCOVERY_TIMEOUT, metavar="SECONDS",
                        help="how long --shard-run's listing may run before its process group is ended")
    args = parser.parse_args(argv)
    if args.shard_run is not None:
        package, shard_s, shards_s = args.shard_run
        try:
            shard, shards = int(shard_s), int(shards_s)
        except ValueError:
            parser.error("--shard-run SHARD and SHARDS must be integers")
        if not 0 < args.discovery_timeout < float("inf"):
            parser.error("--discovery-timeout must be a positive number of seconds")
        try:
            print(shard_run_pattern(package, shard, shards, args.discovery_timeout), end="")
        except (ValueError, RuntimeError) as err:
            print(f"repeat-plan: {err}", file=sys.stderr)
            return 1
        return 0
    if bool(args.base) != bool(args.head):
        parser.error("--base and --head go together")
    paths = changed_paths(args.base, args.head) if args.base else sys.stdin
    result = plan(paths, with_shard_runs=args.with_shard_runs)
    print(json.dumps(result, indent=2))
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as out:
            out.write(f"run={str(result['run']).lower()}\n")
            out.write(f"packages={result['packages']}\n")
            out.write(f"leak_check_tests={str(result['leak_check_tests']).lower()}\n")
            out.write(f"matrix={json.dumps(result['matrix'], separators=(',', ':'))}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
