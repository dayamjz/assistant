package agents_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// startedWatch records what an Invocation.Started callback was told, so a
// test can hold the adapter to the field's contract: told once when the
// process exists, and told again, through the returned function, only after
// the invocation's process tree has been terminated and before Run returns.
type startedWatch struct {
	mu    sync.Mutex
	pgids []int
	ended int
}

func (w *startedWatch) start(pgid int) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pgids = append(w.pgids, pgid)
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.ended++
	}
}

func (w *startedWatch) told() ([]int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.pgids...), w.ended
}

// TestStartedSpansTheInvocation drives the containment seam on the path a
// service uses it: the adapter tells Started once, with a positive group
// identifier, and has called the ended function it returned by the time Run
// returns, so a registration made in Started spans exactly the invocation's
// process lifetime.
func TestStartedSpansTheInvocation(t *testing.T) {
	runner := newRunner(t)
	watch := &startedWatch{}
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "envelope",
		helperResultVar: "the change rebases cleanly",
	})
	inv.Started = watch.start

	if _, err := runner.Run(t.Context(), agents.PurposeIntent, inv); err != nil {
		t.Fatalf("a well-formed invocation failed: %v", err)
	}
	pgids, ended := watch.told()
	if len(pgids) != 1 {
		t.Fatalf("Started was told %d times, want once", len(pgids))
	}
	if pgids[0] <= 0 {
		t.Errorf("Started was told the group %d, want a positive identifier", pgids[0])
	}
	if ended != 1 {
		t.Errorf("the ended function had run %d times when Run returned, want once", ended)
	}
}

// TestStartedIsToldOnAFailingInvocationToo pins that the span is the
// process's and not the result's: an agent that ran and failed still started,
// so a registration spanning it is still made and still ends.
func TestStartedIsToldOnAFailingInvocationToo(t *testing.T) {
	runner := newRunner(t)
	watch := &startedWatch{}
	inv := invocation(t, agents.ShapeText, map[string]string{
		helperModeVar: "fail",
		helperExitVar: "3",
	})
	inv.Started = watch.start

	if _, err := runner.Run(t.Context(), agents.PurposeIntent, inv); err == nil {
		t.Fatal("a failing stand-in agent produced no error")
	}
	pgids, ended := watch.told()
	if len(pgids) != 1 || ended != 1 {
		t.Fatalf("a failing invocation told Started %d times and ended %d times, want once each",
			len(pgids), ended)
	}
}

// TestStartedIsNotToldWhenNothingStarts is the callback's negative half: an
// invocation whose process never exists has nothing to contain, so Started is
// never called and no registration is made for a group that never was.
func TestStartedIsNotToldWhenNothingStarts(t *testing.T) {
	runner := newRunner(t)
	watch := &startedWatch{}
	inv := invocation(t, agents.ShapeText, map[string]string{helperModeVar: "envelope"})
	// A working directory that does not exist fails the start itself, before
	// any process is created.
	inv.Dir = filepath.Join(t.TempDir(), "absent")
	inv.Started = watch.start

	if _, err := runner.Run(t.Context(), agents.PurposeIntent, inv); err == nil {
		t.Fatal("an invocation with no working directory started anyway")
	}
	if pgids, ended := watch.told(); len(pgids) != 0 || ended != 0 {
		t.Fatalf("Started was told %d times and ended %d times for a process that never existed",
			len(pgids), ended)
	}
}
