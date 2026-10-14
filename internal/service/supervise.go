package service

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The supervisor loop: PRD section 5's supervision as a goroutine of this
// service, with the repair in place that section asks for and the fence that
// makes repair safe.
//
// # What a pass is
//
// A pass reads the fleet in the order P8's "one consistent read" is
// approximated in: the records, in one snapshot per task through
// taskstate.Reader.Observe; then the slot a run advances in, which is this
// service's own fact; then the provider, through Terminals, a probe per
// terminal inside the resolution and a capture per watched live terminal
// after it. A slot that says advancing beside a record that says the run
// settled is no progress, which internal/taskstate decides on
// Resolution.Progress and this loop takes as given. For each task the pass
// then computes internal/supervisor's inputs from the markers
// internal/store holds for it, classifies through supervisor.Classify, and
// either advances the markers or appends each surfaced wake with the markers
// in one transaction. Nothing here decides a verdict: decide composes the
// inputs and Classify answers. A write that fails for any reason but the
// fence leaves the markers where they were, so the next pass makes the same
// detections again and nothing is dropped; the failure is logged and carried
// on the pass report, and the one failure the loop acts on is
// ErrStaleIncarnation, on which the incarnation returns.
//
// Which tasks a pass watches is supervisor.StateInFlight's table plus the
// tasks with a queued wake, which is the set PRD section 5's "work is in
// flight" names. One detection is made for every task, watched or not: an
// open hold naming it or one of its runs, because a run moved to held
// surfaces always and a task so held is not in flight on its state, so
// nothing but this detection would carry the hold to the queue that then
// keeps the task in flight. Everything else, the events past the marker and
// the terminal, is read for a watched task only, and a task outside the set
// has no marker written: a Complete task whose terminal cleanup closed
// produces no terminal-gone wake and no wake for the history it finished
// with. A task that returns to the set, relaunched after a failure, is read
// from the marker it last advanced to, so the events of its return are
// classified then.
//
// # Terminal gone is three facts
//
// A terminal the pass could not observe is terminal gone, which surfaces
// always, and the marker carries which of the three cases it was: dead,
// where the probe proved absence; unobservable, where the probe could not
// say or the capture failed for this terminal; and provider-wide, where the
// capture failed because the provider itself was unavailable. The first two
// surface per terminal, per reference and case, and the third per outage
// across every terminal that met it: on the first task in the pass's order
// that did, and a pass that finds that task's row already surfaced surfaces
// none of the rest. Each surfaces once and then, for as long as it stays
// unobserved, once more per escalation_window, PRD section 5's crashed
// worker that is still silent. The window is measured from the marker
// store.WakeMarkers.GoneSurfacedAt, which the pass that surfaces writes in
// the wake's own transaction and which an observed pass clears, so a
// restart neither surfaces early nor forgets the last surface; whether it
// has elapsed is an input to supervisor.Classify, which decides. Nothing
// reads the newest wake row for the instant (P8). A marker row written
// before that column existed reads unknown beside a wake already surfaced,
// and the window is then taken to start at the pass that first finds it,
// which can put one surface up to a window later than the last one plus a
// window and never earlier.
// The residual gap is the order, newest task first: a task created during
// an outage precedes the one the outage was filed under and surfaces it a
// second time. A relaunch is nobody's here: the loop surfaces and relaunches
// nothing.
//
// # What the terminal reading is
//
// Terminals is the service's own session provider unless a test passed a
// double, on terminals.go's terms: a worker this service launched reads
// Live through the handle the launch holds and its output is captured
// through the same handle, so an unchanged live terminal is absorbed on
// positive evidence and a dead one surfaces as terminal gone; a survivor an
// earlier service launched, another home's reference and every failure to
// read are Unobserved, and surface as unobservable. A task with no session
// reference is read on an empty screen, which surfaces for want of
// evidence on the same terms as an unchanged one, because P9 forbids
// reading the absence of a terminal as no change.
//
// # Captures are evidence
//
// Only session.Capture.Digest reaches a marker or a wake, never the text,
// and every marker is passed through internal/redact before it is written.
// The capture text is worker output, which P13 says is never relayed and
// which may hold a credential.
//
// # Incarnations, the fence and the watchdog
//
// The loop runs as incarnations. Each begins with store.BeginIncarnation,
// which makes its epoch the current one in the store, the owner of that
// fact; the beacon cites the same epoch beside the service's instance. Every
// write the loop makes, a wake appended or markers advanced, is decided
// against that row in the transaction that would write, and the beacon is
// written under the same mutex the epoch is bumped under, after a read of
// the row, so an incarnation that has been replaced writes neither. Every
// blocking call of a pass runs under the incarnation's context and a bound
// of one beacon interval, so a wedge is a timeout wherever the provider and
// the store honour cancellation; where a provider does not, the goroutine
// wedges, the beacon it alone writes goes stale, and the watchdog replaces
// it.
//
// The watchdog is a second goroutine that reads the beacon file at the
// interval, through the same reader and the same freshness predicate the
// guards use, and judges the current incarnation by it after
// supervisor.FirstBeaconWait has passed since it began: a beacon that is
// missing, another instance's, another epoch's or stale means the loop is
// not writing, so the incarnation's context is cancelled, one interval
// passes, and the next incarnation begins with a bumped epoch. An
// incarnation whose goroutine returned on its own, because a pass met
// ErrStaleIncarnation or the loop panicked, is replaced the same way; a
// panic is recovered, recorded in the service log, and treated as a wedge.
// The watchdog never writes the beacon, so it cannot keep a wedged loop
// looking fresh. The delay between incarnations is not one of
// internal/graph's three bounds and carries no counter: a restart loop
// loses no work and stops no run, so do not add a threshold to it. A
// wedged watchdog is detected by the readers of the beacon, which go stale
// the same way, and is not repaired; repair in place is best-effort and
// reaches the loop only.
//
// # What Close owes a wedged loop
//
// A goroutine wedged in a call that ignores its context cannot be ended,
// only fenced out, so Close waits for the supervision a bounded grace and
// then abandons it with ErrSupervisionAbandoned in its result, which is the
// one exception to the rule that Close waits for every piece of work it
// started. Close's doc states the exception; the fence is what makes it
// safe, since an abandoned incarnation that unwedges meets
// ErrStaleIncarnation at the store and a closed writer at the beacon.

