package service_test

import (
	"context"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestARunIsGivenAnIsolatedCopyAndGivesItBack is the whole of what the service
// owes a stage body about where it works.
//
// PRD section 8 puts a run's copy at worktrees/<repository>/<run>, and
// internal/stages' StageDeps.Copy opens it and never creates it. So a run that
// nothing built a copy for reaches its first body and fails there, which is
// what this build did before the service built one.
//
// Both halves are read through a real run rather than asserted about the code
// that would do it. The copy is observed from inside a stage body, through the
// same seam every body opens it by, so what this sees is what a body would
// have been handed. That it is gone is read after the run has settled, at the
// path the home names, which is where a copy nobody reclaimed would still be.
func TestARunIsGivenAnIsolatedCopyAndGivesItBack(t *testing.T) {
	requiresIdentifiedPeer(t)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)

	seen := make(chan copyObservation, 1)
	opts := options(t, h)
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Intent = copyObservingStage(deps, seen)
		return all
	}

	var run = struct {
		id string
	}{}
	withServiceOptions(t, opts, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		run.id = started.Record.ID
	})

	var got copyObservation
	select {
	case got = <-seen:
	default:
		t.Fatal("the run never reached a stage body, so nothing read the copy it was given")
	}
	if got.err != nil {
		t.Fatalf("a stage body could not open the copy its run works in: %v", got.err)
	}
	// The copy is checked out at the commit the run was asked to validate. A
	// copy standing anywhere else would be a body reviewing, testing and
	// pushing something other than what was submitted.
	submitted := git(t, subject, "rev-parse", "HEAD")
	if got.head != submitted {
		t.Fatalf("the copy stands at %s, want the submitted commit %s", got.head, submitted)
	}

	path := h.Worktree(repository.ID, run.id)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the copy of run %s is still at %s after the run ended: %v", run.id, path, err)
	}
}

// copyObservingStage is a stage body that opens the copy its run works in and
// reports where it stands, through the seam every body opens one by.
// copyObservation is where a run's copy stood when a body opened it, or why it
// could not be opened.
type copyObservation struct {
	head string
	err  error
}

func copyObservingStage(deps stages.StageDeps, seen chan<- copyObservation) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				repositoryID, runID, err := repositoryAndRun(in)
				if err != nil {
					return pipeline.Output{}, err
				}
				var head string
				copied, err := deps.Copy(ctx, repositoryID, runID)
				if err == nil {
					head, err = copied.ResolveCommit(ctx, "HEAD")
				}
				select {
				case seen <- copyObservation{head: head, err: err}:
				default:
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "opened the copy this run works in",
				}}, nil
			}
		},
	}
}

// repositoryAndRun reads the two facts StageDeps.Copy locates a copy from.
func repositoryAndRun(in pipeline.Input) (repositoryID, runID string, err error) {
	repositoryValue, err := in.State.Get(pipeline.KeyRepository)
	if err != nil {
		return "", "", err
	}
	runValue, err := in.State.Get(pipeline.KeyRun)
	if err != nil {
		return "", "", err
	}
	repositoryID, _ = repositoryValue.Text()
	runID, _ = runValue.Text()
	return repositoryID, runID, nil
}

// TestACopyHoldingWorkIsNotTakenAway is the refusal that makes reclaim safe.
//
// PRD principle P6 says never lose work, and what a run leaves in its copy is
// work: an agent's edits that never reached a commit, output a body wrote. Git
// refuses to remove a worktree holding modified or untracked files, and the
// service takes that refusal as the answer rather than forcing past it, so a
// run that ended badly leaves something a person can still look at.
//
// This is asserted of the mechanism the service uses rather than of a run,
// because what has to hold is that the refusal exists and that the untracked
// file survives it. A run arranged to leave a file behind would demonstrate
// the same thing through more machinery that could fail for other reasons.
func TestACopyHoldingWorkIsNotTakenAway(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	copyPath := h.Worktree("subject", "a-run-that-left-something")
	bare, err := vcs.OpenBare(t.Context(), gateRepositoryOf(t, h, subject), vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	if err := bare.Fetch(t.Context(), vcs.FetchSpec{
		Remote:   subject,
		Refspecs: []string{"+refs/heads/main:refs/assistant/test"},
	}); err != nil {
		t.Fatalf("bringing the subject's commit into the gate repository: %v", err)
	}
	copied, err := bare.AddWorktree(t.Context(), vcs.WorktreeSpec{
		Path:   copyPath,
		Commit: "refs/assistant/test",
	})
	if err != nil {
		t.Fatalf("creating a copy to leave work in: %v", err)
	}

	const left = "not-committed.txt"
	if err := writeFile(copyPath+"/"+left, "an agent wrote this and never committed it\n"); err != nil {
		t.Fatalf("leaving work in the copy: %v", err)
	}
	if err := copied.RemoveWorktree(t.Context(), copyPath); err == nil {
		t.Fatal("a copy holding uncommitted work was removed, so P6's promise rests on nothing here")
	}
	if _, err := os.Stat(copyPath + "/" + left); err != nil {
		t.Fatalf("the refused removal did not leave the work standing: %v", err)
	}
}

// gateRepositoryOf is the bare repository the gate of this working copy holds,
// found the way the service finds it: the binding assistant init wrote, and
// internal/gate's own answer for where that gate lives.
func gateRepositoryOf(t *testing.T, h *home.Home, workingPath string) string {
	t.Helper()
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = records.Close() }()
	binding, err := records.GateBinding(t.Context(), workingPath)
	if err != nil {
		t.Fatalf("reading the gate binding of %s: %v", workingPath, err)
	}
	repository, err := gate.RepositoryFor(h.Root(), binding.GateID)
	if err != nil {
		t.Fatalf("locating the gate repository: %v", err)
	}
	return repository
}
