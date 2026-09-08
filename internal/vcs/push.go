package vcs

import (
	"context"
	"strings"
)

// Lease is the state a destination reference must already be in for a push to
// proceed. It is the compare-and-swap the update is performed under: the push
// is refused by the remote unless the reference still stands where this says
// it does.
//
// Absent is a state rather than a missing value, the way it is for a read. A
// reference that must not exist yet and a reference that must name a
// particular commit are both leases, and a lease that could not express the
// first would leave a creation to be performed with no lease at all.
//
// What a lease is worth depends on where its Commit came from, and that is not
// this package's question. Nothing here can tell a commit a run observed
// before doing its work from the tip it read a moment ago, so PRD principle
// P6's rule about which one an update may be anchored to lives in
// internal/safety, which is the only producer of a value fit to put here.
type Lease struct {
	// Exists reports whether the destination must currently exist.
	Exists bool
	// Commit is the commit it must currently name, and must be empty when
	// Exists is false.
	Commit string
}

// PushSpec describes one reference update on a remote.
//
// There is no field that turns the lease off, and that is the shape rather
// than an omission: every push this package performs is a compare-and-swap
// against a state the caller states in advance. A caller that wants an
// unleased push does not get one here.
//
// The narrowness of that is worth being exact about. It buys that no update
// this package performs overwrites a reference whose current value the caller
// did not name, which is the failure mode a bare force has. It does not buy
// that the value named is one worth leasing on; see Lease.
type PushSpec struct {
	// Remote is a configured remote name or a URL. It is required.
	Remote string
	// Ref is the full reference name to update on the remote, such as
	// refs/heads/main. It is required, and it is a full name because a short
	// one is ambiguous on the wire.
	Ref string
	// Commit is the revision in this repository the reference is to name. It
	// is resolved here, so a revision that does not resolve fails before the
	// remote is contacted.
	Commit string
	// Lease is the state Ref must already be in for the update to proceed.
	Lease Lease
}

// Push updates one reference on a remote to name a commit this repository
// holds, under the lease the spec carries.
//
// It performs the update and reports what happened to it. It decides nothing
// about whether the update should have been proposed: which commit may replace
// which, and on what anchor, is internal/safety's question, and this package
// deliberately answers none of it.
//
// A remote that refuses the update returns a *PushRejection, which matches
// ErrPushRejected. A lease that no longer holds is the case this exists for,
// and it is not the only one a remote may refuse for: a hook on the receiving
// side refuses through the same channel. The rejection carries git's own
// reason as text rather than classifying it, because the set of reasons a
// remote may give is the remote's and not this package's to enumerate.
//
// Nothing is fetched, merged, or moved locally. The only thing that changes is
// the reference on the remote.
//
// Submodule recursion is disabled, on the same grounds as Fetch: the
// configuration that turns it on makes a push contact further URLs named by
// the .gitmodules of the branch being pushed, and PRD principle P7 does not
// let the branch under validation choose what is contacted. A pre-push hook is
// not disabled, because a hook can only refuse an update, and refusing is what
// this operation is already built to report.
func (r *Repository) Push(ctx context.Context, spec PushSpec) error {
	if err := checkArg("remote", spec.Remote); err != nil {
		return err
	}
	if err := checkArg("reference", spec.Ref); err != nil {
		return err
	}
	if !strings.HasPrefix(spec.Ref, "refs/") {
		return &argumentError{what: "reference", value: spec.Ref, reason: "must be a full refs/ name"}
	}
	lease, err := leaseArgument(spec)
	if err != nil {
		return err
	}
	commit, err := r.ResolveCommit(ctx, spec.Commit)
	if err != nil {
		return err
	}
	// Exit status 1 is what git push reports when the remote refused an
	// update, which the porcelain lines below say which of. Any other status
	// is a failure and comes back as a *CommandError.
	out, code, err := r.runExpecting(ctx, "push", []int{1},
		"push", "--porcelain", "--no-recurse-submodules", lease, "--end-of-options",
		spec.Remote, commit+":"+spec.Ref)
	if err != nil {
		return err
	}
	rejected, err := r.rejectedRef(out)
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	if code != 0 {
		// git exited as though something was refused and the porcelain lines
		// named nothing refused. Reporting success here would be reading a
		// failure as a push that happened, so this refuses instead.
		return &outputError{op: "push", detail: "git reported a failed push and no rejected reference"}
	}
	return nil
}

