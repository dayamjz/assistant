"""Say, per Q1 attempt, whether the gate ran, from the gate record cleanup.sh saved.

README "Gate record instrument" owns the two fields, the mapping table, the
counts and the limits. This reads `<attempt>.record.json` files only: no
transcript, and no shell command an attempt typed.

    python3 gate_record.py --revision REV --records DIR ATTEMPT...

REV is the revision of the assistant binary the harness built (`go version -m`).
ATTEMPT is an attempt root's basename; every one named is reported, so a record
file that is missing is reported as the harness failing, never dropped.
"""
import argparse, collections, json, os, re, subprocess, sys

INDETERMINATE = "indeterminate"
STATES = ("ran", "reached-no-stage", "never-reached", "no-home", "no-checkout", INDETERMINATE)
STATUSES = {"pending", "running", "held", "passed", "failed", "terminated"}
HEX40, HEX64 = re.compile(r"[0-9a-f]{40}\Z"), re.compile(r"[0-9a-f]{64}\Z")
# Error lines that are not on the read path: the usage figure and the stop.
# Every other line, one this instrument does not know included, is read-path.
NOT_READ_PATH = ("usage: ", "stop: ")

# The mapping table, one row per cleanup.sh outcome and stop result it can
# write, onto (gate_observation, cleanup_status). "runs" means valid, with the
# state the checked runs give. Any other pair is indeterminate, because
# cleanup.sh cannot write it (it stops in a finally once a home is seen).
TABLE = {
    ("read", "ok"): ("runs", "success"),
    ("read", "failed"): ("runs", "failure"),
    ("no-checkout", "ok"): ("no-checkout", "success"),
    ("no-checkout", "failed"): ("no-checkout", "failure"),
    ("no-home", "absent"): ("no-home", "not-attempted"),
    ("error", "ok"): (INDETERMINATE, "success"),
    ("error", "failed"): (INDETERMINATE, "failure"),
    ("error", "absent"): (INDETERMINATE, "failure"),
}


def exited_zero(step):
    """Whether a recorded step's exit is the integer 0 (JSON false is not)."""
    return isinstance(step, dict) and type(step.get("exit")) is int and step["exit"] == 0


def stop_result(record):
    """ok, failed or absent: the stop step as cleanup.sh recorded it."""
    if "stop" not in record:
        return "absent"
    return "ok" if exited_zero(record["stop"]) else "failed"


def indeterminate(reason):
    return {"validity": INDETERMINATE, "reason": reason}


def commit_in(checkout, sha):
    """Whether sha names a commit in the repository at checkout."""
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    try:
        return subprocess.run(["git", "cat-file", "-e", sha + "^{commit}"], cwd=checkout, env=env,
                              stdin=subprocess.DEVNULL, capture_output=True, timeout=60).returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def observe_runs(record, revision):
    """The gate state the runs read shows, or the first check it fails."""
    step, checkout = record.get("runs"), record.get("checkout")
    if not isinstance(step, dict) or step.get("cwd") != checkout:
        return indeterminate("the runs read is missing or was not made from the record's checkout")
    doc = step.get("output")
    runs = doc.get("runs", False) if isinstance(doc, dict) else False  # null is no runs, as cleanup.sh reads it
    if not exited_zero(step) or not isinstance(runs, (list, type(None))):
        return indeterminate("the runs step is not a successful runs document")
    runs, seen = runs or [], []
    for run in runs:
        build = run.get("build") if isinstance(run, dict) else None
        if not isinstance(build, dict) or not isinstance(run.get("id"), str) or not run["id"]:
            return indeterminate("a run has no id or no build")
        rid, head = run["id"], run.get("submitted_head")
        if not isinstance(run.get("status"), str) or run["status"] not in STATUSES:
            return indeterminate("run %s: unknown status %r" % (rid, run.get("status")))
        if not isinstance(head, str) or not HEX40.match(head) or not commit_in(checkout, head):
            return indeterminate("run %s: submitted_head %r is not a commit in %s" % (rid, head, checkout))
        if build.get("revision") != revision:
            return indeterminate("run %s: build.revision %r is not %s" % (rid, build.get("revision"), revision))
        if not isinstance(run.get("config_digest"), str) or not HEX64.match(run["config_digest"]):
            return indeterminate("run %s: config_digest %r is not a sha256" % (rid, run.get("config_digest")))
        seen.append({"id": rid, "status": run["status"], "submitted_head": head,
                     "build": {"revision": build["revision"]}, "config_digest": run["config_digest"]})
    repositories = {r.get("repository_id") if isinstance(r.get("repository_id"), str) else None for r in runs}
    if len(repositories) > 1 or repositories & {None, ""}:
        return indeterminate("the runs name repositories %s, not one" % sorted(map(repr, repositories)))
    if not seen:
        state = "never-reached"
    elif all(r["status"] == "pending" for r in seen):
        state = "reached-no-stage"
    else:
        state = "ran"
    return {"validity": "valid", "state": state, "runs": seen}


