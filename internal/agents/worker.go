package agents

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"
)

// WorkerRunner is a Runner that can be started as a long-lived process driven
// over its standard input, which is what CapabilityWorkerSessions names. An
// adapter implements it exactly when it declares that capability; Resolve
// refuses one where the two disagree, in either direction.
//
// A caller holding a Runner reaches this through OpenWorker, which reads the
// declaration first, on the terms OpenFixer states for SessionRunner: the
// assertion is still expressible, since this interface is exported, and
// Resolve is what makes that harmless for a run.
//
// It sits on the fixer side of P4's split. Start takes no purpose, and a
// WorkerSpec has no shape, so a worker cannot be told it is a review in
// either of the two ways an Invocation can say so. What it has instead is
// memory for as long as it runs, which is why nothing here reaches it from a
// Runner without the declaration.
type WorkerRunner interface {
	Runner

	// Start launches one worker in spec.Dir, delivers spec.Instructions on
	// its standard input, and returns the handle. The process is the leader
	// of a process group of its own, exactly as an invocation's is, and the
	// handle reports that group.
	//
	// ctx bounds the start, including the delivery of the instructions, and
	// nothing after it: a worker outlives the call that started it and is
	// ended by Close. A start ctx ends is cleaned up before this returns.
	//
	// A spec that cannot be started as written is refused before any process
	// starts, with an error wrapping ErrInvalidInvocation.
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}

// WorkerSpec is everything a worker is started with. It has no purpose and no
// shape on purpose: a purpose is Runner.Run's parameter and a shape is an
// Invocation's field, and a worker is neither a stage's invocation nor a fix
// round. It is also where the environment the worker runs in is decided, and
// that environment is built by the one builder this package has, with one
// rule more that Start applies and Runner.Run does not: the variables that
// would point git at another repository are removed, through
// vcs.StripRedirecting, after the spec's own Env has been applied. So a
// GIT_DIR inherited from a hook or from the service, and one a caller put in
// Env, both stay out of the worker.
type WorkerSpec struct {
	// Dir is the absolute directory the worker runs in. It is required. What
	// it proves is nothing: this package starts the worker where it is told
	// and does not ask whether that is an isolated copy, which is the
	// question PRD principle P11 gives whoever launches a task.
	Dir string
	// Instructions is what the worker is told first, on its standard input,
	// framed the way Worker.Send frames text for the adapter that starts it.
	// It is required, it is bounded by MaxPromptBytes before any framing, and
	// it is content: nothing here records it.
	Instructions string
	// Env are environment variables set for this worker only, applied over
	// the environment the runner was built with and before the redirecting
	// variables are removed. A value may be a credential, so no part of it is
	// recorded.
	Env map[string]string
}

// Validate reports whether the spec can be started at all. Every refusal
// wraps ErrInvalidInvocation and names the field, as an Invocation's does,
// since a spec is the whole of what a worker is invoked with.
func (s WorkerSpec) Validate() error {
	if strings.TrimSpace(s.Instructions) == "" {
		return &invocationFieldError{field: "Instructions", reason: "instructions are required"}
	}
	if len(s.Instructions) > MaxPromptBytes {
		return &invocationFieldError{
			field:  "Instructions",
			reason: fmt.Sprintf("%d bytes exceeds the limit of %d", len(s.Instructions), MaxPromptBytes),
		}
	}
	if err := validateDir(s.Dir); err != nil {
		return err
	}
	return validateEnv(s.Env)
}

// Worker is one running worker as whoever started it holds it. It is a
// handle over a process and nothing else: it is not a Runner, it opens no
// session, and it produces no Result, so a caller holding one has what it
// needs to drive and end the process and no route to anything this package
// keeps behind a declaration.
//
// A Worker is safe for concurrent use. Sends are serialized against each
// other and against the instructions, so text arrives in the order it was
// sent, and a send that ctx cut off keeps its place in that order: the next
// send waits for it to drain or for the input to close before writing
// anything. What that costs is stated on Send.
type Worker interface {
	// Send delivers text on the worker's standard input as one line, framed
	// the way the adapter that started the worker frames it: the scripted
	// adapter writes text itself, followed by a newline when it does not end
	// in one, and the Claude Code adapter writes one stream-json user message
	// carrying text exactly, on the terms its Start states. Either way the
	// worker reads lines, and one Send is one unit of what it reads.
	// It returns once the text has been written, or ErrWorkerEnded when the
	// process is no longer running, so nothing is reported delivered to a
	// process that is gone. ctx bounds the wait: a worker that is not
	// reading its input holds the write until it does, and a caller that
	// cannot wait that long ends its wait through ctx.
	//
	// A send ctx ended returns ctx's error and leaves the write pending,
	// because the bytes already in the pipe cannot be taken back and the
	// rest must follow them or a later send would interleave with them. So
	// the pending write is drained first by whatever comes next: a later
	// Send waits on it under its own ctx and writes nothing until it has
	// drained, and Close ends it by closing the input. Every waiting Send
	// honours its own ctx, whether it is waiting for the pending write or
	// for its turn behind another Send, so a worker that never reads turns
	// every later Send into a wait that its own ctx or Close ends, which is
	// the worker's doing and is visible as such.
	Send(ctx context.Context, text string) error

	// Capture returns the most recent output the worker has written on
	// standard output and standard error, interleaved in the order they were
	// read, up to the runner's output limit. Output that fell off the front
	// is counted rather than kept.
	Capture() Capture

	// Group is the identifier of the process group the worker leads. It is
	// what a caller registers wherever it keeps track of workers; this
	// package registers it nowhere, because which task a worker belongs to is
	// not a fact this package holds. On a platform without process groups it
	// is the leader's process identifier.
	Group() int

	// Done is closed once the worker's process has exited and been reaped,
	// whether on its own or through Close.
	Done() <-chan struct{}

	// Close ends the worker: its standard input is closed, and its process
	// tree is terminated politely first and forcefully after the runner's
	// grace period, on the terms the package documentation states for an
	// invocation's tree. It returns once the tree has been terminated, it is
	// idempotent, and a worker that has already exited costs it nothing.
	Close()
}

