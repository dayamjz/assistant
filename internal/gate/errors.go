package gate

import "errors"

// Errors a caller is expected to handle. Each is a typed result rather than a
// warning execution continues past, and each is returned before the mutation
// it refuses. Test with errors.Is.
var (
	// ErrInvalidSpec is returned when a Spec cannot describe a gate: a home
	// or working path that is not absolute, a working path that is not a
	// directory, or a hook command that is not an absolute path to an
	// executable file. The refusal happens before anything is created.
	ErrInvalidSpec = errors.New("gate: specification cannot describe a gate")
	// ErrTemplateHooks is returned when a repository this package has just
	// created was born carrying a hook. A git template chose that hook, which
	// means a process outside this one chose code that would run inside the
	// gate, so initialization refuses instead of adopting it. See doc.go for
	// how a template reaches a repository whose environment was filtered.
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
	// ErrNotAGate is returned when a path that would be deleted as a gate
	// repository is not one: it is outside this home's repository directory,
	// or it carries no gate record. Removal refuses rather than deleting a
	// directory it cannot identify.
	ErrNotAGate = errors.New("gate: path is not a gate repository of this home")
	// ErrNoGate is returned by Remove when the working copy has no gate to
	// remove.
	ErrNoGate = errors.New("gate: working copy has no gate")
	// ErrGateClaimed is returned by any operation asked to act on a gate
	// whose own record binds it to a different working copy that is still
	// pointing at it. Initialize meets it when two paths hash to one
	// identifier, and Remove when a copied project directory carries the
	// original's remote. Nothing has been created, written, or deleted when
	// it is returned. The message names the gate, the working copy holding
	// it, the working copy asking, and what has to change.
	ErrGateClaimed = errors.New("gate: another working copy holds the gate at this identifier")
	// ErrGateBindingInferred is returned by Remove when the gate's record says
	// its binding came from taking over a repository that carried no record.
	// Nothing but a remote said the gate was this working copy's, and a copy
	// of a gated project inherits that remote, so the binding is not evidence
	// enough for the one act here that cannot be undone. Nothing has been
	// removed when it is returned, and the message says what the operator can
	// do instead.
	ErrGateBindingInferred = errors.New("gate: gate binding rests on a remote rather than on a record")
	// ErrMalformedRecord is returned when a gate's record file exists but
	// cannot be read as one. The gate's binding to a working copy lives in
	// that record, so a record that cannot be read is a fact that cannot be
	// established rather than one to guess at.
	ErrMalformedRecord = errors.New("gate: gate record cannot be read")
)
