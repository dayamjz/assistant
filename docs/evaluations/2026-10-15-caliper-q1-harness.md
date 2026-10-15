# Caliper Q1 harness, 2026-10-15

This continues step 3 of `docs/caliper-evaluation-plan.md` into the harness
issue #287 asked for, building on the pilot in
`docs/evaluations/2026-10-15-caliper-q1-pilot.md` and PR #284. It is not a
measurement of Q1: no agent run was spent here. It records the specs,
graders, neighbour decision, and code-host stand-in so a full run at k = 5
can start without redesigning the subject.

## What was added

| | |
| --- | --- |
| Spec | `evals/caliper/q1/q1.eval.yaml` |
| Caliper | `caliper-eval` 0.19.0 (validated; not run with agents) |
| `assistant` | built from this branch with `make build` |
| Families | defect, benign, overstated, fake-shape, boundary, lost-work, benign-max |
| Neighbour | `no-mistakes` from `kunchenguid/no-mistakes` @ `1773fc4b465bd154ed225c909eec271ab8fc73d2` |
| Code host | `evals/caliper/q1/bin/gh` + `url.<local>.insteadOf` for `https://github.com/dayamjz/caliper-q1.git` |
| Grader check | `python3 evals/caliper/q1/verify_graders.py` — all assert outcomes pass |

Each assert family was checked for both held and shipped outcomes before any
agent run. `overstated` and `fake-shape` are `expect:` (judge) tasks; the
assert path refuses them so a tree check cannot silently pass them.

## Decisions

**How a `no-mistakes` attempt is graded when it cannot run in the temporary
HOME.** The neighbour is listed so caliper's activation report shows which
skill an agent reached for. The `no-mistakes` binary is not installed in
Caliper's temporary HOME. An agent that activates it and tries to drive it
will fail to gate; success grading still reads the remote (or the judge).
No fake `no-mistakes` binary is planted, because that would measure a second
gate rather than activation. Recorded in `evals/caliper/q1/README.md`.

**Code host without waiting on #283.** Gated runs get a PATH `gh` stand-in
shaped like `internal/forge/forgetest`, and subjects rewrite
`https://github.com/dayamjz/caliper-q1.git` to the local bare remote. The
prompt requires `assistant init --upstream https://github.com/dayamjz/caliper-q1.git`,
because a plain origin read follows the rewrite and leaves the pull request
stage with no repository — the stall #283 describes. This is an eval-side
stand-in; it does not choose a product outcome for host-less remotes.

**Defects that do not announce themselves.** The pilot's empty-limit default
sat under a package comment that forbade defaults, and the bare agent caught
it. The new families keep nearby comments and tests agreeing with the
planted surface:

- **overstated:** `Decide`'s doc claims an ancestor check; the body always
  returns true; tests only cover the equal-tip path.
- **fake-shape:** `Guard` looks real; its failing test constructs a `Ref`
  `ParseLine` cannot produce.
- **boundary:** `internal/db` still claims SQL ownership; `reports` quietly
  opens the database.
- **lost-work:** the work clone stays at the agent's tip while a colleague
  commit is already on the remote feature branch; grading asks whether that
  colleague tip remains an ancestor.

## What this does not cover

- **No full Q1 agent run.** This environment has no Claude Code credentials
  and no provider quota check. Issue #287 items 4–5 — k = 5 both arms with
  `--judge-model` pinned, in a disposable environment, and the write-up with
  figures — remain. Projection from the pilot is still about 11M agent
  tokens plus gate usage from the cleanup files.
- **Plan section 3.3's token assumption stays falsified** until a full run
  adds the cleanup usage files into the reported cost. The pilot record now
  names that disagreement explicitly.
- **#283 is open.** Host-less remotes still stall in product; only this
  eval subject avoids that path.
- **Planted defects are ours**, as plan section 4 warns.

## Next

1. Confirm provider headroom (`quota-axi`).
2. Run `q1.eval.yaml` at k = 5 both arms with `--judge-model` pinned, in a
   disposable environment.
3. Write the run up as a new `docs/evaluations/` entry answering Q1 with
   limits, every figure traceable to a checked-in results file. Then Q3,
   then Q2, per plan step 4.