// Capture is what Worker.Capture hands back.
type Capture struct {
	// Text is the retained output, which is the most recent of it.
	Text string
	// Dropped is how many bytes of earlier output were discarded to keep Text
	// within the limit. Zero means Text is everything the worker wrote.
	Dropped int64
}

// OpenWorker starts a worker on r. It is the only route to one this package
// offers a caller holding a Runner, and it reads the declaration before it
// looks at the type, exactly as OpenFixer does for a fixer session.
//
// A refusal comes in one of three shapes. An adapter that has not declared
// worker sessions is refused with a *CapabilityError naming the capability.
// One that declared them and is no WorkerRunner is refused with an
// *AdapterError wrapping ErrAdapterDeclaration, which Resolve would already
// have caught for an adapter it returned. A spec that cannot be started as
// written is refused by the adapter with an error wrapping
// ErrInvalidInvocation.
func OpenWorker(ctx context.Context, r Runner, spec WorkerSpec) (Worker, error) {
	if !r.Capabilities().Has(CapabilityWorkerSessions) {
		return nil, &CapabilityError{
			Agent:      r.Name(),
			Capability: CapabilityWorkerSessions,
			Path:       "the worker session",
		}
	}
	workers, ok := r.(WorkerRunner)
	if !ok {
		return nil, declaredWithoutMechanism(r.Name(), CapabilityWorkerSessions)
	}
	return workers.Start(ctx, spec)
}

// procWorker is the Worker over one process this package started. It owns
// the process tree on the same terms runProcess does, with the difference
// that the tree is ended by Close or by the leader's own exit rather than by
// the return of a call.
type procWorker struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *tailWriter
	grace time.Duration
	// done is closed by the wait goroutine once the leader has been reaped
	// and whatever it left in its group has been terminated.
	done chan struct{}

	// slot serializes writes to standard input, the instructions among
	// them. It is a one-place channel rather than a mutex so that a sender
	// waiting its turn waits under its own ctx: a mutex would hold every
	// queued sender until the one ahead returned, whatever their contexts
	// said. pending is the write a ctx cut off and left in flight, nil when
	// there is none; it is read holding the slot, and whoever holds the slot
	// next drains it before writing, which is what keeps the order of sends
	// the order of bytes on the pipe.
	slot    chan struct{}
	pending chan error
	// frame turns one send into the bytes written for it, which end in a
	// newline. It is the adapter's: plainLine for the scripted adapter, and
	// claudeUserMessage for the Claude Code adapter.
	frame func(text string) ([]byte, error)
	// closeOnce makes Close idempotent, and closeMu guards the stdin close
	// against a concurrent Send.
	closeOnce sync.Once
	stdinMu   sync.Mutex
	stdinOpen bool
}

// startWorker starts one process as a worker, delivers instructions on its
// standard input, and returns the handle. spec.stdin and spec.started are not
// read: the instructions are written here, after the process exists, and the
// caller reads the group off the handle rather than being told. frame is how
// the adapter puts one send on the wire, the instructions among them.
func startWorker(ctx context.Context, spec procSpec, instructions string, frame func(string) ([]byte, error)) (*procWorker, error) {
	// A send that cannot be framed is refused before any process exists, so
	// a worker is never started over instructions it could not be given.
	if _, err := frame(instructions); err != nil {
		return nil, fmt.Errorf("framing the worker's instructions: %w", err)
	}
	cmd := exec.Command(spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening the worker's standard input: %w", err)
	}
	out := &tailWriter{limit: spec.maxOut}
	// One writer for both streams, guarded by its own mutex, so what Capture
	// reports is the order the writes reached it.
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = spec.grace
	setProcessGroup(cmd)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &procWorker{
		cmd:       cmd,
		stdin:     stdin,
		out:       out,
		grace:     spec.grace,
		done:      make(chan struct{}),
		slot:      make(chan struct{}, 1),
		frame:     frame,
		stdinOpen: true,
	}
	go func() {
		defer close(w.done)
		_ = cmd.Wait()
		// The leader has been reaped. Anything still in its group is a child
		// it left behind, and the worker owns it too.
		terminateTree(cmd.Process, spec.grace, false, nil)
	}()

	if err := w.write(ctx, instructions); err != nil {
		w.Close()
		return nil, fmt.Errorf("delivering the worker's instructions: %w", err)
	}
	return w, nil
}

