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
import argparse, collections, json, os, re, sys

INDETERMINATE = "indeterminate"
STATES = ("ran", "started-not-finished", "reached-no-stage", "never-reached", "no-home", "no-checkout", INDETERMINATE)
ENDED = {"held", "passed", "failed", "terminated"}  # a hold or an end status
STATUSES = ENDED | {"pending", "running"}
HEX40, HEX64 = re.compile(r"[0-9a-f]{40}\Z"), re.compile(r"[0-9a-f]{64}\Z")
# Error lines that are not on the read path: the usage figure and the stop.
# Every other line, one this instrument does not know included, is read-path.
# The per-run stage reads are the stage verdict's to judge, not the gate's.
# A missing attempt key loses the "as run" join (#936) and makes any run cannot tell, since its
# head check needs the key (#941); the arm is gate_by_arms.py's, the CI and review evidence their verdicts'.
NOT_READ_PATH = ("usage: ", "stop: ", "run:", "log:", "run-after:", "key: ", "arm: ", "ci: ", "review: ")
# 1 predates the stage capture (#926): its stage verdict is not measured. 3 adds the attempt key (#936).
# 4 adds head_checks (#941): a run in a record without them is cannot tell.
# 5 adds wait_down (a#956): a stop in a record without it is failed (stop_result). 6 adds arm and ci_checks.
# 7 adds review_checks.
SCHEMAS = (1, 2, 3, 4, 5, 6, 7)

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


def wait_down(record):
    """The record's wait_down when it is an object, else None."""
    return record["wait_down"] if isinstance(record.get("wait_down"), dict) else None


def stop_result(record):
    """ok, failed or absent: the stop step as cleanup.sh recorded it. ok needs the stop's
    exit 0 and wait_down's down true: a service never reported down, or a stop with no
    wait_down (every record before schema 5), was never confirmed down, so it is failed."""
    if "stop" not in record:
        return "absent"
    return "ok" if exited_zero(record["stop"]) and (wait_down(record) or {}).get("down") is True else "failed"


def indeterminate(reason):
    return {"validity": INDETERMINATE, "reason": reason}


def head_verified(record, checks, rid, head):
    """Whether cleanup.sh found head a commit of this attempt's checkout for run rid: exactly one
    head_checks entry names the run, and it carries the record's attempt key, that same sha and
    is_commit true. Anything else is not verified, and never proof of more than the commit."""
    key, mine = (record.get("task_id"), record.get("caliper_attempt")), [c for c in checks if c.get("run_id") == rid]
    if len(mine) != 1 or not isinstance(key[0], str) or not key[0] or type(key[1]) is not int:
        return False
    c = mine[0]
    return c.get("task_id") == key[0] and type(c.get("caliper_attempt")) is int and c["caliper_attempt"] == key[1] \
        and c.get("submitted_head") == head and c.get("is_commit") is True


def observe_runs(record, built):
    """The gate state the runs read shows, or the first check it fails."""
    step, checkout = record.get("runs"), record.get("checkout")
    if not isinstance(step, dict) or step.get("cwd") != checkout:
        return indeterminate("the runs read is missing or was not made from the record's checkout")
    doc = step.get("output")
    runs = doc.get("runs", False) if isinstance(doc, dict) else False  # null is no runs, as cleanup.sh reads it
    if not exited_zero(step) or not isinstance(runs, (list, type(None))):
        return indeterminate("the runs step is not a successful runs document")
    runs, seen, checks = runs or [], [], record.get("head_checks", [])
    if not isinstance(checks, list) or not all(isinstance(c, dict) for c in checks):
        return indeterminate("head_checks is not a list of entries")
    for run in runs:
        build = run.get("build") if isinstance(run, dict) else None
        if not isinstance(build, dict) or not isinstance(run.get("id"), str) or not run["id"]:
            return indeterminate("a run has no id or no build")
        rid, head = run["id"], run.get("submitted_head")
        if not isinstance(run.get("status"), str) or run["status"] not in STATUSES:
            return indeterminate("run %s: unknown status %r" % (rid, run.get("status")))
        if not isinstance(head, str) or not HEX40.match(head) or not head_verified(record, checks, rid, head):
            return indeterminate("run %s: submitted_head %r has no head check that found it a commit in %s"
                                 % (rid, head, checkout))
        if build.get("revision") != built["revision"] or build.get("modified") is not built["modified"]:
            return indeterminate("run %s: build.revision %r, build.modified %r is not the built binary's %s, %s" % (
                rid, build.get("revision"), build.get("modified"), built["revision"], built["modified"]))
        if not isinstance(run.get("config_digest"), str) or not HEX64.match(run["config_digest"]):
            return indeterminate("run %s: config_digest %r is not a sha256" % (rid, run.get("config_digest")))
        seen.append({"id": rid, "status": run["status"], "submitted_head": head, "config_digest": run["config_digest"],
                     "build": {"revision": build["revision"], "modified": build["modified"]}})
    if len(checks) != len(runs):
        return indeterminate("head_checks has %d entries for %d runs" % (len(checks), len(runs)))
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


