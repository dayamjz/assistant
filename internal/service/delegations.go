package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The supervisor loop's pass over the delegations: PRD section 6's
// subordinate gone, and section 11's hold for the person once a
// delegation's subordinate coordinator has been relaunched relaunch_attempts
// times, each carried by internal/store's domain wake, the queue's second
// shape, since a subordinate is no task and the wake table's row names one.
//
// # What a pass reads
//
// After the tasks, the pass reads every standing delegation, its attempts,
// and the markers internal/store keeps for it, and probes the latest
// attempt's session through the same Terminals the tasks are probed
// through. A Live probe is the subordinate at work: the markers are cleared
// and nothing surfaces. Dead and Unobserved are the subordinate gone, on
// supervise.go's terms for a task's terminal: the wake is
// supervisor.KindSubordinateGone with the case, the attempt and the
// unobserved-since instant in its marker, surfaced once per attempt and
// case and then once more per escalation_window measured from the
// delegation's GoneSurfacedAt marker, which the pass that surfaces writes in
// the wake's own transaction (P8: the spacing is read off the marker row and
// never off the newest wake). An attempt that has recorded no session is
// starting while escalation_window has not elapsed since the attempt was
// claimed, read off the attempt row's StartedAt: the claim is written
// before the process starts and the session recorded after, as a task's
// is, so a pass landing between the two would otherwise read a subordinate
// that is coming up as gone. Past that window with no session it is read
// as unobservable, since nothing can be probed for it, and a start that
// failed is then told to the coordinator the way a gone one is.
//
// # The bound's hold
//
// A delegation whose latest attempt probes Dead and whose attempts past the
// first reach relaunch_attempts is one reconciliation will not relaunch, so
// the pass registers the bound's hold for it, subordinates.go's
// holdDomainBound, keyed by that attempt, and surfaces the open hold once as
// supervisor.KindHoldOpened on the queue's second shape, so the coordinator
// is told rather than the delegation standing silently past the bound. The
// hold is answered through task.respond given the domain, which
// subordinates.go's respondDomain owns: a retry begins the subordinate once
// more and a stop ends the delegation.
//
// # Which coordinator
//
// Every domain wake reaches the first coordinator: both kinds are about a
// subordinate, and internal/store offers a subordinate's drain none, so a
// subordinate never receives a wake about itself.

// delegationObservation is what one pass found about one delegation,
// before the decision.
type delegationObservation struct {
	delegation store.Delegation
	// attempt is the latest attempt's sequence, zero for a delegation with
	// none.
	attempt int64
	// started is when the latest attempt was claimed, which precedes its
	// process, and zero for a delegation with no attempt.
	started time.Time
	// ref is the latest attempt's session reference, empty when it has
	// none.
	ref string
	// liveness is the probe's answer for ref, Unobserved when there is no
	// reference to probe.
	liveness taskstate.Liveness
	markers  store.DelegationMarkers
	has      bool
	// surfaced is every (kind, marker) already in the domain wake table
	// for the delegation, queued or acknowledged, on observation.surfaced's
	// terms.
	surfaced map[string]bool
	// relaunches is the attempts past the first.
	relaunches int
	// hold is the bound's hold for the latest attempt as it stands, and
	// nil when none is registered.
	hold *store.Hold
}

// delegationDecision is what decideDelegation composed for one delegation.
type delegationDecision struct {
	detections []detection
	advance    store.DelegationMarkerAdvance
	// write is whether the markers are written when nothing surfaced.
	write bool
}

// decideDelegation composes internal/supervisor's inputs for one delegation
// from what the pass observed and classifies each detection, on decide's
// terms for a task: pure, with now the pass's clock and cfg the escalation
// window. It is decideDelegationWith over supervisor.Classify; the test
// runs the same composition over a counterfeit classifier to show its
// checks can fail.
func decideDelegation(o delegationObservation, cfg config.Config, now time.Time) delegationDecision {
	return decideDelegationWith(o, cfg, now, supervisor.Classify)
}

