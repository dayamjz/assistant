package machine

import (
	"testing"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A task the program dispatched reads as waiting on kept work exactly when
// its answer names the entries holding it, and as a worker being launched
// otherwise; a task in any other state is phrased by its state whatever it
// carries. A run's answer has its kept work line exactly when its project
// holds an open entry.
func TestAHeldDispatchIsPhrasedAsWaitingOnKeptWork(t *testing.T) {
	t.Parallel()
	entries := []store.KeptWork{{ID: 1, CopyPath: "/c", Reason: "unproven"}}
	dispatched := taskstate.Resolution{State: taskstate.Dispatched, Source: taskstate.SourceEvent}
	if got := TaskPhrase(Task{State: dispatched, KeptWork: entries}); got != phraseTaskWaitsOnKeptWork {
		t.Fatalf("a held dispatch reads %q, want the kept work row", got)
	}
	if got := TaskPhrase(Task{State: dispatched}); got != phraseByState[taskstate.Dispatched] {
		t.Fatalf("a dispatch nothing holds reads %q, want the Dispatched row", got)
	}
	queued := taskstate.Resolution{State: taskstate.Queued, Source: taskstate.SourceEvent}
	if got := TaskPhrase(Task{State: queued, KeptWork: entries}); got != phraseByState[taskstate.Queued] {
		t.Fatalf("a queued task carrying kept work reads %q, want the Queued row", got)
	}
	if _, ok := RunKeptWorkPhrase(Run{}); ok {
		t.Fatal("a run whose project holds no kept work has a kept work line")
	}
	if got, ok := RunKeptWorkPhrase(Run{KeptWork: entries}); !ok || got != phraseRunKeptWork {
		t.Fatalf("a run whose project holds kept work reads %q, %v, want the kept work line", got, ok)
	}
}
