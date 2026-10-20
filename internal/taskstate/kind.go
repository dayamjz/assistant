package taskstate

import (
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
)

// Role is what an event kind does to the resolver.
type Role string

// The roles. Only RoleState and RoleDecisionOpen can answer at the event tier.
const (
	// RoleState is a kind that claims a state of its own.
	RoleState Role = "state"
	// RoleDecisionOpen is a kind that says a decision is waiting. It claims
	// Held when it answers, and is superseded when a higher source moved on.
	RoleDecisionOpen Role = "decision-open"
	// RoleDecisionClosed is a kind that closes a decision. It is decision-only
	// (PRD section 6): it never becomes current state.
	RoleDecisionClosed Role = "decision-closed"
	// RoleNote is a kind that is history and claims nothing.
	RoleNote Role = "note"
)

// Writer is who may append an event kind. It is the table's answer to the
// question a worker's event path has to ask before it records what a worker
// said: a kind the program appends about its own decisions is not one a
// worker may claim.
type Writer string

// The writers.
const (
	// WriterProgram is a kind only the program appends: an intake, a
	// dispatch, a relaunch, or the isolation assertion. A worker's event path
	// refuses it, because a worker that could append "dispatched" could
	// claim a decision the program never made.
	WriterProgram Writer = "program"
	// WriterWorker is a kind a worker appends about its own work. The program
	// may append one too, when it is recording something it observed on the
	// worker's behalf.
	WriterWorker Writer = "worker"
)

// Row is one event kind's meaning.
type Row struct {
	// Kind is the event kind as it is appended.
	Kind string
	// Role is what the kind does.
	Role Role
	// State is the state the kind claims. It is empty for the roles that claim
	// none.
	State State
	// Writer is who may append the kind.
	Writer Writer
	// Routine is whether an entry of the kind is the program's record of an
	// act the coordinator asked for or the program made on its own schedule,
	// which tells the coordinator nothing it does not already have: the
	// intake's entry and the dispatch's. Such an entry needs no decision and
	// asks nobody anything, so internal/supervisor's Event reads it here and
	// its classifier absorbs it, which queues no wake for it; what it claims
	// for the resolver is Role's and
	// State's and is unchanged by this. Only a WriterProgram kind may be
	// routine, which a test holds the table to, and a program kind that can
	// report something going wrong, the isolation refusal, is not.
	Routine bool
	// DeclaredWait is whether the kind is a worker's own declared external
	// wait, PRD section 5's "a declared wait is not a stall". As the most
	// recent event it outranks the terminal's live process signal in
	// Resolve, so a worker that said it is waiting resolves Paused while its
	// terminal runs, and never a dead terminal or an unobserved one, which
	// resolve as they do without it. Only a RoleState kind claiming Paused
	// that a worker appends may be one, which a test holds the table to.
	DeclaredWait bool
	// Detail validates the detail text an event of this kind may carry, and
	// is nil for a kind whose detail is free text. A kind with a decoder has
	// a closed detail vocabulary, which a writer checks before appending
	// because the store records any text and this table is the only place
	// the vocabulary is defined. Resolve never reads a detail: the decoder
	// is for the writer and for whatever renders the history.
	Detail func(detail string) error
}

// IsolationRefusal is why a dispatch refused to launch a worker, the closed
// set of reasons PRD principle P11's assertion can give. It is the detail of
// an "isolation-refused" event, so the reason travels in the history in a
// word this package defines rather than in the asserting code's prose.
//
// The producer is internal/service's dispatch, which maps the comparison
// internal/tasks' assertion failed on to one of these through tasks.Refusal
// and appends the kind anchored to the resolution it dispatched from. Adding
// a reason is adding a row here, where the kind's decoder reads it, and a
// case to that mapping.
type IsolationRefusal string

