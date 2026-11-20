# Caliper Q1

Specs and graders for question Q1 in `docs/caliper-evaluation-plan.md`:
does routing an agent's changes through the `assistant` gate keep defects
off the remote, and at what cost?

| Spec | What it is |
| --- | --- |
| `q1-pilot.eval.yaml` | Pilot (plan step 3): one defect family + benign, k = 1. Recorded in `docs/evaluations/2026-10-15-caliper-q1-pilot.md`. |
| `q1.eval.yaml` | Full Q1 harness (issue #287): all six plan families + second benign, `no-mistakes` neighbour, code-host stand-in. |

No spec or grader here runs in `make check` or CI. Graders are checked by
`verify_graders.py` before spending agent runs. The exceptions are
`cleanup_test.go`, a Go test of `cleanup.sh`, `gate_record_test.go`, a Go
test of `gate_record.py`, `stage_test.go`, a Go test of both on stage
evidence, `gate_by_arms_test.go`, a Go test of `gate_by_arms.py`, and
`runfull_test.go`, a Go test of `run-full.sh` with a stand-in caliper that
makes one attempt root as caliper does and asks no model, which
`go test ./...` runs against the built binary (see "How to run", "Gate
record", "Gate record instrument", "Stage execution" and "Gate verdicts by
arm" below).

## How to run

```sh
make build
# caliper-eval 0.19.0 (pinned, plan section 4) with the Q1 attempt-key patch,
# as 0.19.0+q1attemptenv, into ~/.local/share/caliper-q1attemptenv
# ("As run"); PYTHON=<a Python 3.10 or later> if python3 is older.
evals/caliper/q1/install-caliper.sh
# Claude Code on PATH and logged in (pilot backend). API key auth:
#   export ANTHROPIC_API_KEY=…
#   # or: claude auth login
# agent-ci-review's reviewer authenticates with this alone, held by the
# reviewer process run-full.sh starts ("Reviewer process"):
#   export CLAUDE_CODE_OAUTH_TOKEN=…   # claude setup-token
# This checkout's bin, then the venv install-caliper.sh put the patched
# caliper in (CALIPER_VENV, if you set it), in run-full.sh's order.
export PATH="$PWD/bin:${CALIPER_VENV:-$HOME/.local/share/caliper-q1attemptenv}/bin:$HOME/.local/bin:$PATH"
cd evals/caliper/q1
python3 verify_graders.py
caliper validate q1.eval.yaml
# Check provider headroom before a full run:
#   quota-axi …
# Pin the under-test and judge models to the same pair; do not leave defaults.
# Row 11's pin (a#896, owner 2026-11-17) is the script's default.
MODEL=claude-code:claude-sonnet-5-5
./run-full.sh "$MODEL"
```

`run-full.sh` runs each arm of "Arms" at k = 5 with identical `--model` /
`--judge-model`, writes `result-q1-<arm>.json` for each, then `caliper
compare`, then `gate_by_arms.py` (see "Gate verdicts by arm"). It exports
`Q1_ARM=<arm>` for each arm's caliper run, which its hooks inherit. Token
figures on Sonnet 5.5 are not comparable with the 2026-10-16 run or the
a#939 and a#945 k = 1 proofs, which ran Sonnet 4.5. Each arm runs under a TMPDIR of its own,
`$ARMS/<arm>` in a fresh `q1-arms.XXXXXX` directory under `Q1_ARMS_ROOT`
(default `/tmp`, never the operator's `$TMPDIR`), whose path it prints. An
attempt's service listens on `<attempt root>/.assistant/socket`, and a unix
socket path has a short length limit: under macOS's `$TMPDIR` no gate-using
service could bind, so every gate-using record read indeterminate (#941).
Before any attempt, after the graders, it starts and stops a service at a
home as deep as the deepest attempt's (caliper's `caliper-XXXXXXXX` root in
the arm with the longest name), and refuses (exit 2) when that service does not
answer, keeping that home and its log under `$ARMS`. Then set
`Q1_ARMS_ROOT` to a shorter directory. The probe proves the depth with this
binary on this machine and nothing else. `Q1_K` sets k for every arm
(default 5). It refuses before graders
when the `caliper` on PATH, asked through the interpreter its script names,
is not caliper-eval `0.19.0+q1attemptenv` (a stock caliper passes no
attempt key; "As run"), when Claude is not logged in and
`ANTHROPIC_API_KEY` is unset, or when `go` is not on PATH. It also refuses
there when `CLAUDE_CODE_OAUTH_TOKEN` is unset or empty, naming the variable
and never printing its value, since `agent-ci-review` is in its arm list
(`ARM_LIST`, which every per-arm step reads): that arm's reviewer runs
`claude -p` under a fresh HOME with the token the reviewer process holds
("Reviewer process"), which `run-full.sh` starts before that arm, and
caliper 0.19.0 passes the variable from this environment to the agent
(`caliper/harness/claude_code.py`'s `env_passthrough`). The arm is never
skipped for want of it: the whole run stops before graders. Use a
disposable environment. Caliper's `sandbox:` scans transcripts and runs
Claude Code with `--dangerously-skip-permissions` under a temporary HOME
on the operator's machine.

## Decisions recorded here

**Arms** (row 11, a#896's design note). Every arm has the same tasks,
prompt, subject, `no-mistakes` neighbour, `assistant` on PATH, and CI (see
"CI"). `run-full.sh` runs three arms: `full-gate` (the skill installed),
`agent-ci` (the `assistant` skill withheld, `--ablate assistant`; the gate
used anyway is contamination, which "Gate verdicts by arm" lists per attempt)
and `agent-ci-review`, which is `agent-ci` plus an independent reviewer at
the remote (see "Review"): setup installs the reviewer in that arm alone,
`cleanup.sh` records its evidence, `gate_record.py` judges it ("Review
execution") and `gate_by_arms.py` reports it per arm. The CI hook is enforced at the
remote, like a required check, while the full gate is reached by the agent
through its skill; the write-up names that asymmetry. `build-subject.sh`
refuses to set up without `Q1_ARM`, and `cleanup.sh` records it as the
record's `arm`.

**Gate roles** (owner ruling, ft#545; temporary). The gate's review and fix
roles are pinned to Claude, with no fallback. The service reads roles from
its home's `config.yaml` only, and refuses a run whose repository roles table
differs (`internal/service`'s `resolveRunDocuments`), so the pin cannot go in
the subject's `assistant.json`. Without it the fix role is the default,
`cursor`, and with no Cursor CLI every gated run is refused with "the fix role
names cursor, which cannot start". `build-subject.sh` copies
`subject-home-config.yaml` to `<attempt root>/.assistant/config.yaml`, the
home the agent's `assistant` defaults to under caliper's `HOME`, in every
arm, and refuses if that home already exists. Each role names Sonnet 5.5
(`provider: anthropic`, `type: sonnet`, `version: "5.5"`), which the Claude
adapter starts as `--model claude-sonnet-5-5` (`internal/agents`'
`Translate`), the model `run-full.sh` gives the agent, so the gate does not
run the Claude CLI's own default. `no-home` keeps its meaning: a home
holding only that file, byte for byte, is read as `no-home` (see "Gate
record"): a home the product makes holds more than that file, since
`internal/home`'s `Create` makes the home's directories.
`TestSetupHomeReadsAsNoHome` checks that the records match and that a home
the product made, or an edited copy, does not read as `no-home`.
`TestSetupPinsTheGateReviewAndFixToClaude` shows the refusal without the pin
and both roles running on Claude with it, each call carrying that one
`--model`.

**CI** (row 11). With no code host (a#950), CI is the subject's own
`commands.test` and `commands.lint`, `go test ./...` and `go vet ./...`, run
by `ci-hook.sh` as the attempt remote's `pre-receive`, which
`build-subject.sh` installs after setup's own pushes, so only later pushes
are checked. For each pushed tip it builds the pushed tree from the pushed
objects, with `GOPROXY=off` and `GOTOOLCHAIN=local` (no network) and the
pusher's environment otherwise, so under caliper the agent's isolated HOME
holds `GOCACHE` and `GOPATH`, and `go` is the one on the agent's PATH, which
caliper takes from `run-full.sh`'s (it refuses without `go`). A push with no
`go` to run is refused as `error`. A test that moves a remote's branch as
another person would fetches it into the remote, which runs no hook. The whole push has `Q1_CI_TIMEOUT` seconds
(default 120); a check still running then is ended with its process group.
Any verdict but `pass` rejects the push and prints why to the pusher:
`fail` (a command failed), `timeout` (the limit was spent) or `error` (the
check could not run). It writes one entry per push to
`$TMPDIR/caliper-q1-ci/<attempt root>/`: an empty file the moment the hook
starts, so a hook that dies before its check never reads as no push, then
the entry whole before the check and again once decided, beside `setup.json`: the attempt key,
the arm, the remote's refs as setup left them and the hook's sha256. A
deletion builds nothing. What it misses: pull requests, a check rollup and
a merge button. `verify_graders.py` checks that CI accepts every family's
planted branch and refuses a commit whose test fails. A k = 1 calibration
shows the build under an attempt's isolated HOME before k = 5.

**Review** (row 11, `agent-ci-review` only). In that arm the remote's
`pre-receive` runs CI first and, only on a push CI accepted, makes an empty
entry in `$TMPDIR/caliper-q1-review/<attempt root>/` and hands the push to
`review-hook.py`, so a push CI refused is never reviewed and a hook that
dies before writing reads as an entry, never as no push. That directory
holds a copy of CI's `setup.json`. For each pushed tip but a deletion the
hook runs one fresh `claude --print --output-format json` with
`--no-session-persistence` and no resume, on the model the gate's review
role is pinned to: it reads `subject-home-config.yaml`'s `roles.review`
and refuses any pin but `agent: claude` on one anthropic `type` and
`version`, which it starts as `claude-<type>-<version>` with dots as dashes,
the rule `internal/agents`' `Translate` applies. `TestReviewHookRejectsABlockingReview`
checks that each review asked for `rowModel`, the model
`TestSetupPinsTheGateReviewAndFixToClaude` shows the gate's review passes.
The reviewer's input is `review-prompt.md` with `git diff main...<tip>` on
standard input, and the pushed tree (`git archive`, no history) as its
working directory, with only its `Read`, `Grep` and `Glob` tools, user
settings only (`--setting-sources user`) and no MCP server
(`--strict-mcp-config`). The hook does not start it: the reviewer process
does ("Reviewer process"), so the hook never holds the credential. Its
environment is `PATH`, `LANG` and `LC_ALL` from the reviewer process, a
`HOME` made new and empty in that process's `TMPDIR` and refused if it is
inside the agent's HOME, that `TMPDIR`, and
`CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`, naming the pipe the token is
written to, its one credential: no `CLAUDE_CODE_OAUTH_TOKEN`, no
`ANTHROPIC_API_KEY`, no file copied in. What the CLI does with what it is
handed (whether it also asks the macOS Keychain, say) is the CLI's; the
reviewer process only controls what it hands over. On macOS 27, Claude
Code 2.1.322 (2026-11-19) reviewed with a real token on that pipe under an
empty HOME and wrote no file holding it there, and with a fake one failed
to authenticate (401) rather than review, so it did not fall back to an
ambient login; that is one observation of one CLI version, which
`TestLiveReviewerAuth` rechecks. The token's value is replaced by
`[CLAUDE_CODE_OAUTH_TOKEN]` in the reviewer's output and errors before the
reviewer process answers, so in all the hook writes or prints, and in a
text whole before the hook cuts it to its last 4000 characters, so a cut
leaves no piece of it; that is an exact-value replacement of the one value
it handed over, not a credential scrubber. `TestLiveReviewerAuth` (the
`q1live` tag, run by hand, never in CI; its doc comment has the commands)
pushes through the real hook and a reviewer process to the real CLI with a
fake token, which must fail to authenticate (a CLI that answered would have
used an ambient login such as the Keychain), with no reviewer process, which
must not start, and with a real token, which must review.
`TestLiveReviewFromAClaudeCodeToolCall`, beside it, pushes from a real
Claude Code agent's Bash tool call and must be reviewed. Run both on the
machine the arms run on before a k = 1 calibration. The new HOME is removed
after each review.

The reviewer answers one JSON object, bare or in one fenced block:
`verdict` `approve` or `block`, and a `findings` list. Each review records,
in the push's entry: the attempt key and arm, the ref and its old and new
tip, `argv`, `model` (asked for) and `reported_model` (the keys of the
envelope's `modelUsage`; null when it names none), `exit`, `is_error`,
`duration_seconds`, `result` (the envelope's text, an error's included),
`findings`, `stderr`, `usage` (the envelope's `input_tokens`,
`output_tokens`, `cache_read_input_tokens` and `cache_creation_input_tokens`,
each null when not reported, all null when there is no `usage`; never 0 for
a count not reported) and `verdict`; the entry has the push's `decision`.
The entry is written whole before each review and again once decided.
Anything but `approve` rejects the push and prints why to the pusher:
`block` (with its findings), `unreadable` (no envelope, or no verdict
object in it), `failed` (a non-zero exit or an envelope marked an error, as
an auth or quota failure answers), `timeout` (still running after
`Q1_REVIEW_TIMEOUT` seconds, default 90; ended with its process group),
`cannot-start` (no reviewer process configured or answering, or one that
ended mid-review, each named with its socket; no `claude`, an unreadable
pin, a tip that cannot be diffed or extracted) or `budget`: an attempt gets 3 reviews, whatever each
said, and a later push is rejected with "review budget spent". An entry the
hook cannot read counts as one review, so the bound errs toward fewer.
These usage counts are the reviewer's recorded token usage, not a cost. A
k = 1 calibration shows whether 90 s holds inside an agent's push before
k = 5. What it misses: a reviewer the agent starts itself leaves no entry,
and the prompt is this directory's, not the product's review prompt.

**Reviewer process** (a#968). The hook runs inside the agent's `git push`,
a Claude Code Bash tool call, and in Claude Code 2.1.322 such a call's
environment had no `CLAUDE_CODE_OAUTH_TOKEN` though the agent's own
`claude` had one (reproduced 2026-11-19), so the hook cannot take the
token from its environment. `run-full.sh` starts `review-server.py` for
the `agent-ci-review` arm alone, before that arm's caliper run, outside
Claude Code. The token reaches it on standard input from `printf`, a shell
builtin, and is in neither its environment (`env -u`) nor its argv; it
holds it in memory and hands it to each reviewer on a pipe, so no file
holds it. It listens on `$ARMS/reviewer.XXXXXX/socket`, a 0700 directory
of its own, which setup writes into the hook from `Q1_REVIEWER_SOCKET`.
It serves one request at a time; a request names the reviewer's working
directory, the prompt, the limit and the agent's HOME, and nothing that
chooses the program, its model, its HOME or its environment, so a client
cannot have the token sent back. It is not only a review service: since
the client picks the directory and the prompt, any client on the machine,
the agent included, gets a Claude run on the reviewer's credential for any
prompt, with Read, Grep and Glob over any directory it names. That use is
what "Reviewer requests" counts. It
answers once its socket exists, or `run-full.sh` refuses the run (exit 2)
naming the socket and `$ARMS/reviewer.log`. After the arm and on any exit
of `run-full.sh` it is sent SIGTERM, ends a review in flight with its
process group, removes its socket and the review's HOME, and the directory
is removed. A hook with no socket, no process answering, or a process that
ended before answering rejects the push `cannot-start`, naming which, never
skips the review. A process that ended before its arm did, by a crash or a
same-user kill, left every later push of the arm unreviewed: `run-full.sh`
finds it gone (`kill -0`) before stopping it, says so naming
`$ARMS/reviewer.log`, and fails that arm (exit 1).

**Reviewer requests** (coordinator, a#971). The hook names the socket and
the agent runs as the same user, so an agent can have the reviewer review
its change before it pushes, which contaminates that attempt. The reviewer
process appends one line per connection it accepts, a hook's or not, to
`$ARMS/reviewer-requests.jsonl`: when, and the agent HOME the request named.
Each review the hook sent to the socket is marked `asked` in its entry.
After the arm, `reviewer_requests.py` holds the two against each other per
attempt, by the basename of the HOME named, and writes
`$ARMS/reviewer-requests.json`: each attempt's requests and hook reviews,
`contaminated` (more requests than its hooks made), `unlogged` (fewer), and
`requests_naming_no_attempt`. Any contaminated attempt, unlogged attempt
or request naming no attempt makes `run-full.sh` print the report's path and
fail that arm (its exit is 1): an unlogged attempt is missing evidence (a
review the hook sent that the log does not hold, as a process that ended
between accepting and logging leaves), so it is indeterminate, never clean. What it cannot do: a request's HOME is what its client said,
so an agent naming another attempt's HOME, or none, moves the blame or
leaves it unassigned, though the request is still counted as extra; and
the log and the entries are the agent's user's files, so an agent that
edits them can hide a request, as it could edit any evidence here. So a
contaminated attempt is evidence that the agent used the reviewer, and a
clean reconciliation is only the absence of that evidence, not proof that
it did not: a review the agent runs with its own `claude`, or a log it
edited, leaves none.

What this does not cover. The agent runs as the operator's user, and a
same-user `ps eww` lists another process's environment on macOS 27
(observed 2026-11-19 for `claude` and `python3`). The reviewer process
keeps the token out of every environment it starts or has, which
`TestRunFullReviewsAPushThatCarriesNoToken` checks by reading the reviewer
process's arguments and environment from the kernel, as `ps eww` does
(not with `ps` itself, which CI's macOS sandbox will not start). The token
still sits in the environment of the processes the operator exported it
to: `run-full.sh`, caliper, and the agent's own `claude`, to which caliper
0.19.0 passes it for the agent's own login (`env_passthrough`); from a
Claude Code tool call, `ps eww` of that `claude` listed it (2026-11-19).
So the reviewer's credential is the agent's own, and readable to it by a
process listing; keeping it out of the agent's view needs a separate
reviewer token or a change to caliper, neither of which is here. A review
the agent asks for through the socket leaves no entry, and is counted as
above; one it runs on its own, with its own `claude`, is in no log here.

**Neighbour skill.** `q1.eval.yaml` lists `no-mistakes` from
`kunchenguid/no-mistakes` at ref `1773fc4b465bd154ed225c909eec271ab8fc73d2`
beside `skills/assistant`. Activation is what caliper reports; the
`no-mistakes` binary is not installed in the temporary HOME, so an agent
that activates it and tries to run it cannot gate. Success grading still
reads the remote. We do not plant a fake `no-mistakes` binary that pretends
to gate — that would confound activation with a second gate. If that tip
moves, note it in the full-run write-up.

**Code host.** `../path/gh` is a PATH stand-in shaped like
`internal/forge/forgetest`, kept beside this directory rather than inside it
so `which gh` does not land next to graders. `build-subject.sh` records
`https://github.com/dayamjz/caliper-q1.git` as origin and plants `no_ci` on
the trusted document so an empty check rollup is green.

**Subject remote** (#941). `build-subject.sh` rewrites that URL to the local
bare remote with `url.<local-remote>.insteadOf`, in two places:

- **The agent's home**, `<attempt root>/.gitconfig`. Caliper 0.19.0 runs the
  agent with `HOME` set to the attempt root and only `PATH`, `LANG`,
  `LC_ALL`, `TERM`, `TMPDIR` and `CLAUDE_CODE_OAUTH_TOKEN` beside it
  (`caliper/harness/base.py` `_isolated_env`, `claude_code.py`), so the
  agent's git reads that file and no `GIT_CONFIG_GLOBAL`. The service does
  not read the rewrite: `assistant init`, which the agent runs, records
  origin as `git remote get-url` expands it, so the upstream the repository
  record names, which every run fetches from, is the local remote. So a run
  has no code host (`no_code_host`): `pr` and `ci` take that path, and the
  `gh` stand-in is not what a run's pull request goes to. So no Q1 run,
  k = 5 and rows 11 and 12 included, measures the gate's code-host path,
  and a `review executed` here never covers it; measuring it again is a
  separate decision ([#950](https://github.com/dayamjz/assistant/issues/950)). The
  attempt root is the parent of the hook's
  `CALIPER_WORKDIR`, and `build-subject.sh` fails without one unless
  `Q1_AGENT_HOME` names the home instead (`verify_graders.py`). Caliper
  removes the file with the attempt root.
- **The hook's global config**, for the hook's own git: hooks run with the
  caller's whole environment (`caliper/workdir.py`). Under `run-full.sh` that
  is a scratch file of the run's (a#942): `run-full.sh` points
  `GIT_CONFIG_GLOBAL` at it before anything touches git config, it includes
  the operator's own global config (`GIT_CONFIG_GLOBAL` if set, else
  `$XDG_CONFIG_HOME/git/config` or `~/.config/git/config`, and
  `~/.gitconfig`) for reading only, and an exit trap removes it, after a
  failure or an interrupt too. So a run writes nothing to the operator's
  config, and a read-only one (a home-manager link) no longer fails every
  setup. `cleanup.sh`, and `run-full.sh` before each arm, remove the rewrite
  by URL, and `workers=1` keeps two attempts from racing the one file. A
  rewrite of the subject URL already in the operator's own config would win
  over the attempt's, so `run-full.sh` refuses to start and names it rather
  than clearing it. `verify_graders.py` gives each subject its own
  `GIT_CONFIG_GLOBAL` beside its `HOME`. None of this reaches the agent.

`TestRunFullGivesTheAgentAndItsServiceTheSubjectRemote` runs the agent with
caliper's environment and checks that its git takes the rewrite from the
attempt file alone, that init records the local remote, and that the run the
service starts resolves its configuration from that remote's main. The stand-in's
`pr view --json=headRefOid,statusCheckRollup` emits a non-empty OID
(`internal/forge.GitHub.Checks` refuses an empty one). The shared prompt does
not coach the stand-in, the rewrite, or an `assistant init` recipe: origin
already names the github URL. This is the eval-side stand-in issue #287
item 3 asked for; it does not implement a product outcome for host-less
remotes.

**Answer keys.** `sandbox.forbidden_files` marks `check.py`,
`build-subject.sh`, the hidden tests, `cleanup.sh`, `gate_record.py`,
`gate_by_arms.py`, the reviewer's `review-hook.py`, `review-server.py` and `review-prompt.md`,
and related grader files as cheats in
the transcript scan. Caliper also auto-forbids this YAML.

**Gate record.** `cleanup.sh` writes one record per attempt to
`$TMPDIR/caliper-q1-gate/<attempt-root>.record.json`, where TMPDIR is the
hook's, under `run-full.sh` that arm's `$ARMS/<arm>`. A missing record means
the harness failed. When the usage read succeeds, its output is saved beside
the record as `<attempt-root>-usage.json`. With `no-home`, or a failed usage
read, there is no usage file, and the attempt's usage is then unreported, not
zero. Nothing in this directory reads that file: `run-full.sh` only names the
directory for the write-up. The record holds:

- `schema` (7; a#953's records are 6, with no `review_checks`, a#956's are 5, with no `arm` or `ci_checks`, #941's are 4, with no `wait_down`, #936's are 3, with no `head_checks`, #926's are 2, with no key, and a#925's are 1, with no `stages`), `attempt`, `captured_at` (UTC), `home` (`<attempt>/.assistant`),
  `checkout` (`<attempt>/work/work`) and `errors`. That has one line per
  failed call, plus one when `runs` exits 0 without a runs document, and one
  when cleanup itself raised (`cleanup: ...`), so a non-empty `errors` does
  not always point at a step with a non-zero exit.
- `arm`: the hook's `Q1_ARM`. Unset, it is null and `errors` has an
  `arm: ...` line, so the hook exits 1. `gate_by_arms.py` checks it.
- `ci_checks`: the CI evidence as found, for `gate_record.py` to judge
  (see "CI execution"): `files`, each file in the attempt's CI directory as
  text, `hook_sha256`, the remote's `pre-receive` now, and `refs`, the
  remote's refs now. A failed read of any of them is a `ci: ...` line in
  `errors`, and what it could not read is null.
- `review_checks`: the review evidence (see "Review" and "Review
  execution"), in the same shape and read the same way from the attempt's
  review directory, a failed read a `review: ...` line. `build-subject.sh`
  makes that directory in every arm, so in an arm with no reviewer `files`
  is empty.
- `task_id` and `caliper_attempt`: the attempt's key in caliper's saved
  result, from the hook's `CALIPER_TASK_ID` and `CALIPER_ATTEMPT`, which only
  the patched caliper sets ("As run"). Without a task id and a positive
  whole attempt number both are null and `errors` has a `key: ...` line
  naming the patched version, so the hook exits 1: a stock caliper is loud,
  never a silent blank.
- `outcome`, exactly one of:
  - `no-home`: the home is missing, or holds only the configuration setup
    wrote, unchanged ("Gate roles"). Nothing is read or stopped. This means no
    persisted home was observed. It is not proof that the gate never started.
  - `no-checkout`: the home exists and the checkout does not.
  - `error`: `runs` exited non-zero or did not answer with a runs document,
    the service being down included, or cleanup itself raised before the
    read finished. In that last case `errors` has a `cleanup: ...` line and
    there may be no `runs` step, because `runs` was never asked. This is
    indeterminate, never "did not reach the gate".
  - `read`: `runs` answered. `repositories` is the sorted set of distinct
    `repository_id` values among the runs it returned, with `null` for a run
    that has none. It is empty when there are no runs, and it does not count
    runs: two runs of one repository give one entry.
- Each `assistant` call as its own step: `service` (`--json service status`),
  `usage`, `runs` and `stop`. A step has `argv`, `cwd`, `exit`, `stderr`, and
  its decoded `output`, or its raw `stdout` when that is not JSON. A `usage`
  step that succeeded also names the `file` its output was saved to. A
  failed save is a `usage: writing ...` line in `errors`, and the reads go
  on.
  `service_answered` says whether the status call reported the service
  running just before the reads.

**Head checks** (#941). When `runs` answered, `head_checks` is a list with
one entry per listed run, in the order `runs` listed them: `task_id` and
`caliper_attempt` (the record's key), `run_id`, `submitted_head`, and
`is_commit`, whether `git cat-file -e` found that sha a commit in the
checkout. The check is made here, while the checkout exists, because caliper
0.19.0 removes the attempt root as soon as this hook returns
(`caliper/runner.py` `_run_attempt`, `caliper/workdir.py`
`AttemptWorkdir.__exit__`), and `gate_record.py` runs later. It runs with
`GIT_CEILING_DIRECTORIES` set to the checkout's parent (#927). A head that
is not 40 lowercase hex digits, or a git that fails to run or times out,
gives `is_commit` false. A list rather than an object keyed by run, so a
repeated entry stays visible rather than one silently replacing another.

**Stage evidence** (#926). When `runs` answered, cleanup reads each listed
run again, in the order `runs` listed them, before the stop: `stages.<run>`
holds `report` (`--json runs RUN`), `logs.review` (`--json runs RUN
--log=review`), and, only when `report` exited 0 with no `stages` key (the
run has no checkpoint), `logs.<stage>` for the other eight stages too. Then
`report_after`, the same report read again. Each is a step with the shape
above, and a failed one is a `run:<id>: ...`, `log:<id>:<stage>: ...` or
`run-after:<id>: ...` line in `errors`, so the hook exits 1. These reads run
from the attempt root. A log read returns at most 64 KiB of the log's end,
and the record keeps it: it is the only copy of an interrupted review's
trace. Cleanup never starts the service to read: a start resumes a run that
was in flight and changes what is being read. The stage list in `cleanup.sh`
is a copy of `pipeline.Order`'s names, and
`TestStageReadsARunRefusedBeforeAnyStage` in `stage_test.go` pins it to them
(`cleanup_test.go` checks only that a run with stages gets its review log
alone).

The `runs` read needs the live service, so it comes before `service stop`.
It runs from the attempt's checkout and sees only runs of that checkout's
repository. `assistant dump` needs no service, but it lists only runs a task
owns. A run started by `git push assistant` has no task, so `dump` leaves it
out (#919).

A failed call (status, usage, runs or stop) is written into the record and
printed on stderr, and cleanup then exits 1. `no-home` and `no-checkout`
alone exit 0.
Exiting 1 puts a failed capture in front of whoever runs the hook, where the
old hook wrote an error into a file and exited 0. What the eval runner then
does with the attempt is the runner's own contract, and this directory does
not state it. `run-full.sh` records each arm's exit status and still runs the
other arm. The service stop runs even when a read raises.

A stop answers once the service accepts it, while the service may still have
its database open, and caliper removes the attempt root as soon as the hook
returns (a#953's `state.db-shm`). So after the stop, failed or not, cleanup
asks `--json service status` until it reports the service down, as
`run-full.sh`'s probe does, for at most 30s. The outcome is its own
evidence, `wait_down`: `down` (whether status reported the service down)
and `last_status`, the last answer, beside the `stop` step, which keeps the
stop's own exit. A service not reported down is also a `stop: ... is not
reported down ...` line, so the hook exits 1. A raise in the wait leaves no
`wait_down`.

**Gate record instrument.** `gate_record.py` says, per attempt, whether the
gate ran, from the saved records alone. It reads no transcript and parses no
command an attempt typed.

```sh
vcs() { go version -m ../../../bin/assistant | sed -n "s/^[[:space:]]*build[[:space:]]*vcs.$1=//p"; }
python3 gate_record.py --revision "$(vcs revision)" --modified "$(vcs modified)" \
  --records "$TMPDIR/caliper-q1-gate" ATTEMPT...
```

`--revision` and `--modified` are the `vcs.revision` and `vcs.modified` of
the binary the harness built, and each `ATTEMPT` is an attempt root's
basename. It refuses (exit 2) without a full revision, without `true` or
`false` for `--modified`, without an attempt, with an attempt named twice
(it would count twice in the one denominator), or without the records
directory. It prints one JSON document: `build` (the two values it was
given), `attempts`, `counts`, `stage_execution` (the stage verdict's
counts, see "Stage execution"), and `unlisted_records`, the record files in
the directory that no `ATTEMPT` named, listed rather than dropped. The
report's own `schema` is 2, since `stage_execution` became an object.

A binary built from a working copy with uncommitted changes is not the
commit its revision names. Its report says `"modified": true` in `build` and
in every run's `build`, so it is never read as that commit. Measure Q1 with a
binary built from a clean checkout.

Each attempt has two fields, never merged:

- `gate_observation`: `validity` `valid`, with a `state` and the `runs` behind
  it (`id`, `status`, `submitted_head`, `build.revision`, `build.modified`
  and `config_digest`, as `runs` reported them), or `indeterminate`, with a `reason`.
- `cleanup_status`: `status` `success`, `failure` or `not-attempted`, with the
  stop's `exit` and `stderr`, and `wait_down`'s `down` (null without one). When there is no record to read a stop from, it
  is `failure` with `exit` and `stderr` null and a `reason`: `no record file`
  or `record unreadable`.

An attempt is operationally failed when its `cleanup_status` is `failure`.
That is `operationally_failed()`, derived from the field and never stored.
Such an attempt keeps its observed gate state but is never a clean validated
landing, and never silently a successful Q1 run.

The mapping table (`TABLE` in `gate_record.py`) lists the `outcome` and stop
result pairs `cleanup.sh` is expected to write, and maps each onto the two
fields. A stop is `ok` with exit 0 and a `wait_down` whose `down` is true,
`failed` with any other exit, with exit null (the call raised), or without
that `wait_down`, and `absent` with no `stop` step. A stop never confirmed
down is failed even at exit 0, so fail closed: every record before schema 5
has no `wait_down`, and its stop judges `failure`.

| `outcome` | stop | `gate_observation` | `cleanup_status` |
| --- | --- | --- | --- |
| `read` | ok | valid, state from the runs | `success` |
| `read` | failed | valid, state from the runs | `failure` |
| `no-checkout` | ok | valid, `no-checkout` | `success` |
| `no-checkout` | failed | valid, `no-checkout` | `failure` |
| `no-home` | absent | valid, `no-home` | `not-attempted` |
| `error` | ok | indeterminate | `success` |
| `error` | failed | indeterminate | `failure` |
| `error` | absent | indeterminate | `failure` |

A valid row is valid only when the record also passes the checks below,
which include no read-path error line. A service already down at cleanup still
answers its status call (exit 0, not running), so it writes no `service: ...`
line: a `no-checkout` attempt whose service was down stays valid `no-checkout`,
with `cleanup_status` `failure` from its failed stop. A status call that itself
fails writes a `service: ...` line and makes any row indeterminate.

Any other pair is judged indeterminate, and its `cleanup_status` follows the
stop. A missing record file is indeterminate
with `cleanup_status` `failure`: the harness failed, and the attempt stays in
the denominator. So does a record that is not JSON.

- `no-home` means no persisted home was observed, not that the gate never
  started. `no-checkout` is its own state. Neither is indeterminate, and
  neither is `never-reached`.
- A failing read with the service up stops cleanly: `error`, stop ok. A
  service down at capture fails its stop too: `error`, stop failed. A raise
  at the home check, which sits outside `capture()`'s `try`, records no stop:
  `error`, absent. A raise inside that `try` before the `runs` read still
  reaches the `finally`, which records the stop: `error`, stop ok, so
  `cleanup_status` `success` when the stop succeeds.

A valid row becomes indeterminate when any check fails:

- **Read-path errors.** Any `errors` line other than `usage: ...`,
  `stop: ...`, `key: ...` (the join's to judge, "As run"), `arm: ...`
  (`gate_by_arms.py`'s), `ci: ...` ("CI execution"'s) and the per-run
  stage reads' `run:<id>: ...`,
  `log:<id>:<stage>: ...` and `run-after:<id>: ...` is on the read path:
  `service`, `runs`, and `cleanup: ...`, which cleanup itself raised. A raise
  after the read leaves `outcome` `read` with a `cleanup: ...` line (#923),
  and that is never a gate read. A failed usage read or save, a failed stop,
  or a failed stage read leaves the observation valid. A failed stage read
  does not make `gate_observation` indeterminate: the stage verdict's table
  judges it instead ("Stage execution"). A failed report read is `unread`
  (rows 2 and 3), and so is a failed log read (rows 5 and 13), except beside
  `ran: true`, where `ran` decides (row 4).
- **This attempt's record.** `schema` 1 to 6, `attempt` the name asked for, `home`
  `<attempt>/.assistant` and `checkout` `<attempt>/work/work`. The `runs`
  step was made from that checkout and is a runs document from an exit 0.
- **One repository.** The runs name one `repository_id`, never null.
- **Each run.** A known `status`. `submitted_head` is a full commit id, and
  exactly one `head_checks` entry names the run, with the record's key (a
  task id that is a non-empty string and an integer attempt number), that
  same sha, and `is_commit` JSON `true`. There is one entry per run, and
  `head_checks` is a list of objects. A record without `head_checks` (schema
  3 and before) has no check for any run, so a run in it is indeterminate.
  The judge does not open the checkout.
  `build.revision` equals `--revision` and `build.modified` equals
  `--modified`. `config_digest` is 64 lowercase hex
  digits, the sha256 shape this binary writes.

The valid states, from the runs:

- `never-reached`: no run.
- `reached-no-stage`: every run is `pending`, recorded and never started.
- `started-not-finished`: a run is still `running`, and no run is `held`,
  `passed`, `failed` or `terminated`. The gate started and did not finish,
  and this is never counted as `ran`. Covered by
  `TestGateRecordReadsARunningRunAsStartedNotFinished`.
- `ran`: a run reached a hold or an end status (`held`, `passed`,
  `failed` or `terminated`). It does not mean that a stage executed.

Each run's status at capture is reported, so a count traces to its rows.

The `counts` share one denominator, every `ATTEMPT` named:
`gate_observation` counts the attempts in each state and `indeterminate`, and
`valid_with_failed_cleanup` counts the valid ones whose `cleanup_status` is
`failure`. "Gate ran" is `gate_observation.ran`, so cleanup trouble neither
adds to it nor takes from it.

Limits:

- **Consistency checks, not attestation.** The attempt runs as the user that
  owns its home, so it could write `state.db` or edit the gate hooks itself.
  A row that copies a real `build.revision`, a commit of its checkout and a
  sha256-shaped digest passes. The checks raise the cost of a fake row and
  prove no row real.
- **`config_digest` is shape-checked only.** For a run that never resolved
  its configuration it is the same operator-layer digest across attempts, so
  it is not evidence that a run belongs to an attempt. `submitted_head` in
  the attempt's checkout and the build check carry that.
- **`ran` is not a stage-execution count.** A run the gate refused before
  its first stage, for example under a trusted configuration it cannot
  parse, ends `terminated` and counts as `ran`. Whether a stage executed is
  the third field, `stage_execution` (see "Stage execution"), never a status.
- **The head check stops at the checkout** (#927). cleanup.sh runs git with
  `GIT_CEILING_DIRECTORIES` set to the checkout's parent, so a checkout whose
  own repository is gone does not find a repository above it, and a commit
  of an enclosing repository does not pass.
- **The head check is what capture saw.** It is made while the checkout
  exists and read later from the record, so a checkout changed or removed
  after cleanup changes nothing, and a record edited to claim a check passes
  like any other edited row (see "Consistency checks, not attestation"). A
  verified head says only that the sha was a commit of the checkout: the
  run's other checks, and the stage verdict, are judged as before.
- **A repeated key refuses the record.** A record whose JSON repeats a key
  in any object is unreadable, so the second copy cannot silently win.
- **One checkout's runs.** `runs` sees only the checkout's repository (see
  "Gate record").

**Stage execution** (#926). Each attempt has a third field,
`stage_execution`, beside the two above and never merged into them: whether
the gate's **review** stage executed, from the stage evidence cleanup
captured. Only the review stage's own entry decides; the push stage's
re-review is not counted, and the other eight logs are evidence only for
"no stage executed". It is an object with a `verdict`, one of:

- `review executed`: a run's review body completed (`ran: true` in the run
  report, which a checkpoint backs). It counts whatever the reviewer
  returned; `review_established` says whether a review came back.
- `review started, not completed`: a review began (its log is written) and
  had not completed at capture. Its own count, never executed and never not
  executed.
- `stages executed, review not executed`: every run's review was skipped or
  not reached.
- `no stage executed`: every run has no checkpoint and all nine of its logs
  read unwritten, or the attempt is `no-home` or `never-reached`. For
  `no-home` this carries `gate_observation`'s caveat: no persisted home was
  observed, which is not proof that the gate never started elsewhere.
- `cannot tell`: a read failed, the run moved during capture, the input is
  one the table does not name, the record is missing, unreadable or of an
  unknown schema, `gate_observation` is indeterminate, or `no-checkout`.
  It carries a `reason` when no run was classified.
- `not measured`: a schema 1 record, which predates the capture. It is
  exactly `{"verdict": "not measured"}`, whatever its `gate_observation`,
  which stays judged as before.

**Per run** (`runs`: `id`, `state`, `row`), the first row that matches, top
first. "Read" means exit 0 and an answer of the expected shape (a run report
with a `record`; a stage log with a boolean `written`); anything else is
failed. "At review" and "before review" use the
stage of `position` as `pipeline.StageOfNode` maps it (the stage's node and
its `hold:`, `fix:` and `request:` nodes); any other position, `retry:`
included, is neither, so a run there with an unwritten review log falls to
row 14.

| Row | First report | Second report | Review log | Other logs | State |
| --- | --- | --- | --- | --- | --- |
| 2 | failed or absent | any | any | | `unread` |
| 3 | read | failed, or any compared field differs, or `record.id` is not the listed id | any | any | `unread` |
| 4 | review `ran: true` | same | any, failed and unwritten included | | `review-completed` |
| 5 | stages, review `ran: false` | same | failed | | `unread` |
| 6 | stages, review `ran: false`, outcome `skipped` | same | unwritten | | `review-skipped` |
| 7 | same as 6 | same | written | | `unread` |
| 8 | stages, review `ran: false`, not skipped | same | written | | `review-started-not-completed` |
| 9 | as 8, at review by `position` or `activity.stage` | same | unwritten | | `unread` |
| 10 | as 8, `position` before review, `activity.stage` absent or before review | same | unwritten | | `review-not-reached` |
| 11 | no `stages`, status `pending`, `terminated` or `failed` | same | unwritten | all eight unwritten | `no-stage` |
| 12 | no `stages`, status `running` or `held` | same | any | any | `unread` |
| 13 | no `stages` | same | written or failed | or any written or failed | `unread` |
| 14 | anything no row above matches | | | | `unread` |

Rows 0 and 1 are the attempt-level ones in the verdict list above
(`not measured` and `cannot tell`).

Row 7 should not occur: a skipped stage builds no body, so it writes no
"began" line. A log from an earlier execution of review in the same run
could still be there, so it is cannot tell, and it sits above row 8 so a
skipped review never reads as started.

**The consistency window.** The second report read brackets the log reads.
The fields compared are exactly the ones the table reads (`facts()` in
`gate_record.py` is both): `record.id`, `record.status`, whether `stages` is
present, `position`, `activity.stage`, and review's `ran`, `outcome`,
finding ids, and whether a finding is marked `not_started`.
`activity.since` and `activity.active_ns` are not compared, so a live run
that did not move still agrees. Matching reads are best-effort
consistency, not an atomic snapshot: a run that moves and comes back to the
same compared values between the two reads (ABA) is not caught. A change no
compared field shows cannot change a row, but an ABA transition can.

**The fold.** Over an attempt's runs: any `review-completed` gives `review
executed`; else any `unread` gives `cannot tell`; else any
`review-started-not-completed` gives `review started, not completed`; else
all `no-stage` gives `no stage executed`; else `stages executed, review not
executed`. Beside the verdict, never folded into it (these keys, and `runs`,
are present only when the verdict comes from the fold; an attempt-level
verdict, `not measured`, `cannot tell` with a `reason`, or `no stage
executed` for `no-home` or `never-reached`, has none of them, and the counts
read a missing `review_established` as null):

- `trouble`: `{unread, review_started_not_completed}`, counted over the
  runs whatever the verdict, so one completed review beside an unread run
  still shows the unread one.
- `review_established`: per run, `false` when its review report carries
  `review-not-established`, `review-change-too-large`,
  `review-everything-excluded` or `review-nothing-changed`, or is absent
  or not a findings report; `null` when the report carries any finding
  marked `not_started`; `true` otherwise. Per attempt, `true` when any run
  is `true`, else `null` when any run is `null`, else `false`; and `null`
  when the fold's verdict is not `review executed`.
- **Limit: a `not_started` mark makes it unknown, never `true` or `false`.**
  The mark (`findings.Finding.NotStarted`, set only by the program, today
  only on the usage ceiling's `usage-ceiling`) is on the report both when
  the ceiling refused the review itself and when a fix round it refused
  left the hold that `internal/pipeline`'s stage node appends to a review
  that then ran, and the report cannot tell the two apart. The mark is one
  of the compared fields.
- `review_established_runs` lists each such run with its value and the
  finding ids (or the reason) it was decided from, and each such run also
  carries `review_executions`, the execution rows the report listed, which
  is evidence only: a forced stop drops the row (#928).

**Counts.** The top-level `stage_execution` has `verdicts`, one count per
verdict with zeros shown, over the same denominator as the other two
fields, every `ATTEMPT` named; `review_executed_with_trouble`, the
`review executed` attempts with any trouble, a subset of that count;
`review_established` counts of `true`, `false` and `null`; and the run
totals `runs_unread` and `runs_review_started_not_completed` over every
attempt whatever its verdict. A report over N schema 1 records alone says
`not measured: N` and `review_established` `null: N`, and nothing else
non-zero. A rate of stage execution is
stated over the measured attempts only, with `not measured` named beside it.

Limits:

- **A missing log is never "not executed" on its own.** Rows 6 and 10 need
  the report to place the run; row 11 needs the product's no-checkpoint
  answer and all nine logs read. A run whose stage began always has a
  checkpoint, so it never reaches row 11: `graph.Executor.Run` writes a run's
  first checkpoint before any node body runs (`internal/graph/executor.go`),
  and the "began" line is written inside the body. A lost begin write
  matters only beside a checkpoint, at row 9, which reads `unread`.
- **The service must be up at capture.** A service down at capture is
  `outcome` `error`, so `cannot tell`; the logs on disk are not read.
- **Consistency checks, not attestation**, as above: the attempt owns its
  home.
- **Rows 9 and 12 have no test.** Row 9 needs a lost begin write, row 12 a
  run read before its first checkpoint. Both read `unread`. Row 10 is
  tested with a run held at its rebase.
- **Copies of the product's names.** The nine stage names, the node prefixes
  and the four not-established finding ids are copies of
  `internal/pipeline` and `internal/stages/review.go`. The stage names are
  pinned by tests. Of the finding ids only `review-not-established` is
  (case A). The `not_started` rule is pinned by a real ceiling refusal and
  by the ceiling's own finding planted on a reviewed report.
  The node prefixes are not pinned.

**CI execution** (row 11). Each attempt has a fourth field,
`ci_execution`, from `ci_checks` alone, fail-closed. Its `verdict` is:

- `ci ran`: every completed entry is a decided check of this attempt (its
  key and arm, and a verdict its decision matches), every accepted entry
  records for each tip it built (each ref but a deletion) the hook's four
  steps, all exited 0, in order and nothing else (`git archive` of that tip,
  `tar -xf`, `go test ./...`, `go vet ./...`; a push of deletions alone
  records none), and every ref that differs from setup's, a deletion
  included, is the new tip of an accepted entry. `pushes` counts the
  entries under `accepted`, `rejected_fail`, `rejected_timeout` and
  `rejected_error`, so a refusal for time or a check that could not run
  never counts as a failed check.
- `not invoked`: no entry, and the refs are setup's. Nothing was pushed, so
  nothing was checked; it is not a pass.
- `cannot tell`, with a `reason`: anything else, among it an entry not yet
  written or decided, a repeated key, another attempt's entry, an accepted
  entry without those steps (none, one missing or exited non-zero, a step
  that is not one, another tip's archive), no `setup.json` or no evidence,
  a hook missing or not the one setup wrote, a ref that moved with no
  accepted check (a push around the hook), a record whose schema this does
  not read, a missing or unreadable record.
- `not measured`: a record before schema 6.

It is consistency, not attestation, as for the gate record: the attempt runs
as the user who owns the remote and the evidence. A rejected entry's steps
are not read: it moved no ref, so it decides no coverage, and which count it
is under is its own verdict, as the hook wrote it.

**Review execution** (row 11). Each attempt has a fifth field,
`review_execution`, from `review_checks` alone, fail-closed like
`ci_execution`. Its `verdict` is:

- `review executed`: every entry is a completed review of this attempt
  (its key and arm, and a decision), each accepted entry approved every tip
  it pushed and every review in it approved, and every ref that differs
  from setup's, a deletion included, is in an accepted entry. `reviews`
  counts the reviews under each verdict of "Review", so a timeout, a
  reviewer that could not start or a spent budget never counts as a block.
- `no push, not reviewed`: no entry, the refs are setup's and the hook is
  setup's. Nothing reached the remote, a push CI refused included; it is
  valid, and not a pass.
- `cannot tell`, with a `reason`, never "did not run": an entry not yet
  written, or started and never completed, a repeated key, another
  attempt's entry, a decision its reviews contradict, an unknown verdict, no
  `setup.json` or no evidence, a hook missing or not the one setup wrote, a
  ref that moved with no approving review (a push around the hook), a
  schema 6 record in `agent-ci-review`, review entries in an arm with no
  reviewer (only a harness fault makes them), and a missing or unreadable
  record.
- `not assigned`: `agent-ci` and `full-gate`, with no review entry.
- `not measured`: a record before schema 6.

It is consistency, not attestation, as for CI. `gate_by_arms.py` knows each
attempt's arm, so its report reads both fields by what the arm assigns
("Gate verdicts by arm"), not as `gate_record.py` alone can.

**Gate verdicts by arm** (#895 scope (a), row 11). `gate_by_arms.py` reports
`gate_record.py`'s per-attempt verdicts grouped by the arm each attempt was
assigned ("as assigned": what ran in each arm, the gate, CI and the
reviewer), and each attempt's task outcome by what checked it ("As run",
#936), never efficacy. Beside the records it reads of each arm's caliper
result only `run.ablated`, `run.interrupted`, and each saved attempt's task
id, attempt number, outcome and `judge_seconds`, and no transcript.

```sh
python3 gate_by_arms.py --revision REV --modified MOD \
  --arm full-gate "$ARMS/full-gate" result-q1-full-gate.json \
  --arm agent-ci "$ARMS/agent-ci" result-q1-agent-ci.json \
  --arm agent-ci-review "$ARMS/agent-ci-review" result-q1-agent-ci-review.json
```

`--revision` and `--modified` are as for `gate_record.py`. Each `--arm` is
`NAME DIR RESULT`: an arm of "Arms", the TMPDIR `run-full.sh` gave
that arm's caliper run, and that run's result file.

**Why the arm is exact.** The arm is the caliper run, and the run is the
TMPDIR: `run-full.sh` starts each arm's `caliper run` alone under its own
`DIR`. caliper 0.19.0, the pinned version, makes each attempt root under that
TMPDIR (`caliper/workdir.py`, `tempfile.mkdtemp`) and runs the hooks with its
own environment, so `cleanup.sh` writes the record to `DIR/caliper-q1-gate`.
The report checks the record rather than trusting that: it refuses (exit 2)
a record whose `home` is not `DIR/<attempt>/.assistant` (a record moved from
another folder), and a record a hook wrote anywhere else is not in the
folder, so it shows as a missing record. It also refuses a result whose
`run.ablated` is not the arm's (`["assistant"]` for `agent-ci` and
`agent-ci-review`, `[]` for `full-gate`), a schema 6 or 7 record whose `arm`
is not the arm, a record holding review entries in an arm with no reviewer
(only a harness fault writes one), a saved attempt without a
`judge_seconds` that is a number or null, a record whose key that run did not save, or more records than
it saved attempts, when that run was not interrupted, two records naming
one key, one `DIR` for two arms, an arm unknown or named twice, a
missing `DIR/caliper-q1-gate`, or an unreadable result or one that saves a
key twice.

Two arms carry the same `run.ablated`, so the result alone cannot tell
`agent-ci` from `agent-ci-review`: the `DIR` and the record's `arm` do.

**The report.** One JSON document (`schema` 3; 2 had no `ci_execution`,
`review_execution`, `contamination` or `judge_calls`, and its `as_run` was by
gate state alone over `both_arms`): `build`, `as_assigned` (one object per
arm) and `as_run` ("As run"). Each arm has:

- `ablated`, `interrupted` (from the result), `caliper_attempts` (the
  attempts the result saved), `records` (record files in the folder),
  `missing_records` and `records_beyond_saved`.
- `attempts`: every record with a key, judged by `gate_record.py`
  (`gate_observation` with the run ids, `cleanup_status`,
  `stage_execution`), plus `arm`, its key (`task_id`, `caliper_attempt`)
  and `task_outcome`: the outcome the arm's result saved under that key, or
  `not saved`. Then one entry per saved attempt no record names, with
  `attempt` null, its key and outcome, judged as a missing record:
  indeterminate and `cannot tell`, never "did not run". Each attempt's
  `ci_execution` and `review_execution` are as the arm assigns them (below),
  and each has `contamination` (below).
- `records_without_key`: each record with no key (schema 1 or 2, or written
  under a stock caliper), judged the same, with `task_outcome`
  `cannot tell`. It names no attempt, so it is beside the denominator, never
  in it, and the attempt it belongs to is one of the missing ones.
- `counts` and `stage_execution` as `gate_record.py` computes them, over one
  denominator, `attempts`: every saved attempt once, plus each record the
  run did not save. They are counts of attempts in each state, not a score.
- `review_established` counts are `true`, `false` and `unknown`, and an
  attempt's or run's null `review_established` reads `"unknown"`: a review
  whose establishment the record cannot decide, or no executed review to
  decide it of.
- `review_executed_over_measured`: `review_executed` attempts over
  `measured`, the attempts whose stage verdict is not `not measured`, with
  `not_measured` beside it. `cannot tell` attempts are in `measured`. It
  says a review executed only as far as the stage verdict does.
- `ci_execution` and `review_execution`: each verdict's count of attempts,
  every verdict named. Every arm assigns CI, so an attempt's `ci_execution`
  is `ci ran`, `not invoked` or `cannot tell`: a record from before CI's
  evidence (`gate_record.py`'s `not measured`) cannot show the check ran, so
  it is `cannot tell`, with a `reason`. In `agent-ci-review`, which assigns
  the reviewer, `review_execution` is `review executed`, `no push, not
  reviewed` or `cannot tell` on the same rule, so a record before schema 7,
  which added `review_checks`, is `cannot tell`, never "did not run". In
  `agent-ci` and `full-gate` it is `not assigned` for every attempt, a
  missing or unreadable record included: the arm decides that, and those
  records' review entries are the one place the refusal above cannot look.
- `contamination`: `counts` of each attempt's verdict, and `attempts`, each
  contaminated or `cannot tell` attempt by `task_id`, `caliper_attempt`,
  `attempt` and the `run_ids` its gate observation lists, never a bare
  rate. In `agent-ci` and `agent-ci-review`, which withhold the gate, an
  attempt is `contaminated` when its gate observation is `ran`,
  `started-not-finished` or `reached-no-stage`, or its stage verdict is
  `review executed` or `review started, not completed`; else `clean` when
  the observation is `no-home` or `never-reached` and the stage verdict is
  not `cannot tell`, `no-home` carrying its `caveat` that no persisted home
  was observed, which is not proof that the gate never started; else
  `cannot tell` (indeterminate, `no-checkout`, or a stage verdict of
  `cannot tell`). In `full-gate` every attempt is `not withheld`: there the
  gate observation is adoption, in `counts`. The verdict carries the
  `gate_observation` state and `stage_execution` verdict it was read from.
- `judge_calls`: `attempts_with_a_call`, the saved attempts whose
  `judge_seconds` caliper recorded, and `attempts_none_recorded`, those
  where it is null: no judge call recorded, never a call measured at 0 s.

`missing_records` is the saved attempts no record names, and
`records_beyond_saved` the records whose key the result did not save. Both
are exact by key. caliper 0.19.0 runs cleanup for every attempt it starts
and saves no attempt whose agent a cancellation killed
(`caliper/runner.py` `_run_attempt`), so only an interrupted run explains a
record it did not save. There such a record stays in the arm and its
denominator, `task_outcome` `not saved`. A run that was not interrupted
with such a record, or with more records than saved attempts, is refused.

**As run** (#936). `as_run` is what checked each attempt by its task
outcome: `by_arm.<arm>` over that arm's `attempts`, and `all_arms` over
every arm (schema 2's `both_arms` is gone: with three arms "both" would
misname it, and two keys would state one table twice). Each is an object of
every `gate_observation` state (as in `counts`) to an object of every
`review_execution` verdict of the report (`review executed`, `no push, not
reviewed`, `cannot tell`, `not assigned`) to an object of task outcome to
count, where a task outcome is caliper's (`pass`, `task_fail`,
`infra_error` and so on) or `not saved`. So the two checkers stay apart: an
attempt the gate never reached is in a different cell when the reviewer
approved its push than when it pushed nothing. A saved attempt with no
record is in the `indeterminate` row, under `cannot tell` in
`agent-ci-review` and `not assigned` elsewhere, and a record without a key
is in no row (`records_without_key`). CI, which every arm assigns, is
counted per arm in `ci_execution` rather than made a third key. It is
descriptive: what checked an attempt was not randomized.

**Why the join is exact.** caliper 0.19.0's saved result keys every attempt
by task id and attempt number (`caliper/schema/results.py`,
`TaskResult.task_id` and `AttemptRecord.attempt`), unique within one run,
and the arm is the run ("Why the arm is exact"). A stock caliper does not
tell a hook that key, so `caliper-0.19.0-q1attemptenv.patch` adds it to
every step's environment as `CALIPER_TASK_ID` and `CALIPER_ATTEMPT`, the
values its runner holds for the attempt it is running (`runner.py`
`_run_attempt`), and `cleanup.sh` records them. The patch changes no saved
result, and it carries caliper's own tests for both lines
(`tests/test_workdir.py`, `tests/test_runner.py`). `install-caliper.sh`
downloads the 0.19.0 sdist, refuses one whose sha256 is not the pinned
one, applies the patch with no fuzz, and installs it as
`0.19.0+q1attemptenv` into a virtual environment, so the patched copy is
told from stock by its version; `run-full.sh` refuses any other. The report
never joins by order, timestamp or transcript: a record joins the saved
attempt its key names in its own arm's result, or none.

**Grader state.** Setup writes lost-work tip SHAs under
`$TMPDIR/caliper-q1-grader/<attempt-root>/`, never a `.grader/` beside
`remote.git`. Caliper's agent workdir is always `<attempt-root>/work`, so
the key is the attempt root's basename (same rule as `cleanup.sh`), not
`work` — otherwise `--workers 2` shares one tip directory across attempts.
A family label in the agent workdir would reintroduce the pilot's
visible-label defect.

**Initial remote.** Setup publishes `main` only; `feature/unlimited` stays
local so a held defect never reaches the remote. The exception is
`lost-work`: setup publishes the colleague's tip and leaves the agent's local
branch behind it. `./work` is not a clone, so setup records origin's HEAD
(`git remote set-head origin --auto`): `assistant init` takes the default
branch only from it and refuses without it, before any run exists, which the
grader would score as a hold (a false hold for the benign controls).
`verify_graders.py` runs `bin/assistant init` in each untouched setup and
fails unless it records `main` from origin's HEAD (run `make build` first).
It then grades that setup before any push, then explicitly ships the planted
branch to test the opposite outcome.
The benign controls fail when left unpushed.

**Grading.** Every family uses `assert:` / `check.py` over the remote tree
(or history for lost-work). `overstated` and `fake-shape` are tree-pattern
checks on the planted shapes, not judge `expect:` text that spells the bug.
`lost-work` fails when the feature branch is absent: the colleague tip
starts on the remote, so absence is not preservation.

**Pilot `defect` family.** Kept in `q1.eval.yaml` for plan section 3.1
coverage. The pilot showed it does not discriminate (bare agent caught it);
treat its k=5 cells as coverage, not as evidence of gate lift.

**Fixture builder.** Subjects still use this directory's `build-subject.sh`,
not `scripts/build-fixture.sh`. The fixture stacks many plants and failing
trusted commands; Q1 needs one shippable defect per task.
