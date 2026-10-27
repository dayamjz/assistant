package service

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A landing's completion takes a failed read of a run's submitted
// reference for what it is, a read that failed, and never for the run not
// being the landing's (the final full review of feddd14, P6: a safety fact
// that cannot be verified refuses). The claimed landing's run stands in
// the gate at the copy's commit with a checkpoint, and landedRunSince
// names it, the control. Read through a handle whose invocations refuse
// the reference's own output as over the limit, the same read is an error
// and not "no run": before, it was no run, and the completion started a
// second run that superseded the one validating the work.
func TestALandingCompletionDoesNotTakeAFailedReferenceReadForNoRun(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	sub := newDispatchSubject(t)
	ctx := t.Context()
	repository, err := sub.records.Repository(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	gatePath, err := sub.service.gatePathOf(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := vcs.OpenBare(ctx, gatePath)
	if err != nil {
		t.Fatal(err)
	}
	const run = "run-of-the-landing"
	if err := bare.Fetch(ctx, vcs.FetchSpec{Remote: repository.WorkingPath, Refspecs: []string{"+HEAD:refs/assistant/runs/" + run}}); err != nil {
		t.Fatal(err)
	}
	head, err := bare.ResolveCommit(ctx, "refs/assistant/runs/"+run)
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := sub.records.RecordKeptWork(ctx, store.KeptCopy{TaskID: sub.task, CopyPath: sub.copy, Holds: []string{head},
		Lacks: []string{head}, Against: head, Reason: "unproven"})
	if err != nil {
		t.Fatal(err)
	}
	if entry, err = sub.records.ClaimKeptWorkLanding(ctx, entry.ID, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.records.CreateRun(ctx, store.Run{
		ID: run, RepositoryID: repository.ID, Branch: "task/one", SubmittedHead: head,
		Base: head, Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.records.AppendGraphCheckpoint(ctx, run, "", 0, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	plan := landing{repository: repository, branch: "task/one", head: head}

	if got, err := sub.service.landedRunSince(ctx, entry, plan, bare); err != nil || got != run {
		t.Fatalf("landedRunSince over a readable gate = %q, %v; want the landing's run %s", got, err, run)
	}
	// Five bytes is "true\n", which opening the handle reads, and not the
	// forty-one a resolved commit is, so the reference read fails as over
	// the limit while nothing else about the gate changes.
	refusing, err := vcs.OpenBare(ctx, gatePath, vcs.WithMaxOutput(5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refusing.ResolveCommit(ctx, "refs/assistant/runs/"+run); !errors.Is(err, vcs.ErrOutputTooLarge) {
		t.Fatalf("the refusing handle resolves the reference with %v; want the read refused, which is the failure under test", err)
	}
	got, err := sub.service.landedRunSince(ctx, entry, plan, refusing)
	if err == nil || got != "" {
		t.Fatalf("landedRunSince over a reference read that fails = %q, %v; want the failure, not no run", got, err)
	}
	if errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("the failure reads as the reference not found: %v", err)
	}
}
