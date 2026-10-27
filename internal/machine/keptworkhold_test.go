package machine

import (
	"strings"
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
	if got := TaskPhrase(Task{State: dispatched, KeptWorkUnread: true}); got != phraseTaskKeptWorkUnread {
		t.Fatalf("a dispatch whose hold could not be read reads %q, want the row saying so", got)
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

// Every row that tells a person a new task waits on kept work says both
// ways the wait ends in this build, the finding of #468's fresh review: the
// work decided, or its work turning up somewhere safe, which the cleanup's
// removal on a proof is and which is the one close this build performs.
// phraseTaskWaitsOnKeptWork already said both; status's line, the catch-up
// part, the run's line and the notice said only "decided", so a person
// whose run was cancelled read that the project stays blocked until an
// answer this build does not take, when pushing the kept commit lifts it.
func TestEveryRowSayingANewTaskWaitsSaysHowTheWaitEnds(t *testing.T) {
	t.Parallel()
	rows := append(keptWorkRows(), phraseTaskWaitsOnKeptWork)
	waits := 0
	for _, row := range rows {
		for _, sentence := range []string{row.Outcome, row.Consequence, row.Decision} {
			if !strings.Contains(sentence, "new task") && !strings.Contains(sentence, "begins on its own") {
				continue
			}
			waits++
			if !strings.Contains(sentence, "decided") || !strings.Contains(sentence, "turns up somewhere safe") {
				t.Errorf("%q says a task waits on kept work without saying it ends once the work is decided or turns up somewhere safe", sentence)
			}
		}
	}
	if waits != 5 {
		t.Fatalf("%d sentences say a task waits on kept work; want the five rows that do, each once", waits)
	}
}
