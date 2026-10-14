package service_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// checkoutSnapshot is everything about the person's checkout a mutation
// would show in: its references, HEAD, and its tree's state.
func checkoutSnapshot(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)") + "\n" +
		git(t, dir, "symbolic-ref", "HEAD") + "\n" +
		git(t, dir, "status", "--porcelain=v2", "--untracked-files=all", "--branch")
}

// syncOf asks the service for assistant sync's answer.
func syncOf(t *testing.T, client *ipc.Client, workingPath string, confirm bool) machine.Plan {
	t.Helper()
	var plan machine.Plan
	if err := client.Call(t.Context(), ipc.MethodBranchSync, machine.SyncRequest{
		Working: machine.Working{WorkingPath: workingPath}, Confirm: confirm,
	}, &plan); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return plan
}

// statusOf asks the service for assistant status's answer.
func statusOf(t *testing.T, client *ipc.Client, workingPath string) machine.Status {
	t.Helper()
	var status machine.Status
	if err := client.Call(t.Context(), ipc.MethodStatus, machine.StatusRequest{
		Working: machine.Working{WorkingPath: workingPath},
	}, &status); err != nil {
		t.Fatalf("status: %v", err)
	}
	return status
}

// classOf is the class a status or a plan carries, empty when it carries a
// refusal.
func classOf(c *custody.Classification) custody.Class {
	if c == nil {
		return ""
	}
	return c.Class
}

// Custody through the service, over a run the service really walked. While
// the run holds for a decision, sync and status both classify the branch as
// one the pipeline holds and a confirmed sync changes nothing. A commit made
// in the run's copy after its last checkpoint - the work a service killed
// mid-stage would leave - and the run then ended: the branch is recoverable
// on that commit, which the checkpoint never named. Status reports it and
// writes nothing in the checkout; the plan is printed and nothing changes
// until it is confirmed; confirmed, the work is brought back under the run's
// recovery reference and the branch is not moved. A second branch whose run
// ended without moving anything is yours already, from the run's own
// checkpoint.
func TestSyncThroughTheServiceHoldsThenRecoversARunsWork(t *testing.T) {
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")

	withService(t, h, func(running serviceUnderTest) {
		started := startRun(t, running.client, subject)
		if started.Decision == nil {
			t.Fatalf("the run did not stop for a decision: %+v", started.Outcome)
		}
		if plan := syncOf(t, running.client, subject, false); classOf(plan.Classification) != custody.ClassPipelineHolds || len(plan.Steps) != 0 {
			t.Fatalf("a branch whose run holds for a decision planned %+v", plan)
		}
		before := checkoutSnapshot(t, subject)
		if plan := syncOf(t, running.client, subject, true); plan.Applied || plan.Blocked() {
			t.Fatalf("a confirmed sync of a held branch answered %+v", plan)
		}
		if after := checkoutSnapshot(t, subject); after != before {
			t.Fatalf("a sync of a held branch changed the checkout:\n%s\n---\n%s", before, after)
		}
		if status := statusOf(t, running.client, subject); status.Custody == nil || classOf(status.Custody.Classification) != custody.ClassPipelineHolds {
			t.Fatalf("status of a held branch reports %+v", status.Custody)
		}

		copyPath := h.Worktree(repository.ID, started.Record.ID)
		if err := os.WriteFile(filepath.Join(copyPath, "late.txt"), []byte("committed after the last checkpoint\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, copyPath, "add", "-A")
		git(t, copyPath, "commit", "--quiet", "-m", "work the checkpoint never named")
		late := git(t, copyPath, "rev-parse", "HEAD")
		var ended machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunCancel, machine.CancelRequest{Run: started.Record.ID}, &ended); err != nil {
			t.Fatalf("ending the run: %v", err)
		}

		before = checkoutSnapshot(t, subject)
		status := statusOf(t, running.client, subject)
		if status.Custody == nil || classOf(status.Custody.Classification) != custody.ClassRecoverable ||
			status.Custody.Classification.Work != late {
			t.Fatalf("status of an ended run with work in its copy reports %+v", status.Custody)
		}
		plan := syncOf(t, running.client, subject, false)
		if classOf(plan.Classification) != custody.ClassRecoverable || plan.Applied || plan.Blocked() {
			t.Fatalf("the plan for recoverable work is %+v", plan)
		}
		kinds := make([]custody.StepKind, 0, len(plan.Steps))
		for _, step := range plan.Steps {
			kinds = append(kinds, step.Kind)
		}
		if !slices.Equal(kinds, []custody.StepKind{custody.StepFetch, custody.StepRecover, custody.StepRelease}) {
			t.Fatalf("recovery plans %v", kinds)
		}
		if after := checkoutSnapshot(t, subject); after != before {
			t.Fatalf("status and an unconfirmed sync changed the checkout:\n%s\n---\n%s", before, after)
		}

		plan = syncOf(t, running.client, subject, true)
		if !plan.Applied || plan.Refusal != "" {
			t.Fatalf("the confirmed recovery answered %+v", plan)
		}
		if got := git(t, subject, "rev-parse", custody.RecoveredRef(started.Record.ID)); got != late {
			t.Fatalf("the recovered work is %s, want %s", got, late)
		}
		if got := git(t, subject, "rev-parse", "HEAD"); got != submitted {
			t.Fatalf("recovery moved the branch to %s", got)
		}

		// A second branch, whose run ended where it began.
		commitOn(t, subject, "untouched", "untouched.txt")
		second := startRun(t, running.client, subject)
		if err := running.client.Call(t.Context(), ipc.MethodRunCancel, machine.CancelRequest{Run: second.Record.ID}, &ended); err != nil {
			t.Fatalf("ending the second run: %v", err)
		}
		if plan := syncOf(t, running.client, subject, true); classOf(plan.Classification) != custody.ClassYoursAlready || plan.Blocked() || len(plan.Done) != 0 {
			t.Fatalf("a branch whose run never moved its work answered %+v", plan)
		}
	})
}
