---
paths: ["internal/pipeline/**"]
---

# internal/pipeline

- P2 is structural rather than checked: a `Stages` is nine named fields, so
  another order, a missing stage, and an added one are unsayable, and the order
  itself is the unexported stage table in `stage.go`.
- A checkpoint's state must hold exactly the declared keys, so a key added to
  `key.go` refuses every older checkpoint unless its row is late
  (`graph.Key.Late`), which is only for a key whose zero value is how a run
  without it behaved.
- For `suppress_project_instructions` there is no second half in
  `internal/agents`, which implements no suppression, so `New`'s check is the
  only enforcement of PRD section 10's refusal before launch.
- A fixer reports nothing that can clear a stage: the one finding it may return
  is an `ask` in `FixOutput.Holds`, anything else is `ErrFixerFinding`, and the
  stage node adds those holds to the stage's next report itself, so no stage
  body can drop one.

Read `doc.go` before changing the loop or the holds, and for the residual gaps;
`repush.go` and `repair.go` own the two paths back from the checks stage and
the bound their hold exits carry.
