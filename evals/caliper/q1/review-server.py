"""review-server.py SOCKET REQUESTS: the agent-ci-review reviewer process (README "Reviewer process", a#968). run-full.sh starts
one per arm, outside Claude Code, whose tool calls run without CLAUDE_CODE_OAUTH_TOKEN, so the review hook an agent's
push runs never has it. It reads the token from standard input, never from its environment or argv, holds it in memory
only, and hands it to each reviewer on a pipe (CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR), never in an environment or a
file. review-hook.py asks it for one review at a time on SOCKET: the pushed tree, the prompt, the limit and the agent's
HOME. A request chooses no program, model, HOME or environment, so a client can have a review and never the token; the
answer is what ran and what it said, the token's value replaced in both. Every connection it accepts, whoever made it,
appends one line to REQUESTS: when, and the agent HOME it named (null when it named none), which reviewer_requests.py
holds against the hooks' entries (README "Reviewer process"). Once SOCKET answers it prints its pid. SIGTERM,
SIGINT or SIGHUP ends a review in flight with its process group, removes its HOME and SOCKET, and exits."""
import contextlib, json, os, re, shutil, signal, socket, subprocess, sys, tempfile, time

spec, path, requests = os.path.dirname(os.path.abspath(__file__)), sys.argv[1], sys.argv[2]
token = sys.stdin.read().strip()
hide = lambda text: text.replace(token, "[CLAUDE_CODE_OAUTH_TOKEN]")

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

def run(req, scratch):
    """One review of req["tree"] under a new HOME in scratch: what ran and what it said, or why it cannot start."""
    root = os.path.realpath(req["root"])
    if os.path.commonpath([os.path.realpath(scratch), root]) == root:
        raise ValueError("its HOME %s would be inside the agent's HOME %s" % (scratch, root))
    got = {"model": pinned_model()}
    got["argv"] = ["claude", "--print", "--output-format", "json", "--model", got["model"], "--no-session-persistence",
                   "--tools", "Read,Grep,Glob", "--setting-sources", "user", "--strict-mcp-config"]
    home = os.path.join(scratch, "home")
    os.mkdir(home)
    r, w = os.pipe()
    with os.fdopen(w, "w") as f:
        f.write(token)  # far below a pipe's buffer, so this never waits on the reader
    env = {k: os.environ[k] for k in ("PATH", "LANG", "LC_ALL") if k in os.environ}
    env.update(HOME=home, TMPDIR=scratch, CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=str(r))
    start = time.monotonic()
    try:
        p = subprocess.Popen(got["argv"], cwd=req["tree"], env=env, pass_fds=(r,), stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    finally:
        os.close(r)
    try:
        out, err = p.communicate(req["prompt"].encode(), timeout=float(req["limit"]))
    except subprocess.TimeoutExpired:
        out = err = None
    finally:  # also on a signal (SystemExit): the reviewer's whole group, at its end, the limit or teardown
        with contextlib.suppress(OSError):
            os.killpg(p.pid, signal.SIGKILL)
        p.wait()
    got.update(duration_seconds=round(time.monotonic() - start, 3), timeout=out is None, exit=p.returncode)
    if out is not None:  # hidden whole, so a cut the hook makes never leaves a piece of the token
        got.update(stdout=hide(out.decode(errors="replace")), stderr=hide(err.decode(errors="replace")))
    return got

def review(conn):
    """Answers the one request conn carries."""
    conn.settimeout(30)  # a client that sends nothing holds no later review up for long
    scratch, req = tempfile.mkdtemp(prefix="q1-review-"), None
    try:
        req = json.loads(conn.makefile("rb").readline())
        conn.settimeout(None)
        reply = run(req, scratch)
    except (OSError, ValueError, KeyError, TypeError, AttributeError, subprocess.SubprocessError) as e:
        reply = {"error": str(e) if type(e) is ValueError else repr(e)}
    finally:
        shutil.rmtree(scratch, ignore_errors=True)
        with open(requests, "a") as f:  # a log that cannot be written ends this process, so the gap is loud
            f.write(json.dumps({"at": time.time(), "root": req.get("root") if isinstance(req, dict) else None}) + "\n")
    with contextlib.suppress(OSError):
        conn.sendall(json.dumps(reply).encode() + b"\n")

for s in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
    signal.signal(s, lambda *_: sys.exit(0))
if not token:
    sys.exit("review-server.py: no CLAUDE_CODE_OAUTH_TOKEN on standard input")
server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
try:
    server.bind(path + ".new")  # renamed once listening, so SOCKET never exists before it answers
    server.listen()
    os.rename(path + ".new", path)
    print(os.getpid(), flush=True)
    while True:
        conn, _ = server.accept()
        with conn:
            review(conn)
finally:
    for name in (path, path + ".new"):
        with contextlib.suppress(OSError):
            os.unlink(name)
