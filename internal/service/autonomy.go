package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/store"
)

// Merge authority: the merge half of PRD section 6's autonomy flag.
//
// # When the program merges
//
// Only a run that ended checks-passed - passed, with no pull request read
// ending it - in a repository the operator's global-only autonomy.merge
// lists, by the specifier the code host names it with. Nothing else reaches
// the code host here: a repository nobody listed is never asked anything,
// and a run that passed with no pull request recorded has nothing to merge.
//
// # The live read, bound to the observed head
//
// Before the merge, the pull request is read by the number the pull request
// stage recorded and must be open, not a draft, reported mergeable by the
// forge, and standing at the head the run observed, which is the commit its
// push stage forwarded; and the checks are read and must report that same
// head and evaluate passed, an empty list counting only under the run's
// no_ci declaration, re-read from the trusted copy as a run reads it. Any
// other answer, and any read that fails, is a refusal and no merge is asked.
// The merge itself is forge.Provider.Merge bound to that head (P6), so a
// commit pushed between the read and the merge is not merged.
//
// # The forge decides, and the merge writes nothing
//
// Whether the merge is allowed is the code host's: a refusal from it is
// reported in the service's log as what it is and nothing is retried, with
// another method or without the binding. The merge writes no record. A
// code-host read after it writes the merge record when it reads the pull
// request merged at the head the run observed, and the run reads passed from
// then on; a read that does not records nothing. That read is mergeread.go's
// recordMergeRead, the same one a person's merge is read with, so the
// program's merge and a person's are recorded on one rule; when mergeread.go's
// own pass reads this merge first, the record it wrote is the one record.
// The one-line outcome PRD section 6 asks for is the log line naming the
// pull request's URL as that read reported it.
//
// # Residual gaps
//
//   - The merge is attempted once, as the run settles. A refusal is not
//     re-asked later, by a restart or otherwise, and the run stays
//     checks-passed for a person to merge.
//   - A merge the code host accepted and the read after it could not
//     record is left to mergeread.go's next pass, which reads it like a
//     person's merge.
//   - The outcome line is the service log's; no surface phrases it from the
//     record yet.
//   - Which merge method is used is internal/forge's GitHub adapter's
//     choice among those the repository permits, stated there.

// errMergeNotReady is the class of every refusal the live read makes.
var errMergeNotReady = errors.New("service: the pull request is not ready for the program to merge")

// mergeNotReady is one refusal of the live read, naming what it found.
type mergeNotReady struct {
	number int
	why    string
}

func (e *mergeNotReady) Error() string {
	return fmt.Sprintf("service: pull request %d was not merged by the program: %s", e.number, e.why)
}

func (e *mergeNotReady) Unwrap() error { return errMergeNotReady }

// mergeAfterLiveRead is the live read and the bound merge, over one provider,
// for the pull request with the given number at observed. It returns the
// pull request as the merge answered it, or the refusal: a *mergeNotReady
// for an answer the read does not accept, and the provider's own refusal,
// unchanged, for a read or a merge it refused.
func mergeAfterLiveRead(ctx context.Context, provider forge.Provider, number int, observed string,
	noCI forge.NoCIDeclaration) (forge.PullRequest, error) {
	refuse := func(format string, args ...any) error {
		return &mergeNotReady{number: number, why: fmt.Sprintf(format, args...)}
	}
	if observed == "" {
		return forge.PullRequest{}, refuse("the run recorded no head it observed, so a merge could not be bound to one")
	}
	pr, err := provider.Get(ctx, number)
	if err != nil {
		return forge.PullRequest{}, err
	}
	switch {
	case pr.State != forge.PullRequestStateOpen:
		return forge.PullRequest{}, refuse("it reads %s, not open", pr.State)
	case pr.Draft:
		return forge.PullRequest{}, refuse("it is a draft")
	case pr.Mergeability != forge.MergeabilityMergeable:
		return forge.PullRequest{}, refuse("the forge reports it %s, not mergeable", pr.Mergeability)
	case pr.HeadCommit != observed:
		return forge.PullRequest{}, refuse("its head is %s and the run observed %s", pr.HeadCommit, observed)
	}
	report, err := provider.Checks(ctx, number)
	if err != nil {
		return forge.PullRequest{}, err
	}
	if report.HeadCommit != observed {
		return forge.PullRequest{}, refuse("its checks were read against %s and the run observed %s", report.HeadCommit, observed)
	}
	if result := report.Evaluate(noCI); result.Verdict != forge.VerdictPassed {
		return forge.PullRequest{}, refuse("its checks on %s are %s, not passed", observed, result.Verdict)
	}
	return provider.Merge(ctx, number, observed)
}

// mergeUnderAutonomy is called for a run settle moved to passed with no pull
// request read ending it, and merges it on this file's terms when its
// repository is listed. It returns nil for a run it does not act on, before
// anything is read, and the refusal, which settle logs, for one it refused;
// on a recorded merge it cleans up what the merge landed and releases what it
// cleared, as settle does for a run its pull request's merge ended.
func (s *Service) mergeUnderAutonomy(ctx context.Context, built *driver, record store.Run, facts passedPullRequest) error {
	if !s.cfg.MergesIn(facts.specifier) {
		return nil
	}
	resolved, err := s.resolveRunConfig(ctx, record)
	if err != nil {
		return fmt.Errorf("service: run %s was not merged, because its configuration could not be read: %w", record.ID, err)
	}
	provider, err := s.openCodeHost(ctx, record.RepositoryID, facts.specifier)
	if err != nil {
		return fmt.Errorf("service: run %s was not merged: %w", record.ID, err)
	}
	number := facts.number
	if _, err := mergeAfterLiveRead(ctx, provider, number, facts.observed, forge.DeclaredNoCI(resolved.cfg)); err != nil {
		return fmt.Errorf("service: run %s: %w", record.ID, err)
	}
	read, err := s.recordMergeRead(ctx, built, record, provider, facts)
	switch {
	case errors.Is(err, store.ErrMergeRecorded):
		// The person-merge read in mergeread.go read the merge first and
		// recorded it, and cleaned up after it; the record is written once.
	case err != nil:
		return fmt.Errorf("service: run %s: the forge accepted the merge of pull request %d, and %w", record.ID, number, err)
	}
	s.log.Printf("run %s: merged %s under %s", record.ID, read.URL, config.KeyAutonomyMerge)
	return nil
}
