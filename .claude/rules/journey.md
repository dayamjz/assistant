---
paths: ["internal/journey/**"]
---

# internal/journey

- It takes the git exception (`Git` and `GitWith` build git command lines and
  run them, under the isolation the fixture built its subject with) for the
  reason `internal/fixture` takes it: a harness that confirmed the product's
  own git operation by asking the package under validation would be reporting
  that package agreeing with itself.
- Mechanism rather than rule: a check is a `Check[O]`, a list of `Clause[O]`
  over a typed observation plus the counterfeit observations it must reject.
  `Verify` runs both halves on every invocation, and a check naming no
  counterfeit is refused, as is one carrying a clause no counterfeit reaches,
  so neither a check nor a part of one that nobody has shown can fail may
  ship.
- A counterfeit is a mutation of the real observation rather than one
  written from nothing. Keeping the mutation inside a shape the product could
  have produced is a rule the writer keeps.
  A clause asserting an absence declares itself one and carries `Possible`.
- Three tables account for everything in both directions: `Coverage` against
  the principles its own tests cite, `Drives` against the planted catalog, and
  `Settlements` against the questions `internal/fixture` left open, which this
  package owns and answers rather than editing that one.
- Platform guards, on `internal/cli`'s and `internal/service`'s terms: a check
  that drives a run skips where `internal/ipc` reads no local socket peer
  credentials, and a check whose service did not come up skips where there is
  no local socket transport. The second is taken wherever a service was
  expected to come up and never wherever one is started. Either skip is
  recorded as a limit in `README.md` and in the `Coverage` note of every
  principle it takes down, and never as a list of the checks it takes down.

Read its `README.md` first, then `doc.go`: they own what a green run here
establishes, which walks reach review, push and a code host and why, and the
residual gaps.
