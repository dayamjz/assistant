# assistant

**Run many coding agents at once, and let none of them ship unvalidated work.**

Assistant does two things you currently do by hand. It runs a fleet of coding
agents in parallel, each in its own isolated copy of a repository, and it puts a
validation gate in front of your remote so no change reaches a shared branch
until it has been independently reviewed, tested, documented, and linted.

## Status

Early. The product requirements are settled and checked in at
[`docs/prd.html`](docs/prd.html). Sixteen pieces exist so far. The execution
engine in `internal/graph` is the first: the graph builder with its
construction-time checks, an executor with halt points and bounded cycles, and
checkpoints behind a four-operation store. The second is `internal/findings`,
the vocabulary every pipeline stage speaks: a finding with its severity and its
action, the report a stage returns, the defensive parsing that turns untrusted
agent output into a validated report, and the one rule a review report answers
for beyond that: it carries the revision it read and the paths it actually
read, and a finding reaching past that evidence is refused and reported as
refused rather than believed. The third is `internal/config`, the
configuration schema: the two-layer merge, the defaults, the parse-time
validation, and the path matcher. The fourth is `internal/vcs`, the only
package that invokes git. The fifth is `internal/safety`, the policy
layer over it: it decides whether a branch update may proceed and on what
anchor, and refuses rather than guessing when a fact the decision rests on
cannot be verified. The sixth is `internal/store`, the durable record of what
the gate did: an embedded sqlite database behind typed accessors, with additive
migrations checked against the database's own catalog, and every hold
resolution recorded with who made it, from a closed set that is the type
itself rather than a check, so no text a caller was handed decodes into one.
The seventh is
`internal/agents`, the only package that starts an agent process: the separate
reviewing and fixing roles, the capabilities an adapter declares and is held
to, the Claude Code adapter, the review shape that carries the evidence its
answer is bound to and that the fixer refuses, ordered fallback resolution, and
a record of what each invocation cost. The eighth is
`internal/forge`, the only package that talks to a code host: the provider
interface over pull requests, mergeability, and checks, a GitHub adapter over
the `gh` command line, and a checks model in which an empty check list is not a
pass. The ninth is `internal/ipc`, the local protocol between the command line
and the background service: the method table, the event taxonomy and its
bounded stream, a client, a server, and peer identification the kernel answers
for. Who resolved a held decision that arrives here is derived from the
surface rather than read out of the request, so the answer is the machine
interface and never a person, including when a person typed it, because
nothing the kernel tells this protocol separates the two. The tenth is
`internal/gate`, which owns the local bare repository a push
is validated through: where it lives, the admission and notification hooks that
make a push mean something, its identity across a move or a copy, and one seam
every operation obtains its gate from, so no operation can skip the ownership
question that an index in `internal/store` answers. The eleventh is
`internal/pipeline`, the nine delivery-gate stages as a graph over the
execution engine: the contract one stage implements, the fixed order carried as
nine named fields rather than a list, the state schema as one key table, the
refusal of a path the resolved adapter has not declared, and the fix loop with
its halt points and its three bounds. It defines a topology and executes
nothing. The twelfth is `internal/scope`, the review stage's scope
lens rather than a tenth stage: the guidance that asks a reviewer to trace every
path a change touched back to the recorded intent, and the notes an untraced
path becomes. It ships on and no configuration key turns it off, and what it
produces is always a note that informs and blocks nothing. The thirteenth is
`internal/fixture`, the adversarial subject repository the end-to-end harness
validates against: seven scenarios built from nothing on demand, each planting
conditions a stage or a refusal has to answer, with the answer each one must
produce recorded beside it. The fourteenth is `internal/runs`, the service that
owns a run: its status changes as a table of anchored moves, each read and
written in one transaction against the statuses the move is legal out of, and
the one durable fixer session a run keeps, recorded on the run itself rather
than in pipeline state, so a run's fixer resumes the same conversation across a
segment boundary and across a restart of the service. The fifteenth is
`internal/checkpoints`, the durable form of the four-operation store the
execution engine writes through: a run's whole checkpoint history in the
embedded database, where every write is anchored to the checkpoint the caller
observed and one that anchors to a run that has moved is refused rather than
appended, so a run survives a restart as a position and not only as a record.
The sixteenth is `internal/principles`, the build-time check that fails when a
principle the PRD lists is neither cited by a test nor written down as a
declared gap: the PRD owns that list and the constants here are pinned to it in
both directions, and a test cites by calling `principles.Cite` inside its own
body, so a comment naming a principle does not count. What it establishes is
only that no principle goes silently unclaimed; a citation says a test claims to
check a principle, never that the principle is covered or that it holds. The
nine stage bodies are separate work against that contract and do not exist yet,
including the review stage that puts the scope lens in front of a reviewer and
binds what comes back to what the reviewer declared reading, and neither the
harness nor the `assistant` binary exists either, so the only thing in here you
can run is the fixture builder.