# A completed CI entry's (verdict, decision), and the count it is reported under (README "CI").
CI_OUTCOMES = {("pass", "accepted"): "accepted", ("fail", "rejected"): "rejected_fail",
               ("timeout", "rejected"): "rejected_timeout", ("error", "rejected"): "rejected_error"}
CI_STEPS = (["git", "archive", "--format=tar", "-o"], ["tar", "-xf", "-C"], ["go", "test", "./..."], ["go", "vet", "./..."])
# What a review entry's review can say (README "Review execution"), each counted apart.
REVIEW_VERDICTS = ("approve", "block", "unreadable", "failed", "timeout", "cannot-start", "budget")


def hook_evidence(record, field, what):
    """A hook's entries parsed, the refs moved since setup and the attempt's key (README "CI", "Review"),
    once setup.json is this attempt's and the hook the one setup wrote; anything else raises."""
    found = record.get(field) if isinstance(record.get(field), dict) else {}
    key, docs = (record.get("task_id"), record.get("caliper_attempt"), record.get("arm")), {}
    for name, text in found["files"].items():
        try:
            docs[name] = json.loads(text, object_pairs_hook=unique)
        except ValueError as e:
            raise ValueError("%s is unreadable: %s" % (name, e))
    setup, now = docs.pop("setup.json"), found["refs"]
    if (setup["task_id"], setup["caliper_attempt"], setup["arm"]) != key or not isinstance(key[0], str) \
            or type(key[1]) is not int or not isinstance(now, dict) or not isinstance(setup["refs"], dict):
        raise ValueError("setup.json or the refs at capture are not this attempt's")
    if not isinstance(setup["hook_sha256"], str) or found.get("hook_sha256") != setup["hook_sha256"]:
        raise ValueError("the %s hook is missing or not the one setup wrote" % what)
    moved = {(n, sha) for n, sha in now.items() if setup["refs"].get(n) != sha} | \
            {(n, "0" * 40) for n in setup["refs"] if n not in now}
    return docs, moved, key


def ci_execution(record):
    """Whether CI checked every push that moved the attempt's remote (README "CI"), fail-closed:
    "ci ran" with each push counted under its outcome, "not invoked" when nothing was pushed, and
    cannot tell for anything the evidence does not show, never a check that did not run."""
    if schema(record) in (1, 2, 3, 4, 5):
        return {"verdict": NOT_MEASURED}
    try:
        if schema(record) not in (6, 7):
            raise ValueError("schema %r is not one this reads" % (record.get("schema"),))
        docs, moved, key = hook_evidence(record, "ci_checks", "CI")
        pushes, checked = dict.fromkeys(CI_OUTCOMES.values(), 0), set()
        for name, e in docs.items():
            outcome = CI_OUTCOMES.get((e.get("verdict"), e.get("decision"))) if isinstance(e, dict) else None
            if not name.startswith("push-") or outcome is None or e.get("schema") != 1 \
                    or (e.get("task_id"), e.get("caliper_attempt"), e.get("arm")) != key:
                raise ValueError("%s is not a decided check of this attempt" % name)
            want = [w + [r["new"]] * (w[0] == "git") for r in e["refs"] if r["new"] != "0" * 40 for w in CI_STEPS]
            ran = [[a for a in s["argv"] if a[:1] != "/"] if exited_zero(s) else None for s in e["steps"]]  # absolute paths out
            if outcome == "accepted" and ran != want:
                raise ValueError("%s is accepted without a passing archive, extract, go test and go vet of each tip" % name)
            pushes[outcome] += 1
            if outcome == "accepted":
                checked |= {(r["name"], r["new"]) for r in e["refs"]}
        if moved - checked:
            raise ValueError("refs moved with no accepted check: %s" % sorted(moved - checked))
    except (ValueError, KeyError, TypeError, AttributeError) as e:
        return {"verdict": CANNOT, "reason": "CI evidence: %s" % (e if isinstance(e, ValueError) else repr(e))}
    return {"verdict": "ci ran" if docs else "not invoked", "pushes": pushes}


