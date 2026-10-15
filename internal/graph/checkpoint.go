package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Status is where a run stood when a checkpoint was written.
type Status uint8

const (
	// StatusInvalid is the zero Status and never appears in a valid checkpoint.
	StatusInvalid Status = iota
	// StatusRunning means the run has more work to do at the recorded position.
	StatusRunning
	// StatusCompleted means the run reached a terminal node, one that declares
	// no outgoing edge. Any other node declares an unconditional edge, so a
	// run leaving it always has somewhere to go.
	StatusCompleted
	// StatusHalted means the run stopped before the node at the recorded
	// position, which has not started, and is waiting on the open decision.
	StatusHalted
	// StatusRoundsExhausted means a back edge reached the bound it declared.
	StatusRoundsExhausted
	// StatusBudgetExhausted means the run reached its run-wide step budget.
	StatusBudgetExhausted
	// StatusConverged means a round crossed a back edge without changing state.
	// A back edge marked Edge.Answered never produces it.
	StatusConverged
)

// String returns the wire name of the status, which is also what appears in
// diagnostics.
func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusHalted:
		return "halted"
	case StatusRoundsExhausted:
		return "rounds_exhausted"
	case StatusBudgetExhausted:
		return "budget_exhausted"
	case StatusConverged:
		return "converged"
	case StatusInvalid:
		return "invalid"
	default:
		return "status(" + strconv.Itoa(int(s)) + ")"
	}
}

// Stopped reports whether the status is one a run does not advance out of on
// its own. It covers both of the ways PRD section 5 distinguishes, which is
// why it is not named after either: StatusHalted is a hold, waiting on a
// person's decision, and the other three are parks, a bound stopping the run.
//
// A halted run needs a decision. A parked run needs something other than an
// answer, because Answer takes only a halted run: Executor.AdoptBudget moves a
// budget-exhausted one onto more room where it stands, and any of the three is
// taken further by forking the run from an earlier checkpoint and resuming
// that, which runs on the counters that checkpoint recorded rather than the
// ones the park is standing on.
func (s Status) Stopped() bool {
	switch s {
	case StatusHalted, StatusRoundsExhausted, StatusBudgetExhausted, StatusConverged:
		return true
	case StatusInvalid, StatusRunning, StatusCompleted:
		return false
	default:
		return false
	}
}

// Parked reports whether the status is one of the three parks: a bound, not a
// person, stopped the run. It is Stopped without StatusHalted, which is the
// hold, and it names which bound only through the status itself, so a caller
// that needs the bound reads Checkpoint.Park or Result.Park rather than
// switching on the status beside this.
func (s Status) Parked() bool {
	switch s {
	case StatusRoundsExhausted, StatusBudgetExhausted, StatusConverged:
		return true
	case StatusInvalid, StatusRunning, StatusCompleted, StatusHalted:
		return false
	default:
		return false
	}
}

// EdgeRef names one declared edge by its endpoints and the round bound it
// declares. It is how a checkpoint records which edge's bound parked a run,
// in terms a reader holding no graph can read; Graph.Validate holds a
// checkpoint's EdgeRef to an edge the graph actually declares.
type EdgeRef struct {
	// From is the node the edge leaves.
	From string `json:"from"`
	// To is the node the edge enters.
	To string `json:"to"`
	// Rounds is the round bound the edge declares, zero for an edge that
	// declares none.
	Rounds int `json:"rounds"`
}

// clone returns an independent copy of the reference.
func (r *EdgeRef) clone() *EdgeRef {
	if r == nil {
		return nil
	}
	out := *r
	return &out
}

