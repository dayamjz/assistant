---
paths: ["internal/stages/**"]
---

# internal/stages

- A stage with no body is `Pending`, which reads nothing and reports one
  `ask` finding, so P3 holds the stage for a person and no stage reports a
  pass it did not establish.
- The one owner of which stages have a body is the `written` table: `All`
  places implementations from it and `Implemented` reports it, so adding a
  body is adding an entry.
- PRD section 5's judge rule lives in `judge.go`.
- A body is handed a `StageDeps` at construction, carrying the adapters that
  do not vary with the run; a fact that does vary is a declared state key in
  `internal/pipeline` instead. Lifetime decides which, and `deps.go` has that
  argument and the P4 reason `Agent` is a `StageAgent`.
- A fix body is handed a `FixDeps` instead, a separate type for the same
  reason: it carries a route to the run's fixer, and a stage body that could
  reach one could fix what it is about to report on. Do not merge them.
- Three things about the intent stage generalize:
  - PRD section 5's "this stage never blocks a run" is owed by the
    implementation and not by `internal/pipeline`, which refuses to enforce
    it structurally because a stage that could not hold would have to drop an
    ask finding. The test runs every path it has and is itself checked
    against a report that blocks, so the assertion cannot pass vacuously.
  - A body landing can move where a run stops, so a test may not name the
    stage it expects a hold at: the helpers in `internal/cli` and
    `internal/service` derive a run's stops from `stages.Holding`, which owns
    which stages hold under a configuration (having a body and holding are
    different questions). `internal/journey` names its body-less stages and
    the stages a run stops at in declarations checked against the build, so
    landing a body means writing it down there too.
  - A body refusing what its run cannot give it cannot be walked past
    either, because it fails rather than holding, so `internal/service`'s
    answer-to-the-end test skips such a stage for the one run instead of the
    body softening.
- A body reads another stage's record through `pipeline.StageResultKeys` and
  `pipeline.ReadStageResult`, so `internal/pipeline` stays the one owner of
  the report encoding.
- A stage implements the part of its PRD section the phase list has reached
  and ships no seam for the rest; what deferred work inherits is a note in
  the package documentation rather than an unwired interface.
- Exported surface answers to a consumer: one that exists today, or specified
  work whose absence would otherwise have each of several consumers re-cut
  the same file. A seam is the second case and names in its own doc which
  consumers it answers to. Surface added because deferred work might plug
  into it answers to nobody and is dropped in review.

Read the package comment in `stages.go` before changing `Pending`, the
`written` table or the intent stage's deferred half, and `judge.go`,
`deps.go` and `fix.go` for their rules and residual gaps.
