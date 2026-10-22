package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// keptRunEntry records an open kept work entry about the store's one
// running run, as a person's push leaves one.
func keptRunEntry(t *testing.T, s *Store, path string) KeptWork {
	t.Helper()
	run := runningRun(t, s)
	entry, _, err := s.RecordKeptWork(context.Background(), KeptCopy{RunID: run.ID, CopyPath: path, Holds: []string{"aaaa"},
		Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	return entry
}

// PRD section 8's kept-work wake is appended once per entry, under the
// incarnation fence, about an open entry only, and records when the entry
// surfaced in its own transaction: a replay answers the queued row with
// nothing written, the same entry once acknowledged is refused, an answered
// or closed entry is owed no wake, and the project is read off the entry's
// row rather than stored on the wake's (P14).
func TestAKeptWorkWakeIsAppendedOncePerOpenEntryUnderTheFence(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	entry := keptRunEntry(t, s, "/worktrees/run")
	inc := incarnationOf(t, s)
	request := KeptWorkWakeRequest{KeptWork: entry.ID, Kind: "kept-work", Marker: "kept-work:1"}
	if _, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: 99, Kind: "kept-work", Marker: "m"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wake about no entry = %v, want ErrNotFound", err)
	}
	for _, bad := range []KeptWorkWakeRequest{{Kind: "kept-work", Marker: "m"}, {KeptWork: entry.ID, Marker: "m"}, {KeptWork: entry.ID, Kind: "kept-work"}} {
		if _, err := s.AppendKeptWorkWake(ctx, inc, bad); err == nil {
			t.Fatalf("the malformed request %+v was appended", bad)
		}
	}
	first, err := s.AppendKeptWorkWake(ctx, inc, request)
	if err != nil || first.Disposition != WakeAppended || first.Wake.KeptWork != entry.ID || !first.Wake.Queued() ||
		first.Wake.Project != entry.RepositoryID || first.Wake.Branch != entry.Branch || !entry.Branch.IsKnown() {
		t.Fatalf("the first append = %+v, %v; want entry %d's queued wake under its project and branch, %v and %v", first, err, entry.ID, entry.RepositoryID, entry.Branch)
	}
	surfaced, err := s.KeptWorkEntry(ctx, entry.ID)
	if err != nil || surfaced.SurfacedAt != Known(first.Wake.CreatedAt) {
		t.Fatalf("after the wake the entry reads %+v, %v; want it surfaced at the wake's creation, %s", surfaced, err, first.Wake.CreatedAt)
	}
	again, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: entry.ID, Kind: "kept-work", Marker: "another"})
	if err != nil || again.Disposition != WakeAlreadyQueued || again.Wake.ID != first.Wake.ID {
		t.Fatalf("a second append about the entry = %+v, %v; want the first row, already queued", again, err)
	}
	stale := inc
	inc = incarnationOf(t, s)
	if _, err := s.AppendKeptWorkWake(ctx, stale, request); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a write by a replaced incarnation = %v, want ErrStaleIncarnation", err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, []int64{first.Wake.ID}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendKeptWorkWake(ctx, inc, request); !errors.Is(err, ErrWakeHandled) {
		t.Fatalf("the entry raised again once its wake was acknowledged = %v, want ErrWakeHandled", err)
	}
	if still, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
		t.Fatalf("acknowledging the wake left the entry %+v, %v; want it open, since a wake answers nothing", still, err)
	}

	answered, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: entry.RunID.Or(""), CopyPath: "/worktrees/answered", Holds: []string{"bbbb"},
		Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, answered.ID, KeptDiscard, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: answered.ID, Kind: "kept-work", Marker: "m"}); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("a wake about an answered entry = %v, want ErrKeptWorkAnswered", err)
	}
	all, err := s.KeptWorkWakes(ctx)
	if err != nil || len(all) != 1 || all[0].ID != first.Wake.ID || all[0].Queued() || all[0].AcknowledgedBy != Known(AcknowledgedByCoordinator()) {
		t.Fatalf("KeptWorkWakes = %+v, %v; want the one acknowledged row", all, err)
	}
}

