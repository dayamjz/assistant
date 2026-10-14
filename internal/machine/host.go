package machine

import "github.com/dayamjz/assistant/internal/supervisor"

// The stop guard's two phrases, which the host hook reads out to the
// coordinator when PRD section 5's stop guard decides a turn. They are rows
// of the same table as the pull guard's phrases and are held to the same
// rule: each names what happened, what it costs and what to decide next, in
// no internal vocabulary, and Phrases lists them so the vocabulary test holds
// them to it. Neither carries the next decision itself: the host's message
// is this phrase followed by the pull guard's, which names the decision the
// situation calls for, so the two tables cannot say different things about
// one situation.
const (
	// PhraseTurnBlocked is what the coordinator reads when its turn may not
	// end: work is under way, nothing is watching it, and the turn has to
	// deal with that before it ends.
	PhraseTurnBlocked = "This turn cannot end yet, because work is under way and nothing is watching it. Deal with that before ending the turn."
	// PhraseTurnReEntered is what the coordinator reads when its turn may
	// end although nothing is watching the work, because the host re-entered
	// it after a block: the guard answers once per re-entry and judges the
	// next turn afresh.
	PhraseTurnReEntered = "This turn may end although work is under way and nothing is watching it, because the host re-entered it after a block. The next turn is judged afresh."
)

// hostPhrases is the host hook's half of the phrase table, which Phrases
// appends to the pull guard's.
func hostPhrases() []string {
	return []string{PhraseTurnBlocked, PhraseTurnReEntered}
}

// HostStop is the stop guard's answer to the coordinator's host, PRD section
// 5's stop guard as one document: whether the turn may end, what the guard
// found and why, and the message the host relays. It is the verdict's shape
// and nothing about the host's protocol: which exit status says blocked and
// which stream the message goes on are the host's convention, and
// internal/cli's host adapter maps this shape onto them.
//
// Allowed, Message and the supervision are written together by HostStopOf
// and nowhere else, so an answer that blocks cannot stand beside an empty
// message and an answer that allows cannot carry one that says the turn is
// blocked.
type HostStop struct {
	// Supervision is what the guard found: internal/supervisor's verdict
	// and, when it blocks, the pull guard's phrase for it. A Reason that is
	// empty is a guard that was not computed, which Detail explains; it
	// blocks, because no answer must mean blocked.
	Supervision Supervision `json:"supervision"`
	// ReEntered says the host supplied its re-entry signal, which is the one
	// input that allows a turn to end with work under way unwatched.
	ReEntered bool `json:"re_entered"`
	// Allowed is whether the turn may end: the verdict allows, or the host
	// re-entered the turn.
	Allowed bool `json:"allowed"`
	// Message is what the coordinator reads, empty when the verdict allows
	// on its own: the turn's phrase and then the pull guard's, which names
	// the next decision.
	Message string `json:"message,omitempty"`
	// Detail says why the guard could not be computed, when it could not,
	// and is empty otherwise.
	Detail string `json:"detail,omitempty"`
}

// HostStopOf is the one translation from the guard's verdict and the host's
// re-entry signal to the answer the host reads. A verdict that allows is an
// allowed answer with no message, whatever the signal. A verdict that blocks
// without the signal is a blocked answer whose message says the turn cannot
// end and then what to do, through SupervisionOf. The same verdict with the
// signal is an allowed answer whose message says why it was allowed, so the
// one turn the host re-entered can end and the record of why is on the
// stream the host keeps.
//
// The signal allows exactly the invocation it was given to. Nothing here
// stores anything, so this function cannot tell a first re-entered turn from
// a second: that bound is the host's, which supplies the signal on the turn
// it re-entered after a block and on no other, and the host's own protocol
// is what consumes it. A host that supplied the signal on every turn would
// have disabled the guard, and nothing here can tell.
func HostStopOf(v supervisor.StopVerdict, reEntered bool) HostStop {
	answer := HostStop{Supervision: SupervisionOf(v), ReEntered: reEntered}
	if v.Allowed() {
		answer.Allowed = true
		return answer
	}
	if reEntered {
		answer.Allowed = true
		answer.Message = PhraseTurnReEntered + " " + answer.Supervision.Warning
		return answer
	}
	answer.Message = PhraseTurnBlocked + " " + answer.Supervision.Warning
	return answer
}
