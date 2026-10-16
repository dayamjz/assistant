# Caliper evaluation of assistant: plan

Status: entry criteria met; pilot recorded
(`docs/evaluations/2026-10-15-caliper-q1-pilot.md`); full Q1 harness checked
in under `evals/caliper/q1/` (`docs/evaluations/2026-10-15-caliper-q1-harness.md`).
The full agent run at k = 5 is still outstanding (issue #287). Nothing here
runs in `make check` or CI.

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

This answers three questions about `assistant` that nothing in this repository
measures today. `internal/journey/README.md` states the gap in its first
limit: green there "is not evidence the gate catches bugs".

| # | Question | What gets ablated |
| --- | --- | --- |
| Q1 | Does routing an agent's changes through the `assistant` gate stop defects from reaching the remote, and at what cost in tokens and time? | An `assistant` skill that tells the agent to push through the gate |
| Q2 | Which review instructions change what the review stage catches? | One instruction block at a time, through the run's repository configuration |
| Q3 | Does this repository's 78KB `CLAUDE.md` make an agent working on `assistant` follow its rules? | `CLAUDE.md` in the task's working copy |

## 2. Entry criteria ("fully operational")

The evaluation is meaningful only when a gated run can reach a verdict about
a change without a person. Each criterion is checkable:

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

`IMPLEMENTATION_COMPLETE.md` (2026-09-09) calls the build fully operational
and lists five stages as placeholders. That is stale: the `written` table in
`internal/stages/stages.go` now has a body for all nine stages. Neither
document is evidence for criteria 1-4, so check them against the journey
harness when this plan is picked up.

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
