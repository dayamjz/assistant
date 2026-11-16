"""Say, per Q1 attempt, whether the gate ran, from the gate record cleanup.sh saved.

README "Gate record instrument" owns the two fields, the mapping table, the
counts and the limits, and "Stage execution" owns the third field. This reads
`<attempt>.record.json` files only: no transcript, and no shell command an
attempt typed.

    python3 gate_record.py --revision REV --modified MOD --records DIR ATTEMPT...

REV and MOD are the vcs.revision and vcs.modified of the assistant binary the
harness built (`go version -m`). ATTEMPT is an attempt root's basename; every
one named is reported, so a record file that is missing is reported as the
harness failing, never dropped.
"""
import argparse, collections, json, os, re, subprocess, sys

INDETERMINATE = "indeterminate"
STATES = ("ran", "started-not-finished", "reached-no-stage", "never-reached", "no-home", "no-checkout", INDETERMINATE)
ENDED = {"held", "passed", "failed", "terminated"}  # a hold or an end status
STATUSES = ENDED | {"pending", "running"}
HEX40, HEX64 = re.compile(r"[0-9a-f]{40}\Z"), re.compile(r"[0-9a-f]{64}\Z")
# Error lines that are not on the read path: the usage figure and the stop.
# Every other line, one this instrument does not know included, is read-path.
# The per-run stage reads are the stage verdict's to judge, not the gate's.
NOT_READ_PATH = ("usage: ", "stop: ", "run:", "log:", "run-after:")
SCHEMAS = (1, 2)  # 1 predates the stage capture (#926): its stage verdict is not measured

# The mapping table: the (outcome, stop result) pairs cleanup.sh is expected
# to write, onto (gate_observation, cleanup_status). "runs" means valid, with
# the state the checked runs give. Any other pair is judged indeterminate.
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
    env["GIT_CEILING_DIRECTORIES"] = os.path.dirname(os.path.abspath(checkout))  # never a repository above it (#927)
    try:
        return subprocess.run(["git", "cat-file", "-e", sha + "^{commit}"], cwd=checkout, env=env,
                              stdin=subprocess.DEVNULL, capture_output=True, timeout=60).returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def observe_runs(record, built):
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
        if build.get("revision") != built["revision"] or build.get("modified") is not built["modified"]:
            return indeterminate("run %s: build.revision %r, build.modified %r is not the built binary's %s, %s" % (
                rid, build.get("revision"), build.get("modified"), built["revision"], built["modified"]))
        if not isinstance(run.get("config_digest"), str) or not HEX64.match(run["config_digest"]):
            return indeterminate("run %s: config_digest %r is not a sha256" % (rid, run.get("config_digest")))
        seen.append({"id": rid, "status": run["status"], "submitted_head": head, "config_digest": run["config_digest"],
                     "build": {"revision": build["revision"], "modified": build["modified"]}})
    repositories = {r.get("repository_id") if isinstance(r.get("repository_id"), str) else None for r in runs}
    if len(repositories) > 1 or repositories & {None, ""}:
        return indeterminate("the runs name repositories %s, not one" % sorted(map(repr, repositories)))
    if not seen:
        state = "never-reached"
    elif any(r["status"] in ENDED for r in seen):
        state = "ran"
    elif any(r["status"] == "running" for r in seen):
        state = "started-not-finished"  # never counted as ran
    else:
        state = "reached-no-stage"
    return {"validity": "valid", "state": state, "runs": seen}


REVIEW, BEFORE_REVIEW = "review", {"intent", "rebase"}
STAGES = ("intent", "rebase", REVIEW, "test", "document", "lint", "push", "pr", "ci")  # pipeline.Order's wire names
EXECUTED, CANNOT, NOT_MEASURED = "review executed", "cannot tell", "not measured"
STARTED, NOT_REVIEWED, NO_STAGE = "review started, not completed", "stages executed, review not executed", "no stage executed"
VERDICTS = (EXECUTED, STARTED, NOT_REVIEWED, NO_STAGE, CANNOT, NOT_MEASURED)
UNREAD, COMPLETED, RUN_STARTED = "unread", "review-completed", "review-started-not-completed"
# A review that completed with one of these reviewed nothing (internal/stages/review.go).
NOT_ESTABLISHED = {"review-not-established", "review-change-too-large", "review-everything-excluded",
                   "review-nothing-changed"}


def schema(record):
    """The record's schema when it is an integer (JSON true is not), else None."""
    return record["schema"] if type(record.get("schema")) is int else None


def stage_of(node):
    """pipeline.StageOfNode: the stage of a stage, hold:, fix: or request: node, else None."""
    for prefix in ("", "hold:", "fix:", "request:"):
        if isinstance(node, str) and node.startswith(prefix) and node[len(prefix):] in STAGES:
            return node[len(prefix):]
    return None


def review_of(doc):
    """The one review entry of a run report's stages, else None."""
    entries = [e for e in doc.get("stages") or [] if isinstance(e, dict) and e.get("stage") == REVIEW] \
        if isinstance(doc.get("stages"), list) else []
    return entries[0] if len(entries) == 1 else None


