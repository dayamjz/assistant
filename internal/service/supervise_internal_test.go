package service

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The loop is driven here the way the product drives it, through Open, with
// the beacon interval at its floor so a stale beacon is noticed in seconds,
// the two windows at their floors and the inspection count low, so a window
// can elapse under the injected clock and the inspection level is reached
// in a handful of surfaces. Every pass a test waits on is one the loop
// reported through the afterPass hook, and every capture it waits on is one
// the double signalled: nothing here polls a file or sleeps for a pass.
const loopConfig = `{"supervision_beacon_interval": "1s", "escalation_window": "1m",
	"recheck_window": "1m", "stall_inspection_count": 2}` + "\n"

// loopWait bounds every wait on the loop. The longest is the watchdog's
// replacement of a wedged incarnation: the first-beacon wait, then the
// delay, at the one-second interval.
const loopWait = 20 * time.Second

// scriptedTerminals is the Terminals double: a probe answer and a capture
// per reference, with the capture able to block on a channel and ignore its
// context, which is the real shape of a blocking read of a worker's output.
// Every capture call is signalled on calls, so a test synchronizes on the
// call rather than on time. Digests are session.Digest over the scripted
// text, the real normalization, so a redraw-only change digests equal here
// for the reason it does in the product.
type scriptedTerminals struct {
	mu       sync.Mutex
	liveness map[string]session.Observation
	text     map[string]string
	fail     map[string]error
	// blockFirst blocks the first capture call on block until it is closed,
	// ignoring the context, when it is set.
	blockFirst bool
	block      chan struct{}
	blocked    bool
	calls      chan string
}

func newScriptedTerminals() *scriptedTerminals {
	return &scriptedTerminals{
		liveness: make(map[string]session.Observation),
		text:     make(map[string]string),
		fail:     make(map[string]error),
		block:    make(chan struct{}),
		calls:    make(chan string, 1024),
	}
}

func (s *scriptedTerminals) live(ref, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness[ref] = session.Observation{Liveness: taskstate.Live, Reason: session.ReasonHeld}
	s.text[ref] = text
}

func (s *scriptedTerminals) dead(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness[ref] = session.Observation{Liveness: taskstate.Dead, Reason: session.ReasonExited}
}

func (s *scriptedTerminals) failing(ref string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveness[ref] = session.Observation{Liveness: taskstate.Live, Reason: session.ReasonHeld}
	s.fail[ref] = err
}

func (s *scriptedTerminals) Probe(_ context.Context, ref string) session.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.liveness[ref]
	if !ok {
		return session.Observation{Liveness: taskstate.Unobserved, Reason: session.ReasonUnheld}
	}
	return obs
}

func (s *scriptedTerminals) Capture(_ context.Context, ref string) (session.Capture, error) {
	s.mu.Lock()
	first := s.blockFirst && !s.blocked
	if first {
		s.blocked = true
	}
	text, err := s.text[ref], s.fail[ref]
	s.mu.Unlock()
	s.calls <- ref
	if first {
		<-s.block
	}
	if err != nil {
		return session.Capture{}, err
	}
	return session.Capture{Text: text, Digest: session.Digest(text)}, nil
}

// loopSubject is a service opened for its loop: the hooks the tests drive it
// through and the records the tasks were planted in.
type loopSubject struct {
	service  *Service
	home     *home.Home
	passes   chan passReport
	replaced chan [2]*incarnation
	kick     chan struct{}
	clock    *fakeClock
}

// fakeClock is an injected clock a test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// loopHome makes a home with the loop's configuration, and returns it with
// its records open for planting; the records are closed before the service
// opens.
func loopHome(t *testing.T) (*home.Home, *store.Store) {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(loopConfig), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return h, records
}

// openLoop opens the service over a planted home with the loop's hooks. The
// records handed in are closed first, because one service owns the home.
func openLoop(t *testing.T, h *home.Home, records *store.Store, terminals Terminals, clock *fakeClock) *loopSubject {
	t.Helper()
	return openLoopWith(t, h, records, terminals, clock, nil)
}

// openLoopWith is openLoop with the catalog the service resolves its agent
// from, for a test whose loop reads a worker the service launched; nil is
// the product's default catalog, which launches nothing. A nil terminals is
// the product's own wiring, the service's provider.
func openLoopWith(t *testing.T, h *home.Home, records *store.Store, terminals Terminals, clock *fakeClock,
	catalog *agents.Catalog) *loopSubject {
	t.Helper()
	if err := records.Close(); err != nil {
		t.Fatalf("closing the records: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	subject := &loopSubject{
		home:     h,
		passes:   make(chan passReport, 1024),
		replaced: make(chan [2]*incarnation, 16),
		kick:     make(chan struct{}),
		clock:    clock,
	}
	hooks := supervisionHooks{
		afterPass: func(r passReport) { subject.passes <- r },
		onReplace: func(replaced, next *incarnation) { subject.replaced <- [2]*incarnation{replaced, next} },
		kick:      subject.kick,
	}
	if clock != nil {
		hooks.now = clock.Now
	}
	running, err := Open(t.Context(), Options{
		Home: h, NewStages: stages.All, NewFixer: stages.Fix, Build: build, LockWait: -1,
		Terminals: terminals, hooks: hooks, Catalog: catalog,
	})
	if err != nil {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, err)
		}
		t.Fatalf("opening the service: %v", err)
	}
	subject.service = running
	return subject
}

