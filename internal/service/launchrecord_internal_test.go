package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// launchOutcome is what a launchTask run on its own goroutine returned.
type launchOutcome struct {
	session *session.Session
	err     error
}

// recordWindow is a launch subject whose launch runs on its own goroutine,
// so a test can hold it at a seam of the record of its session and read the
// service from outside, knowing from awaitingSessionRecord whether a reader
// is waiting on the record rather than guessing from time.
type recordWindow struct {
	*launchSubject
	terminals heldTerminals
	waiting   chan struct{}
	release   chan struct{}
	once      sync.Once
	launched  chan launchOutcome
}

// newRecordWindow builds the subject and dispatches its task. letGo, which
// the test's cleanup also calls, ends whatever hold a seam is in.
func newRecordWindow(t *testing.T) *recordWindow {
	t.Helper()
	return recordWindowOver(t, newLaunchSubject(t, workerStandIn(t)))
}

// recordWindowOver is newRecordWindow over a launch subject the caller
// built, whose task it dispatches.
func recordWindowOver(t *testing.T, sub *launchSubject) *recordWindow {
	t.Helper()
	w := &recordWindow{
		launchSubject: sub,
		terminals:     heldTerminals{sub.service},
		waiting:       make(chan struct{}, 1),
		release:       make(chan struct{}),
		launched:      make(chan launchOutcome, 1),
	}
	sub.service.terminals = w.terminals
	sub.service.awaitingSessionRecord = func(string) {
		select {
		case w.waiting <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() {
		w.letGo()
		<-w.launched
	})
	return w
}

// letGo ends the seam's hold, once.
func (w *recordWindow) letGo() { w.once.Do(func() { close(w.release) }) }

// launch runs launchTask on its own goroutine, under ctx.
func (w *recordWindow) launch(ctx context.Context, t *testing.T) {
	t.Helper()
	d, err := w.service.dispatchTask(t.Context(), w.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	go func() {
		launched, err := w.service.launchTask(ctx, d)
		w.launched <- launchOutcome{session: launched, err: err}
	}()
}

// atSeam waits for the seam to report the reference, failing when the
// launch returned without reaching it.
func (w *recordWindow) atSeam(t *testing.T, reached <-chan string) string {
	t.Helper()
	select {
	case ref := <-reached:
		return ref
	case out := <-w.launched:
		w.launched <- out
		t.Fatalf("the launch returned (%v) without reaching the record of its session", out.err)
		return ""
	}
}

// knows reports whether a stop beginning now would name the worker ref
// names among the groups it forces: knownGroups is what Shutdown reads, and
// it must never wait on the record.
func (w *recordWindow) knows(t *testing.T, ref string) bool {
	t.Helper()
	parsed, err := session.ParseReference(ref)
	if err != nil {
		t.Fatalf("the recorded reference %q does not parse: %v", ref, err)
	}
	return slices.Contains(w.service.knownGroups(), agents.Identity{PID: parsed.PID, Start: parsed.Start})
}

// recordReading is what a reader of the record finds when it asks the
// service about the worker the record names.
type recordReading struct {
	probe   session.Observation
	process taskstate.Liveness
	holds   bool
	held    *session.Session
}

// A reader that reads a session's reference off the attempt's record and
// then asks this service about that worker finds it held and live: a status
// read's probe, the task's process tier, the landed pass's holdsSession,
// and a message's or a peek's sessionOf. The launch is held at the point
// the record is written and the hold not yet settled, so the losing order
// of #538, where the record named a session the service did not hold yet
// and every one of those readers answered as for a worker this service did
// not start, is reached by construction. A stop beginning there names the
// worker without waiting, which is what keeps Shutdown bounded (#546).
func TestAReaderOfASessionsRecordFindsItHeld(t *testing.T) {
	ctx := t.Context()
	w := newRecordWindow(t)
	s := w.service
	reached := make(chan string, 1)
	s.afterSessionRecorded = func(_, ref string) {
		reached <- ref
		<-w.release
	}
	w.launch(ctx, t)
	ref := w.atSeam(t, reached)
	attempts, err := s.store.TaskAttempts(ctx, w.task)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := attempts[len(attempts)-1].SessionRef.Get(); got != ref {
		t.Fatalf("at the seam the record names %q, want %q", got, ref)
	}
	if !w.knows(t, ref) {
		t.Fatalf("at the record of session %s a stop would not name its worker among the groups it forces", ref)
	}
	read := make(chan recordReading, 1)
	go func() {
		var r recordReading
		r.probe = w.terminals.Probe(ctx, ref)
		o, err := s.tasks().ObserveTask(ctx, w.task)
		if err != nil {
			t.Errorf("reading task %s: %v", w.task, err)
		}
		r.process = o.State.Basis.Process
		r.holds = s.holdsSession(w.task, ref)
		_, r.held, _ = s.sessionOf(w.task)
		read <- r
	}()
	var r recordReading
	select {
	case <-w.waiting:
		// The reader is waiting on the hold's settlement, which the write
		// already decided; let it settle.
		w.letGo()
		r = <-read
	case r = <-read:
		// It answered without waiting, which is allowed only if it found
		// the worker held.
		w.letGo()
	}
	out := <-w.launched
	w.launched <- out
	if out.err != nil {
		t.Fatalf("launching: %v", out.err)
	}
	if r.held != out.session || !r.holds {
		t.Fatalf("a reader of the record naming session %s found held %v (holdsSession %v), want the launched session",
			ref, r.held, r.holds)
	}
	if r.probe.Liveness != taskstate.Live || r.probe.Reason != session.ReasonHeld {
		t.Fatalf("a reader of the record naming session %s probed %+v, want Live through the held handle", ref, r.probe)
	}
	if r.process != taskstate.Live {
		t.Fatalf("a reader of the record naming session %s read the task's process %s, want Live", ref, r.process)
	}
}

// A reader that asks for a task's session by the task alone, before the
// record names it, is answered from how the record ended: held, and named
// by the record, when it was written, and not held, with nothing recorded,
// when it was not. The launch is held after the session is held pending and
// before the write, the order that only reordering the record and the hold
// leaves open, where a reader found a session held whose record a launch
// Close cancelled then failed to write. A stop beginning there names the
// worker without waiting.
func TestASessionAskedForByTaskIsAnsweredFromItsRecord(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails bool
	}{{name: "recorded"}, {name: "not recorded", fails: true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRecordWindow(t)
			s := w.service
			launchCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reached := make(chan string, 1)
			s.beforeSessionRecord = func(_, ref string) {
				reached <- ref
				<-w.release
				if tc.fails {
					// The record's write is asked under a context that is
					// done, the way Close's cancellation reaches a launch.
					cancel()
				}
			}
			w.launch(launchCtx, t)
			ref := w.atSeam(t, reached)
			if !w.knows(t, ref) {
				t.Fatalf("with session %s pending a stop would not name its worker among the groups it forces", ref)
			}
			type answer struct {
				held *session.Session
				ok   bool
			}
			asked := make(chan answer, 1)
			go func() {
				_, held, ok := s.sessionOf(w.task)
				asked <- answer{held: held, ok: ok}
			}()
			var a answer
			waited := false
			select {
			case <-w.waiting:
				waited = true
				w.letGo()
				a = <-asked
			case a = <-asked:
				// Before the record is written the only answer allowed
				// without waiting is that nothing is held.
				if a.ok {
					t.Fatalf("sessionOf answered session %s held before its record was written", a.held.Reference())
				}
				w.letGo()
			}
			out := <-w.launched
			w.launched <- out
			attempts, err := s.store.TaskAttempts(t.Context(), w.task)
			if err != nil {
				t.Fatal(err)
			}
			recorded, isRecorded := attempts[len(attempts)-1].SessionRef.Get()
			if tc.fails {
				if !errors.Is(out.err, context.Canceled) {
					t.Fatalf("the launch whose record was asked under a done context answered %v", out.err)
				}
				if isRecorded {
					t.Fatalf("the attempt records session %s, want none", recorded)
				}
				if a.ok {
					t.Fatalf("sessionOf answered session %s held, whose record was never written", a.held.Reference())
				}
				if _, _, ok := s.sessionOf(w.task); ok {
					t.Fatal("after the record failed the service still holds a session for the task")
				}
				if w.knows(t, ref) {
					t.Fatalf("after the record failed a stop would still name session %s", ref)
				}
				return
			}
			if out.err != nil {
				t.Fatalf("launching: %v", out.err)
			}
			if !isRecorded || recorded != ref {
				t.Fatalf("the attempt records %q (known %v), want %s", recorded, isRecorded, ref)
			}
			if waited && (!a.ok || a.held != out.session) {
				t.Fatalf("sessionOf waited on the record and answered %v (%v), want the launched session", a.held, a.ok)
			}
			if _, held, ok := s.sessionOf(w.task); !ok || held != out.session {
				t.Fatalf("after the record the service holds %v (%v), want the launched session", held, ok)
			}
		})
	}
}