// Park is which of the three bounds parked a run, and where, as the
// checkpoint that recorded the park says. It is read off a checkpoint or a
// result and is never stored on its own, so it cannot disagree with the
// checkpoint it describes.
type Park struct {
	// Status is the park itself, one of the three statuses Parked accepts:
	// StatusRoundsExhausted for an edge's round limit, StatusConverged for a
	// round that changed nothing, StatusBudgetExhausted for the run-wide step
	// budget.
	Status Status
	// Node is the node the run stands in front of, which has not run.
	Node string
	// Edge is the edge whose bound parked the run, for a round limit or a
	// convergence park. It is nil for a budget park, which is no edge's bound,
	// and for an edge park recorded by a build that did not yet write
	// Checkpoint.ParkedAt: the park is then known and its edge is not.
	Edge *EdgeRef
	// Budget is the run-wide step budget the run was held to when it parked.
	Budget int
}

// MarshalJSON writes the status as its wire name.
func (s Status) MarshalJSON() ([]byte, error) {
	switch s {
	case StatusRunning, StatusCompleted, StatusHalted,
		StatusRoundsExhausted, StatusBudgetExhausted, StatusConverged:
		return json.Marshal(s.String())
	case StatusInvalid:
		return nil, fmt.Errorf("cannot encode status %s", s)
	default:
		return nil, fmt.Errorf("cannot encode status %s", s)
	}
}

// UnmarshalJSON reads a wire name. An unrecognized name is refused rather than
// defaulted, because a checkpoint is untrusted input.
func (s *Status) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	switch name {
	case "running":
		*s = StatusRunning
	case "completed":
		*s = StatusCompleted
	case "halted":
		*s = StatusHalted
	case "rounds_exhausted":
		*s = StatusRoundsExhausted
	case "budget_exhausted":
		*s = StatusBudgetExhausted
	case "converged":
		*s = StatusConverged
	default:
		return fmt.Errorf("unrecognized status %q", name)
	}
	return nil
}

// CheckpointID identifies one checkpoint within one run. Seq is assigned by
// the store in write order and starts at 1.
type CheckpointID struct {
	// Run is the run the checkpoint belongs to.
	Run string `json:"run"`
	// Seq is the checkpoint's position in that run's history, from 1.
	Seq int `json:"seq"`
}

// String renders the identifier as "run#seq".
func (id CheckpointID) String() string { return id.Run + "#" + strconv.Itoa(id.Seq) }

// Decision is the open question a halted run is waiting on. It is a copy of
// the halt point's declaration, so restoring a checkpoint re-emits exactly
// what the run stopped for without consulting anything else.
type Decision struct {
	// Node is the node the run stopped before.
	Node string `json:"node"`
	// Question is the decision put to whoever answers it.
	Question string `json:"question"`
	// Options, when non-empty, is the closed set of permitted answers.
	Options []string `json:"options,omitempty"`
	// Into is the state key the answer is written to.
	Into string `json:"into"`
	// Detailed is the answers that must come with a detail, which
	// Executor.AnswerWith writes into the halt point's Halt.Detail key. It is
	// empty for a halt point that takes none, and a checkpoint written before
	// it existed reads back that way.
	Detailed []string `json:"detailed,omitempty"`
}

// equals reports whether two decisions are the same question in the same place.
func (d *Decision) equals(other *Decision) bool {
	if d == nil || other == nil {
		return d == other
	}
	if d.Node != other.Node || d.Question != other.Question || d.Into != other.Into {
		return false
	}
	return sameStrings(d.Options, other.Options) && sameStrings(d.Detailed, other.Detailed)
}

// sameStrings reports whether two lists hold the same strings in the same order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// clone returns an independent copy of the decision.
func (d *Decision) clone() *Decision {
	if d == nil {
		return nil
	}
	out := *d
	out.Options = append([]string(nil), d.Options...)
	out.Detailed = append([]string(nil), d.Detailed...)
	return &out
}