// pass makes the loop run one pass and returns its report.
func (l *loopSubject) pass(t *testing.T) passReport {
	t.Helper()
	select {
	case l.kick <- struct{}{}:
	case <-time.After(loopWait):
		t.Fatal("the loop did not take a pass: it is not idle in its select")
	}
	return l.nextPass(t)
}

// freshPass is pass over a drained report channel: the reports of passes
// the ticker already made are discarded first, so the report returned is of
// a pass that began after the test's last step, whichever of the kick and
// the ticker started it.
func (l *loopSubject) freshPass(t *testing.T) passReport {
	t.Helper()
	for {
		select {
		case <-l.passes:
			continue
		default:
		}
		break
	}
	return l.pass(t)
}

// nextPass waits for the next pass report, whichever incarnation made it.
func (l *loopSubject) nextPass(t *testing.T) passReport {
	t.Helper()
	select {
	case r := <-l.passes:
		return r
	case <-time.After(loopWait):
		t.Fatal("no pass was reported")
		return passReport{}
	}
}

// records opens the home's database beside the service, for reading what
// the loop wrote.
func (l *loopSubject) records(t *testing.T) *store.Store {
	t.Helper()
	return l.service.store
}

// plantRunning plants a task that resolves Running from its own history,
// with an attempt and, when ref is not empty, that session reference. The
// "started" event is a worker's, so the task is not one a dispatch at open
// would cut a copy for.
func plantRunning(t *testing.T, records *store.Store, id, ref string) store.Task {
	t.Helper()
	ctx := t.Context()
	task, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/w/" + id,
	}, store.Disposition{})
	if err != nil {
		t.Fatalf("creating task %s: %v", id, err)
	}
	if _, err := records.AppendTaskEvent(ctx, id, "started", ""); err != nil {
		t.Fatalf("starting task %s: %v", id, err)
	}
	if _, err := records.BeginTaskAttempt(ctx, id, 0); err != nil {
		t.Fatalf("beginning the attempt of %s: %v", id, err)
	}
	if ref != "" {
		if err := records.SetAttemptSession(ctx, id, 1, ref); err != nil {
			t.Fatalf("recording the session of %s: %v", id, err)
		}
	}
	return task
}

// wakesOfKind is the task's wakes of one kind, oldest first.
func wakesOfKind(t *testing.T, records *store.Store, task string, kind supervisor.Kind) []store.Wake {
	t.Helper()
	all, err := records.TaskWakes(t.Context(), task)
	if err != nil {
		t.Fatalf("listing the wakes of %s: %v", task, err)
	}
	var out []store.Wake
	for _, w := range all {
		if w.Kind == string(kind) {
			out = append(out, w)
		}
	}
	return out
}

