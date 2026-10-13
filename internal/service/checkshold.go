package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// ChecksHoldKey is the key under which this service files the hold the checks
// stage raised when checks_timeout elapsed on a run, when it closes that hold
// itself. It is derived from the run so that the hold is one record per run
// however many times it is looked at, which is what the registration's
// idempotence rests on.
//
// It is exported for a reader of the record, such as a test or a report, and
// for nothing else: the key names a hold and resolving one is this package's.
func ChecksHoldKey(runID string) string { return "run/" + runID + "/checks-timeout" }

// ChecksHoldReadBound is how long a recovery-time re-read of a held run's pull
// request may take before it counts as not read and the hold stands. It
// bounds the one provider call the re-read makes, Get; Host.Open makes none.
// It is here because nothing else bounds that call: internal/forge's grace
// deadline only bounds the wait on output pipes once the context has ended,
// so without a context that ends a provider command that never answers would
// hold Open with the socket already bound. An answer to a hold runs under
// the caller's own context instead, which ends when the caller gives up.
const ChecksHoldReadBound = 30 * time.Second

// closeChecksHoldAtRecovery is PRD section 11's recovery step 3 for one held
// run, under ChecksHoldReadBound, logging what it did not do; a write refused
// after a positive read is logged too, because at recovery there is nobody
// else to tell.
func (s *Service) closeChecksHoldAtRecovery(ctx context.Context, record store.Run) {
	bounded, cancel := context.WithTimeout(ctx, ChecksHoldReadBound)
	defer cancel()
	if _, err := s.closeChecksHoldIfPullRequestEnded(bounded, record); err != nil {
		s.log.Printf("recovery could not end held run %s on its pull request: %v", record.ID, err)
	}
}

// closeChecksHoldIfPullRequestEnded is PRD section 11's recovery step 3, and
// it is also what a response reaches before it resumes a held run. For the
// hold the checks stage raised when checks_timeout elapsed, and only that
// hold, it re-reads the run's pull request by its recorded identifier, and a
// positively read merged or closed state closes the hold by an explicit
// resolution this service records - itself as the resolver, the state it read
// as the resolution - and ends the run as PRD section 5's "Waiting on checks"
// says for that state, writing section 8's merge record. It reports which end
// it ended the run on, or the zero end when it did not.
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
// pull request. Open and a state this build does not recognize stay held.
//
// Everything short of a positive read leaves the hold to the person, and
// none of it is an error to the caller: a position or report that could not
// be read, a recorded number that is not one, a host that could not open a
// provider, and a read the provider refused or that ran out are each a
// typed answer this service reads, logs, and treats as not read, which is
// what PRD section 11 asks of "not read". That is deliberate rather than
// lenient. The one caller besides recovery is a person answering their own
// hold, and an error here would stop that answer over a code host that is
// down; the hold is theirs to answer whatever the host says. What that leaves
// open is named rather than implied: a provider that refuses on every
// restart leaves the hold to the person every time, and nothing here retries
// between restarts or answers. The error that is returned is a write refused
// after a positive read, because by then the run is being ended and a caller
// has to know it was not.
//
// The hold is registered and resolved in the same store transaction as the
// run's move and its merge record, through internal/runs' two pull request
// moves, so no row ever says the program resolved a hold on a run the program
// did not move: a move refused because the run is no longer held - a person
// answered it between the read and the write - rolls the resolution back with
// it, and is reported here as not ended rather than as an error. A hold
// already resolved while the run is still held is left as it stands and the
// move still happens, which store.HoldClosure states. The hold row is written
// here rather than when the stage held, because nothing in this build
// registers a hold row for a halt: a run's hold is its checkpoint's decision
// and its record's held status, and registering it here is what gives the
// resolution a hold to be a resolution of. Its Detail is the stage's report
// summary, which names the configured timeout and nothing time-varying; what
// is time-varying is the resolution's reading, and it lives in the hold row
// alone, never in graph state.
//
// The window that remains is between the read and the transaction, and it
// is the record's own nature rather than a gap: the merge record is the
// program's record that it saw the pull request merged or closed, and a pull
// request reopened after the read was still read as it was.
func (s *Service) closeChecksHoldIfPullRequestEnded(ctx context.Context, record store.Run) (store.PullRequestEnd, error) {
	if record.Status != store.RunHeld {
		return "", nil
	}
	read, report, ok := s.readChecksHoldPullRequest(ctx, record)
	if !ok {
		return "", nil
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return "", err
	}
	number := strconv.Itoa(read.PullRequest)
	closing := store.HoldClosure{
		Hold: store.Hold{
			Key:     ChecksHoldKey(record.ID),
			RunID:   store.Known(record.ID),
			Subject: "checks_timeout elapsed on pull request " + number,
			Detail:  report.Summary,
		},
		Resolution: "pull request " + number + " read as " + read.End.String() + ": " + read.End.Reason(),
		By:         store.ResolvedByProgram(),
	}
	var move func(context.Context, string, store.PullRequestRead, store.HoldClosure) (store.Run, error)
	switch read.End {
	case store.PullRequestMerged:
		move = built.runs.PassOnMerge
	case store.PullRequestClosed:
		move = built.runs.FailOnClose
	default:
		return "", fmt.Errorf("service: run %s: %w: %q", record.ID, store.ErrUnknownPullRequestEnd, read.End)
	}
	moved, err := move(ctx, record.ID, read, closing)
	if err != nil {
		var wrong *store.RunStatusError
		if !errors.As(err, &wrong) {
			return "", err
		}
		// Answered, ended, or already moved by an earlier look: the record
		// is authoritative, and this is reconciliation rather than a
		// decision about what the run may do. The transaction rolled the
		// resolution back, so the hold row does not say otherwise.
		s.log.Printf("run %s was %s rather than held when its pull request was read as %s; nothing was written",
			record.ID, wrong.Actual, read.End)
		return "", nil
	}
	s.log.Printf("run %s ended as %s: pull request %s was read as %s at the checks hold", record.ID, moved.Status, number, read.End)
	s.reclaimCopy(ctx, moved)
	s.publishRunState(ctx, record.ID)
	return read.End, nil
}

