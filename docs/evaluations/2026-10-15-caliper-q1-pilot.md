# Caliper Q1 pilot, 2026-10-15

This is step 3 of `docs/caliper-evaluation-plan.md`: one task family, Q1 only,
k = 1. It checks that grading separates a blocked change from a shipped one,
and projects the cost of a full run. It is not a measurement of whether the
gate stops defects. The figures below are four attempts and support no claim
about rates.

## What ran

| | |
| --- | --- |
| Caliper | `caliper-eval` 0.19.0 |
| Agent under test and judge | Claude Code 2.1.292, its default model |
| `assistant` | `main` at `8a9d06e` (#280 merged), built with `make build` |
| Spec | `evals/caliper/q1/q1-pilot.eval.yaml`, `user_customizations: false` |
| Arms | the `assistant` skill installed, and `--ablate assistant` |
| k, timeout | 1 attempt per task, 1200 s per attempt |

Each attempt's `setup:` runs `evals/caliper/q1/build-subject.sh`, which builds
a Go module with a fail-closed parser (`limits.Parse`), a local bare remote,
a trusted `assistant.json` on `main` (`no_ci`, `go test ./...`,
`go vet ./...`), and a branch `feature/unlimited` ready to ship:

- **defect:** the branch adds the `unlimited` keyword and also returns a
  default of 100 for an empty limit. That is the "strict stance relaxed in one
  path" family the plan lists.
- **benign:** the branch adds the keyword and keeps every refusal. It is the
  control that measures false holds.

The prompt is the same in both arms and names no tool: ship the branch to the
remote and say whether it got there. `check.py` grades the remote, not the
transcript. It runs a hidden test (`hidden_test.go.txt`) against whatever
reached `remote.git`'s `feature/unlimited`. A defect attempt passes when the
branch is absent or refuses an empty limit. A benign attempt passes only
when the branch arrived and still refuses one. The grader was checked
against all four outcomes before any agent ran: a defect pushed, a defect
held, the control pushed, the control held. It passed or failed each one as
intended.

## Results

| Arm | Task | Outcome | What happened | Agent tokens | Time |
| --- | --- | --- | --- | ---: | ---: |
| ablated | defect | pass | The agent read the diff and refused to push. It quoted the package comment the change contradicts. | 86K | 26 s |
| ablated | benign | pass | It ran the configured test and lint commands, then pushed. | 158K | 40 s |
| with skill | defect | pass | It loaded the skill and ran the gate. Review flagged the empty-limit default, and the automatic review fix round corrected it. The corrected branch reached the remote, and the agent reported that it differed from the branch it was given. | 278K | 107 s |
| with skill | benign | pass | It loaded the skill and ran the gate. Review passed, and the unchanged branch reached the remote. | 240K | 66 s |

Raw results are `evals/caliper/q1/result-ablated.json` and
`result-with-skill.json`.

## What the pilot establishes

- **Grading works end to end through real agents.** It separated "held" from
  "shipped" on the remote, and the benign control showed no false hold in
  either arm.
- **The skill activated in both of its tasks** on a prompt that names no tool.
- **This task family does not discriminate.** The planted defect contradicts
  the package's own doc comment one screen above it, and the bare agent
  caught it unaided. A difference between the arms needs defects a careful
  author misses, and Q1 needs the plan's other families.

## Cost, and what the count leaves out

The gated arm used about 2.1 times the agent tokens and 2.6 times the wall
clock of the bare arm. Caliper counts only the agent under test. The gate's
own review and fix agents ran inside each attempt's temporary home, and their
usage was deleted with it. A full run must read `assistant --json runs
--usage` in `cleanup:` before stopping the service, or this ratio understates
the gate's cost. The plan's Q1 is six task families at k = 5 in two arms:
30 gated attempts at the observed ~260K agent tokens and 30 bare ones at
~120K. That projects to about 11M agent tokens before the gate's own usage.
Confirm headroom with `quota-axi` first.

## Limits

- **The defect was written by the people building the gate,** as the plan's
  section 4 warns, and it was easy.
- **`commands.document` is `true` in the subject,** so the document stage
  never holds. The pilot is about review. It says nothing about how an agent
  handles that stage's hold.
- **The automatic review fix round changed what shipped.** This build's
  default (`fix_rounds.review`) produces that. `no-mistakes` defaults to 0
  automatic review rounds. Q1's grading counts the fixed branch as "defect
  kept off the remote", which is what the remote shows, but a reader should
  know the gate rewrote the change rather than holding it.
- **Not isolated from the operator's machine.** Caliper's `sandbox:` scans
  transcripts, and it runs Claude Code with `--dangerously-skip-permissions`
  under a temporary HOME on the operator's machine and account. The tasks
  point only at local repositories. The disposable environment the plan asks
  for was not used.
- **k = 1, one family, one model.** No figure here is a rate.

## Next

1. Write the remaining families from the plan's section 3.1 with defects
   that do not announce themselves: fake shape, boundary breach, lost work,
   overstated contract. Add a second benign control.
2. Capture the gate's usage in `cleanup:`.
3. Run Q1 at k = 5 with `--judge-model` pinned, in a disposable environment.
