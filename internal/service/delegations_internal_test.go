package service

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// plantDelegation delegates a domain with one attempt whose session is ref,
// the way domain.delegate and the launch record one.
func plantDelegation(t *testing.T, records *store.Store, domain, ref string) store.Delegation {
	t.Helper()
	ctx := t.Context()
	d, err := records.Delegate(ctx, store.DelegationRequest{Domain: domain, Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatalf("delegating %s: %v", domain, err)
	}
	if _, err := records.BeginDelegationAttempt(ctx, d.ID, 0); err != nil {
		t.Fatalf("beginning the attempt of %s: %v", domain, err)
	}
	if err := records.SetDelegationAttemptSession(ctx, d.ID, 1, ref); err != nil {
		t.Fatalf("recording the session of %s: %v", domain, err)
	}
	return d
}

// domainDetections is the pass's detections of one kind for a domain.
func domainDetections(r passReport, domain string, kind supervisor.Kind) []detection {
	var out []detection
	for _, d := range r.Delegations[domain].Detections {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// domainWakesOfKind is the delegation's domain wakes of one kind, oldest
// first, read from the records.
func domainWakesOfKind(t *testing.T, records *store.Store, delegation int64, kind supervisor.Kind) []store.DomainWake {
	t.Helper()
	all, err := records.DelegationWakes(t.Context(), delegation)
	if err != nil {
		t.Fatalf("reading the wakes of delegation %d: %v", delegation, err)
	}
	var out []store.DomainWake
	for _, w := range all {
		if w.Kind == string(kind) {
			out = append(out, w)
		}
	}
	return out
}

// A standing delegation whose subordinate's terminal probes Dead surfaces
// subordinate-gone on the queue's second shape, once, then is absorbed on
// every pass inside the escalation window and surfaces again once the
// window since that surface has elapsed, with the instant in the marker,
// exactly as a task's terminal gone is spaced. The spacing is read off the
// delegation's marker row, GoneSurfacedAt, written in the wake's own
// transaction, and never off the newest wake row (P8). relaunch_attempts
// is zero so that recovery at open leaves the dead subordinate for the loop
// rather than relaunching it first; the bound's hold that raises is the
// next test's subject and is left alone here.
func TestAGoneSubordinateSurfacesOncePerEscalationWindowFromItsMarkerRow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "relaunch_attempts": 0}`)
	d := plantDelegation(t, records, "alpha", "sub-crashed")
	terminals := newScriptedTerminals()
	terminals.dead("sub-crashed")
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()

	gone := func(want int) []store.DomainWake {
		t.Helper()
		rows := domainWakesOfKind(t, l.records(t), d.ID, supervisor.KindSubordinateGone)
		if len(rows) != want {
			t.Fatalf("at %v the subordinate-gone wakes are %d, want %d: %+v", clock.Now(), len(rows), want, rows)
		}
		return rows
	}
	lastSurface := func(want time.Time) {
		t.Helper()
		m, has, err := l.records(t).DelegationMarkers(t.Context(), d.ID)
		if err != nil || !has {
			t.Fatalf("markers: %v %v", has, err)
		}
		if at, ok := m.GoneSurfacedAt.Get(); !ok || !at.Equal(want) {
			t.Fatalf("the last surface marker is %v (known %v), want %v", at, ok, want)
		}
	}
	surfaces := func(r passReport) bool {
		t.Helper()
		got := domainDetections(r, "alpha", supervisor.KindSubordinateGone)
		if len(got) != 1 || r.Delegations["alpha"].Err != nil {
			t.Fatalf("at %v the pass's subordinate-gone detections are %+v (%v), want one", clock.Now(), got, r.Delegations["alpha"].Err)
		}
		return got[0].Verdict.Surfaces()
	}

	if !surfaces(l.nextPass(t)) {
		t.Fatal("the first pass over the dead subordinate did not surface it")
	}
	first := gone(1)[0]
	if first.Marker != subordinateGoneMarker(goneDead, 1, start) || first.Domain != "alpha" || !first.Queued() {
		t.Fatalf("the first surface is %+v, want a queued row of alpha with the first composition", first)
	}
	lastSurface(start)
	for i := 0; i < 3; i++ {
		clock.Advance(15 * time.Second)
		if surfaces(l.pass(t)) {
			t.Fatalf("a pass %v after the surface surfaced again", clock.Now().Sub(start))
		}
		gone(1)
	}
	lastSurface(start)
	clock.Advance(15 * time.Second)
	if !surfaces(l.pass(t)) {
		t.Fatal("the pass at the window did not surface the silence again")
	}
	again := gone(2)[1]
	if want := subordinateGoneAgainMarker(goneDead, 1, start, clock.Now()); again.Marker != want || !again.Queued() {
		t.Fatalf("the second surface is %+v, want a queued row with marker %q", again, want)
	}
	lastSurface(clock.Now())
}

// Past relaunch_attempts a dead subordinate's delegation holds for a
// person: the pass registers the bound's hold keyed by the attempt, reads
// it back as open, and surfaces it once as a hold-opened wake on the
// queue's second shape beside the subordinate-gone, and a later pass
// surfaces neither again. The hold is read from its row (P8), and it is
// the one respondDomain answers.
func TestADelegationPastTheBoundHoldsForAPersonAndIsToldOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "relaunch_attempts": 0}`)
	d := plantDelegation(t, records, "alpha", "sub-crashed")
	terminals := newScriptedTerminals()
	terminals.dead("sub-crashed")
	clock := &fakeClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()

	first := l.nextPass(t)
	if err := first.Delegations["alpha"].Err; err != nil {
		t.Fatalf("the first pass over the delegation failed: %v", err)
	}
	for _, kind := range []supervisor.Kind{supervisor.KindHoldOpened, supervisor.KindSubordinateGone} {
		got := domainDetections(first, "alpha", kind)
		if len(got) != 1 || !got[0].Verdict.Surfaces() || got[0].Disposition != store.WakeAppended {
			t.Fatalf("the first pass's %s detections are %+v, want one surfaced and appended", kind, got)
		}
	}
	hold, err := l.records(t).Hold(t.Context(), domainBoundKey("alpha", 1))
	if err != nil || !hold.Open() || hold.TaskID.IsKnown() || !strings.Contains(hold.Detail, "--domain") {
		t.Fatalf("the bound's hold reads %+v (%v), want it open, naming no task and saying how it is answered", hold, err)
	}
	if rows := domainWakesOfKind(t, l.records(t), d.ID, supervisor.KindHoldOpened); len(rows) != 1 || rows[0].Marker != "hold:"+hold.Key {
		t.Fatalf("the hold-opened domain wakes are %+v, want one naming the hold", rows)
	}
	clock.Advance(10 * time.Second)
	second := l.pass(t)
	if got := domainDetections(second, "alpha", supervisor.KindHoldOpened); len(got) != 0 {
		t.Fatalf("the second pass detected the hold again: %+v", got)
	}
	if got := domainDetections(second, "alpha", supervisor.KindSubordinateGone); len(got) != 1 || got[0].Verdict.Surfaces() {
		t.Fatalf("the second pass's subordinate-gone detections are %+v, want one absorbed inside the window", got)
	}
	if rows := domainWakesOfKind(t, l.records(t), d.ID, supervisor.KindHoldOpened); len(rows) != 1 {
		t.Fatalf("the hold surfaced %d times", len(rows))
	}
}

