package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

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
// everything else Unobserved, so a worker an earlier service started in tmux
// reads after a restart exactly as one it started on a pipe would. The
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
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return nil, fmt.Errorf("session: naming the terminal socket: %w", err)
	}
	worker, err := agents.OpenTerminalWorker(ctx, t.runner,
		agents.Terminal{Tmux: t.tmux, Socket: t.home.TerminalSocket(hex.EncodeToString(name[:]))},
		agents.WorkerSpec{Dir: spec.Isolation.Copy, Instructions: spec.Instructions, Route: spec.Route, Model: spec.Model, Effort: spec.Effort})
	if err != nil {
		return nil, fmt.Errorf("session: starting the worker in a terminal: %w", err)
	}
	return t.adopt(spec.TaskID, worker, worker.Attach(), worker.Host())
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
// earlier service started is not attached to through this provider, on the
// terms its Probe answers Unobserved for it.
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
