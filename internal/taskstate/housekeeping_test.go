package taskstate_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// RunSettled is runStates' reading of which statuses a run has ended in, and
// internal/runs' move table owns which statuses a run moves out of: the two
// answer alike for every status, so the pass's "every attributed run has
// ended" cannot mean one thing to the supervisor and another to the store.
func TestRunSettledIsTheMoveTablesEnd(t *testing.T) {
	t.Parallel()
	for _, status := range store.RunStatuses() {
		if got, want := taskstate.RunSettled(status), runs.Finished(status); got != want {
			t.Errorf("RunSettled(%s) = %v, and runs.Finished says %v", status, got, want)
		}
	}
	if taskstate.RunSettled("parked-forever") {
		t.Error("a status nobody defined reads as settled")
	}
}

// Delivered is the terminal tier and nothing else: an investigation's
// findings relayed record, and a landed run's merge record, put a task
// there; a task held, reported, or with a passed run does not reach it.
func TestDeliveredIsTheTerminalTierAlone(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	investigation := store.Task{ID: "t", Shape: store.TaskInvestigation}
	delivery := store.Task{ID: "t", Shape: store.TaskDelivery}
	passed := store.Run{ID: "r", Status: store.RunPassed, CreatedAt: at}
	for name, c := range map[string]struct {
		snap store.TaskSnapshot
		want bool
	}{
		"relayed": {store.TaskSnapshot{Task: investigation, ReportStoredAt: store.Known(at),
			Relay: store.Known(store.TaskRelay{TaskID: "t", RecordedAt: at})}, true},
		"merged": {store.TaskSnapshot{Task: delivery, Runs: []store.Run{passed},
			MergeRecords: []store.MergeRecord{{Run: "r", End: store.PullRequestMerged}}}, true},
		"closed unmerged": {store.TaskSnapshot{Task: delivery, Runs: []store.Run{passed},
			MergeRecords: []store.MergeRecord{{Run: "r", End: store.PullRequestClosed}}}, false},
		"passed":   {store.TaskSnapshot{Task: delivery, Runs: []store.Run{passed}}, false},
		"reported": {store.TaskSnapshot{Task: investigation, ReportStoredAt: store.Known(at)}, false},
		"held":     {store.TaskSnapshot{Task: investigation, Holds: []store.Hold{{Key: "k", TaskID: store.Known("t"), Subject: "s"}}}, false},
	} {
		if got := taskstate.Delivered(c.snap); got != c.want {
			t.Errorf("%s: Delivered = %v, want %v", name, got, c.want)
		}
	}
}

// PRD section 6's housekeeping: a pass that resolves Held from its hold tier
// while the service advances its newest run, which has not settled, rests on
// positive evidence, and the same reading of a task that is no pass does
// not, nor a pass whose run is not being advanced or has settled.
func TestAHeldPassWhoseRunAdvancesIsProgress(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	hold := store.Hold{Key: "k", TaskID: store.Known("t"), Subject: "a report decision"}
	in := func(pass, advancing bool, status store.RunStatus) taskstate.Inputs {
		return taskstate.Inputs{
			Task:      store.Task{ID: "t", Shape: store.TaskDelivery, Housekeeping: pass},
			Runs:      []store.Run{{ID: "r", Status: status, CreatedAt: at}},
			Holds:     []store.Hold{hold},
			Process:   taskstate.Live,
			Advancing: advancing,
		}
	}
	got := taskstate.Resolve(in(true, true, store.RunRunning))
	if got.State != taskstate.Held || got.Source != taskstate.SourceHold || !got.Progress {
		t.Fatalf("a held pass whose run advances resolves %+v, want Held from its hold with progress", got)
	}
	for name, c := range map[string]taskstate.Inputs{
		"no pass":       in(false, true, store.RunRunning),
		"not advancing": in(true, false, store.RunRunning),
		"a settled run": in(true, true, store.RunFailed),
	} {
		if r := taskstate.Resolve(c); r.Progress {
			t.Errorf("%s: %+v reads as progress", name, r)
		}
	}
}
