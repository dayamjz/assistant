package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// The read that writes the merge record of a run that ended checks-passed:
// PRD section 6's "the existing code-host read writes the merge record as it
// does for a merge you made", and the Ready to Landed edge of its task
// lifecycle, "merge proof read from the code host, recorded once".
//
// # One read, two callers
//
// readMergeAt is the one place a read of a pull request is turned into a
// merge record's input for a run that already passed, and recordMergeRead is
// the one place that input is written, through internal/runs'
// RecordMergeAfterPass. autonomy.go's read after the program's own merge and
// this file's read of a person's merge both go through them, so the two
// cannot come apart on what counts as this run's merge (P14).
//
// What counts is a pull request the provider positively reports merged with
// its head at the commit the run observed, which is pipeline.KeyPushed, the
// commit the push stage forwarded (P6). A pull request merged at another head
// is not this run's merge: someone pushed past what the run validated, and
// the record would claim the run's commit landed when another one did. That
// answer is reported in the service log and recorded nowhere. Open, closed,
// and a state internal/forge does not recognize record nothing either, and a
// read that fails records nothing and moves nothing.
//
// # When a person's merge is read
//
// readPersonMerges is one pass, and the service runs one as it opens, on a
// goroutine of its own once recovery is done, and then one every
// mergeReadInterval for as long as it serves. A pass reads only the runs
// machine.OutcomeOf reads as checks-passed, which is a passed record with no
// merge record, and of those only a run whose final state names a repository
// on the code host and a pull request number; any other run is passed over
// before the code host is opened. Each pass reads each such run's pull
// request at most once, with one forge.Provider.Get under ChecksHoldReadBound
// of its own, so the code host is asked at most once per such run per
// interval, and never twice at once, because the passes run one after
// another on one goroutine.
//
// That trigger was chosen over the other two re-reads this service already
// makes. Recovery alone would leave a merge made while the service runs
// unrecorded until the next restart, and the task Ready rather than Landed
// for all that time. Reading on demand, when runs.list or run.get reads a
// checks-passed run, would make a read verb wait on the code host once per
// run it lists and write as a side effect, and it would still never record a
// merge nobody asked about, which leaves internal/taskstate's Landed waiting
// on a status call. The supervisor loop's own pass was not used either:
// every blocking call there is bounded by one beacon interval and fenced by
// the incarnation, and a code-host read inside it would put the beacon, and
// so the watchdog's verdict on the loop, at the mercy of the code host.
//
// A pull request the pass finds merged at another head is final, since a
// merged pull request does not reopen, so the run is not read again by this
// service; the same holds for a run whose state recorded no observed head,
// which nothing can anchor. Both are remembered in memory only, so a restart
// reads and reports each once more.
//
// # Residual gaps
//
//   - The merge record is the program's record that it saw the merge, and the
//     record lags the merge by up to mergeReadInterval plus the pass's own
//     reads, or by the time to the next start for a service that is down.
//   - A run whose pull request stays open or is closed without merging is
//     read once per interval for as long as it stays checks-passed, which is
//     forever: nothing ages it out, so the reads per interval grow with the
//     number of such runs in the home.
//   - The record is written through internal/runs, whose run service needs a
//     resolved agent, so a home where no agent resolves records nothing and
//     says so in the log on every pass that found a merge to record.
//   - A pass that read a merge and then could not write it reads it again on
//     the next pass; nothing in between says the record is pending.

// mergeReadInterval is how long the service waits between passes of
// readPersonMerges. Five minutes keeps a checks-passed run's cost to the code
// host at one pull request read per five minutes, against the checks body's
// one a minute for a run whose checks it is waiting on, and bounds how long
// a person's merge goes unrecorded while the service runs. It is chosen, not
// measured.
const mergeReadInterval = 5 * time.Minute

// errNoMergeRead is the class of every answer readMergeAt does not record.
var errNoMergeRead = errors.New("service: the pull request was not read merged at the head the run observed")

// mergeNotRead is one answer readMergeAt does not record: the pull request as
// it was read, or no read at all when the run observed no head.
type mergeNotRead struct {
	number   int
	observed string
	// read is false when nothing was asked of the code host.
	read  bool
	state forge.PullRequestState
	head  string
}

func (e *mergeNotRead) Error() string {
	switch {
	case !e.read:
		return fmt.Sprintf("service: pull request %d was not read: the run recorded no head it observed, so no merge could be bound to one", e.number)
	case e.mergedElsewhere():
		return fmt.Sprintf("service: pull request %d reads merged at %s and the run observed %s, so it is not this run's merge",
			e.number, e.head, e.observed)
	default:
		return fmt.Sprintf("service: pull request %d reads %s, not merged", e.number, e.state)
	}
}

func (e *mergeNotRead) Unwrap() error { return errNoMergeRead }

