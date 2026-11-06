package machine_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The coordinator's catch-up read is bounded: every list holds at most
// CatchUpRows rows and counts exactly the rest, and the task list's cut
// never hides a task a listed wake or hold names, keeping unfinished tasks
// ahead of finished ones.
func TestTheCoordinatorCatchUpIsBoundedAndKeepsWhatIsOwedAnAct(t *testing.T) {
	base := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	task := func(id string, state taskstate.State, age int) machine.Task {
		return machine.Task{Record: store.Task{ID: id, Project: "p", CreatedAt: base.Add(-time.Duration(age) * time.Minute)},
			State: taskstate.Resolution{State: state}}
	}
	var tasks []machine.Task
	// The oldest task of all is finished and named by a wake, so only the
	// rule that a named task is never cut keeps it.
	tasks = append(tasks, task("named-landed", taskstate.Landed, 10_000))
	for i := range machine.CatchUpRows {
		tasks = append(tasks, task(fmt.Sprintf("finished-%02d", i), taskstate.Complete, i))
	}
	for i := range machine.CatchUpRows {
		tasks = append(tasks, task(fmt.Sprintf("running-%02d", i), taskstate.Running, 1000+i))
	}
	var queued machine.QueuedWakes
	for i := range machine.CatchUpRows + 3 {
		queued.Wakes = append(queued.Wakes, machine.QueuedWake{Wake: store.Wake{ID: int64(i + 1), TaskID: "named-landed",
			Kind: string(supervisor.KindTerminalGone)}})
	}
	queued.DomainWakes = []store.DomainWake{{ID: 4, Domain: "alpha", Kind: string(supervisor.KindSubordinateGone)}}
	hold := store.Hold{Key: "task/running-07/stopped/1", TaskID: store.Known("running-07"), Subject: "whether to begin again"}

	got := machine.CoordinatorCatchUpOf(supervisor.Arm{Outcome: supervisor.ArmAttached}, queued,
		[]machine.Waiting{{Hold: &hold, Answer: machine.TaskAnswering("running-07")}}, tasks, nil)

	if len(got.Wakes) != machine.CatchUpRows || got.MoreWakes != 4 {
		t.Fatalf("the read lists %d wakes and %d more, want %d and 4", len(got.Wakes), got.MoreWakes, machine.CatchUpRows)
	}
	if got.Wakes[0].ID != "1" || got.Wakes[0].Task != "named-landed" {
		t.Fatalf("the first wake row is %+v, want wake 1 about named-landed", got.Wakes[0])
	}
	if len(got.Holds) != 1 || got.Holds[0].Key != hold.Key || got.Holds[0].Answer.Args[1] != "running-07" {
		t.Fatalf("the read lists holds %+v, want the one hold with its answer", got.Holds)
	}
	if len(got.Tasks) != machine.CatchUpRows || got.MoreTasks != len(tasks)-machine.CatchUpRows {
		t.Fatalf("the read lists %d tasks and %d more, want %d and %d", len(got.Tasks), got.MoreTasks,
			machine.CatchUpRows, len(tasks)-machine.CatchUpRows)
	}
	// The two named tasks first, newest first, then
	// the unfinished ones newest first; no finished task fits.
	if got.Tasks[0].ID != "running-07" || got.Tasks[1].ID != "named-landed" {
		t.Fatalf("the read's first tasks are %s and %s, want the two a wake or a hold names", got.Tasks[0].ID, got.Tasks[1].ID)
	}
	if got.Tasks[2].ID != "running-00" {
		t.Fatalf("the first unnamed task is %s, want the newest unfinished one", got.Tasks[2].ID)
	}
	for _, row := range got.Tasks[2:] {
		if row.State != taskstate.Running {
			t.Fatalf("a finished task %s was listed ahead of an unfinished one", row.ID)
		}
	}
}
