package taskstate_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

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
