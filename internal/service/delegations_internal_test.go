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
	hold, err := l.records(t).Hold(t.Context(), domainBoundKey(d, 1))
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

// The bound's hold is keyed by the delegation as well as the attempt: a
// second delegation of the same domain whose first attempt is found dead
// past the bound holds again, on a key of its own, and is not begun again
// on the answer the first delegation's hold was given. The counterfeit is
// the key the first slice used, the domain and the attempt alone, resolved
// retry the way the first delegation's answer left it: with that key the
// reconciliation would read the answer as the second delegation's and
// relaunch past the bound, so the check reads the second delegation's
// attempts and its own hold rather than the first's.
func TestASecondDelegationOfADomainHoldsAtTheBoundOnItsOwnKey(t *testing.T) {
	t.Parallel()
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "relaunch_attempts": 0}`)
	first := plantDelegation(t, records, "alpha", "sub-one")
	terminals := newScriptedTerminals()
	terminals.dead("sub-one")
	terminals.dead("sub-two")
	clock := &fakeClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	ctx := t.Context()

	if err := l.nextPass(t).Delegations["alpha"].Err; err != nil {
		t.Fatalf("the first pass: %v", err)
	}
	firstKey := domainBoundKey(first, 1)
	if hold, err := l.records(t).Hold(ctx, firstKey); err != nil || !hold.Open() {
		t.Fatalf("the first delegation's hold reads %+v (%v), want it open", hold, err)
	}
	// A person answers retry, as respondDomain records it, and the
	// delegation is then ended by the coordinator; the counterfeit key is
	// answered the same way.
	if _, err := l.records(t).ResolveHold(ctx, firstKey, "retry", store.ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	counterfeit := "domain/alpha" + domainBoundPrefix + "1"
	if _, err := l.records(t).RegisterHold(ctx, store.Hold{Key: counterfeit, Subject: "the key the first slice used"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.records(t).ResolveHold(ctx, counterfeit, "retry", store.ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.records(t).EndDelegation(ctx, first.ID, store.EndedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	second, err := l.records(t).Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated, Expected: first.ID})
	if err != nil || second.ID == first.ID {
		t.Fatalf("delegating alpha again: %+v, %v", second, err)
	}
	if _, err := l.records(t).BeginDelegationAttempt(ctx, second.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := l.records(t).SetDelegationAttemptSession(ctx, second.ID, 1, "sub-two"); err != nil {
		t.Fatal(err)
	}
	secondKey := domainBoundKey(second, 1)
	if secondKey == firstKey || secondKey == counterfeit {
		t.Fatalf("the second delegation's key %q is not its own", secondKey)
	}
	if answer, err := l.service.relaunchAnswerOf(ctx, counterfeit); err != nil || answer != boundRetry {
		t.Fatalf("the counterfeit key reads %v, %v; want retry, which is what the first slice would have relaunched on", answer, err)
	}

	l.service.reconcileSubordinates(ctx)
	if attempts, err := l.records(t).DelegationAttempts(ctx, second.ID); err != nil || len(attempts) != 1 {
		t.Fatalf("after reconciliation the second delegation's attempts are %+v (%v), want still one: no answer of its own was given", attempts, err)
	}
	if hold, err := l.records(t).Hold(ctx, secondKey); err != nil || !hold.Open() {
		t.Fatalf("the second delegation's hold reads %+v (%v), want it open on its own key", hold, err)
	}
	if err := l.pass(t).Delegations["alpha"].Err; err != nil {
		t.Fatalf("the pass over the second delegation: %v", err)
	}
	if rows := domainWakesOfKind(t, l.records(t), second.ID, supervisor.KindHoldOpened); len(rows) != 1 || rows[0].Marker != "hold:"+secondKey {
		t.Fatalf("the second delegation's hold-opened wakes are %+v, want one naming its own hold", rows)
	}
	if latest, _, err := l.records(t).Delegation(ctx, "alpha"); err != nil || latest.ID != second.ID || !latest.Standing() {
		t.Fatalf("the domain's latest delegation reads %+v (%v), want the second, standing", latest, err)
	}
}

// An attempt that has recorded no session is starting, not gone: the claim
// is written before the process and the session after, so a pass landing
// between the two reads a delegation whose subordinate is coming up. Inside
// escalation_window from the attempt's StartedAt the pass surfaces nothing
// and writes no marker; past it, with still no session, the subordinate is
// read as unobservable and surfaced as gone, which is what a start that
// failed looks like. The window is read off the attempt row (P8). The
// counterfeit is the same observation with the attempt claimed longer ago
// than the window, which surfaces, so the check can tell a loop that
// stopped reading the window from one that reads it.
func TestAnAttemptWithNoSessionIsStartingInsideTheWindowAndGonePastIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s", "escalation_window": "1m", "relaunch_attempts": 0}`)
	ctx := t.Context()
	d, err := records.Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginDelegationAttempt(ctx, d.ID, 0); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	l := openLoop(t, h, records, newScriptedTerminals(), clock)
	defer func() { _ = l.service.Close() }()

	first := l.nextPass(t)
	if r := first.Delegations["alpha"]; r.Err != nil || len(r.Detections) != 0 {
		t.Fatalf("the pass over a starting subordinate reported %+v, want nothing detected", r)
	}
	if _, has, err := l.records(t).DelegationMarkers(ctx, d.ID); err != nil || has {
		t.Fatalf("the pass over a starting subordinate wrote markers (%v, %v)", has, err)
	}
	if rows, err := l.records(t).DelegationWakes(ctx, d.ID); err != nil || len(rows) != 0 {
		t.Fatalf("the pass over a starting subordinate queued %+v (%v)", rows, err)
	}

	clock.Advance(2 * time.Minute)
	past := l.pass(t)
	got := domainDetections(past, "alpha", supervisor.KindSubordinateGone)
	if len(got) != 1 || !got[0].Verdict.Surfaces() || !strings.HasPrefix(got[0].Marker, "subordinate-gone:unobservable:1:") {
		t.Fatalf("past the window the detections are %+v, want one surfaced unobservable subordinate-gone", got)
	}
	if rows := domainWakesOfKind(t, l.records(t), d.ID, supervisor.KindSubordinateGone); len(rows) != 1 || rows[0].Domain != "alpha" {
		t.Fatalf("past the window the subordinate-gone wakes are %+v, want one", rows)
	}

	// The decision alone, both ways.
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	cfg := config.Config{EscalationWindow: time.Minute}
	starting := delegationObservation{delegation: store.Delegation{ID: 1, Domain: "alpha"}, attempt: 1, started: now.Add(-10 * time.Second),
		liveness: taskstate.Unobserved, surfaced: map[string]bool{}}
	if decided := decideDelegation(starting, cfg, now); len(decided.detections) != 0 || decided.write {
		t.Fatalf("a starting attempt decided %+v, want nothing", decided)
	}
	stale := starting
	stale.started = now.Add(-2 * time.Minute)
	if decided := decideDelegation(stale, cfg, now); len(decided.detections) != 1 || !decided.detections[0].Verdict.Surfaces() {
		t.Fatalf("an attempt claimed before the window with no session decided %+v, want subordinate-gone surfaced", decided)
	}
}
