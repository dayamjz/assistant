//go:build unix

package service_test

import (
	"encoding/json"
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

// offeredLocally has the launched local task's worker commit a change and
// push its branch to the gate, and returns the commit pushed, the tip the
// person's main stood at, and the hold offering the commit anchored to that
// tip. It asserts what every local test rests on: the push was admitted, no
// run started, and the offer names both commits.
func offeredLocally(t *testing.T, w launched) (head, tip string, offer store.Hold) {
	t.Helper()
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
	head = mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
	tip = git(t, w.repository.WorkingPath, "rev-parse", "refs/heads/main")

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
	for _, h := range open {
		if h.TaskID.Or("") == w.task {
			offer = h
		}
	}
	if !strings.Contains(offer.Key, head) || !strings.Contains(offer.Key, tip) {
		t.Fatalf("the open holds %+v offer nothing naming %s anchored to %s", open, head, tip)
	}
	return head, tip, offer
}

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
//
// Every answer here is made from the foreground of a terminal, the one
// place the approval hold takes one from, and the landing itself is the
// person's own command line, "--task ID --answer approve" through cli.Run,
// run there. Which peers the hold refuses is
// TestALocalApprovalRefusesAPeerOffATerminalAndTheWorker's.
func TestALocalTasksPushStartsNoRunAndItsApprovalFastForwards(t *testing.T) {
	t.Parallel()
	needsTerminal(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P1)
	launchedWorkerUnder(t, store.TaskDelivery, "task/local", store.Known(config.DeliveryLocal), func(w launched) {
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		ctx := t.Context()
		primary := w.repository.WorkingPath
		head, tip, offer := offeredLocally(t, w)

		if _, code := callAtTerminal(t, w.running, ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: w.task, Answer: "fix"}); code != ipc.CodeInvalidRequest {
			t.Fatalf("an answer the approval does not offer = %q, want %q", code, ipc.CodeInvalidRequest)
		}

		// The person commits on main in between: the anchor no longer
		// describes the branch, and the approval is refused.
		moved := git(t, primary, "commit-tree", "-p", tip, "-m", "a person's own commit", tip+"^{tree}")
		git(t, primary, "update-ref", "refs/heads/main", moved, tip)
		_, err := answerAtTerminal(t, w.home, w.task, machine.AnswerApprove)
		// The branch moved past the anchor onto a commit the worker's does
		// not contain, which internal/safety names as what would be dropped.
		if err == nil || !strings.Contains(err.Error(), string(ipc.CodeRefused)) || !strings.Contains(err.Error(), moved) {
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
		answered, err := answerAtTerminal(t, w.home, w.task, machine.AnswerApprove)
		if err != nil {
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

// PRD section 6 gives the local landing to the person: "You approve". The
// approval hold is answered only from the foreground of a terminal, decided
// by the service from the peer's own lineage and from nothing the request
// says. A peer with no controlling terminal, which is the shape a host
// agent's tool gives the command it runs, is refused not-at-terminal on
// approve and on stop alike, and nothing happens: the hold stays open, no
// landing is recorded, and main does not move (P6). The task's own worker
// is refused as a worker, before the terminal is asked about, as it was.
// The positive control is the same approve from the same binary in the
// foreground of a terminal, which lands the change and records the
// resolver the surface derives, the machine interface's, because a
// terminal's foreground witnesses a terminal and not a person.
func TestALocalApprovalRefusesAPeerOffATerminalAndTheWorker(t *testing.T) {
	t.Parallel()
	needsTerminal(t)
	principles.Cite(t, principles.P6)
	launchedWorkerUnder(t, store.TaskDelivery, "task/local", store.Known(config.DeliveryLocal), func(w launched) {
		records := openRecords(t, w.home)
		defer func() { _ = records.Close() }()
		ctx := t.Context()
		primary := w.repository.WorkingPath
		head, tip, offer := offeredLocally(t, w)

		unchanged := func(after string) {
			t.Helper()
			if got := git(t, primary, "rev-parse", "refs/heads/main"); got != tip {
				t.Fatalf("after %s main is at %s, want it left at %s", after, got, tip)
			}
			if h, err := records.Hold(ctx, offer.Key); err != nil || !h.Open() {
				t.Fatalf("after %s the hold is %+v (%v), want it open", after, h, err)
			}
			if _, found, err := records.LocalLanding(ctx, w.task); found || err != nil {
				t.Fatalf("after %s a landing is recorded: %v, %v", after, found, err)
			}
		}
		for _, answer := range []string{machine.AnswerApprove, machine.AnswerStop} {
			_, code := callDetached(t, w.running, ipc.MethodTaskRespond, machine.TaskRespondRequest{Task: w.task, Answer: answer})
			if code != ipc.CodeNotAtTerminal {
				t.Fatalf("%q from a peer with no controlling terminal answered %q, want %q", answer, code, ipc.CodeNotAtTerminal)
			}
			unchanged("the detached " + answer)
		}
		if _, code := callAs(t, w, ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: w.task, Answer: machine.AnswerApprove}); code != ipc.CodeWorker {
			t.Fatalf("approve from the task's own worker answered %q, want %q", code, ipc.CodeWorker)
		}
		unchanged("the worker's approve")

		raw, code := callAtTerminal(t, w.running, ipc.MethodTaskRespond,
			machine.TaskRespondRequest{Task: w.task, Answer: machine.AnswerApprove})
		if code != "" {
			t.Fatalf("approve from the foreground of a terminal was refused with %q", code)
		}
		var answered machine.TaskAnswered
		if err := json.Unmarshal(raw, &answered); err != nil {
			t.Fatalf("the answer does not decode: %v\n%s", err, raw)
		}
		if answered.Effect != machine.AnswerLanded {
			t.Fatalf("the approval answered %+v, want landed", answered)
		}
		if got := git(t, primary, "rev-parse", "refs/heads/main"); got != head {
			t.Fatalf("the approval left main at %s, want the worker's %s", got, head)
		}
		resolved, err := records.Hold(ctx, offer.Key)
		if err != nil || resolved.Open() || resolved.ResolvedBy.Or(store.Resolver{}) != (ipc.Request{}).HoldResolver() {
			t.Fatalf("the approval left the hold %+v (%v), want it resolved by the resolver the surface derives", resolved, err)
		}
	})
}
