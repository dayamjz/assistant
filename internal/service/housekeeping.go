package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Housekeeping: PRD section 6's housekeeping as the program's part of it.
// The coordinator creates every pass (internal/tasks' intake of a marked
// create); this file is what the program decides about the passes the
// coordinator creates, and it creates none.
//
// # When a pass is due
//
// Each supervision pass, for each repository the store records, with
// housekeeping.interval not zero, the newest pass of the repository is
// found among the tasks the pass already read. While it is open, no due
// check runs: no second pass is created while one is open. Otherwise the
// due check runs when the interval has passed since the newest pass was
// created, or when there was never one, and when the repository's due-check
// marker does not say this check already ran within an interval for that
// same pass. The check runs off the loop's goroutine, one per repository at
// a time, because it fetches the default branch from the upstream, which
// may take longer than the bound every blocking call of a pass runs under;
// its writes are fenced by the incarnation that started it all the same. It
// fetches the default branch once, under housekeepingReadNamespace, and
// reads both the tip and the trusted housekeeping.paths at it:
//
//   - a fetch, a trusted copy or a record that cannot be read raises the
//     housekeeping-due wake naming what could not be read, among them a last
//     pass never dispatched, whose copy records no base, so missing
//     information is never read as nothing having changed;
//   - an empty trusted list raises nothing: housekeeping is off for the
//     repository;
//   - a tip other than the last pass's base, or no pass ever, raises the
//     wake;
//   - a tip that is the base raises nothing.
//
// The base is tasks.PassBase's, the default-branch commit the pass's copy
// was cut at, which is what its worker read, and not the knowledge revision
// its launch recorded at a fresh fetch of the upstream: a primary checkout
// behind the upstream cuts the copy at the older commit, and a pass that
// found nothing there has not checked what the newer one changed.
//
// The marker is advanced in the wake's own transaction, or alone when
// nothing was raised, so the check runs again only after another interval in
// which no pass was created. The interval therefore spaces the fetches too,
// one per repository per interval, which is what housekeeping.interval's
// floor is chosen against; the cost is that a default branch that moves just
// after a check that found it unmoved is seen up to an interval later. The
// check's facts, which pass it follows and that pass's base, are what the
// loop read before the fetch, and a pass created while the fetch was in
// flight makes them stale: a wake raised from them would say no pass was
// ever created, or name a base the new pass replaced, and acting on it
// would create a redundant pass. So the wake and the marker are written
// only while the pass the check followed is still the repository's newest
// and no pass is open, read again in the writing transaction
// (store.DueCheck, store.ErrDueCheckStale); a check that finds its reading
// moved writes nothing, and the next supervision pass reads the new pass.
//
// # An open pass
//
// Once the newest pass has been open for a whole interval, the wake names it
// and what it waits on, keyed by the interval it reached, so it is raised
// once per interval while the pass stays open, whether or not the default
// branch moved, and whether or not something moves it: that is the long-run
// escalation the coordinator relays. When nothing moves it
// (supervisor.PassMoves), it is also offered the retirement hold, unless a
// dispatch or relaunch holds its slot unsettled, which is a launch under
// way (dispatch.go's launching), keyed by the interval, the latest
// attempt and the newest run it was decided against, so at most one is
// registered per interval, none while an earlier one is unanswered, and a
// retire answer given after a run started or the worker was begun again is
// refused by store.RetireHousekeepingPass's anchors. The registration reads the pass again in its own transaction
// (store.OfferRetirementHold), so a pass completed, retired, begun again or
// given a run since the loop read it is offered none. A pass something
// moves is never offered one, and is in flight
// whatever it resolves (supervise.go's watched set, the stop guard through
// feed, the catch-up read), so its terminal is read on every pass. With
// housekeeping.interval zero, no wake and no hold is ever raised about a
// pass, open or not.
//
// # A run that rebases to nothing
//
// No end here. PRD section 6's nothing left to deliver lands a pass whose
// run's rebase leaves nothing to deliver as it lands any delivery task,
// from the nothing-to-deliver record the run's end writes (runs.go,
// store.TransitionRunOnNothingToDeliver), and a landed pass is in the
// terminal tier, so it is no longer open under the one-at-a-time rule
// (taskstate.Delivered reads that record) and is offered no retirement
// hold. A run that ended on the short circuit without that record, its
// branch already published or its work not proven present, lands nothing,
// and its pass is where its records put it: it resolves Ready, its worker
// runs on, the wake names it each interval while something moves it, and
// once nothing does it is offered the retirement hold like any other end
// that does not land. Nothing here reads a run's position, and the program
// retires no pass on its own.
//
// # The end, by an answer
//
// answerRetirement answers the retirement hold: keep closes it and leaves
// the pass open, and for a hold offered before the pass's first attempt,
// which refused the dispatch that read it (store.ErrPassHeld), runs the
// dispatch (dispatchInBackground), since dispatch runs at open and on intake
// and nothing else would begin the pass; retire, under the task's slot, refuses a pass whose worker
// the probe does not read dead and otherwise asks
// store.RetireHousekeepingPass, which answers stop on every open hold of the
// pass whose retry would relaunch its worker (isRetryHold) under the same
// resolver; any other answer is refused with the hold open. Standing consent
// answers no task hold, so it never answers this one.
//
// After a retirement the pass's copy is asked the cleanup decision, so
// a copy holding work that has not landed becomes a kept work entry (P15)
// and nothing is removed without a proof; a copy refused for a report never
// stored or a decision never registered is kept the same way
// (tasks.KeptOnRetirement), since nothing will store that report or
// register those holds now. The retirement record is written before that
// ask and in another transaction, so the ask is not the retirement's alone:
// landed.go's pass asks it again, on its cadence, for every retired pass
// whose copy carries no removal (retiredCleanupOwed), so a service that
// stopped between the two asks it once it opens again and reads the worker
// dead, and a refusal that changes with the world is asked about again. The
// terminal it is asked with is retiredTerminal's. The store refuses a
// retirement hold for a pass already retired. A retired pass is never
// begun again: every attempt claim refuses it in the store
// (store.ErrPassRetired), and relaunch.go refuses it before it sweeps
// anything, and the record of an attempt's session refuses it, so no
// session is recorded for an attempt of a retired pass after its
// retirement. Nor is it in flight by its state alone
// (supervisor.RegardOf): the loop watches it, the stop guard counts it and
// the catch-up read lists it under way only while a run of its branch has
// not ended or a wake about it is queued, so once its wakes are
// acknowledged a pass retired with its worker dead or never launched has
// its terminal read no more and raises no further terminal-gone wake.
//
// # Residual gaps
//
//   - The due check runs off the loop's goroutine, so a pass report does not
//     carry it, and a check that is slower than the interval is not begun a
//     second time until it returns.
//   - A launch records its attempt's session only once the worker's
//     process has started, and until then the attempt reads as never
//     launched, which section 6's "or its latest attempt never had a
//     session" counts as nothing moving the pass. Within this service the
//     dispatch or relaunch holds the task's slot unsettled from its claim
//     until the launch has settled its record (dispatch.go, launch.go), a
//     worker fallback at the launch included (workerfallback.go), the slot
//     every retirement is written under and the retirement hold's offer
//     reads, so a retire answered meanwhile is refused with the hold open
//     (errSteerInFlight), the offer is skipped until the next pass, and a
//     launch that reads a pass already retired under the slot creates no
//     worker (errLaunchRetired); the pass stays open and resolves
//     Dispatched, in flight, until its worker is recorded. For a retirement the slot does not order, written by
//     another incarnation or past a crash, the store decides the two writes
//     with exactly one loser: a retirement anchored to no session refuses
//     once the session is recorded (store.ErrPassMoves), and the record
//     refuses once the pass is retired (store.ErrPassRetired), on which
//     launch.go ends the worker it started and records nothing. What such
//     a worker did between its start and its end stays in the pass's copy,
//     which the cleanup decision is asked about as for any retired pass
//     (P6), and between its start and its end it is a worker no record
//     names, the gap dispatch.go's crash paragraph states.
//   - The program cannot tell a rule from a fact; what a pass may change
//     rests on the worker's instructions and the run's review, and the
//     review's ask on paths outside housekeeping.paths (internal/stages).
//
// housekeepingState is what this service remembers between passes about
// housekeeping, none of it a fact the records do not hold: which
// repositories' due checks are running now. Losing it costs a repeated
// check and nothing else.
type housekeepingState struct {
	mu sync.Mutex
	// checking holds the repositories whose due check is running.
	checking map[string]bool
}