def judge(attempt, path, revision):
    """One attempt's gate_observation and cleanup_status, from its record."""
    unread = {"status": "failure", "exit": None, "stderr": None}
    try:
        with open(path) as f:
            record = json.load(f)
    except FileNotFoundError:
        return {"attempt": attempt, "cleanup_status": dict(unread, reason="no record file"),
                "gate_observation": indeterminate("no record file: the harness failed")}
    except (OSError, ValueError) as e:
        return {"attempt": attempt, "cleanup_status": dict(unread, reason="record unreadable"),
                "gate_observation": indeterminate("record unreadable: %r" % e)}
    if not isinstance(record, dict):
        record = {}
    stop = record.get("stop") if isinstance(record.get("stop"), dict) else {}
    outcome, result = record.get("outcome"), stop_result(record)
    row = (outcome, result) if isinstance(outcome, str) else None
    gate, cleanup = TABLE.get(row, (None, "success" if result == "ok" else "failure"))
    cleanup_status = {"status": cleanup, "exit": stop.get("exit"), "stderr": stop.get("stderr")}
    home, checkout, errors = record.get("home"), record.get("checkout"), record.get("errors")
    if not isinstance(errors, list):
        errors = ["errors is not a list: %r" % (errors,)]
    read_path = [e for e in errors if not str(e).startswith(NOT_READ_PATH)]
    if gate is None:
        observation = indeterminate("outcome %r with stop %s is not a row of the mapping table" % (outcome, result))
    elif gate == INDETERMINATE:
        observation = indeterminate("cleanup outcome error: %s" % "; ".join(map(str, errors)))
    elif record.get("schema") != 1 or record.get("attempt") != attempt or not isinstance(home, str) \
            or os.path.basename(os.path.dirname(home)) != attempt \
            or checkout != os.path.join(os.path.dirname(home), "work", "work"):
        observation = indeterminate("the record's schema, attempt, home or checkout is not this attempt's")
    elif read_path:
        observation = indeterminate("read-path errors: %s" % "; ".join(map(str, read_path)))
    elif gate == "runs":
        observation = observe_runs(record, revision)
    else:
        observation = {"validity": "valid", "state": gate, "runs": []}
    return {"attempt": attempt, "gate_observation": observation, "cleanup_status": cleanup_status}


def state(result):
    """The attempt's valid state, or indeterminate."""
    observation = result["gate_observation"]
    return observation["state"] if observation["validity"] == "valid" else INDETERMINATE


def operationally_failed(result):
    """Derived from the two fields, never stored: the cleanup failed, so the
    attempt is never a clean validated landing whatever the gate showed."""
    return result["cleanup_status"]["status"] == "failure"


def counts(results):
    """Both counts over one denominator: every attempt named."""
    by_state = collections.Counter(state(r) for r in results)
    return {"attempts": len(results), "gate_observation": {s: by_state[s] for s in STATES},
            "valid_with_failed_cleanup": sum(1 for r in results
                                             if state(r) != INDETERMINATE and operationally_failed(r))}


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--revision", required=True, help="the built binary's vcs.revision")
    parser.add_argument("--records", required=True, help="the caliper-q1-gate directory")
    parser.add_argument("attempts", nargs="+")
    args = parser.parse_args(argv)
    if not HEX40.match(args.revision):
        parser.error("--revision %r is not a full commit id" % args.revision)
    if not os.path.isdir(args.records):
        parser.error("--records %r is not a directory" % args.records)
    results = [judge(a, os.path.join(args.records, a + ".record.json"), args.revision) for a in args.attempts]
    named = {a + ".record.json" for a in args.attempts}
    unlisted = sorted(n for n in os.listdir(args.records) if n.endswith(".record.json") and n not in named)
    json.dump({"schema": 1, "revision": args.revision, "attempts": results, "counts": counts(results),
               "unlisted_records": unlisted}, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1:])
