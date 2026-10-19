package pipeline

import (
	"encoding/json"
	"fmt"

	"github.com/dayamjz/assistant/internal/graph"
)

// The path back from the checks stage to review: PRD section 5's repair of a
// red CI run, as the owner decided it on #325. A check that fails after the
// push goes to the checks stage's own fix round, which commits a repair in the
// run's isolated copy, and the stage, running again on that commit, sends the
// run back to the review stage with it. The repaired head is then reviewed in
// a fresh session, passes the test, document and lint stages, is pushed a
// second time anchored on the commit the run last pushed, goes through the
// pull request stage, and is checked again.
//
// It is a path between existing stages and not a tenth stage, on the same
// terms as the path back to rebase in repush.go. The two differ in where they
// land and in what bounds them. A conflict needs the branch rebased, so it
// goes back to rebase and is bounded by RebaseAfterPushRounds; a repair is a
// commit on top of the one the run pushed, which a rebase has nothing to do
// for, so it goes back to the review stage, the first stage that reads what
// the change is, and it is bounded by the checks stage's own fix round limit,
// because every repair is one of that stage's rounds and the owner decided
// the round limit bounds repairs.

// repairAfterPushEdge is the back edge a stage whose row says
// Stage.RepairsAfterPush takes when its outcome is OutcomeRepair. It carries
// the stage's fix round limit, rounds, which is above zero wherever wire
// builds it.
//
// The bound is the same number the edge into the stage's fixer carries. Every
// traversal of this edge follows one entry into the fixer, which that edge
// counted first, and a resubmission of the same repair without another fix
// round is counted the same way: the checks stage reports it as another fix
// finding rather than taking this edge again, so a run whose repairs keep
// failing parks rounds-exhausted in front of the fixer before this edge could
// reach its own bound. The graph requires a number on every back edge, and
// this is the one that cannot fire first under that counting.
//
// The convergence bound applies here as to every back edge that is not
// answered, and the run-wide step budget counts every node the path runs.
func repairAfterPushEdge(stage Stage, rounds int) graph.Edge {
	e := guarded(stage.Node(), stage, OutcomeRepair, StageReview.Node())
	e.Rounds = rounds
	return e
}

// repairRounds is the fix round limit of the stage that repairs after the
// push, from limits, which New computed in stage table order. It is zero when
// that stage takes no rounds, and then wire builds no path back to review.
func repairRounds(limits []int) int {
	for i, row := range stageTable {
		if row.repairsAfterPush {
			return limits[i]
		}
	}
	return 0
}

// Repair is one traversal of the path back to review, as KeyRepairs recorded
// it and RepairsOf reads it.
type Repair struct {
	// Pushed is the commit the run had pushed when the repair was sent back:
	// what the pull request stood on while its checks failed.
	Pushed string `json:"pushed"`
	// Repaired is the head the checks stage's fix round left, which the run
	// took back to the review stage.
	Repaired string `json:"repaired"`
	// Republished says the push stage has since forwarded a commit other than
	// Pushed. The push stage pushes only behind a completed review, and the
	// path a repair takes reaches the push stage only through the review
	// stage, so a republished repair went through review again before it was
	// pushed. It is derived when RepairsOf reads the record and not stored:
	// for an entry with a later one it is whether that one's Pushed differs,
	// and for the last it is whether KeyPushed does.
	//
	// It does not say the pushed commit is Repaired. A fix round of the test or
	// lint stage on the way, or a rebase after a later conflict, puts another
	// commit on top of it or replays it, and the push stage reviews those on
	// its own terms.
	Republished bool `json:"republished"`
}

// repairEntry is the durable form of one repair, the two commits under names
// that will not change when Repair is refactored.
type repairEntry struct {
	Pushed   string `json:"pushed"`
	Repaired string `json:"repaired"`
}

// repairValue encodes one entry as the item the stage node appends to
// KeyRepairs.
func repairValue(pushed, repaired string) (graph.Value, error) {
	encoded, err := json.Marshal(repairEntry{Pushed: pushed, Repaired: repaired})
	if err != nil {
		return graph.Value{}, fmt.Errorf("pipeline: recording the repair of %s: %w", pushed, err)
	}
	return graph.ListValue(string(encoded)), nil
}

// RepairAlreadySent reports whether the run already recorded sending the same
// pushed and repaired commits back to review. The checks stage asks before it
// takes the path again, so a resubmission without another fix round counts
// toward the fix round limit instead of appending a second record and
// traversing this edge again.
func RepairAlreadySent(r Reader, pushed, repaired string) (bool, error) {
	if pushed == "" || repaired == "" {
		return false, nil
	}
	value, err := r.Get(KeyRepairs)
	if err != nil {
		return false, err
	}
	items, ok := value.List()
	if !ok {
		return false, nil
	}
	for _, item := range items {
		var entry repairEntry
		if err := json.Unmarshal([]byte(item), &entry); err != nil {
			return false, fmt.Errorf("%w: repair already sent: %w", ErrBadReport, err)
		}
		if entry.Pushed == pushed && entry.Repaired == repaired {
			return true, nil
		}
	}
	return false, nil
}

// RepairsOf reads every repair the run sent back to review, in the order they
// were taken, and nil for a run that sent none, which is every run whose state
// does not declare KeyRepairs. An entry that cannot be decoded is refused with
// ErrBadReport rather than skipped, because a skipped entry would present a
// run that repaired something as one that did not.
func RepairsOf(s graph.State) ([]Repair, error) {
	value, ok := s.Get(string(KeyRepairs))
	if !ok {
		return nil, nil
	}
	items, _ := value.List()
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]Repair, 0, len(items))
	for i, item := range items {
		var entry repairEntry
		if err := json.Unmarshal([]byte(item), &entry); err != nil {
			return nil, fmt.Errorf("%w: repair %d: %w", ErrBadReport, i+1, err)
		}
		if entry.Pushed == "" || entry.Repaired == "" {
			return nil, fmt.Errorf("%w: repair %d names no commit", ErrBadReport, i+1)
		}
		out = append(out, Repair{Pushed: entry.Pushed, Repaired: entry.Repaired})
	}
	pushed := ""
	if v, ok := s.Get(string(KeyPushed)); ok {
		pushed, _ = v.Text()
	}
	for i := range out {
		next := pushed
		if i+1 < len(out) {
			next = out[i+1].Pushed
		}
		out[i].Republished = next != "" && next != out[i].Pushed
	}
	return out, nil
}
