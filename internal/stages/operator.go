package stages

import (
	"context"
	"io"

	"github.com/dayamjz/assistant/internal/redact"
)

// OperatorCommand is one command line the operator configured that runs
// outside any stage. It is run by the runner every commands.* key runs
// through, runCommand, so it is a line of shell handed to the platform's
// interpreter whole, it inherits the service's environment, its output is
// redacted on the way in, and every residual gap commandGrace states about
// that runner holds for it too, the descendant it does not end among them.
//
// It is exported for one consumer, internal/service's notify.go, which runs
// the global-only notify.command once per wake. That is the reason this
// package's runner is reached from outside it rather than a second process
// starter being written there: a configured command line has one runner.
type OperatorCommand struct {
	// Line is the command line, run through the platform's interpreter.
	Line string
	// Dir is the directory it runs in.
	Dir string
	// Stdin is written to the command's standard input, which is then
	// closed. Empty leaves standard input the null device.
	Stdin string
	// Tail bounds how much of the end of the command's output, after
	// redaction, the result keeps. Zero keeps none. Nothing else of the
	// output is kept: there is no record beside it.
	Tail int
}

// OperatorResult is what running an OperatorCommand produced. It reports
// what happened and decides nothing about what it means.
type OperatorResult struct {
	// Exited is true when the command ran and reported an exit status of its
	// own, and false for one that never started and for one something else
	// ended, such as the context.
	Exited bool
	// Code is that status, or -1 when the command reported none.
	Code int
	// Tail is the redacted end of the command's output, bounded by
	// OperatorCommand.Tail.
	Tail string
	// Err is the error from starting or waiting on the process, nil when it
	// ran and exited on its own, including when it exited non-zero, or the
	// reason its output was not read to the end.
	Err error
}

// RunOperatorCommand runs c to completion on runCommand's terms. Ending ctx
// ends the command, which the result then reports as not having exited on
// its own; a caller that bounds a command with a deadline reads why from
// the context rather than from the result.
func RunOperatorCommand(ctx context.Context, c OperatorCommand) OperatorResult {
	res := runCommand(ctx, commandSpec{
		command:    c.Line,
		dir:        c.Dir,
		stdin:      c.Stdin,
		record:     io.Discard,
		projection: c.Tail,
		grace:      commandGrace,
		redact:     redact.New(),
	})
	out := OperatorResult{Exited: res.exited, Code: res.code, Tail: res.tail, Err: res.err}
	if out.Err == nil {
		out.Err = res.short
	}
	return out
}
