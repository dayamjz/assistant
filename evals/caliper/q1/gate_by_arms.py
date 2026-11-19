"""Report gate_record.py's per-attempt verdicts grouped by the arm each Q1 attempt was assigned.

README "Gate verdicts by arm" owns the report, why its arm assignment is exact,
and its limits; "As run" owns the join. "As assigned" is what ran, per arm: the
gate (adoption, or contamination in an arm that withholds it), CI and the
reviewer. "As run" is each attempt's task outcome by what checked it (gate
state, then review execution), joined on the attempt key (task id, attempt
number) the patched caliper passes cleanup.sh, never by order, timestamp or
transcript.

    python3 gate_by_arms.py --revision REV --modified MOD --arm NAME DIR RESULT...

REV and MOD are as gate_record.py takes them. Each --arm is one arm run-full.sh
ran: NAME (agent-ci, agent-ci-review or full-gate), DIR (the TMPDIR its caliper run had, so its
records are DIR/caliper-q1-gate) and RESULT (that run's caliper result file).
"""
import argparse, json, os, sys

import gate_record

# run-full.sh's arms (README "Arms"), each with the run.ablated its caliper result must carry.
# An arm whose result ablates the skill withholds the gate, so a gate it used anyway is contamination.
ARMS = {"agent-ci": ["assistant"], "agent-ci-review": ["assistant"], "full-gate": []}
REVIEWED = "agent-ci-review"  # the one arm with a reviewer at the remote (README "Review")
# Each checker's verdicts as the report states them, every one named in its counts:
# an arm assigns CI everywhere and a reviewer in REVIEWED alone.
CI_STATES = ("ci ran", "not invoked", gate_record.CANNOT)
REVIEW_STATES = (gate_record.EXECUTED, "no push, not reviewed", gate_record.CANNOT, "not assigned")
CONTAMINATION = ("contaminated", gate_record.CANNOT, "clean", "not withheld")
GATE_USED = ("ran", "started-not-finished", "reached-no-stage")
NO_HOME = "no persisted home was observed, which is not proof that the gate never started"
# An attempt's task outcome when it is not caliper's: a record with no attempt key,
# and a record whose key the arm's result did not save.
NO_KEY, NOT_SAVED = "cannot tell", "not saved"


def refuse(message):
    sys.stderr.write("gate_by_arms.py: %s\n" % message)
    sys.exit(2)


def saved_attempts(name, path):
    """({(task_id, attempt): outcome} of every attempt the arm's caliper result saved,
    whether that run was interrupted), refusing a result that is unreadable, is not
    this arm's, or saves a key twice."""
    try:
        with open(path) as f:
            doc = json.load(f)
        ablated, interrupted, saved, judged = doc["run"]["ablated"], doc["run"]["interrupted"], {}, 0
        for task in doc["task_results"]:
            for attempt in task["attempts"]:
                key, seconds = (task["task_id"], attempt["attempt"]), attempt["judge_seconds"]
                if not isinstance(key[0], str) or type(key[1]) is not int or key in saved \
                        or not isinstance(attempt["outcome"], str) or type(seconds) not in (int, float, type(None)):
                    raise ValueError("attempt %r is not a new key with an outcome and judge_seconds" % (key,))
                saved[key], judged = attempt["outcome"], judged + (seconds is not None)
    except (OSError, ValueError, KeyError, TypeError) as e:
        refuse("arm %s: %s is not a readable caliper result: %r" % (name, path, e))
    if ablated != ARMS[name]:
        refuse("arm %s: %s has run.ablated %r, not this arm's %r" % (name, path, ablated, ARMS[name]))
    # A null judge_seconds is no judge call recorded, never a call measured at 0 s.
    return saved, interrupted is True, {"attempts_with_a_call": judged, "attempts_none_recorded": len(saved) - judged}


def read_record(path):
    """The record, or {} when it cannot be read (gate_record judges that)."""
    try:
        with open(path) as f:
            record = json.load(f)
    except (OSError, ValueError):
        return {}
    return record if isinstance(record, dict) else {}


def key_of(record):
    """The record's attempt key (task_id, caliper_attempt), or None when it has none:
    schema 1 and 2 predate it, and cleanup.sh under a stock caliper writes nulls."""
    key = (record.get("task_id"), record.get("caliper_attempt"))
    ok = gate_record.schema(record) in (3, 4, 5, 6, 7) and isinstance(key[0], str) and key[0] and type(key[1]) is int
    return key if ok else None


