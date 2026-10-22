//go:build linux || darwin

package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

// wedgedTmux is a tmux that answers like the real one, the wrapper
// standin.Tmux resolved, for every command but one: the first whose
// arguments name the subcommand written into arm. That client instead
// ignores SIGTERM, starts a descendant in its own process group that ignores
// it too, appends both identifiers to pids, and waits for ever, which is a
// client of a wedged server as far as its invoker can tell. Arming is taken
// once, so the client the call makes next, the one that ends the server
// among them, is the real one again.
type wedgedTmux struct {
	tmux, arm, pids string
}

func newWedgedTmux(t *testing.T) wedgedTmux {
	t.Helper()
	real := standin.Tmux(t)
	dir := t.TempDir()
	w := wedgedTmux{tmux: filepath.Join(dir, "tmux"), arm: filepath.Join(dir, "arm"), pids: filepath.Join(dir, "pids")}
	script := "#!/bin/sh\n" +
		"armed=$(cat '" + w.arm + "' 2>/dev/null)\n" +
		"for a in \"$@\"; do\n" +
		"  if [ -n \"$armed\" ] && [ \"$a\" = \"$armed\" ] && mv '" + w.arm + "' '" + w.arm + ".taken' 2>/dev/null; then\n" +
		"    trap '' TERM\n" +
		"    /bin/sleep 3600 &\n" +
		"    echo $$ $! >> '" + w.pids + "'\n" +
		"    while :; do wait; done\n" +
		"  fi\n" +
		"done\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(w.tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return w
}

