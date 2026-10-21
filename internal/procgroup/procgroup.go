// Package procgroup runs one external program to completion as the leader of
// a process group of its own, and ends that whole group when the call returns
// or its context ends. It is PRD section 8's "the program runs in a process
// group that the invoker ends when the call returns or times out", as one
// mechanism the packages that invoke short-lived programs share rather than
// each writing their own: internal/vcs runs git through it,
// internal/forge runs gh, internal/stages a configured command line,
// internal/agents its forceful tree kill, and internal/agents/standin a Run
// exchange's command, and internal/agents waits on its lsof run through
// AwaitExit.
//
// It decides nothing about what a program's exit means. Run returns what
// os/exec's Wait reported, or a refusal of its own, and the caller reads its
// context's error to tell an expiry from an answer.
//
// # The one rule about signalling
//
// A group is signalled only while its leader is unreaped. A group's
// identifier is its leader's process identifier, and that identifier cannot
// be given to another process while the leader is a zombie nobody reaped, so
// a signal sent then reaches this call's group and nothing else. That is
// mechanism rather than timing on the platforms Run groups on: nothing in
// Run reaps the leader before cmd.Wait, which it calls only after its last
// signal, and AwaitExit observes the leader's exit without reaping it.
//
// # Residual gaps
//
//   - A descendant that left the group, by setsid or setpgid of its own, is
//     reached by no signal here. The grace bounds how long it can keep the
//     call waiting on an output pipe, and nothing ends it.
//   - A caller's reader or writer that blocks cannot be interrupted, so Run
//     returns without the goroutine blocked in it: one reading the caller's
//     reader stays until that one read returns, and one writing to the
//     caller's writer until that one write returns. Neither touches the
//     child, and neither calls the caller's reader or writer again: the
//     input copier checks that Run closed the input before every read, and
//     the output copier's next read is of a pipe end Run closed. Run's doc
//     says when each can happen.
//   - A descendant that stays in the group is ended when the leader exits,
//     including one a program started on purpose to outlive the call. What
//     that costs is whatever that descendant was for.
//   - When this process itself is killed outright, nothing here runs, and a
//     group it started goes on until it exits by itself. No record names the
//     group, so no later incarnation reclaims it.
//   - A leader SIGKILL does not end within the grace is left to a goroutine
//     that reaps it once it exits, and Run answers ErrAbandoned without it.
//   - Anything else in this process that reaped every child, a wait on any
//     process, would undo the unreaped guarantee. Nothing in this module
//     does.
//   - Only linux and darwin group a call. Everywhere else, Windows included,
//     Run ends only the program itself when the context ends, reaches no
//     descendant at all, and does not bound the wait for a killed program.
package procgroup

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// ErrAbandoned is Run's answer when the context ended, the group was sent
// SIGKILL, and the leader still had not exited within the grace. The leader is
// reaped by a goroutine once it does exit, and nothing it wrote is reported.
var ErrAbandoned = errors.New("procgroup: the program did not exit within the grace after its group was killed")

// ErrMisconfigured is Run's refusal of a command it cannot start on its own
// terms: one built with exec.CommandContext, whose own cancellation would end
// the leader alone on a schedule Run does not own; one whose SysProcAttr is
// already set, which Run would have to overwrite to start the group; or a
// grace of zero or less, which would bound nothing.
// Nothing is started.
var ErrMisconfigured = errors.New("procgroup: the command cannot be run as a group of its own")

// Bound returns ctx itself when it already carries a deadline, and otherwise
// ctx with a deadline d from now. It is how a caller gives an invocation a
// deadline when the caller's own context has none, which is what a context
// derived through context.WithoutCancel, or context.Background, carries. The
// cancel function is always safe to call and must be.
//
// A caller's deadline is kept even when it is later than d: Bound adds a
// deadline where there is none and never shortens one a caller chose.
func Bound(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// RunStarted is Run that calls started with the program's process identifier
// once it has started and before Run waits on it, so a caller can read what
// the operating system says of the running program, such as the group it
// leads, while the identifier still names it: nothing reaps the program
// before started returns, since only Run's own cmd.Wait does, and Run calls
// that after started. The program may already have exited when started is
// called, and an exited program is then unreaped rather than gone.
//
// started is called on Run's goroutine, at most once, and only for a program
// that started; Run neither bounds nor interrupts it, so a started that
// blocks holds the call past ctx and the grace alike, and the program's
// streams are copied meanwhile. Everything else is Run's.
func RunStarted(ctx context.Context, cmd *exec.Cmd, grace time.Duration, started func(pid int)) error {
	return run(ctx, cmd, grace, started)
}
