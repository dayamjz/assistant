# Critique of PR #60 (decision-model implementation)

Addressed to the agent implementing
[#60](https://github.com/dayamjz/assistant/pull/60)
(`claude/impl-decision-model`). This is a review of **that implementation**,
not a re-litigation of PRD #56. Where this document and the PRD disagree, the
PRD wins — raise a PRD finding rather than coding around it.

Related reading: `docs/decision-model-implementation-critique.md` (sequencing
and product risks before code existed). Exploration evidence:
`notes/jev-handoff.md` on `claude/jev-handoff` (PR #55).

## What is already strong (do not undo)

- Own `internal/judge` package; answers only add scrutiny.
- Typed `Result` is probability **or** failure, never both; `Fired` is false
  on failure.
- Dedicated `ASSISTANT_DECISION_MODEL_KEY` with no fallback to
  `TYPESAFE_API_KEY`.
- Bearer redaction owned in `internal/redact`.
- `judgetest` answers over the wire from OpenAPI-shaped bodies.
- Doc confessions on measurement limits and tripwire status match the
  mechanism more carefully than the PR title alone suggests.
- Mode `off` / no key send zero requests; test binaries unset the key.
- Trace check never partial-splits; screen splits without truncating and
  fails a file if any piece was unread.

Keep those. The items below are what still needs addressing before this is
ready to merge as the PRD’s phase-2 entry.

---

## Must address before merge

### M1. Evaluation record is incomplete against the PRD you cite

PRD section 5’s evaluation story is: record the answer **and what the run did
next** with that path, so `evaluate` can justify `enforce` or `off`.

What shipped: `decision_answer` stores check, path, model, probability,
cutoff, failure, and finding action. `internal/judge/doc.go` and the PR body
admit that “what the run did afterwards” and “propose turning a check off”
are not done.

**Address by one of:**

1. **Defer evaluate outbound until the record is complete.** Keep the client
   and store, but treat missing “outcome” columns as blocking for any mode
   that sends (or document a temporary `evaluate` that records answers only
   and raise an explicit PRD gap that section 5’s pay-for-themselves question
   cannot be answered yet), **or**
2. **Land the follow-up in this PR / a stacked PR before merge:** a durable
   link from a `decision_answer` row to later stage / hold / fix activity on
   that path (even a coarse “touched later: yes/no/unknown” filled when the
   run settles).

Best-effort drop-on-store-error is acceptable only if the undercount is
visible (counter / doctor check / report note). Today a silent log line makes
`evaluate` look healthier than it is.

### M2. Diff-read failures never enter `decision_answer`

In `stages/decision.go`, `ask` sets `unread` when `DiffPath` fails, and
`screenFindings` / `traceFindings` **skip** `record` for those rows
(`if c.unread == ""`). The person sees a “did not run” note; the evaluation
table does not.

That understates how often the check could not run for a mechanical reason.
Record them: same table, with a failure reason (extend `judge.Failure` or a
stage-owned failure string the store already accepts as opaque text),
probability unset, finding empty or note.

### M3. Cutoffs are pinned without an in-repo measurement suite

`check.go` hard-codes 0.8 / 0.3. The package doc correctly says the labeled
sets are outside the tree. The PRD’s tuning rule is then unenforceable.

**Address before treating cutoffs as settled:**

- Check in a fixture corpus (or recorded request/response pairs) and an
  offline check that the pinned cutoffs still beat the documented baselines
  on that corpus, **or**
- Move cutoffs behind a clearly named “provisional” seam and refuse
  `enforce` until a measurement command exists (PRD currently allows
  `enforce`; if you block it, that is a PRD ask, not a silent code default).

Do not merge `enforce` as a supported operator mode while the only evidence
is an external session notebook.

### M4. Merge and land order vs #59

This PR and [#59](https://github.com/dayamjz/assistant/pull/59) both touch
`internal/config/key.go`, `internal/stages/review.go`, and
`internal/stages/deps.go`. The implementing critique asked for judge paths
**before** the hosted classifier.

**Address:** rebase/merge #59 into this branch (or wait for #59 on `main`)
before ready-for-review; do not ask a human to resolve the semantic
interaction of two review-stage append paths under time pressure. Confirm
both halves of review (judge-path notes/asks **and** decision-model
notes/asks) compose, and that neither drops the other.

### M5. Section 13 verification should cite `principles` where it claims a principle

`decision_test.go` exercises the “at least as strict as with the model off”
row. If any of that is meant to establish a numbered principle (P3 fail-closed
on unreadable actions is nearby; the decision-model row is its own section 13
entry), add `principles.Cite` only where a principle is actually claimed. Do
not fake a citation. If it is only the Decision model row, say so in the test
doc comment so reviewers do not hunt for a missing Cite.

---

## Should address (product / contract honesty)

### S1. Default `evaluate` still means “key present ⇒ outbound diffs”

You correctly avoided `TYPESAFE_API_KEY`. That does not remove the product
risk: the first time an operator sets `ASSISTANT_DECISION_MODEL_KEY`, every
reviewed file’s changed lines leave the machine in the default mode.

**Address:** open a short PRD follow-up (or amend in a stacked docs commit)
proposing default `off` until the evaluation record has answered section 14’s
“pay for themselves” question — **or** keep `evaluate` but require an
explicit second consent flag. Do not silently code a different default than
the PRD.

### S2. Screen runs after the reviewer; findings must not imply otherwise

`review.go` states the screen protects the person’s reading of the verdict,
not the reviewer’s exposure. `flaggedFinding` already says the reviewer has
already read the file. Good.

Still verify every user-visible string (notes, asks, doctor, future PR body
text) never says “blocked before the agent saw it” or “prompt-injection
defense.” The package doc’s tripwire confession must remain the loudest
claim.

### S3. Wrong-model answers are folded into `FailureUnreadable`

The PR body lists “a different model answering” as its own failure class.
`readAnswer` returns `FailureUnreadable` for a model ≠ `ModelVersion`.

**Address:** either add `FailureWrongModel` (or similar) so metrics can
separate provider drift from JSON trash, or amend the PR body so it does not
promise a distinct class the code lacks. Prefer the typed failure — cutoffs
are version-bound; drift should be countable.

### S4. `FailureNothingToAsk` on empty diffs may drown the “did not run” note

Renames, mode-only changes, or paths with empty `DiffPath` output become
failures recorded (once M2 is fixed) and listed in the aggregate note. That
can train operators to ignore “did not run” noise.

**Address:** treat empty-diff paths as “nothing to ask, not a failure of the
check” — omit from the scary aggregate note, still record if useful, and do
not count them against evaluate quality.

### S5. Concurrency and provider load are undocumented knobs

`decisionConcurrency = 4` is fixed. A large review can still issue ≥1
request per file (plus piece splits). The package doc mentions the bound;
operator-facing docs / doctor should say a big diff means many outbound
calls and latency on the review stage.

---

## Nice to have (follow-ups, can be stacked)

### N1. Pull request body does not yet say which checks ran

Stated gap. Fine as follow-up if evaluate recording (M1) lands first.

### N2. Propose-turn-off note

Depends on M1. Do not implement a proposal from incomplete data.

### N3. Trace-check latency vs review hold

Checks run only on a usable review. Cancelled context aborts the report.
Confirm a slow provider cannot strand a run past existing timeouts without a
report note (client timeout is 30s per request × N files). Consider a
stage-level budget or clearer “checks abandoned” note when the parent
context is cancelled mid-fanout (today you return `ctx.Err()` and drop the
partial report — good for stop, but ensure the person sees why on restart).

---

## Explicit non-goals (do not “fix” these in this PR)

- Scope-creep detection, fixer file ranking, document-staleness ranking.
- Using Jev as a fourth loop bound or fix-loop progress judge.
- Blocking edits on a probability (“rule enforcer”).
- Claiming the screen is a security boundary.
- Implementing judge paths here — that is #59; compose with it (M4).

---

## Suggested response shape from the implementing agent

In the PR (comment or commits), answer each **M** item with: done / deferred
with reason / disagree with evidence. Do not resolve threads by restating the
package doc. Prefer tests that fail if the gap returns (especially M2
recording, M4 composition with judge-path findings, and M1’s chosen option).
