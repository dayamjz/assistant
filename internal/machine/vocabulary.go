package machine

import "github.com/dayamjz/assistant/internal/supervisor"

// The vocabulary a person is spoken to in, which P13 makes outcomes and not
// mechanics. Each phrase names what happened to the person's work, what it
// costs them, and what they decide next, and none carries a task identifier,
// a path, a stage name, a state label or a watcher's word; the vocabulary test
// derives the forbidden set from the packages that own each of those and
// holds every phrase here to it. This table holds the pull guard's phrases,
// and #110 extends it to every renderer.
const (
	// PhraseServiceDown is what the pull guard says when work is under way
	// and no service holds this home: the next decision is starting one.
	PhraseServiceDown = "Work is under way and nothing is watching it, so a worker that stops or needs a decision will go unnoticed until the service is up. Run assistant service start."
	// PhraseServiceNotWatching is what the pull guard says when a service
	// holds this home and nothing in it is watching the fleet: starting one
	// is not the next decision, because one is up; restarting it is.
	PhraseServiceNotWatching = "Work is under way and the service is up, but nothing in it is watching the fleet, so a worker that stops or needs a decision will go unnoticed. Run assistant service restart."
	// PhraseUndecided is what the pull guard says when it could not tell
	// whether work is under way or whether a service holds this home: the
	// record or the home could not be read, and the next decision is finding
	// out why.
	PhraseUndecided = "Whether work is under way could not be decided, because this home could not be read, so a worker that stops or needs a decision may go unnoticed. Run assistant doctor."
)

// phraseByReason is the one table from a blocking verdict to the phrase a
// person reads. Every reason internal/supervisor can answer with that blocks
// has a row, which the vocabulary test derives from supervisor.StopReasons
// and checks; a reason that allows has none. The rows group by the next
// decision and not by the mechanic: a free lock is a service to start, every
// failure under a held lock is a service to restart, and a reading that
// could not be taken is something to find out.
var phraseByReason = map[supervisor.StopReason]string{
	supervisor.StopLockFree:         PhraseServiceDown,
	supervisor.StopNoBeacon:         PhraseServiceNotWatching,
	supervisor.StopBeaconStale:      PhraseServiceNotWatching,
	supervisor.StopBeaconAhead:      PhraseServiceNotWatching,
	supervisor.StopInstanceMismatch: PhraseServiceNotWatching,
	supervisor.StopEpochMismatch:    PhraseServiceNotWatching,
	supervisor.StopLockUnknown:      PhraseUndecided,
	supervisor.StopInFlightUnknown:  PhraseUndecided,
}

// PhraseFor is the phrase for a blocking reason, and false for a reason that
// allows or that the table has no row for.
func PhraseFor(reason supervisor.StopReason) (string, bool) {
	phrase, ok := phraseByReason[reason]
	return phrase, ok
}

// Phrases returns every phrase this table holds, which is what the vocabulary
// test walks. The result is a copy.
func Phrases() []string {
	return []string{PhraseServiceDown, PhraseServiceNotWatching, PhraseUndecided}
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
// that blocks carries the phrase phraseByReason gives its reason, so the next
// decision a person reads is the one their situation calls for. A blocking
// reason the table has no row for, which the vocabulary test keeps from
// being one of internal/supervisor's, carries PhraseUndecided rather than
// nothing, because a block with no warning would be the silent failure the
// guard exists to prevent.
func SupervisionOf(v supervisor.StopVerdict) Supervision {
	s := Supervision{Reason: v.Reason}
	if v.Allowed() {
		return s
	}
	phrase, ok := PhraseFor(v.Reason)
	if !ok {
		phrase = PhraseUndecided
	}
	s.Warning = phrase
	return s
}
