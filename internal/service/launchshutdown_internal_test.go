package service

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// P15: a forced stop is bounded while a worker's launch is waiting on the
// store to record its session. The launch is held at the record behind a
// write the store cannot finish, the way a long write or a contended
// database holds it, and the stop is forced at stop_grace one second. The
// stop reads the groups it knows of before it starts Close or waits on its
// deadline, so a launch that waited on the store while holding what that
// read takes would hold the stop for as long as the store did, past every
// bound P15 sets, which is what #546's first round shipped. Shutdown must answer
// ErrStopForced within stop_grace plus forceSettle. That the worker is
// among the groups that read names is launchrecord_internal_test.go's to
// show; how many groups the force then counts is not read here, because
// Close, running beside it, may end the worker first.
//
// It does not run in parallel: it bounds how long Shutdown takes from
// above, and the package's other tests beside it would be measuring their
// load rather than the stop.
func TestAForcedStopIsBoundedWhileALaunchWaitsOnTheStore(t *testing.T) {
	principles.Cite(t, principles.P15)
	const grace = time.Second
	h, records := loopHomeConfigured(t, closeGraceConfig(grace))
	agent := workerStandIn(t)
	l := openLoopWith(t, h, records, nil, nil, agents.NewCatalog(fixedRunner{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()}))
	s := l.service
	defer func() { _ = s.Close() }()
	ctx := t.Context()
	// Planted after open, so the dispatch at open does not launch it and
	// the launch below is the only one.
	id, _ := plantLaunchable(t, h, s.store)
	d, err := s.dispatchTask(ctx, id)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	type hold struct {
		release func() error
		ref     string
	}
	held := make(chan hold, 1)
	s.beforeSessionRecord = func(task, ref string) {
		if task != id {
			return
		}
		release, err := s.store.HoldWritesForTest(ctx, t)
		if err != nil {
			t.Errorf("holding the store's writer: %v", err)
			release = func() error { return nil }
		}
		held <- hold{release: release, ref: ref}
	}
	s.launchInBackground(d)
	var at hold
	select {
	case at = <-held:
	case <-time.After(loopWait):
		t.Fatal("the launch never reached the record of its session")
	}
	defer func() {
		if err := at.release(); err != nil {
			t.Error(err)
		}
	}()

	force := make(chan struct{})
	close(force)
	began := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(force) }()
	bound := grace + forceSettle
	var stopErr error
	select {
	case stopErr = <-stopped:
	case <-time.After(bound + grace):
		// The stop is past its bound. Releasing the store lets it go on, so
		// the test ends rather than hangs, and how long it then took says
		// what it was waiting on.
		_ = at.release()
		stopErr = <-stopped
		t.Fatalf("a forced stop with stop_grace %v had not returned %v after it began, and returned %v after it once the store write the launch waited on was released (%v)",
			grace, bound+grace, time.Since(began), stopErr)
	}
	took := time.Since(began)
	var forced *StopForcedError
	if !errors.As(stopErr, &forced) || !forced.Early {
		t.Fatalf("the forced stop answered %v, want a *StopForcedError for a stop forced early", stopErr)
	}
	if took > bound {
		t.Fatalf("the forced stop took %v, want within stop_grace plus forceSettle, %v", took, bound)
	}
}
