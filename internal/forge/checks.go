package forge

import (
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
)

// CheckState is what a provider says about one check.
//
// The states are split on two questions rather than on one. Settled reports
// whether the provider has published a conclusion that nothing will replace,
// and Passes reports whether a settled state stands in the way of a pass.
// Bucketing every state that is not a success together loses the first
// question, and losing it is how a run waits forever on a check that already
// finished.
//
// The zero value is CheckStateUnrecognized, which is not settled, so a check
// nobody filled in keeps a caller waiting rather than letting it conclude.
type CheckState uint8

const (
	// CheckStateUnrecognized is the provider reported a state this package
	// does not know. It is not settled, so the caller keeps waiting.
	//
	// This is a deliberate choice of which way to be wrong. Guessing that an
	// unknown state is terminal risks acting on a verdict that has not
	// arrived, and that is not recoverable; waiting longer than necessary is.
	// The wait is bounded by PRD section 10's checks_timeout, which belongs to
	// the caller, and CheckRun.Reported names the state so an operator can see
	// what this package failed to recognize.
	CheckStateUnrecognized CheckState = iota
	// CheckStatePending is the check is registered and has not finished. It is
	// not settled.
	CheckStatePending
	// CheckStateSucceeded is the check finished and passed.
	CheckStateSucceeded
	// CheckStateFailed is the check finished and did not pass. It is settled,
	// so a caller acts on it rather than waiting.
	CheckStateFailed
	// CheckStateCancelled is the check was cancelled. It is settled and it
	// does not pass.
	//
	// This is the state most easily mistaken for pending, and the mistake is
	// unbounded waiting: a cancelled check is a published conclusion, and
	// nothing is coming to replace it. It stays a state of its own rather than
	// folding into CheckStateFailed because "cancelled" is what a person has
	// to be told; the run may need a check registered again rather than a fix.
	CheckStateCancelled
	// CheckStateNeutral is the check finished with a conclusion that neither
	// passes nor blocks. It is settled and does not stand in the way of a
	// pass.
	CheckStateNeutral
	// CheckStateSkipped is the check was skipped. It is settled and does not
	// stand in the way of a pass.
	CheckStateSkipped
)

// String returns the state's name, which is what appears in a report.
func (s CheckState) String() string {
	switch s {
	case CheckStatePending:
		return "pending"
	case CheckStateSucceeded:
		return "succeeded"
	case CheckStateFailed:
		return "failed"
	case CheckStateCancelled:
		return "cancelled"
	case CheckStateNeutral:
		return "neutral"
	case CheckStateSkipped:
		return "skipped"
	case CheckStateUnrecognized:
		return "unrecognized"
	default:
		return "check-state(" + strconv.Itoa(int(s)) + ")"
	}
}

// Settled reports whether the provider has published a conclusion for this
// check that nothing will replace. A caller waits on a check that is not
// settled and acts on one that is.
func (s CheckState) Settled() bool {
	switch s {
	case CheckStateSucceeded, CheckStateFailed, CheckStateCancelled,
		CheckStateNeutral, CheckStateSkipped:
		return true
	case CheckStateUnrecognized, CheckStatePending:
		return false
	default:
		// A state added to this type without being classified here is not
		// settled, for the same reason CheckStateUnrecognized is not: waiting
		// is the error that can still be corrected.
		return false
	}
}

// Passes reports whether this state lets a pull request's checks be green. It
// is meaningful only for a settled state and is false for every unsettled one,
// so a caller can read Settled and Passes independently without an unsettled
// check ever counting toward a pass.
func (s CheckState) Passes() bool {
	switch s {
	case CheckStateSucceeded, CheckStateNeutral, CheckStateSkipped:
		return true
	default:
		return false
	}
}

// CheckRun is one check on a commit.
type CheckRun struct {
	// Name is what the provider calls the check. It may be empty when the
	// provider named none.
	Name string
	// State is what the provider says about it, mapped onto this package's
	// vocabulary.
	State CheckState
	// URL is where a person looks at the check. It may be empty, and it names
	// the host the check ran on, so it is for a report and not for a decision.
	URL string
	// Reported is the provider's own words for a state this package does not
	// recognize. It is empty for every recognized state, and it is diagnostic
	// text for a person: nothing may branch on it, because it is the one field
	// here whose vocabulary belongs to the provider rather than to this
	// package. It exists so an operator whose run is waiting on
	// CheckStateUnrecognized can see what was not recognized.
	Reported string
}

