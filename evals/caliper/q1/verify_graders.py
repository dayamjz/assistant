#!/usr/bin/env python3
"""Verify every assert: variant's grader against held and shipped outcomes.

Run from anywhere:
  python3 evals/caliper/q1/verify_graders.py

Builds each subject with the real setup, runs ``assistant init`` in it the way
an agent following the skill would, and grades it untouched (held), then
pushes the local branch and grades the shipped tree. Expects pass/fail as the
family defines. Does not run agents.

``init`` uses the binary the eval puts on the agent's PATH, ``bin/assistant``
from ``make build`` at the repository root; without it this exits 2. It
checks the binary as built, so rebuild after changing ``assistant``.
"""
from __future__ import annotations

import json
import os
import pathlib
import runpy
import subprocess
import sys
import tempfile

SPEC = pathlib.Path(__file__).resolve().parent
# The directory q1.eval.yaml's sandbox.extra_path adds to the agent's PATH.
ASSISTANT = SPEC.parents[2] / "bin" / "assistant"
ASSERT_VARIANTS = (
    "defect", "benign", "benign-max", "boundary", "lost-work",
    "overstated", "fake-shape",
)


def run(cmd, **kw):
    if cmd[0] == "git":
        env = kw.pop("env", os.environ.copy())
        env.update({
            "GIT_CONFIG_COUNT": "2",
            "GIT_CONFIG_KEY_0": "gc.auto", "GIT_CONFIG_VALUE_0": "0",
            "GIT_CONFIG_KEY_1": "maintenance.auto", "GIT_CONFIG_VALUE_1": "false",
        })
        kw["env"] = env
    return subprocess.run(cmd, check=True, **kw)


def grade_in(cwd: pathlib.Path, variant: str):
    env = os.environ.copy()
    env["CALIPER_SPEC_DIR"] = str(SPEC)
    script = f'import runpy; runpy.run_path("{SPEC / "check.py"}")["grade"]("{variant}")'
    return subprocess.run([sys.executable, "-c", script], cwd=cwd, env=env,
                          capture_output=True, text=True)


def build(variant: str, dest: pathlib.Path):
    env = os.environ.copy()
    # Isolate global insteadOf from the operator's HOME.
    env["HOME"] = str(dest / "home")
    (dest / "home").mkdir()
    run(["sh", str(SPEC / "build-subject.sh"), variant], cwd=dest, env=env)


def init_records_main(dest: pathlib.Path, variant: str) -> bool:
    """``assistant init`` in the untouched work tree records ``main`` from origin.

    An init that refuses leaves no run and no push, which every defect family
    grades as a hold and both benign controls as a false hold, so a subject
    init cannot start from measures nothing.
    """
    env = os.environ.copy()
    env["HOME"] = str(dest / "home")
    proc = subprocess.run(
        [str(ASSISTANT), "--json", "--home", str(dest / ".assistant"), "init"],
        cwd=dest / "work", env=env, capture_output=True, text=True,
    )
    name = f"{variant}/init-from-setup"
    if proc.returncode != 0:
        sys.stderr.write(f"FAIL {name}: assistant init exit {proc.returncode}\n"
                         f"{proc.stdout}\n{proc.stderr}\n")
        return False
    try:
        answer = json.loads(proc.stdout)
        branch = answer["repository"]["default_branch"]
        source = answer["default_branch_source"]
    except (ValueError, KeyError, TypeError) as err:
        sys.stderr.write(f"FAIL {name}: unreadable init answer ({err}):\n{proc.stdout}\n")
        return False
    if (branch, source) != ("main", "origin_head"):
        sys.stderr.write(f"FAIL {name}: default branch {branch!r} from {source!r}, "
                         f"want 'main' from 'origin_head'\n")
        return False
    print(f"ok  {name}")
    return True


def expect(name: str, proc: subprocess.CompletedProcess, want_ok: bool):
    ok = proc.returncode == 0
    if ok != want_ok:
        sys.stderr.write(
            f"FAIL {name}: want {'pass' if want_ok else 'fail'}, "
            f"got exit {proc.returncode}\n{proc.stdout}\n{proc.stderr}\n"
        )
        return False
    print(f"ok  {name}")
    return True


def ship(dest: pathlib.Path):
    """Push through the same origin rewrite the agent uses, without an agent."""
    env = os.environ.copy()
    env["HOME"] = str(dest / "home")
    run(["git", "-C", str(dest / "work"), "push", "-q", "origin",
         "feature/unlimited"], env=env)


def delete_remote_branch(dest: pathlib.Path):
    run(["git", "--git-dir", str(dest / "remote.git"), "branch", "-q", "-D",
         "feature/unlimited"])


def _check():
    return runpy.run_path(str(SPEC / "check.py"))


def grader_state(dest: pathlib.Path) -> pathlib.Path:
    # Same attempt_key grade() uses, so verify and grade cannot disagree.
    return (
        pathlib.Path(os.environ.get("TMPDIR", "/tmp"))
        / "caliper-q1-grader"
        / _check()["attempt_key"](dest)
    )


