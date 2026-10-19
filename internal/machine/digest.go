package machine

import (
	"fmt"
	"sort"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// DigestLimit is how many in-flight tasks, open holds, and unacknowledged
// wakes one digest shows. It is a fixed count in the program and not a
// setting, on the same terms as LandedLimit. The rest are counted in the
// More fields and stay in the records.
const DigestLimit = 10

// FleetDigest is one reading of a fleet listing. Counts are the resolved
// states of the tasks handed to DigestOf, one row per state in
// taskstate.States and a row for any other state those tasks named, so a
// state with nothing in it is said. InFlight, Holds and Wakes are bounded
// by DigestLimit. Nothing here authorizes work: the digest names no answer
// and acknowledges no wake.
//
// A task resolved Running without Resolution.Progress is counted under
// Running and in that row's Unproven. Progress is true for Running only when
// the terminal was observed live, so a worker whose terminal was not seen,
// including one the event log still describes as running, is not a worker
// seen succeeding.
type FleetDigest struct {
	// Counts is every resolved state, in taskstate.States order, then any
	// state outside that set. Nil means the answer was built without a
	// digest, which only a test fixture does.
	Counts []StateCount `json:"counts"`
	// InFlight is the tasks supervisor.TaskInFlight counts, identifier
	// order, at most DigestLimit of them. A task the table cannot decide
	// is listed too, with Undecided set, because an undecided reading is
	// not a reading that the task is idle.
	InFlight []DigestTask `json:"in_flight"`
	// InFlightMore is how many in-flight tasks InFlight does not show.
	InFlightMore int `json:"in_flight_more"`
	// Holds are the open holds, oldest first, at most DigestLimit of them,
	// with subject and detail redacted. A hold that is not open is not
	// listed.
	Holds []store.Hold `json:"holds"`
	// HoldsMore is how many open holds Holds does not show.
	HoldsMore int `json:"holds_more"`
	// Wakes are the unacknowledged wakes, oldest first, at most DigestLimit
	// of them. An acknowledged wake is not listed. Kind and marker are
	// redacted.
	Wakes []store.Wake `json:"wakes"`
	// WakesMore is how many unacknowledged wakes Wakes does not show.
	WakesMore int `json:"wakes_more"`
}

// StateCount is how many listed tasks resolved to one state.
type StateCount struct {
	// State is the resolved state.
	State taskstate.State `json:"state"`
	// Count is how many listed tasks resolved to State.
	Count int `json:"count"`
	// Unproven is how many of Count are Running without Resolution.Progress.
	// It is zero for every other state.
	Unproven int `json:"unproven,omitempty"`
}

// DigestTask is one task the in-flight rule counts. State is that task's
// resolved state from the same Task DigestOf was given, not a second
// reading. A wake whose task is not in the listing has an empty state and
// Undecided set: the digest does not invent a state for it.
type DigestTask struct {
	// Task is the task's identifier.
	Task string `json:"task"`
	// State is the resolved state, empty when the task was not in the listing.
	State taskstate.State `json:"state,omitempty"`
	// Proven is the resolution's Progress. For Running it is true only when
	// the terminal was observed live.
	Proven bool `json:"proven"`
	// Undecided is set when supervisor.TaskInFlight could not decide the
	// state. The task is listed because that is not a reading that it is idle.
	Undecided bool `json:"undecided,omitempty"`
}

// DigestOf composes a fleet digest from records the caller already read.
// tasks are the listing's resolved tasks. wakeCounts is how many
// unacknowledged wakes each task has, as store.UnacknowledgedWakes returns
// it. holds are the holds the caller read; only the open ones are listed.
// queued are unacknowledged wake rows, and queuedTotal is how many
// unacknowledged wakes there are, which may be more than len(queued) when
// the caller already kept only the oldest DigestLimit. A total smaller than
// the rows handed over is read as the length of those rows.
//
// It reads no event log. A task's state is the resolution it carries, so a
// newer event the resolution superseded does not change the count. Which
// tasks are in flight is supervisor.TaskInFlight, the same rule the catch-up
// read and the pull guard use. Subject, detail, kind and marker are passed
// through internal/redact before they are copied, and the caller's values
// are left as they were.
//
// The result authorizes nothing.
func DigestOf(tasks []Task, wakeCounts map[string]int, holds []store.Hold, queued []store.Wake, queuedTotal int) FleetDigest {
	if wakeCounts == nil {
		wakeCounts = map[string]int{}
	}
	redactor := redact.New()
	byState := make(map[taskstate.State]StateCount, len(taskstate.States()))
	for _, state := range taskstate.States() {
		byState[state] = StateCount{State: state}
	}
	seen := make(map[string]bool, len(tasks))
	var moving []DigestTask
	for _, task := range tasks {
		seen[task.Record.ID] = true
		state := task.State.State
		row := byState[state]
		row.State = state
		row.Count++
		if state == taskstate.Running && !task.State.Progress {
			row.Unproven++
		}
		byState[state] = row
		n := wakeCounts[task.Record.ID]
		inFlight, decided := supervisor.TaskInFlight(state, n)
		if inFlight || !decided {
			moving = append(moving, DigestTask{
				Task:      task.Record.ID,
				State:     state,
				Proven:    task.State.Progress,
				Undecided: !decided,
			})
		}
	}
	for id, n := range wakeCounts {
		if n <= 0 || seen[id] {
			continue
		}
		inFlight, decided := supervisor.TaskInFlight("", n)
		if inFlight || !decided {
			moving = append(moving, DigestTask{Task: id, Undecided: !decided})
		}
	}
	sort.SliceStable(moving, func(i, j int) bool { return moving[i].Task < moving[j].Task })

	var open []store.Hold
	for _, h := range holds {
		if !h.Open() {
			continue
		}
		copied := h
		copied.Subject = redactor.Redact(h.Subject)
		copied.Detail = redactor.Redact(h.Detail)
		open = append(open, copied)
	}
	sort.SliceStable(open, func(i, j int) bool {
		if !open[i].OpenedAt.Equal(open[j].OpenedAt) {
			return open[i].OpenedAt.Before(open[j].OpenedAt)
		}
		return open[i].Key < open[j].Key
	})

	var wakes []store.Wake
	for _, w := range queued {
		if !w.Queued() {
			continue
		}
		copied := w
		copied.Kind = redactor.Redact(w.Kind)
		copied.Marker = redactor.Redact(w.Marker)
		wakes = append(wakes, copied)
	}
	sort.SliceStable(wakes, func(i, j int) bool { return wakes[i].ID < wakes[j].ID })

	counts := make([]StateCount, 0, len(byState))
	for _, state := range taskstate.States() {
		counts = append(counts, byState[state])
		delete(byState, state)
	}
	var extra []StateCount
	for _, row := range byState {
		extra = append(extra, row)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].State < extra[j].State })
	counts = append(counts, extra...)

	inFlight, inFlightMore := bound(moving, DigestLimit)
	shownHolds, holdsMore := bound(open, DigestLimit)
	shownWakes, _ := bound(wakes, DigestLimit)
	total := queuedTotal
	if total < len(wakes) {
		total = len(wakes)
	}
	wakesMore := total - len(shownWakes)
	if wakesMore < 0 {
		wakesMore = 0
	}
	return FleetDigest{
		Counts:       counts,
		InFlight:     inFlight,
		InFlightMore: inFlightMore,
		Holds:        shownHolds,
		HoldsMore:    holdsMore,
		Wakes:        shownWakes,
		WakesMore:    wakesMore,
	}
}

