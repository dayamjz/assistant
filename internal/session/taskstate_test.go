package session_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// taskstate's process tier over the real provider on the stand-in: the
// attempt's session reference is the provider's Reference, recorded through
// store.SetAttemptSession and nowhere else, and the Reader probes by it. A
// live worker with no attributed run resolves Running from the process
// tier with progress; a dead one resolves Unknown from that tier rather
// than trusting the log's stale claim; a reference this provider cannot
// answer for leaves the tier unobserved and the log decides.
func TestTaskstateResolvesThroughTheRealProvider(t *testing.T) {
	ctx := context.Background()
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	waitCapture(t, p, s, "worker ready")

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateTask(ctx, store.Task{ID: sub.id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: sub.isolation.Copy}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	attempt, err := st.BeginFirstTaskAttempt(ctx, sub.id, store.OnThisMachine())
	if err != nil {
		t.Fatal(err)
	}
	// The log claims the worker is running; the tiers above decide whether
	// that claim stands.
	if _, err := st.AppendTaskEvent(ctx, sub.id, "working", "the worker says so"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAttemptSession(ctx, sub.id, attempt.Sequence, s.Reference().String(), store.SessionHost{}); err != nil {
		t.Fatal(err)
	}
	probe := func(ctx context.Context, ref string) taskstate.Liveness { return p.Probe(ctx, ref).Liveness }
	reader := taskstate.NewReader(st, probe, nil)

	got, err := reader.Task(ctx, sub.id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskstate.Running || got.Source != taskstate.SourceProcess || !got.Progress || got.Basis.Process != taskstate.Live {
		t.Fatalf("a live worker resolved %+v, want Running from the process tier with progress", got)
	}

	if err := p.Close(s); err != nil {
		t.Fatal(err)
	}
	waitProbe(t, p, s.Reference().String(), session.ReasonAbsent)
	got, err = reader.Task(ctx, sub.id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess || got.Progress || got.Basis.Process != taskstate.Dead {
		t.Fatalf("a dead worker resolved %+v, want Unknown from the process tier, not the log's running", got)
	}

	// A provider that cannot answer for the reference leaves the tier
	// unobserved: another home's provider over the same reference.
	foreign := session.NewPipe(mustHome(t), p2runner(t))
	unobserved := taskstate.NewReader(st, func(ctx context.Context, ref string) taskstate.Liveness { return foreign.Probe(ctx, ref).Liveness }, nil)
	got, err = unobserved.Task(ctx, sub.id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != taskstate.SourceEvent || got.State != taskstate.Running || got.Basis.Process != taskstate.Unobserved {
		t.Fatalf("an unobserved terminal resolved %+v, want the log to decide", got)
	}
}

// mustHome is a second home on this machine, for a provider that holds
// nothing of the subject's.
func mustHome(t *testing.T) *home.Home {
	t.Helper()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h
}
