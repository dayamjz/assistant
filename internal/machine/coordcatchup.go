package machine

import (
	"sort"
	"strconv"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// CoordinatorCatchUp is assistant coordinator catch-up: the arm verdict, the
// queued wakes, the open decisions, every task's state and the open kept
// work, as compact rows a session with no memory of the fleet acts from. It
// hands out, acknowledges, answers and marks nothing, and is not CatchUp, the
// person's read. Each list holds at most CatchUpRows rows, its More count is
// exactly how many were left out, and CoordinatorCatchUpOf owns which are kept.
type CoordinatorCatchUp struct {
	// Supervision is the verdict assistant coordinator supervision gives.
	Supervision supervisor.Arm `json:"supervision"`
	Wakes       []WakeRow      `json:"wakes"`
	MoreWakes   int            `json:"more_wakes"`
	Holds       []HoldRow      `json:"holds"`
	MoreHolds   int            `json:"more_holds"`
	Tasks       []TaskRow      `json:"tasks"`
	MoreTasks   int            `json:"more_tasks"`
	Kept        []KeptRow      `json:"kept"`
	MoreKept    int            `json:"more_kept"`
}

// CatchUpRows bounds each list of CoordinatorCatchUp; it is not a setting.
const CatchUpRows = 50

// WakeRow is one queued wake. ID is the name acknowledge takes, prefixed for
// the three shapes no task names; Line is the phrase table's row for it.
type WakeRow struct {
	ID   string `json:"id"`
	Task string `json:"task,omitempty"`
	Kind string `json:"kind"`
	Line string `json:"line"`
}

// HoldRow is one open decision: a hold record, or a run held at a decision
// its checkpoint records, which names Run and no Key. Subject and Detail
// are the hold's own sentences, and Answer is the existing answer path.
type HoldRow struct {
	Key     string    `json:"key,omitempty"`
	Task    string    `json:"task,omitempty"`
	Run     string    `json:"run,omitempty"`
	Subject string    `json:"subject"`
	Detail  string    `json:"detail,omitempty"`
	Answer  Answering `json:"answer"`
}

// TaskRow is one task's resolved state; Line is TaskPhrase's reading of it,
// with its next action.
type TaskRow struct {
	ID      string          `json:"id"`
	Project string          `json:"project"`
	Branch  string          `json:"branch,omitempty"`
	State   taskstate.State `json:"state"`
	Line    string          `json:"line"`
}

// KeptRow is one open kept work entry. Key is what --hold names when it is
// answered, given --task for an entry a task names and --run otherwise.
type KeptRow struct {
	ID     int64  `json:"id"`
	Key    string `json:"key"`
	Task   string `json:"task,omitempty"`
	Run    string `json:"run,omitempty"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
}

// CoordinatorCatchUpOf compacts and bounds what the service resolved: the
// queue as wakes.peek reads it, the decisions as CatchUp lists them, every
// task and the open kept work. Wakes, holds and kept work keep their order.
// Tasks a listed wake, hold or kept row names come first and are never cut,
// then unfinished tasks newest first, then Landed and Complete ones newest
// first, and the cut takes from the end.
func CoordinatorCatchUpOf(arm supervisor.Arm, queued QueuedWakes, waiting []Waiting, tasks []Task, kept []store.KeptWork) CoordinatorCatchUp {
	out := CoordinatorCatchUp{Supervision: arm, Wakes: []WakeRow{}, Holds: []HoldRow{}, Tasks: []TaskRow{}, Kept: []KeptRow{}}
	var wakes []WakeRow
	row := func(prefix string, id int64, task, kind, line string) {
		wakes = append(wakes, WakeRow{ID: prefix + strconv.FormatInt(id, 10), Task: task, Kind: kind, Line: line})
	}
	for _, w := range queued.Wakes {
		row("", w.Wake.ID, w.Wake.TaskID, w.Wake.Kind, QueuedWakePhrase(w).Outcome)
	}
	for _, w := range queued.DomainWakes {
		row(DomainWakePrefix, w.ID, "", w.Kind, w.Domain+": "+QueuedDomainWakePhrase(w).Outcome)
	}
	for _, w := range queued.KeptWorkWakes {
		row(KeptWorkWakePrefix, w.ID, "", w.Kind, QueuedKeptWorkWakePhrase(w).Outcome)
	}
	for _, w := range queued.HousekeepingWakes {
		row(HousekeepingWakePrefix, w.ID, "", w.Kind, w.Repository+": "+QueuedHousekeepingWakePhrase(w).Outcome)
	}
	out.Wakes, out.MoreWakes = cut(out.Wakes, wakes)

	var holds []HoldRow
	for _, w := range waiting {
		switch {
		case w.Hold != nil:
			holds = append(holds, HoldRow{Key: w.Hold.Key, Task: w.Hold.TaskID.Or(""), Run: w.Hold.RunID.Or(""),
				Subject: w.Hold.Subject, Detail: w.Hold.Detail, Answer: w.Answer})
		case w.Run != nil:
			subject := w.Run.Phrase()
			if w.Run.Decision != nil {
				subject = DecisionPhrase(*w.Run.Decision)
			}
			holds = append(holds, HoldRow{Run: w.Run.Record.ID, Subject: subject.String(), Answer: w.Answer})
		}
	}
	out.Holds, out.MoreHolds = cut(out.Holds, holds)

	var keptRows []KeptRow
	for _, k := range kept {
		keptRows = append(keptRows, KeptRow{ID: k.ID, Key: k.Key(), Task: k.TaskID.Or(""), Run: k.RunID.Or(""),
			Branch: k.Branch.Or(""), Reason: k.Reason})
	}
	out.Kept, out.MoreKept = cut(out.Kept, keptRows)

	named := map[string]bool{}
	for _, w := range out.Wakes {
		named[w.Task] = true
	}
	for _, h := range out.Holds {
		named[h.Task] = true
	}
	for _, k := range out.Kept {
		named[k.Task] = true
	}
	rank := func(t Task) int {
		if named[t.Record.ID] {
			return 0
		}
		if t.State.State == taskstate.Landed || t.State.State == taskstate.Complete {
			return 2
		}
		return 1
	}
	ordered := append([]Task(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := rank(ordered[i]), rank(ordered[j])
		if ri != rj {
			return ri < rj
		}
		if a, b := ordered[i].Record.CreatedAt, ordered[j].Record.CreatedAt; !a.Equal(b) {
			return a.After(b)
		}
		return ordered[i].Record.ID < ordered[j].Record.ID
	})
	for i, t := range ordered {
		if rank(t) != 0 && i >= CatchUpRows {
			out.MoreTasks = len(ordered) - i
			break
		}
		out.Tasks = append(out.Tasks, TaskRow{ID: t.Record.ID, Project: t.Record.Project, Branch: t.Record.Branch.Or(""),
			State: t.State.State, Line: TaskPhrase(t).String()})
	}
	return out
}

// cut appends at most CatchUpRows of rows to into and counts the rest.
func cut[T any](into, rows []T) ([]T, int) {
	if len(rows) > CatchUpRows {
		return append(into, rows[:CatchUpRows]...), len(rows) - CatchUpRows
	}
	return append(into, rows...), 0
}