func bound[T any](in []T, limit int) (shown []T, more int) {
	if len(in) > limit {
		return append([]T(nil), in[:limit]...), len(in) - limit
	}
	return append([]T(nil), in...), 0
}

// DigestPhrase is what a person reads about a fleet digest. It names counts
// and no identifier, state, hold or wake, which the structured answer
// carries. The two wordings are the two rows DigestPhrases lists: one where
// no Running task lacks progress, and one where some do, which says those
// are not read as succeeding.
func DigestPhrase(d FleetDigest) Phrase {
	outcome := fmt.Sprintf("%d piece(s) of fleet work are on record, and %d of them are moving.", d.total(), d.moving())
	consequence := fmt.Sprintf("%d decision(s) are waiting on you, and %d notice(s) are still unanswered. This reading starts nothing and answers nothing.", d.holds(), d.wakes())
	if n := d.unproven(); n > 0 {
		consequence = fmt.Sprintf("%d of them have a history that says they are at work while their terminal was not seen, so they are not read as succeeding. ", n) + consequence
	}
	return Phrase{
		Outcome:     outcome,
		Consequence: consequence,
		Decision:    "Nothing to decide from this line. The lines under it say what each piece needs, and assistant tasks --json carries the counts, which pieces are moving, which decisions are open and which notices are unanswered.",
	}
}

// DigestPhrases returns the two rows TablePhrases lists for a fleet digest:
// the row with nothing unproven, and the row that withholds a succeeding
// reading. The numbers are the placeholders the table lists, on
// LandedMorePhrase's terms.
func DigestPhrases() []Phrase {
	return []Phrase{
		DigestPhrase(FleetDigest{}),
		DigestPhrase(FleetDigest{Counts: []StateCount{{State: taskstate.Running, Count: 1, Unproven: 1}}}),
	}
}

func (d FleetDigest) total() int {
	n := 0
	for _, c := range d.Counts {
		n += c.Count
	}
	return n
}

func (d FleetDigest) moving() int { return len(d.InFlight) + d.InFlightMore }

func (d FleetDigest) holds() int { return len(d.Holds) + d.HoldsMore }

func (d FleetDigest) wakes() int { return len(d.Wakes) + d.WakesMore }

func (d FleetDigest) unproven() int {
	n := 0
	for _, c := range d.Counts {
		if c.State == taskstate.Running {
			n += c.Unproven
		}
	}
	return n
}
