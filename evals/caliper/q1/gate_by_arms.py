"""Report gate_record.py's per-attempt verdicts grouped by the arm each Q1 attempt was assigned.

README "Gate verdicts by arm" owns the report, why its arm assignment is exact,
and its limits; "As run" owns the join. "As assigned" is adoption: whether the
gate ran, per arm. "As run" is each attempt's task outcome by whether the gate
ran, joined on the attempt key (task id, attempt number) the patched caliper
passes cleanup.sh, never by order, timestamp or transcript.

    python3 gate_by_arms.py --revision REV --modified MOD --arm NAME DIR RESULT...

REV and MOD are as gate_record.py takes them. Each --arm is one arm run-full.sh
ran: NAME (with-skill or ablated), DIR (the TMPDIR its caliper run had, so its
records are DIR/caliper-q1-gate) and RESULT (that run's caliper result file).
"""
import argparse, json, os, sys

import gate_record

# run-full.sh's arms, each with the run.ablated its caliper result must carry.
ARMS = {"with-skill": [], "ablated": ["assistant"]}
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
        ablated, interrupted, saved = doc["run"]["ablated"], doc["run"]["interrupted"], {}
        for task in doc["task_results"]:
            for attempt in task["attempts"]:
                key = (task["task_id"], attempt["attempt"])
                if not isinstance(key[0], str) or type(key[1]) is not int or key in saved \
                        or not isinstance(attempt["outcome"], str):
                    raise ValueError("attempt %r is not a new key with an outcome" % (key,))
                saved[key] = attempt["outcome"]
    except (OSError, ValueError, KeyError, TypeError) as e:
        refuse("arm %s: %s is not a readable caliper result: %r" % (name, path, e))
    if ablated != ARMS[name]:
        refuse("arm %s: %s has run.ablated %r, not this arm's %r" % (name, path, ablated, ARMS[name]))
    return saved, interrupted is True


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
    ok = gate_record.schema(record) in (3, 4, 5) and isinstance(key[0], str) and key[0] and type(key[1]) is int
    return key if ok else None


def as_run(results):
    """Gate observation by task outcome, every state named, over the attempts given."""
    table = {state: {} for state in gate_record.STATES}
    for r in results:
        cell = table[gate_record.state(r)]
        cell[r["task_outcome"]] = cell.get(r["task_outcome"], 0) + 1
    return table


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
    saved, interrupted = saved_attempts(name, result)
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
    stages = gate_record.stage_counts(results)
    established = stages.pop("review_established")
    stages["review_established"] = {"true": established["true"], "false": established["false"],
                                    "unknown": established["null"]}
    verdicts = stages["verdicts"]
    stages["review_executed_over_measured"] = {
        "review_executed": verdicts[gate_record.EXECUTED], "not_measured": verdicts[gate_record.NOT_MEASURED],
        "measured": len(results) - verdicts[gate_record.NOT_MEASURED]}
    shown = [[dict(r, arm=name, stage_execution=unknown(r["stage_execution"])) for r in rs] for rs in (results, keyless)]
    return {"ablated": ARMS[name], "caliper_attempts": len(saved), "interrupted": interrupted, "records": found,
            "missing_records": len(set(saved) - set(joined)), "records_beyond_saved": beyond,
            "counts": gate_record.counts(results), "stage_execution": stages, "as_run": as_run(results),
            "attempts": shown[0], "records_without_key": shown[1]}


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
    both = as_run([a for arm in arms.values() for a in arm["attempts"]])
    json.dump({"schema": 2, "build": built, "as_assigned": arms, "as_run": {"by_arm": run, "both_arms": both}},
              sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1:])