// Terminals is what the supervisor loop observes a task's terminal through:
// the probe and the capture of PRD section 8's provider row, addressed by
// the session reference the attempt's record holds rather than by a handle,
// because the loop reads references out of the record and holds no
// session. The product's implementation is terminals.go's heldTerminals,
// over the service's own provider: it maps a reference to the handle the
// provider holds and answers from the provider, and answers
// ErrTerminalsUnavailable from Capture when the failure is the provider's
// and not the session's. A test passes a double to script what a terminal
// answers.
type Terminals interface {
	// Probe answers what the terminal named by ref looks like now, on
	// session.Provider.Probe's terms: it never fails, and every answer it
	// cannot give is Unobserved.
	Probe(ctx context.Context, ref string) session.Observation
	// Capture returns the terminal's retained output and its digest, on
	// session.Provider.Capture's terms, with the context the one difference:
	// a capture that honours it ends when it does. A failure wrapping
	// ErrTerminalsUnavailable is the provider's; any other failure is this
	// terminal's.
	Capture(ctx context.Context, ref string) (session.Capture, error)
}

// ErrTerminalsUnavailable is what a Terminals implementation wraps when a
// capture failed because the provider as a whole could not answer, rather
// than because this terminal could not be read. The loop surfaces it once
// per pass rather than once per terminal, and marks the wake provider-wide,
// so a provider restart does not read as every worker dying at once. Its one
// producer is heldTerminals' Capture on a service whose provider cannot be
// built, which terminals.go states the reach of.
var ErrTerminalsUnavailable = errors.New("service: the terminal provider is unavailable")

// ErrSupervisionAbandoned reports that Close stopped waiting for the
// supervisor loop or its watchdog after CloseGrace and gave up the home with
// a goroutine of its own still running. The goroutine is fenced out of every
// write and cannot be ended; Close's doc says why this is the one piece of
// work it does not wait for.
var ErrSupervisionAbandoned = errors.New("service: the supervisor loop did not end within Close's grace and was abandoned")

// CloseGraceMultiple is how many beacon intervals Close waits for the
// supervision to end before abandoning it. Two covers a pass that is inside
// its one bounded call when the stop arrives and the beacon write after it,
// each bounded by one interval, so a loop that honours cancellation ends
// within the grace and only a wedged one is abandoned. It is a multiple of
// the interval rather than a configuration row of its own, which PRD section
// 10's "every count or duration is a row" would otherwise ask for: it bounds
// a shutdown and nothing a run observes, and a row for it could only be set
// inconsistently with the per-call bound it exists to cover. Chosen, not
// measured.
const CloseGraceMultiple = 2

