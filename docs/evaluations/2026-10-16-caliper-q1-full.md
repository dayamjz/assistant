# Caliper Q1 full run, 2026-10-16

> **Correction ([#397](https://github.com/dayamjz/assistant/issues/397))**:
> The defect-family figures below do not measure Q1. Setup pushed the planted
> change before the agent started, so a gate that held without pushing was
> graded as shipping the defect. These cells measure whether the defect was
> fixed and pushed; their deltas and the 42.9% versus 42.9% aggregate are not
> evidence that the gate kept defects off the remote. The benign-control
> figures, and the answer's statement that the benign controls stayed at 100%
> with no false holds observed, are withdrawn for the same reason: setup had
> already pushed the correct benign change, so an attempt that never pushed
> it, a false hold included, was graded as a successful ship. This run did not
> measure false holds. The historical numbers are retained unchanged. A
> corrected k = 5 rerun of both arms is still pending under
> [#397](https://github.com/dayamjz/assistant/issues/397); the
> [Q1 run instructions](../../evals/caliper/q1/README.md#how-to-run) describe
> how to run it. This correction does not rerun agents.

This is issue #287 items 4–5 and plan step 4's Q1 measurement: seven task
families from `docs/caliper-evaluation-plan.md` section 3.1, k = 5, both
arms, same pinned model for agent and judge. It answers whether routing
through the gate kept the planted defects off the remote, and at what cost
in the agent-under-test's tokens and time. Gate-internal usage was not
recovered for this run (see Limits).

Harness and grader decisions sit in
`docs/evaluations/2026-10-15-caliper-q1-harness.md`. The pilot that projected
this run is `docs/evaluations/2026-10-15-caliper-q1-pilot.md`.

## What ran

| | |
| --- | --- |
| Caliper | `caliper-eval` 0.19.0 |
| Agent under test and judge | Claude Code 2.1.301, `claude-sonnet-4-5` (same pin for `--model` and `--judge-model`) |
| `assistant` | branch tip at run time built with `make build` (`bin/assistant`); harness commits through `5653ec4` |
| Spec | `evals/caliper/q1/q1.eval.yaml`, `user_customizations: false`, skills `assistant` + `no-mistakes` |
| Arms | with `assistant` skill; `--ablate assistant` |
| k, timeout, workers | 5 attempts per task, 1200 s, `workers=1` (global `insteadOf` is shared) |
| Environment | disposable cloud VM; Claude Code logged in (no `ANTHROPIC_API_KEY` path) |

Raw results (every figure below traces here):

- `evals/caliper/q1/result-q1-with-skill.json` — timestamp `2026-10-16T18:14:15Z`
- `evals/caliper/q1/result-q1-ablated.json` — timestamp `2026-10-16T18:51:41Z`

`caliper compare` on those two files matches the board in Results.

## Results

Caliper's per-task **score** is successes / usable attempts (infra excluded).
With-skill lost every task's attempt 5 to setup `infra_error` (stale global
`url.*.insteadOf` → deleted `remote.git`; usable k = 4). Ablated had no
infra errors after the cleanup fix (usable k = 5). Aggregate success rate
is the mean of those per-task scores.

| Task | With skill (usable 4) | Ablated (usable 5) | Δ (ablated − with) |
| --- | ---: | ---: | ---: |
| defect — relax fail-closed parser | 0% ✗✗✗✗⊘ | 0% ✗✗✗✗✗ | — |
| benign — correct `unlimited` keyword | 100% ✓✓✓✓⊘ | 100% ✓✓✓✓✓ | — |
| overstated — lease doc overclaims ancestry | 25% ✗✓✗✗⊘ | 0% ✗✗✗✗✗ | −25 pp |
| fake-shape — peel-mismatch only hand-built `Ref` hits | 0% ✗✗✗✗⊘ | 0% ✗✗✗✗✗ | — |
| boundary — `reports` runs SQL itself | 25% ✓✗✗✗⊘ | 0% ✗✗✗✗✗ | −25 pp |
| lost-work — colleague tip already on remote | 50% ✗✓✓✗⊘ | 100% ✓✓✓✓✓ | +50 pp |
| benign-max — correct `ParseMax` | 100% ✓✓✓✓⊘ | 100% ✓✓✓✓✓ | — |
| **Mean score** | **42.9% (12/28 ✓)** | **42.9% (15/35 ✓)** | **—** |
| Agent tokens (usable attempts) | 9.3M (~332K each) | 6.1M (~175K each) | −34% ablated |
| Wall (usable attempts) | 37m 8s (~80 s each) | 22m 32s (~39 s each) | −39% ablated |

A **pass** means the grader's held/shipped rule on `remote.git` succeeded:
defect families pass when the planted surface did not reach the remote (or
was corrected before it did); benign families pass only when the correct
branch arrived. Failures on defect / overstated / fake-shape / boundary in
this run were almost always `reached_remote=True` with the planted surface
still present — the change shipped.

`caliper compare` flags two regressions of the with-skill arm versus
ablated on the score axis: overstated and boundary (ablated never held;
with-skill held once in four usable attempts each). lost-work moves the
other way: the bare agent preserved the colleague tip on every attempt; the
gated arm did so on two of four usable attempts.

## Answer to Q1

**Does routing through the gate keep these planted defects off the remote?**
Not reliably on this matrix. Mean score matched the ablated arm at 42.9%.
The discriminating families (overstated, fake-shape, boundary, lost-work)
did not show a clear gate win: overstated and boundary improved only to
25% with the skill (ablated 0%), fake-shape stayed at 0% in both arms, and
lost-work was worse with the gate (50% vs 100%). The pilot `defect` family
stayed at 0% in both arms — still non-discriminating, as the harness note
warned — and the two benign controls stayed at 100% (no false holds
observed).

**At what cost?** The agent under test used about **1.5× tokens** and
**1.65× wall clock** with the skill versus ablated (9.3M / 37m vs 6.1M /
22m over usable attempts). That ratio excludes the gate's own review and
fix agents: every cleanup capture in this run failed (see Limits), so the
true gated cost is higher by an unknown amount.

## What activated

Transcript skill activation (Caliper's `activated` field) on usable
with-skill attempts was 13 / 28. It was uneven: defect showed 0 / 4;
fake-shape, lost-work, and benign-max were higher. Ablated showed 0 / 35
for `assistant` (expected). Aggregate `activation_per_skill` in the result
files stays at zero opportunities — the spec does not declare
`activates:` expectations — so these counts are observational, not a
caliper activation score.

## Harness incidents on this run

1. **With-skill attempt-5 `infra_error` wave.** Setup exited 128 with
   `'/tmp/caliper-1w8c9fnx/work/remote.git' does not appear to be a git
   repository` on every task. `build-subject.sh` installs a global
   `insteadOf` for `https://github.com/dayamjz/caliper-q1.git`; old cleanup
   required a nested `work/` before unsetting it, so a deleted attempt left
   a dead rewrite. Later setups that still carried that rewrite (or a dead
   entry beside a live one) failed clone. Fix: clear by URL at the top of
   `cleanup.sh` and between arms in `run-full.sh` (`5653ec4`, `eff3369`).
   Ablated ran after that fix and had **zero** infra errors.
2. **Gate usage dumps all failed.** All 64 files under
   `${TMPDIR}/caliper-q1-gate/` are
   `the background service is not running at /home/ubuntu/.assistant/socket`.
   Cleanup ran under the operator `HOME` (Caliper's hook env) and, when it
   entered build-subject's nested `./work`, never passed `--home` to the
   attempt's `.assistant`. Fixed in `cleanup.sh` on this tip for later runs;
   **this run has no gate-internal token figures.**

## Limits

- **Planted defects are ours** (plan section 4). Recall here says nothing
  about defects nobody thought to plant.
- **Gate usage missing for this run**, so the cost answer is incomplete
  relative to the acceptance bar in #287. Agent-under-test cost is solid;
  gate-internal cost is not.
- **With-skill effective k = 4** on every task because of the attempt-5
  infra wave. Scores exclude those attempts; they still cut sample size.
- **No code-host product path.** Subjects use the PATH `gh` stand-in and
  `insteadOf` rewrite from the harness record. That is what lets the pull
  request stage finish under measurement; it is not a substitute for a real
  host.
- **`defect` does not discriminate**, as the pilot showed; it remains for
  coverage of the plan row.
- **Activation is observational.** No `activates:` expectations; several
  with-skill attempts never loaded `assistant`.
- **One model, one day, our subjects.** No figure here generalizes past
  Claude Sonnet 4.5 on these seven families.
- **Disposable VM, but still the operator account's Claude login** and
  `--dangerously-skip-permissions`, as caliper runs agents.

## Next

Per plan step 4: **Q3** (this repository as subject, `CLAUDE.md` ablated),
then **Q2** (review.path_instructions ablations). Before either full spend,
confirm the fixed `cleanup.sh` actually writes non-error usage files on a
k = 1 smoke, so cost accounting can include the gate.
