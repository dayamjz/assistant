package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// A promotion window keep reads an investigation copy as every keep does:
// an unchanged copy holds nothing the default branch lacks and writes no
// entry (NothingKept), while a copy with a commit or a file no commit
// records is one entry naming what it holds, written once however often it
// is asked. A delivery task's copy and a run's are refused, so the reason
// names only the copy PRD section 6 keeps for a promotion, and so is a keep
// for a worker that ended inside the window. The unchanged
// case is the control for the other two: the same keep of the same fixture
// with work in it does write.
func TestAPromotionWindowKeepRecordsWorkAndNotAnUnchangedCopy(t *testing.T) {
	ctx := context.Background()
	window := func(id string) Kept {
		return Kept{Task: id, Reason: KeptPromotionWindow, Detail: "the window passed"}
	}
	t.Run("an unchanged copy keeps nothing and writes nothing", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		s.storeReport(t, tk.id)
		got, err := s.cleaner.Keep(ctx, window(tk.id))
		if err != nil || !got.NothingKept || got.Recorded || got.Entry.ID != 0 {
			t.Fatalf("keeping an unchanged copy answered %+v (%v), want nothing kept and no entry", got, err)
		}
		if open, err := s.store.OpenKeptWork(ctx); err != nil || len(open) != 0 {
			t.Fatalf("an unchanged copy left kept work %+v (%v), want none", open, err)
		}
	})
	t.Run("a commit is one entry, written once", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		s.storeReport(t, tk.id)
		head := commit(t, tk.copy, "g.txt", "investigated\n", "investigated")
		first, err := s.cleaner.Keep(ctx, window(tk.id))
		if err != nil || first.NothingKept || !first.Recorded || first.Entry.Reason != KeptPromotionWindow ||
			first.Entry.TaskID.Or("") != tk.id || !slices.Contains(first.Entry.Holds, head) {
			t.Fatalf("keeping a copy with a commit answered %+v (%v), want an entry holding %s", first, err, head)
		}
		again, err := s.cleaner.Keep(ctx, window(tk.id))
		if err != nil || again.Recorded || again.NothingKept || again.Entry.ID != first.Entry.ID {
			t.Fatalf("keeping the same copy again answered %+v (%v), want entry %d found and not written", again, err, first.Entry.ID)
		}
		if open, err := s.store.OpenKeptWork(ctx); err != nil || len(open) != 1 {
			t.Fatalf("the copy left kept work %+v (%v), want one entry", open, err)
		}
	})
	t.Run("a file no commit records is kept", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		s.storeReport(t, tk.id)
		if err := os.WriteFile(filepath.Join(tk.copy, "notes.txt"), []byte("found\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := s.cleaner.Keep(ctx, window(tk.id))
		if err != nil || got.NothingKept || !got.Recorded {
			t.Fatalf("keeping a copy with an untracked file answered %+v (%v), want an entry", got, err)
		}
	})
	for _, reason := range []string{KeptPromotionWindow, KeptPromotionWorkerEnded} {
		t.Run("a delivery task's copy is refused for "+reason, func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			tk := s.deliveryTask(t, "task/window")
			if got, err := s.cleaner.Keep(ctx, Kept{Task: tk.id, Reason: reason}); !errors.Is(err, ErrCleanupRecord) {
				t.Fatalf("keeping a delivery copy for %s answered %+v (%v), want ErrCleanupRecord", reason, got, err)
			}
		})
		t.Run("a run's copy is refused for "+reason, func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			if got, err := s.cleaner.Keep(ctx, Kept{Run: "run-1", Reason: reason}); !errors.Is(err, ErrCleanupRecord) {
				t.Fatalf("keeping a run's copy for %s answered %+v (%v), want ErrCleanupRecord", reason, got, err)
			}
		})
	}
	t.Run("a worker ended inside the window is one entry", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		s.storeReport(t, tk.id)
		head := commit(t, tk.copy, "g.txt", "investigated\n", "investigated")
		got, err := s.cleaner.Keep(ctx, Kept{Task: tk.id, Reason: KeptPromotionWorkerEnded, Detail: "the worker ended"})
		if err != nil || !got.Recorded || got.Entry.Reason != KeptPromotionWorkerEnded || !slices.Contains(got.Entry.Holds, head) {
			t.Fatalf("keeping a copy whose worker ended inside the window answered %+v (%v), want an entry holding %s", got, err, head)
		}
	})
}
