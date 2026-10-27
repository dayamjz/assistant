# Caliper evaluation of assistant: plan

Status: entry criteria met; pilot recorded
(`docs/evaluations/2026-10-15-caliper-q1-pilot.md`); full Q1 harness checked
in under `evals/caliper/q1/` (`docs/evaluations/2026-10-15-caliper-q1-harness.md`).
The full agent run at k = 5 is still outstanding (issue #287). Q4 has a
one-case pilot from the fleet's replay benchmark
(`docs/evaluations/2026-10-27-caliper-q4-replay-pilot.md`), run before its
harness is ported. Nothing here runs in `make check` or CI.

## 1. What caliper measures, and why it fits

`edonadei/caliper` runs a real agent CLI (Claude Code, Codex, Pi or Hermes)
on a fixed set of tasks, k times each. It runs them with and without a given
skill, MCP server or user layer, and reports:

- the single-run success rate,
- whether the agent chose the skill (activation), scored separately from
  success,
- token and wall-clock cost.

`--ablate <skill>` re-runs without the skill, and `caliper compare` shows the
difference.

This answers four questions that nothing in this repository measures today.
Q1 to Q3 are about `assistant` itself. `internal/journey/README.md` states the
gap in its first limit: green there "is not evidence the gate catches bugs".
Q4 is about the agents that fix what review finds, the role that
`assistant`'s fix rounds and task workers fill. It measures which agent setup
turns a failing review into a passing one at the least cost, so that each new
model release can be tested on real work before it becomes a default.

| # | Question | What gets ablated |
| --- | --- | --- |
| Q1 | Does routing an agent's changes through the `assistant` gate stop defects from reaching the remote, and at what cost in tokens and time? | An `assistant` skill that tells the agent to push through the gate |
| Q2 | Which review instructions change what the review stage catches? | One instruction block at a time, through the run's repository configuration |
| Q3 | Does this repository's 78KB `CLAUDE.md` make an agent working on `assistant` follow its rules? | `CLAUDE.md` in the task's working copy |
| Q4 | Which agent setup clears review findings most efficiently? | Nothing. Each arm is one agent setup (section 3.5), and a new release is one new arm. |

## 2. Entry criteria ("fully operational")

Q1 to Q3 are meaningful only when a gated run can reach a verdict about a
change without a person. Each criterion below is checkable. Q4 routes nothing
through the gate, so these criteria do not gate it. Its own entry condition
is in section 3.5: graders verified before any arm runs.

1. **A run reaches review.** The `internal/journey` walks no longer skip the
   review stage. The review body launches the resolved agent over the run's
   isolated copy, and the run records a verdict.
2. **A run reaches a code host.** At least one end-to-end run opens a pull
   request on a real repository. Every walk skips the pull request stage
   today because no run record names a repository on the code host.
3. **The agent caliper drives is one assistant resolves.** Caliper drives
   `claude-code` and `codex`. `internal/agents` must resolve the same CLI, so
   the gate's reviewer and the agent under test are both known quantities.
4. **Holds can be answered without a person, or are reported as the outcome.**
   A task that ends at a hold is graded as "held", never as success or
   failure. That needs a scripted answer or a terminal outcome caliper can
   read through `assistant`'s structured output (`internal/machine`).
5. **An `assistant` skill exists.** It is modelled on the user-level
   `no-mistakes` skill already installed in `~/.claude/skills`. Q1 has
   nothing to ablate without it.

A root report of 2026-09-09, `IMPLEMENTATION_COMPLETE.md` (commit `5037cd7`,
since removed under #323), called the build fully operational and listed five
stages as placeholders. That was stale: the `written` table in
`internal/stages/stages.go` now has a body for all nine stages. It was never
evidence for criteria 1-4, so check them against the journey harness when
this plan is picked up.

## 3. Design

### 3.1 Subject and tasks

Every task's `setup:` builds a fresh subject with `scripts/build-fixture.sh`,
so each attempt starts from the adversarial repository `internal/fixture`
already plants and documents. A task is a prompt asking the agent to make a
change and ship it. Each planted defect is a class of review finding this
repository has actually paid for, taken from the "What review keeps catching"
list in `CLAUDE.md`:

| Task family | Planted defect | Graded by |
| --- | --- | --- |
| Overstated contract | A doc comment promising a protection the code does not implement | `expect:` (judge) |
| Strict stance relaxed in one path | A fail-closed function with one branch that falls back to a default | `assert:` on the pushed tree |
| Fake shape | A test double that states a value the real parser cannot produce | `expect:` |
| Boundary breach | SQL or a `git` call outside its owner | `assert:` (or the anti-slop analyzers, once they land) |
| Lost work (P6) | A push anchored to a freshly read tip | `assert:` on the remote's history |
| Benign control | A correct change | `assert:` that it reached the remote unblocked |

The benign controls exist to measure false holds. A gate that stops
everything would score perfectly on the defect rows otherwise.

### 3.2 Specs and variants

- **Q1:** one spec. Its `skills:` names the `assistant` skill and, as a
  neighbour, `no-mistakes`, so activation also shows which one an agent
  reaches for when asked to "ship it". It runs as `caliper run --ablate
  assistant`.
- **Q2:** caliper ablates skills and MCP servers, not a tool's own
  configuration, so this uses one spec variant per instruction block. Each
  variant's `setup:` writes a repository configuration with that block
  removed, and the variants are compared with `caliper compare`. Blocks are
  ablated one at a time against the full set, not added one at a time to an
  empty set, because the question is which block earns its bytes in the
  section as it stands. The results become evidence for, or against, the
  case `docs/upstream-review-instruction-bounds.md` makes.
- **Q3:** the subject is a copy of this repository, not the fixture. One
  variant's `setup:` deletes `CLAUDE.md`. Tasks are rules an agent working
  here is known to break, such as "add a configuration key" (expected: a row
  in `internal/config/key.go`, and no second list) or "add a git operation"
  (expected: in `internal/vcs`). Each is graded by `assert:` against the
  resulting tree.

### 3.3 Sample size and cost

- k = 5 per task, and the single-run success rate is reported first, as
  caliper does. Q2 has 12 variants: one for each of the 11
  `review.path_instructions` blocks, plus the full-set baseline.
- Cost is an estimate to confirm on a pilot, not a figure this plan can
  vouch for:
  - `caliper run` reports tokens per attempt, and a gated attempt includes
    the gate's own review agent.
  - Run one task family at k = 1 first, and project the full run from that.
- Use `quota-axi` before a full run to confirm provider headroom.

### 3.4 Where results go

Results are a record of a run, in the shape `internal/judge/doc.go` uses for
its measurement: date, agent versions, caliper version, and the figures, plus
what they do not cover. They live in a new `docs/evaluations/` entry, not in
any package doc, and nothing in `make check` re-derives them.

### 3.5 Q4: the replay suite

Q4 is a standing suite, not a one-off run. Each case replays a real fix round
that failed, and each arm is one agent setup that tries it again. When a
model is released, it runs the frozen cases as a new arm and is compared
against the current default.

**Cases.**

- A case is one real failing fix round: the commit a review read, that
  review's exact findings, and, once the branch lands, the accepted fix.
- Cases come from the product's round records. Section 6 of `docs/prd.md`,
  "Learning what converges", owns what a case holds, when it completes, and
  why it never leaves the home. It is *proposed*, and nothing it describes is
  built. Until it is, cases are picked by hand from the fleet's review
  records, as the pilot's were.
- The scoreboard is a frozen, versioned subset of cases. A version is never
  edited. Adding or dropping a case makes a new version, and figures from
  different versions are not compared.
- A frozen case must keep its commits readable after its branch is gone.
  Section 6 has Q4 read cases where they are, in the home, and keeps their
  commits reachable there. A subset kept anywhere else would be a copy a
  person made, outside what section 6 describes. Where the subset lives, and
  how it keeps its commits readable, is step 7's decision.
- A case also needs a positive control. Its blocking findings must reproduce
  at its commit in the suite's own environment. A toolchain or dependency
  change can stop a finding from reproducing, and then every arm passes.

**Arms.**

- An arm is agent x model x effort x context, for example Claude Code,
  `claude-opus-5-5`, effort `high`, fresh session.
- Every arm of a case gets the same prompt: the task text, the worker rules,
  and the case's findings verbatim.
- Context is fresh or resumed. A replay can only start fresh, because a
  harvested case holds no session (section 6 names what a case holds, and a
  session is not part of it).
  The resumed side is therefore read from the live rounds in the round
  records, and it is not a controlled arm.
- Each arm runs under the same cap. An arm that has not committed by then is
  a failure.
- Whether caliper 0.19.0 can set an arm's effort, or the harness passes it
  some other way, is step 7's to settle. The pilot used the CLI's `--effort`.

**Graders.** An arm's attempt succeeds only when every grader that applies
to it passes.

- **Objective: the accepted fix's tests.** The landed fix's tests are run
  against the arm's result, as Q1's hidden tests are run against the remote.
  - **Verify before use.** A test grades a case only if it fails at the
    case's commit and passes at the landed head. A case with no such test is
    graded by the judge alone, and the record says so.
  - **Not every landed test fits the case.** The accepted fix is the head
    that landed, which can be many rounds after the case. Case 475-A's
    landed head is 76 commits later. Its tests also cover findings raised
    after the case, which the arm was never shown. Only the tests that check
    the case's own findings may grade it. The round record does not link a
    test to a finding, so that selection is made by hand, per case, and
    recorded with it.
  - **A test that does not compile against the arm's tree** (for example, it
    names a function the arm called something else) is reported as "grader
    did not apply". It is never counted as a pass or a failure.
- **A pinned judge.**
  - The judge is a review of the arm's result, with the case's findings as
    prior findings. It reports a verdict, its blockers, and any new defects.
  - The pin is the reviewer agent, its model, its effort, its prompt
    version, and the base it compares against. The base is pinned per case,
    so a moving default branch cannot change a review.
  - Whenever any part of the pin changes, rescore a baseline arm on the
    frozen subset before comparing new arms.
  - The judge is a fresh session, separate from every worker (P4).

**Metrics.** Each arm reports:

- **Success rate.**
  - At least k = 3 attempts per case.
  - Reported per case and pooled, each with a 95% Wilson interval.
  - Pooling treats attempts as independent, and cases differ in
    difficulty, so the pooled interval is narrower than the evidence. The
    per-case figures come first.
- **New-defect rate.** The judge's new findings per attempt, with new
  blockers counted on their own.
- **Tokens per success.**
  - Input, output, cache read and cache write each kept separate.
  - All attempts' usage, failures included, divided by successes.
  - A count an agent did not report stays unreported, never zero.
  - Worker and judge are reported separately, because the two vendors count
    differently.
- **API-equivalent $ per success.** These are API-equivalent at list price
  (illustrative; actual billing is subscription-based). Each figure names its
  rates and their source. The ranking AGE-492 asks for is success per
  dollar. These dollars are not what a subscription bills, so whether the
  ranking should use quota instead is for step 8 to settle.
- **Wall time.** With what else shared the machine named beside it.

Reporting follows `.claude/rules/usage-reporting.md`.

**Reviewer evaluation.**

- The same cases test a reviewer. A case whose blocker the accepted fix
  resolved has a known true blocker.
- Run a candidate reviewer at the case's commit, and score recall: did it
  find the known blocker?
- Run it at the landed head, and count the blockers it raises there, as a
  false-positive proxy. The proxy is an upper bound, because a later review
  can miss a defect that is really there.
- Pin a candidate reviewer's prompt and effort as the judge's are, and keep
  its results apart from the arm results.

**Where results go.** As section 3.4 says, each run is a record under
`docs/evaluations/`. A release run names the frozen subset's version, the
judge's pin, and the default it was compared against.

## 4. Risks and limits

- **Planted defects are ours.** Like the judge corpus, the defects were
  written by the people building the gate. Recall on them says nothing about
  defects nobody thought to plant, and the write-up must say so.
- **The judge is an LLM.** Use `assert:` wherever the outcome is a fact about
  a tree or a remote, and `expect:` only where it is not. Pin `--judge-model`
  across every run that is compared.
- **Caliper is third-party code that runs agents with your credentials.** Run
  it in a disposable environment, and check caliper's `sandbox.py` before
  assuming isolation. Pin a caliper release so the method does not move
  between runs.
- **Q4's cases are this fleet's rounds.** They are real defects, unlike the
  planted ones, but they come from a few repositories, mostly this one, and
  from the reviewers the fleet happened to use. A setup that wins here is
  shown to win on those rounds, not in general.
- **Q4's judge is a reviewer, and an LLM.** Its verdicts vary, and the pilot
  shows how much that matters. A replay of a live round drew an APPROVE
  where the live round had drawn a blocker. One sample cannot separate
  reviewer variance from the different worker output. That is why the plan
  asks for k >= 3, the objective grader wherever it applies, and a rescored
  baseline whenever the judge changes.
- **A judge may favor arms from its own vendor.** The pilot cannot show
  whether that happens, because its judge (Codex) and its arms (Claude) came
  from different vendors. Once an arm shares the judge's vendor, rescore
  that arm under a second judge from another vendor.
- **A delta approval is not a landing.** On PR #475, the fleet recorded each
  delta approval being followed by a full review that found a new blocker.
  Unless the judge runs full reviews, a Q4 success means "the next delta
  review was clean", and the record says so.
- **Q2 measures the review stage only through outcomes.** A block that never
  changes a verdict on these tasks may still matter on a defect class the
  tasks do not plant. "No measured effect" is not "safe to delete", and
  `CLAUDE.md` forbids freeing room by deleting a rule regardless.

## 5. Steps when picked up

1. Confirm entry criteria 1-5 against the journey harness and the stages
   table.
2. Write the `assistant` skill (criterion 5).
3. Pilot: one task family, Q1 only, k = 1. Check that grading separates
   blocked from shipped, and project the cost.
4. Full Q1, then Q3, which needs no gate, then Q2, the most expensive.
5. Write each run up in `docs/evaluations/` with its limits.
6. Q4 pilot: the fleet's replay benchmark, one judged case at k = 1. It is
   recorded in `docs/evaluations/2026-10-27-caliper-q4-replay-pilot.md`. Its
   15 unjudged runs are kept, so they can be judged later on the same trees.
7. Port the Q4 harness to `evals/caliper/q4/` on caliper 0.19.0. Freeze the
   first case subset. Before spending any arm runs, check each case as
   section 3.5 requires (AGE-492 step 2):
   - its positive control;
   - each objective test the case uses, failing at the case's commit and
     passing at the landed head;
   - the judge's pin.

   A case with no such test is recorded as judge-only, not dropped.
8. Add the release workflow: run the frozen subset against a new release,
   compare it with the current default, and record the result under
   `docs/evaluations/`. A person changes the role table defaults or the
   ladder order from that record, in configuration. Nothing changes them
   automatically (AGE-492 step 3).
