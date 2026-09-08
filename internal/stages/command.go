package stages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// commandGrace is what a configured command's output is given to arrive once
// the command itself has ended, and what os/exec's WaitDelay is set to.
//
// It is what two waits are measured against. The first is this file's own read
// of the command's output after the command exited, which is where a
// descendant still holding the pipe would otherwise hold the call for as long
// as it lives. The second is os/exec's wait for the command to end after the
// run was cancelled.
//
// The first of those is a bound on unix and not everywhere. What ends that
// wait is closing the read end, and os.File.Close undertakes to cancel a
// pending operation only on a file that supports os.File.SetDeadline. A pipe
// from os.Pipe supports one on unix, which is what makes the bound hold there
// and what the unix-only give-up test exercises. On Windows it does not: the
// handles os.Pipe returns there answer os.ErrNoDeadline, so Close carries no
// documented promise to end a read already in flight, and this package
// establishes nothing about what a descendant holding that handle does to the
// wait. Nothing in this repository has run this code on that platform.
//
// That asymmetry is inherited rather than introduced by reading the output
// here: os/exec closed its own parent pipes and then waited on the same copy
// in the same order when it owned it.
//
// Both waits begin after the command exited or after the run was cancelled, so
// this bounds neither how long the command itself runs nor what the command
// leaves behind.
//
// Nothing here bounds the command's own runtime. There is no stage-level
// timeout and no configuration key holding one, so a configured command that
// hangs holds the run's segment until an operator cancels the run, which is
// the recourse that ends it. A duration would belong in internal/config's key
// table, which is that schema's single owner, so adding one is its own change
// rather than something this file may decide.
//
// Nor does it end a descendant. Giving up on the output closes this side of
// the pipe and nothing more: this file starts one process and ends that one
// process, and a child the command left behind goes on running with the
// descriptor it inherited. internal/agents terminates the process tree an
// agent invocation started, and nothing here does the equivalent for a
// configured command, so a command that starts something and detaches leaves
// it behind. That is a gap rather than a decision this file is entitled to
// make quietly.
const commandGrace = 5 * time.Second

