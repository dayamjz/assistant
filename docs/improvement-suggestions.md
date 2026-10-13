# Suggestions: delivery efficiency and PRD completeness

Feedback from comparing this repository to `docs/prd.html` and to common
GitHub-area delivery tools (Actions + rulesets, Copilot code review / cloud
agent, CodeRabbit-style PR reviewers, Graphite-style merge orchestration,
Cursor cloud agents). It is **not** a PRD amendment. When this document and
the PRD disagree, the PRD wins — raise a finding against the PRD rather than
coding around it.

Related:

- `docs/decision-model-implementation-critique.md` — sequencing for judge
  paths vs hosted decision-model checks
- `docs/pr60-implementation-critique.md` — review of the decision-model
  implementation PR (while open)
- PRD §12 (MVP cut), §14 (open questions), §5 (stages, judge paths,
  decision-model checks)

## Thesis (keep)

Assistant is an **inner gate**: validate on the local push path, in an
isolated copy, before the named remote tip moves. GitHub’s thin layer
(Actions, rulesets, optional AI PR reviewers, merge queues) is the **outer**
check. Competing on PR chrome (comment UX, stack UI, auto-merge) dilutes the
product. Improve the gate; integrate out to the forge.

---

## Priority 1 — close holes in what a pass means

### S1. Unreviewed post-review fix commits

