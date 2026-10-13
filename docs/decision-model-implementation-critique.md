# Critique: decision-model checks and judge paths

This is a reading for agents about to implement the PRD work landed in
pull requests [#56](https://github.com/dayamjz/assistant/pull/56) and
[#57](https://github.com/dayamjz/assistant/pull/57). It is not a second
PRD and it does not amend `docs/prd.html`. When this document and the
PRD disagree, the PRD wins; raise a finding against the PRD rather than
editing it to match an implementation preference.

The measurements and reject list behind the proposal live on the draft
branch `claude/jev-handoff` as `notes/jev-handoff.md` (PR #55). Read that
before building. The scripts and labeled sets are **not** in this
repository.

## What the PRD now requires

### Decision-model checks (PR #56, section 5)

Two checks may run beside review, driven by the global-only
`decision_model` key (`evaluate` | `enforce` | `off`, default
`evaluate`):

1. **Trace-reason check.** Each path trace a reviewer reports is scored
   against that path's changed lines. If the model does not confirm the
   reason, the trace silences nothing and the path gets the scope lens's
   ordinary untraced note.
2. **Reviewer-directed text screen.** Each reviewed file's changed lines
   are screened for text that tries to steer a reviewing agent. Hits are
   `note` in `evaluate`, `ask` in `enforce`.

A new `judge` module owns the fixed questions, the pinned model version,
the cutoffs and the measurement behind them, and request splitting.
Answers may only add scrutiny. A missing key, a failed call, or an
unreadable answer holds nothing and is reported. With mode `off` or no
key, no request leaves the machine.

### Judge paths (PR #57, section 5)

`judge_paths` names the files that decide a check's verdict. Trusted-only.
A pipeline-authored commit after review that adds, changes, deletes, or
renames a matching path holds with `ask`. Deleting or moving a judge file
in the author's change holds at review; editing or adding one is a
`note`. Empty list turns the rule off; a repository value replaces the
default.

## What held up in measurement (and what did not)

From `notes/jev-handoff.md`, short form:

| Proposed use | Verdict on this repo's data | In the PRD? |
|---|---|---|
| Injection screen (per-file) | Tripwire holds on author-written payloads | Yes |
| Trace-reason check (clean file-level labels) | Holds after label cleanup | Yes |
| Scope creep / "follows intent" | Sibling creep not separable | No |
| Document staleness | Weak; no consumer | No |
| Fixer file ranking | No gain over BM25; no consumer | No |
| Severity triage | No labels; no consumer | No |
| Rule enforcer (block edits on probability) | Violates P3 / review evidence rules | No |

Do not reopen the rejected uses as "while we are here" work. Surface with
no consumer is dropped in review in this repository.

## Critique for implementers

### 1. Prefer judge paths before the hosted classifier

Judge paths close a failure the code can already produce: a test, lint,
or checks fix round edits a verdict file, the stage re-runs green, and
push only requires that the head descends from a reviewed commit
(`internal/stages/push.go`). That comment's claim that re-review "catches"
post-review fix lines is true only for review's own round.

Build `judge_paths` first. It is local, deterministic, and does not send
repository content to a third party. The decision-model checks can wait
until evaluate-mode recording exists.

### 2. Treat the default mode as a product risk, not a free choice

The PRD defaults `decision_model` to `evaluate`. With a key in the
environment, that sends changed lines of every reviewed file to the
provider. "Key present is consent" is easy to trip: an operator who adds
the secret for a different experiment starts outbound review traffic.

When implementing:

- Do not soften the PRD by coding a different default silently.
- Do raise, in the PR that lands the key, whether default `evaluate`
  should become `off` until an evaluation record has justified the cost.
  That is a PRD change with evidence, not an implementation shortcut.

### 3. Do not overclaim the measurements

The PRD's evidence section rests on numbers from sessions whose scripts
are not checked in.

- Trace-reason labels were produced by a model that had the diff in front
  of it. Adversarial or human-written false reasons were not the test set.
- Injection payloads were written by the explorer. The handoff already
  calls this a tripwire for low-effort attempts, not a defense against
  someone adapting to it.

Doc comments and user-facing findings must confess that residual gap as
loudly as they state the check. A claim of "prompt-injection defense" or
"verified trace reasons" that the mechanism does not enforce will fail
review in this repository.

### 4. Check evidence into the repository before tuning cutoffs

The PRD's tuning rule requires a change to a question, version, or
cutoff to beat the one it replaces on held-out labeled data, by more than
call-to-call variation, without editing the scorer and the labels in the
same change.

That rule is unenforceable while the labeled sets live only on
`claude/jev-handoff`. An implementation that hard-codes cutoffs from the
handoff numbers without a reproducible suite is guessing. Land a fixture
corpus (or a recorded stand-in suite with fixed answers) in the same
change that pins a cutoff, or leave cutoffs unexported constants behind
a measurement package that the suite owns.

### 5. Wire the seams once; do not invent a second owner

Ordering the PRD leaves implied:

- The **screen** reads changed lines. It must run from the review stage
  body against the run's diff, not from a second parser of agent output.
- The **trace-reason check** needs the reviewer's traces. It runs after
  `findings.ParseReviewReport` (or equivalent), on the bound report, so a
  demoted or unbound finding never reaches it ahead of P3's path.
- Both belong behind the `judge` package the PRD names. Call sites in
  `internal/stages` (and any scope interaction) **point at** that owner;
  they do not re-encode questions, cutoffs, or HTTP.

`internal/agents` is the wrong home: Jev is HTTP, not an agent process.
P4's type split does not apply to it, and stuffing it there teaches the
wrong lesson.

### 6. Evaluation recording is load-bearing, not optional polish

`evaluate` exists so answers can be judged from evidence: what the model
said, and what the run did next. Section 9's command surface for that
record is deferred. Without a durable record owned in one place
(candidate: extend the invocation / run history already on the store,
with a typed decision-model row rather than a free-form log), `evaluate`
is a silent third-party call that nobody can turn into a decision about
`enforce` or `off`.

Do not ship outbound calls in `evaluate` without writing the evaluation
record in the same change. A note proposing to turn a check off when it
never leads anywhere (PRD text) needs that record as input.

### 7. Failure and absence must look like "checks off," and say so

The PRD already requires: missing key, failed call, unreadable answer →
hold nothing, report the gap. Implement that as a typed result the review
body must handle, never a logged warning execution continues past. A
pass must remain defined without these checks; their absence is reported
in the stage report where a person reads it.

### 8. Judge-path patterns replace; defaults are Go-heavy

A repository value replaces the default list. An empty list turns the
rule off. Defaults lean on Go test names, GitHub workflows, and a few
lint configs. Repositories that are mostly TypeScript, Python, or custom
layouts will need an explicit trusted-copy list or they inherit a list
that matches little and protects less than it appears to.

When implementing defaults, claim only what the patterns match. Do not
document them as "all test and CI files." The PRD already states the
residual gap: scripts a command calls, manifests holding test config, and
weakened assertions inside an edited test are outside the hold.

### 9. The larger gap is still open: unreviewed fix commits

PR #57 records in section 14 that test, lint, and checks fix rounds can
land code nobody reviewed; judge paths only stop those rounds from
editing verdict files without a person. Closing that properly means push
requires a review covering the head, which reorders cost against "why
review comes before test."

Do not pretend judge paths or Jev close P5 for post-review fix commits.
If an implementation comment restates the overclaim at
`internal/stages/push.go` (re-review "catches" those lines), fix or
narrow that comment in the same change that lands judge paths.

### 10. Redaction and secrets

The provider key is environment / global config only (P7). A bearer token
is a credential shape `internal/redact` does not know today; adding the
client means adding that shape there, not filtering at the call site.
Record the resolved model version with each decision (PRD). Tests use an
`httptest` server returning real response JSON, not a fake that states
typed verdicts the wire cannot produce.

## Suggested implementation order

1. **`judge_paths` mechanism** in the stages that author commits after
   review (test, lint, checks, document in-pass), plus the review-time
   delete/move `ask` and edit/add `note`. Trusted-only config row.
   Narrow the push-stage comment that overstates re-review.
2. **Evaluation record owner** for decision-model answers and what the
   run did next (even if the section 9 verb comes later).
3. **`internal/judge` client** with pinned version, fixed questions,
   stand-in tests over the wire, redaction of the secret, and no outbound
   call when mode is `off` or the key is missing.
4. **Screen and trace-reason wiring** in the review stage, note-only
   under `evaluate`, with cutoffs backed by a checked-in measurement
   suite.
5. **`enforce` for the screen only** after the evaluation record shows
   the screen earning its keep. Trace-reason stays note-only per PRD.

## Explicit non-goals for the first implementation PRs

- Using the decision model as a fourth loop bound or progress judge in
  the fix loop.
- Scope-creep detection, file ranking for the fixer, document-staleness
  ranking, or severity triage.
- Blocking an edit on a probability (the video's "rule enforcer").
- Declaring the injection screen a security boundary.
- Editing the PRD to match incomplete code, or coding a different default
  than the PRD without a PRD change.

## How to disagree with this critique

If implementation shows a critique item wrong, say so in the PR with
evidence from this repository (a test, a measurement suite, or a PRD
citation). Do not silently drop a PRD requirement because this document
ranked it later. Priority here is sequencing and risk, not a license to
skip.
