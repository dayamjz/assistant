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

## What review keeps catching

These sit alongside `P1`-`P14` as a reading lens, not a replacement for them.
Each has cost this repository more than one round of review.

- **Claim only what a reader can verify from this repository.** A doc comment is
  a contract, so it may not promise more than the mechanism enforces.
  `internal/graph` and `internal/vcs` both shipped claims of protection nothing
  implemented. When a measure is best-effort, say so and name the residual gap.
- **Do not assert how an external system behaves internally.** Say what a measure
  buys this package instead. `internal/vcs` lost four rounds to sentences about
  git's internals, checkable only by probing git and falsified by the next round.
- **Watch for a strict stance quietly relaxed in one path.** `internal/findings`
  failed toward the human but substituted the schema example, `internal/config`
  accepted nothing silently but let a duplicate key win last, and
  `internal/graph` reported an exhausted guard set as a completed run. The
  invisible exception is the part that would have shipped.
- **A fake may not produce a shape the real mechanism cannot.** `internal/safety`
  shipped a guard against a reference that peels to another object which could
  never fire, because the read behind it did not ask for the peeled line and so
  could only ever come back with the two fields it compares equal. Its test
  passed because the fake stated `vcs.Ref` values directly and stated one that
  read cannot produce. Mutation testing does not surface this, since deleting
  the guard does fail the test. Model what the mechanism puts on the wire and
  derive the values from it the way the real parser does.
- **Check a confession as hard as a promise.** `internal/pipeline` came within a
  fix round of disclosing that a configuration change could defeat its
  convergence bound, a failure the mechanism cannot produce; the round-limit
  half of the same disclosure was true, which is what made the false half read
  as plausible. A doc comment is a contract in both directions, so verify a
  stated gap against the mechanism before writing it down.

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
- The configuration schema has one owner: the key table in
  `internal/config/key.go`. A key's default, trust class, decoding, and where it
  lands in a `Config` are one row. Add a key by adding a row, never by adding a
  second list of keys somewhere else.
- `internal/graph` owns execution and all three loop bounds. A bound only
  survives a resume with the thing it is compared against, so `Counters` carries
  the run's step budget and a digest of the edge vector its per-edge counts are
  indexed against, and a graph whose edges differ is refused rather than
  resumed. Add a bound by adding it there, never beside the counter that reads
  it. The budget is the one bound a caller may change on a live run, and only
  through `Executor.AdoptBudget`, which writes the change into the run's
  history; a difference nobody asked for is `ErrBudgetChanged`. Read its
  `doc.go` before changing any of that, and `identity.go` for what the digest
  covers and what it deliberately leaves out.
- `internal/vcs` is the only package that invokes git. Do not shell out to git
  anywhere else; add a typed operation there instead. Its package comment states
  the two rules it applies to every invocation and the residual gaps in them.
- `internal/store` is the only package that opens the database and the only one
  that writes SQL. Add a typed accessor there rather than a query elsewhere. Its
  driver is pure Go on purpose, so `make check` needs no cgo on any platform.
  A schema change is a new migration appended to `schema.go`, never an edit to
  one that has shipped; the tests number what they append from the shipped list
  rather than spelling a version out.
- `internal/safety` owns whether a branch update may proceed and on what anchor.
  `internal/vcs` stays mechanism only, so a lease, an incorporation check, or a
  force decision belongs in `internal/safety` even when it would be shorter to
  write at the git call. Its anchor is an `Observation`, and only `Guard.Observe`
  or `RestoreObservedFromCheckpoint` produces one. The wrong anchor is not
  unrepresentable: a restored anchor's provenance rests on the checkpoint it came
  out of, not on the type. Read its `doc.go` for why, and for the residual gaps
  that leaves.
- `internal/forge` is the only package that talks to a code host. Everything
  provider-specific stays behind `Provider`, and `internal/vcs` still owns git,
  so a forge adapter never shells out to it. Its checks model is the part that
  is not a thin wrapper: an empty check list is not green, only a `no_ci`
  declaration makes it so, a cancelled check is settled, and an unrecognized
  state deliberately keeps the caller waiting. Read its `doc.go` before
  changing any of those four.
- `internal/gate` owns the local bare repository a push is validated through:
  where it lives, what it is born with, its hooks, and its identity across a
  move or a copy. It composes `internal/vcs` and builds no command lines, and it
  composes `internal/store` for the ownership index without opening a database.
  Two things there are mechanism rather than rule, because this package wrote
  both rules down and then broke them. No exported operation takes a gate's
  path, only the working copy it is asked about: one unexported seam resolves
  the gate, settles who it belongs to, and seals it, and an operation that
  cannot obtain a handle cannot skip any of that. And nothing this package
  produced counts as evidence of ownership, so neither the `assistant` remote
  nor the path hash is weighed; the question is asked of the store's ownership
  index and the gate's own record, and every answer either gives is checked
  against the working copy actually standing there. Read its `doc.go` before
  changing any of that, and for the residual gaps: a path that outlives its
  working copy, `core.hooksPath`, and letter case in a path.
- `internal/agents` is the only package that starts an agent process. It owns
  the process tree, the per-invocation environment, and what is recorded about
  a call. P4 lives in its type split rather than in a rule callers follow:
  `Runner.Run` cannot be given a session and `Fixer.Apply` cannot be given a
  purpose, so keep any new entry point on one side of that line.
- `internal/ipc` owns the local protocol: the method table, the event
  taxonomy, and the bounded stream. Two rules there are load-bearing rather
  than stylistic. An event class decides what overflow may discard, and an
  unrecognized type is state, so a type added later is retained; adding a type
  means adding a row to the class table in `event.go`. Authority over a request
  comes from `Credentials`, read off the socket, and never from `Marker`, which
  a caller writes for itself. Read its `doc.go` for where state may still be
  collapsed into a gap marker and why that is not the strict rule relaxed.
- `internal/pipeline` is the nine stages as a graph definition, not a second
  executor: `internal/graph` owns execution, halting, and all three bounds. P2
  is structural there rather than checked. A `Stages` is nine named fields, so
  another order, a missing stage, and an added one are unsayable, and the order
  itself is the unexported stage table in `stage.go`, which also says which
  five stages may take fix rounds. The state schema is the key table in
  `key.go`, on the same terms as `internal/config`: a key's kind, its merge
  rule, and whether a stage may write it are one row, and a stage that names
  anything else fails to build. Read its `doc.go` before changing the loop or
  the holds, and for the residual gaps: the requested fix round PRD section 5's
  hold offers is not wired, and convergence is over the whole state.

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