// leaseArgument renders the spec's lease as the option that carries it. An
// empty expected value is how the wire format says the reference must not
// already exist, so the two states of a Lease are one argument rather than two
// code paths.
//
// It refuses a lease that describes no state a read could produce, which is an
// absent reference naming a commit or a present one naming none. Such a value
// would otherwise be sent as a lease on absence, which is the opposite of what
// a caller that filled in a commit meant.
func leaseArgument(spec PushSpec) (string, error) {
	switch {
	case spec.Lease.Exists && spec.Lease.Commit == "":
		return "", &argumentError{what: "lease", value: "",
			reason: "says the reference exists and does not say what it names"}
	case !spec.Lease.Exists && spec.Lease.Commit != "":
		return "", &argumentError{what: "lease", value: spec.Lease.Commit,
			reason: "says the reference does not exist and also names a commit"}
	}
	if spec.Lease.Commit != "" {
		if err := checkArg("lease", spec.Lease.Commit); err != nil {
			return "", err
		}
	}
	return "--force-with-lease=" + spec.Ref + ":" + spec.Lease.Commit, nil
}

// rejectedRef returns the rejection the porcelain output reports, or nil when
// it reports none.
//
// The format is one line per reference: a one-character flag, the refspec, and
// a summary, separated by tabs. The flag is what is read here, because it is
// the field that says what happened; the summary is carried along as the
// reason without being interpreted, since what a remote may say is the
// remote's vocabulary. A line this package cannot read at all is refused
// rather than skipped, because a skipped rejection reads as a push that
// succeeded.
func (r *Repository) rejectedRef(out []byte) (*PushRejection, error) {
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		// Only the reference lines carry tabs. "To <url>", "Done", and the
		// error lines git writes around them do not, and none of them reports
		// the fate of a reference.
		if line == "" || !strings.Contains(line, "\t") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 || len([]rune(fields[0])) != 1 {
			return nil, &outputError{op: "push", detail: "expected a flag, a refspec and a summary, got " + line}
		}
		if fields[0] != "!" {
			continue
		}
		refspec := fields[1]
		_, ref, _ := strings.Cut(refspec, ":")
		if ref == "" {
			ref = refspec
		}
		return &PushRejection{
			Ref: r.set.redactor.Redact(ref),
			// The summary is git's own text and the only line of this output
			// that reaches a report, so it is redacted like every other
			// message this package hands back.
			Reason: r.set.redactor.Redact(strings.Join(fields[2:], " ")),
		}, nil
	}
	return nil, nil
}

// PushRejection reports that a remote refused an update. It is the answer to
// the question a push asks, not a warning a caller may continue past: no
// reference moved.
type PushRejection struct {
	// Ref is the reference on the remote the update would have moved.
	Ref string
	// Reason is git's own account of why the remote refused, carried as the
	// text git produced. This package does not classify it, so a caller
	// reports it rather than branching on it.
	Reason string
}

func (e *PushRejection) Error() string {
	return "vcs: the remote refused to update " + e.Ref + ": " + e.Reason
}

// Unwrap makes every rejection match ErrPushRejected.
func (e *PushRejection) Unwrap() error { return ErrPushRejected }

// CommitsNotIn returns the commits reachable from have and not from
// incorporated, most recent first. An empty result means incorporated already
// contains everything have does, so replacing have with incorporated loses no
// commit.
//
// Both revisions are resolved in this repository, so a commit only the remote
// holds is ErrRefNotFound rather than an answer drawn from history this
// repository does not have. A caller that needs such a commit counted fetches
// it first.
//
// It answers one question and draws no conclusion from it. Whether an update
// that would drop these commits may proceed is internal/safety's decision.
func (r *Repository) CommitsNotIn(ctx context.Context, have, incorporated string) ([]string, error) {
	h, err := r.ResolveCommit(ctx, have)
	if err != nil {
		return nil, err
	}
	i, err := r.ResolveCommit(ctx, incorporated)
	if err != nil {
		return nil, err
	}
	out, err := r.run(ctx, "commits-not-in", "rev-list", "--end-of-options", i+".."+h)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		commits = append(commits, line)
	}
	return commits, nil
}
