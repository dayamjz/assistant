package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/config"
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
// escalation the coordinator relays. A pass something moves
// (supervisor.PassMoves) is in flight whatever it resolves (supervise.go's
// watched set, the stop guard through feed, the catch-up read), so its
// terminal is read on every pass. With housekeeping.interval zero, no wake
// is ever raised about a pass, open or not.
//
// # Residual gaps
//
//   - The due check runs off the loop's goroutine, so a pass report does not
//     carry it, and a check that is slower than the interval is not begun a
//     second time until it returns.
//   - A repository whose newest pass is open gets no due check, and nothing
//     here ends such a pass: the retirement hold is the link above this
//     one.
//   - The program cannot tell a rule from a fact; what a pass may change
//     rests on the worker's instructions and the run's review, and the
//     review's ask on paths outside housekeeping.paths (internal/stages).

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
// interval it stays open.
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
	return out
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

// passRun reports whether a run is a housekeeping pass's, which begin
// records as pipeline.Start.HousekeepingPass so the review stage holds the
// change to the pass's scope (internal/stages' housekeeping.go). It is
// decided from the task records and never from the push: the run's tasks
// are the tasks whose snapshot attributes the run to them
// (store.TasksOfRun), so a pass's own push, a rerun of its run, and a
// person's push of its branch while the pass holds the branch are all the
// pass's and held to the scope, which is wider than PRD section 6's "the
// run that a pass's push starts" and never narrower; a run of the branch
// after another task's attempt took it, as an ordinary delivery may once
// the pass is retired, is that task's and not held. A run attributed to a
// pass and to another task too is held, the stricter reading. A read that
// fails refuses the begin rather than starting a pass's run without the
// rule.
func (s *Service) passRun(ctx context.Context, record store.Run) (bool, error) {
	owners, err := s.store.TasksOfRun(ctx, record)
	if err != nil {
		return false, fmt.Errorf("service: whether run %s is a housekeeping pass's could not be read: %w", record.ID, err)
	}
	return slices.ContainsFunc(owners, func(t store.Task) bool { return t.Housekeeping }), nil
}
