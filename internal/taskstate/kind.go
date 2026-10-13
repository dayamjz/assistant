package taskstate

import "github.com/dayamjz/assistant/internal/store"

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

// Row is one event kind's meaning.
type Row struct {
	// Kind is the event kind as a worker appends it.
	Kind string
	// Role is what the kind does.
	Role Role
	// State is the state the kind claims. It is empty for the roles that claim
	// none.
	State State
}

// kinds is the one owner of what an event kind means. A kind with no row is
// unrecognized, and an unrecognized kind never answers (P3's stance carried to
// this table: what cannot be classified is not trusted). The store appends any
// kind a worker uses; this table is the only place one acquires a meaning.
var kinds = []Row{
	{store.OpeningEventKind, RoleState, Queued},
	{"dispatched", RoleState, Dispatched},
	{"started", RoleState, Running},
	{"working", RoleState, Running},
	{"waiting-external", RoleState, Paused},
	{"handed-off", RoleState, Validating},
	{"validation-started", RoleState, Validating},
	{"report-ready", RoleState, Reported},
	{"failed", RoleState, Failed},
	{"decision-open", RoleDecisionOpen, Held},
	{"decision-closed", RoleDecisionClosed, ""},
	{"progress-note", RoleNote, ""},
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
