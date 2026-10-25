# Anti-slop analyzers: proposal

Status: proposal, with decisions D1-D3 taken by the owner on 2026-10-14
(section 6). Nothing here is implemented, and nothing in `docs/prd.md`
changes because of it.

## 1. Problem

Two places carry this repository's architectural rules today:

- `CLAUDE.md` reaches an agent only through project-doc discovery in the
  working copy, so a branch can delete or change it.
- `.no-mistakes.yaml` `review.path_instructions` are read from the default
  branch, but the section sits against a byte cap the gate checks before a run
  starts. `docs/upstream-review-instruction-bounds.md` records that there is
  well under a rule's worth of room left.

Both reach a reviewer as text, and a reviewer can miss text. Many of the rules
are not judgment calls, though. "Only `internal/store` opens the database" is
a fact about the import graph.

The review text is also aimed at the wrong place. `.no-mistakes.yaml` states
"No SQL outside this package" in the `internal/store/**` block and "the only
package that invokes git" in the `internal/vcs/**` block. A path block
reaches a reviewer only when a matching file changes. A branch that adds SQL
to `internal/service` and touches nothing in `internal/store` therefore never
shows a reviewer that sentence. The boundary rules have no channel that
reaches the file that breaks them.

`dmmulroy/anti-slop` takes the opposite approach for TypeScript. It turns a
team's recurring review complaints into lint rules that fail the build, and it
documents each rule's analysis boundary. Its rules do not apply to Go. The
approach does.

## 2. Goal

Turn every rule in `CLAUDE.md` that can be decided mechanically into a check
`make check` runs and that fails the build, wherever the violating file is.
Each check has a positive control that proves it can fire.

Freeing review budget is a side benefit, and a small one. Only two such
sentences exist in `.no-mistakes.yaml` today, and section 3's other
boundaries have no review text at all. The value is coverage review cannot
give, not bytes.

Non-goals:

- **Judgment rules.** "A doc comment may not promise more than the mechanism
  enforces" stays with review. No analyzer here claims to check it.
- **Style.** Formatting and naming stay with `gofmt`, `goimports` and `revive`.
- **Replacing review.** An analyzer covers only the rule as it states it. A
  violation reached by another route is still review's job.

## 3. Boundary audit (state on `main` at `e6b29e0`)

The audit used `go list` import data, a grep for `exec.Command(..., "git")`,
and a `go/ast` scan of test doc comments. All three were run on 2026-10-14.

| Rule (source: `CLAUDE.md`) | State today | Enforced by |
| --- | --- | --- |
| Only `internal/vcs` invokes git; `internal/fixture` and `internal/journey` are the named exceptions | Production code holds. Test helpers in `service`, `gate`, `cli`, `stages`, `tasks` and `session` run `git` directly (19 call sites) | Nothing |
| Only `internal/store` opens the database or writes SQL | Holds: `database/sql` and `modernc.org/sqlite` are imported only there | Nothing |
| Only `internal/forge` talks to a code host | Holds for production; `journey/shim.go` is a fake `gh`, which is test infrastructure | Nothing |
| Only `internal/agents` starts an agent process | `os/exec` is imported by `vcs`, `agents`, `forge`, `stages` (configured shell commands), `cli` (relaunching itself), `gate` (`LookPath` only), `fixture` and `journey`. Each use is legitimate, but no list says so | Nothing |
| `internal/graph` has no git, network or agent calls | Holds: it imports only the standard library, without `os`, `os/exec` or `net` | Nothing |
| Network access | `net` or `net/http` appear only in `ipc`, `judge`, `judge/judgetest` and `service` | Nothing |
| A refusal is a typed result, never a logged warning | No production package imports `log` or `log/slog` | Nothing |
| `store.ResolvedByProgram` has exactly one producer, `ResolvedByPerson` none | Holds | `internal/service/producers_test.go` (a `go/ast` scan) |
| A principle-citing test calls `principles.Cite`; a comment is not a citation | 130 test doc comments name a principle; 30 of those tests never call `Cite` | `internal/principles` counts calls only, so it is blind to this |
| Tests needing an agent script `standin` rather than writing a `Runner` | No hand-written `Run(ctx, agents.Invocation)` found outside `internal/agents` | Nothing |
| Build a `machine.Run` through `Decide`, not by filling fields | Holds in production; test files build literals for render tests | Unexported action field (by type) |

