package vcs

import (
	"context"
	"fmt"
	"strings"
)

// Lease is what a remote reference must already be for a push to be allowed to
// change it. It is git's --force-with-lease expectation as a typed value.
//
// There is no zero lease that means "no lease". PushSpec requires one, so a
// push through this package always carries an expectation about what it is
// overwriting, and the argument vector never reaches git without one. That is
// mechanism rather than convention: a caller cannot forget the lease, because
// there is no shape of this type that omits it.
//
// What it does not decide is whether the expectation is worth anything. A
// lease built from a read taken a moment earlier is a lease that always holds,
// which is the trap PRD principle P6 names outright. Where the expectation
// comes from is internal/safety's question, and Decision.Anchor is its answer.
type Lease struct {
	// commit is the commit the reference must currently name, empty when the
	// reference must not exist at all.
	commit string
}

// LeaseAbsent is the expectation that the reference does not exist yet, which
// is what a push creating a branch is allowed on.
func LeaseAbsent() Lease { return Lease{} }

// LeaseAt is the expectation that the reference currently names commit. A push
// under it is refused when the reference has moved, been deleted, or never
// existed, whether or not the update would discard anything.
func LeaseAt(commit string) Lease { return Lease{commit: commit} }

// String renders the lease as the commit expected, or as absent.
func (l Lease) String() string {
	if l.commit == "" {
		return "absent"
	}
	return l.commit
}

// PushSpec describes one reference update to perform on a remote.
type PushSpec struct {
	// Remote is a configured remote name or a URL. It is required.
	Remote string
	// Commit is the commit to put on the remote reference. It is required,
	// and it is a commit rather than a revision so that what lands on the
	// remote is the object the caller decided about and not whatever a name
	// resolves to at the moment of the push.
	Commit string
	// Ref is the full reference name to update, such as refs/heads/work. It is
	// required and it is full, because this updates whatever the name resolves
	// to and a short name resolves through git's disambiguation rules.
	Ref string
	// Lease is what Ref must already be for this update to proceed.
	Lease Lease
}

// Push updates one reference on a remote, under a lease.
//
// It performs the update and decides nothing about it: whether an update may
// proceed, and on what expectation, is internal/safety's decision, and this is
// the mechanism that decision is carried out with.
//
// # What the lease buys, and what it does not
//
// The update is refused unless the remote reference is exactly what the lease
// expects at the moment the remote processes the push. That covers a reference
// that moved, one that was deleted, and one that appeared where the lease
// expected absence.
//
// It is stated as a property of this operation rather than as a description of
// how a remote behaves internally, because that is what a reader can check
// here. What this package does is pass the expectation on every push and never
// offer a way to push without one; what the remote does with it is the remote's
// behaviour, and a remote that ignored it would leave nothing here to notice.
//
// The residual gap is worth naming: this reports that the update was refused
// and does not report what the reference stood at instead. A caller that needs
// to know reads the remote again, and gets a fresh answer rather than the one
// the refusal was made against.
//
// Nothing here pushes more than one reference, and nothing here deletes one.
// Both are deliberate: a caller that has a decision has one about one
// reference, and this package has no operation that removes a reference from a
// remote at all.
func (r *Repository) Push(ctx context.Context, spec PushSpec) error {
	if err := checkArg("remote", spec.Remote); err != nil {
		return err
	}
	if err := checkArg("commit", spec.Commit); err != nil {
		return err
	}
	if err := checkArg("ref", spec.Ref); err != nil {
		return err
	}
	if !strings.HasPrefix(spec.Ref, "refs/") {
		return fmt.Errorf("%w: %q is not a full reference name, which begins refs/",
			ErrInvalidArgument, spec.Ref)
	}
	if spec.Lease.commit != "" {
		if err := checkArg("lease", spec.Lease.commit); err != nil {
			return err
		}
	}
	// The lease is built here rather than taken as an argument because it is
	// an option: checkArg refuses a value that git would read as one, so a
	// caller could not have passed this through.
	lease := "--force-with-lease=" + spec.Ref + ":" + spec.Lease.commit
	_, err := r.run(ctx, "push", "push", "--quiet", "--no-recurse-submodules", lease,
		"--end-of-options", spec.Remote, spec.Commit+":"+spec.Ref)
	return err
}
