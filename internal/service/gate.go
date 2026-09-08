package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// subject is what a gate's identifier resolves to: the working copy the gate
// belongs to and the repository record a run of it is created against.
//
// Both hooks need both, and they need them decided the same way, so the
// resolution is here rather than once per hook. Neither half is taken from the
// request: a caller names a gate and nothing else, so nothing a caller writes
// can attach a push to another repository's runs.
type subject struct {
	workingPath string
	repository  store.Repository
}

// gateSubject resolves the gate a hook names.
//
// Which working copy a gate belongs to is internal/gate's question and is
// asked there, so this service holds no second answer to it; the ownership
// index that package consults is this home's store, which is why it is handed
// one. What this adds is the repository record, because a run needs one and a
// gate is not one, which is the pairing assistant init writes.
func (s *Service) gateSubject(ctx context.Context, id string) (subject, error) {
	workingPath, err := gate.WorkingCopyFor(ctx, s.home.Root(), id, gate.WithIndex(s.store))
	if err != nil {
		return subject{}, err
	}
	repository, err := s.repositoryAt(ctx, workingPath)
	if err != nil {
		return subject{}, err
	}
	return subject{workingPath: workingPath, repository: repository}, nil
}

// admit decides whether a push to a gate may proceed.
//
// It is called from the gate's admission hook before any reference in the gate
// changes, and its refusal is what rejects the push, so PRD section 5's
// ordering - a refusal instead of a change rather than after one - rests on
// where the hook calls this from and not on anything here.
//
// What it establishes is that the push has somewhere to go: the gate resolves
// to exactly one working copy standing today, that working copy has a
// repository record, and the reference updates are ones this build can read.
// For a push that would start a run it establishes one thing more, that a
// driver could be built for this home. A push admitted without those is a push
// the gate accepts and starts nothing for, which is the failure a sealed gate
// exists to prevent, arriving through the front door instead.
//
// The driver is asked for here rather than left to the notification because of
// where the two hooks sit. This runs before any reference changes and its
// refusal rejects the push; the notification runs after every reference has
// moved, where a failure can be printed and cannot reject anything. A home
// nothing runnable can be built for would otherwise take the push, hold the
// branch, and start no run.
//
// It is asked for only when startsARun says some update in the push would
// start one, which is the same predicate the notification starts runs by. A
// push carrying only tags, only deletions, or no updates is admitted without
// one, because the gate is a repository git can push to normally and refusing
// a tag or the deletion of a stale branch would make it something else. That
// is a narrower check rather than a weaker one: no push that would start a run
// escapes it.
//
// What it does not establish is why a driver could not be built. Building one
// resolves an agent, opens the run service, assembles the pipeline and its
// executor, and any of those can be what failed, so the refusal carries the
// reason it was given rather than naming a cause of its own.
//
// What that does not cover is the gap between the two hooks. The notification
// asks for the driver again and can still fail, so a home that stops being
// able to build one after this answered leaves a push accepted with no run
// started, and no failure there can reject a push this already admitted.
//
// It does not judge the change. Whether the branch should be shared is what
// the nine stages are for, and admission that reached for that answer would be
// a second, weaker reviewer running before the first.
//
// Containment is decided before this is reached rather than here.
// internal/ipc refuses a restricted method to a caller its Ancestry places
// inside an active validation stage, and this method is restricted, so this is
// where PRD section 9's "push around a pipeline" would be refused - before any
// reference in the gate changes, on the one surface a push arrives on. Whether
// anything is contained today is a different question, and doc.go answers it:
// nothing calls StageStarted in this build, so the registry is empty and the
// refusal has nothing to fire on. That is stated rather than implied, because
// a restricted method reads as a guarded one.
func (s *Service) admit(ctx context.Context, req machine.GateRequest) (machine.Admission, error) {
	// Resolving is the check; the resolution itself is the notification's to
	// use.
	if _, err := s.gateSubject(ctx, req.Gate); err != nil {
		return machine.Admission{}, err
	}
	if err := checkUpdates(req); err != nil {
		return machine.Admission{}, err
	}
	if pushNeedsADriver(req) {
		if _, err := s.driverFor(ctx); err != nil {
			return machine.Admission{}, fmt.Errorf("service: %w - so nothing that could validate a push can "+
				"be built for this home, and the push to gate %s is refused rather than taken and left "+
				"unvalidated; resolve what that reason names and push again, because nothing records this "+
				"refusal. assistant doctor reports on the agent this home resolves, which is one of the "+
				"things a run needs and not all of them", err, req.Gate)
		}
	}
	refs := make([]string, 0, len(req.Updates))
	for _, update := range req.Updates {
		refs = append(refs, update.Ref)
	}
	return machine.Admission{Gate: req.Gate, Refs: refs}, nil
}