// Send delivers text as lines, on the terms Worker.Send states.
func (w *procWorker) Send(ctx context.Context, text string) error {
	return w.write(ctx, text)
}

// write puts text, framed, on standard input. It refuses once the
// process is gone or standard input has been closed. When ctx ends while the
// write is still in flight it returns ctx's error and leaves the write
// pending, on the terms Worker.Send states; the next write drains it first.
func (w *procWorker) write(ctx context.Context, text string) error {
	select {
	case w.slot <- struct{}{}:
	case <-w.done:
		return ErrWorkerEnded
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.slot }()
	if w.pending != nil {
		select {
		case <-w.pending:
			w.pending = nil
		case <-w.done:
			return ErrWorkerEnded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-w.done:
		return ErrWorkerEnded
	default:
	}
	w.stdinMu.Lock()
	open := w.stdinOpen
	w.stdinMu.Unlock()
	if !open {
		return ErrWorkerEnded
	}
	framed, err := w.frame(text)
	if err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() {
		_, err := w.stdin.Write(framed)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			// A write that failed because the process is gone, or because
			// Close shut its input while this write was pending, is the
			// ended case and not an I/O failure of its own.
			w.stdinMu.Lock()
			closed := !w.stdinOpen
			w.stdinMu.Unlock()
			select {
			case <-w.done:
				return ErrWorkerEnded
			default:
			}
			if closed {
				return ErrWorkerEnded
			}
			return err
		}
		return nil
	case <-w.done:
		w.pending = written
		return ErrWorkerEnded
	case <-ctx.Done():
		w.pending = written
		return ctx.Err()
	}
}

// Capture returns the retained output.
func (w *procWorker) Capture() Capture {
	text, dropped := w.out.snapshot()
	return Capture{Text: text, Dropped: dropped}
}

// Group is the leader's process identifier, which names the group everywhere
// setProcessGroup gives it one.
func (w *procWorker) Group() int { return w.cmd.Process.Pid }

// Done is closed once the leader has been reaped and its tree terminated.
func (w *procWorker) Done() <-chan struct{} { return w.done }

// Close ends the worker on the terms Worker.Close states.
func (w *procWorker) Close() {
	w.closeOnce.Do(func() {
		w.stdinMu.Lock()
		if w.stdinOpen {
			w.stdinOpen = false
			_ = w.stdin.Close()
		}
		w.stdinMu.Unlock()
		select {
		case <-w.done:
			return
		default:
		}
		terminateTree(w.cmd.Process, w.grace, true, w.done)
	})
	<-w.done
}

// tailWriter keeps the most recent limit bytes written to it and counts what
// it let go. It is what Capture reads, so what it keeps is the end of the
// stream rather than the beginning: a worker's recent output is what a caller
// deciding whether it is making progress wants.
type tailWriter struct {
	limit   int64
	mu      sync.Mutex
	buf     []byte
	dropped int64
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(p)) >= w.limit {
		w.dropped += int64(len(w.buf)) + int64(len(p)) - w.limit
		w.buf = append(w.buf[:0], p[int64(len(p))-w.limit:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if over := int64(len(w.buf)) - w.limit; over > 0 {
		w.dropped += over
		w.buf = append(w.buf[:0], w.buf[over:]...)
	}
	return len(p), nil
}

// snapshot returns a copy of what is retained and how much was dropped.
func (w *tailWriter) snapshot() (string, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf), w.dropped
}

// WorkerEnvironment is the environment a worker is started with, built from
// base, which a runner's is os.Environ() unless it was built with another,
// with extra applied over it and the variables that would point git at
// another repository removed afterwards through vcs.StripRedirecting. It is
// the one builder of that environment: every WorkerRunner this package ships
// starts its worker under exactly this, and a launcher asserting what a
// worker would inherit asks this rather than deriving a second answer.
//
// What it does not know is which base a particular runner was built with.
// A caller holding a Runner cannot read that base back, so a launcher
// computing the environment to assert passes the base it believes the runner
// has, and the assertion is of that base: for a runner built without
// WithEnvironment the two are os.Environ() and agree, and for one built with
// it the caller is responsible for passing the same slice. Nothing here
// records the result, because a value in it may be a credential.
func WorkerEnvironment(base []string, extra map[string]string) []string {
	return vcs.StripRedirecting(environment(base, extra))
}

// plainLine is the scripted adapter's framing: the text itself, followed by a
// newline when it does not end in one. It refuses nothing.
func plainLine(text string) ([]byte, error) {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text), nil
}