// String renders the check as name=state, with the provider's word appended
// when the state was not recognized.
func (c CheckRun) String() string {
	name := c.Name
	if name == "" {
		name = "(unnamed)"
	}
	s := name + "=" + c.State.String()
	if c.Reported != "" {
		s += "(" + c.Reported + ")"
	}
	return s
}

// ChecksReport is what a provider says about the checks on one commit. It is
// an observation, not a verdict: turning it into one is Evaluate's job,
// because the rule that an empty list is not a pass has to live in one place.
type ChecksReport struct {
	// HeadCommit is the commit the checks were read against. A caller
	// comparing it with the commit it pushed can tell a check list belonging
	// to an older head from a current one.
	HeadCommit string
	// Runs are the checks registered on that commit, in the order the provider
	// reported them. An empty slice means no check is registered, which is an
	// answer and not a failure.
	Runs []CheckRun
}

// Verdict is what a set of checks means for a run.
//
// The zero value is VerdictNoChecks, which is not green, so a result nobody
// filled in cannot be read as a pass.
type Verdict uint8

const (
	// VerdictNoChecks is no check is registered on the head and configuration
	// does not declare that this repository has none. It is not a pass: an
	// empty check list means unregistered, not passing. It is also not a
	// failure, because a check may still register; the caller waits, bounded
	// by checks_timeout.
	VerdictNoChecks Verdict = iota
	// VerdictRunning is every settled check passes and at least one has not
	// settled. The caller waits.
	VerdictRunning
	// VerdictPassed is the checks are green. That is either every registered
	// check settled and passing, or an empty check list together with the
	// no_ci declaration, and ChecksResult.Declaration says which.
	VerdictPassed
	// VerdictFailed is at least one settled check does not pass. It is
	// reported even while other checks are still running, because a settled
	// failure will not be replaced and waiting for the rest cannot change the
	// verdict.
	VerdictFailed
)

// String returns the verdict's name, which is what appears in a report.
func (v Verdict) String() string {
	switch v {
	case VerdictNoChecks:
		return "no-checks"
	case VerdictRunning:
		return "running"
	case VerdictPassed:
		return "passed"
	case VerdictFailed:
		return "failed"
	default:
		return "verdict(" + strconv.Itoa(int(v)) + ")"
	}
}

// NoCIDeclaration is the positive statement that a repository has no checks.
// It is the only thing that turns an empty check list into a pass.
//
// Its fields are unexported and DeclaredNoCI is the only constructor, so a
// declaration cannot be assembled out of something that is not one. Elapsed
// time, a repository's history, a workflow file, and the name of a branch
// produce no value whose Declared reports true, and the zero value declares
// nothing.
//
// What it does not establish is where the configuration value came from. PRD
// section 10 makes no_ci trusted-only, internal/config's key table is where
// that is enforced, and reading the trusted document from the default branch
// is the gate's part of P7. All of that happens before a Config reaches here.
type NoCIDeclaration struct {
	key      config.Key
	declared bool
}

// DeclaredNoCI returns the no-CI declaration a resolved configuration carries.
// A configuration that does not declare it yields the zero NoCIDeclaration,
// which declares nothing.
func DeclaredNoCI(cfg config.Config) NoCIDeclaration {
	if !cfg.NoCI {
		return NoCIDeclaration{}
	}
	return NoCIDeclaration{key: config.KeyNoCI, declared: true}
}

// Declared reports whether this value is a declaration. The zero value reports
// false.
func (d NoCIDeclaration) Declared() bool { return d.declared }

// Key returns the configuration key that declared it, and is empty when
// Declared reports false.
func (d NoCIDeclaration) Key() config.Key { return d.key }

// String renders the declaration as the key that made it, so a caller
// recording why an empty check list counted as green names the evidence.
func (d NoCIDeclaration) String() string {
	if !d.declared {
		return "no no-CI declaration"
	}
	return "the " + string(d.key) + " declaration"
}