// checkUpdates refuses a request describing a push with an update git could
// not have written.
//
// The rule is gate.RefUpdate.Validate and is asked rather than restated, so
// what the command surface refuses when it reads a hook's standard input and
// what this refuses when it is handed the result are one rule. Both hooks ask,
// because an update this cannot read is an update neither of them can say
// anything about.
func checkUpdates(req machine.GateRequest) error {
	for _, update := range req.Updates {
		if err := update.Validate(); err != nil {
			return fmt.Errorf("service: the push to gate %s is described by an update this build cannot "+
				"read: %w", req.Gate, err)
		}
	}
	return nil
}

// notify starts the runs a push the gate accepted calls for, and answers as
// soon as they are recorded.
//
// PRD section 8 has a push return immediately, with the notification handing
// off and the service owning everything long-running, so each run's execution
// goes to a goroutine of this service's rather than being advanced on the
// caller's connection. A hook that exits the moment this answers leaves the
// runs moving; a hook that blocked until they finished would hold git open for
// the length of a validation.
//
// One run is started per branch the push moved. A reference that is not a
// branch, and a branch the push deleted, start nothing, and each is reported
// as ignored rather than passed over in silence: a push of five references
// that started two runs has to read as that rather than as a push that was
// fully acted on.
//
// A deletion also ends nothing. The PRD is silent on what deleting a branch
// should do to a run of it, so that branch's run keeps advancing and its
// ignored reason says so and names the command that ends it, rather than
// leaving an operator reading "nothing to validate" as "nothing outstanding".
func (s *Service) notify(ctx context.Context, req machine.GateRequest) (machine.Notification, error) {
	found, err := s.gateSubject(ctx, req.Gate)
	if err != nil {
		return machine.Notification{}, err
	}
	if err := checkUpdates(req); err != nil {
		return machine.Notification{}, err
	}
	out := machine.Notification{Gate: req.Gate}
	for _, update := range req.Updates {
		if !startsARun(update) {
			out.Ignored = append(out.Ignored, machine.Ignored{Ref: update.Ref, Reason: ignoredReason(update)})
			continue
		}
		started, err := s.startPushedBranch(ctx, found, update.Branch(), update.New)
		if err != nil {
			return machine.Notification{}, pushPartlyStarted(req.Gate, out.Started, update.Ref, err)
		}
		out.Started = append(out.Started, started)
	}
	return out, nil
}

// startsARun reports whether a push carrying this update starts a run for it,
// which is true of a branch the push moved and of nothing else.
//
// It is one predicate rather than one per caller because two callers ask it:
// the notification, to decide what to start, and admission, to decide whether
// this push needs a driver at all. A second spelling of the rule would let
// admission refuse a push for want of something the notification would never
// have used.
func startsARun(u gate.RefUpdate) bool {
	return u.Branch() != "" && !u.Deleted()
}

// ignoredReason says why a push started no run for this update, and answers
// only for one startsARun rejects.
//
// Both answers are reported rather than dropped, and neither may read as
// nothing being outstanding: the gate is a repository git can push to
// normally, so a tag and a deletion are accepted, and a deletion leaves that
// branch's run advancing.
func ignoredReason(u gate.RefUpdate) string {
	if u.Branch() == "" {
		return "not a branch, and the gate validates branches"
	}
	return "the push deletes this branch, so it starts no run, and this branch's existing run if it " +
		"has one is still advancing; assistant --cancel on that branch ends it"
}

