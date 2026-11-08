package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// ErrNoTmux is Create's refusal on a provider built with no absolute path to
// a tmux binary, which is what a caller that found none on its PATH builds:
// which binary hosts every worker is decided once, by that caller, and never
// looked up again per worker. The refusal is Create's rather than NewTmux's
// so that a service whose operator chose tmux on a machine without it still
// builds its provider, observes what it can by identity, and refuses each
// launch with this error, as it refuses one for an adapter without the
// capability.
var ErrNoTmux = errors.New("session: the tmux provider needs an absolute path to a tmux binary")

// Tmux is the tmux provider: every worker is the one process of a tmux
// server of its own, which a person can attach to, watch, and type into. It
// starts nothing itself: Create asks the runner for a terminal worker
// through agents.OpenTerminalWorker, which refuses an adapter that has not
// declared agents.CapabilityTerminalWorkers, and internal/agents owns the
// server, the environment, the typing and the capture, on the terms its
// terminal mechanism states.
//
// Everything a provider decides about a session it decides as Pipe does,
// through the same held set: Live only through a handle this provider holds
// whose process has not exited, Dead only where absence is proven, and
// everything else Unobserved. What it adds over Pipe across a restart is
// Release and Readopt, on the package documentation's restart terms: a
// worker an earlier service started in tmux reads after a restart as one it
// started on a pipe would until Readopt holds it again. The
// worker group set is produced the same way, from the pane's process, which
// leads its own group, with the server the adapter read as the pane's
// parent recorded as the membership's host; the package documentation says
// what placement then reads through the tmux server.
//
// What it adds is the terminal. AttachReference answers the command a
// person runs to attach to a held session; Capture is the pane's screen
// rather than a pipe's stream, on internal/agents' terms; and Send is typed
// into the pane, which is the same input a person attached to it types into.
//
// A Tmux is safe for concurrent use.
type Tmux struct {
	home   *home.Home
	runner agents.Runner
	tmux   string
	// readopting serializes Readopt, so a second call for a reference finds
	// the session the first holds rather than verifying and holding another
	// handle over the same worker.
	readopting sync.Mutex
	*held
}

// NewTmux returns the tmux provider for the home h, starting workers through
// r on the tmux binary at tmux. r is asked for a terminal worker at Create,
// so an adapter that has not declared terminal workers is refused there, and
// a tmux that is not an absolute path is refused there with ErrNoTmux.
func NewTmux(h *home.Home, r agents.Runner, tmux string) *Tmux {
	t := &Tmux{home: h, runner: r, tmux: tmux}
	t.held = newHeld(HomeMarker(h), t)
	return t
}

// Workers is the set of live worker groups this provider has created, on
// the terms Workers states.
func (t *Tmux) Workers() *Workers { return t.workers }

// Create starts the worker on the terms Provider.Create states, hosted in a
// new tmux server whose socket is home.TerminalSocket under a name drawn at
// random, so no two sessions, in this service or a later one, share a
// server.
func (t *Tmux) Create(ctx context.Context, spec Spec) (*Session, error) {
	if !spec.Isolation.Proven() {
		return nil, ErrUnproven
	}
	if err := home.ValidTaskID(spec.TaskID); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(t.tmux) {
		return nil, fmt.Errorf("%w: %q", ErrNoTmux, t.tmux)
	}
	var name [terminalNameLength / 2]byte
	if _, err := rand.Read(name[:]); err != nil {
		return nil, fmt.Errorf("session: naming the terminal socket: %w", err)
	}
	terminal := hex.EncodeToString(name[:])
	worker, err := agents.OpenTerminalWorker(ctx, t.runner,
		agents.Terminal{Tmux: t.tmux, Socket: t.home.TerminalSocket(terminal)},
		agents.WorkerSpec{Dir: spec.Isolation.Copy, Instructions: spec.Instructions, Route: spec.Route, Model: spec.Model, Effort: spec.Effort})
	if err != nil {
		return nil, fmt.Errorf("session: starting the worker in a terminal: %w", err)
	}
	return t.adopt(spec.TaskID, worker, worker.Attach(), worker.Host(), terminal)
}

// Release lets go of a held worker's session on Host.Release's terms: the
// handle stops watching the worker and the worker and its server go on
// running, owned by the attempt's recorded reference, which names the
// server's socket, and by the handle Readopt makes, which takes over the
// socket and the worker's cleanup (agents.TerminalWorker.Release).
func (t *Tmux) Release(s *Session) error {
	if !t.holds(s) {
		return ErrNotHeld
	}
	worker, ok := s.worker.(agents.TerminalWorker)
	if !ok || s.ref.Terminal == "" {
		return ErrNotDurable
	}
	return t.letGo(s, worker)
}

// Interrupt ends the turn a held worker is running on Host.Interrupt's
// terms, through agents.InterruptTerminalWorker over the adapter this
// provider was built over, which reads its declaration first.
func (t *Tmux) Interrupt(ctx context.Context, s *Session) (bool, error) {
	if !t.holds(s) {
		return false, ErrNotHeld
	}
	worker, ok := s.worker.(agents.TerminalWorker)
	if !ok {
		return false, ErrNotInterruptible
	}
	return agents.InterruptTerminalWorker(ctx, t.runner, worker)
}