def review_execution(record):
    """Whether the reviewer approved every push that moved the attempt's remote (README "Review
    execution"): "review executed" with each review counted under its verdict, "no push, not
    reviewed", "not assigned" in an arm with no reviewer, and else cannot tell, as for CI."""
    if schema(record) in (1, 2, 3, 4, 5):
        return {"verdict": NOT_MEASURED}
    try:
        if record.get("arm") != "agent-ci-review" and not (record.get("review_checks") or {}).get("files"):
            return {"verdict": "not assigned"}
        if schema(record) != 7 or record.get("arm") != "agent-ci-review":
            raise ValueError("a schema %r record of arm %r holds no review entries" % (record.get("schema"), record.get("arm")))
        docs, moved, key = hook_evidence(record, "review_checks", "review")
        reviews, approved = dict.fromkeys(REVIEW_VERDICTS, 0), set()
        for name, e in docs.items():
            if not name.startswith("push-") or e.get("schema") != 1 or e.get("decision") not in ("accepted", "rejected") \
                    or (e.get("task_id"), e.get("caliper_attempt"), e.get("arm")) != key:
                raise ValueError("%s is not a completed review of this attempt" % name)
            tips = {(r["name"], r["new"]) for r in e["refs"] if r["new"] != "0" * 40}
            approves = {(r["name"], r["new"]) for r in e["reviews"] if r["verdict"] == "approve"}
            if (e["decision"] == "accepted") != (tips <= approves and len(approves) == len(e["reviews"])):
                raise ValueError("%s's decision is not its reviews'" % name)
            for r in e["reviews"]:
                reviews[r["verdict"]] += 1
            approved |= {(r["name"], r["new"]) for r in e["refs"]} if e["decision"] == "accepted" else set()
        if moved - approved:
            raise ValueError("refs moved with no approving review: %s" % sorted(moved - approved))
    except (ValueError, KeyError, TypeError, AttributeError) as e:
        return {"verdict": CANNOT, "reason": "review evidence: %s" % (e if isinstance(e, ValueError) else repr(e))}
    return {"verdict": "review executed" if docs else "no push, not reviewed", "reviews": reviews}


def unique(pairs):
    """A JSON object whose keys are all distinct; a repeated key would win last, so it is refused."""
    keys = [k for k, _ in pairs]
    if len(set(keys)) != len(keys):
        raise ValueError("a key appears more than once: %s" % sorted({k for k in keys if keys.count(k) > 1}))
    return dict(pairs)


def no_record(attempt, reason="no record file"):
    """The result for an attempt with no record (file): the harness failed, so
    cannot tell, never "did not run". gate_by_arms.py uses it, with its own
    reason, for an attempt caliper saved that no record names."""
    return {"attempt": attempt, "cleanup_status": {"status": "failure", "exit": None, "stderr": None,
                                                   "reason": reason},
            "gate_observation": indeterminate(reason + ": the harness failed"),
            "stage_execution": {"verdict": CANNOT, "reason": reason}, "ci_execution": {"verdict": CANNOT, "reason": reason},
            "review_execution": {"verdict": CANNOT, "reason": reason}}


def judge(attempt, path, built):
    """One attempt's gate_observation and cleanup_status, from its record."""
    unread = {"status": "failure", "exit": None, "stderr": None}
    try:
        with open(path) as f:
            record = json.load(f, object_pairs_hook=unique)
    except FileNotFoundError:
        return no_record(attempt)
    except (OSError, ValueError) as e:
        return {"attempt": attempt, "cleanup_status": dict(unread, reason="record unreadable"),
                "gate_observation": indeterminate("record unreadable: %r" % e),
                "stage_execution": {"verdict": CANNOT, "reason": "record unreadable"},
                "ci_execution": {"verdict": CANNOT, "reason": "record unreadable"},
                "review_execution": {"verdict": CANNOT, "reason": "record unreadable"}}
    if not isinstance(record, dict):
        record = {}
    stop = record.get("stop") if isinstance(record.get("stop"), dict) else {}
    outcome, result = record.get("outcome"), stop_result(record)
    row = (outcome, result) if isinstance(outcome, str) else None
    gate, cleanup = TABLE.get(row, (None, "success" if result == "ok" else "failure"))
    cleanup_status = {"status": cleanup, "exit": stop.get("exit"), "stderr": stop.get("stderr"),
                      "down": (wait_down(record) or {}).get("down")}
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
            "stage_execution": stage_execution(record, observation), "ci_execution": ci_execution(record),
            "review_execution": review_execution(record)}


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