// wedge arms the next client naming subcommand.
func (w wedgedTmux) wedge(t *testing.T, subcommand string) {
	t.Helper()
	if err := os.WriteFile(w.arm, []byte(subcommand+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wedgedClient is the identities of the wedged client and its descendant,
// read while they run, so whether they are gone afterwards is asked of the
// processes they were and never of a bare identifier.
func (w wedgedTmux) wedgedClient(t *testing.T, returned <-chan struct{}) []agents.Identity {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, _ := os.ReadFile(w.pids)
		if fields := strings.Fields(string(data)); len(fields) == 2 {
			var ids []agents.Identity
			for _, f := range fields {
				pid, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("the wedged tmux wrote %q, want two identifiers", data)
				}
				id, present, err := agents.Identify(pid)
				if err != nil || !present {
					t.Fatalf("process %d the wedged tmux started was not running when looked for (%v): the call ended it before its bound", pid, err)
				}
				t.Cleanup(func() {
					if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
						_ = syscall.Kill(id.PID, syscall.SIGKILL)
					}
				})
				ids = append(ids, id)
			}
			return ids
		}
		select {
		case <-returned:
			t.Fatalf("the call returned before the wedged tmux client ran (it wrote %q)", data)
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the wedged tmux client never started (it wrote %q)", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// requireGone fails the test unless every process in ids reads absent.
func requireGone(t *testing.T, ids []agents.Identity) {
	t.Helper()
	for _, id := range ids {
		gone := time.Now().Add(5 * time.Second)
		for {
			presence, err := id.Presence()
			if err == nil && presence != agents.PresencePresent {
				break
			}
			if !time.Now().Before(gone) {
				t.Fatalf("process %d of the wedged tmux client's group reads present (%v) after the call returned: its group was not ended", id.PID, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestEachKindOfTmuxClientEndsAtItsBound holds P15 for the tmux clients a
// terminal worker runs: for each kind, a client that never answers, beside
// a descendant in its group, both ignoring SIGTERM, is ended with its whole
// group at that kind's bound, under a context that never ends, as
// internal/service hands a worker's start and its sends. The start and a
// send answer agents.ErrTerminalClientTimedOut, and never the caller's own
// deadline, a Capture answers the last capture the handle took, and a Close
// returns with the server ended by the next client, and in every case
// neither process is running afterwards, asked by identity. Without the
// bound, the start and the send block for as long as the client runs, and
// a capture or a close bounded by a context alone leaves the descendant
// running.
func TestEachKindOfTmuxClientEndsAtItsBound(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	never := context.WithoutCancel(t.Context())
	open := func(t *testing.T, w wedgedTmux) (agents.TerminalWorker, *standin.Agent, agents.Terminal, error) {
		term := agents.Terminal{Tmux: w.tmux, Socket: filepath.Join(standin.SocketDir(t), "s")}
		agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{{Contains: "hello", Reply: "hi"}},
		}}, agents.WithTerminationGrace(300*time.Millisecond))
		worker, err := agents.OpenTerminalWorker(never, agent.Runner(), term, workerSpec(t))
		if err == nil {
			t.Cleanup(worker.Close)
		}
		return worker, agent, term, err
	}
	for _, c := range []struct {
		name, subcommand string
		bound            time.Duration
		before           bool
		call             func(t *testing.T, worker agents.TerminalWorker, term agents.Terminal, err error) func() error
	}{
		{"the start", "new-session", agents.TmuxStartBound, true, func(t *testing.T, _ agents.TerminalWorker, _ agents.Terminal, err error) func() error {
			return func() error { return err }
		}},
		{"a send", "load-buffer", agents.TmuxTypeBound, false, func(t *testing.T, worker agents.TerminalWorker, _ agents.Terminal, _ error) func() error {
			return func() error { return worker.Send(never, "hello") }
		}},
		{"a capture", "capture-pane", agents.TmuxReadBound, false, func(t *testing.T, worker agents.TerminalWorker, _ agents.Terminal, _ error) func() error {
			last := worker.Capture()
			return func() error {
				if got := worker.Capture(); got != last {
					return errors.New("the capture answered " + strconv.Quote(got.Text) + ", not the last capture " + strconv.Quote(last.Text))
				}
				return nil
			}
		}},
		{"a close", "kill-server", agents.TmuxEndBound, false, func(t *testing.T, worker agents.TerminalWorker, term agents.Terminal, _ error) func() error {
			return func() error {
				worker.Close()
				if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
					return errors.New("the socket outlived the close")
				}
				return nil
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWedgedTmux(t)
			var (
				worker agents.TerminalWorker
				term   agents.Terminal
				err    error
				start  time.Time
			)
			type answer struct {
				err     error
				elapsed time.Duration
			}
			done := make(chan answer, 1)
			returned := make(chan struct{})
			if c.before {
				w.wedge(t, c.subcommand)
				go func() {
					start = time.Now()
					worker, _, term, err = open(t, w)
					done <- answer{err, time.Since(start)}
					close(returned)
				}()
			} else {
				worker, _, term, err = open(t, w)
				if err != nil {
					t.Fatalf("starting the terminal worker: %v", err)
				}
				if err := worker.Send(t.Context(), "hello"); err != nil {
					t.Fatalf("a send before anything was wedged: %v", err)
				}
				call := c.call(t, worker, term, nil)
				w.wedge(t, c.subcommand)
				go func() {
					start := time.Now()
					err := call()
					done <- answer{err, time.Since(start)}
					close(returned)
				}()
			}
			ids := w.wedgedClient(t, returned)
			within := c.bound + 3*agents.TmuxClientGrace + 2*time.Second
			var got answer
			select {
			case got = <-done:
			case <-time.After(within):
				t.Fatalf("%s was still blocked on a tmux client that never answers %s after it started, past its bound of %s", c.name, within, c.bound)
			}
			if c.before {
				got.err = c.call(t, worker, term, got.err)()
			}
			if got.elapsed < c.bound {
				t.Fatalf("%s answered %v after %s, before its bound of %s: something other than the bound ended it", c.name, got.err, got.elapsed, c.bound)
			}
			switch c.subcommand {
			case "new-session", "load-buffer":
				if !errors.Is(got.err, agents.ErrTerminalClientTimedOut) || errors.Is(got.err, context.DeadlineExceeded) {
					t.Fatalf("%s answered %v, want agents.ErrTerminalClientTimedOut and not a caller's deadline", c.name, got.err)
				}
			default:
				if got.err != nil {
					t.Fatalf("%s: %v", c.name, got.err)
				}
			}
			requireGone(t, ids)
		})
	}
}

// TestAClaudeTerminalSendRefusesAScreenReadingThatRanPastItsBound is
// review's case against the send's readiness wait: a Claude Code worker's
// send reads the screen before it types, and a reading whose client never
// answers is ended at tmuxReadBound like every other client. The send then
// refuses with agents.ErrTerminalClientTimedOut and agents.ErrTerminalNotReady
// under a context that never ends, rather than reading again for as long as
// the server stays wedged, and the client's group is gone, asked by
// identity. Nothing was typed: the next send, once the server answers again,
// is the worker's second message, after its instructions, and the refused
// text is not among them. Before the bound ended the wait, the refused send
// read again, typed, and answered nil.
func TestAClaudeTerminalSendRefusesAScreenReadingThatRanPastItsBound(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	never := context.WithoutCancel(t.Context())
	w := newWedgedTmux(t)
	term := agents.Terminal{Tmux: w.tmux, Socket: filepath.Join(standin.SocketDir(t), "s")}
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond), agents.WithBaseEnvironment(claudeTerminalEnv(t)))
	worker, err := agents.OpenTerminalWorker(never, agent.Runner(), term, workerSpec(t))
	if err != nil {
		t.Fatalf("starting a Claude Code terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	awaitInteractive(t, agent, answered(1))

	w.wedge(t, "capture-pane")
	type answer struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan answer, 1)
	returned := make(chan struct{})
	go func() {
		start := time.Now()
		err := worker.Send(never, "refused")
		done <- answer{err, time.Since(start)}
		close(returned)
	}()
	ids := w.wedgedClient(t, returned)
	within := agents.TmuxReadBound + 3*agents.TmuxClientGrace + 2*time.Second
	var got answer
	select {
	case got = <-done:
	case <-time.After(within):
		t.Fatalf("a send over a screen reading that never answers was still blocked %s after it started, past the reading's bound of %s", within, agents.TmuxReadBound)
	}
	if !errors.Is(got.err, agents.ErrTerminalClientTimedOut) || !errors.Is(got.err, agents.ErrTerminalNotReady) || errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("a send over a screen reading that never answers answered %v after %s, want agents.ErrTerminalClientTimedOut and agents.ErrTerminalNotReady and not a caller's deadline", got.err, got.elapsed)
	}
	if got.elapsed < agents.TmuxReadBound {
		t.Fatalf("the send answered %v after %s, before the reading's bound of %s: something other than the bound ended it", got.err, got.elapsed, agents.TmuxReadBound)
	}
	requireGone(t, ids)

	if err := worker.Send(t.Context(), "after"); err != nil {
		t.Fatalf("a send once the server answered again: %v", err)
	}
	call := awaitInteractive(t, agent, answered(2))
	if want := []string{"work on the task", "after"}; !slices.Equal(call.Lines, want) {
		t.Fatalf("the worker answered %q, want %q: the refused send was typed", call.Lines, want)
	}
}

// TestATerminalStartCancelledDuringItsClientAnswersTheCancellation is
// review's case against the cancellation promise: a start whose client that
// starts the server never answers, cancelled by its caller while that client
// runs, answers the caller's context.Canceled, with the client's own failure
// kept beside it, within the client's graces of the cancellation; the
// client's group is gone, asked by identity, and so is the socket. Before
// runTmux put the caller's error first, the start answered the client's
// "signal: killed" alone.
func TestATerminalStartCancelledDuringItsClientAnswersTheCancellation(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	w := newWedgedTmux(t)
	term := agents.Terminal{Tmux: w.tmux, Socket: filepath.Join(standin.SocketDir(t), "s")}
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond))
	w.wedge(t, "new-session")
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	done := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		worker, err := agents.OpenTerminalWorker(ctx, agent.Runner(), term, workerSpec(t))
		if err == nil {
			worker.Close()
		}
		done <- err
		close(returned)
	}()
	ids := w.wedgedClient(t, returned)
	cancelled := time.Now()
	cancel()
	within := 3*agents.TmuxClientGrace + agents.TmuxEndBound + 2*time.Second
	var err error
	select {
	case err = <-done:
	case <-time.After(within):
		t.Fatalf("a start cancelled during its client was still blocked %s after the cancellation", within)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a start cancelled during its client answered %v after %s, want context.Canceled", err, time.Since(cancelled))
	}
	requireGone(t, ids)
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket outlived the cancelled start: %v", err)
	}
}

