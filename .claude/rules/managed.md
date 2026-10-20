---
paths: ["internal/managed/**"]
---

# internal/managed

- `internal/cli`'s `managed.go` decides when it acts (refused while a service
  answers, and removal only after the service's own stop).

Read `doc.go` for what a definition starts, the protected-folder refusal, and
what was verified and what was not.
