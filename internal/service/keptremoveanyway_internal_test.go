package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// refusedLoss names --answer remove-anyway for the layout/loss refusals
// remove-anyway can answer (PRD section 9).
func TestRefusedLossNamesRemoveAnyway(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)

	entry := store.KeptWork{ID: 7}
	for _, cause := range []error{
		tasks.ErrLayoutUnsupported,
		tasks.ErrLossUnestablished,
		tasks.ErrCopyGone,
		tasks.ErrRecordNotTheCopys,
	} {
		err := refusedLoss(entry, cause)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "remove-anyway") {
			t.Fatalf("refusedLoss(%v) = %v; want ErrRefused naming remove-anyway", cause, err)
		}
	}
}

// refuseRemoveAnywayWhileBusy refuses while a land, carry-on or discard
// claim stands on the entry, while any claim stands on another open entry
// of its copy, and while a discard set-aside stands beside the copy (PRD
// section 6), over the real store.
func TestRefuseRemoveAnywayWhileBusy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)

	now := time.Now().UTC()
	for _, c := range []struct {
		name  string
		entry store.KeptWork
		want  string
	}{
		{"land claim", store.KeptWork{ID: 1, KeptAt: now, LandingClaimedAt: store.Known(now)}, "land claim"},
		{"carry-on claim", store.KeptWork{ID: 2, KeptAt: now, CarriedOnTask: store.Known("carried")}, "carry-on claim"},
		{"discard claim", store.KeptWork{ID: 4, KeptAt: now, DiscardClaimedAt: store.Known(now)}, "discard claim"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := cleanupService(t)
			c.entry.CopyPath = "/nowhere/copy"
			err := s.refuseRemoveAnywayWhileBusy(t.Context(), c.entry)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refuse while %s = %v; want ErrRefused naming %q", c.name, err, c.want)
			}
		})
	}

	t.Run("a claim on another entry of the copy", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		s, records, _ := cleanupService(t)
		claimed := plantKeptRun(t, records, "run-claimed-on-the-copy", "work")
		other, wrote, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: "run-claimed-on-the-copy",
			CopyPath: claimed.CopyPath, Holds: []string{"cccc"}, Reason: "unproven", Against: "dddd"})
		if err != nil || !wrote || other.ID == claimed.ID {
			t.Fatalf("recording a second entry of the copy = %+v, %v, %v; want a new entry", other, wrote, err)
		}
		if err := s.refuseRemoveAnywayWhileBusy(ctx, other); err != nil {
			t.Fatalf("with no claim on the copy = %v; want no refusal", err)
		}
		if _, err := records.ClaimKeptWorkLanding(ctx, claimed.ID, store.ResolvedByPerson()); err != nil {
			t.Fatal(err)
		}
		err = s.refuseRemoveAnywayWhileBusy(ctx, other)
		if want := fmt.Sprintf("a claim stands on kept work entry %d", claimed.ID); !errors.Is(err, ipc.ErrRefused) ||
			!strings.Contains(err.Error(), want) {
			t.Fatalf("with a land claim on the copy's other entry = %v; want ErrRefused naming %q", err, want)
		}
	})

	t.Run("a set-aside beside the copy", func(t *testing.T) {
		t.Parallel()
		s, _, h := cleanupService(t)
		id, err := home.NewTaskID()
		if err != nil {
			t.Fatal(err)
		}
		copyPath, err := h.TaskCopy(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(copyPath, 0o700); err != nil {
			t.Fatal(err)
		}
		dir, err := h.Discarding(copyPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		err = s.refuseRemoveAnywayWhileBusy(t.Context(), store.KeptWork{ID: 3, KeptAt: now, CopyPath: copyPath})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "set aside") {
			t.Fatalf("refuse with a set-aside = %v; want ErrRefused naming the set-aside", err)
		}
	})
}

// A remove-anyway claim owed at open whose re-check fails without the
// refusal clearing stays owed for the next open, not taken back (PRD
// section 6, remove anyway's What it does): only a refusal that no longer
// holds takes it back. The planted run's repository is bound to no gate,
// so the re-check cannot read the copy at all.
func TestAnOwedRemoveAnywayClaimStaysOwedWhenItsRecheckFails(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	ctx := t.Context()
	s, records, h := cleanupService(t)
	entry := plantKeptRun(t, records, "run-owed-remove-anyway", "work")
	if _, err := records.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "discard refused: planted", nil, store.ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	s.completeRemoveAnywaysAtOpen(ctx)
	if still, err := records.KeptWorkEntry(ctx, entry.ID); err != nil || !still.RemoveAnywayClaimed() {
		t.Fatalf("after a failed re-check the entry reads %+v, %v; want it open with the claim still owed", still, err)
	}
	if got := serviceLog(t, h); !strings.Contains(got, fmt.Sprintf("completing remove-anyway of kept work entry %d", entry.ID)) {
		t.Errorf("the service log reads %q; want the failed completion reported", got)
	}
}