// Counters is the bound accounting a run must not lose across a resume. It
// travels in the checkpoint because a resume that restarted the counters would
// leave the run with bounds that no longer bound anything.
//
// Carrying a count is only half of it. A count means nothing without the bound
// it is compared against and without the edge it accrued on, so both of those
// travel here too: Budget is the run-wide bound Steps is spent against, and
// EdgeDigest says which edge vector Traversals and Fingerprints are indexed
// against. A resume that read either of them from somewhere else would leave
// the run with bounds that still count but no longer bound what they were
// counting.
type Counters struct {
	// Steps is how many node executions the run has spent against its
	// run-wide budget.
	Steps int `json:"steps"`
	// Budget is the run-wide step budget this run is held to. It is recorded
	// when the run starts and is the bound Steps is measured against for the
	// rest of the run, so a resume is bounded by what the run began under
	// rather than by whatever the resuming executor was configured with.
	// Executor.AdoptBudget is the one way it changes, and it changes by
	// writing a checkpoint that says so.
	Budget int `json:"budget"`
	// Traversals counts, per edge index, how many times the edge was taken.
	Traversals []int `json:"traversals"`
	// Fingerprints holds, per edge index, the state fingerprint recorded the
	// last time the edge was traversed. The convergence check compares against
	// it. An empty entry means the edge has not been traversed.
	Fingerprints []string `json:"fingerprints"`
	// EdgeDigest fingerprints the edge vector the two vectors above are
	// indexed against. Both are positional, so a graph whose edges differ
	// would read them against edges that did not produce them; the digest is
	// what makes that refusable rather than invisible. Comparing lengths does
	// not settle it, because a change that drops one edge and adds another
	// leaves the length alone.
	EdgeDigest string `json:"edge_digest"`
	// Unattended records that the latest answer the run took was given by
	// Executor.AnswerUnattended, so a traversal of an Edge.Answered back edge
	// is held to the convergence check rather than exempt from it. Every
	// answer writes it on the checkpoint that claims the run, true for
	// AnswerUnattended and false for Answer and AnswerWith, and the build
	// admits Answered only on an edge every traversal of which follows an
	// answer, so the value a traversal reads is always its own answer's. A
	// checkpoint written before the field existed carries false, which is the
	// exemption every answer had then.
	Unattended bool `json:"unattended,omitempty"`
}

// clone returns counters whose slices are independent of these, so a value
// already handed to a store does not change as the run spends more of its
// bounds.
func (c Counters) clone() Counters {
	out := c
	out.Traversals = append([]int(nil), c.Traversals...)
	out.Fingerprints = append([]string(nil), c.Fingerprints...)
	return out
}

// newCounters returns the accounting a run of g starts with under the given
// run-wide step budget: no steps spent, no edge traversed, and both the bound
// and the edge vector this run's counters will be read against recorded.
func (g *Graph) newCounters(budget int) Counters {
	return Counters{
		Budget:       budget,
		Traversals:   make([]int, len(g.edges)),
		Fingerprints: make([]string, len(g.edges)),
		EdgeDigest:   g.digest,
	}
}

// Checkpoint is state, position, and any open decision, written after every
// node and once more when a segment claims the run before starting one. It
// carries the run's bound accounting as well, because those counters are part
// of what must survive a resume.
//
// A Checkpoint serializes as data and nothing else. It is validated against
// the graph it claims to belong to before a run is resumed from it.
type Checkpoint struct {
	// Run is the run this checkpoint belongs to.
	Run string `json:"run"`
	// Seq is this checkpoint's position in the run's history, from 1. The
	// store assigns it.
	Seq int `json:"seq"`
	// Position is the node the run has not yet run. It is empty exactly when
	// the run completed.
	Position string `json:"position"`
	// Status is where the run stood when the checkpoint was written.
	Status Status `json:"status"`
	// Reason explains a run that stopped short. The executor sets one only
	// when a bound parks the run, and clears it on any status the run can
	// advance out of on its own. Validation asks the wider question of
	// Status.Stopped, so a checkpoint built by hand or read out of a store may
	// carry one on a halted run; see Graph.validateReason.
	Reason string `json:"reason,omitempty"`
	// ParkedAt is the edge whose bound parked the run. The executor sets it
	// exactly when it parks a run on a round limit or on convergence, and
	// clears it on every other status, so a budget park, which no edge
	// caused, never carries one. A checkpoint written before the field
	// existed reads back without it on any status; Graph.Validate refuses one
	// that names an edge the graph does not declare, or that stands anywhere
	// but where that edge leads.
	ParkedAt *EdgeRef `json:"parked_at,omitempty"`
	// State is the typed record as of this point.
	State State `json:"state"`
	// Decision is the open decision, non-nil exactly when Status is
	// StatusHalted.
	Decision *Decision `json:"decision,omitempty"`
	// Counters is the run's bound accounting.
	Counters Counters `json:"counters"`
	// ForkedFrom names the checkpoint this one was copied from, set only on
	// checkpoints a fork produced.
	ForkedFrom *CheckpointID `json:"forked_from,omitempty"`
}

