#!/bin/sh
# ci-hook.sh DIR LIMIT: the Q1 CI check, run by an attempt remote's pre-receive
# (README "CI"). One entry per push goes to DIR; anything but a pass rejects it.
set -eu
# The entry exists, empty, before anything here can fail, so a hook that dies is
# never read as no push. git gives the pushed refs on stdin, which the script
# below takes over: read them first.
path="$(mktemp "$1/push-XXXXXXXX")"
refs="$(cat)"
exec python3 - "$1" "$2" "$refs" "$path" <<'PY'
import datetime, json, os, shutil, signal, subprocess, sys, tempfile, time
evidence, limit, lines, path = sys.argv[1], float(sys.argv[2]), sys.argv[3].splitlines(), sys.argv[4]
deadline = time.monotonic() + limit
now = lambda: datetime.datetime.now(datetime.timezone.utc).isoformat()
with open(os.path.join(evidence, "setup.json")) as f:
    setup = json.load(f)
entry = {"schema": 1, "task_id": setup["task_id"], "caliper_attempt": setup["caliper_attempt"], "arm": setup["arm"],
         "limit_seconds": limit, "started_at": now(), "steps": [],
         "refs": [dict(zip(("old", "new", "name"), line.split())) for line in lines if line.strip()]}

def save():
    with open(path + ".tmp", "w") as f:
        json.dump(entry, f, indent=2, sort_keys=True)
    os.replace(path + ".tmp", path)

class Timeout(Exception):
    pass

def step(argv, cwd, env):
    """Run argv in its own process group, recorded; on the deadline the group is killed."""
    rec = {"argv": argv, "exit": None}
    entry["steps"].append(rec)
    if deadline <= time.monotonic():
        raise Timeout()
    p = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                         stderr=subprocess.STDOUT, start_new_session=True)
    try:
        out = p.communicate(timeout=deadline - time.monotonic())[0]
    except subprocess.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        rec["output"] = p.communicate()[0].decode(errors="replace")[-4000:]
        raise Timeout()
    rec["exit"], rec["output"] = p.returncode, out.decode(errors="replace")[-4000:]
    return p.returncode == 0

save()
# The go commands see no git variable of the hook's and reach no network.
go = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
go.update(GOPROXY="off", GOTOOLCHAIN="local")
scratch, verdict = tempfile.mkdtemp(prefix="q1-ci-"), "pass"
try:
    for i, ref in enumerate(entry["refs"]):
        if ref.get("new") == "0" * 40:
            continue  # a deletion builds nothing
        tree, tar = os.path.join(scratch, str(i)), os.path.join(scratch, "%d.tar" % i)
        os.mkdir(tree)
        if not (step(["git", "archive", "--format=tar", "-o", tar, ref.get("new", "")], None, dict(os.environ))
                and step(["tar", "-xf", tar, "-C", tree], tree, go)):
            verdict = "error"
            break
        if not (step(["go", "test", "./..."], tree, go) and step(["go", "vet", "./..."], tree, go)):
            verdict = "fail"
            break
except Timeout:
    verdict = "timeout"
except OSError as e:
    verdict, entry["error"] = "error", repr(e)
finally:
    shutil.rmtree(scratch, ignore_errors=True)
entry.update(verdict=verdict, decision="accepted" if verdict == "pass" else "rejected", finished_at=now())
save()
if verdict != "pass":
    last = entry["steps"][-1] if entry["steps"] else {}
    sys.stderr.write("CI rejected this push (%s):\n%s\n" % (verdict, last.get("output") or entry.get("error", "")))
    sys.exit(1)
PY