// CloseGrace is how long Close waits for the supervision at the configured
// beacon interval: CloseGraceMultiple intervals.
func CloseGrace(interval time.Duration) time.Duration {
	return CloseGraceMultiple * interval
}

// gone is which of PRD section 5's three terminal-gone cases a wake carries.
// It is the component of the wake's marker a coordinator reads the case
// from, and it is never a Kind: the verdict is the same for all three.
type gone string

const (
	// goneDead is a probe that proved the terminal's process absent.
	goneDead gone = "dead"
	// goneUnobservable is a probe that could not say, or a capture that
	// failed for this terminal.
	goneUnobservable gone = "unobservable"
	// goneProviderWide is a capture that failed because the provider could
	// not answer at all.
	goneProviderWide gone = "provider-wide"
)

// supervision is the loop's state on the service: the beacon writer, the
// provider seam, the clock, and what Close reads.
type supervision struct {
	writer    *feed.Writer
	interval  time.Duration
	terminals Terminals
	now       func() time.Time
	redactor  redact.Redactor
	// mu orders a beacon write against the epoch bump: both read the
	// current incarnation out of the store under it, so a write by an
	// incarnation the watchdog is replacing is refused rather than raced.
	mu sync.Mutex
	// done is closed when the watchdog returns, which it does after the
	// current incarnation ended or the grace for it ran out.
	done chan struct{}
	// abandoned is whether an incarnation was still running when the
	// watchdog stopped waiting for it.
	abandoned atomic.Bool
	hooks     supervisionHooks
}

// supervisionHooks are the seams this package's tests drive the loop
// through. Every one is nil in production and reached from internal tests
// only, on the terms betweenAttemptReadAndClaim is: the loop starts inside
// Open, so they are handed over on Options rather than set afterwards.
type supervisionHooks struct {
	// now replaces the clock the loop and the watchdog read, so a window
	// can elapse under a test without waiting for it.
	now func() time.Time
	// afterPass is called with every pass's report, after its writes.
	afterPass func(passReport)
	// onReplace is called by the watchdog with the incarnation it is
	// replacing and the one it began, after the delay.
	onReplace func(replaced, next *incarnation)
	// kick makes the current incarnation run a pass when it is idle: a
	// receive on it, by the loop, is one pass.
	kick chan struct{}
}

// incarnation is one incarnation of the loop: its fence, its context, when it
// began, and the channel closed when its goroutine returns, which a wedged
// goroutine never closes.
type incarnation struct {
	inc     store.Incarnation
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
	ended   chan struct{}
}

// passReport is what one pass found and did, for the service log and the
// tests: per task, the detections with their verdicts and dispositions, and
// any failure.
type passReport struct {
	Epoch uint64
	// Err is a failure that ended the pass before any task was read.
	Err   error
	Tasks map[string]taskReport
}

// taskReport is one task's part of a pass.
type taskReport struct {
	Watched    bool
	Detections []detection
	// Err is the failure that stopped this task's writes, nil when they
	// landed. A failure leaves the markers where they were, so the next
	// pass makes the same detections again.
	Err error
}

// detection is one wake the pass composed for a task, with Classify's answer
// and, when it surfaced, what the store did with it.
type detection struct {
	Kind        supervisor.Kind
	Marker      string
	Verdict     supervisor.Verdict
	Disposition store.WakeDisposition
}

// startSupervision opens the beacon writer, begins the first incarnation and
// starts the watchdog. It runs after recovery and before the caller serves,
// so a run recovery continued is already under supervision when the first
// request arrives.
func (s *Service) startSupervision(hooks supervisionHooks, terminals Terminals) error {
	writer, err := feed.OpenWriter(s.home.Beacon())
	if err != nil {
		return err
	}
	now := hooks.now
	if now == nil {
		now = time.Now
	}
	s.sup = &supervision{
		writer:    writer,
		interval:  s.cfg.SupervisionBeaconInterval,
		terminals: terminals,
		now:       now,
		redactor:  redact.New(),
		done:      make(chan struct{}),
		hooks:     hooks,
	}
	go s.watchdog()
	return nil
}

// bound is the per-operation context every blocking call of a pass runs
// under: the parent, which is the incarnation's, and one beacon interval.
func (sv *supervision) bound(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, sv.interval)
}

