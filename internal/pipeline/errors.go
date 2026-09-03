package pipeline

import "errors"

// Errors a caller is expected to handle. Each is a typed result, never a
// warning execution continues past.
var (
	// ErrMissingStage is returned when a Stages does not carry an
	// implementation for one of the nine stages. The pipeline is refused
	// rather than built short: a stage that is absent is a stage that was
	// removed, which P2 does not allow.
	ErrMissingStage = errors.New("pipeline: a stage has no implementation")
	// ErrMissingFixer is returned when a stage is configured to take automatic
	// fix rounds and no fixer was supplied to apply them.
	ErrMissingFixer = errors.New("pipeline: a stage takes fix rounds and no fixer was supplied")
	// ErrNegativeRounds is returned when a fix round limit is below zero. Zero
	// is meaningful and sends every finding to the person; a negative limit
	// says nothing, so it is refused rather than read as zero.
	ErrNegativeRounds = errors.New("pipeline: a fix round limit is negative")
	// ErrUndeclaredKey is returned when an implementation declares a read or a
	// write of a state key the schema does not hold, or declares the same key
	// twice.
	ErrUndeclaredKey = errors.New("pipeline: state key is not declared by the pipeline schema")
	// ErrReservedKey is returned when an implementation declares a write of a
	// key this package owns. A stage's outcome, its report, its hold answer,
	// and its fix summary are reported by the pipeline, so a stage writing one
	// would give that fact a second author.
	ErrReservedKey = errors.New("pipeline: state key is written by the pipeline, not by a stage")
	// ErrUndeclaredRead is returned when a stage or fixer body reads a key its
	// implementation did not declare. The step fails.
	ErrUndeclaredRead = errors.New("pipeline: body read a state key its implementation did not declare")
	// ErrUndeclaredWrite is returned when a stage or fixer body returns a
	// write of a key its implementation did not declare. The step fails and
	// none of its writes are applied.
	ErrUndeclaredWrite = errors.New("pipeline: body wrote a state key its implementation did not declare")
	// ErrUnknownStage is returned when a run is started naming a stage to skip
	// that is not one of the nine.
	ErrUnknownStage = errors.New("pipeline: not one of the nine stages")
	// ErrIncompleteRun is returned when a run is started without the branch,
	// the base, or the submitted commit it is validating.
	ErrIncompleteRun = errors.New("pipeline: a run needs a branch, a base, and a submitted commit")
	// ErrBadReport is returned when a stage's recorded report cannot be read
	// back out of state. It is refused rather than read as an empty report,
	// which would present a stage that found something as one that found
	// nothing.
	ErrBadReport = errors.New("pipeline: a stage's recorded report could not be read")
)