// TestATerminalSendCancelledDuringItsClientKeepsTheClientsFailure is
// review's case against what a cancelled send answers: a send whose typing
// client never answers, cancelled by its caller while that client runs,
// answers the caller's context.Canceled with the client's own failure, the
// *exec.ExitError of the group it ended, kept beside it rather than
// discarded, and the client's group is gone, asked by identity. Before Send
// kept runTmux's answer, it answered context.Canceled alone.
func TestATerminalSendCancelledDuringItsClientKeepsTheClientsFailure(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	w := newWedgedTmux(t)
	term := agents.Terminal{Tmux: w.tmux, Socket: filepath.Join(standin.SocketDir(t), "s")}
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond))
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if err != nil {
		t.Fatalf("starting the terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	w.wedge(t, "load-buffer")
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	done := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		done <- worker.Send(ctx, "hello")
		close(returned)
	}()
	ids := w.wedgedClient(t, returned)
	cancel()
	within := 3*agents.TmuxClientGrace + 2*time.Second
	select {
	case err = <-done:
	case <-time.After(within):
		t.Fatalf("a send cancelled during its client was still blocked %s after the cancellation", within)
	}
	var exit *exec.ExitError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &exit) {
		t.Fatalf("a send cancelled during its client answered %v, want context.Canceled and the client's *exec.ExitError beside it", err)
	}
	requireGone(t, ids)
}