// beginIncarnation bumps the epoch in the store, under the mutex the beacon
// write takes, and returns the incarnation to run.
func (s *Service) beginIncarnation() (*incarnation, error) {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	ctx, cancel := s.sup.bound(s.stopCtx)
	defer cancel()
	inc, err := s.store.BeginIncarnation(ctx)
	if err != nil {
		return nil, err
	}
	c := &incarnation{inc: inc, started: s.sup.now(), ended: make(chan struct{})}
	c.ctx, c.cancel = context.WithCancel(s.stopCtx)
	return c, nil
}

// writeBeacon writes the beacon for an incarnation, fenced: the current
// epoch is read out of the store under the mutex beginIncarnation bumps it
// under, and a mismatch is ErrStaleIncarnation with nothing written. It is
// the only writer of the beacon, and it is called from the loop's own select
// and nowhere else.
func (s *Service) writeBeacon(c *incarnation) error {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	ctx, cancel := s.sup.bound(c.ctx)
	defer cancel()
	epoch, _, ok, err := s.store.CurrentIncarnation(ctx)
	if err != nil {
		return err
	}
	if !ok || epoch != c.inc.Epoch() {
		return fmt.Errorf("%w: a beacon write by epoch %d, current %d", store.ErrStaleIncarnation, c.inc.Epoch(), epoch)
	}
	_, err = s.sup.writer.Write(supervisor.Identity{Instance: s.instance, Epoch: c.inc.Epoch()}, s.sup.now())
	return err
}

// loop is one incarnation's goroutine. It writes the beacon from its own
// select, at the interval, idle fleet or not, and runs a pass after each
// write; it returns when its context ends, when a write of its is refused as
// stale, or when a pass panics, and the watchdog replaces it in the last two
// cases.
func (s *Service) loop(c *incarnation) {
	defer close(c.ended)
	defer func() {
		if value := recover(); value != nil {
			s.log.Printf("supervisor incarnation %d panicked and is treated as wedged: %v\n%s", c.inc.Epoch(), value, debug.Stack())
		}
	}()
	if err := s.writeBeacon(c); err != nil {
		s.log.Printf("supervisor incarnation %d could not write its first beacon: %v", c.inc.Epoch(), err)
		return
	}
	if s.passAndReport(c) {
		return
	}
	ticker := time.NewTicker(s.sup.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-s.sup.hooks.kick:
			if s.passAndReport(c) {
				return
			}
		case <-ticker.C:
			if err := s.writeBeacon(c); err != nil {
				s.log.Printf("supervisor incarnation %d stops: %v", c.inc.Epoch(), err)
				return
			}
			if s.passAndReport(c) {
				return
			}
		}
	}
}

// passAndReport runs one pass, logs what failed, hands the report to the
// test hook, and reports whether the incarnation has been replaced, which is
// what ErrStaleIncarnation on any write means.
func (s *Service) passAndReport(c *incarnation) (replaced bool) {
	report := s.pass(c)
	if report.Err != nil {
		s.log.Printf("supervisor incarnation %d could not read the fleet: %v", c.inc.Epoch(), report.Err)
		replaced = errors.Is(report.Err, store.ErrStaleIncarnation)
	}
	for task, tr := range report.Tasks {
		if tr.Err != nil {
			s.log.Printf("supervisor incarnation %d could not record its pass over task %s: %v", c.inc.Epoch(), task, tr.Err)
			if errors.Is(tr.Err, store.ErrStaleIncarnation) {
				replaced = true
			}
		}
	}
	if s.sup.hooks.afterPass != nil {
		s.sup.hooks.afterPass(report)
	}
	return replaced
}

// pass is one pass over the fleet, in the order the file comment states.
func (s *Service) pass(c *incarnation) passReport {
	report := passReport{Epoch: c.inc.Epoch(), Tasks: make(map[string]taskReport)}
	now := s.sup.now()
	probes := make(map[string]session.Observation)
	probe := func(ctx context.Context, ref string) taskstate.Liveness {
		obs := s.sup.terminals.Probe(ctx, ref)
		probes[ref] = obs
		return obs.Liveness
	}
	ctx, cancel := s.sup.bound(c.ctx)
	observed, err := s.reader(probe).Observe(ctx)
	cancel()
	if err != nil {
		report.Err = err
		return report
	}
	// The pass is the one reader that publishes a task's state, on
	// publish.go's terms: every task whose basis moved since the last
	// publication, once, from the resolution the pass is about to decide on.
	s.publishTaskChanges(observed)
	ctx, cancel = s.sup.bound(c.ctx)
	unacknowledged, err := s.store.UnacknowledgedWakes(ctx)
	cancel()
	if err != nil {
		report.Err = err
		return report
	}
	providerWideSurfaced := false
	for _, o := range observed {
		id := o.Snapshot.Task.ID
		report.Tasks[id] = s.superviseTask(c, o, probes, unacknowledged[id] > 0, now, &providerWideSurfaced)
	}
	return report
}

