package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD section 6's "nothing left to deliver": what the rebase stage records
// beside section 5's empty-diff short circuit, and the one reader of it.
//
// An empty difference after the replay is not proof that the run's work is
// on the default branch. The rebase skips a commit whose patch matches one
// the default branch gained since the branch left it, even when the default
// branch reverted that one later, and the replay is then clean and empty
// while the default branch does not hold the change. So when this stage
// takes the short circuit it also asks P12's content check, internal/safety's
// incorporation, of the commits the run was submitted with that the
// default-branch commit it read does not reach, against that commit, and
// records the commit and the answer in pipeline.KeyDiffEmptyCheck in the
// same write as pipeline.KeyDiffEmpty. NothingToDeliverOf reads them back,
// with the push anchor the same execution recorded, from a run's position,
// so whatever decides on them reads the checkpoint and never a report, the
// run's status, or its outcome.
//
// Only the two paths of the first pass that rebase onto the default branch
// ask it: the replay, and a head already standing on the default branch.
// The path that incorporates a branch somebody already published records no
// check, because a branch present where the run would push never lands this
// way, and the pass after a push never takes the short circuit.

// The closed set of answers the check records.
const (
	// checkPresent is every examined commit present by content in the
	// default-branch commit read: integrating each into it would change
	// nothing.
	checkPresent = "present"
	// checkAbsent is at least one examined commit that integrating into the
	// commit read would change.
	checkAbsent = "absent"
	// checkUnanswered is a check that could not answer: no commit of the
	// run's own to examine, or a comparison internal/safety could not make.
	checkUnanswered = "unanswered"
)

// emptyCheckRecord is the durable form of the check as it travels in run
// state, under names that will not change when this file is refactored,
// because what is written here has to be readable by the build that reads
// the checkpoint back.
type emptyCheckRecord struct {
	Against string   `json:"against"`
	Commits []string `json:"commits,omitempty"`
	Answer  string   `json:"answer"`
	Detail  string   `json:"detail,omitempty"`
}

// askEmptyCheck asks the content check of the commits submitted holds that
// against does not reach, against against, and returns the value
// pipeline.KeyDiffEmptyCheck records. A branch with no such commit is no
// answer, because an empty set of commits is not one internal/safety
// examines; a comparison it could not make is no answer either, and both are
// recorded with what was found rather than refusing the stage. An error is a
// repository that could not be read at all.
func askEmptyCheck(ctx context.Context, copied *vcs.Repository, submitted, against string) (graph.Value, error) {
	rec := emptyCheckRecord{Against: against}
	commits, err := copied.CommitsNotIn(ctx, submitted, against)
	if err != nil {
		return graph.Value{}, fmt.Errorf("stages: listing the commits %s holds that %s does not: %w", submitted, against, err)
	}
	switch {
	case len(commits) == 0:
		rec.Answer = checkUnanswered
		rec.Detail = fmt.Sprintf("%s holds no commit of its own beyond %s, so there is no work to look for", submitted, against)
	default:
		rec.Commits = commits
		result, err := safety.New(copied).Incorporation(ctx, commits, against)
		switch {
		case errors.Is(err, safety.ErrNotEstablished):
			rec.Answer, rec.Detail = checkUnanswered, err.Error()
		case err != nil:
			return graph.Value{}, fmt.Errorf("stages: checking whether %s holds the submitted work: %w", against, err)
		case result.Incorporated():
			rec.Answer, rec.Commits = checkPresent, result.Commits()
		default:
			rec.Answer, rec.Commits = checkAbsent, result.Commits()
			rec.Detail = "integrating them into " + result.Into() + " would change it"
		}
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		return graph.Value{}, fmt.Errorf("stages: recording the content check against %s: %w", against, err)
	}
	return graph.TextValue(string(encoded)), nil
}

