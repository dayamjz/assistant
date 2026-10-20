---
paths: ["internal/agents/standin/**"]
---

# internal/agents/standin

- It implements no `agents` interface and builds no `agents.Result`: it
  prints bytes and exits with a status, and the real adapter reads that, so a
  shape the adapter cannot produce is unsayable there.
- An exchange marked `Run` has the worker run what the line carries in a
  process group the worker does not lead, the shape observed of the Claude
  Code CLI's Bash tool, so a test of a worker rule drives a worker's push or
  call that way rather than placing it in the worker's group itself.

Read `doc.go` for what a recorded `Call` does and does not prove about P4,
and for the two residual gaps. `tmux.go` owns `standin.Tmux` and
`terminal_unix.go` owns `standin.AtTerminal`. `owner_unix.go` is the one
owner watch a test's processes end with (P15), the binary `AtTerminal` runs
again and the owner sweep for a tmux server included, and `procs_unix.go`'s
`LeftAfterTests` is the check every `AtTerminal` or `standin.Tmux` package's
`TestMain` runs after its tests.