// superviseTask is the pass over one task: the terminal reading for a watched
// task, the decision, and the writes.
func (s *Service) superviseTask(c *incarnation, o taskstate.Observed, probes map[string]session.Observation,
	queued bool, now time.Time, providerWideSurfaced *bool) taskReport {
	id := o.Snapshot.Task.ID
	inFlight, decided := supervisor.StateInFlight(o.State.State)
	obs := observation{observed: o, watched: inFlight || !decided || queued}
	var err error
	ctx, cancel := s.sup.bound(c.ctx)
	obs.markers, obs.has, err = s.store.WakeMarkers(ctx, id)
	cancel()
	if err != nil {
		return taskReport{Watched: obs.watched, Err: err}
	}
	ctx, cancel = s.sup.bound(c.ctx)
	wakes, err := s.store.TaskWakes(ctx, id)
	cancel()
	if err != nil {
		return taskReport{Watched: obs.watched, Err: err}
	}
	obs.surfaced = make(map[string]bool, len(wakes))
	for _, w := range wakes {
		obs.surfaced[surfacedKey(supervisor.Kind(w.Kind), w.Marker)] = true
	}
	if n := len(o.Snapshot.Attempts); n > 0 {
		obs.ref, _ = o.Snapshot.Attempts[n-1].SessionRef.Get()
	}
	if obs.watched && obs.ref != "" {
		obs.screen, obs.gone = s.readTerminal(c, obs.ref, probes[obs.ref])
	}
	d := decide(obs, s.cfg, now, providerWideSurfaced)
	for i := range d.detections {
		d.detections[i].Marker = s.sup.redactor.Redact(d.detections[i].Marker)
	}
	return s.record(c, id, d)
}

// readTerminal is the provider half of a watched task's reading: a capture
// of a live terminal, or which gone case it is.
func (s *Service) readTerminal(c *incarnation, ref string, probed session.Observation) (store.Optional[string], gone) {
	switch probed.Liveness {
	case taskstate.Live:
		ctx, cancel := s.sup.bound(c.ctx)
		defer cancel()
		captured, err := s.sup.terminals.Capture(ctx, ref)
		switch {
		case errors.Is(err, ErrTerminalsUnavailable):
			return store.Optional[string]{}, goneProviderWide
		case err != nil:
			return store.Optional[string]{}, goneUnobservable
		}
		return store.Known(captured.Digest), ""
	case taskstate.Dead:
		return store.Optional[string]{}, goneDead
	default:
		return store.Optional[string]{}, goneUnobservable
	}
}

// record makes the pass's writes for one task: each surfaced detection with
// the markers in one transaction, or the markers alone when nothing
// surfaced, and nothing when the task was not watched and nothing was
// detected. Every write is fenced by the incarnation.
func (s *Service) record(c *incarnation, id string, d decision) taskReport {
	out := taskReport{Watched: d.watched, Detections: d.detections}
	surfaced := 0
	for i, det := range d.detections {
		if !det.Verdict.Surfaces() {
			continue
		}
		surfaced++
		ctx, cancel := s.sup.bound(c.ctx)
		appended, err := s.store.AppendWakeWithMarkers(ctx, c.inc,
			store.WakeRequest{TaskID: id, Kind: string(det.Kind), Marker: det.Marker}, d.advance)
		cancel()
		if err != nil {
			out.Err = err
			return out
		}
		out.Detections[i].Disposition = appended.Disposition
	}
	if surfaced == 0 && d.write {
		ctx, cancel := s.sup.bound(c.ctx)
		_, err := s.store.AdvanceWakeMarkers(ctx, c.inc, id, d.advance)
		cancel()
		out.Err = err
	}
	return out
}