// mergedElsewhere reports whether the pull request was read merged at a head
// other than the one the run observed, which is final for the run: a merged
// pull request does not reopen.
func (e *mergeNotRead) mergedElsewhere() bool {
	return e.read && e.state == forge.PullRequestStateMerged && e.head != e.observed
}

// readMergeAt reads the pull request with the given number through provider
// and returns the merge record's input when it reads merged at observed. Any
// other answer is a *mergeNotRead, and a read the provider refused is the
// provider's error, unchanged. An empty observed is refused before the code
// host is asked anything.
func readMergeAt(ctx context.Context, provider forge.Provider, number int, observed string) (store.PullRequestRead, forge.PullRequest, error) {
	if observed == "" {
		return store.PullRequestRead{}, forge.PullRequest{}, &mergeNotRead{number: number}
	}
	pr, err := provider.Get(ctx, number)
	if err != nil {
		return store.PullRequestRead{}, forge.PullRequest{}, err
	}
	if pr.State != forge.PullRequestStateMerged || pr.HeadCommit != observed {
		return store.PullRequestRead{}, pr, &mergeNotRead{number: number, observed: observed, read: true,
			state: pr.State, head: pr.HeadCommit}
	}
	return store.PullRequestRead{PullRequest: number, Head: pr.HeadCommit, End: store.PullRequestMerged}, pr, nil
}

// passedPullRequest is what the merge read takes off a passed run's final
// state: the repository on the code host, the pull request's number, and the
// head the run observed, which is the commit its push stage forwarded.
type passedPullRequest struct {
	specifier string
	number    int
	observed  string
}

// passedPullRequestOf reads the facts off a run's state, and reports false
// for a run that recorded no pull request on a code host, which has nothing
// to read. An observed head that was not recorded is the empty string, which
// readMergeAt refuses before reading.
func passedPullRequestOf(state graph.State) (passedPullRequest, bool) {
	specifier, number, err := recordedPullRequest(state)
	if err != nil {
		return passedPullRequest{}, false
	}
	observed := ""
	if v, ok := state.Get(string(pipeline.KeyPushed)); ok {
		observed, _ = v.Text()
	}
	return passedPullRequest{specifier: specifier, number: number, observed: observed}, true
}

// recordMergeRead is the read and the write for a run that passed: readMergeAt
// over provider, and on a merge at the observed head, the merge record through
// internal/runs' RecordMergeAfterPass, after which it cleans up what the merge
// landed and releases what it cleared, as settle does for a run its pull
// request's merge ended. It returns the pull request as read.
//
// What it refuses is returned rather than logged, so each caller says it in
// its own words: a *mergeNotRead for an answer readMergeAt does not record,
// the provider's refusal wrapped for a read that failed, and the store's
// refusal wrapped for a write it refused, store.ErrMergeRecorded among them
// when another read recorded the merge first.
func (s *Service) recordMergeRead(ctx context.Context, built *driver, record store.Run, provider forge.Provider,
	facts passedPullRequest) (forge.PullRequest, error) {
	read, pr, err := readMergeAt(ctx, provider, facts.number, facts.observed)
	var notRead *mergeNotRead
	switch {
	case errors.As(err, &notRead):
		return pr, err
	case err != nil:
		return pr, fmt.Errorf("pull request %d could not be read, so nothing was recorded: %w", facts.number, err)
	}
	if _, err := built.runs.RecordMergeAfterPass(ctx, record.ID, read); err != nil {
		return pr, fmt.Errorf("pull request %d was read merged and the record could not be written: %w", facts.number, err)
	}
	s.cleanUpLanded(ctx, record)
	s.releaseQueued(ctx)
	return pr, nil
}

// mergeReadResult is what one pass did about one run, for the log and the
// tests.
type mergeReadResult string

const (
	// mergeReadSkipped is a run passed over before the code host was opened:
	// no repository on the code host or no pull request number recorded, no
	// observed head, or a run an earlier pass of this service settled.
	mergeReadSkipped mergeReadResult = "skipped"
	// mergeReadRecorded is a merge at the observed head, recorded.
	mergeReadRecorded mergeReadResult = "recorded"
	// mergeReadNotMerged is a pull request read open, closed, or in a state
	// internal/forge does not recognize, which records nothing.
	mergeReadNotMerged mergeReadResult = "not merged"
	// mergeReadElsewhere is a pull request read merged at another head,
	// reported and not recorded.
	mergeReadElsewhere mergeReadResult = "merged at another head"
	// mergeReadAlready is a merge another read recorded first.
	mergeReadAlready mergeReadResult = "already recorded"
	// mergeReadFailed is a read, an open, or a write that failed, which
	// records nothing and moves nothing.
	mergeReadFailed mergeReadResult = "failed"
)