// detectionsOf is a task's detections of one kind in a pass report.
func detectionsOf(r passReport, task string, kind supervisor.Kind) []detection {
	var out []detection
	for _, d := range r.Tasks[task].Detections {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

func readBeacon(t *testing.T, h *home.Home) supervisor.Beacon {
	t.Helper()
	b, err := feed.ReadBeacon(h.Beacon())
	if err != nil {
		t.Fatalf("reading the beacon: %v", err)
	}
	return b
}

// The beacon is written on an idle fleet, from the loop's own select at the
// interval, under this instance and the incarnation the store says is
// current, and its sequence advances with every write: the one beacon
// source is the loop, and an idle service is still read as supervised.
func TestTheLoopWritesTheBeaconOnAnIdleFleet(t *testing.T) {
	h, records := loopHome(t)
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()

	first := l.nextPass(t)
	if first.Epoch != 1 || first.Err != nil || len(first.Tasks) != 0 {
		t.Fatalf("the first pass = %+v, want epoch 1 over no tasks", first)
	}
	// The next tick writes the beacon and passes again; the pass report is
	// what says the write before it has happened.
	second := l.nextPass(t)
	if second.Epoch != 1 {
		t.Fatalf("the second pass is epoch %d, want the same incarnation", second.Epoch)
	}
	b := readBeacon(t, h)
	if b.Instance != l.service.instance || b.Epoch != 1 || b.Sequence < 2 {
		t.Fatalf("the beacon = %+v, want this instance, epoch 1 and at least the second write", b)
	}
	epoch, _, ok, err := l.records(t).CurrentIncarnation(t.Context())
	if err != nil || !ok || epoch != 1 {
		t.Fatalf("the store's current incarnation = %d, %v, %v; want epoch 1", epoch, ok, err)
	}
	if err := l.service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A loop wedged in a capture that ignores its context stops writing the
// beacon, the watchdog replaces it with the next incarnation after the first
// beacon wait and the delay, and when the wedged capture returns the old
// incarnation's write is refused as stale and it returns: the beacon then
// carries the new epoch alone. The double blocks the way a blocking read of
// a worker's output does, with no hook on the production type.
func TestAWedgedIncarnationIsReplacedAndFencedOut(t *testing.T) {
	principles.Cite(t, principles.P10)
	h, records := loopHome(t)
	task := plantRunning(t, records, "wedgedaaaaaa", "pane-wedged").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-wedged", "working\n")
	terminals.blockFirst = true
	l := openLoop(t, h, records, terminals, nil)
	defer func() { _ = l.service.Close() }()

	select {
	case <-terminals.calls:
	case <-time.After(loopWait):
		t.Fatal("the first pass never captured the terminal")
	}
	var old, next *incarnation
	select {
	case pair := <-l.replaced:
		old, next = pair[0], pair[1]
	case <-time.After(loopWait):
		t.Fatal("the watchdog did not replace the wedged incarnation")
	}
	if old == nil || next == nil || old.inc.Epoch() != 1 || next.inc.Epoch() != 2 {
		t.Fatalf("replaced epoch %v with %v, want 1 with 2", old, next)
	}
	// The new incarnation's first pass captures too, and lands its markers.
	for {
		r := l.nextPass(t)
		if r.Epoch == 2 {
			if r.Tasks[task].Err != nil {
				t.Fatalf("the new incarnation's pass failed: %v", r.Tasks[task].Err)
			}
			break
		}
	}
	if b := readBeacon(t, h); b.Epoch != 2 {
		t.Fatalf("the beacon after the replacement is epoch %d, want 2", b.Epoch)
	}

	close(terminals.block)
	var stale passReport
	for {
		stale = l.nextPass(t)
		if stale.Epoch == 1 {
			break
		}
	}
	// Two fences refuse the write, and which one answers first depends on
	// whether the watchdog's cancellation reached the context before the
	// store was asked: the context's refusal or the store's are both the
	// old incarnation writing nothing.
	if err := stale.Tasks[task].Err; !errors.Is(err, store.ErrStaleIncarnation) && !errors.Is(err, context.Canceled) {
		t.Fatalf("the old incarnation's write after it unwedged = %v, want it refused as stale or cancelled", err)
	}
	select {
	case <-old.ended:
	case <-time.After(loopWait):
		t.Fatal("the old incarnation did not return after its write was refused")
	}
	// The store's fence on its own, asked with the old incarnation's value
	// under a live context, which is the write a loop whose cancellation
	// had not reached it would make.
	if _, err := l.records(t).AdvanceWakeMarkers(t.Context(), old.inc, task, store.MarkerAdvance{}); !errors.Is(err, store.ErrStaleIncarnation) {
		t.Fatalf("a marker advance by the replaced incarnation = %v, want ErrStaleIncarnation", err)
	}
	if err := l.service.writeBeacon(&incarnation{inc: old.inc, ctx: t.Context()}); !errors.Is(err, store.ErrStaleIncarnation) {
		t.Fatalf("a beacon write by the replaced incarnation = %v, want ErrStaleIncarnation", err)
	}
	// Exactly one beacon source: the next writes are the new incarnation's,
	// and nothing moves the epoch back.
	l.nextPass(t)
	if b := readBeacon(t, h); b.Epoch != 2 || b.Instance != l.service.instance {
		t.Fatalf("the beacon after the old incarnation returned = %+v, want epoch 2 under this instance", b)
	}
	if err := l.service.Close(); err != nil {
		t.Fatalf("Close with every incarnation ended: %v", err)
	}
}

// Close with a wedged incarnation returns within its grace with
// ErrSupervisionAbandoned, closes the store and releases the lock rather
// than waiting for a goroutine nothing can end; the unwedged goroutine then
// meets a closed store and a closed writer. The home is free afterwards,
// which is what the bounded wait buys.
func TestCloseReturnsWithinItsGraceWithAWedgedLoop(t *testing.T) {
	h, records := loopHome(t)
	plantRunning(t, records, "wedgedclosea", "pane-close")
	terminals := newScriptedTerminals()
	terminals.live("pane-close", "working\n")
	terminals.blockFirst = true
	l := openLoop(t, h, records, terminals, nil)
	defer close(terminals.block)

	select {
	case <-terminals.calls:
	case <-time.After(loopWait):
		t.Fatal("the first pass never captured the terminal")
	}
	grace := CloseGrace(l.service.sup.interval)
	began := time.Now()
	err := l.service.Close()
	took := time.Since(began)
	if !errors.Is(err, ErrSupervisionAbandoned) {
		t.Fatalf("Close = %v, want ErrSupervisionAbandoned", err)
	}
	if took > grace+grace/2 {
		t.Fatalf("Close took %v, want within the grace of %v", took, grace)
	}
	lock, err := h.Acquire(t.Context(), -1)
	if err != nil {
		t.Fatalf("the home's lock after Close: %v", err)
	}
	_ = lock.Release()
}

// A run record saying running with nothing advancing it is a stall and
// surfaces: the task resolves Validating with no evidence, and the second
// pass over its unchanged empty screen raises terminal unchanged for want of
// evidence. The control is the slot: with the run claimed as advancing the
// same pass absorbs on progress.
func TestAStalledRunRecordSurfaces(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: "run-stalled", RepositoryID: "subject", Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("moving the run to running: %v", err)
	}
	task := plantRunning(t, records, "stalledaaaaa", "").ID
	if err := records.SetTaskRun(ctx, task, run.ID); err != nil {
		t.Fatalf("attributing the run: %v", err)
	}
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()

	l.nextPass(t) // the baseline pass, which records the empty screen
	r := l.pass(t)
	got := detectionsOf(r, task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 || got[0].Verdict.Level != supervisor.Surface || got[0].Verdict.Reason != supervisor.ReasonNoEvidence {
		t.Fatalf("the stalled run's detections = %+v, want one terminal-unchanged surface for no evidence", got)
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalUnchanged); len(rows) != 1 || !rows[0].Queued() {
		t.Fatalf("the wake rows = %+v, want one queued", rows)
	}

	// The control: a segment advancing the run is positive evidence, and
	// the next pass absorbs on it rather than on time.
	if err := l.service.claim(run.ID, func() {}); err != nil {
		t.Fatalf("claiming the run's slot: %v", err)
	}
	r = l.pass(t)
	if got := r.Tasks[task].Detections; len(got) != 1 || got[0].Kind != supervisor.KindNoChange || got[0].Verdict.Level != supervisor.Absorb {
		t.Fatalf("with the run advancing the detections = %+v, want no-change absorbed", got)
	}
}

// A slot that says advancing beside a record that says the run is held is
// no progress: the hold surfaces, and the pass that follows surfaces the
// unchanged screen for want of evidence rather than absorbing on the slot.
// It is the loop reading what internal/taskstate decided, in the order the
// records first and the slot after.
func TestASlotDisagreeingWithTheRecordSurfaces(t *testing.T) {
	principles.Cite(t, principles.P8)
	h, records := loopHome(t)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: "run-held", RepositoryID: "subject", Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunHeld); err != nil {
		t.Fatalf("moving the run to held: %v", err)
	}
	if _, err := records.RegisterHold(ctx, store.Hold{Key: "hold-1", RunID: store.Known(run.ID), Subject: "a decision", Detail: "which"}); err != nil {
		t.Fatalf("registering the hold: %v", err)
	}
	task := plantRunning(t, records, "disagreeaaaa", "").ID
	if err := records.SetTaskRun(ctx, task, run.ID); err != nil {
		t.Fatalf("attributing the run: %v", err)
	}
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()
	if err := l.service.claim(run.ID, func() {}); err != nil {
		t.Fatalf("claiming the run's slot: %v", err)
	}

	first := l.nextPass(t)
	holds := detectionsOf(first, task, supervisor.KindHoldOpened)
	if len(holds) != 1 || holds[0].Marker != "hold:hold-1" || holds[0].Verdict.Reason != supervisor.ReasonActionable {
		t.Fatalf("the first pass's hold detections = %+v, want the hold surfaced as actionable", holds)
	}
	second := l.pass(t)
	if got := detectionsOf(second, task, supervisor.KindHoldOpened); len(got) != 0 {
		t.Fatalf("the second pass detected the hold again: %+v", got)
	}
	got := detectionsOf(second, task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 || got[0].Verdict.Reason != supervisor.ReasonNoEvidence {
		t.Fatalf("with the slot claimed on a held run the detections = %+v, want surfaced for no evidence", got)
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindHoldOpened); len(rows) != 1 {
		t.Fatalf("hold wakes = %+v, want exactly one", rows)
	}
}

// A capture that fails and a probe that proves the terminal dead are both
// terminal gone, surfaced once each with the case in the marker, and a
// provider-wide failure is one wake across every terminal that met it.
func TestAFailedCaptureIsTerminalGoneWithItsCase(t *testing.T) {
	h, records := loopHome(t)
	failed := plantRunning(t, records, "failedcaptur", "pane-failed").ID
	dead := plantRunning(t, records, "deadterminal", "pane-dead").ID
	wideA := plantRunning(t, records, "providerwide", "pane-wide-a").ID
	wideB := plantRunning(t, records, "providerwid2", "pane-wide-b").ID
	terminals := newScriptedTerminals()
	terminals.failing("pane-failed", errors.New("the pipe closed"))
	terminals.dead("pane-dead")
	terminals.failing("pane-wide-a", ErrTerminalsUnavailable)
	terminals.failing("pane-wide-b", ErrTerminalsUnavailable)
	l := openLoop(t, h, records, terminals, nil)
	defer func() { _ = l.service.Close() }()

	first := l.nextPass(t)
	want := map[string]string{failed: "terminal-gone:unobservable:pane-failed:", dead: "terminal-gone:dead:pane-dead:"}
	for task, prefix := range want {
		got := detectionsOf(first, task, supervisor.KindTerminalGone)
		if len(got) != 1 || !strings.HasPrefix(got[0].Marker, prefix) || got[0].Disposition != store.WakeAppended {
			t.Fatalf("task %s's terminal-gone detections = %+v, want one appended with marker %s...", task, got, prefix)
		}
	}
	wide := append(detectionsOf(first, wideA, supervisor.KindTerminalGone), detectionsOf(first, wideB, supervisor.KindTerminalGone)...)
	if len(wide) != 1 || !strings.HasPrefix(wide[0].Marker, "terminal-gone:provider-wide:") {
		t.Fatalf("the provider-wide detections across two terminals = %+v, want exactly one", wide)
	}

	// Surfaced once: inside the escalation window the next pass surfaces
	// none of them again, and acknowledging does not bring them back
	// either. A pass that classifies one absorbs it as surfaced this
	// window; the provider-wide terminal after the first is not
	// classified at all.
	quiet := func(r passReport, task string) bool {
		for _, d := range detectionsOf(r, task, supervisor.KindTerminalGone) {
			if d.Verdict.Surfaces() || d.Verdict.Reason != supervisor.ReasonSurfacedThisWindow {
				return false
			}
		}
		return r.Tasks[task].Err == nil
	}
	second := l.pass(t)
	for _, task := range []string{failed, dead, wideA, wideB} {
		if !quiet(second, task) {
			t.Fatalf("task %s surfaced terminal gone again: %+v", task, detectionsOf(second, task, supervisor.KindTerminalGone))
		}
	}
	queued, err := l.records(t).DrainWakes(t.Context(), 100)
	if err != nil {
		t.Fatalf("draining: %v", err)
	}
	var ids []int64
	for _, w := range queued {
		if w.Kind == string(supervisor.KindTerminalGone) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("queued terminal-gone wakes = %d, want three: two per terminal and one provider-wide", len(ids))
	}
	if err := l.records(t).AcknowledgeWakes(t.Context(), ids, store.AcknowledgedByProgram()); err != nil {
		t.Fatalf("acknowledging: %v", err)
	}
	third := l.pass(t)
	for _, task := range []string{failed, dead, wideA, wideB} {
		if !quiet(third, task) {
			t.Fatalf("after acknowledgement task %s: detections %+v, err %v; want nothing surfaced",
				task, detectionsOf(third, task, supervisor.KindTerminalGone), third.Tasks[task].Err)
		}
	}
}

// The terminal-ish wait: a live terminal whose screen does not change
// surfaces once and is then absorbed, surfaces once more after a real
// change, and is not surfaced by a redraw that normalizes to the same
// screen. The evidence is the probe's Live, so what ends the absorb is the
// change and never a pass count.
func TestATerminalIshWaitSurfacesOnce(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	task := plantRunning(t, records, "terminalisha", "pane-ish").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-ish", "progress: 1\n")
	clock := &fakeClock{now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()

	l.nextPass(t) // baseline
	levels := func(r passReport) []supervisor.Level {
		var out []supervisor.Level
		for _, d := range detectionsOf(r, task, supervisor.KindTerminalUnchanged) {
			out = append(out, d.Verdict.Level)
		}
		return out
	}
	// With evidence the unchanged screen absorbs on progress; the surface
	// comes from the window, which the injected clock moves. Then the
	// surfaced-once absorb covers N identical captures.
	if got := levels(l.pass(t)); len(got) != 1 || got[0] != supervisor.Absorb {
		t.Fatalf("an unchanged live screen inside the window = %v, want absorbed on progress", got)
	}
	clock.Advance(time.Minute)
	if got := levels(l.pass(t)); len(got) != 1 || got[0] != supervisor.Surface {
		t.Fatalf("an unchanged screen past the window = %v, want surfaced", got)
	}
	for i := 0; i < 3; i++ {
		if got := levels(l.pass(t)); len(got) != 1 || got[0] != supervisor.Absorb {
			t.Fatalf("identical capture %d after the surface = %v, want absorbed", i, got)
		}
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalUnchanged); len(rows) != 1 {
		t.Fatalf("wakes after N identical captures = %d, want one", len(rows))
	}

	// A real change: the pass that sees it makes no wake and resets; the
	// window then elapses on the new screen and it surfaces once more.
	terminals.live("pane-ish", "progress: 1\nprogress: 2\n")
	if got := detectionsOf(l.pass(t), task, supervisor.KindTerminalUnchanged); len(got) != 0 {
		t.Fatalf("the pass that saw the change raised terminal unchanged: %+v", got)
	}
	clock.Advance(time.Minute)
	if got := levels(l.pass(t)); len(got) != 1 || got[0] != supervisor.Surface {
		t.Fatalf("the changed screen past the window = %v, want surfaced once more", got)
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalUnchanged); len(rows) != 2 {
		t.Fatalf("wakes after the change = %d, want two", len(rows))
	}

	// A redraw-only change digests the same and is absorbed.
	terminals.live("pane-ish", "progress: 1\nprogress: 2\rprogress: 2\n")
	for i := 0; i < 2; i++ {
		if got := levels(l.pass(t)); len(got) != 1 || got[0] != supervisor.Absorb {
			t.Fatalf("redraw-only capture %d = %v, want absorbed", i, got)
		}
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalUnchanged); len(rows) != 2 {
		t.Fatalf("wakes after the redraws = %d, want still two", len(rows))
	}
}

// The loop's half of P9: absorbed passes do not move the window start, so a
// screen unchanged with evidence of progress surfaces when the escalation
// window elapses and not before, and every surface of the same screen
// counts a repetition, so the surface at the inspection count is
// surface-and-inspect. The clock is injected; nothing here waits a minute.
func TestEscalationAtTheWindowUnderAnInjectedClock(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	task := plantRunning(t, records, "escalationaa", "pane-esc").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-esc", "thinking\n")
	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	l.nextPass(t)

	windowStart := func() time.Time {
		m, has, err := l.records(t).WakeMarkers(t.Context(), task)
		if err != nil || !has {
			t.Fatalf("markers: %v %v", has, err)
		}
		at, ok := m.WindowStart.Get()
		if !ok {
			t.Fatal("no window start recorded")
		}
		return at
	}
	if got := windowStart(); !got.Equal(start) {
		t.Fatalf("the window started at %v, want the baseline pass's clock %v", got, start)
	}
	for i := 0; i < 3; i++ {
		clock.Advance(10 * time.Second)
		got := detectionsOf(l.pass(t), task, supervisor.KindTerminalUnchanged)
		if len(got) != 1 || got[0].Verdict.Reason != supervisor.ReasonProgress {
			t.Fatalf("absorbed pass %d = %+v, want absorbed on progress", i, got)
		}
		if at := windowStart(); !at.Equal(start) {
			t.Fatalf("an absorbed pass moved the window start to %v from %v", at, start)
		}
	}
	clock.Advance(time.Minute)
	got := detectionsOf(l.pass(t), task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 || got[0].Verdict.Reason != supervisor.ReasonWindowElapsed || got[0].Verdict.Level != supervisor.Surface {
		t.Fatalf("the pass past the window = %+v, want surfaced for the window elapsing", got)
	}
	if at := windowStart(); !at.Equal(clock.Now()) {
		t.Fatalf("the surface left the window start at %v, want reset to %v", at, clock.Now())
	}
	// Repetitions: the second surface is still a surface, the third, at
	// stall_inspection_count 2, demands an inspection.
	clock.Advance(time.Minute)
	got = detectionsOf(l.pass(t), task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 || got[0].Verdict.Level != supervisor.Surface {
		t.Fatalf("the second window elapse = %+v, want a surface", got)
	}
	clock.Advance(time.Minute)
	got = detectionsOf(l.pass(t), task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 || got[0].Verdict.Level != supervisor.SurfaceAndInspect {
		t.Fatalf("the surface at the inspection count = %+v, want surface-and-inspect", got)
	}
	rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalUnchanged)
	if len(rows) != 3 || rows[0].Marker == rows[1].Marker || rows[1].Marker == rows[2].Marker {
		t.Fatalf("the escalation's wakes = %+v, want three rows with distinct markers", rows)
	}
}

// PRD principle P9, the loop's half through the product's own terminal
// seam rather than a double: the service launches a worker at open, the
// loop probes it through the service's provider and captures its output
// through the same handle, so a pass over the unchanged live screen absorbs
// on the positive evidence a Live probe is, within the escalation window;
// once the worker has finished on its own the same seam reads it Dead, the
// pass surfaces terminal gone as dead, and the wake row is written. Nothing
// here states a liveness: every reading is the provider's, and the task
// reads Running from the process tier while the worker runs.
func TestTheLoopAbsorbsALiveWorkerAndSurfacesADeadOne(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	ctx := t.Context()
	id, _ := plantLaunchable(t, h, records)
	agent := workerStandIn(t)
	l := openLoopWith(t, h, records, nil, nil, agents.NewCatalog(fixedRunner{runner: agent.Runner()}))
	defer func() { _ = l.service.Close() }()

	provider, launched, ok := l.service.sessionOf(id)
	if !ok {
		t.Fatalf("the service holds no session for task %s after open", id)
	}
	ref := launched.Reference().String()
	// A status line answered means every line sent before it, the
	// instructions among them, has been read and answered, so the screen
	// is still from here until something is sent.
	if err := provider.Send(ctx, launched, "status?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitCaptured(t, provider, launched, "still working on it")
	if got, err := l.service.tasks().Task(ctx, id); err != nil || got.State != taskstate.Running || got.Basis.Process != taskstate.Live {
		t.Fatalf("the launched task resolves %+v (%v), want Running from the live terminal", got, err)
	}

	// The screen's first reading, then the unchanged one, absorbed. The
	// ticker makes passes of its own at the interval, so each report read
	// here is of a pass begun after the step before it.
	first := l.freshPass(t)
	if gone := detectionsOf(first, id, supervisor.KindTerminalGone); len(gone) != 0 {
		t.Fatalf("a live worker was read as terminal gone: %+v", gone)
	}
	captured, err := provider.Capture(launched)
	if err != nil {
		t.Fatal(err)
	}
	markers, has, err := l.records(t).WakeMarkers(ctx, id)
	if err != nil || !has || markers.CaptureDigest.Or("") != captured.Digest {
		t.Fatalf("the markers are %+v (has %v, %v), want the live screen's digest %s", markers, has, err, captured.Digest)
	}
	second := l.freshPass(t)
	unchanged := detectionsOf(second, id, supervisor.KindTerminalUnchanged)
	if len(unchanged) != 1 || unchanged[0].Verdict.Surfaces() {
		t.Fatalf("the unchanged live screen = %+v, want one terminal-unchanged detection absorbed on the live probe", unchanged)
	}
	if gone := detectionsOf(second, id, supervisor.KindTerminalGone); len(gone) != 0 {
		t.Fatalf("a live worker was read as terminal gone: %+v", gone)
	}

	// The worker finishes on its own, and the same seam surfaces it.
	if err := provider.Send(ctx, launched, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitCaptured(t, provider, launched, "worker finished")
	waitLiveness(t, provider, ref, taskstate.Dead)
	// A ticker pass may have surfaced it already, in which case the kicked
	// pass finds the row standing and absorbs it as surfaced this window;
	// the wake table is the owner of the fact, so it is what is read.
	third := l.freshPass(t)
	if gone := detectionsOf(third, id, supervisor.KindTerminalGone); len(gone) == 1 && !gone[0].Verdict.Surfaces() &&
		gone[0].Verdict.Reason != supervisor.ReasonSurfacedThisWindow {
		t.Fatalf("the finished worker = %+v, want terminal gone surfaced", gone)
	}
	rows := wakesOfKind(t, l.records(t), id, supervisor.KindTerminalGone)
	if len(rows) != 1 || !rows[0].Queued() || !strings.HasPrefix(rows[0].Marker, "terminal-gone:dead:"+ref+":") {
		t.Fatalf("the terminal-gone wakes = %+v, want one queued row for the dead terminal", rows)
	}
	if got, err := l.service.tasks().Task(ctx, id); err != nil || got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess {
		t.Fatalf("the finished task resolves %+v (%v), want Unknown from the dead terminal", got, err)
	}
}

// PRD section 5's crashed worker that is still silent, #95 line H, through
// the real store under an injected clock: a dead terminal surfaces once,
// is absorbed on every pass inside the escalation window, surfaces again
// once the window since that surface has elapsed and not before, with the
// instant in the wake's marker and in the markers' GoneSurfacedAt, and
// keeps that spacing across a restart of the service, because the instant
// it measures from is the durable marker. The wake table is read for
// counts, since a ticker pass may make a surface before the kicked one;
// nothing here sleeps. P9's "absence of evidence surfaces" is what is
// established for the loop: the silence is never absorbed for good.
func TestASilentCrashSurfacesAgainOncePerEscalationWindow(t *testing.T) {
	principles.Cite(t, principles.P9)
	h, records := loopHome(t)
	task := plantRunning(t, records, "silentcrasha", "pane-crashed").ID
	terminals := newScriptedTerminals()
	terminals.dead("pane-crashed")
	start := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	l := openLoop(t, h, records, terminals, clock)
	closed := false
	defer func() {
		if !closed {
			_ = l.service.Close()
		}
	}()

	gone := func(want int) []store.Wake {
		t.Helper()
		rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalGone)
		if len(rows) != want {
			t.Fatalf("at %v the terminal-gone wakes are %d, want %d: %+v", clock.Now(), len(rows), want, rows)
		}
		return rows
	}
	lastSurface := func(want time.Time) {
		t.Helper()
		m, has, err := l.records(t).WakeMarkers(t.Context(), task)
		if err != nil || !has {
			t.Fatalf("markers: %v %v", has, err)
		}
		if at, ok := m.GoneSurfacedAt.Get(); !ok || !at.Equal(want) {
			t.Fatalf("the last surface marker is %v (known %v), want %v", at, ok, want)
		}
	}

	l.freshPass(t)
	first := gone(1)[0]
	if !strings.HasPrefix(first.Marker, "terminal-gone:dead:pane-crashed:") || strings.Contains(first.Marker, ":again:") {
		t.Fatalf("the first surface's marker is %q, want the first composition", first.Marker)
	}
	lastSurface(start)

	// Inside the window: every pass absorbs, however many.
	for i := 0; i < 3; i++ {
		clock.Advance(15 * time.Second)
		r := l.freshPass(t)
		for _, d := range detectionsOf(r, task, supervisor.KindTerminalGone) {
			if d.Verdict.Surfaces() {
				t.Fatalf("a pass %v after the surface surfaced again: %+v", clock.Now().Sub(start), d)
			}
		}
		gone(1)
	}
	lastSurface(start)

	// The window since the surface elapses: one more surface, at once, and
	// the window restarts at it.
	clock.Advance(15 * time.Second)
	l.freshPass(t)
	l.freshPass(t)
	again := gone(2)[1]
	resurfaced := clock.Now()
	if want := terminalGoneAgainMarker(goneDead, "pane-crashed", start, resurfaced); again.Marker != want || !again.Queued() {
		t.Fatalf("the second surface is %+v, want a queued row with marker %q", again, want)
	}
	lastSurface(resurfaced)

	// Acknowledging it changes nothing about the spacing: just short of
	// the next window nothing surfaces, and at it one more does.
	if err := l.records(t).AcknowledgeWakes(t.Context(), []int64{first.ID, again.ID}, store.AcknowledgedByProgram()); err != nil {
		t.Fatalf("acknowledging: %v", err)
	}
	clock.Advance(59 * time.Second)
	l.freshPass(t)
	gone(2)
	// Two passes, as at every surface here: a ticker pass already under
	// way when the clock moved can be the report the first one returns.
	clock.Advance(time.Second)
	l.freshPass(t)
	l.freshPass(t)
	gone(3)
	third := clock.Now()
	lastSurface(third)

	// A restart: the new service reads the instant from the marker, so it
	// neither surfaces early nor forgets the last surface.
	if err := l.service.Close(); err != nil {
		t.Fatalf("closing the service: %v", err)
	}
	closed = true
	clock.Advance(30 * time.Second)
	reopened, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	l = openLoop(t, h, reopened, terminals, clock)
	closed = false
	l.freshPass(t)
	l.freshPass(t)
	gone(3)
	lastSurface(third)
	clock.Advance(30 * time.Second)
	l.freshPass(t)
	l.freshPass(t)
	gone(4)
	lastSurface(clock.Now())
}

// A Complete task with no session is outside the watched set under the
// product's own terminal seam: no pass detects anything for it, no wake is
// written, and a task whose latest attempt names a reference nothing can
// answer surfaces terminal gone as unobservable, which is the reading a
// survivor or a foreign reference gets rather than a dead one.
func TestACompleteTaskProducesNoWakeAndAnUnanswerableReferenceSurfacesAsUnobservable(t *testing.T) {
	h, records := loopHome(t)
	ctx := t.Context()
	unseen := plantRunning(t, records, "unseenaaaaaa", "pane-unseen").ID
	complete := plantComplete(t, records)
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()

	first := l.nextPass(t)
	second := l.freshPass(t)
	if got := detectionsOf(first, unseen, supervisor.KindTerminalGone); len(got) != 1 ||
		!strings.HasPrefix(got[0].Marker, "terminal-gone:unobservable:pane-unseen:") {
		t.Fatalf("the task with an unanswerable reference = %+v, want terminal gone as unobservable", got)
	}
	for _, r := range []passReport{first, second} {
		if got := len(r.Tasks[complete].Detections); got != 0 || r.Tasks[complete].Watched {
			t.Fatalf("the complete task was detected %d times, watched %v; want neither", got, r.Tasks[complete].Watched)
		}
	}
	all, err := l.records(t).TaskWakes(ctx, complete)
	if err != nil || len(all) != 0 {
		t.Fatalf("the complete task's wakes = %+v (%v), want none", all, err)
	}
}

// plantComplete plants a delivery task that resolves Complete: an attributed
// run whose pull request was read as merged and a cleanup record with the
// copy removed, which is the tier internal/taskstate derives Complete from.
func plantComplete(t *testing.T, records *store.Store) string {
	t.Helper()
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "landed", WorkingPath: "/checkouts/landed", UpstreamURL: "https://example.test/landed.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: "run-landed", RepositoryID: "landed", Branch: "done", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("moving the run to running: %v", err)
	}
	if _, err := records.TransitionRunOnPullRequest(ctx, run.ID, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 1, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("recording the merge: %v", err)
	}
	const id = "completedone"
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "headless", WorktreePath: "/w/" + id,
		RepositoryID: store.Known("landed"), Branch: store.Known("done"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	if err := records.SetTaskRun(ctx, id, run.ID); err != nil {
		t.Fatalf("attributing the run: %v", err)
	}
	if _, err := records.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: id, Proof: store.CleanupProofRemoteBranch}, store.TaskDelivery); err != nil {
		t.Fatalf("recording the cleanup: %v", err)
	}
	if err := records.MarkCleanupRemoved(ctx, id, time.Now()); err != nil {
		t.Fatalf("marking the copy removed: %v", err)
	}
	resolved, err := taskstate.NewReader(records, nil, nil).Task(ctx, id)
	if err != nil || resolved.State != taskstate.Complete {
		t.Fatalf("the planted task resolves %s (%v), want complete", resolved.State, err)
	}
	return id
}
