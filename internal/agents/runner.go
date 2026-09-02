package agents

import "context"

// Runner is one coding agent behind the adapter interface. Every stage in the
// product reaches an agent through this interface and through Fixer, and the
// split between the two is where P4 lives.
//
// Run is session-free and has no parameter a session could be named in. Fixer
// is the only route to memory that survives a round, and everything it starts
// is a fix. A review invocation therefore cannot carry a session, and that
// holds without a caller remembering anything.
//
// An implementation must be safe for concurrent use: one background service
// runs concurrent validation runs, and a Runner resolved once is used by all
// of them.
type Runner interface {
	// Name is the agent's name as configuration spells it, such as "claude".
	Name() string

	// Run executes one invocation with no session. The agent is given no
	// memory of any earlier invocation, and a session it opens for itself is
	// discarded when the invocation ends.
	//
	// purpose must be recognized; an unrecognized one is refused with
	// ErrUnrecognizedPurpose before anything starts. PurposeFix is allowed
	// here and means a fix round that keeps no session, which is what
	// configuration asks for when session reuse is off.
	//
	// The invocation owns its process tree: on completion, on failure, and on
	// cancellation the tree is terminated, politely first and forcefully after
	// a grace period. Run does not return while a process it started is still
	// being terminated.
	Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error)

	// Fixer opens the one durable session a run's fixer role keeps across
	// rounds. resume is empty for a new session, or the reference a previous
	// Fixer reported, which is how a fixer session survives a service restart.
	//
	// A Fixer is scoped to one run. Two runs must not share one, because they
	// would then share the agent's memory of each other's changes.
	Fixer(ctx context.Context, resume string) (Fixer, error)
}

// Fixer is the fixer role's durable session. It exists as a separate type so
// that keeping memory across rounds is something only the fixer can do: its
// Apply takes no purpose, so every invocation it makes is PurposeFix, and
// nothing else in this package accepts a session.
//
// A Fixer holds one conversation, so its calls are serialized against each
// other. It is safe to use from more than one goroutine, but rounds still
// happen one at a time.
//
// A round that reported a session reference is continued by the next one
// whether or not it produced a usable result, because a round that failed may
// already have edited files and the round after it should see the conversation
// those edits were made in rather than start blind. That is a statement about
// which fix rounds share one memory and leaves P4 exactly where it was: Run
// still has no session to pass in and returns nothing a session could be read
// out of.
type Fixer interface {
	// Apply runs one fix round. The purpose is PurposeFix and cannot be
	// anything else.
	Apply(ctx context.Context, inv Invocation) (Result, error)

	// Reference is the agent's opaque handle for this session, empty before
	// the first round has produced one. It is a reference, not content: a
	// caller persists it so a restarted service can resume the same fixer
	// session, and passes it back to Runner.Fixer.
	Reference() string
}

// Sweep is the seam for the identity-based sweep PRD section 8 requires and
// this package does not implement.
//
// Terminating the process group covers a tree that stays in it. A descendant
// that puts itself in a new group escapes that, and finding it again needs a
// scan of the machine's processes matched on the isolated copy their working
// directory resolves under, never on their command line. That scan reads
// state this package does not own, including which runs are still active, so
// it belongs to whatever owns run state.
//
// The seam is here so the two are connected rather than merely intended: a
// runner built WithSweep calls SweepUnder after the process tree of every
// invocation has been terminated, giving it that invocation's working
// directory. Until a caller supplies one, the residual gap is real and a
// detached descendant survives the invocation that started it.
type Sweep interface {
	// SweepUnder is asked to end any process still running under dir. It is
	// called after the invocation's process tree was terminated and its leader
	// reaped, on a context that the invocation's own cancellation does not
	// cancel, since a cancelled invocation is exactly when a sweep matters
	// most.
	//
	// It returns nothing. An implementation knows which runs are active and
	// what it was unable to end, and reporting that is its own job; folding a
	// sweep failure into this invocation's error would either discard a fix
	// that already happened or hide the sweep behind an unrelated result.
	SweepUnder(ctx context.Context, dir string)
}