def as_run(results):
    """Task outcome by what checked the attempt: gate observation, then review execution,
    every state of each named, over the attempts given (checked by checked)."""
    table = {state: {review: {} for review in REVIEW_STATES} for state in gate_record.STATES}
    for r in results:
        cell = table[gate_record.state(r)][r["review_execution"]["verdict"]]
        cell[r["task_outcome"]] = cell.get(r["task_outcome"], 0) + 1
    return table


def assigned(verdict, states, what):
    """A checker the arm assigns, as gate_record judged it, or cannot tell when that is not one of
    states (not measured: a record from before the checker's evidence), never did not run."""
    if verdict["verdict"] in states:
        return verdict
    return {"verdict": gate_record.CANNOT, "reason": "%s is %r in an arm that assigns it" % (what, verdict["verdict"])}


def contamination(name, r):
    """Whether an attempt of an arm that withholds the gate used it anyway (README "Gate verdicts by
    arm"): contaminated, clean only on no-home or never-reached, and else cannot tell."""
    if not ARMS[name]:
        return {"verdict": "not withheld"}
    state, stage = gate_record.state(r), r["stage_execution"]["verdict"]
    basis = {"gate_observation": state, "stage_execution": stage}
    if state in GATE_USED or stage in (gate_record.EXECUTED, gate_record.STARTED):
        return dict(basis, verdict="contaminated")
    if state in ("no-home", "never-reached") and stage != gate_record.CANNOT:
        return dict(basis, verdict="clean", **({"caveat": NO_HOME} if state == "no-home" else {}))
    return dict(basis, verdict=gate_record.CANNOT)


def checked(name, r):
    """One judged attempt with what its arm assigned: CI everywhere, the reviewer in REVIEWED alone
    (elsewhere not assigned), and its contamination."""
    review = assigned(r["review_execution"], REVIEW_STATES[:3], "review_execution") if name == REVIEWED \
        else {"verdict": "not assigned"}
    return dict(r, ci_execution=assigned(r["ci_execution"], CI_STATES, "ci_execution"), review_execution=review,
                contamination=contamination(name, r))


def tally(results, field, states):
    """Each state's count of results[field]'s verdict, every state named."""
    return {s: sum(1 for r in results if r[field]["verdict"] == s) for s in states}


def unknown(stage):
    """stage_execution as reported here: a null review_established reads "unknown"."""
    shown = dict(stage)
    if "review_established" in shown and shown["review_established"] is None:
        shown["review_established"] = "unknown"
    if "review_established_runs" in shown:
        shown["review_established_runs"] = [unknown(r) for r in shown["review_established_runs"]]
    return shown


