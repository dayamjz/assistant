package pipeline

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// Implementation is one stage's contract: the state keys it reads, the state
// keys it writes, and a constructor for the body that does the work. The
// declarations are data rather than something a body reports about itself, so
// the pipeline can check them against its schema before anything runs.
//
// The nine stages are separate implementations of this one contract. Which
// stage an implementation is used as is decided by the field it is placed in
// on a Stages, not by anything the implementation says about itself.
type Implementation struct {
	// Reads lists every state key the body may read. A read of any other key
	// fails the step with ErrUndeclaredRead.
	Reads []Key
	// Writes lists every state key the body may write. Returning a write of
	// any other key fails the step with ErrUndeclaredWrite. A key the pipeline
	// owns, such as a stage's outcome or its report, may not be listed here.
	Writes []Key
	// NewBody constructs the implementation for one advance segment. The graph
	// calls it once per Run, Resume, or Answer call and shares nothing between
	// runs, so a body may hold state for the rounds within one segment and
	// must keep anything a later segment needs in declared state.
	NewBody func() Body
}

// Body is a stage's work for one advance segment. It reads through in.State,
// returns what it found and what it wants written, and returns an error only
// when the stage could not run at all. A finding is not an error: a stage that
// ran and found something wrong returns it in the report, which is what
// decides whether the run fixes, holds, or advances.
//
// A body that returns an error stops the run and leaves state untouched.
type Body func(ctx context.Context, in Input) (Output, error)

// Input is what a stage body is given.
type Input struct {
	// Stage names the stage this body is running as, so one implementation can
	// serve more than one stage and still know which it is.
	Stage Stage
	// State reads exactly the keys the implementation declared.
	State Reader
}

// Output is what a stage body returns: what it found, and what it wants
// written to state.
type Output struct {
	// Report is what the stage found. The pipeline normalizes it before
	// recording it, which is where P3's fail-closed default lands: a finding
	// with a missing, empty, or unrecognized action becomes ask, and an ask
	// finding holds the stage for a person.
	Report findings.Report
	// Writes are the state writes the stage asks for, at most one per key.
	// Every key must be one the implementation declared.
	Writes map[Key]graph.Value
}

// Fixer applies a stage's fix-eligible findings. It is a separate contract
// from Implementation because reviewing and fixing are separate roles with
// separate memory, per P4: nothing here lets a stage body fix, and nothing
// lets a fixer report findings that could clear the stage it just changed.
type Fixer struct {
	// Reads lists every state key a fix body may read.
	Reads []Key
	// Writes lists every state key a fix body may write. A fixer that commits
	// its work declares KeyHead here, which is also what makes the graph's
	// convergence bound meaningful: a round that changed nothing leaves state
	// as it was.
	Writes []Key
	// NewBody constructs the fixer for one advance segment, on the same terms
	// as Implementation.NewBody.
	NewBody func() FixBody
}

// FixBody applies one round of fixes for one stage.
type FixBody func(ctx context.Context, in FixInput) (FixOutput, error)

// FixInput is what a fix body is given.
type FixInput struct {
	// Stage names the stage whose findings are being fixed.
	Stage Stage
	// Findings are the stage's fix-eligible findings, and nothing else. An ask
	// finding never reaches here: it holds the stage before the round starts.
	Findings []findings.Finding
	// Previous is the sanitized summary the last round of this stage's fixer
	// wrote, empty on the first round. It is the only thing P4 lets cross
	// between rounds.
	Previous string
	// State reads exactly the keys the fixer declared.
	State Reader
}

// FixOutput is what a fix body returns.
type FixOutput struct {
	// Summary is the sanitized account of what was changed, carried to the
	// next round of the same stage.
	Summary string
	// Writes are the state writes the fixer asks for, at most one per key.
	Writes map[Key]graph.Value
}

// Reader gives a body read access to exactly the state keys its
// implementation declared. A read of any other key is refused, which is what
// makes the declaration load-bearing rather than documentation.
type Reader interface {
	// Get returns the current value of key. It returns an error wrapping
	// ErrUndeclaredRead when the implementation did not declare reading it.
	Get(key Key) (graph.Value, error)
}

// reader restricts a graph.Reader to the keys one implementation declared.
// The graph node also declares the keys the pipeline's own adapter reads, so
// without this a body could read them; the declaration would then bound the
// node rather than the implementation.
type reader struct {
	from    graph.Reader
	allowed map[Key]struct{}
	who     string
}

// Get implements Reader.
func (r reader) Get(key Key) (graph.Value, error) {
	if _, ok := r.allowed[key]; !ok {
		return graph.Value{}, fmt.Errorf("%w: %s, key %q", ErrUndeclaredRead, r.who, key)
	}
	return r.from.Get(string(key))
}

// keySet indexes a declaration for lookup.
func keySet(keys []Key) map[Key]struct{} {
	out := make(map[Key]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
}

// applyWrites writes what a body returned, refusing any key its
// implementation did not declare. Keys are applied in sorted order so a body
// cannot make one run differ from another by the order it filled its map.
func applyWrites(w graph.Writer, allowed map[Key]struct{}, who string, writes map[Key]graph.Value) error {
	for _, key := range sortedKeys(writes) {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%w: %s, key %q", ErrUndeclaredWrite, who, key)
		}
		if err := w.Set(string(key), writes[key]); err != nil {
			return err
		}
	}
	return nil
}
