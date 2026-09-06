package runs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
)

// Fixer is one run's fixer role: the entry point every fix round of that run
// goes through, and the owner of the durable agent session those rounds share.
//
// It is an agents.Fixer, and it is one for the reason that interface exists.
// Apply takes an invocation and no purpose, so every invocation reachable
// through this type is a fix, and there is no field, parameter, or option here
// that could attach this run's session to anything else. That is P4 kept
// structurally rather than remembered: this package widens nothing.
//
// What it adds to the adapter's own fixer is durability. The session an
// adapter opens lives in a Go value, so it is gone when the process is, and
// internal/pipeline builds a new fix body for every advance segment. Both
// breaks are crossed here: the reference a round reports is written to the
// run's record before that round's result is handed back, and a Fixer built
// later - in a new segment or in a new process - opens the session from what
// the record holds.
//
// Which mode a Fixer is in is the service's SessionReuse, decided once when
// the service was built and never per round. With it, rounds share one
// conversation. Without it, every round is session-free, which is the run PRD
// section 8 leaves an adapter that cannot keep a session: no memory across
// rounds rather than a session by another name. A caller writes the round the
// same way either way.
//
// A Fixer is safe for concurrent use and its rounds happen one at a time,
// which is what agents.Fixer asks of an implementation and what one
// conversation can honestly support.
type Fixer struct {
	service *Service
	run     string

	// mu serializes rounds and guards everything below it. It is held for the
	// whole of a round, because a second round entering the same conversation
	// while the first is still in it is what "one conversation" excludes.
	//
	// A session-free round needs no such exclusion and takes the lock anyway.
	// A type whose concurrency behaviour depended on configuration would be
	// worse than one that serializes a little more than it must, and one run's
	// fix rounds are one node of one graph at a time in any case.
	mu sync.Mutex
	// session is the adapter's fixer, opened on the first round that needs it
	// and nil for the life of a session-free Fixer.
	session agents.Fixer
	// reference is the session reference the run has recorded. It is what a
	// later segment or a later process opens the session from, and it is
	// updated only once the record carries it.
	reference string
}

// The assertion is as load-bearing as the type: giving this a purpose, a
// session parameter, or a second entry point would have to be a change to
// internal/agents first.
var _ agents.Fixer = (*Fixer)(nil)

// Fixer returns the run's fixer role, which is one object per run: two callers
// asking for the same run get the same Fixer, so two fix nodes of one run
// cannot open two conversations about the same change.
//
// It refuses an unknown run with store.ErrNotFound and a run that has finished
// with ErrRunEnded, because a run nothing can take further has no next fix
// round to answer.
//
// The bound on "one per run" is this service. Two Services over one store
// would each hand out one, and nothing here detects the second; what makes
// that not happen is PRD section 8's exclusive lock on a home, which is a fact
// about how the program is arranged rather than one this package establishes.
//
// The refusal is at the hand-out. A Fixer a caller is already holding is not
// revoked when the run ends, and Apply does not re-ask: ending a run's agent
// work is the cancellation of the context its rounds run on, which is what
// internal/agents ties a process tree to, and a status read per round would be
// a second and slower one that a round already in flight would be past.
//
// Reading the run's status and recording the role happen under one lock, which
// is the lock a lifecycle move is made under, so a role is never handed out
// for a run that finished between the two.
//
// The status is read on every hand-out rather than taken as settled by there
// being a role in the index. What decides is the run's own record, so a run
// ended by something that did not go through this service is refused here too,
// rather than served on the strength of this service having handed a role out
// earlier. Such a run's index entry is dropped here, at the refusal, which is
// the only occasion this service has to notice an ending it did not make; a
// run ended through this service was already dropped by the move itself.
func (s *Service) Fixer(ctx context.Context, id string) (*Fixer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	if finished(r.Status) {
		delete(s.fixers, id)
		return nil, fmt.Errorf("%w: run %s is %s", ErrRunEnded, id, r.Status)
	}
	if existing, ok := s.fixers[id]; ok {
		return existing, nil
	}
	f := &Fixer{service: s, run: id}
	if s.reuse {
		// A run configured for no session reuse carries no session, whatever a
		// record written under other configuration happens to hold. Reading it
		// there would resume a conversation this run was told not to keep.
		f.reference, _ = r.FixerSession.Get()
	}
	s.fixers[id] = f
	return f, nil
}

// Apply runs one fix round for this run.
//
// An invocation asking for a review shape is refused with
// agents.ErrReviewInFixerSession before anything starts, in both modes. The
// adapter's fixer makes that refusal for itself; the session-free mode asks
// agents.Invocation.ValidateForFixer for the same answer, because the rule is
// about the role and not about whether the round happens to keep memory, and
// a mode that let a review through would be exactly the strict stance relaxed
// in one path.
//
// A round whose result this returns has had the session reference it reported
// recorded on the run. There is no window in which a caller holds a fix
// round's result and a restart would resume something else. When the record
// cannot be written the round fails: the error always names why the reference
// could not be kept, and names the round's own failure as well when the round
// itself failed. No result is returned either way, so a round that otherwise
// succeeded is readable only from the agents.Recorder's invocation record and
// not from anything returned here. The fix that round may already have made to
// the working copy stands.
//
// That is a harder answer than internal/agents gives to a round that reports
// no reference at all, and the difference is what can be said about the loss.
// There, the round's own result stands because the loss is visible: the
// invocation record says the session was not opened. Here the thing that could
// not be written is the record, so there is nothing left to make it visible
// with, and failing is what is left.
//
// The reference is recorded on a context the round's own cancellation does not
// cancel, for the reason internal/agents sweeps on one: a cancelled round is
// exactly the round whose conversation the run will want back.
func (f *Fixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.service.reuse {
		if err := inv.ValidateForFixer(); err != nil {
			return agents.Result{}, err
		}
		return f.service.agent.Runner.Run(ctx, agents.PurposeFix, inv)
	}

	if f.session == nil {
		session, err := agents.OpenFixer(ctx, f.service.agent.Runner, f.reference)
		if err != nil {
			return agents.Result{}, fmt.Errorf("runs: opening the fixer session of run %s: %w", f.run, err)
		}
		f.session = session
	}
	res, err := f.session.Apply(ctx, inv)
	if recorded := f.record(context.WithoutCancel(ctx), f.session.Reference()); recorded != nil {
		return agents.Result{}, errors.Join(err, recorded)
	}
	return res, err
}

// Reference is the session reference this run has recorded, which is what a
// restart of this service would open the session from. It is empty until some
// round of the run has reported one, which may have been a round of an earlier
// process, and empty for the life of a session-free Fixer.
//
// It is the recorded reference rather than the adapter's, and the two differ
// only in the window Apply refuses to leave open: a round whose reference
// could not be written failed, so no caller was handed a result that this
// disagrees with.
func (f *Fixer) Reference() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reference
}

// record writes a reference the run does not already carry, and moves this
// Fixer onto it only once the record holds it. A round that reported nothing,
// and one that reported what is already recorded, write nothing.
func (f *Fixer) record(ctx context.Context, reference string) error {
	if reference == "" || reference == f.reference {
		return nil
	}
	if err := f.service.store.SetRunFixerSession(ctx, f.run, reference); err != nil {
		return fmt.Errorf("runs: recording the fixer session of run %s: %w", f.run, err)
	}
	f.reference = reference
	return nil
}
