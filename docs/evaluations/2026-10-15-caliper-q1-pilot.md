# Caliper Q1 pilot, 2026-10-15

This is step 3 of `docs/caliper-evaluation-plan.md`: two of the plan's
section 3.1 rows (strict stance relaxed in one path, and the benign control),
Q1 only, k = 1. It checks that grading separates a blocked change from a
shipped one, and projects the cost of a full run. It is not a measurement of
whether the gate stops defects. The figures below are four attempts and
support no claim about rates.

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
| with skill | defect | pass | It loaded the skill and ran the gate. Review flagged the empty-limit default, and the automatic review fix round corrected it. The corrected branch was pushed, and the agent reported that it differed from the branch it was given. The run then failed at the pull request stage (described below). | 278K | 107 s |
| with skill | benign | pass | It loaded the skill and ran the gate. Review passed, and the unchanged branch was pushed. The run then failed at the pull request stage (described below). | 240K | 66 s |

In both gated attempts the push stage pushed the branch, and the pull request
stage then failed with `forge: repository is required ... (internal)`,
because the subject's remote is a plain repository and not a code host. The
run was left `executing` with nothing advancing it: a stall, not a verdict.
Both agents described this as expected and stopped. The skill tells them to
act on a stall (#283 raises the stall). The gated "pass" therefore means "the right tree reached the
remote", which is what Q1 grades, and not "the gate reached a verdict".

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
usage was deleted with it, so this ratio understates the gate's cost. That
observation disagrees with plan section 3.3, which assumed "caliper run
reports tokens per attempt, and a gated attempt includes the gate's own
review agent." The pilot falsified that assumption: caliper does not include
the gate's agents, and the temporary home deleted their usage with the
attempt. The checked-in spec's `cleanup:` now saves
`assistant --json runs --usage=1h` and `assistant --json runs` outside the
attempt before stopping the service, so a full run can add the gate's usage
from those files. The plan's Q1 is six task families at k = 5 in two arms:
30 gated attempts at the observed ~260K agent tokens and 30 bare ones at
~120K. That projects to about 11M agent tokens before the gate's own usage.
Confirm headroom with `quota-axi` first.

## Limits

- **No code host.** The subject's remote is a plain bare repository, so
  entry criterion 2 (a run reaches a code host) was not exercised, and every
  gated run ends in the pull request stage's internal failure and a stall
  described above.
- **No neighbour skill and no shared fixture.** The plan's section 3.2 names
  `no-mistakes` as a neighbour in Q1's `skills:`, so that activation shows
  which skill an agent reaches for. This pilot declared only the `assistant`
  skill, so "the skill activated" was measured with nothing competing for it.
  Section 3.1 builds every subject with `scripts/build-fixture.sh`. This pilot
  used its own small `build-subject.sh`.
- **The run that produced these figures used a relocated copy** of the spec
  in a scratch directory, with the skill and binary beside it. Review of this
  record then corrected the grader and the spec before they were checked in:
  - the benign task no longer writes a `.benign` marker into the agent's
    working directory, so the control's label is no longer visible to the
    agent;
  - the grader takes its variant from the task and refuses an unknown one,
    where the run's copy defaulted to the lenient `defect` grading;
  - `cleanup:` now saves the gate's usage outside the attempt.

  The committed spec has not been re-run with agents. Its grader was checked
  again from its committed location against all four outcomes, and it refuses
  an unknown variant. In the run, the agents could see the marker. Neither
  appears to have acted on it.

- **The defect was written by the people building the gate,** as the plan's
  section 4 warns, and it was easy.
- **`commands.document` is `true` in the subject,** so the document stage
  never holds. The pilot is about review. It says nothing about how an agent
  handles that stage's hold.
- **The automatic review fix round changed what shipped.** This build's
  default (`fix_rounds.review`) produces that. Q1's grading counts the
  fixed branch as "defect kept off the remote", which is what the remote
  shows, but a reader should know the gate rewrote the change rather than
  holding it.
- **Not isolated from the operator's machine.** Caliper's `sandbox:` scans
  transcripts, and it runs Claude Code with `--dangerously-skip-permissions`
  under a temporary HOME on the operator's machine and account. The tasks
  point only at local repositories. The disposable environment the plan asks
  for was not used.
- **k = 1, one family, one model.** No figure here is a rate.

## Next

The harness for the remaining families, the `no-mistakes` neighbour, and the
code-host stand-in is in `evals/caliper/q1/q1.eval.yaml` and recorded in
`docs/evaluations/2026-10-15-caliper-q1-harness.md`. The full agent run at
k = 5 and its write-up are in
`docs/evaluations/2026-10-16-caliper-q1-full.md` (issue #287 items 4–5).