// decideDelegationWith is decideDelegation with the classifier named.
func decideDelegationWith(o delegationObservation, cfg config.Config, now time.Time, classify classifier) delegationDecision {
	d := delegationDecision{advance: store.DelegationMarkerAdvance{UnobservedSince: o.markers.UnobservedSince, GoneSurfacedAt: o.markers.GoneSurfacedAt}}
	detect := func(wake supervisor.Wake, marker string, window supervisor.WindowState, surfaced bool) supervisor.Verdict {
		v := classify(wake, supervisor.NoEvidence, window, surfaced, supervisor.Stall{})
		d.detections = append(d.detections, detection{Kind: wake.Kind(), Marker: marker, Verdict: v})
		return v
	}
	seen := func(kind supervisor.Kind, marker string) bool { return o.surfaced[surfacedKey(kind, marker)] }
	if o.hold != nil && o.hold.Open() {
		marker := "hold:" + o.hold.Key
		if !seen(supervisor.KindHoldOpened, marker) {
			wake, _ := supervisor.Of(supervisor.KindHoldOpened)
			detect(wake, marker, supervisor.WindowOpen, false)
		}
	}
	if o.liveness == taskstate.Live {
		d.advance.UnobservedSince = store.Optional[time.Time]{}
		d.advance.GoneSurfacedAt = store.Optional[time.Time]{}
		d.write = o.has && (o.markers.UnobservedSince.IsKnown() || o.markers.GoneSurfacedAt.IsKnown())
		return d
	}
	if o.ref == "" && o.attempt > 0 && elapsed(o.started, cfg.EscalationWindow, now) == supervisor.WindowOpen {
		// Starting, on the file comment's terms: nothing surfaces and the
		// markers are left as they are, so a start that comes up clears
		// them as Live and one that does not is read past the window.
		return d
	}
	g := goneDead
	if o.liveness != taskstate.Dead {
		g = goneUnobservable
	}
	since, ok := o.markers.UnobservedSince.Get()
	if !ok {
		since = now
	}
	d.advance.UnobservedSince = store.Known(since)
	d.write = true
	marker := subordinateGoneMarker(g, o.attempt, since)
	surfaced := seen(supervisor.KindSubordinateGone, marker)
	window := supervisor.WindowOpen
	if surfaced {
		last, ok := o.markers.GoneSurfacedAt.Get()
		if !ok {
			last = now
			d.advance.GoneSurfacedAt = store.Known(now)
		}
		if window = elapsed(last, cfg.EscalationWindow, now); window == supervisor.WindowElapsed {
			marker = subordinateGoneAgainMarker(g, o.attempt, since, now)
		}
	}
	wake, _ := supervisor.Of(supervisor.KindSubordinateGone)
	if v := detect(wake, marker, window, surfaced); v.Surfaces() {
		d.advance.GoneSurfacedAt = store.Known(now)
	}
	return d
}

// classifier is supervisor.Classify's shape, so decideDelegationWith can
// take a counterfeit.
type classifier func(supervisor.Wake, supervisor.Evidence, supervisor.WindowState, bool, supervisor.Stall) supervisor.Verdict

// subordinateGoneMarker composes a subordinate-gone wake's marker on
// terminalGoneMarker's terms: the case, the attempt whose session was found
// gone, and the unobserved-since instant. It, subordinateGoneAgainMarker and
// subordinateGoneAbout are the composers and the one reader.
func subordinateGoneMarker(g gone, attempt int64, since time.Time) string {
	return fmt.Sprintf("subordinate-gone:%s:%d:%s", g, attempt, since.UTC().Format(time.RFC3339Nano))
}

// subordinateGoneAgainMarker is terminalGoneAgainMarker's composition for a
// subordinate gone.
func subordinateGoneAgainMarker(g gone, attempt int64, since, at time.Time) string {
	return subordinateGoneMarker(g, attempt, since) + ":again:" + at.UTC().Format(time.RFC3339Nano)
}

// subordinateGoneAbout reports whether a subordinate-gone marker this loop
// composed names the attempt, on terminalGoneAbout's terms.
func subordinateGoneAbout(marker string, attempt int64) bool {
	rest, ok := strings.CutPrefix(marker, "subordinate-gone:")
	if !ok {
		return false
	}
	for _, g := range []gone{goneDead, goneUnobservable} {
		if strings.HasPrefix(rest, string(g)+":"+strconv.FormatInt(attempt, 10)+":") {
			return true
		}
	}
	return false
}

// superviseDelegations is the pass over every standing delegation, on the
// file comment's terms, reporting per domain on taskReport's terms.
func (s *Service) superviseDelegations(c *incarnation, now time.Time) map[string]taskReport {
	ctx, cancel := s.sup.bound(c.ctx)
	standing, err := s.store.StandingDelegations(ctx)
	cancel()
	if err != nil {
		s.log.Printf("supervisor incarnation %d could not read the standing delegations: %v", c.inc.Epoch(), err)
		return nil
	}
	if len(standing) == 0 {
		return nil
	}
	out := make(map[string]taskReport, len(standing))
	for _, d := range standing {
		out[d.Domain] = s.superviseDelegation(c, d, now)
	}
	return out
}

