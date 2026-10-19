//go:build unix

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// promoteRunningInvestigation promotes the launched investigation onto
// branch through task.promote from this process, which is outside every
// worker group, asking for posture, and returns the answer or the refusal.
func promoteRunningInvestigation(t *testing.T, w launched, branch, posture string) (machine.TaskPromoted, error) {
	t.Helper()
	var promoted machine.TaskPromoted
	err := w.running.client.Call(t.Context(), ipc.MethodTaskPromote, machine.TaskPromoteRequest{
		Task: w.task, Repository: w.repository.ID, Branch: branch, Intent: "Make the change the report found.",
		Delivery: posture,
	}, &promoted)
	return promoted, err
}

// stillAnInvestigation fails unless the task is the investigation it was:
// no posture, no branch, and no promoted entry in its history.
func stillAnInvestigation(t *testing.T, w launched, after string) {
	t.Helper()
	stored, err := recordsOf(t, w.home).Task(t.Context(), w.task)
	if err != nil || stored.Shape != store.TaskInvestigation || stored.Branch.IsKnown() || stored.Delivery.IsKnown() {
		t.Fatalf("after %s the task is %+v (%v), want still an investigation with no posture", after, stored, err)
	}
	for _, kind := range kindsOf(t, w.home, w.task) {
		if kind == store.PromotedEventKind {
			t.Fatalf("%s left a promoted entry in the history", after)
		}
	}
}

// workerPushes has the worker commit a change in the task's copy and push
// the branch to the gate the way a delivery worker does, and returns the
// commit.
func workerPushes(t *testing.T, w launched, branch string) string {
	t.Helper()
	copyPath := resolvedCopy(t, w.home, w.task)
	quietMaintenance(t, copyPath)
	if err := os.WriteFile(filepath.Join(copyPath, "promoted.txt"), []byte("the promoted change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGitAs(t, w, copyPath, "add", "-A")
	mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "the promoted change")
	head := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
	if out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, branch); err != nil {
		t.Fatalf("the promoted worker's push was refused:\n%s", out)
	}
	if !strings.Contains(gateRefs(t, w.gateRepo), "refs/heads/"+branch+" "+head) {
		t.Fatalf("the push did not reach the gate:\n%s", gateRefs(t, w.gateRepo))
	}
	return head
}

// Promotion reads the posture the way intake does, end to end through
// task.promote, the service's trusted-copy read at a fresh fetch of the
// default branch, and a real worker's push (#187). Over a trusted copy that
// does not parse, the promotion is refused as unavailable and records
// nothing, with no posture assumed. Over a gated project, local asked for is
// refused as lighter and records nothing. The operator's own file says local,
// the only layer that may loosen the posture (#304), so the gated project is
// a trusted copy tightening it. Once the project declares local,
// the promotion records local in the flip, and the promoted worker's push of
// its branch starts no run and raises the approval hold offering its commit,
// which is the path an intake-recorded local task takes.
func TestAPromotionInALocalProjectRecordsLocalAndItsPushAsksForApproval(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	principles.Cite(t, principles.P14)
	launchedWorkerConfigured(t, store.TaskInvestigation, "", store.Unknown[config.Delivery](),
		`{"supervision_beacon_interval": "1s", "delivery": "local"}`, func(w launched) {
			ctx := t.Context()
			subject := w.repository.WorkingPath

			publishTrusted(t, subject, `{"delivery": "direct"}`)
			if _, err := promoteRunningInvestigation(t, w, "task/promoted", ""); !errors.Is(err, ipc.ErrUnavailable) {
				t.Fatalf("a promotion over an unparseable trusted copy = %v, want unavailable", err)
			}
			stillAnInvestigation(t, w, "the promotion over an unparseable copy")

			publishTrusted(t, subject, `{"delivery": "gated"}`)
			if _, err := promoteRunningInvestigation(t, w, "task/promoted", "local"); !errors.Is(err, ipc.ErrRefused) {
				t.Fatalf("local asked of a gated project's promotion = %v, want refused", err)
			}
			stillAnInvestigation(t, w, "the lighter promotion")

			publishTrusted(t, subject, `{"delivery": "local"}`)
			promoted, err := promoteRunningInvestigation(t, w, "task/promoted", "")
			if err != nil {
				t.Fatalf("the promotion in a local project: %v", err)
			}
			if promoted.Record.Delivery != store.Known(config.DeliveryLocal) {
				t.Fatalf("the promotion answered a task under %v, want local", promoted.Record.Delivery)
			}
			records := openRecords(t, w.home)
			defer func() { _ = records.Close() }()
			if stored, err := records.Task(ctx, w.task); err != nil || stored.Shape != store.TaskDelivery ||
				stored.Delivery != store.Known(config.DeliveryLocal) {
				t.Fatalf("the stored task is %+v (%v), want a delivery task under local", stored, err)
			}

			head := workerPushes(t, w, "task/promoted")
			if runs, err := records.RunsForRepository(ctx, w.repository.ID); err != nil || len(runs) != 0 {
				t.Fatalf("the promoted local task's push started %d runs (%v), want none", len(runs), err)
			}
			open, err := records.OpenHolds(ctx)
			if err != nil {
				t.Fatal(err)
			}
			offered := false
			for _, h := range open {
				if h.TaskID.Or("") == w.task && strings.Contains(h.Key, "/local-approval/"+head+"/") {
					offered = true
				}
			}
			if !offered {
				t.Fatalf("the open holds %+v offer nothing of task %s naming %s", open, w.task, head)
			}
		})
}

// The positive control: a gated project's promoted task is recorded gated,
// and its worker's push of the same shape starts the full run on the branch
// and raises no approval, so the local path above is the posture's doing
// and not something every promoted push does.
func TestAPromotionInAGatedProjectRunsTheFullGate(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	launchedWorkerOf(t, store.TaskInvestigation, "", func(w launched) {
		ctx := t.Context()
		promoted, err := promoteRunningInvestigation(t, w, "task/promoted", "")
		if err != nil {
			t.Fatalf("the promotion in a gated project: %v", err)
		}
		if promoted.Record.Delivery != store.Known(config.DeliveryGated) {
			t.Fatalf("the promotion answered a task under %v, want gated", promoted.Record.Delivery)
		}
		workerPushes(t, w, "task/promoted")
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		runs, err := records.RunsForRepository(ctx, w.repository.ID)
		if err != nil || len(runs) != 1 || runs[0].Branch != "task/promoted" {
			t.Fatalf("the promoted gated task's push started %d runs (%v), want one on its branch", len(runs), err)
		}
		open, err := records.OpenHolds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range open {
			if h.TaskID.Or("") == w.task && strings.Contains(h.Key, "/local-approval/") {
				t.Fatalf("the gated task's push raised an approval: %+v", h)
			}
		}
	})
}
