package findings

import (
	"encoding/json"
	"strings"
)

// Action decides who resolves a finding. It is the single most important field
// in the system, so the recognized set is closed and anything outside it
// normalizes to ActionAsk rather than to something a machine may act on.
type Action string

const (
	// ActionUnset is the zero Action, which is what a finding arriving with no
	// action field holds. It is not one of the three recognized actions:
	// Normalize rewrites it to ActionAsk, and it is never fix-eligible.
	ActionUnset Action = ""
	// ActionFix marks a finding that is objectively wrong and mechanically
	// fixable. It is the only action eligible for the automatic fix loop.
	ActionFix Action = "fix"
	// ActionAsk marks a finding that touches the user's intent or judgment. It
	// parks for a decision and never enters the automatic fix loop. It is also
	// the value every unrecognized action normalizes to.
	ActionAsk Action = "ask"
	// ActionNote marks an informational finding. It needs no fix and blocks
	// nothing.
	ActionNote Action = "note"
)

// ParseAction maps agent output to an Action. Surrounding space is trimmed and
// ASCII case is folded, so "Fix" and " fix " are both ActionFix. Every other
// input, including the empty string, returns ActionAsk. This is P3: an
// unclassified finding goes to the person, never to the fix loop.
func ParseAction(s string) Action {
	switch Action(strings.ToLower(strings.TrimSpace(s))) {
	case ActionFix:
		return ActionFix
	case ActionAsk:
		return ActionAsk
	case ActionNote:
		return ActionNote
	default:
		return ActionAsk
	}
}

// Recognized reports whether a is exactly one of the three actions this
// package defines. It compares the stored value as it stands and does not
// trim, fold, or otherwise repair it.
func (a Action) Recognized() bool {
	return a == ActionFix || a == ActionAsk || a == ActionNote
}

// String returns the action as stored, which for a recognized action is its
// wire name. An unrecognized action renders as whatever it holds, so a
// diagnostic can quote what the agent actually said.
func (a Action) String() string { return string(a) }

// UnmarshalJSON reads an action from untrusted output without failing. A JSON
// string is stored verbatim, so a diagnostic can still quote it; any other JSON
// value, including a number, an object, or null, becomes ActionUnset. Neither
// outcome is fix-eligible, and Normalize resolves both to ActionAsk. Decoding
// deliberately cannot fail here: an action nobody recognizes is P3's defined
// case, not a reason to discard the surrounding findings.
func (a *Action) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		*a = ActionUnset
		return nil
	}
	*a = Action(s)
	return nil
}

// Severity orders and colors a finding for the person reading the list. It
// never decides who resolves the finding; Action does that alone.
type Severity string

const (
	// SeverityUnset is the zero Severity, which is what a finding arriving with
	// no severity field holds. Normalize rewrites it to SeverityWarning.
	SeverityUnset Severity = ""
	// SeverityError marks a finding whose subject is broken.
	SeverityError Severity = "error"
	// SeverityWarning marks a finding worth attention that is not a break. It
	// is also the value every unrecognized severity normalizes to.
	SeverityWarning Severity = "warning"
	// SeverityInfo marks a finding that is purely informational.
	SeverityInfo Severity = "info"
)

// ParseSeverity maps agent output to a Severity, trimming space and folding
// ASCII case as ParseAction does. Every unrecognized input, including the empty
// string, returns SeverityWarning: an unreadable severity must not read as
// harmless, and it cannot make a finding fix-eligible either way.
func ParseSeverity(s string) Severity {
	switch Severity(strings.ToLower(strings.TrimSpace(s))) {
	case SeverityError:
		return SeverityError
	case SeverityWarning:
		return SeverityWarning
	case SeverityInfo:
		return SeverityInfo
	default:
		return SeverityWarning
	}
}

// Recognized reports whether s is exactly one of the three severities this
// package defines, comparing the stored value as it stands.
func (s Severity) Recognized() bool {
	return s == SeverityError || s == SeverityWarning || s == SeverityInfo
}

// String returns the severity as stored.
func (s Severity) String() string { return string(s) }

// UnmarshalJSON reads a severity from untrusted output without failing, on the
// same terms as Action.UnmarshalJSON: a JSON string is stored verbatim and any
// other JSON value becomes SeverityUnset, both of which Normalize resolves to
// SeverityWarning.
func (s *Severity) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		*s = SeverityUnset
		return nil
	}
	*s = Severity(raw)
	return nil
}

// Risk is a stage's optional judgment of how risky the change it looked at is.
// It heads the findings list for the person reading it.
type Risk string

const (
	// RiskUnstated is the zero Risk. The stage offered no risk level, which is
	// legal: risk is optional.
	RiskUnstated Risk = ""
	// RiskLow is a stage's judgment that the change carries little risk.
	RiskLow Risk = "low"
	// RiskMedium is a stage's judgment that the change carries moderate risk.
	RiskMedium Risk = "medium"
	// RiskHigh is a stage's judgment that the change carries high risk.
	RiskHigh Risk = "high"
)

// ParseRisk maps agent output to a Risk, trimming space and folding ASCII
// case. It reports whether the input was recognized. Unlike ParseAction and
// ParseSeverity it has no fail-closed answer to offer: mapping an unrecognized
// word down would understate the risk and mapping it up would manufacture
// alarm, so the caller is told and Validate refuses. The empty string is
// recognized as RiskUnstated, because a stage need not state a risk at all.
func ParseRisk(s string) (Risk, bool) {
	switch Risk(strings.ToLower(strings.TrimSpace(s))) {
	case RiskUnstated:
		return RiskUnstated, true
	case RiskLow:
		return RiskLow, true
	case RiskMedium:
		return RiskMedium, true
	case RiskHigh:
		return RiskHigh, true
	default:
		return Risk(strings.TrimSpace(s)), false
	}
}

// Recognized reports whether r is RiskUnstated or one of the three risk levels
// this package defines, comparing the stored value as it stands.
func (r Risk) Recognized() bool {
	return r == RiskUnstated || r == RiskLow || r == RiskMedium || r == RiskHigh
}

// String returns the risk as stored.
func (r Risk) String() string { return string(r) }