// pushNeedsADriver reports whether any update in this push is one a run would
// be started for, which is what makes a driver something the push depends on.
func pushNeedsADriver(req machine.GateRequest) bool {
	for _, update := range req.Updates {
		if startsARun(update) {
			return true
		}
	}
	return false
}

// pushPartlyStarted reports a push whose branches did not all get a run, and
// names the ones that did.
//
// A push can carry several branches, and a failure on one arrives after the
// runs before it have been recorded and handed to this service. Those runs
// exist and are advancing, so a refusal naming only the branch that failed
// would report a push that started nothing while runs it started were moving.
//
// They are in the message rather than in a field, and that is the residual
// gap: this answer is an error, so a caller decoding the structured failure
// reads the identifiers out of prose. Giving them a field would mean a wire
// shape whose only producer is a store failure part way through a multi-branch
// push, which nothing in this repository can drive, and an untestable field is
// worth less than a message that says the same thing.
func pushPartlyStarted(gateID string, started []machine.Started, ref string, cause error) error {
	if len(started) == 0 {
		return fmt.Errorf("service: the push to gate %s started no run for %s: %w", gateID, ref, cause)
	}
	names := make([]string, 0, len(started))
	for _, run := range started {
		names = append(names, run.Run+" for "+run.Branch)
	}
	return fmt.Errorf("service: the push to gate %s started no run for %s, and the runs it did start "+
		"are advancing: %s: %w", gateID, ref, strings.Join(names, ", "), cause)
}

// startPushedBranch records the run for one pushed branch and hands its
// execution to this service.
//
// The commit is the one the push moved the branch to, and the branch is the
// one the push named. Neither is read from the working copy: the working copy
// stands wherever its owner left it, and a run recorded from its head would be
// a run about a commit nobody pushed.
func (s *Service) startPushedBranch(ctx context.Context, found subject, branch, head string) (machine.Started, error) {
	// What can be done before the branch is claimed is: resolving an agent
	// asks what is runnable on this machine, and the base is a git invocation.
	// Under the claim, branches would queue behind each other's.
	//
	// Not everything slow is out. claimPush reclaims the copy of the run it
	// supersedes and creates the new record under the claim, and both reach
	// internal/gate - a reachability walk over the gate's references for the
	// first, a resolution for the second. gateHoldsHead states that cost; what
	// waits on it is a second push of this same branch.
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.Started{}, err
	}
	base := s.baseOfPushedCommit(ctx, found, head)
	record, superseded, err := s.claimPush(ctx, built, found.repository.ID, branch, head, base)
	if err != nil {
		return machine.Started{}, err
	}
	s.beginInBackground(record, pipeline.Start{
		Branch:    record.Branch,
		Base:      found.repository.DefaultBranch,
		Submitted: record.SubmittedHead,
	})
	return machine.Started{Branch: branch, Head: head, Run: record.ID, Superseded: superseded}, nil
}