## The two promises

**Parallel work you can watch.** Every worker runs in a real terminal you can
attach to, read, and type into. If you intervene directly in a worker's
terminal, that intervention is authoritative.

**Nothing unvalidated is shared.** The gate is a named remote you push to on
purpose. Pushing to it is how you consent to having that change validated,
fixed, pushed, and turned into a pull request. Your ordinary `git push` keeps
working as it always did.

## What it is not

- Not continuous integration. Your CI stays the shared outer check.
- Not a code host, and it does not own merge policy.
- Not a model or an agent harness. It drives whichever coding agent you use.
- Not a team governance platform. It publishes facts, not verdicts.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/assistant` | The binary. Not written yet, so `make build` has nothing to build. |
| `cmd/fixture` | Builds the fixture repository into a directory you name. `scripts/build-fixture.sh DIR` runs it. |
| `internal/agents` | The only package that starts an agent process: the run and fix roles, the capability declaration every adapter is held to, the Claude Code adapter, fallback resolution, the review shape and the evidence demand it carries, and invocation records. |
| `internal/agents/standin` | The scripted agent the tests outside `internal/agents` run against: the test binary re-executed as the agent process, read by the production adapter. |
| `internal/checkpoints` | The durable `graph.CheckpointStore` over `internal/store`: a run's checkpoint history, appends anchored to what the caller observed, and the fork that copies a run's history up to a point into a new run. |
| `internal/config` | The configuration schema: layers, defaults, merge, validation, path matcher. |
| `internal/findings` | The stage vocabulary: findings, actions, reports, parsing of agent output, and the evidence a review report's findings are bound to. |
| `internal/fixture` | The adversarial subject repository the end-to-end harness validates against: the seven scenarios, the conditions planted in them, and what each one is expected to produce. |
| `internal/forge` | The only package that talks to a code host: the provider interface over pull requests, mergeability, and checks, and the GitHub adapter over the `gh` command line. |
| `internal/gate` | The local bare repository a push is validated through: where it lives, its two hooks, its identity across a move or a copy, and the ownership question every operation asks before it adopts or deletes one. |
| `internal/graph` | The execution engine: nodes, edges, bounds, halt points, checkpoints. |
| `internal/ipc` | The local protocol between the command line and the background service: the method table, the event taxonomy, the bounded stream, the client and the server, peer identification, and the one resolver it derives for an answer that arrives here. |
| `internal/pipeline` | The nine stages as a graph definition over `internal/graph`: the stage contract, the fixed order, the state key table, the capabilities a path needs of the adapter, the fix loop, and its halt points and bounds. |
| `internal/principles` | The build-time check that no principle the PRD lists goes unclaimed: the constants pinned to that list, the `Cite` call a test claims a principle with, the scan that finds those calls, and the written table of what nothing claims. |
| `internal/runs` | The run service: the anchored table of a run's status changes, and the one durable fixer session a run keeps, recorded on the run so a restarted service resumes the same conversation. |
| `internal/safety` | The data-loss policy over git: whether a branch update may proceed, on what anchor, and when to refuse. |
| `internal/scope` | The review stage's scope lens, not a tenth stage: the guidance a reviewer traces each touched path against, and the note an untraced path becomes. |
| `internal/store` | The only package that opens the database: the schema, its additive migrations, and typed accessors for repositories, runs, stages, rounds, a run's anchored checkpoint history, tasks, task state and events, holds and the closed set of who resolved each one, and the gate ownership index. |
| `internal/vcs` | The only package that invokes git: typed operations over repositories, worktrees, refs, diffs, and remotes. |
| `docs/prd.html` | The product requirements. The specification this code answers to. |

## Development

```sh
make build     # build the binary into ./bin
make test      # go test -race ./...
make lint      # vet and golangci-lint
make check     # lint and test, what CI runs
```

```sh
scripts/build-fixture.sh DIR   # build the fixture repository into DIR
```

`DIR` must not exist or must be empty, and the manifest describing what was
planted is written to `DIR/manifest.json`, whose path the script prints. The
fixture is built from nothing every time, so none of it is checked in.

`make test` exercises `internal/vcs`, `internal/gate`, and `internal/fixture`
against a real git, so it needs a git binary on `PATH`; `internal/vcs`'s
package comment states the minimum version it needs. An `internal/fixture`
test skips itself when git is not on `PATH`, and the ones that drive the
planted toolchain conditions skip when `go` is not either.

`make lint` requires golangci-lint from the v2 series, the line that can read
this module's `.golangci.yml`. It refuses when the linter is missing or comes
from another major series rather than quietly running `go vet` alone, so a
green `make check` always means lint ran. `make lint-guard-test` proves that
refusal still fires.

## License

MIT. See [LICENSE](LICENSE).
