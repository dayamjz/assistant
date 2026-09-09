package stages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/redact"
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
// and what the give-up test exercises. That test is unix-only because leaving
// a descendant holding a pipe has no portable spelling, so the abandoned read
// is the one path here no test reaches on another platform. On Windows the
// handles os.Pipe returns answer os.ErrNoDeadline, so Close carries no
// documented promise to end a read already in flight, and this package
// establishes nothing about what a descendant holding that handle does to the
// wait.
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
	// standard error interleaved as the command produced them, after redact
	// has removed what it recognizes. It is the run's test evidence, and it
	// is written while the command runs rather than afterwards - a line at a
	// time, as redaction completes each one - so output survives a command
	// this call gives up waiting on.
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
	// redact is applied to the command's output on its way into the record
	// and the projection, upstream of both, so that the projection's bound
	// cannot cut a credential's recognizable shape apart before it is
	// removed. The zero value redacts, so a caller cannot leave it off. What
	// it removes and what it misses is internal/redact's contract;
	// redactingWriter owns the one gap this stream adds to it.
	redact redact.Redactor
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
	// tail is the bounded projection of the output as redacted, ending at
	// the last thing the command wrote.
	tail string
	// omitted is how many bytes of earlier output the projection leaves out,
	// counted over the redacted stream the record holds, so the count and
	// the file it points a reader at measure the same text.
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
// output to spec.record and keeping a bounded tail of it. The output passes
// through spec.redact on its way to both, so neither the record nor the tail
// is byte-identical to what the command printed; redactingWriter says how and
// names the gap that buys.
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
		// The redaction sits upstream of the record and the projection both,
		// so one pass covers the two and the projection's bound cannot cut a
		// credential's shape apart before it was seen whole. The flush runs
		// before copied is signalled, so the waits below still guarantee that
		// nothing is writing to either when the caller reads them.
		redacting := &redactingWriter{dst: out, redact: spec.redact}
		_, err := io.Copy(redacting, read)
		if flushErr := redacting.flush(); err == nil {
			err = flushErr
		}
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
	// Closing this side of the pipe is what ends a read still in flight; that
	// read then fails because of the close, so what it answers describes this
	// call rather than the output, and the abandonment is reported instead.
	// The read is waited for so nothing is still writing to the record or the
	// projection when the caller reads them. A read that drains nil here
	// reached the end of the output in a photo-finish with the timer, and an
	// output read whole is reported whole.
	_ = read.Close()
	if err := <-copied; err == nil {
		return nil
	}
	return fmt.Errorf("%w: it was still arriving %s after the command ended", errOutputAbandoned, grace)
}

// redactBound is the most of a single unfinished line redactingWriter holds
// before passing part of it on, so a command that prints without a line break
// is bounded here the way tailWriter bounds the projection.
const redactBound = 64 << 10

// redactingWriter removes credentials from a stream on its way to dst.
//
// Redaction is over whole lines rather than over each Write, because what
// arrives here is whatever one read of the command's output pipe returned, and
// the boundary between two reads can fall inside a URL; each half of a
// credential split that way carries no shape internal/redact recognizes, so
// redacting the halves keeps the secret. The shape it recognizes spans no line
// break, so a complete line cannot split it.
//
// A line that outgrows redactBound is not held whole. Enough of it goes on to
// keep the hold bounded, cut at its last space or tab where one falls late
// enough, because the recognized shape spans no whitespace either. That leaves
// this stream's one addition to internal/redact's own gaps: a single
// whitespace-free token longer than redactBound goes on in pieces, and a
// credential whose URL shape straddles such a cut is not recognized in either
// piece.
//
// flush redacts and passes on whatever is still held, so once the stream has
// ended, dst holds everything that arrived.
type redactingWriter struct {
	dst    io.Writer
	redact redact.Redactor
	buf    []byte
}

// Write implements io.Writer. It consumes all of p whatever it passed on, and
// carries dst's error if passing some of it on failed.
func (w *redactingWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	// Everything through the last line break is complete and goes now.
	cut := bytes.LastIndexByte(w.buf, '\n') + 1
	if len(w.buf)-cut > redactBound {
		least := len(w.buf) - redactBound
		if at := bytes.LastIndexAny(w.buf[cut:], " \t"); at >= 0 && cut+at+1 >= least {
			cut += at + 1
		} else {
			cut = least
		}
	}
	return len(p), w.emit(cut)
}

// emit redacts the first n held bytes and passes them on.
func (w *redactingWriter) emit(n int) error {
	if n == 0 {
		return nil
	}
	_, err := io.WriteString(w.dst, w.redact.Redact(string(w.buf[:n])))
	w.buf = w.buf[:copy(w.buf, w.buf[n:])]
	return err
}

// flush redacts and passes on what is still held: the final line of a stream
// that did not end in one.
func (w *redactingWriter) flush() error { return w.emit(len(w.buf)) }

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
// What writes here is runCommand's redacting writer over its copy off the
// command's output pipe, which passes text on in redacted pieces it bounds by
// redactBound and by what each read of that pipe returned, so a command that
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
