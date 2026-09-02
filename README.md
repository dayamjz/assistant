# assistant

**Run many coding agents at once, and let none of them ship unvalidated work.**

Assistant does two things you currently do by hand. It runs a fleet of coding
agents in parallel, each in its own isolated copy of a repository, and it puts a
validation gate in front of your remote so no change reaches a shared branch
until it has been independently reviewed, tested, documented, and linted.

## Status

Early. The product requirements are settled and checked in at
[`docs/prd.html`](docs/prd.html). Five pieces exist so far. The execution
engine in `internal/graph` is the first: the graph builder with its
construction-time checks, an executor with halt points and bounded cycles, and
checkpoints behind a four-operation store. The second is `internal/findings`,
the vocabulary every pipeline stage speaks: a finding with its severity and its
action, the report a stage returns, and the defensive parsing that turns
untrusted agent output into a validated report. The third is
`internal/config`, the configuration schema: the two-layer merge, the defaults,
the parse-time validation, and the path matcher. The fourth is `internal/vcs`,
the only package that invokes git. The fifth is `internal/store`, the durable
record of what the gate did: an embedded sqlite database behind typed
accessors, with additive migrations checked against the database's own catalog.
Nothing joins them into a pipeline yet, so there is still nothing to run.

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
| `internal/config` | The configuration schema: layers, defaults, merge, validation, path matcher. |
| `internal/findings` | The stage vocabulary: findings, actions, reports, and parsing of agent output. |
| `internal/graph` | The execution engine: nodes, edges, bounds, halt points, checkpoints. |
| `internal/store` | The only package that opens the database: the schema, its additive migrations, and typed accessors for repositories, runs, stages, rounds, checkpoints, tasks, task state and events, and holds. |
| `internal/vcs` | The only package that invokes git: typed operations over repositories, worktrees, refs, diffs, and remotes. |
| `docs/prd.html` | The product requirements. The specification this code answers to. |

## Development

```sh
make build     # build the binary into ./bin
make test      # go test -race ./...
make lint      # vet and golangci-lint
make check     # lint and test, what CI runs
```

`make test` exercises `internal/vcs` against a real git, so it needs a git
binary on `PATH`; that package's comment states the minimum version it needs.

`make lint` requires golangci-lint from the v2 series, the line that can read
this module's `.golangci.yml`. It refuses when the linter is missing or comes
from another major series rather than quietly running `go vet` alone, so a
green `make check` always means lint ran. `make lint-guard-test` proves that
refusal still fires.

## License

MIT. See [LICENSE](LICENSE).