**Context.** Judge paths (#59) stop test/lint/CI verdict files from changing
without a person. Test/lint/checks fix rounds can still land **code** edits
that reach push with only ancestry from a reviewed commit. PRD §14 already
records this.

**Suggestion.** Open a PRD decision (or a small options RFC in `docs/`) that
picks one:

1. **Re-review after later-stage fixes** — push requires a review report whose
   revision is the head (or an ancestor within a declared “reviewed tip”
   key updated only by review). Cost: review returns after test/lint/CI;
   revisit “why review comes before test.”
2. **Narrower hold** — any post-review fix commit that touches paths outside
   a trusted allowlist holds with `ask` (stronger than judge paths alone).
3. **Explicit non-goal for phase 1** — keep §14 open, but fix
   `internal/stages/push.go` comments and operator docs so “pass” never
   implies those lines were reviewed.

Do not leave the overclaim / underclaim gap in comments and journeys.

### S2. Person-requested fix rounds from a hold

**Context.** PRD §5 offers a requested fix round at a hold. Pipeline docs
already call this unwired. At a judge hold today, only approve / skip /
cancel apply.

**Suggestion.** Wire the requested round through the existing hold surface
and the same judge rule (commit decides). Until then, surface the limitation
in `assistant` help / hold text so operators do not expect a fix action that
is not there.

### S3. Journey / binary coverage of delivery

**Context.** `internal/journey` correctly refuses to claim review quality from
scripted agents. Most walks still skip review, PR, and code host. That proves
principles and under-proves **time-to-signal on a real delivery path**.

**Suggestion.** Add a small set of walks (still with standin) that:

- take review → test → lint → push against a local gate remote,
- assert wall-clock budgets or stage timings in the record,
- fail if a stage regresses into an unexpected hold.

Keep the “not review quality” banner. The goal is **machinery latency and
reach**, not finding real bugs.

---

## Priority 2 — efficiency of execution

### S4. Make push-path cost visible and bounded

**Context.** A full run can pay rebase + review agent + test + lint + push +
PR + CI watch, and (with #60) N outbound decision-model calls per file.

**Suggestion.**

1. Record per-stage duration in a place `assistant status` / machine output
   can show (extend existing records; one owner).
2. For decision-model checks: keep one-file-per-request, but document and
   optionally cap fan-out (files × pieces × concurrency) so a huge diff
   cannot stall review indefinitely without a note.
3. Prefer **failing closed with a hold** over silent timeout when the budget
   is exceeded.

### S5. Do not duplicate the outer CI

**Context.** PRD permanent non-goal: “Being your CI.” CI stage watches the
forge; Actions remains the shared outer check.

**Suggestion.** Default operator docs should say: require GitHub/GitLab
checks as usual; assistant is the inner gate for agent branches. Avoid
config recipes that re-run the entire CI suite inside `commands.test` unless
the repository truly has no host CI.

### S6. Prove multi-agent adapters or shrink the catalog

**Context.** Cursor / OpenAI / Grok factories were added for doctor
auto-detect. Dead adapters that resolve on PATH but cannot speak the
envelope waste runs and trust.

**Suggestion.** For each catalog entry either:

- a conformance probe against a real or standin CLI (as Claude has), or
- remove / hide from `auto` until probed.

Doctor should report “resolves but envelope unproven” separately from
“runnable.”

---

## Priority 3 — fleet and phase-1 surface

### S7. Honest fleet status

**Context.** PRD §12 phase 1 lists fleet task shapes, dispatch, isolation
assertion, promotion from investigation to delivery. Much of that is still
ahead of the binary operators use for a single gated push.

**Suggestion.** Until fleet lands, README / `assistant doctor` should not
imply multi-repo fleet orchestration. Prefer: “single-home gate for agent
pushes” as the supported story; fleet as the next milestone with a checklist
mapped to §12.

### S8. Investigation → delivery promotion

**Context.** Phase 1 calls for promotion with decision holds and cleanup
proofs.

**Suggestion.** When building fleet, keep promotion as an explicit hold
(person or recorded decision), never an automatic stage skip (permanent
non-goal). Reuse findings vocabulary; do not invent a second outcome set
(`internal/outcomes` pins the PRD row).

---

## Priority 4 — decision-model (#60) and evaluation

### S9. Finish evaluate before leaning on enforce

**Context.** Critique of #60: answers are stored; “what the run did next”
and propose-turn-off are not. Cutoffs lack an in-repo measurement suite.

**Suggestion.** After #60 merges (or in a stack):

1. Link each `decision_answer` to coarse later outcome on that path.
2. Check in fixtures + offline measurement before treating `enforce` as
   production-ready.
3. Keep user-facing copy as tripwire / after-reviewer; never “injection
   defense.”

See `docs/pr60-implementation-critique.md` for the itemized list.

### S10. Default mode and consent (product, not silent code)

**Context.** PRD defaults `decision_model` to `evaluate`. Dedicated
`ASSISTANT_DECISION_MODEL_KEY` helps. First key install still starts
outbound diffs.

**Suggestion.** Raise a short PRD PR if evidence shows surprise outbound
traffic: default `off`, or require a second consent flag. Do not change the
default in code without a PRD change.

---

## Priority 5 — integration with GitHub’s thin layer (do this; don’t clone it)

### S11. Emit forge check runs that Actions can require

**Context.** CodeRabbit / Copilot become gates only when a required check
exists. Assistant already talks to the forge for PR/CI; it does not need to
own merge policy.

**Suggestion.** Optional: post a check run / commit status summarizing the
gate run outcome (held / parked / pushed) so branch protection can require
“assistant gate” **in addition to** CI — for remotes that still accept
direct pushes without the local gate. Keep the local gate primary.

### S12. Stay out of merge queue and stack UX

**Context.** Graphite optimizes stacking and merge order. That is forge-side.

**Suggestion.** Explicit non-goal in operator docs: use Graphite/GitHub merge
queue for landing order; assistant validates each push that enters the
stack. No stacked-PR UI in this binary.

### S13. Complement Copilot / CodeRabbit; don’t replace them

**Context.** Hosted reviewers are good at PR commentary and optional
pre-merge checks. They are weak at P4, P6, P7, and post-fix verdict
integrity.

**Suggestion.** Document a reference layout:

1. Agent pushes through `assistant` remote (inner).
2. PR opened/updated by the push/PR stages.
3. Copilot/CodeRabbit comment (outer, advisory or required as you choose).
4. Actions CI (outer, required).
5. Human or CODEOWNERS for merge.

---

## What not to improve by imitation

| Temptation | Why not |
|---|---|
| Configurable stage order / skip rules | Permanent non-goals; breaks comparable “pass” |
| Risk-based auto-skip of review | Classifier must never weaken a pass |
| Hosted multi-tenant service | Credentials and initiative stay local |
| Becoming CI | Outer check stays the forge |
| Silent degradation when agent/CI missing | Refuse and explain |

---

## Suggested sequencing for implementers

1. **S1** decision + docs/code honesty for unreviewed fix commits.  
2. **S2** hold → requested fix (or honest UX).  
3. **S4** + **S3** — visible timing and a delivery walk.  
4. **S6** adapter honesty.  
5. **S9** evaluation completeness after decision-model lands.  
6. **S7** / **S8** fleet only when a real multi-task user exists.  
7. **S11** optional forge check export when a repo needs protection without
   the local remote.

## Response shape

Treat each **S** item as: accept (issue/PR filed), defer with owner, or
disagree with PRD citation. Prefer stacked PRs over one megamerge. Do not
edit `docs/prd.html` to match incomplete code.
