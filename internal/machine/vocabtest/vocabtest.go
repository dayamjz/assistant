// Package vocabtest holds a user-facing phrase to P13's rule that no internal
// vocabulary reaches a person, with the forbidden set derived from the
// packages that own each kind of word rather than written down here. It is a
// package rather than a helper in internal/machine's test because two
// packages ask it, internal/machine of its phrase table and internal/cli of
// what the pull guard printed, and two copies of a derivation drift.
//
// A caller must pair its assertion with Control, which fails a phrase that
// does carry a forbidden term, or a derivation that produced nothing would
// hold every phrase to nothing.
package vocabtest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Term is one forbidden word or phrase and which owner it was derived from.
type Term struct {
	// Text is the word, compared as a whole word without regard to case,
	// with a dash read as a space as well.
	Text string
	// Owner names the package and table the word came from.
	Owner string
}

// Forbidden derives the set from its owners: internal/pipeline's stage
// table, internal/taskstate's states and event kinds, internal/supervisor's
// wake kinds, stop reasons and arm vocabulary, internal/home's lock states
// and the names of the files its layout composes, and the shape of a task
// identifier and a home path. It is computed on every call and nothing here
// caches or hand-lists it, so a word added to an owner's table is forbidden
// from the next run.
func Forbidden(tb testing.TB) []Term {
	tb.Helper()
	var out []Term
	add := func(owner string, words ...string) {
		for _, w := range words {
			if w != "" {
				out = append(out, Term{Text: w, Owner: owner})
			}
		}
	}
	for _, s := range pipeline.Order() {
		add("internal/pipeline's stage table", s.String())
	}
	for _, s := range taskstate.States() {
		add("internal/taskstate's states", string(s))
	}
	for _, k := range taskstate.Kinds() {
		add("internal/taskstate's event kinds", k.Kind)
	}
	for _, k := range supervisor.Kinds() {
		add("internal/supervisor's wake kinds", string(k.Kind), string(k.Class))
	}
	for _, r := range supervisor.StopReasons() {
		add("internal/supervisor's stop reasons", string(r))
	}
	add("internal/supervisor's arm vocabulary",
		string(supervisor.ArmStarted), string(supervisor.ArmAttached), string(supervisor.ArmFailed),
		string(supervisor.ArmServiceDown), string(supervisor.ArmNoBeacon),
		string(supervisor.ArmInstanceMismatch), string(supervisor.ArmLoopStale))
	for _, l := range []home.LockState{home.LockUnknown, home.LockFree, home.LockHeld} {
		add("internal/home's lock states", l.String())
	}
	h, err := home.Open(sampleRoot)
	if err != nil {
		tb.Fatalf("vocabtest: opening a sample home: %v", err)
	}
	for _, path := range []string{h.Beacon(), h.InstanceFile(), h.LockFile(), h.Database(), h.Socket()} {
		add("internal/home's layout", leaf(path))
	}
	add("a home path", h.Root())
	add("a task identifier", sampleTaskID)
	return out
}

// sampleRoot is an absolute path of the shape a home root has on every
// platform the module builds for, and sampleTaskID is one identifier in the
// generator's format.
const (
	sampleRoot   = "/home/someone/.assistant"
	sampleTaskID = "abcdefghij23"
)

// leaf is the last element of a path composed by internal/home, taken on
// either separator so the derivation reads the same on every platform.
func leaf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// Violations returns every forbidden term text carries, as a whole word
// without regard to case; a term with a dash in it is matched with the dash
// and again with a space in its place, so "terminal gone" is caught as well
// as "terminal-gone". A path or an identifier is matched as a substring,
// because neither is a word.
func Violations(tb testing.TB, text string) []Term {
	tb.Helper()
	var found []Term
	for _, term := range Forbidden(tb) {
		if matches(term, text) {
			found = append(found, term)
		}
	}
	return found
}

func matches(term Term, text string) bool {
	if strings.ContainsAny(term.Text, `/\`) || term.Owner == "a task identifier" {
		return strings.Contains(text, term.Text)
	}
	for _, form := range []string{term.Text, strings.ReplaceAll(term.Text, "-", " ")} {
		pattern := `(?i)\b` + regexp.QuoteMeta(form) + `\b`
		if regexp.MustCompile(pattern).MatchString(text) {
			return true
		}
	}
	return false
}

// Assert fails the test when text carries a forbidden term, naming each one
// and its owner.
func Assert(tb testing.TB, text string) {
	tb.Helper()
	for _, v := range Violations(tb, text) {
		tb.Errorf("%q reaches a person and carries %q, which is %s's word", text, v.Text, v.Owner)
	}
}

// Control is the positive control every caller runs beside Assert: a phrase
// built from one word of each owner must be caught for each of them, or the
// derivation is producing less than it claims and Assert holds phrases to
// nothing. It fails the test naming the owner whose word was not caught.
func Control(tb testing.TB) {
	tb.Helper()
	byOwner := map[string]Term{}
	for _, term := range Forbidden(tb) {
		if _, seen := byOwner[term.Owner]; !seen {
			byOwner[term.Owner] = term
		}
	}
	if len(byOwner) == 0 {
		tb.Fatal("vocabtest: the forbidden set is empty, so nothing is held to anything")
	}
	for owner, term := range byOwner {
		if !matches(term, "The phrase says "+term.Text+" to a person.") {
			tb.Errorf("vocabtest: %s's word %q is not caught in a phrase that carries it", owner, term.Text)
		}
	}
}
