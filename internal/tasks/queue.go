package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Exclude takes one branch's exclusion and returns the function that gives
// it back. internal/service's branch gate is the one producer: it is the same
// exclusion a run start takes, so an intake deciding a branch's disposition
// and a push deciding the branch's run do not interleave. A caller whose
// context ends while queued gives up with the context's error.
type Exclude func(ctx context.Context, repository, branch string) (release func(), err error)

// BranchInGate reports whether the repository's gate already carries
// refs/heads/ for the branch. internal/service is the producer, because it
// is what knows a repository's gate; this package never opens one.
type BranchInGate func(ctx context.Context, repository store.Repository, branch string) (bool, error)

// Deps is what a Queue is built from. Every field is required.
type Deps struct {
	// Home says where a task's instructions and its copy go.
	Home *home.Home
	// Store holds the records.
	Store *store.Store
	// Reader resolves a task's state, which is the only way this package
	// learns one: the live set, the clear condition and a release's anchor
	// are all read off a Resolution.
	Reader *taskstate.Reader
	// Exclude is the per-branch exclusion a delivery intake takes.
	Exclude Exclude
	// BranchInGate answers whether a branch is already in the gate.
	BranchInGate BranchInGate
}

// ErrIncomplete is returned by New when a dependency is missing.
var ErrIncomplete = errors.New("tasks: a dependency is missing")

// Queue is intake and release over one store. It is safe for concurrent use:
// what serializes two intakes of one branch is Exclude, and what serializes
// two releases of one task is the anchor on its history.
type Queue struct {
	home   *home.Home
	store  *store.Store
	reader *taskstate.Reader
	hold   Exclude
	inGate BranchInGate
	// betweenReadAndWrite runs after an intake or a release has read the
	// records it decides from and before it writes. It is nil in production
	// and set only by this package's tests, which use it to hold two callers
	// at the point where their decisions are made and their writes are not.
	betweenReadAndWrite func()
}

// New builds a Queue, refusing with ErrIncomplete when any dependency is nil.
func New(d Deps) (*Queue, error) {
	switch {
	case d.Home == nil:
		return nil, fmt.Errorf("%w: no home", ErrIncomplete)
	case d.Store == nil:
		return nil, fmt.Errorf("%w: no store", ErrIncomplete)
	case d.Reader == nil:
		return nil, fmt.Errorf("%w: no state reader", ErrIncomplete)
	case d.Exclude == nil:
		return nil, fmt.Errorf("%w: no branch exclusion", ErrIncomplete)
	case d.BranchInGate == nil:
		return nil, fmt.Errorf("%w: no gate reader", ErrIncomplete)
	}
	return &Queue{home: d.Home, store: d.Store, reader: d.Reader, hold: d.Exclude, inGate: d.BranchInGate}, nil
}

// Clears reports whether a blocker in this state no longer holds the tasks
// queued behind it: PRD section 6's "blocker clears". It is true of Failed and
// Reported and of nothing else this build resolves.
//
// Ready never clears. A Ready task has a passed run and an open pull request,
// so its branch is still live, and a successor dispatched onto it would share
// that branch's runs with it. What moves a task past Ready is Landed, derived
// from the merge record, and Complete, from the cleanup record; neither is a
// state internal/taskstate resolves yet (#107), so a blocker that reaches
// Ready holds its successors until that tier exists, and a merge record on
// its run changes nothing here today. When #107 declares them they belong in
// this set, and the test that seeds a merge record is the one that flips.
//
// Failed is not terminal in the lifecycle: a relaunch (#94) moves a Failed
// task back to Dispatched. Clearing on it therefore lets a successor and a
// relaunched blocker share a branch, which is why the relaunch must re-run
// the same floor Create applies and queue itself when a live task now holds
// the branch. Nothing here enforces that on the relaunch; it is #94's
// contract.
func Clears(s taskstate.State) bool {
	return s == taskstate.Failed || s == taskstate.Reported
}

// Live reports whether a task in this state holds its branch against a new
// task: every state Clears does not, Unknown among them. Unknown is a task the
// resolver declined to answer for, and a floor that read it as cleared would
// dispatch onto a branch whose worker may still be there; the floor fails
// toward queueing.
func Live(s taskstate.State) bool { return !Clears(s) }
