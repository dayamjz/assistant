package pipeline

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
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
	// any other key fails the step with ErrUndeclaredWrite. Only a key the
	// schema marks writable by a stage may be listed at all; a key that is not
	// is refused when the pipeline is built, with the sentinel errors.go names
	// for that case.
	Writes []Key
	// Requires lists every agent capability this stage's body needs. A
	// capability the adapter has not declared refuses the pipeline when it is
	// built, naming the stage and the capability, which is PRD section 8's
	// rule that a path the adapter has not declared is refused before that
	// adapter is launched.
	//
	// It is checked for all nine stages whatever a run then skips, because a
	// skip is a per-run choice on Start and a pipeline is built once and used
	// by many runs. A stage in the topology is a stage some run will take.
	//
	// Declaring nothing is the common case and means the stage needs nothing
	// beyond an agent that runs. A stage that needed a session and did not
	// declare it is not thereby served: agents.OpenFixer refuses at the call
	// instead, so what the declaration buys there is the refusal arriving
	// before the run rather than partway through it. That backstop is per
	// capability rather than general, and capability.go says which
	// requirements have one.
	Requires []agents.Capability
	// NewBody constructs the implementation for one execution of the stage.
	// The pipeline calls it each time the stage runs and never reuses a body,
	// so a stage that takes fix rounds is built again for every round: this
	// package hands no stage body from one round, one segment, or one run to
	// the next, and anything a later execution needs lives in declared state.
	//
	// That is a narrowing rather than a guarantee. NewBody is supplied by the
	// caller, so its closure may capture whatever the caller likes and keep it
	// across every round and every run; nothing here refuses that. What it
	// does remove is the easiest way a stage keeps a value between rounds, and
	// P4's load-bearing half is enforced downstream and typed, at
	// agents.Runner.Run, which has no parameter a session could be named in.
	//
	// A stage this run skips does not run its body, so nothing is constructed
	// for it.
	NewBody func() Body
}