// NothingToDeliver is what a run's recorded position says about PRD section
// 6's "nothing left to deliver", read by NothingToDeliverOf. It decides
// nothing about the task the run belongs to; Lands says whether the
// position carries every fact the section asks of a run that writes the
// nothing-to-deliver record.
//
// It is exported for internal/service, which writes that record in the
// same write that ends the run, and whose one reader of these keys this is.
type NothingToDeliver struct {
	// ShortCircuit is whether the rebase stage took section 5's empty-diff
	// short circuit, pipeline.KeyDiffEmpty.
	ShortCircuit bool
	// Against is the default-branch commit the rebase stage read at its
	// fresh fetch, and empty when it recorded no check.
	Against string
	// Submitted is the commit the run was submitted with.
	Submitted string
	// Examined is the commits of the run's own the content check examined,
	// as it resolved them, and empty when it examined none.
	Examined []string
	// Present is whether the content check answered that every examined
	// commit is present by content in Against.
	Present bool
	// Unpublished is whether the push anchor the same pass observed found
	// the branch absent where the run would push it.
	Unpublished bool
	// Pushed is the commit the run's push stage forwarded, empty when it
	// pushed nothing.
	Pushed string
	// Detail is what the check recorded about an answer other than present,
	// and empty for present.
	Detail string
}

// Lands reports whether the position is one PRD section 6 has write the
// nothing-to-deliver record: the short circuit taken, before any push, of a
// branch the anchor found absent, with every commit of the run's own found
// present by content in the default-branch commit read. why says which of
// those is missing when it is not.
func (n NothingToDeliver) Lands() (lands bool, why string) {
	switch {
	case !n.ShortCircuit:
		return false, "the run did not end on the empty-diff short circuit"
	case n.Pushed != "":
		return false, "the run pushed " + n.Pushed + " before its rebase left nothing to push"
	case n.Against == "":
		return false, "the rebase stage recorded no content check beside the short circuit"
	case !n.Unpublished:
		return false, "the branch was already published where the run would push it"
	case !n.Present:
		return false, "the content check did not find the submitted work present in " + n.Against + ": " + n.Detail
	}
	return true, ""
}

// NothingToDeliverOf reads what s, a run's recorded position, says about
// nothing left to deliver: the short circuit, the content check recorded
// beside it, the push anchor, the push, and the submitted commit. A key the
// position does not hold reads as its zero value, which is how a run that
// never wrote it behaved. An error is a recorded check or anchor that cannot
// be read back, which a caller treats as no record rather than as one.
func NothingToDeliverOf(s graph.State) (NothingToDeliver, error) {
	text := func(key pipeline.Key) string {
		v, ok := s.Get(string(key))
		if !ok {
			return ""
		}
		t, _ := v.Text()
		return t
	}
	var out NothingToDeliver
	if v, ok := s.Get(string(pipeline.KeyDiffEmpty)); ok {
		out.ShortCircuit, _ = v.Bool()
	}
	out.Submitted, out.Pushed = text(pipeline.KeySubmitted), text(pipeline.KeyPushed)
	if checked := text(pipeline.KeyDiffEmptyCheck); checked != "" {
		var rec emptyCheckRecord
		if err := json.Unmarshal([]byte(checked), &rec); err != nil {
			return NothingToDeliver{}, fmt.Errorf("stages: the content check this run recorded cannot be read: %w", err)
		}
		if rec.Against == "" || !slices.Contains([]string{checkPresent, checkAbsent, checkUnanswered}, rec.Answer) ||
			(rec.Answer == checkPresent && len(rec.Commits) == 0) {
			return NothingToDeliver{}, fmt.Errorf("stages: the content check this run recorded is not one this stage writes: %s", checked)
		}
		out.Against, out.Examined, out.Detail = rec.Against, rec.Commits, rec.Detail
		out.Present = rec.Answer == checkPresent
	}
	anchor, recorded, err := decodeAnchor(text(pipeline.KeyPushAnchor))
	if err != nil {
		return NothingToDeliver{}, err
	}
	out.Unpublished = recorded && !anchor.State().Exists
	return out, nil
}
