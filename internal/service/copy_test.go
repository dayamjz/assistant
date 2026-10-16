package service_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
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
//
// Giving it back is PRD principle P12's decision, so the branch under
// validation is pushed to the upstream before the run: the copy stands on
// that commit, the remote-branch proof finds it reachable from the upstream's
// branch at once, and the copy is removed on it. The run that follows
// establishes what that decision does with a copy whose work has not landed.
//
// The same body reads automatic maintenance from inside the copy. The copy is
// a linked worktree of the gate and nothing writes its configuration, so this
// reading is what says the gate's setting reaches it, and it is paired with
// the working copy reading neither key, which is PRD principle P1's half.
func TestARunIsGivenAnIsolatedCopyAndGivesItBack(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P1)

	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "push", "--quiet", "origin", "work")
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

	// The copy is a linked worktree of the gate and is given no configuration
	// write of its own, so what is read from inside it is what the gate's
	// initialization wrote. The working copy's own configuration is read the
	// same way and carries neither key, which is PRD principle P1's half and
	// the control that the read comes back unset where nothing set it.
	if want := map[string]string{"gc.auto": "0", "maintenance.auto": "false"}; !maps.Equal(got.maintenance, want) {
		t.Fatalf("the copy reads automatic maintenance as %v from its own configuration, want %v", got.maintenance, want)
	}
	if primary, err := ownMaintenanceConfig(subject); err != nil || len(primary) != 0 {
		t.Fatalf("the working copy's own configuration reads %v (%v), want neither key", primary, err)
	}

	path := h.Worktree(repository.ID, run.id)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the copy of run %s is still at %s after the run ended: %v", run.id, path, err)
	}
	// The record says what the removal rested on: the remote-branch proof,
	// over the commit the copy stood on, marked removed after.
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	cleanup, err := records.RunCleanup(t.Context(), run.id)
	if err != nil {
		t.Fatalf("the removed copy of run %s has no cleanup record: %v", run.id, err)
	}
	if cleanup.Proof.Or("") != store.CleanupProofRemoteBranch || !cleanup.RemovedAt.IsKnown() || cleanup.Kept() {
		t.Fatalf("the cleanup record is %+v, want the remote-branch proof with the removal marked", cleanup)
	}
	if len(cleanup.Examined) == 0 || cleanup.Examined[0] != submitted {
		t.Fatalf("the proof examined %v, want the submitted commit %s first", cleanup.Examined, submitted)
	}
	// The reference create wrote in the gate at the submitted commit goes
	// with the copy, so the gate holds nothing of a run that ended.
	gatePath := gateRepositoryOf(t, h, subject)
	if out, err := exec.Command("git", "-C", gatePath, "rev-parse", "--verify", "--quiet", "refs/assistant/runs/"+run.id).CombinedOutput(); err == nil {
		t.Fatalf("the reference of run %s is still in the gate after the run ended: %s", run.id, out)
	}
}

// copyObservingStage is a stage body that opens the copy its run works in and
// reports where it stands, through the seam every body opens one by.
// copyObservation is where a run's copy stood when a body opened it, or why it
// could not be opened.
type copyObservation struct {
	head string
	// maintenance is each automatic-maintenance key as the copy's own
	// configuration reads from inside the copy, absent where it is unset.
	maintenance map[string]string
	err         error
}

// ownMaintenanceConfig reads the two automatic-maintenance keys the way a git
// started in dir reads them from the repository's own configuration, with
// nothing from a global or system file, and reports the ones that are set. It
// runs on a stage body's goroutine, so it reports a failed read as an error
// rather than failing the test there.
func ownMaintenanceConfig(dir string) (map[string]string, error) {
	got := map[string]string{}
	for _, key := range []string{"gc.auto", "maintenance.auto"} {
		cmd := exec.Command("git", "-C", dir, "config", "--local", "--get-all", key)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
			"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"))
		out, err := cmd.CombinedOutput()
		switch {
		case err == nil:
			got[key] = strings.TrimSpace(string(out))
		case len(strings.TrimSpace(string(out))) == 0:
			// git config reports an unset key with a failure and no output.
		default:
			return nil, fmt.Errorf("reading %s in %s: %w: %s", key, dir, err, out)
		}
	}
	return got, nil
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
				var maintenance map[string]string
				copied, err := deps.Copy(ctx, repositoryID, runID)
				if err == nil {
					head, err = copied.ResolveCommit(ctx, "HEAD")
				}
				if err == nil {
					maintenance, err = ownMaintenanceConfig(copied.Path())
				}
				select {
				case seen <- copyObservation{head: head, maintenance: maintenance, err: err}:
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
	t.Parallel()
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

// TestACopyWhoseWorkHasNotLandedIsKeptAndStatusSaysSo is the other half of
// PRD principle P12 at this site: a run whose copy stands on a commit that is
// on no remote branch, in no merged pull request and absent from the default
// branch by content keeps its copy, and the refusal reaches the run's answer
// rather than only the log.
//
// The commit the copy stands on is the submitted one, which this subject
// never pushes to its upstream. That is the same shape a fix round leaves:
// a commit on the copy's detached HEAD that landed nowhere, and the decision
// reads the copy's HEAD and not how the commit got there, so what is
// established here holds for the fix round's commit as it does for this one.
// What is not driven here is a run that takes a fix round; internal/stages'
// own tests drive the round, and the P5 journey walk drives one through the
// binary.
func TestACopyWhoseWorkHasNotLandedIsKeptAndStatusSaysSo(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P12)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)

	var run machine.Run
	withService(t, h, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		run = runByID(t, under.client, started.Record.ID)
	})
	if run.Outcome != machine.OutcomePushed {
		t.Fatalf("the run reads %s, want pushed (local upstream), so that the copy was reclaimed on a finished run", run.Outcome)
	}
	path := h.Worktree(repository.ID, run.Record.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the copy of run %s at %s was taken away with its work landed nowhere: %v", run.Record.ID, path, err)
	}
	if run.Cleanup == nil || !run.Cleanup.Kept() || run.Cleanup.Refusal.Or("") != string(tasks.ReasonUnproven) {
		t.Fatalf("the run's answer carries the cleanup record %+v, want a kept copy refused as unproven", run.Cleanup)
	}
	submitted := git(t, subject, "rev-parse", "HEAD")
	if len(run.Cleanup.Examined) != 1 || run.Cleanup.Examined[0] != submitted {
		t.Fatalf("the refusal examined %v, want the submitted commit %s the copy stands on", run.Cleanup.Examined, submitted)
	}
	for _, proof := range []store.CleanupProof{store.CleanupProofRemoteBranch, store.CleanupProofMergedPullRequest, store.CleanupProofDefaultBranch} {
		if !strings.Contains(run.Cleanup.Detail, string(proof)+":") {
			t.Fatalf("the refusal's detail %q does not say what the %s proof found", run.Cleanup.Detail, proof)
		}
	}
	if strings.Contains(strings.ToLower(run.Cleanup.Detail), "force") {
		t.Fatalf("the refusal's detail %q names a flag", run.Cleanup.Detail)
	}
	// The reference that keeps the copy's commit reachable is kept with it.
	gatePath := gateRepositoryOf(t, h, subject)
	if out, err := exec.Command("git", "-C", gatePath, "rev-parse", "--verify", "--quiet", "refs/assistant/runs/"+run.Record.ID).CombinedOutput(); err != nil {
		t.Fatalf("the reference of the kept copy of run %s is gone from the gate: %s", run.Record.ID, out)
	}
}
