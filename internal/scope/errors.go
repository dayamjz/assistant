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
	// person nothing. The intent stage runs first precisely so this cannot
	// happen, and a caller that reaches here has skipped it.
	ErrNoIntent = errors.New("scope: the lens needs a recorded intent to trace a change to")
)