// observation is what one pass found about one task, before the decision:
// the resolution and its records, the markers, the wakes already surfaced,
// and the terminal reading for a watched task.
type observation struct {
	observed taskstate.Observed
	watched  bool
	markers  store.WakeMarkers
	has      bool
	// surfaced is every (kind, marker) already in the wake table, queued or
	// acknowledged: a detection with that key was surfaced and is not made
	// again. The table is the owner of that fact; it is read for existence
	// and never for what is true of the task now.
	surfaced map[string]bool
	// ref is the latest attempt's session reference, empty when it has
	// none.
	ref string
	// screen is the capture digest, known when the terminal was captured.
	screen store.Optional[string]
	// gone is which gone case the terminal was, empty when it was captured
	// or there was no terminal to read.
	gone gone
}

// decision is what decide composed for one task: the detections with their
// verdicts, the markers to write, and whether to write them at all.
type decision struct {
	watched    bool
	detections []detection
	advance    store.MarkerAdvance
	write      bool
}

func surfacedKey(kind supervisor.Kind, marker string) string { return string(kind) + "\x00" + marker }

// decide composes internal/supervisor's inputs for one task from what the
// pass observed and the markers, and classifies each detection. It is pure:
// now is the pass's clock, cfg carries the two windows and the inspection
// count, and providerWideSurfaced is the pass's one-surface memory for the
// provider-wide case.
//
// The detections, in order: an open hold not yet surfaced, and for a watched
// task each event past the marker's sequence and then its terminal. The
// terminal
// reading is one of: terminal gone, with the case and the unobserved-since
// instant in the marker, surfaced once per reference and case and then
// once per escalation window measured from the GoneSurfacedAt marker, with
// that surface's instant added to the marker; a screen
// that changed, which resets the surfaced flag, the window and the stall
// count and makes no wake; a declared wait, when the task resolves Paused,
// measured against the recheck window; no change, when the screen is empty,
// nothing changed and there is evidence of progress; and otherwise terminal
// unchanged, measured against the escalation window with the marker's
// surfaced flag and stall count. A task with no terminal to read has an
// empty screen, and an empty screen unchanged is read as an unchanged one,
// because P9 forbids reading the absence of a terminal as no change.
//
// The window start moves only on a change and on a surface, never on an
// absorbed pass, which is the loop's half of P9's "never absorb on elapsed
// time alone": the escalation window measures from the screen's first
// appearance or its last surface, so a window can elapse at all.
func decide(o observation, cfg config.Config, now time.Time, providerWideSurfaced *bool) decision {
	d := decision{watched: o.watched, advance: carry(o.markers)}
	evidence := supervisor.EvidenceOf(o.observed.State)
	detect := func(wake supervisor.Wake, marker string, window supervisor.WindowState, surfaced bool, stall supervisor.Stall) supervisor.Verdict {
		v := supervisor.Classify(wake, evidence, window, surfaced, stall)
		d.detections = append(d.detections, detection{Kind: wake.Kind(), Marker: marker, Verdict: v})
		return v
	}
	seen := func(kind supervisor.Kind, marker string) bool { return o.surfaced[surfacedKey(kind, marker)] }

	for _, h := range o.observed.Snapshot.Holds {
		marker := "hold:" + h.Key
		if !h.Open() || seen(supervisor.KindHoldOpened, marker) {
			continue
		}
		wake, _ := supervisor.Of(supervisor.KindHoldOpened)
		detect(wake, marker, supervisor.WindowOpen, false, supervisor.Stall{})
	}
	if !o.watched {
		d.write = len(d.detections) > 0
		return d
	}
	for _, e := range o.observed.Snapshot.Events {
		marker := fmt.Sprintf("event:%d", e.Sequence)
		wake := supervisor.Event(e.Kind)
		if e.Sequence <= o.markers.EventSequence || seen(wake.Kind(), marker) {
			continue
		}
		detect(wake, marker, supervisor.WindowOpen, false, supervisor.Stall{})
	}
	d.advance.EventSequence = max(o.markers.EventSequence, o.observed.State.Basis.EventSequence)
	d.write = true

	if o.gone != "" {
		since, ok := o.markers.UnobservedSince.Get()
		if !ok {
			since = now
		}
		d.advance.UnobservedSince = store.Known(since)
		marker := terminalGoneMarker(o.gone, o.ref, since)
		if o.gone == goneProviderWide && *providerWideSurfaced {
			return d
		}
		surfaced := seen(supervisor.KindTerminalGone, marker)
		window := supervisor.WindowOpen
		if surfaced {
			last, ok := o.markers.GoneSurfacedAt.Get()
			if !ok {
				last = now
				d.advance.GoneSurfacedAt = store.Known(now)
			}
			if window = elapsed(last, cfg.EscalationWindow, now); window == supervisor.WindowElapsed {
				marker = terminalGoneAgainMarker(o.gone, o.ref, since, now)
			}
		}
		wake, _ := supervisor.Of(supervisor.KindTerminalGone)
		if v := detect(wake, marker, window, surfaced, supervisor.Stall{}); v.Surfaces() {
			d.advance.GoneSurfacedAt = store.Known(now)
		}
		if o.gone == goneProviderWide {
			*providerWideSurfaced = true
		}
		return d
	}

	d.advance.UnobservedSince = store.Optional[time.Time]{}
	d.advance.GoneSurfacedAt = store.Optional[time.Time]{}
	if !o.has || o.markers.CaptureDigest != o.screen {
		d.advance.CaptureDigest = o.screen
		d.advance.Surfaced = false
		d.advance.WindowStart = store.Known(now)
		d.advance.StallRepetitions = 0
		return d
	}
	start, ok := o.markers.WindowStart.Get()
	if !ok {
		start = now
		d.advance.WindowStart = store.Known(now)
	}
	screen, _ := o.screen.Get()
	if screen == "" {
		screen = "none"
	}
	switch {
	case o.observed.State.State == taskstate.Paused:
		wake, _ := supervisor.Of(supervisor.KindDeclaredWait)
		marker := fmt.Sprintf("declared-wait:%d:%s", o.observed.State.Basis.EventSequence, start.UTC().Format(time.RFC3339Nano))
		if v := detect(wake, marker, elapsed(start, cfg.RecheckWindow, now), false, supervisor.Stall{}); v.Surfaces() {
			d.advance.WindowStart = store.Known(now)
		}
	case !o.screen.IsKnown() && evidence == supervisor.Progress:
		wake, _ := supervisor.Of(supervisor.KindNoChange)
		detect(wake, "no-change", supervisor.WindowOpen, false, supervisor.Stall{})
	default:
		wake, _ := supervisor.Of(supervisor.KindTerminalUnchanged)
		marker := fmt.Sprintf("terminal:%s@%d#%d", screen, o.markers.ChangeEpoch, o.markers.StallRepetitions+1)
		stall := supervisor.Stall{Repetitions: int(o.markers.StallRepetitions), InspectAt: cfg.StallInspectionCount}
		if v := detect(wake, marker, elapsed(start, cfg.EscalationWindow, now), o.markers.Surfaced, stall); v.Surfaces() {
			d.advance.Surfaced = true
			d.advance.StallRepetitions = o.markers.StallRepetitions + 1
			d.advance.WindowStart = store.Known(now)
		}
	}
	return d
}