// readChecksHoldPullRequest decides whether record holds on an elapsed checks
// wait and, when it does, reads its pull request. It reports the read and
// the checks stage's recorded report only for a positively read merged or
// closed state; every other answer, including each failure on the way,
// is logged where it is not simply "not this hold" and reported as false.
func (s *Service) readChecksHoldPullRequest(ctx context.Context, record store.Run) (store.PullRequestRead, findings.Report, bool) {
	none := store.PullRequestRead{}
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	if err != nil {
		s.log.Printf("held run %s stays held: its position could not be read: %v", record.ID, err)
		return none, findings.Report{}, false
	}
	if latest.Status != graph.StatusHalted || latest.Position != pipeline.StageCI.HoldNode() {
		return none, findings.Report{}, false
	}
	report, err := pipeline.StageReport(latest.State, pipeline.StageCI)
	if err != nil {
		s.log.Printf("held run %s stays held: its checks report could not be read: %v", record.ID, err)
		return none, findings.Report{}, false
	}
	if !stages.ChecksTimeoutHeld(report) {
		return none, findings.Report{}, false
	}
	repository, number, err := recordedPullRequest(latest.State)
	if err != nil {
		s.log.Printf("held run %s stays held: %v", record.ID, err)
		return none, findings.Report{}, false
	}
	provider, err := s.forge.Open(repository)
	if err != nil {
		s.log.Printf("held run %s stays held: the code host for %s could not be opened: %v", record.ID, repository, err)
		return none, findings.Report{}, false
	}
	pr, err := provider.Get(ctx, number)
	if err != nil {
		s.log.Printf("held run %s stays held: pull request %d of %s could not be read: %v", record.ID, number, repository, err)
		return none, findings.Report{}, false
	}
	read := store.PullRequestRead{PullRequest: number, Head: pr.HeadCommit}
	switch pr.State {
	case forge.PullRequestStateMerged:
		read.End = store.PullRequestMerged
	case forge.PullRequestStateClosed:
		read.End = store.PullRequestClosed
	default:
		// Open, and any state forge did not recognize, which it maps to its
		// zero value: only a positively read merged or closed state ends
		// the hold.
		return none, findings.Report{}, false
	}
	return read, report, true
}

// recordedPullRequest reads the repository on the code host and the pull
// request number a run's state records, which is what the pull request stage
// wrote and what the checks stage read. A run holding on an elapsed checks
// wait, or ended by its pull request's state, recorded both, because the
// checks body holds on other terms when either is missing, so an absence
// here is an error about the record rather than a run with nothing to read.
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
