package gate

import (
	"context"
)

// WorkingCopy is the part of a working copy this package writes to and reads
// from. It is an interface rather than a *vcs.Repository so that Remove can
// name the operation internal/vcs does not carry yet without this package
// growing a git invocation of its own; *vcs.Repository is the implementation
// this product uses, and it satisfies this interface today.
//
// The surface is deliberately two methods wide. PRD principle P1 makes an
// untouched origin the consent boundary, and an interface that cannot express
// a write to any remote but the one it is handed a name for is how this
// package's part of that is checkable by reading it.
type WorkingCopy interface {
	// RemoteURL returns the URL configured for a remote, and an error
	// matching vcs.ErrRemoteNotFound when there is no such remote. The URL
	// comes back exactly as configured, so a caller that reports or persists
	// it redacts it first.
	RemoteURL(ctx context.Context, name string) (string, error)
	// SetRemote points a remote at a URL, adding it when it does not exist
	// and changing its URL when it does. It must touch no other remote, and
	// running it again with the same arguments must change nothing.
	SetRemote(ctx context.Context, name, url string) error
}

// Detacher is a WorkingCopy that can also give a remote up again. Remove needs
// it, because a removal that deletes the gate's repository and leaves the
// working copy still pointing at it has not removed the gate.
//
// RemoveRemote is the one method *vcs.Repository does not implement today.
// This package does not add it, because internal/vcs is the only package that
// invokes git and adding an operation there is not this package's change to
// make. Until it exists, the typed operation vcs needs is the one declared
// here, which is git remote remove. This is a named follow-up, not an
// oversight, and Remove refuses with ErrDetachUnsupported before deleting
// anything rather than completing half of a removal.
//
// A second operation is owed to internal/vcs and cannot be declared here at
// all, because this package has no use for it beyond a check it therefore
// cannot make: reading a git configuration value, which is what noticing a
// core.hooksPath that redirects the gate's hooks would take. doc.go states
// that gap; git.go names it so the two follow-ups sit together.
//
// Two checks in this package's tests keep the claims above honest. A
// compile-time assertion fails the build if RemoteURL or SetRemote drifts from
// what *vcs.Repository provides, and a test fails on the day *vcs.Repository
// satisfies Detacher in full, which is the day the paragraph above stops being
// true.
type Detacher interface {
	WorkingCopy
	// RemoveRemote removes a remote and its configuration. Removing a remote
	// that does not exist must be reported as an error matching
	// vcs.ErrRemoteNotFound rather than silently succeeding, so a caller can
	// tell the two apart; Remove treats that particular error as nothing left
	// to do.
	RemoveRemote(ctx context.Context, name string) error
}

// Opener returns a handle on the working copy rooted at path, and an error
// when path is not one. It exists so that Remove can be given a working copy
// that satisfies Detacher while *vcs.Repository does not, and so that a test
// can supply the same. The default opens the working copy with internal/vcs.
type Opener func(ctx context.Context, path string) (WorkingCopy, error)
