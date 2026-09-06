package runs

import "errors"

// Errors a caller is expected to handle. Each is a typed result, never a
// warning execution continues past. Test with errors.Is.
var (
	// ErrIncompleteService is returned by New when the service was not given
	// something it cannot work without. A service missing a store or an agent
	// is refused at construction rather than on the first run that needed the
	// missing thing.
	ErrIncompleteService = errors.New("runs: the service was not given something it cannot work without")
	// ErrRunNotNew is returned by Create when the run it was given is not one
	// that is only now beginning: it names a status other than pending, or it
	// already carries a fixer session. Both describe a run partway through a
	// life it has not started, and recording one would make the run's own
	// history begin with a state nothing here produced.
	ErrRunNotNew = errors.New("runs: a run does not begin already underway")
	// ErrRunEnded is returned when an operation needs a run that can still do
	// something and the run has finished. A finished run has no fixer role to
	// hand out and no status left to establish.
	ErrRunEnded = errors.New("runs: the run has ended")
)
