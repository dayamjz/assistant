# Working agreement for this repository

`assistant` runs a fleet of coding agents in parallel and puts a validation gate
in front of your remote, so no change reaches a shared branch until it has been
independently reviewed, tested, documented, and linted.

## The specification is checked in

`docs/prd.html` is the product requirements, and it is the contract this code
answers to. Read the section you are working in before you change behavior.

The PRD's section 4 lists fourteen numbered principles, `P1` through `P14`. They
are acceptance criteria, not aspirations. Each one exists because the failure it
describes actually happened in a system this design draws from. A change that
violates one is wrong even when it works and even when its tests pass.

When the code and the PRD disagree, that is a finding to raise, not a
documentation gap to close. Do not edit the PRD to match the code.

## The principles that most often get violated by accident

- **P3.** A finding with a missing, empty, or unrecognized action becomes `ask`.
  This is defined behavior, not an error path to log and move past.
- **P4.** Reviewing and fixing are separate roles with separate memory. A review
  never resumes the session that prescribed the fix it is checking.
- **P6.** Never lose work. Anchor every history-rewriting update to a commit the
  run actually observed, never to a tip read immediately before pushing, which
  always succeeds and therefore protects nothing. When a safety fact cannot be
  verified, refuse and report.
- **P7.** Configuration that executes code is read from the default branch, never
  from a pushed commit. If the trusted copy cannot be read and parsed, stop
  before launching anything rather than falling back to defaults.
- **P8.** An append-only event log is not current state. Reading its last line to
  decide what is true now is always wrong.
- **P14.** Every fact has one owner. Everything else points at it.

## Code

- Go, formatted with `gofmt`. `make check` is what CI runs and what the gate
  runs; keep it green.
- Every exported symbol carries a contract, so give it a doc comment that states
  the contract rather than restating the name.
- Prefer a small, testable pure core with the side effects at the edges. The
  execution engine in `internal/graph` has no git, no network, and no agent
  calls in it, and it should stay that way.
- A refusal is a typed result a caller must handle, never a logged warning that
  execution continues past.

## Tests

- Test through an executable interface or a typed model of the behavior. A test
  whose only evidence is that the source text matches a pattern proves nothing
  and will be rejected in review.
- Construction-time rules get construction-time tests: an unbounded cycle must
  fail to build, and an undeclared shared state key must fail to build.
- Concurrency rules need the race detector, so `make test` runs it and CI runs
  it on every platform.

## Commits and pull requests

- Conventional commits: `type(scope): description`. Use `feat` and `fix` for
  anything user-facing.
- Never add an agent as a commit co-author.
- Say what changed and why. The reviewer was not present for the work.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
