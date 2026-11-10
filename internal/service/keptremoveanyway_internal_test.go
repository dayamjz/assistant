package service

import (
	"errors"
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

// refuseRemoveAnywayWhileBusy refuses land, carry-on, and discard claims
// and a standing set-aside directory (PRD section 6).
func TestRefuseRemoveAnywayWhileBusy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)

	now := time.Now().UTC()
	open := store.KeptWork{ID: 3, KeptAt: now}
	cases := []struct {
		name  string
		entry store.KeptWork
		want  string
	}{
		{
			name: "land claim",
			entry: store.KeptWork{
				ID: 1, KeptAt: now, LandingClaimedAt: store.Known(now),
			},
			want: "land claim",
		},
		{
			name: "carry-on claim",
			entry: store.KeptWork{
				ID: 2, KeptAt: now, CarriedOnTask: store.Known("carried"),
			},
			want: "carry-on claim",
		},
		{
			name: "discard claim",
			entry: store.KeptWork{
				ID: 4, KeptAt: now, DiscardClaimedAt: store.Known(now),
			},
			want: "discard claim",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := (&Service{}).refuseRemoveAnywayWhileBusy(c.entry)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refuse while %s = %v; want ErrRefused naming %q", c.name, err, c.want)
			}
		})
	}

	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatal(err)
	}
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
	open.CopyPath = copyPath
	err = (&Service{home: h}).refuseRemoveAnywayWhileBusy(open)
	if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "set aside") {
		t.Fatalf("refuse with a set-aside = %v; want ErrRefused naming the set-aside", err)
	}
}
