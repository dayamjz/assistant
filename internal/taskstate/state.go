package taskstate

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
)

// State is what a task is doing now: the closed vocabulary Resolve answers in.
type State string

// The states. Unknown is an answer, not an absence of one: it says the
// evidence does not support any other state.
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

var states = []State{Queued, Dispatched, Running, Held, Paused, Validating, Ready, Reported, Failed, Unknown}

// States returns the closed set. The result is a copy.
func States() []State { return slices.Clone(states) }

// Recognized reports whether s is one of the states above.
func (s State) Recognized() bool { return slices.Contains(states, s) }

// Source says which rung of PRD section 6's precedence answered.
type Source string

// The sources, in the order of that precedence.
const (
	// SourceRun means a validation run attributed to the task answered.
	SourceRun Source = "run"
	// SourceProcess means the task's terminal answered: it is live, or it is
	// dead and nothing outranked that.
	SourceProcess Source = "process"
	// SourceEvent means the newest event of the log maps to a state.
	SourceEvent Source = "event"
	// SourceNone means there was no evidence to answer from.
	SourceNone Source = "none"
)

var sources = []Source{SourceRun, SourceProcess, SourceEvent, SourceNone}

// Recognized reports whether s is one of the sources above.
func (s Source) Recognized() bool { return slices.Contains(sources, s) }

// Liveness is what a probe of the task's terminal found. Unobserved is not
// Dead: a terminal nobody could check says nothing about whether it is alive,
// and only a positive finding of death outranks the event log.
type Liveness int

// The findings.
const (
	Unobserved Liveness = iota
	Live
	Dead
)

// Superseded is one event the resolver reports as outranked: it claimed a
// state, and a higher rung of the precedence answered differently.
type Superseded struct {
	// Sequence and Kind identify the event in the task's history.
	Sequence int64  `json:"sequence"`
	Kind     string `json:"kind"`
	// Claimed is the state the event's kind maps to.
	Claimed State `json:"claimed"`
	// By is the source that answered instead.
	By Source `json:"by"`
}

// Basis records what an answer was derived from, so a reader can tell which
// input moved. It is not part of the answer: a settle whose answer is
// unchanged refreshes it without moving the revision.
type Basis struct {
	// RunID and RunStatus are the newest attributed run, when there was one.
	RunID     string `json:"run_id,omitempty"`
	RunStatus string `json:"run_status,omitempty"`
	// EventSeq is the sequence of the newest event, zero for an empty log.
	EventSeq int64 `json:"event_seq"`
	// Process is the liveness the answer was resolved under.
	Process Liveness `json:"process"`
}

// Resolution is the resolver's answer: the state, which rung gave it, and what
// it outranked.
type Resolution struct {
	TaskID string
	State  State
	Source Source
	// Detail is anything the source wants to add. It comes from the run record
	// and an open hold's subject, or from the answering event; never from an
	// event that only closes a decision.
	Detail store.Optional[string]
	// Superseded lists, in sequence order, the events outranked by the answer.
	Superseded []Superseded
	Basis      Basis
}

// sameAnswer reports whether two resolutions agree on everything that is the
// answer, which excludes Basis.
func (r Resolution) sameAnswer(o Resolution) bool {
	return r.TaskID == o.TaskID && r.State == o.State && r.Source == o.Source &&
		r.Detail == o.Detail && slices.Equal(r.Superseded, o.Superseded)
}

// write converts a resolution to what the store keeps, encoding the two
// structured fields as JSON text the store stores without reading.
func (r Resolution) write() (store.TaskStateWrite, error) {
	list := r.Superseded
	if list == nil {
		list = []Superseded{}
	}
	sup, err := json.Marshal(list)
	if err != nil {
		return store.TaskStateWrite{}, fmt.Errorf("taskstate: encoding what task %s superseded: %w", r.TaskID, err)
	}
	basis, err := json.Marshal(r.Basis)
	if err != nil {
		return store.TaskStateWrite{}, fmt.Errorf("taskstate: encoding the basis of task %s: %w", r.TaskID, err)
	}
	return store.TaskStateWrite{
		State: string(r.State), Source: string(r.Source), Detail: r.Detail,
		Superseded: store.Known(string(sup)), Basis: store.Known(string(basis)),
	}, nil
}
