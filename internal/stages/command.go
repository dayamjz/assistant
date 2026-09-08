package stages

import (
	"context"
	"io"
	"strings"
	"time"
)

// commandGrace is what os/exec's WaitDelay is set to, which bounds two waits
// that would otherwise be unbounded: how long the call waits on the output
// pipes once the command has exited, and how long it waits for the command to
// end after the run was cancelled. Either one is a descendant holding
// something the call is waiting for, and either way the call ends after this
// rather than stalling the run on a process nothing is going to reap.
//
// It bounds the run and not the machine. What it does not do is end that
// descendant: this file starts one process and ends that one process, and a
// child the command left behind goes on running. internal/agents terminates
// the process tree an agent invocation started, and nothing here does the
// equivalent for a configured command, so a command that starts something and
// detaches leaves it behind. That is a gap rather than a decision this file is
// entitled to make quietly.
const commandGrace = 5 * time.Second

// commandSpec is one configured command line to run.
type commandSpec struct {
	// command is the command line, run through the platform's command
	// interpreter rather than split here. PRD section 10 makes a commands.*
	// value a line of shell, so the interpreter is what decides where its
	// words end.
	command string
	// dir is the directory it runs in.
	dir string
	// record receives everything the command wrote, standard output and
	// standard error interleaved as the command produced them. It is the
	// authoritative full output PRD section 8 asks for, and it is written
	// while the command runs rather than afterwards, so output survives a
	// command this call gives up waiting on.
	record io.Writer
	// projection bounds the tail of that output kept in memory for the
	// stage's report. Zero keeps none.
	projection int
}

// commandResult is what running one command produced. It reports what
// happened and decides nothing about what it means; the stage classifies it.
type commandResult struct {
	// exited is true when the command ran and reported an exit status of its
	// own. It is false both for a command that never started and for one
	// something else ended, which are different things to report than a
	// status.
	exited bool
	// code is that status, or -1 when the command reported none.
	code int
	// tail is the bounded projection of the output, ending at the last thing
	// the command wrote.
	tail string
	// omitted is how many bytes of earlier output the projection leaves out.
	omitted int64
	// err is the error from starting or waiting on the process. It is nil
	// when the command ran and exited on its own, including when it exited
	// non-zero, because a non-zero status is the answer rather than a failure
	// to obtain one.
	err error
}

// runCommand runs one configured command line to completion, writing its whole
// output to spec.record and keeping a bounded tail of it.
//
// The command inherits this process's environment. PRD section 10 has these
// commands run with the operator's own credentials, so withholding part of
// that environment would change what the operator configured rather than
// contain it, and this call is not the place that decides which commands are
// trusted enough to run.
//
// Cancelling ctx ends the command, and the result then reports a command that
// did not settle. A caller that cancelled deliberately should say so from the
// context rather than read that result as a verdict.
func runCommand(ctx context.Context, spec commandSpec) commandResult {
	tail := &tailWriter{limit: spec.projection}
	out := io.MultiWriter(spec.record, tail)

	cmd := shellCommand(ctx, spec.command, spec.dir)
	// Standard input is left unset, which os/exec reads as the null device, so
	// a command that reads it reaches end of file at once rather than waiting
	// for a person who is not there.
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = commandGrace

	err := cmd.Run()

	text, omitted := tail.projection()
	res := commandResult{tail: text, omitted: omitted, code: -1}
	if cmd.ProcessState == nil {
		res.err = err
		return res
	}
	res.exited = cmd.ProcessState.Exited()
	res.code = cmd.ProcessState.ExitCode()
	if !res.exited {
		res.err = err
	}
	return res
}

// tailWriter keeps the last limit bytes written to it and counts what it drops.
// The tail rather than the head is kept because a check that fails says so at
// the end, and a caller shown the beginning of a long run would be shown the
// part that passed.
//
// It is written to while the command runs rather than over the output
// afterwards, so a command that prints without bound is bounded here as it
// prints. The whole output is not lost by that: it goes to the record this
// writer is paired with.
type tailWriter struct {
	limit   int
	buf     []byte
	dropped int64
}

// Write implements io.Writer and never fails, so a command whose output
// overran the limit is not also a command whose output could not be collected.
//
// What it holds between one write and the next is the limit plus that write,
// so the memory this costs is bounded by the limit and by whoever is writing.
// os/exec copies a command's output through a buffer of its own, so a command
// that prints without bound arrives here in pieces rather than in one.
func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 {
		w.dropped += int64(n)
		return n, nil
	}
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.limit; over > 0 {
		w.dropped += int64(over)
		copy(w.buf, w.buf[over:])
		w.buf = w.buf[:w.limit]
	}
	return n, nil
}

// projection returns the tail and how many bytes of earlier output it leaves
// out. It does not modify the writer, so asking twice answers the same both
// times.
//
// A projection that starts mid-line starts at the next line instead, and the
// bytes that costs are counted as omitted rather than quietly dropped. It does
// that only where a whole line follows: a command whose output has no line
// break inside the tail keeps its partial line, because the alternative is an
// empty projection, which tells a reader less than a fragment does. What is
// trimmed from the end is trailing newlines alone, which is presentation and
// leaves nothing out.
func (w *tailWriter) projection() (text string, omitted int64) {
	text, omitted = string(w.buf), w.dropped
	if omitted > 0 {
		if at := strings.IndexByte(text, '\n'); at >= 0 && at+1 < len(text) {
			omitted += int64(at) + 1
			text = text[at+1:]
		}
	}
	return strings.TrimRight(text, "\n"), omitted
}
