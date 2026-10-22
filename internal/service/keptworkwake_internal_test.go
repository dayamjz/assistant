package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/store"
)

// plantKeptRun records a repository and a failed run of it on branch, with
// an open kept work entry about the run's copy, as a person's push leaves
// one, and returns the entry. The copy is not on disk, so the service's
// recovery leaves the entry open and asks nothing of it.
func plantKeptRun(t *testing.T, records *store.Store, runID, branch string) store.KeptWork {
	t.Helper()
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: "subject", Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: runID, CopyPath: "/nowhere/worktrees/" + runID,
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	return entry
}