// terminalGoneMarker composes a terminal-gone wake's marker: the case, the
// session reference and the unobserved-since instant, which is what
// internal/store's WakeRequest states the loop composes for that kind. It
// and terminalGoneAgainMarker are the composers, and terminalGoneAbout
// beside them is the one reader of the composition, for the relaunch that
// acknowledges the rows about a session it replaced.
func terminalGoneMarker(g gone, ref string, since time.Time) string {
	return fmt.Sprintf("terminal-gone:%s:%s:%s", g, ref, since.UTC().Format(time.RFC3339Nano))
}

// terminalGoneAgainMarker composes the marker of a terminal-gone wake that
// surfaces again for the same silence once the escalation window has
// elapsed: the first surface's marker with the instant of this surface
// after it, which is the component that keeps each window's surface from
// reading as a replay of the one before. It begins with terminalGoneMarker's
// composition, so terminalGoneAbout matches it on the same terms.
func terminalGoneAgainMarker(g gone, ref string, since, at time.Time) string {
	return terminalGoneMarker(g, ref, since) + ":again:" + at.UTC().Format(time.RFC3339Nano)
}

// terminalGoneAbout reports whether a terminal-gone marker this loop
// composed names the session reference: the reference sits between the
// case and the instant, and a marker composed any other way is not matched.
func terminalGoneAbout(marker, ref string) bool {
	rest, ok := strings.CutPrefix(marker, "terminal-gone:")
	if !ok {
		return false
	}
	for _, g := range []gone{goneDead, goneUnobservable, goneProviderWide} {
		if strings.HasPrefix(rest, string(g)+":"+ref+":") {
			return true
		}
	}
	return false
}