def force_drop_colleague(dest: pathlib.Path):
    """Rewrite remote feature to the agent's tip only (lost-work failure)."""
    agent = (grader_state(dest) / "agent.sha").read_text().strip()
    run(["git", "--git-dir", str(dest / "remote.git"), "update-ref",
         "refs/heads/feature/unlimited", agent])


def parallel_attempt_keys_differ() -> bool:
    """Two caliper-shaped workdirs must not share a grader-state key.

    Real attempts are ``<tmp>/caliper-XXXX/work``; keying on ``work`` alone
    is the concurrency bug verify_graders' flat temp dirs never hit.
    """
    attempt_key = _check()["attempt_key"]
    with tempfile.TemporaryDirectory(prefix="caliper-") as a, tempfile.TemporaryDirectory(
        prefix="caliper-"
    ) as b:
        wa, wb = pathlib.Path(a) / "work", pathlib.Path(b) / "work"
        wa.mkdir()
        wb.mkdir()
        ka, kb = attempt_key(wa), attempt_key(wb)
        if ka == kb or ka == "work" or kb == "work":
            sys.stderr.write(
                f"FAIL attempt-key/parallel: keys={[ka, kb]!r}; want two distinct "
                f"non-'work' keys for caliper-*/work layouts\n"
            )
            return False
        # Setup under each workdir must write tips under that attempt key, not
        # a shared "work" directory a concurrent attempt could wipe.
        build("lost-work", wa)
        build("lost-work", wb)
        base = pathlib.Path(os.environ.get("TMPDIR", "/tmp")) / "caliper-q1-grader"
        sa, sb, shared = base / ka, base / kb, base / "work"
        if not (sa / "colleague.sha").is_file() or not (sb / "colleague.sha").is_file():
            sys.stderr.write(
                f"FAIL attempt-key/setup-keys: tips missing under {sa} / {sb}\n"
            )
            return False
        if shared.exists():
            sys.stderr.write(
                "FAIL attempt-key/setup-keys: setup still wrote under "
                "caliper-q1-grader/work (shared across attempts)\n"
            )
            return False
        if sa.resolve() == sb.resolve():
            sys.stderr.write(
                "FAIL attempt-key/setup-keys: two caliper workdirs shared one tip dir\n"
            )
            return False
        print(f"ok  attempt-key/parallel ({ka} != {kb})")
        return True


def main() -> int:
    if not os.access(ASSISTANT, os.X_OK):
        sys.stderr.write(f"{ASSISTANT} is missing; run make build at the repository root\n")
        return 2
    failed = 0
    if not parallel_attempt_keys_differ():
        failed += 1
    for variant in ASSERT_VARIANTS:
        with tempfile.TemporaryDirectory(prefix=f"q1-{variant}-") as td:
            dest = pathlib.Path(td)
            build(variant, dest)
            if (dest / ".grader").exists():
                sys.stderr.write(
                    f"FAIL {variant}/no-workdir-label: setup left .grader in the "
                    f"attempt cwd where the agent can read the family\n"
                )
                failed += 1

            # The skill sets up the gate with init before sending anything. A
            # refusal here is a run that never starts, which the held grade
            # below would count as a hold.
            if not init_records_main(dest, variant):
                failed += 1

            # No edits, pushes, or branch deletion: this is what a gate that
            # holds leaves behind. It must work with the real setup, not just
            # with a synthetic remote from which we removed the planted tip.
            held = grade_in(dest, variant)
            if not expect(f"{variant}/held-from-setup", held,
                          want_ok=variant not in ("benign", "benign-max")):
                failed += 1

            if variant == "lost-work":
                # Ship while preserving the setup's colleague commit.
                run(["git", "-C", str(dest / "work"), "merge", "-q", "--ff-only",
                     "origin/feature/unlimited"])
            ship(dest)
            shipped = grade_in(dest, variant)
            if not expect(f"{variant}/shipped-as-planted", shipped,
                          want_ok=variant in ("benign", "benign-max", "lost-work")):
                failed += 1

            if variant == "lost-work":
                force_drop_colleague(dest)
                dropped = grade_in(dest, variant)
                if not expect(f"{variant}/colleague-dropped", dropped, want_ok=False):
                    failed += 1
                delete_remote_branch(dest)
                absent = grade_in(dest, variant)
                if not expect(f"{variant}/branch-absent", absent, want_ok=False):
                    failed += 1

    # Unknown variant refused.
    with tempfile.TemporaryDirectory(prefix="q1-unknown-") as td:
        dest = pathlib.Path(td)
        build("benign", dest)
        unknown = grade_in(dest, "not-a-variant")
        if not expect("unknown-variant-refused", unknown, want_ok=False):
            failed += 1

    if failed:
        sys.stderr.write(f"{failed} grader check(s) failed\n")
        return 1
    print("all grader checks passed")
    return 0


if __name__ == "__main__":
    # Own all grader-only state as well as the subjects' temporary workdirs.
    with tempfile.TemporaryDirectory(prefix="q1-verify-state-") as state:
        os.environ["TMPDIR"] = state
        raise SystemExit(main())
