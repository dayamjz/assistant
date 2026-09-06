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
// *vcs.Repository satisfies it. It did not when this interface was written,
// which is why the interface exists at all rather than Remove taking a
// concrete type: internal/vcs is the only package that invokes git, so the
// operation had to land there and this package could only declare what it
// needed until it did. What the interface still buys is that Remove refuses
// with ErrDetachUnsupported before deleting anything rather than completing
// half of a removal, whatever a caller's Opener returns.
//
// One operation is still owed to internal/vcs and cannot be declared here at
// all, because this package has no use for it beyond a check it therefore
// cannot make: reading a git configuration value, which is what noticing a
// core.hooksPath that redirects the gate's hooks would take. doc.go states
// that gap.
//
// Two checks in this package's tests keep the claims above honest. A
// compile-time assertion fails the build if RemoteURL or SetRemote drifts from
// what *vcs.Repository provides, and another fails if *vcs.Repository stops
// satisfying Detacher, which is the day the paragraph above stops being true.
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
// when path is not one. The default opens the working copy with internal/vcs,
// whose handle satisfies Detacher, so the default is enough for every
// operation here; it stays a seam so that a test can supply a working copy
// that cannot detach and check that a removal refuses rather than completing
// half of itself.
type Opener func(ctx context.Context, path string) (WorkingCopy, error)