Conclusions:

1. Every import-level boundary holds today, and nothing would catch the first
   regression except a reviewer reading the right rule.
2. Some of the 30 uncited principle mentions are context rather than claims.
   They need triage before a rule can be strict.
3. Whether a test helper may run `git` directly was unsettled. `CLAUDE.md`
   calls a third exception "a finding rather than a precedent". Nineteen test
   call sites outside the two exceptions said either that the rule exempts
   tests or that it already had been broken. Decision D1 settles it: test
   files are exempt.

## 4. Requirements

### R1. Two tiers, one owner each

- **Import boundaries go in `depguard`, inside `.golangci.yml`.** It is
  already in the golangci-lint v2 build the Makefile and CI pin. No new
  binary, no new module dependency. Each rule carries a `desc` that names the
  owning package and points at its `doc.go`.
- **Semantic rules go in `go/analysis` analyzers** in `internal/lint`, one
  package per analyzer. `make vet` builds them into one `unitchecker` binary
  and runs `go vet -vettool=bin/assistant-lint ./...`, next to the plain
  `go vet` it already runs. This adds `golang.org/x/tools` as a module
  dependency. It is already in `go.sum` transitively.
- **One owner per rule.** A rule lives in `.golangci.yml` or in an analyzer,
  never both. When a rule lands, the review text it replaces is removed in the
  same change (see R6).

### R2. Import-boundary rules (depguard)

| Rule | Allowed importers | Denied elsewhere |
| --- | --- | --- |
| `sql` | `internal/store` | `database/sql`, `modernc.org/sqlite` |
| `exec` | `internal/vcs`, `internal/agents`, `internal/forge`, `internal/stages`, `internal/cli`, `internal/gate`, `internal/fixture`, `internal/journey`; every `_test.go` file (D1) | `os/exec` |
| `net` | `internal/ipc`, `internal/judge`, `internal/service` | `net`, `net/http` |
| `pure-core` | none | `internal/graph` and `internal/findings` may import nothing outside the standard library, and nothing from `os`, `os/exec`, `net` or `syscall` |
| `no-log` | `cmd/assistant` | `log`, `log/slog` |

Each allowlist entry carries the one-line reason the audit gave. An entry with
no reason is refused in review.

### R3. Semantic analyzers (`internal/lint`)

1. **`gitexec`.** Reports a call to `exec.Command` or `exec.CommandContext`
   whose program argument is the constant `"git"`. Also reports a constant
   that resolves to `"git"`, outside `internal/vcs`, `internal/fixture` and
   `internal/journey`. `_test.go` files are exempt (D1).
   - Analysis boundary: a program name built at run time or passed through a
     variable from another package is not seen.
2. **`citeclaim`.** Reports a `Test` function whose doc comment names a
   principle (`P1`-`P14`) as what the test establishes, when its body contains
   no `principles.Cite` call.
   - The claim form is a sentence that starts a paragraph with the principle,
     or the pattern `establishes Pn` / `checks Pn`. The exact grammar is
     settled from the triage of the 30 current cases, so the rule matches how
     this repository actually writes claims.
   - A mention in passing ("unlike P4") does not fire. That is the rule's
     stated gap.
3. **`standin`.** Reports a type declared in a `_test.go` file outside
   `internal/agents` that implements `agents.Runner`, `agents.Fixer`,
   `agents.SessionRunner` or `agents.WorkerRunner`. It uses `types.Implements`
   against the real interfaces, not a method-name match.
4. **`producers`.** Generalizes `internal/service/producers_test.go` into one
   table: symbol, then the packages allowed to reference it, then the expected
   count.
   - The first rows are `store.ResolvedByProgram` (one producer in
     `internal/service`) and `store.ResolvedByPerson` (none).
   - The existing test is retired in the same change, so the rule keeps one
     owner (P14).
5. **`answerliteral`.** Reports a composite literal of `machine.Run` in a
   non-test file outside `internal/machine`. Today that holds by convention
   only.

### R4. Every rule has a positive control

This follows the `CLAUDE.md` rule that a guard needs a positive control.

- Each analyzer ships `analysistest` testdata with at least one violating
  package and one clean package. The violating case is built the way real code
  would write it, not a shape the real code could not produce.