// carry is the markers as they stand, as the advance a pass starts from.
func carry(m store.WakeMarkers) store.MarkerAdvance {
	return store.MarkerAdvance{
		CaptureDigest: m.CaptureDigest, EventSequence: m.EventSequence, Surfaced: m.Surfaced,
		WindowStart: m.WindowStart, StallRepetitions: m.StallRepetitions, UnobservedSince: m.UnobservedSince,
		GoneSurfacedAt: m.GoneSurfacedAt,
	}
}

// elapsed is whether a window of the given length that began at start has
// elapsed at now.
func elapsed(start time.Time, window time.Duration, now time.Time) supervisor.WindowState {
	if now.Sub(start) >= window {
		return supervisor.WindowElapsed
	}
	return supervisor.WindowOpen
}

// watchdog runs incarnations: it begins the first, judges the current one by
// the beacon at every interval, and replaces one whose beacon is stale or
// whose goroutine returned, after one interval's delay. It never writes the
// beacon. On stop it cancels the current incarnation, waits CloseGrace for
// its goroutine, and records abandonment when that runs out.
func (s *Service) watchdog() {
	defer close(s.sup.done)
	fresh := supervisor.FreshnessFor(s.sup.interval)
	firstWait := supervisor.FirstBeaconWait(s.sup.interval)
	grace := CloseGrace(s.sup.interval)
	var current *incarnation
	begin := func() {
		c, err := s.beginIncarnation()
		if err != nil {
			s.log.Printf("the supervisor could not begin an incarnation: %v", err)
			current = nil
			return
		}
		current = c
		s.log.Printf("supervisor incarnation %d started", c.inc.Epoch())
		go s.loop(c)
	}
	stop := func() {
		if current == nil {
			return
		}
		current.cancel()
		select {
		case <-current.ended:
		case <-time.After(grace):
			s.sup.abandoned.Store(true)
			s.log.Printf("supervisor incarnation %d did not end within %v and is abandoned", current.inc.Epoch(), grace)
		}
	}
	begin()
	ticker := time.NewTicker(s.sup.interval)
	defer ticker.Stop()
	for {
		var ended chan struct{}
		if current != nil {
			ended = current.ended
		}
		select {
		case <-s.stopCtx.Done():
			stop()
			return
		case <-ended:
			s.log.Printf("supervisor incarnation %d ended on its own and is replaced", current.inc.Epoch())
		case <-ticker.C:
			if current != nil && s.beaconLive(current, fresh, firstWait) {
				continue
			}
			if current != nil {
				s.log.Printf("supervisor incarnation %d has a stale beacon and is replaced", current.inc.Epoch())
				current.cancel()
			}
		}
		replaced := current
		select {
		case <-s.stopCtx.Done():
			stop()
			return
		case <-time.After(s.sup.interval):
		}
		begin()
		if s.sup.hooks.onReplace != nil {
			s.sup.hooks.onReplace(replaced, current)
		}
	}
}

// beaconLive is the watchdog's judgement of an incarnation: a beacon under
// this instance and the incarnation's epoch that supervisor.IsFresh reads
// as fresh, or an incarnation that began less than the first-beacon wait
// ago and is not yet judged. It reads the file the guards read, through
// their reader, so what the watchdog accepts is what a guard would.
func (s *Service) beaconLive(c *incarnation, fresh supervisor.Freshness, firstWait time.Duration) bool {
	now := s.sup.now()
	if now.Sub(c.started) < firstWait {
		return true
	}
	b, err := feed.ReadBeacon(s.home.Beacon())
	if err != nil {
		return false
	}
	return b.Instance == s.instance && b.Epoch == c.inc.Epoch() && supervisor.IsFresh(b, fresh, now)
}

// stopSupervision is Close's wait: the grace for the watchdog, which itself
// gives the current incarnation the grace, and then the writer closed
// whatever is still running. It returns ErrSupervisionAbandoned when either
// wait ran out.
func (s *Service) stopSupervision() error {
	if s.sup == nil {
		return nil
	}
	select {
	case <-s.sup.done:
	case <-time.After(CloseGrace(s.sup.interval)):
		s.sup.abandoned.Store(true)
	}
	err := s.sup.writer.Close()
	if s.sup.abandoned.Load() {
		return errors.Join(ErrSupervisionAbandoned, err)
	}
	return err
}
