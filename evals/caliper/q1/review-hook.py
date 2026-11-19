"""review-hook.py DIR LIMIT ROOT ENTRY: the Q1 independent review (README "Review"), run by an agent-ci-review
remote's pre-receive on a push CI accepted. Each pushed tip but a deletion gets one fresh `claude --print` of
main...<tip> with the pushed tree readable, under a new HOME outside ROOT, the agent's HOME, within LIMIT
seconds. ENTRY, made empty first, becomes the push's entry; anything but an approve rejects the push."""
import contextlib, datetime, fcntl, json, os, re, shutil, signal, subprocess, sys, tempfile, time

BUDGET = 3  # reviews per attempt, whatever each said; past it every push is rejected
COUNTS = ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
spec = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, spec)
sys.dont_write_bytecode = True  # nothing written beside the harness
from gate_record import unique  # a repeated key is refused, never won last
evidence, limit, root, path = sys.argv[1], float(sys.argv[2]), os.path.realpath(sys.argv[3]), sys.argv[4]
token = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN", "")
now = lambda: datetime.datetime.now(datetime.timezone.utc).isoformat()
# The reviewer's one credential is written nowhere: taken out of all this writes or prints.
hide = lambda text: text.replace(token, "[CLAUDE_CODE_OAUTH_TOKEN]") if token else text

def save():
    with open(path + ".tmp", "w") as f:
        f.write(hide(json.dumps(entry, indent=2, sort_keys=True)))
    os.replace(path + ".tmp", path)

def pinned_model():
    """The gate review role's model, from its pin (README "Gate roles"): claude on anthropic's TYPE
    VERSION starts as claude-TYPE-VERSION, dots as dashes, as internal/agents' Translate starts it.
    Any other pin, or a further key that changes how the role starts, is refused."""
    with open(os.path.join(spec, "subject-home-config.yaml")) as f:
        block = re.search(r"^  review:\n((?:    .*\n)+)", f.read(), re.M)
    role = dict(re.findall(r'^    (\w+): "?([^"\n]*?)"?$', block.group(1), re.M)) if block else {}
    if sorted(role) != ["agent", "provider", "type", "version"] or (role["agent"], role["provider"]) != ("claude", "anthropic"):
        raise ValueError("the gate's review pin is not claude on one anthropic model: %r" % role)
    return "claude-%s-%s" % (role["type"], role["version"].replace(".", "-"))

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
    rec["result"] = text[-4000:] if isinstance(text, str) else None  # an error too (auth, quota)
    if doc.get("is_error") is not False:
        return "failed"
    fenced = re.fullmatch(r"\s*```(?:json)?\n(.*)\n```\s*", text, re.S)
    said = json.loads(fenced.group(1) if fenced else text, object_pairs_hook=unique)
    if said.get("verdict") not in ("approve", "block") or not isinstance(said.get("findings"), list):
        return "unreadable"
    rec["findings"] = said["findings"]
    return said["verdict"]

def review():
    """One review of rec's tip: its verdict, with what ran and what it said recorded on rec."""
    scratch = tempfile.mkdtemp(prefix="q1-review-")
    try:
        if os.path.commonpath([os.path.realpath(scratch), root]) == root:
            raise ValueError("its HOME %s would be inside the agent's HOME %s" % (scratch, root))
        rec["model"] = pinned_model()
        if not token:
            raise ValueError("CLAUDE_CODE_OAUTH_TOKEN, the reviewer's only credential, is unset or empty")
        tree, home, tar = (os.path.join(scratch, n) for n in ("tree", "home", "tree.tar"))
        os.mkdir(tree), os.mkdir(home)
        run = lambda *a: subprocess.run(a, stdin=subprocess.DEVNULL, capture_output=True, check=True, timeout=limit).stdout
        diff = run("git", "diff", "main..." + rec["new"]).decode(errors="replace")
        run("git", "archive", "--format=tar", "-o", tar, rec["new"])
        run("tar", "-xf", tar, "-C", tree)
        with open(os.path.join(spec, "review-prompt.md")) as f:
            prompt = f.read() + "\n```diff\n" + diff + "\n```\n"
        rec["argv"] = ["claude", "--print", "--output-format", "json", "--model", rec["model"], "--no-session-persistence",
                       "--tools", "Read,Grep,Glob", "--setting-sources", "user", "--strict-mcp-config"]
        env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL") if k in os.environ}
        env.update(HOME=home, TMPDIR=scratch, CLAUDE_CODE_OAUTH_TOKEN=token)
        start = time.monotonic()
        p = subprocess.Popen(rec["argv"], cwd=tree, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, start_new_session=True)
    except (OSError, ValueError, subprocess.SubprocessError) as e:
        shutil.rmtree(scratch, ignore_errors=True)
        rec["reason"] = str(e) if type(e) is ValueError else repr(e)
        return "cannot-start"
    try:
        out, err = p.communicate(prompt.encode(), timeout=limit)
    except subprocess.TimeoutExpired:
        out = None
    with contextlib.suppress(OSError):
        os.killpg(p.pid, signal.SIGKILL)  # the reviewer's whole group, at its end or at the limit
    p.wait()
    rec["duration_seconds"] = round(time.monotonic() - start, 3)
    shutil.rmtree(scratch, ignore_errors=True)
    if out is None:
        return "timeout"
    rec["exit"], rec["stderr"] = p.returncode, err.decode(errors="replace")[-4000:]
    try:
        verdict = answer(out)
    except (ValueError, KeyError, TypeError, AttributeError):
        verdict = "unreadable"
    return "failed" if p.returncode != 0 else verdict

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
        rec = dict(ref, argv=None, model=None, exit=None, duration_seconds=None, result=None, findings=None,
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
    sys.stderr.write(hide("Review rejected this push (%s) of %s:\n%s\n" % (r["verdict"], r["name"], why)))
    sys.exit(1)