// housekeepingScope is internal/tasks' HousekeepingScope: the repository's
// housekeeping.paths read from its trusted copy at a fresh fetch of the
// default branch, on projectPosture's terms, so a pushed copy has no say.
func (s *Service) housekeepingScope(ctx context.Context, repository store.Repository, taskID string) (config.PatternSet, error) {
	trusted, err := s.trustedTaskLayer(ctx, repository, housekeepingReadNamespace, taskID, "housekeeping pass "+taskID)
	if err != nil {
		return nil, err
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return nil, fmt.Errorf("%w: resolving the configuration of housekeeping pass %s: %w", resolutionRefusal(err), taskID, err)
	}
	return resolution.Config.HousekeepingPaths, nil
}

// passOpen is store.PassOpen with internal/taskstate's terminal tier.
func passOpen(o taskstate.Observed) bool { return store.PassOpen(o.Snapshot, taskstate.Delivered) }

// passesByRepository is every pass the loop read, by repository, newest
// first, which is the order the reader returns tasks in.
func passesByRepository(observed []taskstate.Observed) map[string][]taskstate.Observed {
	out := make(map[string][]taskstate.Observed)
	for _, o := range observed {
		if repository, ok := o.Snapshot.Task.RepositoryID.Get(); ok && o.Snapshot.Task.Housekeeping {
			out[repository] = append(out[repository], o)
		}
	}
	return out
}

