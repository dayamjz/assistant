package supervisor

import (
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/taskstate"
)

// The stall hold, PRD section 6's "a stalled worker holds for you": the hold
// a surface of KindTerminalUnchanged at SurfaceInspectAndHold registers,
// naming the task. This file is the one owner of its key space and of the
// two words the program closes one with, because three readers must agree on
// which holds are stall holds: internal/service's loop, which registers and
// closes them and answers them, the stop guard's in-flight reading through
// feed, and the catch-up read, which lists the tasks in flight. Composing or
// parsing the key anywhere else would be a second owner (P14).
//
// The key is the attempt and the surface that reached the count: the change
// epoch the markers carried for the screen and the surface's number in the
// count, which are the components of that surface's own wake marker. So the
// same stall found again at a later pass or after a restart composes the
// same key, which internal/store's registration finds already there, open
// or answered, and a stall that reaches the count again after an approve is
// at a later surface number, so it is a new hold.

// stallHoldSpace is the key space of the stall holds, under the task's own
// "task/<id>" prefix the other task holds use.
const stallHoldSpace = "/stalled/"

// StallHold is what a stall hold's key says: the task it names, the attempt
// whose worker stalled, and the surface that reached the count, as the change
// epoch of the screen and the surface's number in the count of it.
type StallHold struct {
	Task    string
	Attempt int64
	Epoch   int64
	Surface int64
}

// Key composes the hold's key. ParseStallHold is its inverse.
func (h StallHold) Key() string {
	return "task/" + h.Task + stallHoldSpace + strconv.FormatInt(h.Attempt, 10) + "/" +
		strconv.FormatInt(h.Epoch, 10) + "/" + strconv.FormatInt(h.Surface, 10)
}

// ParseStallHold reads a stall hold's key back, and reports false for any key
// Key would not compose: another key space, a missing or extra component, or
// a component that is not a number.
func ParseStallHold(key string) (StallHold, bool) {
	rest, ok := strings.CutPrefix(key, "task/")
	if !ok {
		return StallHold{}, false
	}
	task, tail, ok := strings.Cut(rest, stallHoldSpace)
	if !ok || task == "" || strings.Contains(task, "/") {
		return StallHold{}, false
	}
	parts := strings.Split(tail, "/")
	if len(parts) != 3 {
		return StallHold{}, false
	}
	var nums [3]int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != p {
			return StallHold{}, false
		}
		nums[i] = n
	}
	h := StallHold{Task: task, Attempt: nums[0], Epoch: nums[1], Surface: nums[2]}
	return h, h.Key() == key
}

// The resolutions the program records when it closes a stall hold itself,
// PRD section 6's "a stall that ends by itself closes the hold": the screen
// read changed, or the worker read dead. Each is recorded with the program as
// the resolver and what it read as the note; neither is an answer a person
// gives, and the answer set below refuses both.
const (
	// StallClosedScreenChanged is a supervision pass that read the worker's
	// screen changed. It is the closure that surfaces as KindStallClosed.
	StallClosedScreenChanged = "screen-changed"
	// StallClosedWorkerDead is a probe that read the worker dead. It raises
	// no wake of its own: the dead terminal surfaces as KindTerminalGone.
	StallClosedWorkerDead = "worker-dead"
)

// OpenStallHolds is every open stall hold naming the observed task, oldest
// first in the order the snapshot lists holds, read off the task's own
// snapshot: a hold whose key parses as a stall hold of this task and that
// names no run. Nothing in this build registers more than one open at a time,
// because a task with one open resolves Held and a stall holds only a task
// that resolves Running.
func OpenStallHolds(o taskstate.Observed) []StallHold {
	var out []StallHold
	for _, h := range o.Snapshot.Holds {
		if !h.Open() || h.RunID.IsKnown() {
			continue
		}
		if task, ok := h.TaskID.Get(); !ok || task != o.Snapshot.Task.ID {
			continue
		}
		if s, ok := ParseStallHold(h.Key); ok && s.Task == o.Snapshot.Task.ID {
			out = append(out, s)
		}
	}
	return out
}

// StallHeld reports whether the observed task has an open stall hold, which
// PRD section 6 makes in flight whatever its state and its queue say: its
// worker is still running, so the stop guard counts it and every pass reads
// its terminal.
func StallHeld(o taskstate.Observed) bool { return len(OpenStallHolds(o)) > 0 }
