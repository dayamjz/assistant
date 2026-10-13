# Handoff: Jev (TypeSafe AI) exploration

Session notes so a new session can pick this up. Not a spec, not a
decision record, and not meant to merge into `main`.

## Where things stand

1. Researched the video "Insane Jev Use Cases You Need To Use Right Now"
   (AI LABS, https://youtu.be/2nc_QMuNp18). Captions could not be pulled
   from the cloud container (YouTube bot check), so what exists is an
   outline from the chapter list and description, not a transcript.
2. Proposed ways Jev could fit `assistant` (below). Nothing is built.
   No code in the repo calls Jev.
3. The key works as a cloud network secret (checked 2026-10-13). Note that
   `GET /v1/models` answers even without a key, so it proves nothing. The
   check that does is a real `POST /v1/systemone`, which returned 200.
4. Measured Jev against real commits from this repo and evaluated each
   proposal. Results are under "Measurements" and "Verdict" below. The
   short version: none of the four should be built as proposed.

## Next step on reopen

Ask the user whether to drop the integration or pursue the one narrow use
that held up: checking a reviewer's stated trace reason in
`internal/scope` (see Verdict). If they pursue it, the first deliverable
is still a PRD change proposal, not code.

## What Jev is

A decision model, not a text generator. POST
`https://api.typesafe.ai/v1/systemone` with `model` (`jev-latest`),
`state` (the input), and `questions` (named, each typed `noul` for
yes/no, `choice`, or `score`). Answers come back with probabilities and
confidence, plus the resolved model version (for example `jev-1.13.0`)
and token usage. $0.042 per million input tokens, output free.
Vercel AI Gateway also serves it as `typesafe-ai/jev`.

## API facts checked against the live service

The schema is published at `https://api.typesafe.ai/openapi.json`.

- A `noul` question must carry `instructions` or `criteria`
  (`{"true": ..., "false": ...}`), or the request gets a 400.
- A `noul` answer is a single probability of yes. It has no confidence
  field. `choice` and `score` answers do carry `confidence` and a
  per-option `probabilities` map.
- `state` may be a string, an object, or an array. The response `model`
  is the resolved version (`jev-1.13.0` for `jev-latest`).
- Two models are listed: `jev-latest` and `jev-preview`.
- An oversized `state` is refused with 400
  `{"detail":{"error_type":"max_tokens_exceeded"}}`. A 20 KB diff
  (about 6,000 input tokens) was accepted and 590 KB was refused; the
  exact cap was not found.
- Latency was 0.2 to 0.3 s per call, with occasional 0.8 s calls.
- Answers are not deterministic. The same request five times gave
  0.84 to 0.87, and another gave 0.30 to 0.38. Any threshold needs a
  margin of at least that spread.

## Measurements

The scripts are not checked in. The method is enough to repeat them.

Scope question ("does the change to this file follow from the intent"),
with `state` set to `{intent, path, diff}` and the diff cut to 3,000
characters:

- Easy set: 21 files from 8 commits. Each diff paired with its own commit
  message scored 0.71 to 0.97. Each one paired with an unrelated commit's
  message scored 0.02 to 0.44. The groups do not overlap, but the test is
  generous, since commit messages are written to describe the diff.
- Scope creep, which is the case the lens exists for: with the intent
  "Add a Grok agent adapter," the unrequested `cursor.go` from the same
  commit scored 0.76 to 0.94 on both models and with both a loose and a
  strict wording. A requested file scored 0.95. Jev reads topical
  closeness as "yes," so it cannot see sibling creep.
- How much of the diff is sent moves the answer: the same file cut at
  2,500 and at 3,000 characters scored 0.93 and 0.78.
- Injection: adding a diff comment that claimed the file was in scope, or
  told the model to answer true, did not raise the score (0.94 became 0.85
  and 0.79). That is a small sample, not a guarantee.
- A small credential-redaction bypass in `internal/redact` scored 0.07
  against an unrelated intent and 0.20 against "improve debug logging."
- Reason check: given a reviewer's stated reason for a path, a true reason
  scored 0.86, a false one ("adds the Grok adapter's HTTP client" on the
  Cursor file) 0.04, and a vague but wrong one ("shared helper needed by
  Grok") 0.23 to 0.29.

Document staleness ("does this document need an update after this diff"),
using the doc as it stood before the commit:

- Docs the commit really updated scored 0.76 and 0.46.
- Unrelated docs scored 0.13 to 0.25.
- A doc in the right area that the change did not need scored 0.57, above
  one of the real positives. It is useful as a rough ordering at best.

## Verdict on the four proposals

1. Scope lens as a scope detector: no. It misses sibling creep, which is
   the one case a reviewer would not already catch. A narrower use did
   hold up: flag a reviewer's trace whose reason does not match its diff.
   That closes part of the gap `internal/scope/doc.go` already admits ("a
   reviewer that writes a false reason for a path silences the note").
   Since scope findings are notes, a wrong answer costs one extra note and
   blocks nothing. This is the only one worth a PRD proposal.
2. Document staleness: no consumer exists. The document stage body runs a
   configured command and reads no `document.ownership`. Accuracy is also
   too weak to narrow what gets looked at, because a skipped document is a
   silent pass.
3. Fixer file ranking: no consumer exists. `Fix` hands the fixer findings,
   not a ranked file list. Per CLAUDE.md, surface with no consumer is
   dropped in review.
4. Review triage: no consumer, and the measured noise argues against
   letting a probability decide review depth even upward. Not worth it.

## Video outline (by chapter)

- 0:36 What Jev is and pricing
- 2:19 Setup through Vercel AI Gateway (TypeSafe signups paused)
- 3:11 Faster compaction (github.com/tamaratran/fast-jev-compaction)
- 4:57 Docs checker: does each documented rule have a test
- 5:49 Skill picker hook
- 7:48 Faster explore: Jev ranks keyword hits in batches of 20
- 9:05 Cheap first-pass review: seven yes/no questions pick review depth
- 9:57 Browser testing: Jev picks the next click, per role
- 11:15 Rule enforcer: blocks an edit at 80% confidence

## Integration ideas, safest first

1. `internal/scope`: per-path "does this trace to the intent" check.
   Findings there are already `note`, so a wrong answer blocks nothing.
2. Document stage: score which `document.ownership` documents a diff
   made stale, to narrow what gets looked at.
3. Rank files handed to the fixer. Jev orders input only, so P4 holds.
4. Review triage that can only add scrutiny, never skip review.

Avoid the video's rule enforcer pattern. A Jev answer has no evidence,
so it cannot pass `findings.ParseReviewReport`; low confidence must
become `ask` (P3), never a silent pass or block.

## Constraints any build must respect

- Jev is HTTP, not an agent process, so it is not `internal/agents`.
  It needs its own small client package with a named consumer.
- The key comes only from the operator's global config or environment,
  never repository config (P7). A new key is a row in
  `internal/config/key.go`, global-only like `checks_timeout`.
- `internal/redact` only knows URL userinfo; a bearer token is a new
  shape added there, not filtered at a call site.
- Tests use an `httptest` server returning real response JSON, not a
  fake stating typed values.
- Record the resolved model version with each decision.