// superviseHousekeeping is the pass's part over housekeeping, on the file
// comment's terms, keyed by repository: a repository whose open pass was
// read, or whose due check this pass started, is reported Watched, with the
// detections it wrote. It returns nil when there was neither.
func (s *Service) superviseHousekeeping(c *incarnation, observed []taskstate.Observed, now time.Time) map[string]taskReport {
	byRepository := passesByRepository(observed)
	interval := s.cfg.HousekeepingInterval
	if interval <= 0 {
		return nil
	}
	ctx, cancel := s.sup.bound(c.ctx)
	repositories, err := s.store.Repositories(ctx)
	cancel()
	if err != nil {
		return map[string]taskReport{"": {Err: err}}
	}
	var out map[string]taskReport
	report := func(id string, tr taskReport) {
		if !tr.Watched && len(tr.Detections) == 0 && tr.Err == nil {
			return
		}
		if out == nil {
			out = make(map[string]taskReport)
		}
		out[id] = tr
	}
	for _, r := range repositories {
		passes := byRepository[r.ID]
		if len(passes) > 0 && passOpen(passes[0]) {
			report(r.ID, s.superviseOpenPass(c, r, passes[0], interval, now))
			continue
		}
		var last *taskstate.Observed
		if len(passes) > 0 {
			last = &passes[0]
		}
		report(r.ID, s.dueCheckFromPass(c, r, last, interval, now))
	}
	return out
}

// intervalsOpen is how many whole intervals a pass created at created has
// been open by now.
func intervalsOpen(created, now time.Time, interval time.Duration) int64 {
	if now.Before(created) {
		return 0
	}
	return int64(now.Sub(created) / interval)
}