// ErrNotReadoptable is Readopt's refusal of a reference it will not try to
// hold: one ParseReference refuses, another home's, a subordinate's, or one
// that names no terminal or no start time, which every reference written
// before references named their terminal is. Nothing is read or held.
var ErrNotReadoptable = errors.New("session: the reference names no terminal this provider can hold again")

// Readopt holds again the worker ref names, a reference Create wrote and
// the attempt recorded, whose session an earlier provider released or a
// stopped service left: the worker is verified on
// agents.AdoptTerminalWorker's terms against the pane identity ref records
// and the server on the socket it names, through the adapter this provider
// was built over, and a worker that verifies is held exactly as Create's
// is, under ref, in Workers with its server as host, so it probes Live,
// captures, takes sends and attaches. A reference this provider already
// holds returns that session. ErrNotReadoptable for a reference it will
// not try, and agents.ErrTerminalUnverified, wrapped, for a worker that does
// not verify, which leaves Probe's answer for ref what it was. It starts
// and ends nothing. Calls are serialized, so concurrent calls for one
// reference hold one session and every one of them returns it.
func (t *Tmux) Readopt(ctx context.Context, ref string) (*Session, error) {
	t.readopting.Lock()
	defer t.readopting.Unlock()
	if s, ok := t.lookup(ref); ok {
		return s, nil
	}
	decoded, err := ParseReference(ref)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotReadoptable, err)
	case decoded.Home != t.marker, decoded.Subordinate(), decoded.Terminal == "", decoded.Start == 0:
		return nil, fmt.Errorf("%w: %s", ErrNotReadoptable, ref)
	case !filepath.IsAbs(t.tmux):
		return nil, fmt.Errorf("%w: %q", ErrNoTmux, t.tmux)
	}
	worker, err := agents.AdoptTerminalWorker(ctx, t.runner,
		agents.Terminal{Tmux: t.tmux, Socket: t.home.TerminalSocket(decoded.Terminal)},
		agents.Identity{PID: decoded.PID, Start: decoded.Start})
	if err != nil {
		return nil, fmt.Errorf("session: holding again the worker of task %s: %w", decoded.Task, err)
	}
	return t.hold(decoded, worker, worker.Attach(), worker.Host())
}

// Subordinates is the set of live subordinate coordinators this provider
// has started, on the terms Subordinates states.
func (t *Tmux) Subordinates() *Subordinates { return t.subordinates }

// Delegate starts a subordinate coordinator on the terms Host.Delegate
// states, hosted in a tmux server of its own exactly as Create hosts a
// worker, with the server recorded as the placement's host and reported
// by Session.Host. It starts through agents.OpenTerminalSubordinate, so it
// runs under agents.GrantSubordinate. An adapter that cannot enforce that
// grant in a terminal refuses the start (the Codex adapter with
// agents.ErrGrantUnenforced); the Claude Code adapter starts one.
func (t *Tmux) Delegate(ctx context.Context, spec SubordinateSpec) (*Session, error) {
	if err := home.ValidDomain(spec.Domain); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(t.tmux) {
		return nil, fmt.Errorf("%w: %q", ErrNoTmux, t.tmux)
	}
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return nil, fmt.Errorf("session: naming the terminal socket: %w", err)
	}
	worker, err := agents.OpenTerminalSubordinate(ctx, t.runner,
		agents.Terminal{Tmux: t.tmux, Socket: t.home.TerminalSocket(hex.EncodeToString(name[:]))},
		agents.WorkerSpec{Dir: spec.Dir, Instructions: spec.Instructions, Route: spec.Route, Model: spec.Model, Effort: spec.Effort})
	if err != nil {
		return nil, fmt.Errorf("session: starting the subordinate coordinator of domain %s in a terminal: %w", spec.Domain, err)
	}
	return t.adoptSubordinate(spec.Domain, worker, worker.Attach(), worker.Host())
}

// Capture returns the pane's text and its digest, on the terms
// Provider.Capture and internal/agents' terminal mechanism state.
func (t *Tmux) Capture(s *Session) (Capture, error) { return t.capture(s) }

// CaptureReference is Capture addressed by reference, on the terms
// Pipe.CaptureReference states, except that the read runs the tmux client
// and so blocks for as long as that does, bounded inside internal/agents.
func (t *Tmux) CaptureReference(ctx context.Context, ref string) (Capture, error) {
	return t.captureReference(ctx, ref)
}

// AttachReference is the command a person runs to attach to the held
// session ref names, and ErrNotHeld for any other reference: a worker an
// earlier service started is attached to through this provider only once
// Readopt holds it again, on the terms its Probe answers Unobserved for it
// until then.
func (t *Tmux) AttachReference(ref string) ([]string, error) { return t.attachReference(ref) }

// Send types text into the pane on the terms Provider.Send states, through
// the adapter's terminal framing.
func (t *Tmux) Send(ctx context.Context, s *Session, text string) error { return t.send(ctx, s, text) }

// Probe answers on the terms Provider.Probe states, through the same rule
// Pipe's answers through.
func (t *Tmux) Probe(ctx context.Context, ref string) Observation { return t.probe(ctx, ref) }

// Close ends the session's worker and its server on the terms Provider.Close
// and internal/agents' terminal mechanism state.
func (t *Tmux) Close(s *Session) error { return t.release(s) }
