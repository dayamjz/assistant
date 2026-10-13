package taskstate

import (
	"slices"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

// State is where a task stands, in PRD section 6's lifecycle vocabulary. It is
// a closed set: a word outside States is not a State this package produces.
type State string

// The states a resolution can name. Landed and Complete are in the lifecycle
// and are deliberately absent: they are resolved from the merge record and
// the cleanup record in the tier above the run tier, which this build does not
// read (the package comment says what each waits on), so a state that could
// not be established is not declared rather than declared and never produced.
// Unknown is the resolver declining to say.
const (
	Queued     State = "queued"
	Dispatched State = "dispatched"
	Running    State = "running"
	Held       State = "held"
	Paused     State = "paused"
	Validating State = "validating"
	Ready      State = "ready"
	Reported   State = "reported"
	Failed     State = "failed"
	Unknown    State = "unknown"
)

var states = []State{
	Queued, Dispatched, Running, Held, Paused, Validating, Ready, Reported, Failed, Unknown,
}

// States returns the closed set in the order above. The result is a copy.
func States() []State { return slices.Clone(states) }

// Recognized reports whether s is one of States.
func (s State) Recognized() bool { return slices.Contains(states, s) }

// Source says which input decided the state. The values are PRD section 6's
// precedence tiers, in precedence order.
type Source string

// The sources. SourceNone accompanies Unknown when no tier had anything to say.
const (
	SourceRun     Source = "run"
	SourceProcess Source = "process"
	SourceEvent   Source = "event"
	SourceNone    Source = "none"
)

// Liveness is what a task's terminal looked like when it was asked about.
// Unobserved is not Dead: it means nobody asked, or nothing could answer, and
// the process tier then declines rather than reading absence as a stopped
// terminal.
type Liveness string

// The liveness answers. Any value other than Live or Dead is read as
// Unobserved.
const (
	Unobserved Liveness = "unobserved"
	// Live means the agent process is running in the session: a probe that
	// finds only a shell in the pane, with the agent gone, answers Dead. A
	// session whose shell survives its agent is the ordinary shape of a
	// worker that finished or crashed, and reading it as Live would be the
	// stale evidence the process tier exists to refuse.
	Live Liveness = "live"
	Dead Liveness = "dead"
)

// Inputs is everything Resolve reads. It is a value: Resolve takes no context,
// opens nothing and reads no clock.
type Inputs struct {
	// Task is the task record.
	Task store.Task
	// Runs are the runs attributed to the task, in any order and any status.
	Runs []store.Run
	// Holds are the holds naming the task or one of Runs. Only an open one is
	// read, and only for the detail of a held answer.
	Holds []store.Hold
	// Events are the task's whole history, in any order; Resolve orders them
	// by sequence itself.
	Events []store.TaskEvent
	// Process is what the terminal looked like, as of when it was asked.
	Process Liveness
	// Advancing is whether something is executing the newest of Runs now: the
	// service's own fact about the slot a run advances in, which no record
	// holds. It is read only when the run tier answers Validating, and it is
	// what tells a run that is being driven from one that is merely recorded
	// as running.
	Advancing bool
}

// Resolution is the answer: one state, the source that decided it, and what was
// read to decide.
type Resolution struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// State is what is true now.
	State State `json:"state"`
	// Source says which tier decided it.
	Source Source `json:"source"`
	// Detail is what the source has to add, and it is unknown when it has
	// nothing. It never carries the text of an event: a worker's words are
	// history, not part of an answer.
	Detail store.Optional[string] `json:"detail"`
	// Progress is whether the answer rests on positive evidence that work is
	// proceeding, which PRD section 6's supervisor absorbs a wake on and
	// nothing else. It is derived here and nowhere else, and it is true in
	// exactly two cases: Validating from SourceRun with Inputs.Advancing, and
	// Running from SourceProcess with a Live terminal. Every other answer is
	// no progress, including a run the service says it is advancing while
	// the record says it has settled: the record decided the state, and a
	// slot that disagrees with it is not evidence.
	Progress bool `json:"progress"`
	// Superseded lists the events that claim something a higher source
	// overrode: a decision still open in the log, and the newest event when it
	// maps to a state of its own that differs from State. Empty when the event
	// log decided or nothing outranked it.
	Superseded []Superseded `json:"superseded"`
	// Basis is what was read. Equal Basis from equal records gives an equal
	// Resolution, so it is the cheap way to ask whether an answer could have
	// changed; it is not a revision and carries no order.
	Basis Basis `json:"basis"`
}

// Superseded is one event whose claim a higher source overrode.
type Superseded struct {
	// Sequence and Kind identify the event. Its text is not carried.
	Sequence int64  `json:"sequence"`
	Kind     string `json:"kind"`
	// Claimed is the state the event's kind maps to.
	Claimed State `json:"claimed"`
	// By is the tier whose answer overrode it.
	By Source `json:"by"`
}

// Basis is the facts a Resolution was computed from.
type Basis struct {
	// RunID and RunStatus name the attributed run that was read, and are empty
	// when none was.
	RunID        string          `json:"run_id"`
	RunStatus    store.RunStatus `json:"run_status"`
	RunUpdatedAt time.Time       `json:"run_updated_at"`
	// EventSequence is the highest event sequence read, zero for an empty log.
	EventSequence int64 `json:"event_sequence"`
	// Process is the liveness that was read.
	Process Liveness `json:"process"`
	// Advancing is the Inputs.Advancing that was read.
	Advancing bool `json:"advancing"`
}
