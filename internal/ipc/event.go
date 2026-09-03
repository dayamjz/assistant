package ipc

import (
	"encoding/json"
	"fmt"
)

// Class says what a bounded stream is allowed to do with an event when a
// subscriber cannot keep up. It is the whole of the overflow policy: the
// stream never asks what an event means, only what class it is.
type Class uint8

const (
	// ClassState is a delta to something a consumer holds. It carries a
	// revision and may only be applied to state the consumer has reconciled.
	// It is the zero value on purpose, so an event whose class was never
	// established is state, and state is never dropped without a marker.
	ClassState Class = iota
	// ClassActivity is progress: log lines, output, and heartbeats. It tells a
	// consumer what is happening and nothing it holds depends on it, so it is
	// the only class an overflowing stream may discard.
	ClassActivity
	// ClassControl is about the delivery channel or the session rather than
	// about the work. A consumer cannot recover a control event by
	// reconciling, because there is no state to read it back out of, so it is
	// never discarded.
	ClassControl
)

// String renders the class as it appears in diagnostics.
func (c Class) String() string {
	switch c {
	case ClassState:
		return "state"
	case ClassActivity:
		return "activity"
	case ClassControl:
		return "control"
	default:
		return fmt.Sprintf("class(%d)", uint8(c))
	}
}

// Droppable reports whether an overflowing stream may discard an event of this
// class outright, and it is what Subscription.deliver asks rather than a second
// statement of the same rule. Only activity is droppable. State may be evicted
// only when that eviction collapses into the gap marker its consumer reconciles
// from, which is a different question and lives with the eviction. Control is
// never discarded at all: a queue that holds only control ends the subscription
// instead.
func (c Class) Droppable() bool { return c == ClassActivity }

// Type names one kind of event. The vocabulary is closed in the sense that
// this table is its only owner, and open in the sense that a type this build
// does not know is classified as state rather than rejected.
type Type string

const (
	// TypeLogLine is one line of a stage's log.
	TypeLogLine Type = "log.line"
	// TypeAgentOutput is a chunk of an agent invocation's output.
	TypeAgentOutput Type = "agent.output"
	// TypeHeartbeat is a periodic sign of life from a running stage.
	TypeHeartbeat Type = "stage.heartbeat"

	// TypeRunState is a change to a run's status, head, or position.
	TypeRunState Type = "run.state"
	// TypeStageState is a change to one stage's status, duration, or counts.
	TypeStageState Type = "stage.state"
	// TypeFindings is a change to the finding set a stage produced.
	TypeFindings Type = "run.findings"
	// TypeHoldState is a decision opening or closing.
	TypeHoldState Type = "hold.state"
	// TypeTaskState is a change to a task's resolved current state.
	TypeTaskState Type = "task.state"

	// TypeGap says the stream discarded something. It is produced by this
	// package rather than published, and a consumer that receives one must
	// reconcile before applying another delta.
	TypeGap Type = "stream.gap"
	// TypeServiceStopping says the service is going away and every consumer
	// should detach. Nothing a consumer can read back reports it, which is why
	// it is control rather than state.
	TypeServiceStopping Type = "service.stopping"
)

// classes is the one place a type's class is written down.
var classes = map[Type]Class{
	TypeLogLine:         ClassActivity,
	TypeAgentOutput:     ClassActivity,
	TypeHeartbeat:       ClassActivity,
	TypeRunState:        ClassState,
	TypeStageState:      ClassState,
	TypeFindings:        ClassState,
	TypeHoldState:       ClassState,
	TypeTaskState:       ClassState,
	TypeGap:             ClassControl,
	TypeServiceStopping: ClassControl,
}

// Classify reports the class of a type. A type this build does not recognize
// is state, which is the answer that cannot lose anything: a peer running a
// newer build can add an event type, and this one will retain it and force a
// reconcile rather than discard it as progress noise.
func Classify(t Type) Class {
	c, ok := classes[t]
	if !ok {
		return ClassState
	}
	return c
}

// Types returns every type this build classifies, for diagnostics and tests.
// The order is unspecified.
func Types() []Type {
	out := make([]Type, 0, len(classes))
	for t := range classes {
		out = append(out, t)
	}
	return out
}

// Event is one thing that happened, as it travels on a stream.
type Event struct {
	// Type names the event. An empty type is invalid and cannot be published.
	Type Type `json:"type"`
	// Revision orders state events. It is the revision of the state the delta
	// produces, and a consumer applies a delta only when it is newer than what
	// the consumer already holds. It is zero for activity and control, and a
	// state event may not be published without one.
	Revision uint64 `json:"revision,omitempty"`
	// Payload is the event's body, left as written so this package never
	// becomes a second owner of anything's shape.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Class reports how a bounded stream may treat this event.
func (e Event) Class() Class { return Classify(e.Type) }

// Gap is the body of a TypeGap event. It reports how many events the stream
// discarded since the last marker it delivered, which is diagnostic: the count
// does not say which events they were, and a consumer must reconcile rather
// than reason about it.
type Gap struct {
	// Dropped is the number of events discarded since the previous marker.
	Dropped uint64 `json:"dropped"`
}

// GapPayload reads the body of a TypeGap event. It reports an error for an
// event of any other type, and for a marker whose body does not decode.
func GapPayload(e Event) (Gap, error) {
	if e.Type != TypeGap {
		return Gap{}, fmt.Errorf("ipc: %q is not a gap marker", e.Type)
	}
	var g Gap
	if len(e.Payload) == 0 {
		return g, nil
	}
	if err := json.Unmarshal(e.Payload, &g); err != nil {
		return Gap{}, fmt.Errorf("ipc: gap marker payload: %w", err)
	}
	return g, nil
}

// gapEvent builds the marker delivered for a run of discarded events. It is
// built when the marker is delivered rather than when a drop happens, so
// nothing on the publishing path marshals anything.
func gapEvent(dropped uint64) Event {
	payload, err := json.Marshal(Gap{Dropped: dropped})
	if err != nil {
		// Gap is one uint64 field, so this is unreachable. A marker with no
		// body still reads as a gap, which is what the consumer must act on.
		return Event{Type: TypeGap}
	}
	return Event{Type: TypeGap, Payload: payload}
}
