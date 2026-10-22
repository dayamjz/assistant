# Working agreement for this repository

`assistant` runs a fleet of coding agents in parallel and puts a validation gate
in front of your remote, so no change reaches a shared branch until it has been
independently reviewed, tested, documented, and linted. Package rules are in
`.claude/rules/<package>.md`; see Code.

## The specification is checked in

- `docs/prd.html` is the product requirements and the contract this code answers
  to. Read the section you are working in before you change behavior.
- Its section 4 lists fifteen numbered principles, `P1` through `P15`. They are
  acceptance criteria, not aspirations: each exists because the failure it
  describes actually happened in a system this design draws from, and a change
  that violates one is wrong even when it works and even when its tests pass.
- When the code and the PRD disagree, that is a finding to raise, not a
  documentation gap to close. Do not edit the PRD to match the code.
- Section 5 reserves two words that used to be interchangeable. A **hold** waits
  on a person; a **park** is a bound stopping the run. `internal/findings` owns
  the hold predicate (`Holds`, `Held`, `HasHeld`) and counts no bounds, so
  nothing there parks; `internal/graph` owns the four parks, and
  `graph.Status.Stopped` is named after neither because it is the union of both.
  Prose and identifiers both say this, so a reader may take either literally.
  Keep it that way: a wait on a person is a hold everywhere; only a bound parks.

## The principles that most often get violated by accident

- **P3.** A finding with a missing, empty, or unrecognized action becomes `ask`.
  This is defined behavior, not an error path to log and move past.
- **P4.** Reviewing and fixing are separate roles with separate memory. A review
  never resumes the session that prescribed the fix it is checking.
- **P6.** Never lose work. Anchor every history-rewriting update to a commit the
  run actually observed, never to a tip read immediately before pushing, which
  always succeeds and therefore protects nothing. When a safety fact cannot be
  verified, refuse and report.
- **P7.** Configuration that executes code is read from the default branch,
  never from a pushed commit. If the trusted copy cannot be read and parsed, stop
  before launching anything rather than falling back to defaults.
- **P8.** An append-only event log is not current state. Reading its last line
  to decide what is true now is always wrong.
- **P14.** Every fact has one owner. Everything else points at it.

## What review keeps catching

A reading lens beside `P1`-`P15`, not a replacement; each item has cost this
repository more than one round of review.

- **Claim only what a reader can verify from this repository.** A doc comment is
  a contract, so it may not promise more than the mechanism enforces
  (`internal/graph` and `internal/vcs` both shipped claims of protection nothing
  implemented). When a measure is best-effort, say so and name the residual gap.
- **Do not assert how an external system behaves internally.** Say what a measure
  buys this package instead: `internal/vcs` lost four rounds to sentences about
  git's internals, checkable only by probing git and falsified by the next round.
- **Watch for a strict stance quietly relaxed in one path.** `internal/findings`
  failed toward the human but substituted the schema example, `internal/config`
  accepted nothing silently but let a duplicate key win last, `internal/graph`
  reported an exhausted guard set as a completed run. The invisible exception is
  the part that would have shipped.
- **A fake may not produce a shape the real mechanism cannot.** `internal/safety`
  shipped a guard against a reference that peels to another object which could
  never fire: the read behind it did not ask for the peeled line, and its test
  passed because the fake stated `vcs.Ref` values that read cannot produce.
  Mutation testing misses this, since deleting the guard does fail the test.
  Model what the mechanism puts on the wire and derive values as the parser does.
- **Check a confession as hard as a promise.** `internal/pipeline` nearly
  disclosed that a configuration change could defeat its convergence bound, a
  failure the mechanism cannot produce; the true round-limit half of the same
  disclosure made the false half plausible. A doc comment is a contract in both
  directions, so verify a stated gap against the mechanism before writing it.

## Code

- Go, formatted with `gofmt`. `make check` is what CI runs and what the gate
  runs; keep it green.
- **Package rules live in `.claude/rules/<package>.md`** (`internal/service` is
  `.claude/rules/service.md`) and are part of this agreement. Read the one for
  the package you change, whatever tool you are: Claude Code loads it when it
  touches that package, other tools do not.
- `.no-mistakes.yaml` carries the gate's own review instructions and is
  executable configuration the gate reads from the default branch under P7, so a
  line there steers every future run. An instruction may claim only what the
  mechanism enforces, and where a guarantee is partial it says what it does not
  cover: one that overstates manufactures confident wrong findings for as long
  as it stands. Change it on a branch of its own, never at a document gate,
  where review is already past and no reviewer would see it. A rule that holds
  everywhere is the `path: "*"` block (the schema has no repository-wide review
  key), not a copy per package. The review section is capped with no room for a
  new block: measure with `no-mistakes`' own `ReviewPathInstructionsBytes`
  before writing anything; `docs/upstream-review-instruction-bounds.md` owns the
  accounting, the room left, what the gate does with an overfilled section, and
  each figure's method. Do not make room by deleting a rule: its defect starts
  recurring, and a diff that removes one instruction and adds another reads as
  an edit rather than as the regression it is. A channel with no room left is a
  decision to raise, not to settle at the point of use; that document is the ask.
- This file is not an overflow channel for that section, and neither is
  `.claude/rules/`; moving a review rule into either to make room is not a fix.
  The gate reads `.no-mistakes.yaml` from the default branch, while these files
  reach a reviewer only through the resolved agent CLI's project-doc discovery
  in the pushed working copy, so a rule moved here is deletable by the branch it
  was written to review, and can stop applying with no error and nothing
  reported, because that reach is adapter-dependent and nothing here settles it
  (`docs/upstream-review-instruction-bounds.md` owns which mechanisms decide
  it). Do not confuse the gate's `disable_project_settings` with this project's
  `suppress_project_instructions`, which decides nothing about the gate.