// The mutation check: the decision the loop acts on surfaces a dead
// subordinate through the real classifier, and the same observation run
// through a classifier that absorbs everything, which is the production
// forced off, surfaces nothing, so a loop that stopped producing the kind
// would fail the checks above rather than pass them on nothing. A Live
// probe surfaces nothing through the real classifier either, which is the
// positive control for the absorb.
func TestTheGoneDecisionCanBeForcedOffAndTheChecksSeeIt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	cfg := config.Config{EscalationWindow: time.Minute}
	dead := delegationObservation{delegation: store.Delegation{ID: 1, Domain: "alpha"}, attempt: 1, ref: "r", liveness: taskstate.Dead, surfaced: map[string]bool{}}
	real := decideDelegation(dead, cfg, now)
	if len(real.detections) != 1 || !real.detections[0].Verdict.Surfaces() || real.detections[0].Kind != supervisor.KindSubordinateGone {
		t.Fatalf("the real decision over a dead subordinate is %+v, want one surfaced subordinate-gone", real.detections)
	}
	if at, ok := real.advance.GoneSurfacedAt.Get(); !ok || !at.Equal(now) {
		t.Fatalf("the real decision records the surface at %v (%v), want now", at, ok)
	}
	off := func(supervisor.Wake, supervisor.Evidence, supervisor.WindowState, bool, supervisor.Stall) supervisor.Verdict {
		return supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonNoChange}
	}
	forced := decideDelegationWith(dead, cfg, now, off)
	if len(forced.detections) != 1 || forced.detections[0].Verdict.Surfaces() || forced.advance.GoneSurfacedAt.IsKnown() {
		t.Fatalf("with the production forced off the decision is %+v, so the checks could not tell it from the real one", forced)
	}
	live := dead
	live.liveness = taskstate.Live
	if got := decideDelegation(live, cfg, now); len(got.detections) != 0 || got.write {
		t.Fatalf("a live subordinate with no markers to clear decided %+v, want nothing", got)
	}
}
