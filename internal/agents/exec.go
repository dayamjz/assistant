package agents

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxStderr bounds the diagnostic text one invocation collects from standard
// error. It is applied while the stream is read rather than to the text
// afterwards, so an agent that streams messages cannot grow the buffer past
// it. The full output of a stage lives in that stage's log, which this package
// does not own.
const maxStderr = 8 << 10

// procSpec is one process to run. It is everything the platform-independent
// part of running an agent needs, so the adapter above it builds a spec and
// this file owns the process.
type procSpec struct {
	bin    string
	args   []string
	dir    string
	env    []string
	grace  time.Duration
	maxOut int64
}

// procResult is what running a process produced. It reports what happened
// rather than deciding what it means; the adapter classifies it.
type procResult struct {
	stdout []byte
	// stderr is bounded agent-written text. It is content, so it reaches an
	// *InvocationError and never a Record.
	stderr string
	// code is the exit status, or -1 when the process never reported one.
	code int
	// over is true when the process printed more than maxOut, in which case
	// stdout is empty because the output was discarded rather than truncated.
	over bool
	// err is the error from starting or waiting on the process, nil when it
	// ran and exited on its own.
	err error
}

// runProcess starts one process, collects its output, and terminates the whole
// process tree it started before returning.
//
// The tree is terminated on every path: on completion, on failure, and on
// cancellation. Cancellation terminates it immediately; every other path
// terminates it after the leader was reaped, which is what covers a child the
// agent left running behind itself. Termination is polite first and forceful
// after spec.grace, and what that covers on each platform is in the package
// documentation.
//
// runProcess does not return while the tree is still being terminated, so a
// caller that returns from an invocation has already done everything this
// package can do about the processes it started.
func runProcess(ctx context.Context, spec procSpec) procResult {
	cmd := exec.Command(spec.bin, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	// A nil Stdin is os.DevNull, so an agent that reads standard input sees
	// EOF rather than waiting for a person who is not there.
	cmd.Stdin = nil

	stdout := &boundedWriter{limit: spec.maxOut}
	// Standard error is diagnostic text rather than a result, so it is kept up
	// to the limit and marked, instead of being discarded whole the way an
	// over-limit standard output is.
	stderr := &boundedWriter{limit: maxStderr, truncate: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// WaitDelay bounds the wait for the output pipes to close after the leader
	// exits, so a descendant holding them open cannot keep this call waiting
	// past the grace period.
	cmd.WaitDelay = spec.grace
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return procResult{code: -1, err: err}
	}

	// The watcher terminates the tree the moment the context ends. It is
	// joined before this function returns, so no goroutine outlives the call
	// and no signal is sent after the process identifier could be reused by
	// something else.
	done := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		select {
		case <-ctx.Done():
			terminateTree(cmd.Process, spec.grace, true, done)
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	watcher.Wait()
	// The leader has been reaped. Anything still in its group is a child it
	// left behind, and the invocation owns it too.
	terminateTree(cmd.Process, spec.grace, false, nil)

	res := procResult{
		stderr: stderr.text(),
		code:   exitStatus(cmd),
		over:   stdout.over,
		err:    waitErr,
	}
	if !stdout.over {
		res.stdout = stdout.bytes()
	}
	return res
}

// exitStatus returns the process's exit status, or -1 when it never reported
// one, which is what a process that could not be started or was killed before
// exiting looks like.
func exitStatus(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// boundedWriter collects output up to a limit. Over the limit it either stops
// and marks itself, when truncate is set, or refuses the whole thing by
// setting over, so a caller can discard rather than read a fragment as a
// whole.
type boundedWriter struct {
	limit    int64
	truncate bool
	buf      []byte
	over     bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	room := w.limit - int64(len(w.buf))
	if int64(len(p)) <= room {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	if w.truncate {
		if room > 0 {
			w.buf = append(w.buf, p[:room]...)
		}
		w.over = true
		return len(p), nil
	}
	w.over = true
	w.buf = nil
	return len(p), nil
}

func (w *boundedWriter) bytes() []byte { return w.buf }

// text renders a truncating writer's contents, marking them when the writer
// stopped short so a reader is never shown a fragment that looks whole.
func (w *boundedWriter) text() string {
	s := strings.TrimRight(string(w.buf), "\n")
	if w.over {
		if s != "" {
			s += "\n"
		}
		s += "[agent message truncated]"
	}
	return s
}

// environment builds the environment for one invocation from base, which is
// normally os.Environ(), with extra applied over it. A name present in both
// takes the value from extra.
//
// The result is ordered: base entries in their original order, then the extra
// names sorted, so two invocations built from the same inputs produce the same
// environment. Nothing here is recorded; an environment value may be a
// credential and Record has no field it could be written into.
func environment(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return append([]string(nil), base...)
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)

	override := make(map[string]struct{}, len(extra))
	for _, name := range names {
		override[name] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(names))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := override[name]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for _, name := range names {
		out = append(out, name+"="+extra[name])
	}
	return out
}

// osEnvironment is the base environment a runner uses when a caller names
// none. It is a variable so a test can build a runner over a known
// environment rather than the one the test process happens to have.
var osEnvironment = os.Environ
