---
paths: ["internal/home/**"]
---

# internal/home

- It owns the on-disk layout PRD section 8 gives one root, the exclusive lock
  that makes a home have exactly one service, its probe `LockState`, and the
  instance file on both sides, `WriteInstance` and `ReadInstance`.
- `TerminalSocket` is a tmux-hosted worker's socket, named by the session
  package and composed here.
- `Discarding` names a confirmed discard's directory beside a copy from the
  copy's path alone (`copyPlace`, `CopyAt`'s layout half), since the copy
  may already have moved away; `DiscardingStage` is where its first
  journal is written before it is renamed into place.

Read `doc.go` for the residual gaps.
