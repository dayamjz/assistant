package tasks

import (
	"errors"
	"fmt"

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
