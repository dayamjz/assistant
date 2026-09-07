package scope

import "errors"

// Errors a caller is expected to handle. Each is a typed result, never a
// warning the review continues past.
var (
	// ErrNoIntent is returned when the lens is asked to work against no
	// recorded intent. The intent is the yardstick, so without one there is
	// nothing for a change to trace to: Guidance would ask the reviewer to
	// compare a diff against nothing, and Observe would report every path as
	// unexplained, which is a lens that fires on everything and so tells a
	// person nothing.
	//
	// An ordinary run arrives this way, so it is not only a caller's mistake.
	// The intent stage records that a run carries no intent rather than
	// filling one in: it never blocks a run, and this build infers nothing
	// when none was supplied. So a run started without an intent reaches a
	// review stage with the intent empty, having skipped no stage. What a
	// review does then is the review stage's to decide, on the same terms as
	// ErrNoTouched below - a refusal to handle, not a defect to fail over.
	ErrNoIntent = errors.New("scope: the lens needs a recorded intent to trace a change to")

	// ErrNoTouched is returned when the lens is asked about no path: a
	// Change whose Touched is absent, empty, or holds only entries that are
	// empty once trimmed. The touched paths are the whole question, so
	// without one Guidance would ask the reviewer to account for nothing and
	// Observe could only ever return silence. That silence would mean either
	// that there was nothing reviewable in the change or that every path
	// traced to the intent, and the lens cannot tell those apart. A pass that
	// reads the same either way is a check that passes without checking
	// anything, so the lens refuses instead of producing one.
	//
	// A legitimate run can arrive this way, so it is not only a caller's
	// mistake. Ignore patterns exclude paths from review, this package
	// applies none of that filtering itself, and a change touching only
	// ignored paths therefore reaches a caller with every path removed while
	// the change itself is not empty. A caller meeting this refusal skips the
	// lens for that run and records that it was skipped, rather than failing
	// the review stage over a shape that is not the change's fault.
	ErrNoTouched = errors.New("scope: the lens needs at least one touched path to ask about")
)