def finding_ids(report):
    """The finding ids of a findings report, or None when it is absent or not one."""
    found = report.get("findings", []) if isinstance(report, dict) else None
    if not isinstance(found, list) or not all(isinstance(f, dict) for f in found):
        return None
    return sorted(str(f.get("id", "")) for f in found)


def not_started(report):
    """Whether a finding of the report stands in for work that never started
    (findings.Finding.NotStarted, which only the program sets: the usage ceiling's)."""
    found = report.get("findings") if isinstance(report, dict) else None
    return isinstance(found, list) and any(isinstance(f, dict) and f.get("not_started") is True for f in found)


def facts(step):
    """Every field of a run report the table reads, and so every field the
    two reads are compared on (README "Stage execution"), or None unread."""
    doc = step.get("output") if exited_zero(step) else None
    if not isinstance(doc, dict) or not isinstance(doc.get("record"), dict):
        return None
    review, activity = review_of(doc), doc.get("activity")
    return {"id": doc["record"].get("id"), "status": doc["record"].get("status"), "stages": "stages" in doc,
            "position": doc.get("position"), "activity": activity.get("stage") if isinstance(activity, dict) else None,
            "review": review and {"ran": review.get("ran"), "outcome": review.get("outcome"),
                                  "findings": finding_ids(review.get("report")),
                                  "not_started": not_started(review.get("report"))}}


def written(step):
    """A stage log read's written, or None when the read failed."""
    doc = step.get("output") if exited_zero(step) else None
    return doc["written"] if isinstance(doc, dict) and isinstance(doc.get("written"), bool) else None


def classify(rid, entry):
    """(run state, table row) for one listed run: the first row that matches."""
    first = facts(entry.get("report")) if isinstance(entry, dict) else None
    if first is None:
        return UNREAD, 2
    if facts(entry.get("report_after")) != first or first["id"] != rid:
        return UNREAD, 3
    logs = entry["logs"] if isinstance(entry.get("logs"), dict) else {}
    log, review, at = written(logs.get(REVIEW)), first["review"], stage_of(first["position"])
    if review and review["ran"] is True:
        return COMPLETED, 4
    if first["stages"] and review and review["ran"] is False:
        if log is None:
            return UNREAD, 5
        if review["outcome"] == "skipped":
            return ("review-skipped", 6) if log is False else (UNREAD, 7)
        if log:
            return RUN_STARTED, 8
        if REVIEW in (at, first["activity"]):
            return UNREAD, 9
        if at in BEFORE_REVIEW and first["activity"] in BEFORE_REVIEW | {None}:
            return "review-not-reached", 10
    elif not first["stages"]:
        logged = [written(logs.get(s)) for s in STAGES]
        if first["status"] in ("pending", "terminated", "failed") and logged == [False] * len(STAGES):
            return "no-stage", 11
        if first["status"] in ("running", "held"):
            return UNREAD, 12
        if logged != [False] * len(STAGES):
            return UNREAD, 13
    return UNREAD, 14  # the catch-all: no row above matches


def established(entry):
    """(review_established, what it was decided from) for a review-completed run:
    null, never true or false, when a not_started finding is in the report."""
    review = facts(entry["report"])["review"]
    if review["findings"] is None:
        return False, "the review report is absent or not a findings report"
    if review["not_started"]:
        return None, ("a not-started hold is in the report; the review's own refusal and an "
                      "inherited fixer hold cannot be told apart")
    return not NOT_ESTABLISHED & set(review["findings"]), review["findings"]


def stage_execution(record, observation):
    """The attempt's stage verdict (README "Stage execution"), beside gate_observation."""
    if schema(record) == 1:
        return {"verdict": NOT_MEASURED}
    if observation["validity"] != "valid":
        return {"verdict": CANNOT, "reason": "gate_observation is indeterminate: " + observation["reason"]}
    if observation["state"] in ("no-home", "never-reached"):
        return {"verdict": NO_STAGE, "reason": observation["state"]}
    if observation["state"] == "no-checkout":
        return {"verdict": CANNOT, "reason": "no-checkout: the runs could not be listed"}
    entries = record["stages"] if isinstance(record.get("stages"), dict) else {}
    runs, decided = [], []
    for run in observation["runs"]:
        state, row = classify(run["id"], entries.get(run["id"]))
        runs.append({"id": run["id"], "state": state, "row": row})
        if state == COMPLETED:
            review = review_of(entries[run["id"]]["report"]["output"])
            runs[-1]["review_executions"] = len(review.get("executions") or [])
            value, basis = established(entries[run["id"]])
            decided.append({"id": run["id"], "review_established": value, "from": basis})
    states = [r["state"] for r in runs]
    # The fold, first match wins: one completed review decides, trouble beside it.
    verdict = next(v for v, holds in ((EXECUTED, COMPLETED in states), (CANNOT, UNREAD in states),
                                      (STARTED, RUN_STARTED in states), (NO_STAGE, set(states) == {"no-stage"}),
                                      (NOT_REVIEWED, True)) if holds)
    # review_established: any run true; else any run null (unknown, so not false); else false.
    return {"verdict": verdict, "runs": runs, "review_established_runs": decided,
            "review_established": next((v for v in (True, None, False)
                                        if v in [d["review_established"] for d in decided]), None),
            "trouble": {"unread": states.count(UNREAD), "review_started_not_completed": states.count(RUN_STARTED)}}