// superviseOpenPass is an open pass's part: the wake naming it once per
// interval it stays open, and the retirement hold when nothing moves it.
func (s *Service) superviseOpenPass(c *incarnation, r store.Repository, o taskstate.Observed, interval time.Duration, now time.Time) taskReport {
	epoch := intervalsOpen(o.Snapshot.Task.CreatedAt, now, interval)
	if epoch < 1 {
		return taskReport{Watched: true}
	}
	id := o.Snapshot.Task.ID
	moves := supervisor.PassMoves(o)
	detail := fmt.Sprintf("housekeeping pass %s of repository %s has been open since %s, %d interval(s) of %s, so no later pass is created; it waits on %s",
		id, r.ID, o.Snapshot.Task.CreatedAt.UTC().Format(time.RFC3339), epoch, interval, passWaitsOn(o, moves))
	out := s.raiseHousekeeping(c, store.HousekeepingWakeRequest{Repository: r.ID, Pass: id,
		Marker: fmt.Sprintf("open:%s:%d", id, epoch), Detail: detail}, nil)
	if moves {
		return out
	}
	for _, h := range o.Snapshot.Holds {
		rh, ok := parseRetireHold(h.Key, id)
		if ok && (h.Open() || rh.Epoch == epoch) {
			return out
		}
	}
	rh := retireHold{Task: id, Epoch: epoch, Attempt: latestSequence(o.Snapshot.Attempts)}
	if len(o.Snapshot.Runs) > 0 {
		rh.Run = o.Snapshot.Runs[0].ID
	}
	if s.sup.hooks.beforeRetirementOffer != nil {
		s.sup.hooks.beforeRetirementOffer(id)
	}
	// A launch under way holds the task's slot unsettled from its claim
	// until its record is settled, and reads as nothing moving the pass
	// until then, so the offer waits for the next pass rather than
	// registering a hold that would take a launching pass out of flight.
	// It reads the slot rather than taking it, so a stop or the cleanup
	// ask holding the slot, which exclude nothing here, do not put the
	// offer off. The read decides nothing by itself: the store orders the
	// offer against every attempt claim, refusing the offer once an
	// attempt was claimed (its anchor) and refusing a claim once the hold
	// is open (store.ErrPassHeld), so a dispatcher that read the pass
	// before this offer and claims after it begins nothing and gives its
	// slot back. The read only spares a launch in flight that refusal.
	if s.launching(id) {
		return out
	}
	ctx, cancel := s.sup.bound(c.ctx)
	h, err := s.store.OfferRetirementHold(ctx, c.inc, store.RetirementOffer{
		Hold: store.Hold{Key: rh.key(), TaskID: store.Known(id),
			Subject: "whether to retire a housekeeping pass that has been open for a whole interval and that nothing moves",
			Detail:  retireDetail(r, o, interval)},
		Attempt: rh.Attempt, Session: latestRef(o.Snapshot.Attempts), Run: rh.Run, Ended: runs.Finished, Delivered: taskstate.Delivered,
	})
	cancel()
	var moved *store.TaskMovedError
	switch {
	case errors.As(err, &moved), errors.Is(err, store.ErrPassMoves), errors.Is(err, store.ErrPassRetired), errors.Is(err, store.ErrPassClosed):
		// Begun again or given a run since the pass read it, so it moves,
		// or retired or completed since, so its claim is released: either
		// way it is owed none.
	case err != nil:
		out.Err = errors.Join(out.Err, err)
	default:
		s.publishHoldState(h)
		s.log.Printf("housekeeping pass %s of repository %s is offered retirement on hold %s", id, r.ID, h.Key)
	}
	return out
}

// latestSequence is the sequence of the latest of attempts, zero for none.
func latestSequence(attempts []store.TaskAttempt) int64 {
	if n := len(attempts); n > 0 {
		return attempts[n-1].Sequence
	}
	return 0
}

// passWaitsOn says what an open pass waits on, for the wake and the hold:
// what moves it, when something does, and otherwise what its resolution
// says.
func passWaitsOn(o taskstate.Observed, moves bool) string {
	if moves {
		var what []string
		for _, run := range o.Snapshot.Runs {
			if !taskstate.RunSettled(run.Status) {
				what = append(what, fmt.Sprintf("run %s, which is %s", run.ID, run.Status))
			}
		}
		if ref := latestRef(o.Snapshot.Attempts); ref != "" && o.State.Basis.Process != taskstate.Dead {
			reading := "reads live"
			if o.State.Basis.Process != taskstate.Live {
				reading = "cannot be read"
			}
			what = append(what, fmt.Sprintf("its worker, whose terminal (session %s) %s", ref, reading))
		}
		return "what still moves it: " + strings.Join(what, ", and ")
	}
	said := fmt.Sprintf("nothing moves it, and it resolves %s", o.State.State)
	if detail, ok := o.State.Detail.Get(); ok {
		said += ": " + detail
	}
	if len(o.Snapshot.Runs) > 0 {
		if pr, ok := o.Snapshot.Runs[0].PullRequest.Get(); ok {
			said += fmt.Sprintf("; its run %s opened pull request %s", o.Snapshot.Runs[0].ID, pr)
		}
	}
	return said
}

// retireDetail is what the retirement hold says a person needs: what the
// pass waits on and what retiring it leaves behind.
func retireDetail(r store.Repository, o taskstate.Observed, interval time.Duration) string {
	left := "Retiring it closes no pull request, moves no branch and discards nothing"
	if len(o.Snapshot.Runs) > 0 {
		if pr, ok := o.Snapshot.Runs[0].PullRequest.Get(); ok {
			left += fmt.Sprintf(": pull request %s stays open", pr)
		}
	}
	return fmt.Sprintf("housekeeping pass %s of repository %s was created at %s and %s. Retiring it releases its claim on the repository, "+
		"so the next pass can be created, and nothing begins its work again. %s. %s Answer %q to retire it, or %q to leave it open, "+
		"which offers this again after another interval of %s.",
		o.Snapshot.Task.ID, r.ID, o.Snapshot.Task.CreatedAt.UTC().Format(time.RFC3339), passWaitsOn(o, false), left,
		retireCopy(o), machine.AnswerRetire, machine.AnswerKeep, interval)
}