// ChecksResult is a verdict over one report, together with what the verdict
// rests on. A caller records it as it stands: the positive evidence for a pass
// over an empty check list is in it rather than in an absence nobody can point
// at.
type ChecksResult struct {
	// Verdict is what the checks mean for the run.
	Verdict Verdict
	// HeadCommit is the commit the checks were read against, carried through
	// from the report.
	HeadCommit string
	// Runs are the checks the verdict was reached over, in the order the
	// provider reported them.
	Runs []CheckRun
	// Declaration is the no-CI declaration, and it is set only where the
	// verdict rests on it: an empty check list that counted as green. A
	// repository that declares no_ci and then registers a check is judged on
	// that check, and this is the zero value for that result.
	Declaration NoCIDeclaration
}

// Green reports whether the checks passed. It is the one question a caller
// asks before treating a run as validated, and it is true for exactly
// VerdictPassed.
func (r ChecksResult) Green() bool { return r.Verdict == VerdictPassed }

// Waiting returns the checks that have not settled, which is what the run is
// waiting on. It is empty for VerdictPassed and VerdictNoChecks, and it can be
// non-empty for VerdictFailed, where a settled failure decided the verdict
// while other checks were still running.
func (r ChecksResult) Waiting() []CheckRun {
	var out []CheckRun
	for _, run := range r.Runs {
		if !run.State.Settled() {
			out = append(out, run)
		}
	}
	return out
}

// Blocking returns the checks that settled without passing, which is what a
// fix round has to address.
func (r ChecksResult) Blocking() []CheckRun {
	var out []CheckRun
	for _, run := range r.Runs {
		if run.State.Settled() && !run.State.Passes() {
			out = append(out, run)
		}
	}
	return out
}

// String renders the verdict and what it rests on, in one line a caller can
// record.
func (r ChecksResult) String() string {
	var b strings.Builder
	b.WriteString(r.Verdict.String())
	switch {
	case r.Verdict == VerdictPassed && r.Declaration.Declared():
		b.WriteString(": no checks are registered and " + r.Declaration.String() +
			" says this repository has none")
	case r.Verdict == VerdictNoChecks:
		b.WriteString(": no checks are registered and nothing declares this repository has none")
	default:
		b.WriteString(": " + strconv.Itoa(len(r.Runs)) + " checks")
		if w := r.Waiting(); len(w) > 0 {
			b.WriteString(", waiting on " + names(w))
		}
		if bl := r.Blocking(); len(bl) > 0 {
			b.WriteString(", blocked by " + names(bl))
		}
	}
	return b.String()
}

// names renders a list of checks for a one-line report.
func names(runs []CheckRun) string {
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, run.String())
	}
	return strings.Join(parts, ", ")
}

// Evaluate reports what this set of checks means for a run, given whatever
// no-CI declaration configuration carries.
//
// The rule that decides the empty case lives here and nowhere else. An empty
// check list is VerdictNoChecks, which is not green, because a pull request
// with nothing registered is unregistered rather than passing. It becomes
// VerdictPassed only on a declaration, and then the declaration travels on the
// result so the evidence stays inspectable.
//
// With checks present the declaration decides nothing and is left off the
// result. A settled failure wins over a check that has not settled, and a
// check that has not settled wins over a pass, so the three outcomes are
// reached in that order.
func (r ChecksReport) Evaluate(decl NoCIDeclaration) ChecksResult {
	out := ChecksResult{HeadCommit: r.HeadCommit, Runs: r.Runs}
	if len(r.Runs) == 0 {
		if decl.Declared() {
			out.Verdict = VerdictPassed
			out.Declaration = decl
			return out
		}
		out.Verdict = VerdictNoChecks
		return out
	}
	waiting, blocking := false, false
	for _, run := range r.Runs {
		switch {
		case !run.State.Settled():
			waiting = true
		case !run.State.Passes():
			blocking = true
		}
	}
	switch {
	case blocking:
		out.Verdict = VerdictFailed
	case waiting:
		out.Verdict = VerdictRunning
	default:
		out.Verdict = VerdictPassed
	}
	return out
}
