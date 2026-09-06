package service_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A run whose durable position is there and cannot be read is a failure to
// report, not a run that never executed. The two answers differ in what they
// tell a caller to do, and the wrong one tells them to end a run whose
// checkpoint history is intact and start again, which loses the work the
// history still holds.
func TestAPositionThatCannotBeReadIsRefusedRatherThanReportedAsARunThatNeverRan(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var runID string
	var stood int
	withService(t, h, func(running serviceUnderTest) {
		run := startRun(t, running.client, subject)
		runID = run.Record.ID
		stood = run.Steps
	})
	if stood == 0 {
		t.Fatal("the run spent no steps, so there is no position for this test to make unreadable")
	}

	// Put a checkpoint on the run's history that this build cannot decode,
	// which is what a row written by a newer build or a corrupt one looks
	// like. The payload is opaque to the store, so this is the shape the
	// decoder actually meets.
	records := openRecords(t, h)
	latest, err := records.LatestGraphCheckpoint(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading the run's tip: %v", err)
	}
	if _, err := records.AppendGraphCheckpoint(t.Context(), runID, runID, latest.Seq, []byte("not a checkpoint")); err != nil {
		t.Fatalf("appending an undecodable checkpoint: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	withService(t, h, func(running serviceUnderTest) {
		var run machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run)
		if err == nil {
			t.Fatalf("reading a run whose position does not decode answered %s: %s", run.Outcome, run.NextAction)
		}
		if !strings.Contains(err.Error(), runID) {
			t.Fatalf("the refusal does not name the run whose position could not be read: %v", err)
		}
	})

	// The history is left as it stands, so the position is still there for
	// somebody to look at rather than having been thrown away.
	records = openRecords(t, h)
	defer func() { _ = records.Close() }()
	history, err := records.GraphCheckpointHistory(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading the run's history: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("the run's history holds %d checkpoints, want the ones it stood at plus the unreadable one", len(history))
	}
}

// PRD section 8 has runs of one branch serialize. Two starts that arrive at
// once leave one run, because the check that a branch has no run and the
// creation of one are a single decision rather than a check the loser of a
// race also passes.
func TestTwoStartsOnOneBranchLeaveOneRun(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		second := dial(t, running.service)
		clients := []*ipc.Client{running.client, second}

		var wg sync.WaitGroup
		answers := make([]machine.Run, len(clients))
		failures := make([]error, len(clients))
		begin := make(chan struct{})
		for i, client := range clients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				failures[i] = client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
					Working: machine.Working{WorkingPath: subject},
					Intent:  "a change with acceptance criteria stated up front",
				}, &answers[i])
			}()
		}
		close(begin)
		wg.Wait()

		for i, err := range failures {
			if err != nil {
				t.Fatalf("start %d failed: %v", i, err)
			}
		}
		if answers[0].Record.ID != answers[1].Record.ID {
			t.Fatalf("two starts on one branch answered with two runs, %s and %s",
				answers[0].Record.ID, answers[1].Record.ID)
		}

		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		recorded, err := records.RunsForRepository(t.Context(), repository.ID)
		if err != nil {
			t.Fatalf("listing the repository's runs: %v", err)
		}
		if len(recorded) != 1 {
			t.Fatalf("two starts on one branch left %d runs: %s", len(recorded), branchesOf(recorded))
		}
	})
}

// branchesOf names what was recorded, for a failure that says what it found.
func branchesOf(records []store.Run) string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.ID+" on "+record.Branch+" ("+string(record.Status)+")")
	}
	return strings.Join(out, ", ")
}
