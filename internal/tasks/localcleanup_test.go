package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// localTask records a delivery task under the local posture, stands up the
// repository's working copy at the path its record names, a clone of the
// upstream on main, and cuts the task's copy as deliveryTask does.
func (s *cleanupSubject) localTask(t *testing.T, branch string) (task, string) {
	t.Helper()
	ctx := context.Background()
	repository, err := s.store.Repository(ctx, cleanupRepository)
	if err != nil {
		t.Fatal(err)
	}
	primary := repository.WorkingPath
	rawGit(t, filepath.Dir(primary), "clone", "-q", s.upstream, primary)
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, _ := s.home.TaskCopy(id)
	if _, err := s.store.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(cleanupRepository), Branch: store.Known(branch), Delivery: store.Known(config.DeliveryLocal),
	}, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.store.BeginTaskAttempt(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.Clone(ctx, s.upstream, copyPath, gate.RemoteName); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, copyPath, "switch", "-q", "-c", branch)
	s.snapshot(t, id, copyPath)
	return task{id: id, copy: copyPath}, primary
}

// A local task's copy is held to P12's content check read against the local
// default branch its approval moved, and to nothing else. Its work pushed to
// the upstream proves nothing for it, because a local task's work lands in
// the person's own branch: the copy is kept, and the refusal names the local
// check. Once the working copy's main holds the work, the copy is removed on
// the default-branch proof, which examined the local tip.
func TestALocalTasksCopyIsProvenAgainstTheLocalDefaultBranch(t *testing.T) {
	principles.Cite(t, principles.P12)
	s := newCleanupSubject(t, nil)
	tk, primary := s.localTask(t, "task/local")
	work := strings.TrimSpace(commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "three", "THREE", 1), "the work"))

	// Pushed to the upstream's branch, which would prove a gated task's copy.
	s.push(t, tk.copy, "task/local")
	_, err := s.cleanUp(t, tk.id)
	refusal := refusedWith(t, err, ReasonUnproven)
	if len(refusal.Attempts) != 1 || !strings.Contains(refusal.Detail, "local default branch") {
		t.Fatalf("the refusal tried %d proofs and says %q, want the local check alone", len(refusal.Attempts), refusal.Detail)
	}
	if detail := attemptFor(t, refusal, store.CleanupProofDefaultBranch); !strings.Contains(detail, "local main") {
		t.Fatalf("the local check says %q, which does not name the local branch", detail)
	}
	stillStands(t, tk)

	// The approval's fast-forward, done here with git directly.
	rawGit(t, primary, "fetch", "-q", tk.copy, "task/local")
	rawGit(t, primary, "merge", "-q", "--ff-only", "FETCH_HEAD")
	got, err := s.cleanUp(t, tk.id)
	record := s.removedOn(t, tk, got, err, store.CleanupProofDefaultBranch)
	if n := len(record.Examined); n == 0 || record.Examined[n-1] != work {
		t.Fatalf("the proof examined %v, want the local main's tip %s last", record.Examined, work)
	}
}
