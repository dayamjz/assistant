package stages

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// RebaseRecords is the run rebase record, store.RunRebase, and the one route
// the rebase body has to it. internal/store's RecordRunRebase and RunRebases
// are the implementation.
//
// # Why the rebase stage records what it rewrote
//
// A rebase gives the commits it replays new identifiers, and the run then
// validates and publishes the new ones. Whoever still holds the old ones, the
// task copy whose worker pushed them most of all, holds commits no upstream
// will ever reach under those identifiers, and internal/tasks' cleanup may
// remove a copy only on a proof that its work is upstream (PRD principle P12).
// The stage is the one place that sees which head became which: it ran the
// rebase, and the rebase returned without a conflict. So it writes that down
// at that point, and the cleanup proofs follow the record rather than
// matching commits by what they hold, which would be an inference about two
// commits where this is an observation of one rewrite.
//
// A row is written only for a rebase the stage has established two things
// of, each in the run's copy, where every commit involved is present. The
// replayed head carries the commit the run was submitted: that commit is an
// ancestor of the head, or a head an earlier row of the run recorded is. And
// the rebased head holds what the replayed head held: integrating the
// replayed head into it would change nothing, by internal/safety's
// Incorporation, the content check P12's default-branch proof makes. The
// second is what a clean return from the rebase does not establish:
// rebase_record_test.go drives two replays that return without a conflict
// and leave work out, a submitted merge whose own result added a file and a
// commit whose change the base applied and then reverted. So every row says the run's own rebase carried the
// replayed head's content into its After, and a chain of rebases with fix
// rounds between them still yields a row from the submitted commit to the
// last rebased head.
//
// What it does not record: a rebase that conflicted, which is abandoned or
// left to a fix round, and whatever a fix round rewrote by hand, because
// neither is a replay this stage saw apply; a rebase that left the head
// where it was, which rewrote nothing; and a replay whose result the content
// check does not find holding what it replayed, or cannot decide about,
// which is a run that goes on as before with nothing recorded for it. A landing that went through either is
// one the cleanup proofs cannot follow by this record, so its copy is kept
// unless another proof holds: a missing record can keep a copy and never
// remove one.
//
// It is an interface because a stage body opens no database, per the rule
// that internal/store is the only package that does; internal/service hands
// over the store it already opened.
type RebaseRecords interface {
	// RecordRunRebase writes one run rebase record, on store's terms.
	RecordRunRebase(ctx context.Context, r store.RunRebase) (store.RunRebase, error)
	// RunRebases returns the run's rebase records, oldest first.
	RunRebases(ctx context.Context, run string) ([]store.RunRebase, error)
}

// WithRebaseRecords returns these dependencies with the record the rebase
// body writes each rebase it made to. internal/service is what calls it,
// with the store it opened for the home. A StageDeps without one records
// nothing, which leaves a rebased landing to the proofs that need no record:
// it keeps a copy that a record would have let go, and never the reverse.
func (d StageDeps) WithRebaseRecords(r RebaseRecords) StageDeps {
	d.rebases = r
	return d
}

// rebaseRecorder is what each of the rebase body's three replays records a
// clean replay through: the record, the run's copy the ancestry is read in,
// the run, and the commit it was submitted.
type rebaseRecorder struct {
	records   RebaseRecords
	copied    *vcs.Repository
	run       string
	submitted string
}

// recorderOf is the recorder for one execution of the rebase body.
func recorderOf(deps StageDeps, copied *vcs.Repository, run, submitted string) rebaseRecorder {
	return rebaseRecorder{records: deps.rebases, copied: copied, run: run, submitted: submitted}
}

// record writes that a replay of before onto onto applied and left the copy
// at after, when before carries the submitted commit and after holds what
// before held, and writes nothing for a replay that left the head where it
// was, for one whose result the content check does not establish holds
// before's content, or for a recorder with no record. An error is a record
// that could not be read or written, or ancestry that could not be
// established, and the body returns it rather than carrying on with a
// rewrite nothing recorded.
func (r rebaseRecorder) record(ctx context.Context, before, onto, after string) error {
	if r.records == nil || before == after {
		return nil
	}
	carries, err := r.carries(ctx, before)
	if err != nil {
		return err
	}
	if !carries {
		return nil
	}
	kept, err := r.holds(ctx, before, after)
	if err != nil || !kept {
		return err
	}
	if _, err := r.records.RecordRunRebase(ctx, store.RunRebase{
		Run: r.run, Submitted: r.submitted, Before: before, Onto: onto, After: after,
	}); err != nil {
		return fmt.Errorf("stages: recording that %s was rebased onto %s as %s: %w", before, onto, after, err)
	}
	return nil
}

// holds reports whether after holds what before held: integrating before
// into after would change nothing. A comparison internal/safety could not
// make, a conflict among them, is not that, and answers false; it is no
// failure of the stage, whose run goes on with nothing recorded for the
// replay.
func (r rebaseRecorder) holds(ctx context.Context, before, after string) (bool, error) {
	result, err := safety.New(r.copied).Incorporation(ctx, []string{before}, after)
	switch {
	case errors.Is(err, safety.ErrNotEstablished):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("stages: establishing whether %s holds what %s held: %w", after, before, err)
	}
	return result.Incorporated(), nil
}

// carries reports whether before carries the submitted commit: the commit
// is its ancestor, or a head an earlier rebase of this run recorded is. A
// commit the copy does not hold is not carried, and any other failure is
// returned.
func (r rebaseRecorder) carries(ctx context.Context, before string) (bool, error) {
	candidates := []string{r.submitted}
	earlier, err := r.records.RunRebases(ctx, r.run)
	if err != nil {
		return false, fmt.Errorf("stages: reading the rebases run %s recorded: %w", r.run, err)
	}
	for _, e := range earlier {
		candidates = append(candidates, e.After)
	}
	for _, c := range candidates {
		ok, err := r.copied.IsAncestor(ctx, c, before)
		switch {
		case errors.Is(err, vcs.ErrRefNotFound):
			continue
		case err != nil:
			return false, fmt.Errorf("stages: establishing whether %s carries %s: %w", before, c, err)
		case ok:
			return true, nil
		}
	}
	return false, nil
}
