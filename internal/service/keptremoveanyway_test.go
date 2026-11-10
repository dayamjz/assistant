package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// Fail-before: remove-anyway was not an answer. After: a shallow kept
// clone is shown and then released with --force, keeping the copy; a
// discardable copy is refused naming discard (PRD section 6; P6/P12/P15).
func TestARemoveAnywayReleasesAShallowKeptCopyAndKeepsIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	// Shallow the clone so Loss refuses (layout unsupported) and
	// remove-anyway is offered.
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil ||
			shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" {
			t.Fatalf("remove-anyway shown = %+v, %v; want remove-anyway-shown with a refusal", shown, err)
		}
		if shown.Loss != nil {
			t.Fatalf("shown loss = %+v; want none, since a discard that refuses returns no loss", shown.Loss)
		}

		request.Force = true
		var done machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil ||
			done.Effect != machine.KeptWorkRemovedAnyway || done.Entry.Answer.Or("") != store.KeptRemoveAnyway {
			t.Fatalf("confirmed remove-anyway = %+v, %v; want removed-anyway", done, err)
		}
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("remove-anyway must keep the copy standing: %v", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || still.Open() ||
			still.Answer.Or("") != store.KeptRemoveAnyway || !still.RemovedAnywayAt.IsKnown() {
			t.Fatalf("after confirmation the entry reads %+v, %v; want it answered remove-anyway", still, err)
		} else if len(still.RemoveAnywayShown) != 0 || still.RemoveAnywayRefusal.Or("") != shown.Refusal {
			t.Fatalf("the release recorded shown %v and refusal %q; want nothing shown and the refusal %q",
				still.RemoveAnywayShown, still.RemoveAnywayRefusal.Or(""), shown.Refusal)
		}
	})
}

// A copy whose loss a discard's preview reads is refused for remove-anyway,
// naming discard.
func TestARemoveAnywayIsRefusedWhenADiscardCanRemoveTheCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway),
		}, &machine.KeptWorkAnswered{})
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "discard's preview reads") {
			t.Fatalf("remove-anyway of a discardable copy = %v; want refused naming discard", err)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refuse the entry reads %+v, %v; want it open", still, err)
		}
	})
}

// A copy that cannot be verified is offered for remove-anyway (decision 31 /
// section 13), with the refusal alone and no loss list, and --force releases
// its entry without touching what stands at the copy's path. Both ways a
// clone fails verification are planted: the copy gone, and a copy whose
// .git is another clone's, not the one its entry recorded.
func TestARemoveAnywayIsOfferedForAnUnverifiableCopy(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	for _, c := range []struct {
		name  string
		plant func(t *testing.T, copyPath, workingPath string) (check func(t *testing.T))
	}{
		{"the copy is gone", func(t *testing.T, copyPath, _ string) func(t *testing.T) {
			if err := os.RemoveAll(copyPath); err != nil {
				t.Fatal(err)
			}
			return func(*testing.T) {}
		}},
		{"the copy is not the one its entry recorded", func(t *testing.T, copyPath, workingPath string) func(t *testing.T) {
			other := filepath.Join(t.TempDir(), "other")
			git(t, workingPath, "clone", "--quiet", workingPath, other)
			head := git(t, other, "rev-parse", "HEAD")
			own := filepath.Join(copyPath, ".git")
			if err := os.Rename(own, own+".own"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(other, ".git"), own); err != nil {
				t.Fatal(err)
			}
			return func(t *testing.T) {
				if target, err := os.Readlink(own); err != nil || target != filepath.Join(other, ".git") {
					t.Errorf("the copy's .git reads %q, %v; want it left linking to the other clone", target, err)
				}
				if _, err := os.Stat(own + ".own"); err != nil {
					t.Errorf("the copy's own repository directory must stay: %v", err)
				}
				if got := git(t, other, "rev-parse", "HEAD"); got != head {
					t.Errorf("the other clone's HEAD became %s, was %s", got, head)
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			repository := recordRepository(t, h, subject)
			old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
			check := c.plant(t, entry.CopyPath, repository.WorkingPath)

			withService(t, h, func(under serviceUnderTest) {
				ctx := t.Context()
				request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
				var shown machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil ||
					shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" || shown.Loss != nil {
					t.Fatalf("remove-anyway of an unverifiable copy = %+v, %v; want shown with a refusal and no loss list", shown, err)
				}
				request.Force = true
				var done machine.KeptWorkAnswered
				if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &done); err != nil ||
					done.Effect != machine.KeptWorkRemovedAnyway {
					t.Fatalf("confirmed remove-anyway of an unverifiable copy = %+v, %v; want removed-anyway", done, err)
				}
				if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || still.Open() ||
					still.RemoveAnywayRefusal.Or("") != shown.Refusal {
					t.Fatalf("after confirmation the entry reads %+v, %v; want it released with the refusal %q", still, err, shown.Refusal)
				}
				check(t)
			})
		})
	}
}