// retireCopy is retireDetail's sentence about the pass's copy: a dispatch
// cuts one, so a pass never dispatched has none to decide on.
func retireCopy(o taskstate.Observed) string {
	if len(o.Snapshot.Attempts) == 0 {
		return "It was never dispatched, so it has no copy to decide on."
	}
	return "Its copy is then asked the cleanup decision, which keeps anything not yet landed for you to decide on."
}

// raiseHousekeeping appends one housekeeping-due detection, classified on
// the kind table's terms, under the incarnation's fence, with check
// advanced in the same transaction when it is not nil, and reports the
// write. A marker already raised and acknowledged is nothing owed.
func (s *Service) raiseHousekeeping(c *incarnation, w store.HousekeepingWakeRequest, check *store.DueCheck) taskReport {
	wake, _ := supervisor.Of(supervisor.KindHousekeepingDue)
	det := detection{
		Kind:    supervisor.KindHousekeepingDue,
		Marker:  s.sup.redactor.Redact(w.Marker),
		Verdict: supervisor.Classify(wake, supervisor.NoEvidence, supervisor.WindowOpen, false, supervisor.Stall{}),
	}
	out := taskReport{Watched: true, Detections: []detection{det}}
	if !det.Verdict.Surfaces() {
		return out
	}
	w.Kind, w.Marker = string(det.Kind), det.Marker
	ctx, cancel := s.sup.bound(c.ctx)
	appended, err := s.store.AppendHousekeepingWake(ctx, c.inc, w, check)
	cancel()
	switch {
	case errors.Is(err, store.ErrWakeHandled):
		return taskReport{Watched: true}
	case errors.Is(err, store.ErrDueCheckStale):
		s.log.Printf("the housekeeping due check of repository %s wrote nothing: %v", w.Repository, err)
		return taskReport{Watched: true}
	case err != nil:
		out.Err = err
		return out
	}
	out.Detections[0].Disposition = appended.Disposition
	if appended.Disposition == store.WakeAppended {
		s.wakeBell.ring()
		s.publishHousekeepingWake(appended.Wake)
	}
	return out
}

// dueCheckFromPass decides whether the repository's due check runs now, on
// the file comment's terms, and starts it off the loop's goroutine when it
// does. last is the newest pass, closed, or nil when there was never one.
func (s *Service) dueCheckFromPass(c *incarnation, r store.Repository, last *taskstate.Observed, interval time.Duration, now time.Time) taskReport {
	follows := ""
	if last != nil {
		follows = last.Snapshot.Task.ID
		if intervalsOpen(last.Snapshot.Task.CreatedAt, now, interval) < 1 {
			return taskReport{}
		}
	}
	ctx, cancel := s.sup.bound(c.ctx)
	marker, found, err := s.store.HousekeepingMarkerOf(ctx, r.ID)
	cancel()
	if err != nil {
		return taskReport{Err: err}
	}
	if found && marker.Pass == follows && now.Sub(marker.CheckedAt) < interval {
		return taskReport{}
	}
	s.housekeeping.mu.Lock()
	if s.housekeeping.checking[r.ID] {
		s.housekeeping.mu.Unlock()
		return taskReport{}
	}
	if s.housekeeping.checking == nil {
		s.housekeeping.checking = make(map[string]bool)
	}
	s.housekeeping.checking[r.ID] = true
	s.housekeeping.mu.Unlock()
	if !s.startWork() {
		s.housekeeping.mu.Lock()
		delete(s.housekeeping.checking, r.ID)
		s.housekeeping.mu.Unlock()
		return taskReport{}
	}
	var base store.Optional[string]
	if last != nil {
		base = tasks.PassBase(last.Snapshot.Task)
	}
	var lastCreated time.Time
	if last != nil {
		lastCreated = last.Snapshot.Task.CreatedAt
	}
	go func() {
		defer s.work.Done()
		defer func() {
			s.housekeeping.mu.Lock()
			delete(s.housekeeping.checking, r.ID)
			s.housekeeping.mu.Unlock()
		}()
		tr := s.dueCheck(c, r, follows, lastCreated, base, now)
		if tr.Err != nil {
			s.log.Printf("the housekeeping due check of repository %s could not be recorded: %v", r.ID, tr.Err)
		}
		if s.sup.hooks.afterHousekeepingCheck != nil {
			s.sup.hooks.afterHousekeepingCheck(r.ID, tr)
		}
	}()
	return taskReport{Watched: true}
}

