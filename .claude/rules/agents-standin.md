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
and for the two residual gaps. `tempdir.go` owns a test's temporary
directories. `RegisterTempTag` records the caller's directory within its
module as the namespace. '_' encodes as `_0` and '/' as `_1`, so a slash
and an underscore do not collapse. An absolute path is made relative by
walking up to `go.mod`. A non-absolute path is accepted only when it begins
with the main module path recorded in the binary, which is the form
`-trimpath` leaves, and that path is not opened. `TempDir` and
`SweepTempDirs` take no tag and use only that namespace, and they refuse
when none is recorded. A package's `TestMain`
registers and sweeps before tests. A test file that calls `os.MkdirTemp`
fails the type-checked scan of those calls; the names `TempDir` cannot
make, an unparseable one and a replaced identity, are planted from
`tempdir.go`. A live owner and a name that does not parse are left.
`tmux.go` owns `standin.Tmux` and
`terminal_unix.go` owns `standin.AtTerminal`. `owner_unix.go` is the one
owner watch a test's processes end with (P15), the binary `AtTerminal` runs
again and the owner sweep for a tmux server included, and its `RunOwned` is
how every `AtTerminal` or `standin.Tmux` package's `TestMain` runs its tests,
so `procs_unix.go`'s check of what they left follows them, except when the
binary only lists test names (`testListOnly`), when `RunOwned` runs the
listing alone; `Tmux` refuses a test anywhere else. `held_unix.go`'s
`HeldWait` is the held process a test's shell waits on to be released, never
a release-file loop of its own (#432, #440), and `Main` dispatches it.