// TestAClaudeTerminalSendCancelledDuringItsReadingKeepsTheClientsFailure is
// the same case at the send's readiness wait, the sibling path that
// surfaces a tmux client's error: a Claude Code send whose screen reading
// never answers, cancelled by its caller while that reading runs, answers
// agents.ErrTerminalNotReady and context.Canceled with the reading client's
// *exec.ExitError kept beside them, and the client's group is gone. Before
// the wait kept the reading's answer, it answered ErrTerminalNotReady and
// context.Canceled alone.
func TestAClaudeTerminalSendCancelledDuringItsReadingKeepsTheClientsFailure(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	w := newWedgedTmux(t)
	term := agents.Terminal{Tmux: w.tmux, Socket: filepath.Join(standin.SocketDir(t), "s")}
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond), agents.WithBaseEnvironment(claudeTerminalEnv(t)))
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if err != nil {
		t.Fatalf("starting a Claude Code terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	awaitInteractive(t, agent, answered(1))
	w.wedge(t, "capture-pane")
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	done := make(chan error, 1)
	returned := make(chan struct{})
	sent := time.Now()
	go func() {
		done <- worker.Send(ctx, "refused")
		close(returned)
	}()
	ids := w.wedgedClient(t, returned)
	// The wait's poll ticker starts before its first reading, so once more
	// than one interval has passed since the send began, its tick is
	// waiting while the reading still hangs. The cancellation then ends the
	// reading with both the tick and ctx ready, which is the case where a
	// wait that chose between them could read again under the ended ctx and
	// replace the reading's failure. This wait makes the tick ready; the
	// ticker, not this wait, is what orders it.
	if wait := 2*agents.TerminalPoll - time.Since(sent); wait > 0 {
		time.Sleep(wait)
	}
	cancel()
	within := 3*agents.TmuxClientGrace + 2*time.Second
	select {
	case err = <-done:
	case <-time.After(within):
		t.Fatalf("a send cancelled during its reading was still blocked %s after the cancellation", within)
	}
	var exit *exec.ExitError
	if !errors.Is(err, agents.ErrTerminalNotReady) || !errors.Is(err, context.Canceled) || !errors.As(err, &exit) {
		t.Fatalf("a send cancelled during its reading answered %v, want agents.ErrTerminalNotReady, context.Canceled and the client's *exec.ExitError beside them", err)
	}
	requireGone(t, ids)
}
