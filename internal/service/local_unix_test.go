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
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The local posture end to end through the real gate and a real worker. The
// worker of a task recorded under local pushes its branch to the gate the
// way every delivery worker does: the push is admitted, no run starts, and
// the task holds on an approval offering the commit. An answer the hold does
// not offer is refused. An approval after the person moved their main is
// refused as moved, records nothing and leaves the hold open, because the
// anchor is the tip read when the change was offered (P6). Put back, the
// approval fast-forwards main in the primary checkout to the worker's
// commit, writes the local landing record with both tips, and the task
// resolves Landed from that record. A person's push of the same branch, from
// outside every worker group, starts its run, which is the control that the
// worker's push started none for want of a posture and not of a gate.
func TestALocalTasksPushStartsNoRunAndItsApprovalFastForwards(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P1)
	launchedWorkerUnder(t, store.TaskDelivery, "task/local", store.Known(config.DeliveryLocal), func(w launched) {
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		ctx := t.Context()
		copyPath := resolvedCopy(t, w.home, w.task)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "local.txt"), []byte("made on this machine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, w, copyPath, "add", "-A")
		mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "the local change")
		head := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
		primary := w.repository.WorkingPath
		tip := git(t, primary, "rev-parse", "refs/heads/main")

		out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, "task/local")
		if err != nil {
			t.Fatalf("the local task's push was refused:\n%s", out)
		}
		if !strings.Contains(gateRefs(t, w.gateRepo), "refs/heads/task/local "+head) {
			t.Fatalf("the push did not reach the gate:\n%s", gateRefs(t, w.gateRepo))
		}
		if runs, err := records.RunsForRepository(ctx, w.repository.ID); err != nil || len(runs) != 0 {
			t.Fatalf("the local task's push started %d runs (%v), want none", len(runs), err)
		}
		open, err := records.OpenHolds(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var offer store.Hold
		for _, h := range open {
			if h.TaskID.Or("") == w.task {
				offer = h
			}
		}
		if !strings.Contains(offer.Key, head) || !strings.Contains(offer.Key, tip) {
			t.Fatalf("the open holds %+v offer nothing naming %s anchored to %s", open, head, tip)
		}

		var answered machine.TaskAnswered
		err = w.running.client.Call(ctx, ipc.MethodTaskRespond, machine.TaskRespondRequest{Task: w.task, Answer: "fix"}, &answered)
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("an answer the approval does not offer = %v, want invalid request", err)
		}

		// The person commits on main in between: the anchor no longer
		// describes the branch, and the approval is refused.
		moved := git(t, primary, "commit-tree", "-p", tip, "-m", "a person's own commit", tip+"^{tree}")
		git(t, primary, "update-ref", "refs/heads/main", moved, tip)
		err = w.running.client.Call(ctx, ipc.MethodTaskRespond, machine.TaskRespondRequest{Task: w.task, Answer: machine.AnswerApprove}, &answered)
		// The branch moved past the anchor onto a commit the worker's does
		// not contain, which internal/safety names as what would be dropped.
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), moved) {
			t.Fatalf("an approval over a moved main = %v, want refused naming %s", err, moved)
		}
		if got := git(t, primary, "rev-parse", "refs/heads/main"); got != moved {
			t.Fatalf("a refused approval moved main to %s", got)
		}
		if h, err := records.Hold(ctx, offer.Key); err != nil || !h.Open() {
			t.Fatalf("a refused approval left the hold %+v (%v), want it open", h, err)
		}
		if _, found, err := records.LocalLanding(ctx, w.task); found || err != nil {
			t.Fatalf("a refused approval recorded a landing: %v, %v", found, err)
		}

		git(t, primary, "update-ref", "refs/heads/main", tip, moved)
		if err := w.running.client.Call(ctx, ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: w.task, Answer: machine.AnswerApprove}, &answered); err != nil {
			t.Fatalf("the approval: %v", err)
		}
		if got := git(t, primary, "rev-parse", "refs/heads/main"); got != head {
			t.Fatalf("the approval left main at %s, want the worker's %s", got, head)
		}
		landing, found, err := records.LocalLanding(ctx, w.task)
		if err != nil || !found || landing.Before != tip || landing.After != head || landing.Commit != head ||
			landing.Branch != "refs/heads/main" {
			t.Fatalf("the landing record is %+v (%v, %v)", landing, found, err)
		}
		if answered.Effect != machine.AnswerLanded || answered.Hold.Open() {
			t.Fatalf("the approval answered %+v", answered)
		}
		if s := answered.Task.State; s.State != taskstate.Landed || s.Source != taskstate.SourceRecord || s.Basis.LandedLocally != head {
			t.Fatalf("the approved task resolved %s from %s (basis %+v), want landed from the record", s.State, s.Source, s.Basis)
		}
		if runs, _ := records.RunsForRepository(ctx, w.repository.ID); len(runs) != 0 {
			t.Fatalf("the landing started %d runs", len(runs))
		}

		// A person's push of the branch is consent to validation and starts
		// the full run whatever the task's posture.
		// A commit of the person's own on top, because pushing the commit the
		// gate already holds moves nothing and notifies nothing.
		own := git(t, primary, "commit-tree", "-p", head, "-m", "the person's own change", head+"^{tree}")
		git(t, primary, "branch", "task/local", own)
		if out, err := tryGitIn(t, primary, "push", "--force", gate.RemoteName, "task/local"); err != nil {
			t.Fatalf("a person's push was refused:\n%s", out)
		}
		runs, err := records.RunsForRepository(ctx, w.repository.ID)
		if err != nil || len(runs) != 1 || runs[0].Branch != "task/local" {
			t.Fatalf("a person's push of the local task's branch started %d runs (%v), want one", len(runs), err)
		}
	})
}
