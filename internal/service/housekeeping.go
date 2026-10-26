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
	"github.com/dayamjz/assistant/internal/pipeline"
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
//     pass whose newest attempt recorded no base, so missing information is
//     never read as nothing having changed;
//   - an empty trusted list raises nothing: housekeeping is off for the
//     repository;
//   - a tip other than the last pass's base, or no pass ever, raises the
//     wake;
//   - a tip that is the base raises nothing.
//
// The marker is advanced in the wake's own transaction, or alone when
// nothing was raised, so the check runs again only after another interval in
// which no pass was created. The interval therefore spaces the fetches too,
// one per repository per interval, which is what housekeeping.interval's
// floor is chosen against; the cost is that a default branch that moves just
// after a check that found it unmoved is seen up to an interval later.
//
// # An open pass
//
// Once the newest pass has been open for a whole interval, the wake names it
// and what it waits on, keyed by the interval it reached, so it is raised
// once per interval while the pass stays open, whether or not the default
// branch moved, and whether or not something moves it: that is the long-run
// escalation the coordinator relays. When nothing moves it
// (supervisor.PassMoves), it is also offered the retirement hold, keyed by
// the interval, the latest attempt and the newest run it was decided
// against, so at most one is registered per interval, none while an earlier
// one is unanswered, and a retire answer given after a run started or the
// worker was begun again is refused by store.RetireHousekeepingPass's
// anchors. A pass something moves is never offered one, and is in flight
// whatever it resolves (supervise.go's watched set, the stop guard through
// feed, the catch-up read), so its terminal is read on every pass. With
// housekeeping.interval zero, no wake and no hold is ever raised about a
// pass, open or not.
//
// # Rebase to nothing, on proof
//
// Each pass also looks at every open pass whose newest attributed run
// passed with every attributed run ended and no open hold that would
// relaunch its worker, whatever the interval. Off the loop's goroutine and
// under the task's slot, it reads that run's latest checkpoint and goes on
// only when its recorded position says the rebase stage found nothing left
// to change (pipeline.NothingLeft). It then takes the pass's branch's
// exclusion, which every run creation takes, reads the pass again and asks
// the store whether the retirement would be refused as the records stand
// (store.CheckRetirement), so a run started, a relaunch, or a hold offering
// to relaunch the worker since the loop chose the pass ends nothing (P6).
// Only then, still holding the branch, does it end the worker as a landed
// task's is ended, through the session this service holds and with no hold,
// verifies the end by a probe reading the terminal dead, closes the pass's
// stall holds on that reading as stallhold.go's closure does, and writes the
// retirement record through store.RetireHousekeepingPass, which refuses it
// when something moved the pass meanwhile. A worker this service does not
// hold, which only a stop ends, reads Unobserved, and nothing is written.
// AGE-487 tracks giving every delivery task whose rebase leaves nothing to
// deliver an end of its own; once it lands, this end is removed rather than
// kept beside it (P14).
//
// # Every other end, by an answer
//
// answerRetirement answers the retirement hold: keep closes it and leaves
// the pass open; retire, under the task's slot, refuses a pass whose worker
// the probe does not read dead and otherwise asks
// store.RetireHousekeepingPass, which answers stop on every open hold of the
// pass whose retry would relaunch its worker (isRetryHold) under the same
// resolver; any other answer is refused with the hold open. Standing consent
// answers no task hold, so it never answers this one.
//
// After either retirement the pass's copy is asked the cleanup decision with
// the terminal read dead, so a copy holding work that has not landed becomes
// a kept work entry (P15) and nothing is removed without a proof; a copy
// refused for a report never stored or a decision never registered is kept
// the same way (tasks.KeptOnRetirement), since no later cleanup is asked for
// a retired pass. An on-proof retirement gives way to an open retirement
// hold, which waits on a person, and the store refuses a retirement hold for
// a pass already retired, so the two are never both left standing. A retired
// pass is never begun again: every attempt claim refuses it in the store
// (store.ErrPassRetired), and relaunch.go refuses it before it sweeps
// anything.
//
// # Residual gaps
//
//   - The due check runs off the loop's goroutine, so a pass report does not
//     carry it, and a check that is slower than the interval is not begun a
//     second time until it returns.
//   - The branch's exclusion orders an on-proof retirement against a run of
//     the pass within this service, which PRD section 8 gives a home alone.
//     A push the gate admitted while the retirement held the branch creates
//     its run once the record is written, so that run is of a retired pass:
//     its worker was ended by the retirement and its work is the push, which
//     validates as any run does. Nothing is lost; the pass's claim on the
//     repository is already released.
//   - The on-proof retirement is tried once per run whose position read
//     nothing left; one refused because something moved the pass is tried
//     again only when a later run of the pass passes, and is otherwise left
//     to the retirement hold.
//   - The program cannot tell a rule from a fact; what a pass may change
//     rests on the worker's instructions and the run's review, and the
//     review's ask on paths outside housekeeping.paths (internal/stages).