def judge(attempt, path, built):
    """One attempt's gate_observation and cleanup_status, from its record."""
    unread = {"status": "failure", "exit": None, "stderr": None}
    try:
        with open(path) as f:
            record = json.load(f)
    except FileNotFoundError:
        return {"attempt": attempt, "cleanup_status": dict(unread, reason="no record file"),
                "gate_observation": indeterminate("no record file: the harness failed"),
                "stage_execution": {"verdict": CANNOT, "reason": "no record file"}}
    except (OSError, ValueError) as e:
        return {"attempt": attempt, "cleanup_status": dict(unread, reason="record unreadable"),
                "gate_observation": indeterminate("record unreadable: %r" % e),
                "stage_execution": {"verdict": CANNOT, "reason": "record unreadable"}}
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
    elif schema(record) not in SCHEMAS or record.get("attempt") != attempt or not isinstance(home, str) \
            or os.path.basename(home) != ".assistant" or os.path.basename(os.path.dirname(home)) != attempt \
            or checkout != os.path.join(os.path.dirname(home), "work", "work"):
        observation = indeterminate("the record's schema, attempt, home or checkout is not this attempt's")
    elif read_path:
        observation = indeterminate("read-path errors: %s" % "; ".join(map(str, read_path)))
    elif gate == "runs":
        observation = observe_runs(record, built)
    else:
        observation = {"validity": "valid", "state": gate, "runs": []}
    return {"attempt": attempt, "gate_observation": observation, "cleanup_status": cleanup_status,
            "stage_execution": stage_execution(record, observation)}


def state(result):
    """The attempt's valid state, or indeterminate."""
    observation = result["gate_observation"]
    return observation["state"] if observation["validity"] == "valid" else INDETERMINATE


def operationally_failed(result):
    """Derived from cleanup_status alone, never stored: the cleanup failed,
    so the attempt is never a clean validated landing whatever gate_observation
    shows."""
    return result["cleanup_status"]["status"] == "failure"


def counts(results):
    """Both counts over one denominator: every attempt named."""
    by_state = collections.Counter(state(r) for r in results)
    return {"attempts": len(results), "gate_observation": {s: by_state[s] for s in STATES},
            "valid_with_failed_cleanup": sum(1 for r in results
                                             if state(r) != INDETERMINATE and operationally_failed(r))}


def stage_counts(results):
    """The stage verdict's counts over the same denominator, every attempt named."""
    stages = [r["stage_execution"] for r in results]
    trouble = [s.get("trouble", {}) for s in stages]
    return {"verdicts": {v: sum(1 for s in stages if s["verdict"] == v) for v in VERDICTS},
            "review_executed_with_trouble": sum(1 for s, t in zip(stages, trouble)
                                                if s["verdict"] == EXECUTED and sum(t.values())),
            "review_established": {k: sum(1 for s in stages if s.get("review_established") is v)
                                   for k, v in (("true", True), ("false", False), ("null", None))},
            "runs_unread": sum(t.get("unread", 0) for t in trouble),
            "runs_review_started_not_completed": sum(t.get("review_started_not_completed", 0) for t in trouble)}


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--revision", required=True, help="the built binary's vcs.revision")
    parser.add_argument("--modified", required=True, choices=("true", "false"), help="its vcs.modified")
    parser.add_argument("--records", required=True, help="the caliper-q1-gate directory")
    parser.add_argument("attempts", nargs="+")
    args = parser.parse_args(argv)
    if not HEX40.match(args.revision):
        parser.error("--revision %r is not a full commit id" % args.revision)
    if not os.path.isdir(args.records):
        parser.error("--records %r is not a directory" % args.records)
    if len(set(args.attempts)) != len(args.attempts):  # it would count twice in the one denominator
        parser.error("an attempt is named more than once: %s" % " ".join(args.attempts))
    built = {"revision": args.revision, "modified": args.modified == "true"}
    results = [judge(a, os.path.join(args.records, a + ".record.json"), built) for a in args.attempts]
    named = {a + ".record.json" for a in args.attempts}
    unlisted = sorted(n for n in os.listdir(args.records) if n.endswith(".record.json") and n not in named)
    # ran is never a stage-execution count: stage_execution is its own field (#926).
    json.dump({"schema": 2, "build": built, "attempts": results, "counts": counts(results),
               "stage_execution": stage_counts(results), "unlisted_records": unlisted},
              sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1:])
