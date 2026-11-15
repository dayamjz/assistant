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
# One record per attempt (README "Gate record"), read from the nested checkout
# with --home. runs needs the live service, so the stop comes last. A failed
# call is in the record and on stderr and exits 1; Caliper keeps the grade.
exec python3 - "$attempt_root" "${TMPDIR:-/tmp}/caliper-q1-gate" <<'PY'
import datetime, json, os, subprocess, sys

root, gate = sys.argv[1], sys.argv[2]
attempt = os.path.basename(root)
home = os.path.join(root, ".assistant")
checkout = os.path.join(root, "work", "work")
record = {"schema": 1, "attempt": attempt, "home": home, "checkout": checkout,
          "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
          "errors": []}

def call(name, args, cwd, out=None):
    """Run assistant and record its exit status, stderr and output, never discarded."""
    step = {"argv": ["assistant", "--home", home] + args, "cwd": cwd}
    try:
        p = subprocess.run(step["argv"], cwd=cwd, stdin=subprocess.DEVNULL,
                           capture_output=True, text=True, timeout=120)
        step.update(exit=p.returncode, stderr=p.stderr)
        stdout = p.stdout
    except (OSError, subprocess.TimeoutExpired) as e:
        step.update(exit=None, stderr=str(e))
        stdout = ""
    if out is not None and step["exit"] == 0:
        with open(out, "w") as f:
            f.write(stdout)
        step["file"] = os.path.basename(out)
    else:
        try:
            step["output"] = json.loads(stdout)
        except ValueError:
            step["output"], step["stdout"] = None, stdout
    if step["exit"] != 0:
        record["errors"].append("%s: exit %s: %s" % (
            name, step["exit"], (step["stderr"] or stdout).strip()))
    record[name] = step
    return step

def capture():
    if not os.path.isdir(home):
        record["outcome"] = "no-home"
        return
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
        elif read["exit"] == 0:
            record["errors"].append("runs: exit 0, but the output is not a runs document")
    call("stop", ["service", "stop", "--force"], root)

try:
    capture()
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
