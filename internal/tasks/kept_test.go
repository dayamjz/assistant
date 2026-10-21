package tasks

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/store"
)

// keptForWork is which refusals keep a copy for the work in it, written from
// PRD section 6's "kept because its work has not landed" one reason at a
// time rather than from Unlanded's switch: no proof held, or the tree holds
// what no commit records. Every other reason is a fact not yet established,
// which the next cleanup asks again.
var keptForWork = map[CleanupReason]bool{
	ReasonUnproven:           true,
	ReasonCopyDirty:          true,
	ReasonCopyUntracked:      true,
	ReasonCopyStash:          true,
	ReasonTerminalUnobserved: false,
	ReasonTerminalLive:       false,
	ReasonCopyChanged:        false,
	ReasonReportUnstored:     false,
	ReasonHoldUnregistered:   false,
	ReasonCopyMissing:        false,
	ReasonShapeMoved:         false,
}

// Every reason a refusal can carry is placed, so a reason added later fails
// here until somebody decides whether it is kept work, rather than reading
// as not kept by default; and KeptOf carries the refusal's owner, reason and
// every proof's account for the reasons that are, and nothing for the rest.
func TestEveryRefusalIsPlacedAsKeptWorkOrNot(t *testing.T) {
	t.Parallel()
	for reason := range remedies {
		want, placed := keptForWork[reason]
		if !placed {
			t.Errorf("%s is not placed as kept work or not", reason)
			continue
		}
		refusal := &CleanupRefusal{Run: "run-1", Reason: reason, Detail: "found",
			Attempts: []ProofAttempt{{Proof: store.CleanupProofRemoteBranch, Detail: "not on the branch"}}}
		if got := refusal.Unlanded(); got != want {
			t.Errorf("%s reads Unlanded %v, want %v", reason, got, want)
		}
		kept, ok := KeptOf(refusal)
		switch {
		case ok != want:
			t.Errorf("KeptOf(%s) answered %v, want %v", reason, ok, want)
		case ok && (kept.Run != "run-1" || kept.Task != "" || kept.Reason != string(reason) ||
			!strings.Contains(kept.Detail, "found") || !strings.Contains(kept.Detail, string(store.CleanupProofRemoteBranch)+": not on the branch")):
			t.Errorf("KeptOf(%s) = %+v, want the refusal's owner, reason and every attempt", reason, kept)
		}
	}
	if len(keptForWork) != len(remedies) {
		t.Errorf("the placement names %d reasons and the refusals carry %d", len(keptForWork), len(remedies))
	}
	if _, ok := KeptOf(nil); ok {
		t.Error("KeptOf of no refusal answered a kept copy")
	}
}