// clone returns a checkpoint that shares nothing mutable with this one, so a
// value crossing the store boundary in either direction can be acted on
// without reaching into what the other side still holds. Every field that
// carries a reference is copied here, which is what keeps a field added to
// Checkpoint from needing the same copy written again somewhere else.
func (c Checkpoint) clone() Checkpoint {
	out := c
	out.State = c.State.Clone()
	out.Decision = c.Decision.clone()
	out.Counters = c.Counters.clone()
	out.ParkedAt = c.ParkedAt.clone()
	if c.ForkedFrom != nil {
		origin := *c.ForkedFrom
		out.ForkedFrom = &origin
	}
	return out
}

// ID returns the checkpoint's identifier.
func (c Checkpoint) ID() CheckpointID { return CheckpointID{Run: c.Run, Seq: c.Seq} }

// Park reports which bound parked the run this checkpoint describes, and false
// when its status is not a park.
func (c Checkpoint) Park() (Park, bool) {
	if !c.Status.Parked() {
		return Park{}, false
	}
	return Park{Status: c.Status, Node: c.Position, Edge: c.ParkedAt.clone(), Budget: c.Counters.Budget}, true
}

// Result renders the checkpoint as the result a caller of the executor sees,
// for a caller that read the checkpoint from a store rather than receiving it
// from a segment. It shares nothing mutable with the checkpoint.
func (c Checkpoint) Result() Result {
	return Result{
		Status:     c.Status,
		Position:   c.Position,
		Decision:   c.Decision.clone(),
		State:      c.State.Clone(),
		Checkpoint: c.ID(),
		Steps:      c.Counters.Steps,
		Budget:     c.Counters.Budget,
		Reason:     c.Reason,
		ParkedAt:   c.ParkedAt.clone(),
	}
}

// checkpointJSON is the wire shape. It exists so decoding can refuse unknown
// fields without the alias recursing into Checkpoint's own unmarshaler.
type checkpointJSON struct {
	Run        string        `json:"run"`
	Seq        int           `json:"seq"`
	Position   string        `json:"position"`
	Status     Status        `json:"status"`
	Reason     string        `json:"reason,omitempty"`
	ParkedAt   *EdgeRef      `json:"parked_at,omitempty"`
	State      State         `json:"state"`
	Decision   *Decision     `json:"decision,omitempty"`
	Counters   Counters      `json:"counters"`
	ForkedFrom *CheckpointID `json:"forked_from,omitempty"`
}

// UnmarshalJSON reads a checkpoint, refusing unknown fields and unrecognized
// enumerations. This is the structural half of validating a checkpoint on
// read; Graph.Validate is the half that needs a graph.
func (c *Checkpoint) UnmarshalJSON(b []byte) error {
	var raw checkpointJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("decode checkpoint: %w", err)
	}
	*c = Checkpoint(raw)
	return nil
}

// encodeCheckpoint renders a checkpoint as the bytes a store holds.
func encodeCheckpoint(c Checkpoint) ([]byte, error) {
	b, err := json.Marshal(checkpointJSON(c))
	if err != nil {
		return nil, fmt.Errorf("encode checkpoint: %w", err)
	}
	return b, nil
}

// decodeCheckpoint reads the bytes a store holds back into a checkpoint.
func decodeCheckpoint(b []byte) (Checkpoint, error) {
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return Checkpoint{}, err
	}
	return c, nil
}
