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
3. The user has a typesafe.ai token and was walked through adding it as a
   cloud environment network secret. Not yet confirmed working.

## Next step on reopen

Check the secret from inside the new session:

    curl -sS https://api.typesafe.ai/v1/models

A model list including `jev-latest` means the proxy attaches the key.
A 401 means it does not. The key is never visible as an env var when it
is a network secret; requests just omit the Authorization header.

Then ask the user which integration to start with. The recommendation
was the scope lens, and the first deliverable is a PRD change proposal,
not code (CLAUDE.md: behavior answers to `docs/prd.html`).

## What Jev is

A decision model, not a text generator. POST
`https://api.typesafe.ai/v1/systemone` with `model` (`jev-latest`),
`state` (the input), and `questions` (named, each typed `noul` for
yes/no, `choice`, or `score`). Answers come back with probabilities and
confidence, plus the resolved model version (for example `jev-1.13.0`)
and token usage. $0.042 per million input tokens, output free.
Vercel AI Gateway also serves it as `typesafe-ai/jev`.

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
