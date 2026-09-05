# assistant

**Run many coding agents at once, and let none of them ship unvalidated work.**

Assistant does two things you currently do by hand. It runs a fleet of coding
agents in parallel, each in its own isolated copy of a repository, and it puts a
validation gate in front of your remote so no change reaches a shared branch
until it has been independently reviewed, tested, documented, and linted.

## Status

Early. The product requirements are settled and checked in at
[`docs/prd.html`](docs/prd.html). Thirteen pieces exist so far. The execution
engine in `internal/graph` is the first: the graph builder with its
construction-time checks, an executor with halt points and bounded cycles, and
checkpoints behind a four-operation store. The second is `internal/findings`,
the vocabulary every pipeline stage speaks: a finding with its severity and its
action, the report a stage returns, and the defensive parsing that turns
untrusted agent output into a validated report. The third is
`internal/config`, the configuration schema: the two-layer merge, the defaults,
the parse-time validation, and the path matcher. The fourth is `internal/vcs`,
the only package that invokes git. The fifth is `internal/safety`, the policy
layer over it: it decides whether a branch update may proceed and on what
anchor, and refuses rather than guessing when a fact the decision rests on
cannot be verified. The sixth is `internal/store`, the durable record of what
the gate did: an embedded sqlite database behind typed accessors, with additive
migrations checked against the database's own catalog. The seventh is
`internal/agents`, the only package that starts an agent process: the separate
reviewing and fixing roles, the Claude Code adapter, ordered fallback
resolution, and a record of what each invocation cost. The eighth is
`internal/forge`, the only package that talks to a code host: the provider
interface over pull requests, mergeability, and checks, a GitHub adapter over
the `gh` command line, and a checks model in which an empty check list is not a
pass. The ninth is `internal/ipc`, the local protocol between the command line
and the background service: the method table, the event taxonomy and its
bounded stream, a client, a server, and peer identification the kernel answers
for. The tenth is `internal/gate`, which owns the local bare repository a push
is validated through: where it lives, the admission and notification hooks that
make a push mean something, its identity across a move or a copy, and one seam
every operation obtains its gate from, so no operation can skip the ownership
question that an index in `internal/store` answers. The eleventh is
`internal/pipeline`, the nine delivery-gate stages as a graph over the
execution engine: the contract one stage implements, the fixed order carried as
nine named fields rather than a list, the state schema as one key table, and
the fix loop with its halt points and its three bounds. It defines a topology
and executes nothing. The twelfth is `internal/scope`, the review stage's scope
lens rather than a tenth stage: the guidance that asks a reviewer to trace every
path a change touched back to the recorded intent, and the notes an untraced
path becomes. It ships on and no configuration key turns it off, and what it
produces is always a note that informs and blocks nothing. The thirteenth is
`internal/fixture`, the adversarial subject repository the end-to-end harness
validates against: seven scenarios built from nothing on demand, each planting
conditions a stage or a refusal has to answer, with the answer each one must
produce recorded beside it. The nine stage bodies are separate work against that
contract and do not exist yet, including the review stage that puts the scope
lens in front of a reviewer, and neither the harness nor the `assistant` binary
exists either, so the only thing in here you can run is the fixture builder.

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
| `internal/agents` | The only package that starts an agent process: the run and fix roles, the Claude Code adapter, fallback resolution, and invocation records. |
| `internal/agents/standin` | The scripted agent the tests outside `internal/agents` run against: the test binary re-executed as the agent process, read by the production adapter. |
| `internal/config` | The configuration schema: layers, defaults, merge, validation, path matcher. |
| `internal/findings` | The stage vocabulary: findings, actions, reports, and parsing of agent output. |
| `internal/fixture` | The adversarial subject repository the end-to-end harness validates against: the seven scenarios, the conditions planted in them, and what each one is expected to produce. |
| `internal/forge` | The only package that talks to a code host: the provider interface over pull requests, mergeability, and checks, and the GitHub adapter over the `gh` command line. |
| `internal/gate` | The local bare repository a push is validated through: where it lives, its two hooks, its identity across a move or a copy, and the ownership question every operation asks before it adopts or deletes one. |
| `internal/graph` | The execution engine: nodes, edges, bounds, halt points, checkpoints. |
| `internal/ipc` | The local protocol between the command line and the background service: the method table, the event taxonomy, the bounded stream, the client and the server, and peer identification. |
| `internal/pipeline` | The nine stages as a graph definition over `internal/graph`: the stage contract, the fixed order, the state key table, the fix loop, and its halt points and bounds. |
| `internal/safety` | The data-loss policy over git: whether a branch update may proceed, on what anchor, and when to refuse. |
| `internal/scope` | The review stage's scope lens, not a tenth stage: the guidance a reviewer traces each touched path against, and the note an untraced path becomes. |
| `internal/store` | The only package that opens the database: the schema, its additive migrations, and typed accessors for repositories, runs, stages, rounds, checkpoints, tasks, task state and events, holds, and the gate ownership index. |
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