// Body is a stage's work for one execution of the stage: a stage taking fix
// rounds is handed a different Body value each round. It reads through
// in.State, returns what it found and what it wants written, and returns an
// error only when the stage could not run at all. A finding is not an error: a
// stage that ran and found something wrong returns it in the report, which is
// what decides whether the run fixes, holds, or advances.
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
	// Report is what the stage found. Every stage returns one, including a
	// stage with nothing to say: the pipeline normalizes it and then validates
	// it, and a report that does not validate fails the step with
	// ErrUnusableReport.
	//
	// Validating asks for more than a summary. Every finding needs a
	// description, every evidence entry needs a path, and the risk must be a
	// recognized word or left unstated, because normalizing deliberately does
	// not resolve an unrecognized one.
	//
	// Normalizing is where P3's fail-closed default lands: a finding with a
	// missing, empty, or unrecognized action becomes ask, and an ask finding
	// holds the stage for a person.
	//
	// The review stage's report answers for more than this, and this package
	// does not check it. PRD section 5 binds a review's findings to the paths
	// it declared reading, findings.ParseReviewReport is that rule, and
	// agents.ShapeReview is how a review implementation reaches its agent's
	// answer through it. A review implementation that read its agent's output
	// as an ordinary stage report would return an unbound one and nothing here
	// would say so; what it would also not have is a demand to bind against,
	// because agents.Invocation.Validate refuses one on any other shape.
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
	// Writes lists every state key a fix body may write. Every key here must
	// declare a merge rule, and a key that does not is refused with
	// ErrUnmergeableFixerWrite. One Fixer serves every fix node, so under some
	// fix round limits a key it declares has more writers than the graph
	// permits a key with no merge rule; the refusal is made whenever a Fixer is
	// supplied rather than only under those limits, so the answer does not vary
	// with configuration.
	//
	// A fixer that commits its work declares KeyHead here, which is the key the
	// schema admits today. It is also what makes the graph's convergence bound
	// meaningful: a round that changed nothing leaves state as it was.
	Writes []Key
	// Requires lists every agent capability a fix body needs. A fixer that
	// keeps one durable agent session across the rounds of a stage declares
	// agents.CapabilityResumableSessions here, which is what config.SessionReuse
	// asks for, and a pipeline built against an adapter that has not declared
	// it is refused.
	//
	// Two questions are asked of this and only one is gated. Whether a
	// capability named here is one internal/agents defines is asked whenever a
	// Fixer is supplied, so a typo is refused with ErrUnknownCapability under
	// fix round limits of zero, on the same terms as ErrUnmergeableFixerWrite.
	// Whether the adapter declared it is asked only when some stage's fix
	// round limit is above zero, because a pipeline that builds no fix node
	// takes no fix path. capability.go states why the two differ.
	//
	// A fixer that declares nothing keeps no memory across rounds, which is
	// what PRD section 8 leaves an adapter without resumable sessions: either
	// the run has a declared session or it has no memory across rounds. The
	// declaration is what tells those two apart, and a fixer that forgot it
	// gets no session anyway, since agents.OpenFixer reads the adapter's
	// declaration and not this one.
	Requires []agents.Capability
	// NewBody constructs the fixer for one fix node per advance segment, not
	// for one round: a run with rounds on several stages builds one fix body
	// per fix node it executes. A fix body spans the rounds of the one stage's
	// loop it serves that fall within one advance segment, so it may hold
	// state across those rounds - an agent session, for one.
	//
	// It does not span the whole loop. A round whose body returns an error, or
	// a run interrupted mid-loop, leaves the run's latest checkpoint standing
	// inside the loop still running; the graph's Resume then continues it in a
	// new segment and builds a new fix body, so the rounds after the break get
	// a different FixBody than the rounds before it. That is why
	// agents.Fixer.Reference exists: across a process restart the Go value is
	// gone, so anything a later segment needs is either behind that reference
	// or in declared state. Nothing in this repository persists the reference,
	// so nothing here guarantees a session survives a break. Within one process
	// the new body comes from this same closure, so whatever it captures
	// survives, which this package neither requires nor refuses.
	//
	// The asymmetry with Implementation.NewBody, which is built per execution,
	// is the point: only the fixer keeps a session across rounds.
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
	// wrote, empty on the first round. It crosses from one fix round to the
	// next round of the same stage, and this package routes it nowhere else:
	// a stage body's Input carries no summary.
	//
	// PRD section 5 has the re-review check the previous findings and the fix
	// summary as claims. A stage that wants to see them declares a read of its
	// own Stage.FixKey and Stage.ReportKey, which the schema permits: it bounds
	// what a declaration may write and never what it may read.
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
//
// A body confines its state access to the goroutine it runs on. A body that
// fans out and reads from several goroutines races, and that is a residual gap
// rather than a guarantee: the refusal this Reader records and the graph.Reader
// underneath it are both unsynchronized, so locking this layer alone would buy
// nothing and would suggest a safety this package cannot provide. Fan out the
// work if it helps, and read what it needs before it does.
type Reader interface {
	// Get returns the current value of key. It returns an error wrapping
	// ErrUndeclaredRead when the implementation did not declare reading it.
	Get(key Key) (graph.Value, error)
}

// refusal records the first read a reader turned down, so the node adapter
// can fail the step even when the body discarded the error it was handed. It
// is the same device internal/graph applies one layer down, for the same
// reason: an error a body may ignore bounds nothing.
type refusal struct{ err error }

// fail records err if it is the first refusal and returns it either way.
func (f *refusal) fail(err error) error {
	if f.err == nil {
		f.err = err
	}
	return err
}

// reader restricts a graph.Reader to the keys one implementation declared.
// The graph node also declares the keys the pipeline's own adapter reads, so
// without this a body could read them; the declaration would then bound the
// node rather than the implementation.
type reader struct {
	from    graph.Reader
	allowed map[Key]struct{}
	who     string
	refused *refusal
}

// Get implements Reader.
func (r reader) Get(key Key) (graph.Value, error) {
	if _, ok := r.allowed[key]; !ok {
		return graph.Value{}, r.refused.fail(
			fmt.Errorf("%w: %s, key %q", ErrUndeclaredRead, r.who, key))
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