- Each depguard rule gets a violating file under `internal/lint/testdata`. A
  test runs golangci-lint against it with the repository's own `.golangci.yml`
  and asserts the expected rule fires. Like `make lint`, this test refuses
  rather than skips when golangci-lint is missing.

### R5. Zero baseline, no suppression file

A baseline file that lists existing violations is a strict stance quietly
relaxed in one path. Every rule lands with the tree clean:

- The 30 `citeclaim` cases are each fixed: either the test cites, or the
  comment is reworded to make clear it is not a claim.
- D1 exempts test files, so the 19 test call sites need no change and
  production code is already clean for `gitexec` and the `exec` rule.
- A `//nolint` on any of these rules is refused by review. The `nolintlint`
  linter is enabled with `require-explanation` so a suppression cannot be
  silent.

### R6. Moving rules out of the review budget

Only two sentences in `.no-mistakes.yaml` restate a rule here: "No SQL outside
this package" (`internal/store/**`) and "the only package that invokes git"
(`internal/vcs/**`). The second also introduces the environment-filter
guidance that follows it, so only its first clause is redundant.

When `sql` and `gitexec` land, those clauses can be removed on a branch of
their own, since `CLAUDE.md` says that file changes only on a branch of its
own. Removing them is not deleting a rule, because the rule now has a
stronger owner. The section is then re-measured with
`ReviewPathInstructionsBytes`, and the freed count is recorded in
`docs/upstream-review-instruction-bounds.md`. Expect tens of bytes, not a
rule's worth.

Each package's `doc.go` then states that the boundary is enforced by the named
rule. It keeps naming the residual gap the analyzer cannot see.

### R7. What each rule claims

Each analyzer's package doc states its analysis boundary in the anti-slop
style: what it resolves, what it does not, and which evasion it cannot see. No
analyzer is described as proving a principle. Like `internal/principles`, a
passing analyzer says the pattern is absent, never that the property holds.

## 5. Trust and residual gaps

- **The rules travel with the branch.** `make lint` runs from the pushed
  tree, the same as `.golangci.yml` does today. A branch can loosen an
  allowlist and pass its own lint. Review still has to notice a diff to
  `.golangci.yml` or `internal/lint`. A one-line review rule for that is a
  candidate for the bytes R6 frees, and it is the only new review text this
  plan proposes.
- **Static analysis sees only static use.** Reflection, `plugin`, a program
  name built at run time, and code generated at build time are outside every
  rule here.
- **Import rules do not stop reaching through.** A package can reach a
  boundary through an allowed package's exported API. That is
  by design, because the owning package's typed operation is the sanctioned
  route.

## 6. Decisions

Taken by the owner on 2026-10-14.

- **D1. Test helpers may run git directly.** Every `_test.go` file is exempt
  from `gitexec` and from the `exec` depguard rule, and `CLAUDE.md` says so
  beside the rule it narrows. The reason is the one `internal/fixture` and
  `internal/journey` already give: setting up a test through the package
  under test would have that package confirm itself. The rejected option was
  migrating the 19 call sites to a shared helper in one of the exceptions,
  which costs a refactor and buys no coverage.
  - Residual gap: a test helper can now run any git command, including one
    that should have been a typed operation in `internal/vcs`. Review still
    has to notice that.
- **D2. The `os/exec` uses in `internal/cli` and `internal/gate` are
  intended.** `internal/cli` relaunches its own binary and `internal/gate`
  only calls `LookPath`. Neither starts an agent or runs git, so both stay in
  the `exec` allowlist with those reasons.
- **D3. `pure-core` does not cover `internal/pipeline`.** It imports
  `internal/agents` and `internal/config` on purpose: `New` refuses a path
  the resolved adapter has not declared, and each stage's fix round limit is
  read from `config.FixRounds`. The rule stays on `internal/graph` and `internal/findings`.

## 7. Rollout

1. Settle D1-D3. Done (section 6).
2. Write D1's test-file exemption into `CLAUDE.md`, then land the depguard
   rules with their positive-control test. The tree is already clean for
   them.
3. Triage and fix the 30 `citeclaim` cases, then land `citeclaim`.
4. Land `gitexec`, `standin`, `answerliteral`, and `producers`. The last one
   retires `producers_test.go` in the same change.
5. On a separate branch, remove the review text these rules replace (R6), and
   re-measure the budget.