const (
	// RefusedPrimaryCheckout is the resolved path is the primary checkout
	// itself, inside it, or containing it, which no worker may work in.
	RefusedPrimaryCheckout IsolationRefusal = "primary-checkout"
	// RefusedNotIsolated is the resolved path is not a repository of its own:
	// it does not exist as a repository, it is a linked copy that the tool
	// can be pointed back at its own repository through, which P11 says is
	// not proof on its own, it is not the copy the task was reserved, it
	// stands inside the gate repository, or the environment the worker would
	// launch with points git at another repository.
	RefusedNotIsolated IsolationRefusal = "not-isolated"
	// RefusedCopyMissing is the resolved path does not exist. A dispatch
	// never produces it, because it cuts the copy when its path is absent
	// and asserts the path that is there; a relaunch does, because PRD
	// section 11 relaunches in that same copy and a copy that no longer
	// stands is refused rather than replaced by a fresh clone, and so does
	// internal/tasks' Promote, for a task whose copy is gone when the
	// promotion reaches it, because a promotion cuts nothing and asserts
	// the copy that already stands.
	RefusedCopyMissing IsolationRefusal = "copy-missing"
	// RefusedWrongShape is the resolved path is a repository of its own and
	// distinct from the primary, and is not the shape the task's shape gives
	// it: its remote set is not exactly the one that shape has, or its HEAD
	// is not on the task's branch for a delivery task or not detached for an
	// investigation task.
	RefusedWrongShape IsolationRefusal = "wrong-shape"
)

var isolationRefusals = []IsolationRefusal{RefusedPrimaryCheckout, RefusedNotIsolated, RefusedCopyMissing, RefusedWrongShape}

// IsolationRefusals returns the closed set, in the order above. The result is
// a copy.
func IsolationRefusals() []IsolationRefusal { return slices.Clone(isolationRefusals) }

// Recognized reports whether r is one of the reasons this package defines.
func (r IsolationRefusal) Recognized() bool { return slices.Contains(isolationRefusals, r) }

// ParseIsolationRefusal reads the detail of an "isolation-refused" event back
// into its reason, and refuses text outside the closed set rather than
// returning a reason nobody defined.
func ParseIsolationRefusal(detail string) (IsolationRefusal, error) {
	r := IsolationRefusal(detail)
	if !r.Recognized() {
		return "", fmt.Errorf("taskstate: %q is not an isolation refusal this package defines", detail)
	}
	return r, nil
}

func decodeIsolationRefusal(detail string) error {
	_, err := ParseIsolationRefusal(detail)
	return err
}