def arm_report(name, tmpdir, result, built):
    """One arm: each keyed record its caliper run wrote, judged and joined to the saved
    attempt its key names, plus one cannot-tell attempt per saved attempt no record
    names, over one denominator; each record without a key judged beside it."""
    saved, interrupted, judge = saved_attempts(name, result)
    records = os.path.join(tmpdir, "caliper-q1-gate")
    if not os.path.isdir(records):
        refuse("arm %s: %s is not a directory" % (name, records))
    results, keyless, joined = [], [], {}
    for file in sorted(n for n in os.listdir(records) if n.endswith(".record.json")):
        path, attempt = os.path.join(records, file), file[:-len(".record.json")]
        record = read_record(path)
        home = record.get("home")
        # The attempt root is <TMPDIR>/<attempt>, made by this arm's caliper run alone.
        if isinstance(home, str) and os.path.realpath(os.path.dirname(os.path.dirname(home))) != os.path.realpath(tmpdir):
            refuse("arm %s: record %s names home %s, which is not under this arm's TMPDIR %s" % (name, file, home, tmpdir))
        if gate_record.schema(record) in (6, 7) and record.get("arm") != name:  # cleanup.sh's Q1_ARM, from 6 on
            refuse("arm %s: record %s was written in arm %r" % (name, file, record.get("arm")))
        checks = record.get("review_checks")
        if name != REVIEWED and isinstance(checks, dict) and checks.get("files"):  # only a harness fault writes one
            refuse("arm %s: record %s holds review entries, and this arm has no reviewer" % (name, file))
        key, judged = key_of(record), gate_record.judge(attempt, path, built)
        if key is None:  # it names no attempt, so it is listed beside the denominator, never in it
            keyless.append(dict(judged, task_outcome=NO_KEY))
            continue
        if key in joined:  # one attempt, two records: neither can be its record
            refuse("arm %s: records %s and %s both name attempt %r" % (name, joined[key], file, key))
        joined[key] = file
        results.append(dict(judged, task_id=key[0], caliper_attempt=key[1], task_outcome=saved.get(key, NOT_SAVED)))
    found, beyond = len(results) + len(keyless), len(set(joined) - set(saved))
    # Only an interrupted run explains a record caliper did not save: it saves no attempt
    # whose agent a cancellation killed, though its cleanup ran. Anything else is not this run's.
    if (beyond or found > len(saved)) and not interrupted:
        refuse("arm %s: %d records in %s, %d of them not saved, but %s saved %d attempts and was not interrupted"
               % (name, found, records, beyond, result, len(saved)))
    # Every saved attempt counts once: one no record names is cannot tell, never did not run.
    results += [dict(gate_record.no_record(None, "no record names this attempt's key"), task_id=k[0],
                     caliper_attempt=k[1], task_outcome=saved[k]) for k in sorted(set(saved) - set(joined))]
    results, keyless = [checked(name, r) for r in results], [checked(name, r) for r in keyless]
    stages = gate_record.stage_counts(results)
    established = stages.pop("review_established")
    stages["review_established"] = {"true": established["true"], "false": established["false"],
                                    "unknown": established["null"]}
    verdicts = stages["verdicts"]
    stages["review_executed_over_measured"] = {
        "review_executed": verdicts[gate_record.EXECUTED], "not_measured": verdicts[gate_record.NOT_MEASURED],
        "measured": len(results) - verdicts[gate_record.NOT_MEASURED]}
    shown = [[dict(r, arm=name, stage_execution=unknown(r["stage_execution"])) for r in rs] for rs in (results, keyless)]
    # Every contaminated or cannot-tell attempt by its key and run ids, never a bare rate.
    listed = [{"task_id": r["task_id"], "caliper_attempt": r["caliper_attempt"], "attempt": r["attempt"],
               "run_ids": [run["id"] for run in r["gate_observation"].get("runs", [])], "contamination": r["contamination"]}
              for r in results if r["contamination"]["verdict"] in ("contaminated", gate_record.CANNOT)]
    return {"ablated": ARMS[name], "caliper_attempts": len(saved), "interrupted": interrupted, "records": found,
            "missing_records": len(set(saved) - set(joined)), "records_beyond_saved": beyond,
            "counts": gate_record.counts(results), "stage_execution": stages, "as_run": as_run(results),
            "ci_execution": tally(results, "ci_execution", CI_STATES),
            "review_execution": tally(results, "review_execution", REVIEW_STATES),
            "contamination": {"counts": tally(results, "contamination", CONTAMINATION), "attempts": listed},
            "judge_calls": judge, "attempts": shown[0], "records_without_key": shown[1]}


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--revision", required=True, help="the built binary's vcs.revision")
    parser.add_argument("--modified", required=True, choices=("true", "false"), help="its vcs.modified")
    parser.add_argument("--arm", required=True, nargs=3, action="append", metavar=("NAME", "DIR", "RESULT"))
    args = parser.parse_args(argv)
    if not gate_record.HEX40.match(args.revision):
        parser.error("--revision %r is not a full commit id" % args.revision)
    names, dirs = [a[0] for a in args.arm], [os.path.realpath(a[1]) for a in args.arm]
    if any(n not in ARMS for n in names) or len(set(names)) != len(names):
        parser.error("each arm is one of %s, named once: %s" % (", ".join(ARMS), " ".join(names)))
    if len(set(dirs)) != len(dirs):  # one folder's records would count in two arms
        parser.error("two arms name one DIR")
    built = {"revision": args.revision, "modified": args.modified == "true"}
    arms = {name: arm_report(name, tmpdir, result, built) for name, tmpdir, result in args.arm}
    run = {name: arms[name].pop("as_run") for name in arms}
    every = as_run([a for arm in arms.values() for a in arm["attempts"]])
    json.dump({"schema": 3, "build": built, "as_assigned": arms, "as_run": {"by_arm": run, "all_arms": every}},
              sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1:])