// housekeepingState is what this service remembers between passes about
// housekeeping, none of it a fact the records do not hold: which
// repositories' due checks are running now, and which runs' positions were
// read already. Losing it costs a repeated read and nothing else.
type housekeepingState struct {
	mu sync.Mutex
	// checking holds the repositories whose due check is running.
	checking map[string]bool
	// read holds the runs whose position an on-proof retirement read, and
	// what it said.
	read map[string]bool
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
	for _, passes := range byRepository {
		for _, o := range passes {
			s.retireOnProofFromPass(o)
		}
	}
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
	ctx, cancel := s.sup.bound(c.ctx)
	h, err := s.store.RegisterHoldUnder(ctx, c.inc, rh.Attempt, store.Hold{Key: rh.key(), TaskID: store.Known(id),
		Subject: "whether to retire a housekeeping pass that has been open for a whole interval and that nothing moves",
		Detail:  retireDetail(r, o, interval)})
	cancel()
	var moved *store.TaskMovedError
	switch {
	case errors.As(err, &moved), errors.Is(err, store.ErrPassRetired):
		// Begun again since the pass read it, so it moves, or retired on
		// proof since, so its claim is released: either way it is owed none.
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
		"so the next pass can be created, and nothing begins its work again. %s. Its copy is then asked the cleanup decision, which keeps "+
		"anything not yet landed for you to decide on. Answer %q to retire it, or %q to leave it open, which offers this again "+
		"after another interval of %s.",
		o.Snapshot.Task.ID, r.ID, o.Snapshot.Task.CreatedAt.UTC().Format(time.RFC3339), passWaitsOn(o, false), left,
		machine.AnswerRetire, machine.AnswerKeep, interval)
}

// raiseHousekeeping appends one housekeeping-due detection, classified on
// the kind table's terms, under the incarnation's fence, with check
// advanced in the same transaction when it is not nil, and reports the
// write. A marker already raised and acknowledged is nothing owed.
func (s *Service) raiseHousekeeping(c *incarnation, w store.HousekeepingWakeRequest, check *store.HousekeepingCheck) taskReport {
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
		if n := len(last.Snapshot.Attempts); n > 0 {
			base = last.Snapshot.Attempts[n-1].KnowledgeCommit
		}
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
// the wake or the marker written under the incarnation that started it.
func (s *Service) dueCheck(c *incarnation, r store.Repository, follows string, lastCreated time.Time, base store.Optional[string], now time.Time) taskReport {
	checked := &store.HousekeepingCheck{Repository: r.ID, Pass: follows, CheckedAt: now}
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
		return raise(fmt.Sprintf("the base of housekeeping pass %s of repository %s, the default-branch commit its newest attempt recorded at launch, "+
			"could not be read: it records none, so whether the default branch %s, now at %s, moved since is not known", follows, r.ID, r.DefaultBranch, tip))
	}
	if b == tip {
		return s.advanceHousekeeping(c, *checked)
	}
	return raise(fmt.Sprintf("the default branch %s of repository %s stands at %s, past %s, the base of housekeeping pass %s created at %s",
		r.DefaultBranch, r.ID, tip, b, follows, lastCreated.UTC().Format(time.RFC3339)))
}

