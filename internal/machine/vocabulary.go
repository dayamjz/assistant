package machine

import "github.com/dayamjz/assistant/internal/supervisor"

// The vocabulary a person is spoken to in, which P13 makes outcomes and not
// mechanics. Each phrase names what happened to the person's work, what it
// costs them, and what they decide next, and none carries a task identifier,
// a path, a stage name, a state label or a watcher's word; the vocabulary test
// derives the forbidden set from the packages that own each of those and
// holds every phrase here to it. This table holds the pull guard's phrase,
// and #110 extends it to every renderer.
const (
	// PhraseUnsupervised is what the pull guard says when work is under way
	// and no live supervisor holds this home: the outcome, that nothing is
	// watching; the consequence, that a worker which stops or needs a
	// decision goes unnoticed; and the next decision, starting the service.
	PhraseUnsupervised = "Work is under way and nothing is watching it, so a worker that stops or needs a decision will go unnoticed until the service is up. Run assistant service start."
)

// Phrases returns every phrase this table holds, which is what the vocabulary
// test walks. The result is a copy.
func Phrases() []string {
	return []string{PhraseUnsupervised}
}

// Supervision is what the pull guard found, as a command reports it beside
// its answer. PRD section 5's pull guard has ordinary commands warn when work
// is in flight and the supervisor has stopped, or when queued wakes wait; the
// in-flight table internal/supervisor owns folds the second into the first,
// so one warning covers both.
//
// Warning is the phrase a person reads, empty when there is nothing to warn
// about, and Reason is internal/supervisor's verdict for a caller that drives
// programmatically and wants to know why. The two are written together by
// SupervisionOf and nowhere else, so a reason that blocks cannot stand beside
// an empty warning.
type Supervision struct {
	// Reason is the guard's verdict, in internal/supervisor's closed set.
	Reason supervisor.StopReason `json:"reason"`
	// Warning is the phrase, from the vocabulary above, and empty when the
	// verdict allows.
	Warning string `json:"warning,omitempty"`
}

// SupervisionOf is the one translation from the guard's verdict to what a
// person is told: a verdict that allows carries no warning, and every verdict
// that blocks carries PhraseUnsupervised. The reasons are not told apart for
// a person, because each is a mechanic and the decision is the same.
func SupervisionOf(v supervisor.StopVerdict) Supervision {
	s := Supervision{Reason: v.Reason}
	if !v.Allowed() {
		s.Warning = PhraseUnsupervised
	}
	return s
}
