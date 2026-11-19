"""review-hook.py DIR LIMIT ROOT ENTRY SOCKET: the Q1 independent review (README "Review"), run by an agent-ci-review
remote's pre-receive on a push CI accepted. Each pushed tip but a deletion gets one fresh `claude --print` of
main...<tip> with the pushed tree readable, under a new HOME outside ROOT, the agent's HOME, within LIMIT
seconds, which the reviewer process at SOCKET runs (review-server.py): this hook never holds the credential.
ENTRY, made empty first, becomes the push's entry; anything but an approve rejects the push."""
import datetime, fcntl, json, os, re, shutil, socket, subprocess, sys, tempfile

BUDGET = 3  # reviews per attempt, whatever each said; past it every push is rejected
COUNTS = ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
spec = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, spec)
sys.dont_write_bytecode = True  # nothing written beside the harness
from gate_record import unique  # a repeated key is refused, never won last
evidence, limit, root, path, sock = sys.argv[1], float(sys.argv[2]), os.path.realpath(sys.argv[3]), sys.argv[4], sys.argv[5]
now = lambda: datetime.datetime.now(datetime.timezone.utc).isoformat()

def save():
    with open(path + ".tmp", "w") as f:
        f.write(json.dumps(entry, indent=2, sort_keys=True))
    os.replace(path + ".tmp", path)

def spent():
    """Reviews this attempt has used: those of every other entry, and one for each entry that cannot
    be read (a hook that died), so the bound errs toward fewer reviews."""
    n = 0
    for name in set(os.listdir(evidence)) - {os.path.basename(path), os.path.basename(path) + ".tmp", "setup.json"}:
        try:
            with open(os.path.join(evidence, name)) as f:
                n += sum(r["verdict"] != "budget" for r in json.load(f)["reviews"])
        except (OSError, ValueError, KeyError, TypeError):
            n += 1
    return n

def answer(out):
    """Reads the reviewer's `--output-format json` envelope onto rec: its usage (a count it does not
    report is null), the models its modelUsage names, and what it said. The verdict is the result's
    one JSON object, bare or fenced: approve or block with a findings list, or else unreadable."""
    doc = json.loads(out)
    usage, models, text = doc.get("usage"), doc.get("modelUsage"), doc.get("result")
    rec["usage"] = {k: usage.get(k) if type(usage.get(k)) is int else None for k in COUNTS} if isinstance(usage, dict) else None
    rec["reported_model"] = sorted(models) if isinstance(models, dict) and models else None
    rec["is_error"] = doc.get("is_error")
    rec["result"] = text[-4000:] if isinstance(text, str) else None  # an error too (auth, quota)
    if doc.get("is_error") is not False:
        return "failed"
    fenced = re.fullmatch(r"\s*```(?:json)?\n(.*)\n```\s*", text, re.S)
    said = json.loads(fenced.group(1) if fenced else text, object_pairs_hook=unique)
    if said.get("verdict") not in ("approve", "block") or not isinstance(said.get("findings"), list):
        return "unreadable"
    rec["findings"] = said["findings"]
    return said["verdict"]

def ask(request):
    """The reviewer process's answer to request, or a ValueError that names SOCKET."""
    if not sock:
        raise ValueError("no reviewer process: Q1_REVIEWER_SOCKET was unset or empty when setup installed this hook")
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
            s.settimeout(limit + 60)  # past the reviewer's own limit, which it ends at
            s.connect(sock)
            rec["asked"] = True  # one request the reviewer process logs is this hook's (reviewer_requests.py)
            s.sendall(json.dumps(request).encode() + b"\n")
            line = s.makefile("rb").readline()
    except OSError as e:
        raise ValueError("the reviewer process at %s does not answer: %r" % (sock, e))
    got = json.loads(line) if line else {"error": "it ended without answering"}
    if not isinstance(got, dict) or "error" in got:
        raise ValueError("the reviewer process at %s started no review: %s" % (sock, got.get("error") if isinstance(got, dict) else got))
    return got

def review():
    """One review of rec's tip: its verdict, with what ran and what it said recorded on rec."""
    scratch = tempfile.mkdtemp(prefix="q1-review-")
    try:
        tree, tar = os.path.join(scratch, "tree"), os.path.join(scratch, "tree.tar")
        os.mkdir(tree)
        run = lambda *a: subprocess.run(a, stdin=subprocess.DEVNULL, capture_output=True, check=True, timeout=limit).stdout
        diff = run("git", "diff", "main..." + rec["new"]).decode(errors="replace")
        run("git", "archive", "--format=tar", "-o", tar, rec["new"])
        run("tar", "-xf", tar, "-C", tree)
        with open(os.path.join(spec, "review-prompt.md")) as f:
            prompt = f.read() + "\n```diff\n" + diff + "\n```\n"
        got = ask({"tree": tree, "prompt": prompt, "limit": limit, "root": root})
    except (OSError, ValueError, subprocess.SubprocessError) as e:
        rec["reason"] = str(e) if type(e) is ValueError else repr(e)
        return "cannot-start"
    finally:
        shutil.rmtree(scratch, ignore_errors=True)
    rec.update(argv=got["argv"], model=got["model"], duration_seconds=got["duration_seconds"])
    if got["timeout"]:
        return "timeout"
    rec["exit"], rec["stderr"] = got["exit"], got["stderr"][-4000:]  # hidden whole by the reviewer process, then cut
    try:
        verdict = answer(got["stdout"])
    except (ValueError, KeyError, TypeError, AttributeError):
        verdict = "unreadable"
    return "failed" if got["exit"] != 0 else verdict

with open(os.path.join(evidence, "setup.json")) as f:
    fcntl.flock(f, fcntl.LOCK_EX)  # one push's reviews at a time, so the budget is counted whole
    setup = json.load(f)
    entry = {"schema": 1, "task_id": setup["task_id"], "caliper_attempt": setup["caliper_attempt"], "arm": setup["arm"],
             "limit_seconds": limit, "budget": BUDGET, "started_at": now(), "reviews": [],
             "refs": [dict(zip(("old", "new", "name"), line.split())) for line in sys.stdin.read().splitlines() if line.strip()]}
    save()
    used = spent()
    for ref in entry["refs"]:
        if ref["new"] == "0" * 40:
            continue  # a deletion has no tree to review
        rec = dict(ref, asked=False, argv=None, model=None, exit=None, is_error=None, duration_seconds=None, result=None, findings=None,
                   usage=None, reported_model=None, verdict="budget" if used >= BUDGET else None)
        entry["reviews"].append(rec)
        if rec["verdict"] is None:
            used += 1
            save()
            rec["verdict"] = review()
        if rec["verdict"] != "approve":
            break
    rejected = [r for r in entry["reviews"] if r["verdict"] != "approve"]
    entry.update(decision="rejected" if rejected else "accepted", finished_at=now())
    save()
if rejected:
    r = rejected[0]
    why = {"budget": "review budget spent: this attempt has had its %d reviews" % BUDGET, "block": json.dumps(r["findings"], indent=2)}
    why = why.get(r["verdict"]) or r.get("reason") or r.get("result") or r.get("stderr") or ""
    sys.stderr.write("Review rejected this push (%s) of %s:\n%s\n" % (r["verdict"], r["name"], why))
    sys.exit(1)
