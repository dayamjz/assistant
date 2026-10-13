package taskstate

// role is what an event kind does to the question of what is true now.
type role int

const (
	// roleState kinds claim a state, and answer only as the newest event.
	roleState role = iota
	// roleDecisionOpen kinds say a decision is waiting. They claim Held, are
	// reported as superseded while unclosed and outranked, and answer only as
	// the newest event.
	roleDecisionOpen
	// roleDecisionClosed kinds close a decision. They are decision-only:
	// they never become current state and their text never reaches a detail.
	roleDecisionClosed
	// roleNote kinds are history and claim nothing.
	roleNote
)

// kindRow is one row of the vocabulary: an event kind, its role, and the state
// it claims when the role is one that claims.
type kindRow struct {
	kind  string
	role  role
	state State
}

// kinds is the one owner of what an event kind means. A kind absent from it is
// unrecognized: it is history, it never answers, and it is never an error,
// because the log is the worker's vocabulary and the store stays blind to it.
// Adding a kind is adding a row.
var kinds = []kindRow{
	{"queued", roleState, Queued},
	{"dispatched", roleState, Dispatched},
	{"started", roleState, Running},
	{"working", roleState, Running},
	{"validation-started", roleState, Validating},
	{"handed-off", roleState, Validating},
	{"waiting-external", roleState, Paused},
	{"report-ready", roleState, Reported},
	{"failed", roleState, Failed},
	{"decision-open", roleDecisionOpen, Held},
	{"decision-closed", roleDecisionClosed, ""},
	{"progress-note", roleNote, ""},
}

// lookup returns a kind's row, and whether the kind is recognized.
func lookup(kind string) (kindRow, bool) {
	for _, row := range kinds {
		if row.kind == kind {
			return row, true
		}
	}
	return kindRow{}, false
}

// claims returns the state a kind claims, and whether it claims one.
func claims(kind string) (State, bool) {
	row, ok := lookup(kind)
	if !ok || (row.role != roleState && row.role != roleDecisionOpen) {
		return "", false
	}
	return row.state, true
}
