package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// A rerun acts on the branch the caller is standing on, the way every
// neighbouring verb does. A verb that reached for the repository's newest run
// would restart a branch the caller did not name: standing on one branch and
// asking for a rerun would start a run of another, at another branch's head,
// and answer with a branch that was never mentioned.
func TestARerunIsOfTheBranchTheCallerIsStandingOnAndNotTheRepositorysNewest(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		// Two branches with a run each, the newer of the two on feature-a.
		standing := commitOn(t, subject, "feature-b", "b.txt")
		runAndEnd(t, running.client, subject)
		commitOn(t, subject, "feature-a", "a.txt")
		runAndEnd(t, running.client, subject)

		// Stand on the branch whose run is not the newest, and ask again.
		git(t, subject, "checkout", "--quiet", "feature-b")
		again := rerun(t, running.client, subject)

		if again.Record.Branch != "feature-b" {
			t.Fatalf("a rerun asked for while standing on feature-b started a run of %s", again.Record.Branch)
		}
		if again.Record.SubmittedHead != standing {
			t.Fatalf("the rerun stands at %s, want feature-b's head %s", again.Record.SubmittedHead, standing)
		}
		holdingAt(t, again, pipeline.StageIntent)
	})
}

// A branch that has never been run says so about that branch. Reaching for
// another branch's run would start work nobody asked for.
func TestARerunOnABranchWithNoRunRefusesByThatBranchsName(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		commitOn(t, subject, "feature-a", "a.txt")
		runAndEnd(t, running.client, subject)

		git(t, subject, "checkout", "--quiet", "-b", "feature-untouched")
		var run machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunRerun, machine.RerunRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &run)
		if err == nil {
			t.Fatalf("a rerun on a branch with no run started a run of %s", run.Record.Branch)
		}
		if !strings.Contains(err.Error(), "feature-untouched") {
			t.Fatalf("the refusal is about some other branch: %v", err)
		}
	})
}

// commitOn makes a branch with a commit of its own and returns its head, so
// two branches are distinguishable by the commit a run of each records.
func commitOn(t *testing.T, dir, branch, file string) string {
	t.Helper()
	git(t, dir, "checkout", "--quiet", "-b", branch)
	if err := writeFile(filepath.Join(dir, file), branch+"\n"); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "work on "+branch)
	return git(t, dir, "rev-parse", "HEAD")
}

// runAndEnd starts the branch's run and ends it, so the branch is left with a
// finished run a rerun can carry forward.
func runAndEnd(t *testing.T, client *ipc.Client, workingPath string) {
	t.Helper()
	started := startRun(t, client, workingPath)
	var ended machine.Run
	if err := client.Call(t.Context(), ipc.MethodRunCancel, machine.CancelRequest{Run: started.Record.ID}, &ended); err != nil {
		t.Fatalf("ending run %s: %v", started.Record.ID, err)
	}
	if ended.Record.Status != store.RunTerminated {
		t.Fatalf("the ended run is recorded as %s", ended.Record.Status)
	}
}

// rerun asks for a fresh run of the branch the working copy stands on.
func rerun(t *testing.T, client *ipc.Client, workingPath string) machine.Run {
	t.Helper()
	var run machine.Run
	if err := client.Call(t.Context(), ipc.MethodRunRerun, machine.RerunRequest{
		Working: machine.Working{WorkingPath: workingPath},
	}, &run); err != nil {
		t.Fatalf("asking for a rerun: %v", err)
	}
	return run
}

// A run's record says where its intent came from, and a run carrying intent
// text never records that nothing was given. The wire shape permits an intent
// offered without the supplied flag, so the three cases are three answers
// rather than two answers and a default.
func TestARunRecordsWhereItsIntentCameFrom(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		sources := map[string]string{}
		for _, c := range []struct {
			branch   string
			intent   string
			supplied bool
		}{
			{"with-criteria", "acceptance criteria stated up front", true},
			{"with-a-hint", "a hint about what this change is for", false},
			{"with-nothing", "", false},
		} {
			commitOn(t, subject, c.branch, c.branch+".txt")
			var run machine.Run
			if err := running.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
				Working:        machine.Working{WorkingPath: subject},
				Intent:         c.intent,
				IntentSupplied: c.supplied,
			}, &run); err != nil {
				t.Fatalf("starting the run for %s: %v", c.branch, err)
			}
			if run.Record.Intent != c.intent {
				t.Fatalf("the run for %s recorded the intent %q, want %q", c.branch, run.Record.Intent, c.intent)
			}
			if run.Record.IntentSource == "" {
				t.Fatalf("the run for %s records no intent source", c.branch)
			}
			sources[c.branch] = run.Record.IntentSource
			var ended machine.Run
			if err := running.client.Call(t.Context(), ipc.MethodRunCancel, machine.CancelRequest{Run: run.Record.ID}, &ended); err != nil {
				t.Fatalf("ending the run for %s: %v", c.branch, err)
			}
		}
		if sources["with-a-hint"] == sources["with-nothing"] {
			t.Fatalf("a run carrying intent text records %q, the same as a run given none",
				sources["with-a-hint"])
		}
		if sources["with-a-hint"] == sources["with-criteria"] {
			t.Fatalf("an intent offered as a hint records %q, the same as one stated as acceptance criteria",
				sources["with-a-hint"])
		}
	})
}