// dueCheck is one repository's due check, on the file comment's terms: one
// fetch of the default branch, the trusted scope and the tip read there, and
// the wake or the marker written under the incarnation that started it,
// held to the passes as the loop read them.
func (s *Service) dueCheck(c *incarnation, r store.Repository, follows string, lastCreated time.Time, base store.Optional[string], now time.Time) taskReport {
	checked := &store.DueCheck{Check: store.HousekeepingCheck{Repository: r.ID, Pass: follows, CheckedAt: now}, Delivered: taskstate.Delivered}
	which := "none"
	if follows != "" {
		which = follows
	}
	marker := fmt.Sprintf("due:%s:%d", which, now.Unix())
	raise := func(detail string) taskReport {
		return s.raiseHousekeeping(c, store.HousekeepingWakeRequest{Repository: r.ID, Pass: follows, Marker: marker, Detail: detail}, checked)
	}
	forWhat := "the housekeeping due check of repository " + r.ID
	ctx := c.ctx
	bare, err := s.gateRepositoryOf(ctx, r, forWhat)
	if err != nil {
		return raise(fmt.Sprintf("the due check of repository %s could not read its default branch: %v", r.ID, err))
	}
	ref := readRef(housekeepingReadNamespace, "due")
	defer func() { _ = bare.DeleteRef(context.WithoutCancel(ctx), ref) }()
	trusted, tip, _, err := s.trustedLayerAt(ctx, bare, r, ref, forWhat)
	if err != nil {
		return raise(fmt.Sprintf("the due check of repository %s could not read its default branch or its trusted configuration: %v", r.ID, err))
	}
	if s.sup.hooks.afterDueRead != nil {
		s.sup.hooks.afterDueRead(r.ID)
	}
	resolution, err := config.ResolveRun(s.global, trusted, config.Absent(config.OriginPushed))
	if err != nil {
		return raise(fmt.Sprintf("the due check of repository %s could not resolve its trusted configuration at %s: %v", r.ID, tip, err))
	}
	if len(resolution.Config.HousekeepingPaths) == 0 {
		return s.advanceHousekeeping(c, *checked)
	}
	if follows == "" {
		return raise(fmt.Sprintf("no housekeeping pass of repository %s was ever created, and its default branch %s stands at %s",
			r.ID, r.DefaultBranch, tip))
	}
	b, ok := base.Get()
	if !ok {
		return raise(fmt.Sprintf("the base of housekeeping pass %s of repository %s, the default-branch commit its copy was cut at, "+
			"could not be read: it records none, as a pass never dispatched does, so whether the default branch %s, now at %s, moved since is not known",
			follows, r.ID, r.DefaultBranch, tip))
	}
	if b == tip {
		return s.advanceHousekeeping(c, *checked)
	}
	return raise(fmt.Sprintf("the default branch %s of repository %s stands at %s, past %s, the base of housekeeping pass %s created at %s",
		r.DefaultBranch, r.ID, tip, b, follows, lastCreated.UTC().Format(time.RFC3339)))
}

// advanceHousekeeping writes a due check's marker with no wake, or nothing
// once the passes moved since the check read them, on the file comment's
// terms.
func (s *Service) advanceHousekeeping(c *incarnation, check store.DueCheck) taskReport {
	ctx, cancel := s.sup.bound(c.ctx)
	defer cancel()
	err := s.store.AdvanceHousekeepingMarker(ctx, c.inc, check)
	if errors.Is(err, store.ErrDueCheckStale) {
		s.log.Printf("the housekeeping due check of repository %s wrote nothing: %v", check.Check.Repository, err)
		return taskReport{Watched: true}
	}
	return taskReport{Watched: true, Err: err}
}

// retireHoldSpace is the key space of a pass's retirement holds under the
// task.
const retireHoldSpace = "/housekeeping-retire/"