// superviseDelegation is the pass over one delegation: the reads, the
// probe, the bound's hold, the decision and the writes.
func (s *Service) superviseDelegation(c *incarnation, d store.Delegation, now time.Time) taskReport {
	o := delegationObservation{delegation: d}
	ctx, cancel := s.sup.bound(c.ctx)
	attempts, err := s.store.DelegationAttempts(ctx, d.ID)
	cancel()
	if err != nil {
		return taskReport{Watched: true, Err: err}
	}
	if n := len(attempts); n > 0 {
		o.attempt = attempts[n-1].Sequence
		o.started = attempts[n-1].StartedAt
		o.ref, _ = attempts[n-1].SessionRef.Get()
		o.relaunches = n - 1
	}
	o.liveness = taskstate.Unobserved
	if o.ref != "" {
		ctx, cancel = s.sup.bound(c.ctx)
		o.liveness = s.sup.terminals.Probe(ctx, o.ref).Liveness
		cancel()
	}
	ctx, cancel = s.sup.bound(c.ctx)
	o.markers, o.has, err = s.store.DelegationMarkers(ctx, d.ID)
	cancel()
	if err != nil {
		return taskReport{Watched: true, Err: err}
	}
	ctx, cancel = s.sup.bound(c.ctx)
	wakes, err := s.store.DelegationWakes(ctx, d.ID)
	cancel()
	if err != nil {
		return taskReport{Watched: true, Err: err}
	}
	o.surfaced = make(map[string]bool, len(wakes))
	for _, w := range wakes {
		o.surfaced[surfacedKey(supervisor.Kind(w.Kind), w.Marker)] = true
	}
	// The bound's hold: registered here once the latest attempt is dead
	// and past the bound, so a delegation never stands silently past it,
	// and read as it stands whether this pass or reconciliation registered
	// it.
	if o.liveness == taskstate.Dead && o.relaunches >= s.cfg.RelaunchAttempts {
		past := fmt.Sprintf("its subordinate coordinator's terminal %s is gone, it has been relaunched %d times and relaunch_attempts is %d",
			o.ref, o.relaunches, s.cfg.RelaunchAttempts)
		ctx, cancel = s.sup.bound(c.ctx)
		h, err := s.holdDomainBound(ctx, d, o.attempt, past)
		cancel()
		if errors.Is(err, store.ErrDelegationEnded) {
			// domain.end landed after this pass read the delegation
			// standing: nothing is registered, and the ended delegation is
			// no longer this loop's to surface.
			return taskReport{Watched: true}
		}
		if err != nil {
			return taskReport{Watched: true, Err: err}
		}
		o.hold = &h
	}
	decided := decideDelegation(o, s.cfg, now)
	for i := range decided.detections {
		decided.detections[i].Marker = s.sup.redactor.Redact(decided.detections[i].Marker)
	}
	return s.recordDelegation(c, d, decided)
}

// recordDelegation makes the pass's writes for one delegation, on record's
// terms: each surfaced detection with the markers in one transaction, the
// markers alone when nothing surfaced and the decision says to write, and
// a row appended rings the bell and is pushed as a domain wake.
func (s *Service) recordDelegation(c *incarnation, d store.Delegation, decided delegationDecision) taskReport {
	out := taskReport{Watched: true, Detections: decided.detections}
	surfaced := 0
	for i, det := range decided.detections {
		if !det.Verdict.Surfaces() {
			continue
		}
		surfaced++
		ctx, cancel := s.sup.bound(c.ctx)
		appended, err := s.store.AppendDomainWakeWithMarkers(ctx, c.inc,
			store.DomainWakeRequest{Delegation: d.ID, Kind: string(det.Kind), Marker: det.Marker}, decided.advance)
		cancel()
		if err != nil {
			out.Err = err
			return out
		}
		out.Detections[i].Disposition = appended.Disposition
		if appended.Disposition == store.WakeAppended {
			s.wakeBell.ring()
			s.publishDomainWake(appended.Wake)
		}
	}
	if surfaced == 0 && decided.write {
		ctx, cancel := s.sup.bound(c.ctx)
		_, err := s.store.AdvanceDelegationMarkers(ctx, c.inc, d.ID, decided.advance)
		cancel()
		out.Err = err
	}
	return out
}
