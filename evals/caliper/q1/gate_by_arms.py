"""Report gate_record.py's per-attempt verdicts grouped by the arm each Q1 attempt was assigned.

README "Gate verdicts by arm" owns the report, why its arm assignment is exact,
and its limits. This is adoption only: whether the gate ran, per arm. Results
"as run" (task outcomes by whether the gate ran) are not measured, because
caliper's saved result names no attempt id a gate record can be joined on, and
this never joins by order, timestamp or transcript.

    python3 gate_by_arms.py --revision REV --modified MOD --arm NAME DIR RESULT...

REV and MOD are as gate_record.py takes them. Each --arm is one arm run-full.sh
ran: NAME (with-skill or ablated), DIR (the TMPDIR its caliper run had, so its
records are DIR/caliper-q1-gate) and RESULT (that run's caliper result file).
"""
import argparse, json, os, sys

import gate_record

# run-full.sh's arms, each with the run.ablated its caliper result must carry.
ARMS = {"with-skill": [], "ablated": ["assistant"]}
AS_RUN = {"measured": False, "reason": "caliper's saved result names no attempt id, so an attempt's task result "
                                       "cannot be joined to its gate record; no join by order, timestamp or transcript"}


def refuse(message):
    sys.stderr.write("gate_by_arms.py: %s\n" % message)
    sys.exit(2)


def saved_attempts(name, path):
    """(attempts the arm's caliper result saved, whether that run was interrupted),
    refusing a result that is unreadable or is not this arm's."""
    try:
        with open(path) as f:
            doc = json.load(f)
        ablated, interrupted = doc["run"]["ablated"], doc["run"]["interrupted"]
        count = sum(len(task["attempts"]) for task in doc["task_results"])
    except (OSError, ValueError, KeyError, TypeError) as e:
        refuse("arm %s: %s is not a readable caliper result: %r" % (name, path, e))
    if ablated != ARMS[name]:
        refuse("arm %s: %s has run.ablated %r, not this arm's %r" % (name, path, ablated, ARMS[name]))
    return count, interrupted is True


def home_of(path):
    """The record's home, or None when the record cannot be read (gate_record judges that)."""
    try:
        with open(path) as f:
            record = json.load(f)
    except (OSError, ValueError):
        return None
    return record.get("home") if isinstance(record, dict) else None


def unknown(stage):
    """stage_execution as reported here: a null review_established reads "unknown"."""
    shown = dict(stage)
    if "review_established" in shown and shown["review_established"] is None:
        shown["review_established"] = "unknown"
    if "review_established_runs" in shown:
        shown["review_established_runs"] = [unknown(r) for r in shown["review_established_runs"]]
    return shown


def arm_report(name, tmpdir, result, built):
    """One arm: every record its caliper run wrote, judged, plus one cannot-tell
    attempt for each saved attempt beyond the records, over one denominator."""
    saved, interrupted = saved_attempts(name, result)
    records = os.path.join(tmpdir, "caliper-q1-gate")
    if not os.path.isdir(records):
        refuse("arm %s: %s is not a directory" % (name, records))
    results = []
    for file in sorted(n for n in os.listdir(records) if n.endswith(".record.json")):
        path, attempt = os.path.join(records, file), file[:-len(".record.json")]
        home = home_of(path)
        # The attempt root is <TMPDIR>/<attempt>, made by this arm's caliper run alone.
        if isinstance(home, str) and os.path.realpath(os.path.dirname(os.path.dirname(home))) != os.path.realpath(tmpdir):
            refuse("arm %s: record %s names home %s, which is not under this arm's TMPDIR %s" % (name, file, home, tmpdir))
        results.append(gate_record.judge(attempt, path, built))
    found = len(results)
    results += [gate_record.no_record(None) for _ in range(saved - found)]
    stages = gate_record.stage_counts(results)
    established = stages.pop("review_established")
    stages["review_established"] = {"true": established["true"], "false": established["false"],
                                    "unknown": established["null"]}
    verdicts = stages["verdicts"]
    stages["review_executed_over_measured"] = {
        "review_executed": verdicts[gate_record.EXECUTED], "not_measured": verdicts[gate_record.NOT_MEASURED],
        "measured": len(results) - verdicts[gate_record.NOT_MEASURED]}
    return {"ablated": ARMS[name], "caliper_attempts": saved, "interrupted": interrupted, "records": found,
            "missing_records": len(results) - found, "counts": gate_record.counts(results), "stage_execution": stages,
            "attempts": [dict(r, arm=name, stage_execution=unknown(r["stage_execution"])) for r in results]}


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
    json.dump({"schema": 1, "build": built, "as_assigned": arms, "as_run": AS_RUN},
              sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main(sys.argv[1:])
