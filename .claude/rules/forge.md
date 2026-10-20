---
paths: ["internal/forge/**"]
---

# internal/forge

- Everything provider-specific stays behind `Provider`.
- `Host.Open` takes the repository, which reaches a run as
  `pipeline.KeyForgeRepository`, derived in `internal/service`'s `begin` from
  the run's own repository row.
- `Provider.Merge` is bound to a head through gh's `--match-head-commit` or
  GitLab's `sha` and refuses an unbound one; whether a merge is allowed is the
  host's, and its refusal is a `ReasonRejected` the caller reports and never
  retries around. `internal/service`'s `autonomy.go` is its one caller: the
  global-only `autonomy.merge` list, the live read, and the read after the
  merge that writes the merge record.
- The run carries the fork as `pipeline.KeyForkRepository` (late, empty means
  the upstream), and `internal/stages`' `pushTarget` is the one place it picks
  `ForkRemote`, so the rebase stage's anchor and the push are on the fork's
  branch together.

Read `doc.go` before changing any of that: it owns the checks model, the
one-repository `Provider` and the three measures that keep a write on the
repository the run named, the fork's two specifiers, the GitLab section (where
its model differs from GitHub's and its gaps), the stand-ins `forgetest` and
`labtest`, and the residual gaps.
