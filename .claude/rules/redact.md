---
paths: ["internal/redact/**"]
---

# internal/redact

- `internal/store` refuses to open without it, and `internal/vcs`,
  `internal/forge`, `internal/stages`, `internal/pipeline` and
  `internal/tasks` take it.
- Which strings a report carries is `findings.Report.Rewritten`'s to say, and
  `internal/pipeline` and `internal/tasks` redact a whole report through it
  rather than through a list of their own.

Read the package comment in `redact.go` for which shapes it recognizes, what
it does not, and the residual gaps.
