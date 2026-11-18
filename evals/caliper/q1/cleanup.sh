#!/bin/sh
# Shared cleanup for every Q1 task: drop every global insteadOf rewrite of the
# subject upstream URL (so a failed setup that never created work/ cannot leave
# a dead path for the next attempt), write the attempt's gate record from its
# assistant home, then stop that service.
set -eu
UPSTREAM_URL="https://github.com/dayamjz/caliper-q1.git"
# Clear by URL, not by this attempt's remote path: cleanup used to require
# ../remote.git, and a setup that failed after installing insteadOf then
# exited here with the rewrite still pointing at a deleted tree. Attempt 5 of
# the 2026-10-16 full with-skill arm failed that way on every task.
git config --global --get-regexp '^url\..*\.insteadof$' "$UPSTREAM_URL" 2>/dev/null |
	while read -r key _; do
		git config --global --unset-all "$key" 2>/dev/null || true
	done

# Caliper runs this hook with cwd=$CALIPER_WORKDIR (<attempt>/work). The agent
# starts the service under <attempt>/.assistant. build-subject also creates a
# nested ./work here, so testing for [ -d work ] and cd'ing into it both
# mis-keyed the gate dump and left assistant talking to the operator HOME
# (every 2026-10-16 full-run capture failed that way).
workdir="${CALIPER_WORKDIR:-.}"
attempt_root="$(CDPATH= cd -- "$workdir/.." && pwd)"
mkdir -p "${TMPDIR:-/tmp}/caliper-q1-gate"
# One record per attempt (README "Gate record"). Only runs reads from the nested
# checkout; it needs the live service, so the stop comes last, in a finally,
# which returns only once status reports the service down (wait_down).
exec python3 - "$attempt_root" "${TMPDIR:-/tmp}/caliper-q1-gate" <<'PY'
import datetime, hashlib, json, os, re, subprocess, sys, time
root, gate = sys.argv[1], sys.argv[2]
attempt = os.path.basename(root)
home = os.path.join(root, ".assistant")
checkout = os.path.join(root, "work", "work")
# The attempt's key in caliper's saved result (task_id, attempt), which only the
# patched caliper passes (README "As run"); without it the record says why.
task_id, number = os.environ.get("CALIPER_TASK_ID"), os.environ.get("CALIPER_ATTEMPT")
keyed = bool(task_id) and re.fullmatch(r"[1-9][0-9]*", number or "", re.ASCII) is not None
# The arm run-full.sh ran this attempt in (README "Arms"), which gate_by_arms.py checks.
record = {"schema": 6, "attempt": attempt, "home": home, "checkout": checkout, "errors": [],
          "arm": os.environ.get("Q1_ARM") or None,
          "task_id": task_id if keyed else None, "caliper_attempt": int(number) if keyed else None,
          "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
if not keyed:
    record["errors"].append("key: CALIPER_TASK_ID %r, CALIPER_ATTEMPT %r: no attempt key; this caliper "
                            "is not 0.19.0+q1attemptenv (install-caliper.sh)" % (task_id, number))
if record["arm"] is None:
    record["errors"].append("arm: Q1_ARM is unset; run-full.sh sets it for each arm")

# pipeline.Order's wire names, review first (README "Stage evidence");
# stage_test.go pins the list to the product's.
STAGES = ("review", "intent", "rebase", "test", "document", "lint", "push", "pr", "ci")

def call(name, args, cwd, out=None, into=None, key=None):
    """Run assistant and record its exit status, stderr and output, never
    discarded, as into[key] (record[name] by default)."""
    step = {"argv": ["assistant", "--home", home] + args, "cwd": cwd}
    try:
        p = subprocess.run(step["argv"], cwd=cwd, stdin=subprocess.DEVNULL,
                           capture_output=True, text=True, timeout=120)
        step["exit"], step["stderr"], stdout = p.returncode, p.stderr, p.stdout
    except (OSError, subprocess.TimeoutExpired) as e:
        step["exit"], step["stderr"], stdout = None, str(e), ""
    try:
        step["output"] = json.loads(stdout)
    except ValueError:
        step["output"], step["stdout"] = None, stdout
    (record if into is None else into)[key or name] = step  # stored before the file write below, which may fail
    if step["exit"] != 0:
        record["errors"].append("%s: exit %s: %s" % (
            name, step["exit"], (step["stderr"] or stdout).strip()))
    elif out is not None:
        try:
            with open(out, "w") as f:
                f.write(stdout)
            step["file"] = os.path.basename(out)
        except OSError as e:  # a side file: recorded, and the reads go on
            record["errors"].append("%s: writing %s: %r" % (name, out, e))
    return step

def read_stages(runs):
    """Per listed run, while the service is up: its report, its review log
    (all nine logs when the report shows no checkpoint), then the report again."""
    record["stages"] = {}
    for run in runs:
        rid = run.get("id") if isinstance(run, dict) else None
        if not isinstance(rid, str) or not rid:
            continue  # gate_record.py judges a runs read with such a run indeterminate
        entry = record["stages"][rid] = {"logs": {}}
        first = call("run:" + rid, ["--json", "runs", rid], root, into=entry, key="report")
        bare = first["exit"] == 0 and isinstance(first["output"], dict) and "stages" not in first["output"]
        for stage in STAGES if bare else STAGES[:1]:
            call("log:%s:%s" % (rid, stage), ["--json", "runs", rid, "--log=" + stage], root,
                 into=entry["logs"], key=stage)
        call("run-after:" + rid, ["--json", "runs", rid], root, into=entry, key="report_after")

def check_head(run):
    """The head check for one listed run (README "Head checks"): whether its submitted_head
    names a commit in the checkout, asked here because caliper removes the attempt root
    after this hook. Bound to the attempt key and the run, so gate_record.py can match it."""
    run = run if isinstance(run, dict) else {}
    head = run.get("submitted_head")
    entry = {"task_id": record["task_id"], "caliper_attempt": record["caliper_attempt"],
             "run_id": run.get("id"), "submitted_head": head, "is_commit": False}
    if isinstance(head, str) and re.fullmatch(r"[0-9a-f]{40}", head):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        env["GIT_CEILING_DIRECTORIES"] = os.path.dirname(checkout)  # never a repository above it (#927)
        try:
            entry["is_commit"] = subprocess.run(["git", "cat-file", "-e", head + "^{commit}"], cwd=checkout,
                                                env=env, stdin=subprocess.DEVNULL, capture_output=True,
                                                timeout=60).returncode == 0
        except (OSError, subprocess.TimeoutExpired):
            pass  # is_commit stays false, which gate_record.py reads as cannot tell
    return entry

def wait_down():
    """After the stop, until status reports the service down: the stop answers once
    accepted, while the service may still have its database open, and caliper removes
    the attempt root as soon as this hook returns. Any other answer waits, as
    run-full.sh's probe does, for at most 30s. The outcome is its own evidence,
    wait_down, beside the stop's exit; a service not reported down is a stop: error too."""
    deadline = time.monotonic() + 30
    while True:
        try:
            p = subprocess.run(["assistant", "--home", home, "--json", "service", "status"], cwd=root,
                               stdin=subprocess.DEVNULL, capture_output=True, text=True,
                               timeout=max(deadline - time.monotonic(), 1))
        except (OSError, subprocess.TimeoutExpired) as e:
            answer, doc = repr(e), None
        else:
            answer = (p.stdout + p.stderr).strip()
            try:
                doc = json.loads(p.stdout) if p.returncode == 0 else None
            except ValueError:
                doc = None
        down = isinstance(doc, dict) and doc.get("running") is False
        if down or time.monotonic() >= deadline:
            break
        time.sleep(0.2)
    record["wait_down"] = {"down": down, "last_status": answer}
    if not down:
        record["errors"].append("stop: the service at %s is not reported down 30s after its stop: %s"
                                % (home, answer))

def ci_checks():
    """The CI evidence as found (README "CI"), for gate_record.py to judge: each file
    build-subject.sh and ci-hook.sh wrote, as text, and the remote's hook and refs now."""
    found = record["ci_checks"] = {"files": None, "hook_sha256": None, "refs": None}
    evidence, remote = os.path.join(os.path.dirname(gate), "caliper-q1-ci", attempt), os.path.join(root, "work", "remote.git")
    try:
        found["files"] = {}
        for name in sorted(os.listdir(evidence)):
            with open(os.path.join(evidence, name)) as f:
                found["files"][name] = f.read()
        with open(os.path.join(remote, "hooks", "pre-receive"), "rb") as f:
            found["hook_sha256"] = hashlib.sha256(f.read()).hexdigest()
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        refs = subprocess.run(["git", "--git-dir", remote, "for-each-ref", "--format=%(refname) %(objectname)"],
                              env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60,
                              check=True).stdout.split()
        found["refs"] = dict(zip(refs[::2], refs[1::2]))
    except (OSError, ValueError, subprocess.SubprocessError) as e:
        record["errors"].append("ci: %r" % e)

def capture():
    if not os.path.isdir(home):
        record["outcome"] = "no-home"
        return
    try:  # an exception in a read still reaches the stop, then the record
        status = call("service", ["--json", "service", "status"], root)["output"]
        record["service_answered"] = isinstance(status, dict) and status.get("running") is True
        call("usage", ["--json", "runs", "--usage=1h"], root,
             os.path.join(gate, attempt + "-usage.json"))
        if not os.path.isdir(checkout):
            record["outcome"] = "no-checkout"
        else:
            read = call("runs", ["--json", "runs"], checkout)
            doc = read["output"]
            runs = doc.get("runs", False) if isinstance(doc, dict) else False
            ok = read["exit"] == 0 and (runs is None or isinstance(runs, list))
            record["outcome"] = "read" if ok else "error"
            if ok:
                record["repositories"] = sorted({r.get("repository_id") for r in runs or []
                                                 if isinstance(r, dict)}, key=str)
                record["head_checks"] = [check_head(run) for run in runs or []]
                read_stages(runs or [])
            elif read["exit"] == 0:
                record["errors"].append("runs: exit 0, but the output is not a runs document")
    finally:
        call("stop", ["service", "stop", "--force"], root)
        wait_down()  # even after a failed stop: the root must not go while the service is up

try:
    capture()  # first: its finally stops the service whatever the CI read does
    ci_checks()
except Exception as e:  # still a record: only a missing file means the harness failed
    record.setdefault("outcome", "error")
    record["errors"].append("cleanup: %r" % e)

path = os.path.join(gate, attempt + ".record.json")
with open(path + ".tmp", "w") as f:
    json.dump(record, f, indent=2, sort_keys=True)
os.replace(path + ".tmp", path)
for line in record["errors"]:
    sys.stderr.write("caliper-q1 cleanup: %s: %s\n" % (attempt, line))
sys.exit(1 if record["errors"] else 0)
PY