// A Force confirmation whose discard refusal no longer holds takes the
// claim back and changes nothing (section 13).
func TestARemoveAnywayForceIsRefusedWhenTheRefusalNoLongerHolds(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	old, entry := plantKeptInvestigation(t, h, subject, repository.ID)
	git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Task: old, Hold: entry.Key(), Answer: string(store.KeptRemoveAnyway)}
		var shown machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &shown); err != nil ||
			shown.Effect != machine.KeptWorkRemoveAnywayShown || shown.Refusal == "" {
			t.Fatalf("show = %+v, %v; want remove-anyway-shown", shown, err)
		}
		writer, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, shown.Refusal, nil, store.ResolvedByPerson()); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		// Clear the shallow marker so a discard's preview reads the copy again
		// (fetch --unshallow of this layout leaves it shallow).
		if err := os.Remove(entry.CopyPath + "/.git/shallow"); err != nil {
			t.Fatal(err)
		}

		request.Force = true
		var out machine.KeptWorkAnswered
		err = under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "discard's preview reads") {
			t.Fatalf("force after the refusal cleared = %+v, %v; want refused naming discard", out, err)
		}
		still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID)
		if err != nil || !still.Open() || still.RemoveAnywayClaimed() {
			t.Fatalf("after the refuse the entry reads %+v, %v; want it open with no remove-anyway claim", still, err)
		}
	})
}

// A remove-anyway claim a stopped service left owed is taken up at the
// next open (PRD section 6, section 11): finished, keeping the copy, while
// the discard still refuses; taken back, leaving the entry open, once a
// discard's preview reads the copy.
func TestARemoveAnywayClaimOwedAtOpenIsFinishedOrTakenBack(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	for _, tc := range []struct {
		name     string
		shallow  bool
		released bool
	}{
		{name: "refusal still holds", shallow: true, released: true},
		{name: "refusal cleared", shallow: false, released: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			repository := recordRepository(t, h, subject)
			_, entry := plantKeptInvestigation(t, h, subject, repository.ID)
			if tc.shallow {
				git(t, entry.CopyPath, "fetch", "-q", "--depth=1", ".", "HEAD")
			}
			writer, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.ClaimKeptWorkRemoveAnyway(t.Context(), entry.ID, "discard refused: planted", nil, store.ResolvedByPerson()); err != nil {
				_ = writer.Close()
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			withService(t, h, func(serviceUnderTest) {
				still, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case tc.released && (still.Open() || still.Answer.Or("") != store.KeptRemoveAnyway):
					t.Fatalf("after open the entry reads %+v; want the owed claim finished as remove-anyway", still)
				case !tc.released && (!still.Open() || still.RemoveAnywayClaimed()):
					t.Fatalf("after open the entry reads %+v; want it open with the claim taken back", still)
				}
			})
			if _, err := os.Stat(entry.CopyPath); err != nil {
				t.Fatalf("the copy must stand either way: %v", err)
			}
		})
	}
}
