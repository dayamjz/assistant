package gate

import "errors"

// Errors a caller is expected to handle. Each is a typed result rather than a
// warning execution continues past, and each is returned before the mutation
// it refuses. Test with errors.Is.
//
// One write is common to every refusal here and is stated once rather than in
// each of them. Obtaining a gate seals any gate repository the resolution
// observed that has no admission hook, so a refusal leaves a gate that refuses
// every push rather than one that accepts every push with nothing checking
// them. Where a refusal below says nothing was created, written, or deleted, it
// means nothing beyond that seal, and where the refusal happens before any
// repository has been observed there is nothing to seal either. A seal that
// fails is joined onto the refusal rather than hidden behind it, so a refusal
// below is never returned alone over a gate that was left open; both stay
// matchable with errors.Is, and the joined failure names the admission hook it
// could not write, whichever step it failed at, so the reader is told which
// gate is open. See doc.go for why that outranks the losses the other refusals
// prevent.
var (
	// ErrInvalidSpec is returned when a Spec cannot describe a gate: a home
	// or working path that is not absolute, a working path that is not a
	// directory, or a hook command that is not an absolute path to an
	// executable file. A spec whose paths do not describe a gate is refused
	// before any gate is looked at; a hook command is refused after, so that
	// the gate it would have been written into is sealed first.
	ErrInvalidSpec = errors.New("gate: specification cannot describe a gate")
	// ErrNoIndex is returned by every operation when no Index was supplied.
	// Whether another working copy is still bound to a gate is the question
	// that decides whether a gate may be adopted or deleted, and without an
	// index it can only be inferred. This package refuses rather than infer,
	// so an operation that would have guessed does nothing instead. See
	// WithIndex.
	ErrNoIndex = errors.New("gate: no ownership index was supplied")
	// ErrTemplateHooks is returned when a hook that was not in the gate's
	// hooks directory before the git invocation an initialization makes is
	// there afterwards, whether that invocation created the repository or
	// repaired one that was already there. A git template chose that hook,
	// which means a process outside this one chose code that would run inside
	// the gate, so initialization refuses instead of adopting it. See doc.go
	// for how a template reaches a repository whose environment was filtered,
	// and for the one hook name this can no longer fire for on a repair.
	ErrTemplateHooks = errors.New("gate: repository was created carrying hooks from a git template")
	// ErrCustomHookConflict is returned when a hook this package did not
	// write has to be moved aside and the name it would move to is already
	// taken. Both files were written by somebody, and this package discards
	// neither.
	ErrCustomHookConflict = errors.New("gate: a custom hook cannot be preserved without overwriting another")
	// ErrDetachUnsupported is returned by Remove when the working copy it
	// opened cannot remove a remote. Nothing has been removed when it is
	// returned. See Detacher for the operation internal/vcs still owes.
	ErrDetachUnsupported = errors.New("gate: working copy cannot remove a remote")
	// ErrNotACopy is returned by RemoveCopy when something stands where a
	// run's isolated copy should be and is not a worktree. It is refused
	// rather than removed: this package did not create it, and answering that
	// the copy was given back would leave a caller believing a directory is
	// gone while it is still on disk.
	ErrNotACopy = errors.New("gate: path is not an isolated copy")
	// ErrWorkUnreachable is returned by RemoveCopy when no reference in the
	// gate contains the copy's head, so removing it would leave the commits
	// made in it referenced by nothing. It is the data-loss refusal PRD
	// principle P12 asks for, and the run is not failed for it: the copy is
	// preserved and the refusal reported.
	//
	// What it establishes is bounded. It says the commits are still
	// referenced in the gate and so will not be collected; it is not a claim
	// that the work reached the upstream remote or a merged pull request,
	// which are the other two proofs P12 lists.
	ErrWorkUnreachable = errors.New("gate: the isolated copy holds work no reference in the gate contains")
	// ErrNotAGate is returned when a path that would be deleted as a gate
	// repository is not one: it is outside this home's repository directory,
	// nothing is there, or it carries no gate record. Removal refuses rather
	// than deleting a directory it cannot identify. The message names the step
	// that succeeds from the state the reader is in, which is an
	// initialization when one would establish the binding and a detachment
	// when it would not. A path outside the repository directory is always the
	// second: nothing there is a gate of this home, so there is no binding an
	// initialization could establish over it.
	ErrNotAGate = errors.New("gate: path is not a gate repository of this home")
	// ErrNoGate is returned by Remove when the working copy has no gate to
	// remove.
	ErrNoGate = errors.New("gate: working copy has no gate")
	// ErrGateClaimed is returned by any operation asked to act on a gate
	// another working copy is still bound to. Initialize meets it when the
	// gate its path hashes to is held elsewhere, and Remove when a copied
	// project directory carries the original's remote. Nothing has been
	// created, written, or deleted when it is returned. The message names the
	// gate, the working copy holding it, the working copy asking, and what has
	// to change.
	ErrGateClaimed = errors.New("gate: another working copy is bound to this gate")
	// ErrGateUnbound is returned by WorkingCopyFor when a gate that is there
	// does not resolve to exactly one working copy: none standing today names
	// it, or several do. The two are one refusal because the caller's answer
	// is the same for both - it does not know which repository a push to that
	// gate is about, so it has nothing to validate the push against - and the
	// message says which of the two happened and what changes it.
	ErrGateUnbound = errors.New("gate: gate does not belong to exactly one working copy")
	// ErrMalformedRefUpdate is returned by ParseRefUpdates when a line is not
	// a reference update git would have written. No updates come back with it:
	// admission decides whether a push proceeds, and deciding over the lines
	// that happened to parse is deciding about a push nobody described.
	ErrMalformedRefUpdate = errors.New("gate: reference update line is malformed")
	// ErrMalformedRecord is returned when a gate's record file exists but
	// cannot be read as one. A record that cannot be read is a fact that
	// cannot be established rather than one to guess at. Both operations
	// refuse on it, and the message names both steps that get a reader out
	// because which one applies depends on who is asking. For the working copy
	// the gate is filed under, and for one that moved and would otherwise lose
	// the history in it, the gate is reached by the path hash too, so detaching
	// does not help and removing the file is the step that does, which leaves
	// the gate and everything it holds. For a working copy that reaches the
	// gate only through an inherited remote, the path hash names a different,
	// empty path, so detaching and initializing gives it a gate of its own and
	// takes nothing from anyone. Every producer goes through one constructor
	// that attaches both, so a refusal with no action is not something a new
	// producer can write by omission.
	ErrMalformedRecord = errors.New("gate: gate record cannot be read")
)