// claimPush creates the run a push starts, superseding the branch's run in
// flight when it has one, with the decision and both writes under the branch's
// own exclusion.
//
// PRD section 8 has pushes to one branch serialize and a new push supersede the
// run in progress. It takes the same gate a start and a rerun take, so the
// decision that a branch has one run is made in one place for all three.
//
// What is guaranteed is the record: the branch's newest unfinished run is moved
// to terminated before the new one is created, both under the branch's own
// exclusion, so the record never shows two live runs for one branch. A move
// refused because that run has already finished is not an error here, because
// the record is authoritative and this is reconciliation rather than a decision
// about what that run may do.
//
// What is not guaranteed is that the displaced run has stopped executing, and
// not even that it was told to. A run registers its cancellation with this
// service only once its own goroutine reaches the point that claims it, which
// is after its record already reads running, so a displaced run this service
// holds no cancellation for is signalled nothing. That is looked for twice,
// before the record is moved and again after, and the run is signalled if this
// service held one by the time either read looked; a run that registers after
// the second read is signalled nothing at all and executes its whole pipeline
// against a commit nobody is asking about, and only its own halting stops it.
//
// In no case is the displaced run's departure awaited, so nothing bounds how
// long two runs of one branch may execute at once. That is a missing wait
// rather than an interleaving. What the displaced run does keep for as long as
// it executes is its isolated copy: the record moves here, and
// reclaimWhenEnded gives the copy back only once the displaced segment is
// known to have ended, so a stage body still reading it does not lose its
// working tree. The mechanism that would bound it - a per-branch
// predecessor set, with the wait as the arriving run's own first step - is
// specified outside this tree, in internal/daemon's package documentation at
// tag pre-rebase-2-observation-edges, and is deliberately not implemented here;
// the second read above shortens the reach of the unsignalled state and closes
// none of it.
func (s *Service) claimPush(ctx context.Context, built *driver, repository, branch, head, base string) (store.Run, string, error) {
	key := branchKey{repository: repository, branch: branch}
	release, err := s.holdBranch(ctx, key)
	if err != nil {
		return store.Run{}, "", err
	}
	defer release()

	superseded := ""
	if active, found, err := s.activeRun(ctx, key.repository, key.branch); err != nil {
		return store.Run{}, "", err
	} else if found {
		s.signalCancellation(active.ID)
		if _, err := s.endAndReclaim(ctx, active.ID, built.runs.Terminate); err != nil {
			var wrong *store.RunStatusError
			if !errors.As(err, &wrong) {
				return store.Run{}, "", err
			}
			// The run finished on its own between the read and this write.
			// Nothing was superseded, and saying it was would report a run as
			// ended by this push that ended itself.
			s.log.Printf("run %s was %s when a new push to %s would have superseded it",
				active.ID, wrong.Actual, branch)
		} else {
			superseded = active.ID
			s.signalCancellation(active.ID)
			s.publishRunState(ctx, active.ID)
		}
	}
	record, err := s.create(ctx, run{
		repository: key.repository,
		branch:     key.branch,
		head:       head,
		base:       base,
		source:     intentSourceAbsent,
	})
	if err != nil {
		return store.Run{}, "", err
	}
	return record, superseded, nil
}

// signalCancellation ends the segment a run is advancing and records that
// ending in the run's slot, when this service is holding that run's
// cancellation.
//
// It is endRun, which is what makes the ending outrank the segment: a
// superseded run whose segment stops because this cancelled it is not one
// carryOn picks up again. The record it leaves is given back at once rather
// than held across a write, because the write this stands beside is
// claimPush's and the branch's own exclusion is what orders that.
//
// A run this service holds nothing for is left alone rather than treated as
// stopped: it may be one that has already finished, or one whose goroutine has
// not yet registered its cancellation, and this cannot tell those apart.
// Calling it more than once for one run is how claimPush looks twice, and
// costs nothing, because the cancellation a segment registers is idempotent.
func (s *Service) signalCancellation(runID string) {
	forget := s.endRun(runID)
	forget()
}

// baseOfPushedCommit is the commit the pushed change is measured against,
// empty when it could not be established.
//
// It reads the working copy the gate belongs to, which is where the push came
// from and so is where the pushed commit and the default branch are both
// likely to be. It is a best effort and says so by answering empty: a commit
// pushed from somewhere else is not in this working copy, and internal/store
// takes an empty base rather than having one invented. The gate's own
// repository holds the pushed commit for certain and does not hold the default
// branch, so it is not the better source it looks like.
func (s *Service) baseOfPushedCommit(ctx context.Context, found subject, head string) string {
	working, err := vcs.OpenWorktree(ctx, found.workingPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return ""
	}
	return baseCommit(ctx, working, head, found.repository.DefaultBranch)
}

// beginInBackground walks a run this service has just recorded, on a goroutine
// of its own, so the push that started it returns immediately.
//
// The context is this service's rather than the caller's. A run started by a
// push outlives the hook process that reported the push by design, and one
// derived from the caller's connection would be cancelled the moment that
// process exited, which is every time.
func (s *Service) beginInBackground(record store.Run, start pipeline.Start) {
	s.work.Add(1)
	go func() {
		defer s.work.Done()
		ctx, cancel := context.WithCancel(s.stopCtx)
		defer cancel()
		if _, err := s.begin(ctx, record, start); err != nil {
			s.log.Printf("run %s, started by a push to %s, stopped: %v", record.ID, record.Branch, err)
		}
	}()
}
