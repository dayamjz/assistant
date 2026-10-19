package tasks

import (
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// ErrRequest is the class every refusal of a malformed intake request
// belongs to: a shape outside store.TaskShape's set, an investigation task
// naming a branch, a delivery task naming none, empty instructions, or an
// empty project. The error names which, and nothing is recorded.
var ErrRequest = errors.New("tasks: the intake request cannot be recorded as asked")

// ErrBranchTaken is the class every *BranchTakenError belongs to. It is
// returned by Create for a delivery task naming a branch no task has ever
// worked on that already exists in the gate or already has a run: that is a
// person's branch, and a task is never handed one.
var ErrBranchTaken = errors.New("tasks: the branch is a person's, not a task's")

// BranchTakenError reports a refused intake and names what holds the branch,
// so the coordinator can say why rather than only that it was refused.
type BranchTakenError struct {
	// Repository is the repository the branch is of.
	Repository string
	// Branch is the branch asked for.
	Branch string
	// Held says what holds it: HeldByGate or HeldByRun.
	Held Holder
	// Run names the run when Held is HeldByRun, and is empty otherwise.
	Run string
}

// Holder is what holds a branch a task may not be given.
type Holder string

const (
	// HeldByGate is the gate's bare repository already carries refs/heads/
	// for the branch, so something was pushed through it under that name.
	HeldByGate Holder = "gate"
	// HeldByRun is the branch has a validation run on record, whichever
	// status it is in.
	HeldByRun Holder = "run"
)

// Error names the branch and what holds it.
func (e *BranchTakenError) Error() string {
	switch e.Held {
	case HeldByRun:
		return fmt.Sprintf("tasks: branch %s of %s already has run %s, so it is not a task's to take",
			e.Branch, e.Repository, e.Run)
	default:
		return fmt.Sprintf("tasks: branch %s of %s already exists in the gate, so it is not a task's to take",
			e.Branch, e.Repository)
	}
}

// Unwrap makes every refused branch match ErrBranchTaken.
func (e *BranchTakenError) Unwrap() error { return ErrBranchTaken }

// ErrOutranked is the class every *OutrankedError belongs to. Release returns
// it, on the outcome of one task, when that task's history ends on its queued
// entry and a higher tier of internal/taskstate's precedence decided its state
// anyway: a run attributed to it, or a live terminal. The anchor Release
// appends under protects the history and nothing else, so a state decided by
// a record the anchor does not cover is one this package refuses to append
// past rather than one it reads around.
var ErrOutranked = errors.New("tasks: the task's history says queued but a higher source decided its state")

// OutrankedError reports a refused release and names what decided the state,
// so a caller can see which record to look at.
type OutrankedError struct {
	// Task is the task whose release was refused.
	Task string
	// State is the state the resolver answered.
	State taskstate.State
	// Source is the tier that answered it, which is never the event tier.
	Source taskstate.Source
}

// Error names the task, its state and the tier that decided it.
func (e *OutrankedError) Error() string {
	return fmt.Sprintf("tasks: task %s is queued by its history and %s by its %s, so its release is refused",
		e.Task, e.State, e.Source)
}

// Unwrap makes every refused release match ErrOutranked.
func (e *OutrankedError) Unwrap() error { return ErrOutranked }

// ErrPostureUnreadable is the class of an intake or a promotion refused
// because the project's delivery posture could not be established from its
// trusted copy: the copy could not be read or parsed, or it named a lighter
// posture than the operator's own configuration allows, which the wrapped
// *config.LoosenError says. PRD section 6 delivers nothing when that copy
// cannot be read and parsed, and assumes no posture in its place, not even
// gated; a loosening copy is refused on the same terms.
var ErrPostureUnreadable = errors.New("tasks: the project's delivery posture could not be read from its trusted configuration")

// ErrLighterPosture is the class every *PostureError belongs to.
var ErrLighterPosture = errors.New("tasks: a task may not be recorded under a lighter delivery posture than its project's")

// PostureError refuses an intake or a promotion that asked for a delivery
// posture lighter than the one the project declares. PRD section 6 lets the coordinator
// record a stricter posture for work it judges risky and never a lighter
// one, so no agent judgment weakens the path a project declared.
type PostureError struct {
	// Requested is the posture the intake or the promotion asked for.
	Requested config.Delivery
	// Project is the posture the project's trusted copy declares.
	Project config.Delivery
}

// Error names both postures.
func (e *PostureError) Error() string {
	return fmt.Sprintf("tasks: the project's delivery posture is %q and %q is lighter; a task may be recorded under "+
		"a stricter posture than its project's and never a lighter one", e.Project, e.Requested)
}

// Unwrap makes every refused posture match ErrLighterPosture.
func (e *PostureError) Unwrap() error { return ErrLighterPosture }
