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
	// key the schema does not mark writable by a stage. The key table decides
	// that per row, so the rule holds for keys added later without this
	// sentinel changing.
	//
	// Two kinds of key are refused, for opposite reasons. One is a key this
	// package's own nodes write, where a stage writing it would give a fact
	// the pipeline reports a second author. The other is a run input, which no
	// node writes at all: it is what the run was started with.
	ErrReservedKey = errors.New("pipeline: state key is not one a stage may write")
	// ErrUnmergeableFixerWrite is returned when the fixer declares a write of a
	// state key whose row declares no merge rule.
	//
	// One Fixer serves every fix node, so a key the fixer writes is written by
	// as many nodes as there are stages taking fix rounds, and a key with no
	// merge rule may have only one writer. Refusing it here rather than letting
	// the graph refuse the built topology keeps the answer the same whatever the
	// configured limits are: without this, the same Fixer would build under a
	// configuration giving one stage rounds and fail under one giving two.
	//
	// It is checked whenever a fixer was supplied, whatever the limits are, so
	// a fixer refused under one configuration is refused under all of them.
	//
	// KeyHead is what the schema admits today, and it is the key a fixer that
	// commits its work needs. A stage's writes are not narrowed this way,
	// because no configuration can multiply the nodes writing one stage's
	// declaration. Two stages declaring the same key with no merge rule is a
	// different case and still reachable: internal/graph's single-writer rule
	// refuses it, so the refusal arrives as a graph.BuildError carrying
	// RuleSingleWriter rather than as a sentinel here.
	ErrUnmergeableFixerWrite = errors.New("pipeline: the fixer writes a state key that declares no merge rule")
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
	// ErrEmptyIntent is returned when a run claims its intent was supplied and
	// supplies none. It is separate from ErrIncompleteRun because the two mean
	// different things: that one says the run did not say what it is
	// validating, this one says the run claimed authoritative acceptance
	// criteria and gave none.
	//
	// An empty supplied intent is worse than an absent one. Absent falls back
	// to inference, whereas supplied is framed as authoritative in every
	// downstream prompt, so review would check the diff against nothing while
	// being told that nothing was the contract. A run that supplied no intent
	// says so with IntentSupplied false, which stays legal.
	ErrEmptyIntent = errors.New("pipeline: a run claims a supplied intent and supplies none")
	// ErrUnusableReport is returned when a stage's report does not validate
	// after normalization. The step fails, nothing is recorded, and the run
	// stops; the wrapped error names which defects the report had.
	//
	// It is a refusal rather than something to log past because of what the
	// shape means. An empty or truncated output decodes to a report with no
	// summary, and recording that would mark the stage passed: a stage that
	// said nothing read as a stage that found nothing.
	//
	// Validating in the stage node adapter is the same argument as normalizing
	// there. It is the one place every report from all nine stages passes
	// through, so the fact has one owner instead of nine.
	// findings.ParseReport already validates what it parses, so a stage that
	// parsed agent output meets this twice and a stage that built a report by
	// hand meets it once. It refuses a shape, not a lie: a well-formed report
	// saying something false passes here.
	ErrUnusableReport = errors.New("pipeline: a stage's report did not validate")
	// ErrBadReport is returned when a stage's recorded report cannot be read
	// back out of state. It is refused rather than read as an empty report,
	// which would present a stage that found something as one that found
	// nothing.
	ErrBadReport = errors.New("pipeline: a stage's recorded report could not be read")
)
