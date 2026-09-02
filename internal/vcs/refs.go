package vcs

import (
	"context"
	"strings"
)

// Ref is one reference and what it resolves to.
type Ref struct {
	// Name is the full reference name, such as refs/heads/main. A remote's
	// HEAD is reported under the name HEAD.
	Name string
	// Object is the object the reference points at directly. For an annotated
	// tag this is the tag object, not what the tag points at.
	Object string
	// Commit is the object the reference reaches once the annotated tags on
	// the way are followed, and equals Object when there are none. It is named
	// for the ordinary case, which is a branch or a tag on a commit.
	//
	// It is never empty, and nothing here checks that it holds a commit. A tag
	// on a blob or a tree puts that blob or tree here: for-each-ref reports a
	// peeled object without saying what type it is, and the ^{} line
	// ls-remote emits carries no type at all, so RemoteRefs could not check
	// one even if ListRefs did. A caller that needs a commit passes this to
	// ResolveCommit, which refuses an object that does not peel to one.
	Commit string
}

// ResolveCommit resolves rev to a full commit identifier in this repository.
// It returns ErrRefNotFound when rev names nothing, and when rev names an
// object that does not peel to a commit.
//
// Every operation here that takes a revision resolves it through this method
// first and then passes the resulting identifier to git. That is why those
// operations cannot mistake a revision for a path or for an option, and why a
// name that does not exist fails as ErrRefNotFound rather than as whatever git
// would have said about the command it was going to be part of.
func (r *Repository) ResolveCommit(ctx context.Context, rev string) (string, error) {
	if err := checkArg("revision", rev); err != nil {
		return "", err
	}
	// With --quiet, git reports an unresolvable revision as exit status 1 and
	// writes nothing. Any other status is a real failure.
	out, code, err := r.runExpecting(ctx, "resolve-commit", []int{1},
		"rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if code != 0 || id == "" {
		return "", &refError{rev: rev, repo: r.path}
	}
	return id, nil
}