// retireHold is a retirement hold's key: the pass, the interval it was
// offered in, and the latest attempt and newest run it was decided against,
// which RetireHousekeepingPass anchors a retire answer to.
type retireHold struct {
	Task    string
	Epoch   int64
	Attempt int64
	Run     string
}

func (h retireHold) key() string {
	run := h.Run
	if run == "" {
		run = "-"
	}
	return fmt.Sprintf("task/%s%s%d/%d/%s", h.Task, retireHoldSpace, h.Epoch, h.Attempt, run)
}

// parseRetireHold reads a retirement hold's key of the task, and false for
// any other key.
func parseRetireHold(key, task string) (retireHold, bool) {
	rest, ok := strings.CutPrefix(key, "task/"+task+retireHoldSpace)
	if !ok {
		return retireHold{}, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return retireHold{}, false
	}
	epoch, err1 := strconv.ParseInt(parts[0], 10, 64)
	attempt, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || parts[2] == "" {
		return retireHold{}, false
	}
	h := retireHold{Task: task, Epoch: epoch, Attempt: attempt, Run: parts[2]}
	if h.Run == "-" {
		h.Run = ""
	}
	return h, true
}

// passRelaunches is the Relaunches a retirement of task is decided with:
// the holds whose retry would begin its worker again.
func passRelaunches(task string) func(string) bool {
	return func(key string) bool { return isRetryHold(key, task) }
}

// retirementOf is the store request for a retirement of task, anchored to
// its latest attempt and that attempt's session as the caller read them in
// attempts, and to run, with the owners' predicates.
func retirementOf(task string, attempts []store.TaskAttempt, run string) store.Retirement {
	return store.Retirement{Task: task, Attempt: latestSequence(attempts), Session: latestRef(attempts), Run: run,
		Relaunches: passRelaunches(task), Ended: runs.Finished, Delivered: taskstate.Delivered}
}

// retiredCleanupOwed reports whether o is a retired housekeeping pass whose
// copy the cleanup decision is still owed for, on the file comment's terms:
// retired, not in the terminal tier, whose own rule a pass that landed after
// all is held to instead, its copy not recorded removed, and dispatched at
// least once, since a dispatch cuts the copy after its claim, so a pass
// never dispatched has none. landed.go asks it, at the retirement and again
// from the pass, on that file's cadence.
func retiredCleanupOwed(o taskstate.Observed) bool {
	snap := o.Snapshot
	return snap.Task.Housekeeping && snap.Retirement.IsKnown() && !landedEnds(o.State) &&
		!o.State.Basis.CleanupRemoved && len(snap.Attempts) > 0
}

// retiredTerminal is the terminal a retired pass's cleanup is asked with:
// the liveness its resolution read when its latest attempt has a session,
// and Dead when it never had one. The second is PRD section 6's other half
// of nothing moving a pass, "or its latest attempt never had a session",
// the reading its retirement was decided on, and it stays that way: no
// attempt begins after the retirement and no session is recorded for one
// claimed before it, because every attempt claim and the session's record
// refuse a retired pass (store.ErrPassRetired).
func retiredTerminal(o taskstate.Observed) taskstate.Liveness {
	if latestRef(o.Snapshot.Attempts) == "" {
		return taskstate.Dead
	}
	return o.State.Basis.Process
}

// logRetiredCleanup is logCleanup for a retired pass's copy: a copy
// tasks.KeptOnRetirement keeps for a report or hold its retirement means will
// never come becomes a kept work entry (P15), as a copy kept for its work
// does through logCleanup.
func (s *Service) logRetiredCleanup(ctx context.Context, id string, done tasks.Cleanup, err error) {
	var refused *tasks.CleanupRefusal
	if errors.As(err, &refused) && !refused.Unlanded() {
		if kept, ok := tasks.KeptOnRetirement(refused); ok {
			s.log.Printf("the isolated copy of retired housekeeping pass %s is kept (%s): %s", id, refused.Reason, refused.Detail)
			s.keepWork(ctx, kept)
			return
		}
	}
	s.logCleanup(ctx, done, err)
}