// The third shape drains, is offered, listed, counted and acknowledged on
// the domain wake's terms, numbered apart from the other two, and reaches
// the first coordinator alone, since its entry names no task and so no
// domain. Its seals hold against direct SQL: the row is not rewritten, an
// acknowledgement stands, and neither the wake nor its notification is
// deleted while the entry stands.
func TestAKeptWorkWakeReachesTheFirstCoordinatorAloneAndClosesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	entry := keptRunEntry(t, s, "/worktrees/run")
	inc := incarnationOf(t, s)
	appended, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: entry.ID, Kind: "kept-work", Marker: "kept-work:1"})
	if err != nil {
		t.Fatal(err)
	}
	id := appended.Wake.ID
	coordinator := WakeHandout{To: AcknowledgedByCoordinator(), Under: "u"}
	subordinate := WakeHandout{To: AcknowledgedBySubordinate("alpha"), Under: "u", Route: WakeRoute{Subordinate: "alpha", Actable: []string{"kept-work"}}}
	if n, err := s.UnacknowledgedKeptWorkWakes(ctx); err != nil || n != 1 {
		t.Fatalf("UnacknowledgedKeptWorkWakes = %d, %v; want 1", n, err)
	}
	if n, err := s.OfferedKeptWorkWakes(ctx, nil, subordinate.Route); err != nil || n != 0 {
		t.Fatalf("OfferedKeptWorkWakes on a subordinate's route = %d, %v; want 0", n, err)
	}
	if drained, err := s.DrainAll(ctx, 10, subordinate); err != nil || len(drained.KeptWorkWakes) != 0 {
		t.Fatalf("a subordinate's drain = %+v, %v; want no kept-work wake", drained, err)
	}
	if pending, err := s.PendingKeptWorkNotifications(ctx, 10); err != nil || len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("PendingKeptWorkNotifications = %+v, %v; want wake %d", pending, err, id)
	}
	drained, err := s.DrainAll(ctx, 10, coordinator)
	if err != nil || len(drained.KeptWorkWakes) != 1 || drained.KeptWorkWakes[0].ID != id || !drained.KeptWorkWakes[0].HandedOut() ||
		drained.KeptWorkWakes[0].Handouts != Known(int64(1)) || len(drained.Wakes)+len(drained.DomainWakes) != 0 {
		t.Fatalf("the coordinator's drain = %+v, %v; want wake %d handed out once", drained, err, id)
	}
	if n, err := s.OfferedKeptWorkWakes(ctx, []string{"u"}, WakeRoute{}); err != nil || n != 0 {
		t.Fatalf("a row handed out under an attachment still held is offered %d times (%v), want none", n, err)
	}
	if queued, err := s.QueuedKeptWorkWakes(ctx, 10); err != nil || len(queued) != 1 || !queued[0].HandedOut() {
		t.Fatalf("QueuedKeptWorkWakes = %+v, %v; want the handed-out row still queued", queued, err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, []int64{id, 99}, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("a batch naming an unknown kept-work wake = %v, want ErrWakeUnknown", err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, []int64{id}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, []int64{id}, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeAcknowledged) {
		t.Fatalf("acknowledging the row again = %v, want ErrWakeAcknowledged", err)
	}
	if n, err := s.UnacknowledgedKeptWorkWakes(ctx); err != nil || n != 0 {
		t.Fatalf("UnacknowledgedKeptWorkWakes after the acknowledgement = %d, %v; want 0", n, err)
	}
	if pending, err := s.PendingKeptWorkNotifications(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("an acknowledged wake is pending notification: %+v, %v", pending, err)
	}
	for what, statement := range map[string]string{
		"a rewritten entry":               `UPDATE kept_work_wake SET kept_work = 7 WHERE id = ?`,
		"a second acknowledgement":        `UPDATE kept_work_wake SET handled_at = 'later', acknowledged_by = 'program' WHERE id = ?`,
		"a handout after the closing":     `UPDATE kept_work_wake SET handed_out_at = 'x', handed_out_to = 'coordinator', handed_out_under = 'u', handouts = 2 WHERE id = ?`,
		"a delete while its entry stands": `DELETE FROM kept_work_wake WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, id); err == nil || !strings.Contains(err.Error(), "kept work wake") {
			t.Fatalf("%s = %v, want the seal's refusal", what, err)
		}
	}
}

// A kept-work wake's identifier is never handed to another wake, even once
// forgetting a repository has removed the row that held it: an
// acknowledgement names a wake by identifier, so a coordinator acknowledging
// a wake it was handed before the repository went must be refused rather
// than close an unrelated one that took the same number.
func TestAKeptWorkWakesIdentifierIsNotReusedAfterItsRepositoryIsForgotten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	first := keptRunEntry(t, s, "/worktrees/run")
	inc := incarnationOf(t, s)
	old, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: first.ID, Kind: "kept-work", Marker: "kept-work:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainAll(ctx, 10, WakeHandout{To: AcknowledgedByCoordinator(), Under: "u"}); err != nil {
		t.Fatal(err)
	}
	// The copy's work lands, the removal on its proof closes the entry, the
	// run ends, and the repository is forgotten with the closed entry and
	// its wake.
	run := first.RunID.Or("")
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run, Proof: Known(CleanupProofDefaultBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionRun(ctx, run, []RunStatus{RunRunning}, RunFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetRepository(ctx, first.RepositoryID.Or("")); err != nil {
		t.Fatalf("ForgetRepository: %v", err)
	}
	second := keptRunEntry(t, s, "/worktrees/run-again")
	again, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: second.ID, Kind: "kept-work", Marker: "kept-work:2"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Wake.ID == old.Wake.ID {
		t.Fatalf("the new entry's wake took identifier %d, which the forgotten wake held", again.Wake.ID)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, []int64{old.Wake.ID}, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("acknowledging the forgotten wake's identifier = %v, want ErrWakeUnknown", err)
	}
	if queued, err := s.QueuedKeptWorkWakes(ctx, 10); err != nil || len(queued) != 1 || queued[0].ID != again.Wake.ID {
		t.Fatalf("the queued kept-work wakes are %+v, %v; want the new one, still queued", queued, err)
	}
}
