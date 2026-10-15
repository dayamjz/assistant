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
export PATH="$HOME/.local/bin:$PWD/bin:$PATH"
cd evals/caliper/q1
python3 verify_graders.py
caliper validate q1.eval.yaml
# Check provider headroom before a full run:
#   quota-axi …
caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 2 --judge-model <pinned>
caliper run q1.eval.yaml --k 5 --timeout 1200 --workers 2 --judge-model <pinned> --ablate assistant
caliper compare <results A> <results B>
```

Use a disposable environment. Caliper's `sandbox:` scans transcripts and
runs Claude Code with `--dangerously-skip-permissions` under a temporary
HOME on the operator's machine.

## Decisions recorded here

**Neighbour skill.** `q1.eval.yaml` lists `no-mistakes` from
`kunchenguid/no-mistakes` at ref `1773fc4b465bd154ed225c909eec271ab8fc73d2`
beside `skills/assistant`. Activation is what caliper reports; the
`no-mistakes` binary is not installed in the temporary HOME, so an agent
that activates it and tries to run it cannot gate. Success grading still
reads the remote (or the judge for `expect:` tasks). We do not plant a fake
`no-mistakes` binary that pretends to gate — that would confound activation
with a second gate.

**Code host.** `bin/gh` is a PATH stand-in shaped like
`internal/forge/forgetest`. `build-subject.sh` sets a global
`url.<local-remote>.insteadOf` for `https://github.com/dayamjz/caliper-q1.git`.
The prompt tells the agent to pass that URL as `assistant init --upstream
…`, because a plain origin read follows the rewrite and leaves the pull
request stage with no repository (#283). This is the eval-side stand-in
issue #287 item 3 asked for; it does not implement a product outcome for
host-less remotes.

**Fixture builder.** Subjects still use this directory's `build-subject.sh`,
not `scripts/build-fixture.sh`. The fixture stacks many plants and failing
trusted commands; Q1 needs one shippable defect per task.