// HeadBranch returns the short name of the branch HEAD is on. It returns
// ErrDetachedHead when HEAD is not on a branch.
//
// A bare repository has a HEAD even with no commits, so this reports the
// branch an empty repository would receive its first commit on.
func (r *Repository) HeadBranch(ctx context.Context) (string, error) {
	out, code, err := r.runExpecting(ctx, "head-branch", []int{1}, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if code != 0 || name == "" {
		return "", ErrDetachedHead
	}
	return name, nil
}

// ListRefs returns the references in this repository whose names match any of
// the given patterns, in git's own order. With no patterns it returns every
// reference. A pattern is matched the way git for-each-ref matches one: a
// pattern that names a prefix, such as refs/heads/, matches everything under
// it.
func (r *Repository) ListRefs(ctx context.Context, patterns ...string) ([]Ref, error) {
	args := []string{"for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)"}
	for _, p := range patterns {
		if err := checkArg("ref pattern", p); err != nil {
			return nil, err
		}
	}
	if len(patterns) > 0 {
		args = append(args, "--")
		args = append(args, patterns...)
	}
	out, err := r.run(ctx, "list-refs", args...)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 {
			return nil, &outputError{op: "list-refs", detail: "expected three fields per reference, got " + line}
		}
		ref := Ref{Name: parts[0], Object: parts[1], Commit: parts[1]}
		if parts[2] != "" {
			// A non-empty peeled object means the reference is an annotated
			// tag, and the peeled object is what it points at.
			ref.Commit = parts[2]
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// RemoteRefs reads the references a remote advertises, without changing
// anything in this repository. remote is a configured remote name or a URL,
// and patterns restrict the result the way git ls-remote does.
//
// This is what a data-loss policy reads to learn where a remote branch stands
// now. This package only reads it; deciding whether an update may proceed
// against it belongs to the safety module.
func (r *Repository) RemoteRefs(ctx context.Context, remote string, patterns ...string) ([]Ref, error) {
	if err := checkArg("remote", remote); err != nil {
		return nil, err
	}
	args := []string{"ls-remote", "--end-of-options", remote}
	for _, p := range patterns {
		if err := checkArg("ref pattern", p); err != nil {
			return nil, err
		}
		args = append(args, p)
	}
	out, err := r.run(ctx, "remote-refs", args...)
	if err != nil {
		return nil, err
	}
	var (
		refs  []Ref
		index = map[string]int{}
	)
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		object, name, ok := strings.Cut(line, "\t")
		if !ok || object == "" || name == "" {
			return nil, &outputError{op: "remote-refs", detail: "expected an object and a name, got " + line}
		}
		if base, peeled := strings.CutSuffix(name, "^{}"); peeled {
			// A peeled line follows the annotated tag it belongs to and gives
			// the commit that tag resolves to.
			if i, ok := index[base]; ok {
				refs[i].Commit = object
			}
			continue
		}
		index[name] = len(refs)
		refs = append(refs, Ref{Name: name, Object: object, Commit: object})
	}
	return refs, nil
}

// IsAncestor reports whether ancestor is reachable from descendant, which is
// what makes an update to descendant a fast-forward over ancestor. A commit is
// its own ancestor.
//
// This answers one question and draws no conclusion from it. Whether a
// particular update may proceed is the safety module's decision.
func (r *Repository) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	a, err := r.ResolveCommit(ctx, ancestor)
	if err != nil {
		return false, err
	}
	d, err := r.ResolveCommit(ctx, descendant)
	if err != nil {
		return false, err
	}
	_, code, err := r.runExpecting(ctx, "is-ancestor", []int{1}, "merge-base", "--is-ancestor", a, d)
	if err != nil {
		return false, err
	}
	// git merge-base --is-ancestor answers with its exit status: 0 yes, 1 no.
	// Any other status is a failure and has already been returned as one.
	return code == 0, nil
}

// MergeBase returns the best common ancestor of two commits. It returns
// ErrNoMergeBase when they share none, which is what unrelated histories look
// like.
func (r *Repository) MergeBase(ctx context.Context, a, b string) (string, error) {
	ca, err := r.ResolveCommit(ctx, a)
	if err != nil {
		return "", err
	}
	cb, err := r.ResolveCommit(ctx, b)
	if err != nil {
		return "", err
	}
	out, code, err := r.runExpecting(ctx, "merge-base", []int{1}, "merge-base", ca, cb)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if code != 0 || id == "" {
		return "", ErrNoMergeBase
	}
	return id, nil
}

// refError reports a revision that does not resolve to a commit.
type refError struct {
	rev  string
	repo string
}

func (e *refError) Error() string {
	return "vcs: " + e.rev + " does not resolve to a commit in " + e.repo
}

// Unwrap makes every unresolvable revision match ErrRefNotFound.
func (e *refError) Unwrap() error { return ErrRefNotFound }

// outputError reports git output that did not have the shape the requested
// format promises. Parsing stops rather than skipping the line, because a
// silently skipped reference or change is one nobody sees.
type outputError struct {
	op     string
	detail string
}

func (e *outputError) Error() string { return "vcs: " + e.op + ": " + e.detail }

// Unwrap makes every parse refusal match ErrMalformedOutput.
func (e *outputError) Unwrap() error { return ErrMalformedOutput }

// checkArg refuses an argument before git is invoked when git would read it as
// an option, when it is empty, or when it carries a byte that cannot survive a
// command line or a NUL-separated output format.
//
// It deliberately does not decide what a valid branch or reference name is.
// Git owns that rule and reports it, and a second copy of it here would be two
// mechanisms answering one question.
func checkArg(what, value string) error {
	switch {
	case value == "":
		return &argumentError{what: what, value: value, reason: "must not be empty"}
	case strings.HasPrefix(value, "-"):
		return &argumentError{what: what, value: value, reason: "would be read by git as an option"}
	case strings.ContainsAny(value, "\x00\n"):
		return &argumentError{what: what, value: value, reason: "must not contain a NUL byte or a newline"}
	}
	return nil
}