// errOutputAbandoned is what a result carries when the command's output was
// still arriving after the grace expired and this call stopped reading it.
// Whatever was still unread is not in the record and is not in the
// projection, so a caller holding this may not offer either as whole.
var errOutputAbandoned = errors.New("stages: gave up reading the command's output")

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
	// run's test evidence, and it is written while the command runs rather
	// than afterwards, so output survives a command this call gives up
	// waiting on.
	record io.Writer
	// projection bounds the tail of that output kept in memory for the
	// stage's report. Zero keeps none.
	projection int
	// grace is how long the command's output is given to arrive after the
	// command ended, and what os/exec's WaitDelay is set to. The stage passes
	// commandGrace; a caller passing zero leaves both of the waits described
	// there unbounded, so a descendant holding the pipe holds the call. It is
	// a field rather than the constant read directly so a test can reach the
	// give-up path without waiting the stage's grace out.
	grace time.Duration
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
	// short is set when the read of the command's output did not reach the
	// end of it: the grace expired with the pipe still open, or the read
	// failed. Neither the record nor the projection then holds the whole
	// output, so a caller may not offer either as whole.
	//
	// It is independent of the exit status, because runCommand reads the
	// output itself rather than asking os/exec whether it managed to. A
	// command that exits non-zero and is cut short carries both its status
	// and this.
	short error
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
//
// # Why this owns the pipe
//
// The output pipe is created here and read here, rather than handed to os/exec
// as an io.Writer for it to copy. That is not the shorter way to write this,
// and the shorter way was tried: os/exec will report a wait-delay overrun or a
// copy failure from Cmd.Wait, and a guard can read it off that error. It
// cannot fire where it matters. Cmd.Wait sets its error to an ExitError
// whenever the command exited non-zero, and takes the wait-delay and copy
// errors only where that error is still nil, so on the failing branch - the
// one that produces a fix finding pointing an operator at the record - both
// are discarded before any caller sees them. No inspection of Wait's error
// recovers them.
//
// Handing os/exec an *os.File instead makes it pass that descriptor to the
// child directly and start no copy of its own, which is the point: whether the
// read reached the end of the output becomes a fact this file holds for every
// exit status rather than one os/exec decides whether to report.
//
// What that does not buy is any hold over the command's descendants. Giving up
// closes this side of the pipe; a child that inherited the other side keeps
// it, and keeps running. commandGrace says what is and is not bounded.
func runCommand(ctx context.Context, spec commandSpec) commandResult {
	tail := &tailWriter{limit: spec.projection}
	out := io.MultiWriter(spec.record, tail)
	settle := func(res commandResult) commandResult {
		res.tail, res.omitted = tail.projection()
		return res
	}

	read, write, err := os.Pipe()
	if err != nil {
		return settle(commandResult{code: -1,
			err: fmt.Errorf("opening a pipe for the command's output: %w", err)})
	}
	defer func() { _ = read.Close() }()

	cmd := shellCommand(ctx, spec.command, spec.dir)
	// Standard input is left unset, which os/exec reads as the null device, so
	// a command that reads it reaches end of file at once rather than waiting
	// for a person who is not there. Standard output and standard error are
	// the one descriptor, so the two arrive interleaved as the command wrote
	// them; os/exec passes the same file to both rather than duplicating it.
	cmd.Stdout = write
	cmd.Stderr = write
	cmd.WaitDelay = spec.grace

	if err := cmd.Start(); err != nil {
		_ = write.Close()
		return settle(commandResult{code: -1, err: err})
	}
	// os/exec does not close a descriptor a caller supplied, and the child now
	// holds its own copy. Closing the parent's is what lets the read below
	// reach end of file once the last holder is gone; leaving it open would
	// hold every run for ever, since this process would itself be the writer
	// the read is waiting on.
	_ = write.Close()

	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, read)
		copied <- err
	}()

	// The copy is already draining while the command runs, so waiting on the
	// command cannot deadlock against a command that outruns the pipe buffer.
	waitErr := cmd.Wait()

	res := commandResult{code: -1, short: awaitOutput(copied, read, spec.grace)}
	if cmd.ProcessState == nil {
		res.err = waitErr
		return settle(res)
	}
	res.exited = cmd.ProcessState.Exited()
	res.code = cmd.ProcessState.ExitCode()
	if !res.exited {
		res.err = waitErr
	}
	return settle(res)
}

// awaitOutput waits for the read of the command's output to finish and returns
// nil when it reached the end of it. It is called after the command has ended,
// so anything still arriving is a descendant writing to the descriptor it
// inherited.
//
// A grace of zero waits without bound, which is what a caller that named no
// grace asked for.
//
// The grace expiring is not by itself what ends the wait: closing the read end
// is, and this call then waits for the read to return before answering, so
// nothing is still writing to the record or the projection when the caller
// reads them. Waiting is the deliberate half of that - abandoning the read
// instead would leave it writing into both after this returned. A platform
// where that close left a read in flight therefore leaves this call waiting on
// it, and commandGrace says which platforms undertake to end it.
func awaitOutput(copied <-chan error, read *os.File, grace time.Duration) error {
	if grace <= 0 {
		return <-copied
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-copied:
		return err
	case <-timer.C:
	}
	// Closing this side of the pipe is what ends the read; the read then fails
	// because of that close, so what it answers describes this call rather
	// than the output, and the abandonment is reported instead. The read is
	// waited for so nothing is still writing to the record or the projection
	// when the caller reads them.
	_ = read.Close()
	<-copied
	return fmt.Errorf("%w: it was still arriving %s after the command ended", errOutputAbandoned, grace)
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
// What writes here is runCommand's own copy off the command's output pipe,
// which delivers what each read of that pipe returned, so a command that
// prints without bound arrives here in pieces rather than in one.
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