- Every exported symbol carries a contract, so give it a doc comment that states
  the contract rather than restating the name.
- Prefer a small, testable pure core with the side effects at the edges. The
  execution engine in `internal/graph` has no git, no network, and no agent
  calls in it, and it should stay that way.
- A refusal is a typed result a caller must handle, never a logged warning that
  execution continues past.

### Ownership that binds code outside the owning package (so it lives here)

- Configuration schema: the key table in `internal/config/key.go`. A key's
  default, trust class, decoding, and where it lands in a `Config` are one row;
  add a key by adding a row, never a second list of keys elsewhere.
- Execution and every loop bound: `internal/graph`. Add a bound there, never
  beside the counter that reads it.
- Git: only `internal/vcs` invokes it; add a typed operation there rather than
  shell out. `internal/fixture` and `internal/journey` are the two documented
  exceptions, and a third is a finding rather than a precedent.
  `vcs.StripRedirecting` is the one list of what points git at another
  repository, and every launch path builds a worker's environment through
  `agents.WorkerEnvironment`, so the environment half of P11 holds only that
  far and no second strip list exists or should.
- A lease, an incorporation check, or a force decision belongs in
  `internal/safety` even when shorter at the git call; `internal/vcs` stays
  mechanism only. `internal/custody` is the only path that moves a person's
  branch.
- Only `internal/store` opens the database or writes SQL; add a typed accessor
  there rather than a query elsewhere. The sealed `task_state` table (migration
  10) and the one-row `checkpoint` table (migration 1) never get an accessor.
- Only `internal/forge` talks to a code host, and a forge adapter never shells
  out to git. Only `internal/agents` starts an agent process. Only
  `internal/findings` parses, normalizes, validates, or refuses a report; there
  is never a second parser.
- A temporary directory a test makes with `os.MkdirTemp` is
  `standin.TempDir`'s. `RegisterTempTag` records the caller's directory
  inside the module as the namespace. A slash and an underscore in that
  directory stay distinct. An absolute path is made relative by walking up
  to `go.mod`; a non-absolute path is accepted only as the main module path
  plus that directory, the form `-trimpath` records, and is not opened.
  `TempDir` and `SweepTempDirs` use only that recorded namespace. The
  package's `TestMain` registers and sweeps before tests. A live owner, and
  a name that does not parse, are left. Go's `t.TempDir` and `go-build*`
  directories, and a scratch directory the product makes for itself, are
  not this.
- Only `internal/home` spells a path under the home root, except
  `<home>/repos/<id>.git`, which `internal/gate` composes itself. Only
  `internal/managed` writes a service definition or invokes `launchctl`
  or `systemctl`. Only `internal/checkpoints` serializes a `graph.Checkpoint`
  outside `internal/graph`.
- Only `internal/procgroup` names a starter, an entry of
  `internal/procgroup/guard`'s `Starters` (os/exec's constructors and
  starting methods, `os.StartProcess`, and the syscall and x/sys calls that
  start a program): build a command with `procgroup.Command` and run it with
  `procgroup.Run`. The functions that may do otherwise are the rows of the
  guard's `Exceptions`, each with a typed reason; its test fails the build on
  any other use in non-test code, and on a row that covers none.
- Credential removal is `internal/redact`'s: a new shape is added there, never
  a second remover at a call site. A sentence a person reads is a row in
  `internal/machine`'s phrase table (`phrases.go`), never one at a renderer.
- `internal/cli`'s verb table is PRD section 9's table and nothing else; a verb
  that section does not describe is a finding against the specification, not a
  row to add.

## Tests

- Test through an executable interface or a typed model of the behavior. A test
  whose only evidence is that the source text matches a pattern proves nothing
  and will be rejected in review.
- Construction-time rules get construction-time tests: an unbounded cycle must
  fail to build, and an undeclared shared state key must fail to build.
- Concurrency rules need the race detector, so `make test` runs it and CI runs
  it on both supported platforms, macOS and Linux. Windows is unsupported:
  Windows-tagged sources build and CI vets them (`GOOS=windows go vet`), but no
  lane runs them. On darwin/arm64 `-race` needs Go 1.26.5 or later (go.mod's
  `toolchain` pins it): 1.26.0 to 1.26.4 can kill a race-built test's git or
  agent child before it execs (golang/go#79804, #321).
- A test that establishes a PRD principle cites it with `principles.Cite` as
  well as saying so in its doc comment: the comment is what a reader learns
  from, the call is what `internal/principles` counts, and a principle no test
  cites fails `make check`.
- A test helper that runs git itself turns automatic maintenance off
  (`gc.auto=0`, `maintenance.auto=false`) in the configuration it gives git: a
  background maintenance git outlives the test and races its `TempDir` cleanup
  (#119). As `GIT_CONFIG_COUNT` variables that reaches the git the helper starts
  and its children, not a git the program starts (`internal/vcs` strips them),
  so a test checking the program's own setting still reads it with `--local`.
- A test that needs a coding agent scripts `internal/agents/standin` rather
  than writing its own `Runner`: a double stating typed values directly shipped
  a dead guard once, and one answering over the wire to the real adapter cannot.
- A guard that a caller *cannot reach* something is asked of the type graph,
  never of one assertion: `any(x).(T)` answers whether `x` is a `T`, not whether
  a caller can obtain one. Test P4 that way, through `internal/agents/route`,
  whose package comment owns how the walk works and why it is a package.
- A guard needs a positive control or it can pass by looking at nothing; the
  route walk's is `agents.Resolution`, which really does expose a `Runner`.

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
A rule for one package goes in its `.claude/rules/<package>.md` on the same bar;
one that binds code outside its package goes in the ownership list above.
