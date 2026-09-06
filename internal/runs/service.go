package runs

import (
	"context"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/store"
)

// Options is everything the run service is built from. Every field is
// required, because there is no defensible default for any of them: a service
// with no store records nothing, one with no agent hands out a fixer role
// nothing can run, and session reuse decides whether a run keeps memory across
// its fix rounds at all.
type Options struct {
	// Store is where runs are recorded. It is also where the fixer session
	// reference lives, on the run's own row.
	Store *store.Store
	// Agent is the resolved agent adapter, as agents.Resolve returned it. Its
	// Capabilities are what the fixer path is checked against when this
	// service is built, and its Runner is what a fix round reaches.
	//
	// A Resolution that did not come from agents.Resolve is the caller's to
	// answer for: what makes a declaration and a type agree is that function,
	// not this one. What still holds for a hand-built one is that
	// agents.OpenFixer reads the adapter's own declaration before it looks at
	// the type, so a run cannot reach a session mechanism nobody declared.
	Agent agents.Resolution
	// SessionReuse is config.Config.SessionReuse: the run keeps one durable
	// fixer session across its fix rounds. It is here rather than read from a
	// configuration this package would otherwise not need, on the same terms
	// as pipeline.Options.SuppressProjectInstructions.
	//
	// It is what this service promises to keep. With it set, every fix round
	// of one run is answered in one agent conversation, and the reference to
	// that conversation is recorded on the run so a restarted service
	// continues it. Without it, every round is session-free.
	SessionReuse bool
}

// Service owns the lifecycle of a run's record and the one durable fixer
// session a run keeps. Read doc.go for the boundary: it drives no graph, runs
// no stage, and starts no run.
//
// A Service is safe for concurrent use. One Service answers for one home,
// which is the arrangement PRD section 8's exclusive home lock produces; the
// package documentation says what a second one over the same store would cost.
type Service struct {
	store *store.Store
	agent agents.Resolution
	reuse bool

	// mu serializes the two things that must not interleave: handing out a
	// run's fixer role and ending the run. It is held across the store call
	// each of those makes, which is what stops a role being handed out for a
	// run that has just finished, and it is never held across a fix round, so
	// one run's round is never another caller's wait.
	mu     sync.Mutex
	fixers map[string]*Fixer
}

// New builds the service, and refuses rather than degrades.
//
// It refuses a service missing a store or an agent with ErrIncompleteService.
//
// It refuses a service asked for session reuse against an adapter that has not
// declared resumable sessions, with an *agents.CapabilityError naming the
// capability. That is PRD section 8's rule applied to the one path this
// package owns: configuration asked for a durable session, the adapter cannot
// keep one, and the run that would result has no memory across rounds while
// still reporting that it ran under session reuse. Refusing here puts that
// answer before any run rather than on the first fix round.
//
// It is the early half of the rule rather than the load-bearing half. What
// actually keeps a session away from an adapter that did not declare one is
// agents.OpenFixer, which reads the adapter's own declaration at the call.
// What this buys is that the refusal arrives before a run exists, and that
// FixerRequires and this check read the same table, so a pipeline built from
// the one cannot disagree with the other.
func New(o Options) (*Service, error) {
	if o.Store == nil {
		return nil, fmt.Errorf("%w: no store", ErrIncompleteService)
	}
	if o.Agent.Runner == nil {
		return nil, fmt.Errorf("%w: no resolved agent", ErrIncompleteService)
	}
	for _, capability := range fixerRequires(o.SessionReuse) {
		if !o.Agent.Capabilities.Has(capability) {
			return nil, &agents.CapabilityError{
				Agent:      o.Agent.Name,
				Capability: capability,
				Path:       "the run's fixer session",
			}
		}
	}
	return &Service{
		store:  o.Store,
		agent:  o.Agent,
		reuse:  o.SessionReuse,
		fixers: make(map[string]*Fixer),
	}, nil
}

// fixerRequires is what a run's fixer path needs of the agent adapter, given
// whether configuration asks for session reuse. It is one function so that
// New's refusal and what a caller declares on pipeline.Fixer.Requires are the
// same answer rather than two that happen to agree today.
func fixerRequires(reuse bool) []agents.Capability {
	if !reuse {
		return nil
	}
	return []agents.Capability{agents.CapabilityResumableSessions}
}

// FixerRequires is what a run's fixer path needs of the agent adapter, for a
// caller putting it on pipeline.Fixer.Requires. A service built without
// session reuse needs nothing, which is the run PRD section 8 leaves an
// adapter that cannot keep a session: no memory across rounds rather than a
// session by another name.
//
// A caller that declares this is not thereby protected twice over. New has
// already refused the combination this would refuse, so what declaring it adds
// is that the pipeline names the fix path in its own refusal if the two are
// ever built from different configurations.
func (s *Service) FixerRequires() []agents.Capability { return fixerRequires(s.reuse) }

// SessionReuse reports whether runs of this service keep one durable fixer
// session across their fix rounds.
func (s *Service) SessionReuse() bool { return s.reuse }

// Create records a new run, which always begins pending and always begins with
// no fixer session.
//
// It refuses a run carrying either with ErrRunNotNew. A run recorded as
// already running is a run whose history begins in the middle, and a run
// recorded with a session reference is one claiming a conversation no round of
// it has held; the session on a run's record is written by that run's own
// fixer and by nothing else this package offers.
//
// Everything else about the record is store.CreateRun's, including the
// refusals that keep a run traceable to the build and the configuration that
// produced it. This adds the two rules above and nothing more.
//
// PRD section 8 requires the row to precede the run's directory. Nothing here
// creates a directory, so a caller that makes one does it after this returns.
func (s *Service) Create(ctx context.Context, r store.Run) (store.Run, error) {
	if r.Status != "" && r.Status != store.RunPending {
		return store.Run{}, fmt.Errorf("%w: run %s is given as %s", ErrRunNotNew, r.ID, r.Status)
	}
	if reference, known := r.FixerSession.Get(); known {
		return store.Run{}, fmt.Errorf("%w: run %s is given a fixer session, %q", ErrRunNotNew, r.ID, reference)
	}
	r.Status = store.RunPending
	return s.store.CreateRun(ctx, r)
}