// startMergeReads starts the goroutine that runs readPersonMerges as the
// service opens and every mergeReadInterval after, under the service's own
// context, registered as background work so Close waits for the pass in
// flight, which the context ending cuts short.
func (s *Service) startMergeReads() {
	if !s.startWork() {
		return
	}
	go func() {
		defer s.work.Done()
		ticker := time.NewTicker(mergeReadInterval)
		defer ticker.Stop()
		for {
			s.readPersonMerges(s.stopCtx)
			select {
			case <-s.stopCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// readPersonMerges is one pass of the read on this file's terms, and returns
// what it did about each run it looked at.
func (s *Service) readPersonMerges(ctx context.Context) map[string]mergeReadResult {
	results := make(map[string]mergeReadResult)
	repositories, err := s.store.Repositories(ctx)
	if err != nil {
		s.log.Printf("the read for merges could not list repositories: %v", err)
		return results
	}
	for _, repository := range repositories {
		records, err := s.store.RunsForRepository(ctx, repository.ID)
		if err != nil {
			s.log.Printf("the read for merges could not list the runs of %s: %v", repository.ID, err)
			continue
		}
		for _, record := range records {
			if ctx.Err() != nil {
				return results
			}
			if result, ok := s.readPersonMerge(ctx, record); ok {
				results[record.ID] = result
			}
		}
	}
	return results
}

// readPersonMerge reads one run's pull request when the run is checks-passed,
// and reports false for a run that is not, which the pass does not count.
func (s *Service) readPersonMerge(ctx context.Context, record store.Run) (mergeReadResult, bool) {
	merge, ended, err := s.store.MergeRecord(ctx, record.ID)
	if err != nil {
		s.log.Printf("run %s: the read for merges could not read its merge record: %v", record.ID, err)
		return mergeReadFailed, true
	}
	if s.mergeReadSettled(record.ID) {
		return mergeReadSkipped, true
	}
	// Restore the checkpoint before OutcomeOf so a no-code-host completion
	// (and a record that predates NoCodeHost) is not mistaken for
	// checks-passed. The record's NoCodeHost flag is the durable half.
	latest, err := s.checkpoints.Latest(ctx, record.ID)
	if err != nil {
		s.log.Printf("run %s: the read for merges could not read its position: %v", record.ID, err)
		return mergeReadFailed, true
	}
	standing := machine.Standing{
		Record:     record.Status,
		Park:       record.Park,
		NoCodeHost: record.NoCodeHost,
		Execution:  machine.ExecutionAt(latest.Status, latest.State),
	}
	if ended {
		standing.PullRequestEnd = merge.End
	}
	if machine.OutcomeOf(standing) != machine.OutcomeChecksPassed {
		return "", false
	}
	facts, ok := passedPullRequestOf(latest.State)
	if !ok {
		// No repository on the code host, or no pull request: nothing to ask
		// it about, and the next pass finds the same state.
		return mergeReadSkipped, true
	}
	if facts.observed == "" {
		s.settleMergeRead(record.ID)
		s.log.Printf("run %s: %v", record.ID, &mergeNotRead{number: facts.number})
		return mergeReadSkipped, true
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		s.log.Printf("run %s: the read for merges could not build the run service, so nothing was read: %v", record.ID, err)
		return mergeReadFailed, true
	}
	provider, err := s.forge.Open(facts.specifier)
	if err != nil {
		s.log.Printf("run %s: the read for merges could not open the code host for %s: %v", record.ID, facts.specifier, err)
		return mergeReadFailed, true
	}
	bounded, cancel := context.WithTimeout(ctx, ChecksHoldReadBound)
	pr, err := s.recordMergeRead(bounded, built, record, provider, facts)
	cancel()
	var notRead *mergeNotRead
	switch {
	case err == nil:
		s.log.Printf("run %s: recorded the merge of %s at %s", record.ID, pr.URL, facts.observed)
		s.publishRunState(ctx, record.ID)
		return mergeReadRecorded, true
	case errors.As(err, &notRead) && notRead.mergedElsewhere():
		s.settleMergeRead(record.ID)
		s.log.Printf("run %s: %v; nothing was recorded", record.ID, err)
		return mergeReadElsewhere, true
	case errors.As(err, &notRead):
		return mergeReadNotMerged, true
	case errors.Is(err, store.ErrMergeRecorded):
		return mergeReadAlready, true
	default:
		s.log.Printf("run %s: %v", record.ID, err)
		return mergeReadFailed, true
	}
}

// mergeReadSettled reports whether an earlier pass of this service settled
// the run, so it is not read again.
func (s *Service) mergeReadSettled(run string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mergeReadsSettled[run]
}

// settleMergeRead remembers that the run is not to be read again by this
// service.
func (s *Service) settleMergeRead(run string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mergeReadsSettled == nil {
		s.mergeReadsSettled = make(map[string]bool)
	}
	s.mergeReadsSettled[run] = true
}
