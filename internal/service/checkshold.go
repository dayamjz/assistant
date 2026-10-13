package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// ChecksHoldKey is the key under which this service files the hold the checks
// stage raised when checks_timeout elapsed on a run, when it closes that hold
// itself. It is derived from the run so that the hold is one record per run
// however many times it is looked at, which is what store.RegisterHold's
// idempotence rests on.
//
// It is exported for a reader of the record, such as a test or a report, and
// for nothing else: the key names a hold and resolving one is this package's.
func ChecksHoldKey(runID string) string { return "run/" + runID + "/checks-timeout" }

// closeChecksHoldIfPullRequestEnded is PRD section 11's recovery step 3, and
// it is also what a response reaches before it resumes a held run. For the
// hold the checks stage raised when checks_timeout elapsed, and only that
// hold, it re-reads the run's pull request by its recorded identifier, and a
// positively read merged or closed state closes the hold by an explicit
// resolution this service records - itself as the resolver, the state it read
// as the resolution - and ends the run as PRD section 5's "Waiting on checks"
// says for that state. It reports whether it ended the run.
//
// Which hold it is, is read off the record and not inferred. The run must be
// held, its checkpoint must stand at the checks stage's hold, and the report
// that stage recorded must be the elapsed hold on stages.ChecksTimeoutHeld's
// terms, which refuses a report holding on anything a person was asked to
// decide. A run held anywhere else is returned untouched with no read made,
// because the pull request's state must not answer a question a person was
// asked.
//
// The read is forge.Provider.Get by the number the pull request stage
// recorded, never forge.Provider.Find, which does not find a merged or closed
// pull request. Open and a state this build does not recognize stay held. So
// does a pull request that could not be read: the refusal is a typed answer
// this service reads and logs, and the hold stands for the person it was
// raised for, which is what PRD section 11 asks of "not read". What that
// leaves open is named rather than implied: a provider that refuses on every
// restart leaves the hold to the person every time, and nothing here retries
// between restarts or answers.
//
// The hold record is written here, at the resolution, rather than when the
// stage held, because nothing in this build registers a hold row for a halt:
// a run's hold is its checkpoint's decision and its record's held status.
// Registering it here is what gives the resolution a hold to be a resolution
// of, so the record carries who closed it and on what. The resolution and the
// run's move are two writes, so a service that stops between them leaves a
// closed hold on a held run; the next look re-registers nothing, meets
// store.ErrHoldResolved, and makes the move, so the state is repaired by the
// path that produced it. The move itself is anchored to held, so a run a
// person answered in the meantime is left where their answer put it.
func (s *Service) closeChecksHoldIfPullRequestEnded(ctx context.Context, record store.Run) (bool, error) {
	if record.Status != store.RunHeld {
		return false, nil
	}
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	if err != nil {
		return false, fmt.Errorf("service: reading where held run %s stands: %w", record.ID, err)
	}
	if latest.Status != graph.StatusHalted || latest.Position != pipeline.StageCI.HoldNode() {
		return false, nil
	}
	report, err := pipeline.StageReport(latest.State, pipeline.StageCI)
	if err != nil {
		return false, err
	}
	if !stages.ChecksTimeoutHeld(report) {
		return false, nil
	}
	repository, number, err := recordedPullRequest(latest.State)
	if err != nil {
		return false, fmt.Errorf("service: run %s holds on an elapsed checks wait: %w", record.ID, err)
	}

	provider, err := s.forge.Open(repository)
	if err != nil {
		return false, fmt.Errorf("service: opening the code host for run %s: %w", record.ID, err)
	}
	pr, err := provider.Get(ctx, number)
	if err != nil {
		// Not read keeps the hold, which is the one answer a refusal gets
		// here; it is read as a typed value and named in the log, and the
		// hold stands for the person it was raised for.
		s.log.Printf("run %s stays held: pull request %d of %s could not be read: %v", record.ID, number, repository, err)
		return false, nil
	}
	var end store.PullRequestEnd
	var move func(context.Context, string) (store.Run, error)
	built, err := s.driverFor(ctx)
	if err != nil {
		return false, err
	}
	switch pr.State {
	case forge.PullRequestStateMerged:
		end, move = store.PullRequestMerged, built.runs.PassOnMerge
	case forge.PullRequestStateClosed:
		end, move = store.PullRequestClosed, built.runs.FailOnClose
	default:
		// Open, and any state forge did not recognize, which it maps to its
		// zero value: only a positively read merged or closed state ends
		// the hold.
		return false, nil
	}

	key := ChecksHoldKey(record.ID)
	if _, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     key,
		RunID:   store.Known(record.ID),
		Subject: "checks_timeout elapsed on pull request " + strconv.Itoa(number) + " of " + repository,
		Detail:  report.Summary,
	}); err != nil {
		return false, err
	}
	resolution := "pull request " + strconv.Itoa(number) + " read as " + pr.State.String() + ": " + end.Reason()
	if _, err := s.store.ResolveHold(ctx, key, resolution, store.ResolvedByProgram()); err != nil && !errors.Is(err, store.ErrHoldResolved) {
		return false, err
	}
	moved, err := move(ctx, record.ID)
	if err != nil {
		var wrong *store.RunStatusError
		if !errors.As(err, &wrong) {
			return false, err
		}
		// Answered, ended, or already moved by an earlier look: the record
		// is authoritative, and this is reconciliation rather than a
		// decision about what the run may do.
		s.log.Printf("run %s was %s rather than held when its pull request was read as %s", record.ID, wrong.Actual, pr.State)
		return false, nil
	}
	s.log.Printf("run %s ended as %s: pull request %d of %s was read as %s at the checks hold", record.ID, moved.Status, number, repository, pr.State)
	s.reclaimCopy(ctx, moved)
	s.publishRunState(ctx, record.ID)
	return true, nil
}

// recordedPullRequest reads the repository on the code host and the pull
// request number a run's state records, which is what the pull request stage
// wrote and what the checks stage read. A run holding on an elapsed checks
// wait recorded both, because the checks body holds on other terms when
// either is missing, so an absence here is an error about the record rather
// than a run with nothing to re-read.
func recordedPullRequest(state graph.State) (string, int, error) {
	repository := ""
	if v, ok := state.Get(string(pipeline.KeyForgeRepository)); ok {
		repository, _ = v.Text()
	}
	if repository == "" {
		return "", 0, errors.New("the run's state names no repository on the code host")
	}
	text := ""
	if v, ok := state.Get(string(pipeline.KeyPullRequest)); ok {
		text, _ = v.Text()
	}
	number, err := strconv.Atoi(text)
	if err != nil || number < 1 {
		return "", 0, fmt.Errorf("the run's state records pull request %q, which is not a positive number", text)
	}
	return repository, number, nil
}
