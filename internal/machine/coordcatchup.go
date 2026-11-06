package machine

import (
	"sort"
	"strconv"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// CoordinatorCatchUp is the coordinator's catch-up read, the answer to
// assistant coordinator catch-up: in one bounded document, the supervision
// verdict, the queued wakes, the open decisions, every task's resolved
// state and the open kept work, so a session that kept no memory of the
// fleet can act from it alone. Every row is compact (an identifier, what
// it is about, one line) and the full record is read per task with
// assistant --json coordinator tasks ID.
//
// It is a read: it hands out no wake, acknowledges none, answers nothing
// and marks nothing seen, so a drain, a wait, the wake stream and assistant
// watch see the same queue and seen mark after it as before. Each part is
// resolved from the records that own it on this read (P8), and it is not
// PRD section 9's catching up, CatchUp, which is the person's read and is
// filtered by their seen mark.
//
// Every list holds at most CatchUpRows rows and the More count beside it is
// exactly how many it left out, so the document's size is bounded whatever
// the fleet's history; CoordinatorCatchUpOf owns which rows are kept.
type CoordinatorCatchUp struct {
	// Supervision is the arm verdict over this home, as assistant
	// coordinator supervision answers it.
	Supervision supervisor.Arm `json:"supervision"`
	// Wakes are the queued wakes on offer to the coordinator, handed out
	// or not, oldest first within each shape.
	Wakes     []WakeRow `json:"wakes"`
	MoreWakes int       `json:"more_wakes"`
	// Holds are the open decisions, oldest first, each with its answer.
	Holds     []HoldRow `json:"holds"`
	MoreHolds int       `json:"more_holds"`
	// Tasks are the tasks with their resolved state, in
	// CoordinatorCatchUpOf's order.
	Tasks     []TaskRow `json:"tasks"`
	MoreTasks int       `json:"more_tasks"`
	// Kept are the open kept work entries, oldest first.
	Kept     []KeptRow `json:"kept"`
	MoreKept int       `json:"more_kept"`
}

// CatchUpRows is how many rows each list of the coordinator's catch-up read
// holds at most. It is a fixed count in the program and not a setting, on
// LandedLimit's terms; the rows past it are counted, never dropped silently.
const CatchUpRows = 50

// WakeRow is one queued wake, compact. ID is the name assistant coordinator
// acknowledge takes, which for a domain's, a kept work entry's or a
// repository's housekeeping wake carries its shape's prefix.
type WakeRow struct {
	ID string `json:"id"`
	// Task is the task the wake is about, and empty for the three shapes
	// that are about something else, which Line names.
	Task string `json:"task,omitempty"`
	// Kind is the wake's kind, in internal/supervisor's vocabulary.
	Kind string `json:"kind"`
	// Line is what the wake says, the phrase table's row for its kind.
	Line string `json:"line"`
}

// HoldRow is one open decision, compact: a hold record, or a run held at a
// decision its checkpoint records, which names Run and no Key.
type HoldRow struct {
	Key  string `json:"key,omitempty"`
	Task string `json:"task,omitempty"`
	Run  string `json:"run,omitempty"`
	// Subject and Detail are the hold's own sentences; Detail names the
	// answers a task's hold takes.
	Subject string `json:"subject"`
	Detail  string `json:"detail,omitempty"`
	// Answer is the existing answer path that closes it.
	Answer Answering `json:"answer"`
}

// TaskRow is one task's resolved state, compact.
type TaskRow struct {
	ID      string          `json:"id"`
	Project string          `json:"project"`
	Branch  string          `json:"branch,omitempty"`
	State   taskstate.State `json:"state"`
	// Line is TaskPhrase's reading of the task, with its next action.
	Line string `json:"line"`
}

// KeptRow is one open kept work entry, compact. Key is what --hold names
// when it is answered, given --task for an entry a task names and --run
// for one a run's copy left.
type KeptRow struct {
	ID     int64  `json:"id"`
	Key    string `json:"key"`
	Task   string `json:"task,omitempty"`
	Run    string `json:"run,omitempty"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
}

// CoordinatorCatchUpOf composes the read from what the service resolved,
// and decides nothing else: arm is the verdict, queued the queue as
// wakes.peek reads it, waiting the open decisions as CatchUp lists them,
// tasks every task as resolved, and kept the open entries.
//
// Wakes, holds and kept work keep their order and are cut at CatchUpRows.
// Tasks are ordered so the cut never hides a task the coordinator owes an
// act on: a task a listed wake, hold or kept row names comes first and is
// never cut, then the unfinished tasks newest first, then the finished ones
// (Landed and Complete) newest first, and the cut takes from the end.
func CoordinatorCatchUpOf(arm supervisor.Arm, queued QueuedWakes, waiting []Waiting, tasks []Task, kept []store.KeptWork) CoordinatorCatchUp {
	out := CoordinatorCatchUp{Supervision: arm, Wakes: []WakeRow{}, Holds: []HoldRow{}, Tasks: []TaskRow{}, Kept: []KeptRow{}}
	var wakes []WakeRow
	for _, w := range queued.Wakes {
		wakes = append(wakes, WakeRow{ID: strconv.FormatInt(w.Wake.ID, 10), Task: w.Wake.TaskID, Kind: w.Wake.Kind, Line: QueuedWakePhrase(w).Outcome})
	}
	for _, w := range queued.DomainWakes {
		wakes = append(wakes, WakeRow{ID: DomainWakePrefix + strconv.FormatInt(w.ID, 10), Kind: w.Kind, Line: w.Domain + ": " + QueuedDomainWakePhrase(w).Outcome})
	}
	for _, w := range queued.KeptWorkWakes {
		wakes = append(wakes, WakeRow{ID: KeptWorkWakePrefix + strconv.FormatInt(w.ID, 10), Kind: w.Kind, Line: QueuedKeptWorkWakePhrase(w).Outcome})
	}
	for _, w := range queued.HousekeepingWakes {
		wakes = append(wakes, WakeRow{ID: HousekeepingWakePrefix + strconv.FormatInt(w.ID, 10), Kind: w.Kind, Line: w.Repository + ": " + QueuedHousekeepingWakePhrase(w).Outcome})
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
		named[w.Task] = w.Task != ""
	}
	for _, h := range out.Holds {
		named[h.Task] = named[h.Task] || h.Task != ""
	}
	for _, k := range out.Kept {
		named[k.Task] = named[k.Task] || k.Task != ""
	}
	rank := func(t Task) int {
		switch {
		case named[t.Record.ID]:
			return 0
		case t.State.State == taskstate.Landed || t.State.State == taskstate.Complete:
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
