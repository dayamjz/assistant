package machine_test

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The coordinator's catch-up read is bounded: every list holds at most
// CatchUpRows rows and counts exactly the rest. The task list puts the tasks
// a listed wake or hold names first, then unfinished tasks ahead of finished
// ones, so with fewer named tasks than CatchUpRows, as here, the cut falls on
// finished ones; TestTheCoordinatorCatchUpTaskListNeverPassesItsBound shows a
// named task cut once more than CatchUpRows are named.
func TestTheCoordinatorCatchUpPutsNamedAndUnfinishedTasksFirst(t *testing.T) {
	base := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	task := func(id string, state taskstate.State, age int) machine.Task {
		return machine.Task{Record: store.Task{ID: id, Project: "p", CreatedAt: base.Add(-time.Duration(age) * time.Minute)},
			State: taskstate.Resolution{State: state}}
	}
	var tasks []machine.Task
	// The oldest task of all is finished and named by a wake, so only the
	// rule that a named task comes first keeps it.
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

// The task list holds at most CatchUpRows rows even when more tasks than
// that are named by listed wakes and holds: the named ones come first,
// newest first, and the rest are counted, so the document's bound holds and
// a named task can be left out. Here the oldest named task, which only a
// listed hold names, is the one cut: its hold is listed and its task is
// counted in more_tasks, to be read with assistant --json coordinator tasks.
func TestTheCoordinatorCatchUpTaskListNeverPassesItsBound(t *testing.T) {
	base := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	var tasks []machine.Task
	var queued machine.QueuedWakes
	for i := range machine.CatchUpRows {
		id := fmt.Sprintf("woken-%02d", i)
		tasks = append(tasks, machine.Task{Record: store.Task{ID: id, CreatedAt: base}, State: taskstate.Resolution{State: taskstate.Unknown}})
		queued.Wakes = append(queued.Wakes, machine.QueuedWake{Wake: store.Wake{ID: int64(i + 1), TaskID: id, Kind: string(supervisor.KindTerminalGone)}})
	}
	tasks = append(tasks, machine.Task{Record: store.Task{ID: "held", CreatedAt: base.Add(-time.Hour)}, State: taskstate.Resolution{State: taskstate.Held}})
	hold := store.Hold{Key: "task/held/stopped/1", TaskID: store.Known("held"), Subject: "whether to begin again"}

	got := machine.CoordinatorCatchUpOf(supervisor.Arm{}, queued, []machine.Waiting{{Hold: &hold}}, tasks, nil)
	if len(got.Tasks) != machine.CatchUpRows || got.MoreTasks != 1 {
		t.Fatalf("the read lists %d tasks and %d more, want %d and 1", len(got.Tasks), got.MoreTasks, machine.CatchUpRows)
	}
	if len(got.Holds) != 1 || got.Holds[0].Task != "held" {
		t.Fatalf("the read lists holds %+v, want the hold on the task it cut", got.Holds)
	}
	for _, row := range got.Tasks {
		if row.ID == "held" {
			t.Fatalf("the oldest named task was listed past %d newer named ones", machine.CatchUpRows)
		}
	}
}

// A wake about a kept work entry no task names says which entry it is
// about, so a session can tell which wake to acknowledge once it has acted
// on an entry: two queues that pair the same wakes with the entries the
// other way round read differently.
func TestAKeptWorkWakeRowNamesItsEntry(t *testing.T) {
	read := func(first, second int64) []byte {
		t.Helper()
		queued := machine.QueuedWakes{KeptWorkWakes: []store.KeptWorkWake{
			{ID: 1, KeptWork: first, Branch: store.Known("feature/a"), Kind: string(supervisor.KindKeptWork)},
			{ID: 2, KeptWork: second, Branch: store.Known("feature/a"), Kind: string(supervisor.KindKeptWork)},
		}}
		var b bytes.Buffer
		if err := machine.NewEncoder(&b).Encode(machine.CoordinatorCatchUpOf(supervisor.Arm{}, queued, nil, nil, nil).Wakes); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	if a, b := read(2, 1), read(1, 2); bytes.Equal(a, b) {
		t.Fatalf("wakes k1 and k2 read the same whichever entry each is about:\n%s", a)
	}
}

// Every shape's row carries what the skill acts on it by: a task wake's
// marker, a domain wake's domain, and a housekeeping wake's repository and
// the pass it follows.
func TestEveryWakeRowCarriesWhatItIsActedOnBy(t *testing.T) {
	queued := machine.QueuedWakes{
		Wakes:             []machine.QueuedWake{{Wake: store.Wake{ID: 1, TaskID: "t", Kind: string(supervisor.KindTerminalGone), Marker: "terminal-gone:dead:ref"}}},
		DomainWakes:       []store.DomainWake{{ID: 2, Domain: "alpha", Kind: string(supervisor.KindSubordinateGone)}},
		HousekeepingWakes: []store.HousekeepingWake{{ID: 3, Repository: "repo", Pass: store.Known("pass1"), Kind: "housekeeping-due", Detail: "why"}},
	}
	got := machine.CoordinatorCatchUpOf(supervisor.Arm{}, queued, nil, nil, nil).Wakes
	want := []machine.WakeRow{
		{ID: "1", Task: "t", Kind: string(supervisor.KindTerminalGone), Marker: "terminal-gone:dead:ref"},
		{ID: "d2", Kind: string(supervisor.KindSubordinateGone), Domain: "alpha"},
		{ID: "h3", Kind: "housekeeping-due", Repository: "repo", Pass: "pass1", Detail: "why"},
	}
	for i := range want {
		got[i].Line = ""
		if got[i] != want[i] {
			t.Errorf("row %d reads %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The catch-up read's counts row is a row of the phrase table, with and
// without rows left out, so the table's vocabulary guard holds it.
func TestTheCatchUpCountsRowIsInThePhraseTable(t *testing.T) {
	table := machine.TablePhrases()
	for _, read := range []machine.CoordinatorCatchUp{
		{Wakes: make([]machine.WakeRow, 1)},
		{Wakes: make([]machine.WakeRow, 1), MoreTasks: 1},
	} {
		if p := machine.CoordinatorCatchUpPhrase(read); !slices.Contains(table, p) {
			t.Errorf("the counts row %q is not in TablePhrases", p)
		}
	}
}

// A run held at a decision is listed with the decision itself, the question
// and the findings behind it as the stage reported them, so a session can
// answer or relay it from the read alone.
func TestAHeldRunsRowCarriesItsDecision(t *testing.T) {
	const question = "Is deleting all customer backups intentional?"
	decision := machine.Decision{Decision: graph.Decision{Node: "review", Question: question, Options: []string{"approved", "fix"}},
		Stage: "review", Findings: []findings.Finding{{ID: "f1", Action: findings.ActionAsk, Description: question}}}
	run := machine.Run{Record: store.Run{ID: "r1"}, Decision: &decision}
	got := machine.CoordinatorCatchUpOf(supervisor.Arm{}, machine.QueuedWakes{}, []machine.Waiting{{Run: &run}}, nil, nil)
	var b bytes.Buffer
	if err := machine.NewEncoder(&b).Encode(got.Holds); err != nil {
		t.Fatal(err)
	}
	if len(got.Holds) != 1 || got.Holds[0].Subject != question || !strings.Contains(b.String(), `"findings":[{"id":"f1"`) {
		t.Fatalf("the held run reads %s, want its question as the subject and its findings", b.String())
	}
}
