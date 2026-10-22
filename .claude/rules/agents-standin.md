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
directories: the tag constants are the one list, `TempDir` stamps the
owning process's identity, and a package's `TestMain` sweeps with
`SweepTempDirs` before tests. The test that each binary has one distinct
tag type-checks the calls to `TempDir`, `SweepTempDirs`, and
`RegisterTempTag`: a variable counts as the letter assigned to it, a value
from `RegisteredTempTag` counts as the letter that binary registered, and a
call in a non-test file counts for every test binary that imports its
package. `tmux.go` owns `standin.Tmux` and
`terminal_unix.go` owns `standin.AtTerminal`. `owner_unix.go` is the one
owner watch a test's processes end with (P15), the binary `AtTerminal` runs
again and the owner sweep for a tmux server included, and its `RunOwned` is
how every `AtTerminal` or `standin.Tmux` package's `TestMain` runs its tests,
so `procs_unix.go`'s check of what they left follows them; `Tmux` refuses a
test anywhere else. `held_unix.go`'s `HeldWait` is the held process a test's
shell waits on to be released, never a release-file loop of its own (#432,
#440), and `Main` dispatches it.
