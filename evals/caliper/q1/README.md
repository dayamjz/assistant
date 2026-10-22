# Caliper Q1

Specs and graders for question Q1 in `docs/caliper-evaluation-plan.md`:
does routing an agent's changes through the `assistant` gate keep defects
off the remote, and at what cost?

| Spec | What it is |
| --- | --- |
| `q1-pilot.eval.yaml` | Pilot (plan step 3): one defect family + benign, k = 1. Recorded in `docs/evaluations/2026-10-15-caliper-q1-pilot.md`. |
| `q1.eval.yaml` | Full Q1 harness (issue #287): all six plan families + second benign, `no-mistakes` neighbour, code-host stand-in. |

Nothing here runs in `make check` or CI. Graders are checked by
`verify_graders.py` before spending agent runs.

## How to run

```sh
make build
pip install --user 'caliper-eval==0.19.0'   # pinned (plan section 4)
# Claude Code on PATH and logged in (pilot backend). API key auth:
#   export ANTHROPIC_API_KEY=…
#   # or: claude auth login
export PATH="$HOME/.local/bin:$PWD/bin:$PATH"
cd evals/caliper/q1
python3 verify_graders.py
caliper validate q1.eval.yaml
# Check provider headroom before a full run:
#   quota-axi …
# Pin the under-test and judge models to the same pair; do not leave defaults.
MODEL=claude-code:claude-sonnet-4-5
./run-full.sh "$MODEL"
```

`run-full.sh` runs both arms at k = 5 with identical `--model` /
`--judge-model`, writes `result-q1-with-skill.json` and
`result-q1-ablated.json`, then `caliper compare`. It refuses before graders
when Claude is not logged in and `ANTHROPIC_API_KEY` is unset. Use a
disposable environment. Caliper's `sandbox:` scans transcripts and runs
Claude Code with `--dangerously-skip-permissions` under a temporary HOME
on the operator's machine.

## Decisions recorded here

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
so `which gh` does not land next to graders. `build-subject.sh` sets a global
`url.<local-remote>.insteadOf` for `https://github.com/dayamjz/caliper-q1.git`
(and records that URL as origin), and plants `no_ci` on the trusted document
so an empty check rollup is green. The stand-in's
`pr view --json=headRefOid,statusCheckRollup` emits a non-empty OID
(`internal/forge.GitHub.Checks` refuses an empty one). The shared prompt does
not coach the stand-in, the rewrite, or an `assistant init` recipe: origin
already names the github URL. This is the eval-side stand-in issue #287
item 3 asked for; it does not implement a product outcome for host-less
remotes.

**Answer keys.** `sandbox.forbidden_files` marks `check.py`,
`build-subject.sh`, the hidden tests, and related grader files as cheats in
the transcript scan. Caliper also auto-forbids this YAML.

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
branch behind it. `verify_graders.py` grades each untouched setup before any
push, then explicitly ships the planted branch to test the opposite outcome.
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