// advanceHousekeeping writes a due check's marker with no wake.
func (s *Service) advanceHousekeeping(c *incarnation, check store.HousekeepingCheck) taskReport {
	ctx, cancel := s.sup.bound(c.ctx)
	defer cancel()
	return taskReport{Watched: true, Err: s.store.AdvanceHousekeepingMarker(ctx, c.inc, check)}
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

// passWaits is the Relaunches an on-proof retirement of task is decided
// with: every hold that waits on a person about whether the pass ends, the
// relaunch holds and its retirement hold, since the program answers none.
func passWaits(task string) func(string) bool {
	return func(key string) bool {
		_, retire := parseRetireHold(key, task)
		return retire || isRetryHold(key, task)
	}
}

// retirementOf is the store request for a retirement of o, with the
// owners' predicates.
func retirementOf(task string, attempt int64, run string) store.Retirement {
	return store.Retirement{Task: task, Attempt: attempt, Run: run, Relaunches: passRelaunches(task),
		Ended: runs.Finished, Delivered: taskstate.Delivered}
}

// retireOnProofFromPass starts the on-proof retirement of an open pass the
// loop read, when it is a candidate on the file comment's terms, off the
// loop's goroutine under the task's slot. A slot another writer holds skips
// the pass until the next one.
func (s *Service) retireOnProofFromPass(o taskstate.Observed) {
	snap := o.Snapshot
	if !passOpen(o) || len(snap.Runs) == 0 || snap.Runs[0].Status != store.RunPassed {
		return
	}
	for _, run := range snap.Runs {
		if !taskstate.RunSettled(run.Status) {
			return
		}
	}
	waits := passWaits(snap.Task.ID)
	for _, h := range snap.Holds {
		if h.Open() && waits(h.Key) {
			return
		}
	}
	run := snap.Runs[0].ID
	s.housekeeping.mu.Lock()
	_, read := s.housekeeping.read[run]
	s.housekeeping.mu.Unlock()
	if read {
		return
	}
	id := snap.Task.ID
	release, err := s.claimSteer(id)
	if err != nil {
		return
	}
	if !s.startWork() {
		release()
		return
	}
	go func() {
		defer s.work.Done()
		defer release()
		err := s.retireOnProof(s.stopCtx, id, run)
		if err != nil {
			s.log.Printf("housekeeping pass %s was not retired on run %s: %v", id, run, err)
		}
		if s.sup != nil && s.sup.hooks.afterRetireOnProof != nil {
			s.sup.hooks.afterRetireOnProof(id, run, err)
		}
	}()
}

// errNothingLeftUnread is an on-proof retirement whose run's position did
// not say nothing was left to change.
var errNothingLeftUnread = errors.New("service: the run's recorded position does not say its rebase found nothing left to change")

// retireOnProof is the on-proof retirement of one pass on run, under the
// task's slot, on the file comment's terms. It reports why nothing was
// written, or nil once the record is.
func (s *Service) retireOnProof(ctx context.Context, id, run string) error {
	latest, err := s.checkpoints.Latest(ctx, run)
	if err != nil {
		return fmt.Errorf("reading run %s's position: %w", run, err)
	}
	nothing := pipeline.NothingLeft(latest.State)
	s.housekeeping.mu.Lock()
	if s.housekeeping.read == nil {
		s.housekeeping.read = make(map[string]bool)
	}
	s.housekeeping.read[run] = nothing
	s.housekeeping.mu.Unlock()
	if !nothing {
		return errNothingLeftUnread
	}
	o, err := s.tasks().ObserveTask(ctx, id)
	if err != nil {
		return err
	}
	attempt := latestSequence(o.Snapshot.Attempts)
	r := retirementOf(id, attempt, run)
	r.OnProof, r.Relaunches = true, passWaits(id)
	branch, ok := o.Snapshot.Task.Branch.Get()
	repository, hasRepository := o.Snapshot.Task.RepositoryID.Get()
	if !ok || !hasRepository {
		return fmt.Errorf("%w: pass %s works on no branch, so no run of it can be the proof", store.ErrPassMoves, id)
	}
	// The check, the end of the worker and the record are one act against
	// a new run of the pass: every way a run is created (a push, a start, a
	// rerun) takes its branch's exclusion, which this holds from the check
	// to the write, so no run of the pass starts in between. The slot this
	// runs under keeps a relaunch out the same way. A newer run, a relaunch,
	// or a hold that offers to relaunch the worker since the loop chose the
	// pass refuses at the check, and the worker keeps running (P6).
	release, err := s.holdBranch(ctx, branchKey{repository: repository, branch: branch})
	if err != nil {
		return err
	}
	defer release()
	if err := s.store.CheckRetirement(ctx, r); err != nil {
		return err
	}
	if s.betweenRetirementCheckAndEnd != nil {
		s.betweenRetirementCheckAndEnd()
	}
	if ref := latestRef(o.Snapshot.Attempts); ref != "" {
		if _, err := s.endHeldWorker(id, ref); err != nil {
			return fmt.Errorf("ending its worker: %w", err)
		}
		if s.terminals == nil {
			return errRelaunchUnprobed
		}
		probed := s.terminals.Probe(ctx, ref)
		if probed.Liveness != taskstate.Dead {
			return fmt.Errorf("its worker's end could not be verified: the probe of session %s read %s (%s %s), "+
				"and a worker this service does not hold is ended only by a stop", ref, probed.Liveness, probed.Reason, probed.Detail)
		}
		s.closeStallsOnDead(ctx, id, attempt, ref, "", probed)
	}
	retired, err := s.store.RetireHousekeepingPass(ctx, r)
	if err != nil {
		return err
	}
	s.log.Printf("housekeeping pass %s was retired on the proof of run %s, whose rebase found nothing left to change", id, run)
	s.askRetiredCleanup(ctx, retired.Record.Task)
	return nil
}

// askRetiredCleanup asks the cleanup decision for a retired pass's copy,
// whose worker a retirement has read dead, and logs what it answered. A copy
// kept for its work becomes a kept work entry through logCleanup, and so
// does a copy tasks.KeptOnRetirement keeps for a report or hold its
// retirement means will never come (P15).
func (s *Service) askRetiredCleanup(ctx context.Context, id string) {
	if s.cleaner == nil {
		s.log.Printf("the isolated copy of retired housekeeping pass %s was not cleaned up: this service was built without a cleanup decision", id)
		return
	}
	done, err := s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: taskstate.Dead})
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
	req2 := retirementOf(r.Task, rh.Attempt, rh.Run)
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
	s.askRetiredCleanup(ctx, r.Task)
	return machine.TaskAnswered{Hold: retired.Closed[0], Effect: machine.AnswerRetired}, nil
}

// passBranch reports whether a run is on a housekeeping pass's branch, which
// begin records as pipeline.Start.HousekeepingPass so the review stage holds
// the change to the pass's scope (internal/stages' housekeeping.go). It is
// decided from the task records and the run's repository and branch, never
// from the push: a pass's own push, a rerun of its run, and a person's push
// of its branch are all runs attributed to the pass by its branch, so each
// is held to the scope, which is wider than PRD section 6's "the run that a
// pass's push starts" and never narrower. A read that fails refuses the
// begin rather than starting a pass's run without the rule.
func (s *Service) passBranch(ctx context.Context, record store.Run) (bool, error) {
	all, err := s.store.Tasks(ctx)
	if err != nil {
		return false, fmt.Errorf("service: whether run %s is a housekeeping pass's could not be read: %w", record.ID, err)
	}
	for _, t := range all {
		if t.Housekeeping && t.RepositoryID.Or("") == record.RepositoryID && t.Branch.Or("") == record.Branch {
			return true, nil
		}
	}
	return false, nil
}