// answerRetirement answers a pass's retirement hold, on the file comment's
// terms. The caller has refused a subordinate and a worker and read the
// hold open.
func (s *Service) answerRetirement(ctx context.Context, req ipc.Request, r machine.TaskRespondRequest, hold store.Hold, rh retireHold) (machine.TaskAnswered, error) {
	switch r.Answer {
	case machine.AnswerKeep:
		resolved, err := s.store.ResolveHoldNoted(ctx, hold.Key, r.Answer, r.Note, req.HoldResolver())
		if errors.Is(err, store.ErrHoldResolved) {
			return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		if err != nil {
			return machine.TaskAnswered{}, err
		}
		s.publishHoldState(resolved)
		s.log.Printf("housekeeping pass %s is kept open: its retirement hold %s was answered %q", r.Task, hold.Key, r.Answer)
		if rh.Attempt == 0 {
			// The hold was offered before the pass's first attempt, so the
			// dispatch that read it was refused (store.ErrPassHeld) and
			// claimed nothing, and nothing else begins the pass: dispatch
			// runs at open and on intake, not on the loop's own. The
			// answer runs it, on dispatchInBackground's terms.
			s.log.Printf("housekeeping pass %s is dispatched now that hold %s is answered %q, since the hold had refused its dispatch", r.Task, hold.Key, r.Answer)
			s.dispatchInBackground()
		}
		return machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerRecorded}, nil
	case machine.AnswerRetire:
	default:
		return machine.TaskAnswered{}, fmt.Errorf("%w: task %s holds on %s, which is answered %q or %q, not %q",
			ipc.ErrInvalidRequest, r.Task, hold.Key, machine.AnswerRetire, machine.AnswerKeep, r.Answer)
	}
	release, err := s.claimSteer(r.Task)
	if err != nil {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	defer release()
	o, err := s.tasks().ObserveTask(ctx, r.Task)
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	if ref := latestRef(o.Snapshot.Attempts); ref != "" && latestSequence(o.Snapshot.Attempts) == rh.Attempt {
		if s.terminals == nil {
			return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrUnavailable, errRelaunchUnprobed)
		}
		probed := s.terminals.Probe(ctx, ref)
		if probed.Liveness != taskstate.Dead {
			return machine.TaskAnswered{}, fmt.Errorf("%w: %w: the worker of housekeeping pass %s (session %s) reads %s, so it still moves the pass and the hold %s stays open",
				ipc.ErrRefused, store.ErrPassMoves, r.Task, ref, probed.Liveness, hold.Key)
		}
		s.closeStallsOnDead(ctx, r.Task, rh.Attempt, ref, "", probed)
	}
	req2 := retirementOf(r.Task, o.Snapshot.Attempts, rh.Run)
	req2.Attempt = rh.Attempt
	req2.Hold, req2.Note, req2.By = hold.Key, r.Note, req.HoldResolver()
	retired, err := s.store.RetireHousekeepingPass(ctx, req2)
	switch {
	case errors.Is(err, store.ErrPassMoves), errors.Is(err, store.ErrPassClosed), errors.Is(err, store.ErrHoldResolved):
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w; the hold %s stays as it was", ipc.ErrRefused, err, hold.Key)
	case err != nil:
		return machine.TaskAnswered{}, err
	}
	for _, h := range retired.Closed {
		s.publishHoldState(h)
	}
	s.log.Printf("housekeeping pass %s was retired on the answer %q to hold %s, closing %d hold(s)", r.Task, r.Answer, hold.Key, len(retired.Closed))
	s.cleanUpLandedTask(ctx, r.Task)
	return machine.TaskAnswered{Hold: retired.Closed[0], Effect: machine.AnswerRetired}, nil
}

// passRun reports whether a run is a housekeeping pass's, which begin
// records as pipeline.Start.HousekeepingPass so the review stage holds the
// change to the pass's scope (internal/stages' housekeeping.go). It is
// decided from the task records and never from the push: the run's task is
// the task the run is attributed to (store.TaskOfRun, the same reading the
// task's snapshot makes of its runs), so a pass's own push, a rerun of its
// run, and a person's push of its branch while the pass holds the branch
// are all the pass's and held to the scope, which is wider than PRD
// section 6's "the run that a pass's push starts" and never narrower; a run
// of the branch after another task's attempt took it, as an ordinary
// delivery may once the pass is retired, is that task's and not held. A
// read that fails refuses the begin rather than starting a pass's run
// without the rule.
func (s *Service) passRun(ctx context.Context, record store.Run) (bool, error) {
	t, ok, err := s.store.TaskOfRun(ctx, record)
	if err != nil {
		return false, fmt.Errorf("service: whether run %s is a housekeeping pass's could not be read: %w", record.ID, err)
	}
	return ok && t.Housekeeping, nil
}