// kinds is the one owner of what an event kind means. A kind with no row is
// unrecognized, and an unrecognized kind never answers (P3's stance carried to
// this table: what cannot be classified is not trusted). The store appends any
// kind a caller uses; this table is the only place one acquires a meaning.
//
// Two rows carry an ordering rule their doc states and the store does not
// enforce. "relaunched" is a note: a task whose newest event it is resolves
// Unknown from SourceNone, so the program that appends it appends
// "dispatched" after it, in the same decision. "isolation-refused" claims
// Failed and carries one of IsolationRefusals as its detail, checked by the
// row's decoder before it is appended. "route-refused" claims Failed when
// worker routing covers no configured agent (GAP-02); its detail is free
// text. "knowledge-refused" claims Failed when the repository's curated
// knowledge document on its default branch is one internal/knowledge
// refuses to compose (GAP-13), so the worker is not launched told part of
// it or nothing in its place; its detail is free text. "promoted" is a note
// too, appended by
// store.TransitionTaskShape in the transaction that flips a task's shape,
// and nothing follows it: the worker it tells is already running, and what
// it is doing is the process tier's to answer, so a promoted task whose
// newest event this is resolves Unknown at the event tier, as it does after
// any note, until the worker's next entry. "message-acknowledged" is a
// worker's note naming a message sent to it, PRD section 6's acknowledgement;
// it claims nothing, its detail is the message's identifier, and
// internal/store appends it only in the transaction that records the
// acknowledgement on the message, so the history and the record agree.
// "report-ready" is the same shape for an investigation's report: its one
// writer in the product is store.StoreTaskReport, which appends it with an
// empty detail in the transaction that stores the report and registers its
// holds, and internal/service's worker event path hands the kind to that
// write rather than appending it, so a history the product wrote saying a
// report is ready has the report behind it. store.AppendTaskEvent still
// records the word for any caller that names it, which is how tests build a
// history; nothing here can tell such an entry from the product's.
// "findings-relayed" is the program's note of PRD section 6's "findings
// relayed": its one writer is store.RecordTaskRelay, which appends it with an
// empty detail in the transaction that writes the task's findings relayed
// record, and the one route to that write is internal/service's
// coordinator-only method, so a worker's event path refuses the kind as the
// program's. It is a note and claims nothing at the event tier: the
// investigation's Complete is the terminal tier's, read from the record, so
// an entry with no record behind it moves no task to Complete (P8).
var kinds = []Row{
	{Kind: store.OpeningEventKind, Role: RoleState, State: Queued, Writer: WriterProgram, Routine: true},
	{Kind: store.DispatchedEventKind, Role: RoleState, State: Dispatched, Writer: WriterProgram, Routine: true},
	{Kind: "isolation-refused", Role: RoleState, State: Failed, Writer: WriterProgram, Detail: decodeIsolationRefusal},
	// route-refused claims Failed when no configured agent covers the
	// worker capability need (GAP-02). Its detail is free text naming the
	// refusal; the attempt's adapter/route_reason columns carry a successful
	// selection instead.
	{Kind: "route-refused", Role: RoleState, State: Failed, Writer: WriterProgram},
	{Kind: "knowledge-refused", Role: RoleState, State: Failed, Writer: WriterProgram},
	{Kind: "relaunched", Role: RoleNote, Writer: WriterProgram},
	{Kind: store.PromotedEventKind, Role: RoleNote, Writer: WriterProgram},
	{Kind: store.FindingsRelayedEventKind, Role: RoleNote, Writer: WriterProgram},
	{Kind: "started", Role: RoleState, State: Running, Writer: WriterWorker},
	{Kind: "working", Role: RoleState, State: Running, Writer: WriterWorker},
	{Kind: "waiting-external", Role: RoleState, State: Paused, Writer: WriterWorker, DeclaredWait: true},
	{Kind: "handed-off", Role: RoleState, State: Validating, Writer: WriterWorker},
	{Kind: "validation-started", Role: RoleState, State: Validating, Writer: WriterWorker},
	{Kind: store.ReportReadyEventKind, Role: RoleState, State: Reported, Writer: WriterWorker},
	{Kind: "failed", Role: RoleState, State: Failed, Writer: WriterWorker},
	{Kind: "decision-open", Role: RoleDecisionOpen, State: Held, Writer: WriterWorker},
	{Kind: "decision-closed", Role: RoleDecisionClosed, Writer: WriterWorker},
	{Kind: "progress-note", Role: RoleNote, Writer: WriterWorker},
	{Kind: store.MessageAcknowledgedEventKind, Role: RoleNote, Writer: WriterWorker, Detail: decodeMessageID},
}

// decodeMessageID holds the detail of a message acknowledgement to a message
// identifier, store.ParseMessageID's rule, so a worker's acknowledgement that
// names no message is refused before anything is appended.
func decodeMessageID(detail string) error {
	_, err := store.ParseMessageID(detail)
	return err
}

// Lookup returns the row for a kind, or false for a kind this package defines
// no meaning for. That is a normal answer rather than an error.
func Lookup(kind string) (Row, bool) {
	for _, r := range kinds {
		if r.Kind == kind {
			return r, true
		}
	}
	return Row{}, false
}

// Kinds returns every recognized kind's row. The result is a copy.
func Kinds() []Row { return append([]Row(nil), kinds...) }
