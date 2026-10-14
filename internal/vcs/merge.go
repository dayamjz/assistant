package vcs

import (
	"context"
	"errors"
	"strings"
)

// The mechanism under internal/safety's equivalence proof: every merge base of
// two commits, the tree a three-way merge of two commits produces over a base
// the caller names, and the tree a commit records. Each answers one question
// and draws no conclusion from it; whether two histories are equivalent is
// internal/safety's decision.

// ErrRefExists is returned by CreateRef when the reference it was asked to
// create already exists, whatever it names. Nothing is written.
var ErrRefExists = errors.New("vcs: the reference already exists")

// mergeTreeAtFloor is the git version MergeTreeAt needs, as major and minor:
// the release whose merge-tree takes a merge base named on the command line.
var mergeTreeAtFloor = [2]int{2, 40}

// TreeOf returns the tree identifier the commit rev records. rev is resolved
// through ResolveCommit first, so a revision that names no commit is
// ErrRefNotFound.
func (r *Repository) TreeOf(ctx context.Context, rev string) (string, error) {
	id, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return "", err
	}
	return r.treeOf(ctx, id)
}

// MergeBases returns every best common ancestor of two commits, which is git
// merge-base --all, in the order git reports them. It returns ErrNoMergeBase
// when they share none. MergeBase returns one of these; a caller that has to
// know whether the choice was forced asks this instead, because two histories
// with more than one best common ancestor have no single base to state a
// merge against.
func (r *Repository) MergeBases(ctx context.Context, a, b string) ([]string, error) {
	ca, err := r.ResolveCommit(ctx, a)
	if err != nil {
		return nil, err
	}
	cb, err := r.ResolveCommit(ctx, b)
	if err != nil {
		return nil, err
	}
	out, code, err := r.runExpecting(ctx, "merge-base-all", []int{1}, "merge-base", "--all", ca, cb)
	if err != nil {
		return nil, err
	}
	bases := strings.Fields(string(out))
	if code != 0 || len(bases) == 0 {
		return nil, ErrNoMergeBase
	}
	return bases, nil
}

// MergeTreeAt returns the tree a three-way merge of ours and theirs produces
// over base, with base named by the caller rather than chosen by git, and
// ErrMergeConflict when the merge does not resolve cleanly. All three are
// resolved through ResolveCommit first.
//
// It writes the merged tree's objects into this repository's object store,
// which no reference names, and writes nothing else: no reference, no index,
// and no working tree. A caller that has to state it changes nothing in a
// repository runs this somewhere it may write objects.
//
// It needs git 2.40 or newer and checks that floor before running, refusing
// with ErrUnsupportedGit below it, because a git that did not read the base
// would answer about a merge the caller did not ask for.
func (r *Repository) MergeTreeAt(ctx context.Context, base, ours, theirs string) (string, error) {
	if err := r.requireGitFloor(ctx, mergeTreeAtFloor); err != nil {
		return "", err
	}
	ids := make([]string, 0, 3)
	for _, rev := range []string{base, ours, theirs} {
		id, err := r.ResolveCommit(ctx, rev)
		if err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	// git merge-tree --write-tree answers with its exit status: 0 a clean
	// result whose first line names the tree, 1 a conflict. Any other
	// status is a failure and has already been returned as one.
	out, code, err := r.runExpecting(ctx, "merge-tree-at", []int{1},
		"merge-tree", "--write-tree", "--no-messages", "--merge-base="+ids[0], ids[1], ids[2])
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", ErrMergeConflict
	}
	tree, _, _ := strings.Cut(string(out), "\n")
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return "", &outputError{op: "merge-tree-at", detail: "expected a tree identifier on the first line, got " + string(out)}
	}
	return tree, nil
}

// CreateRef writes the reference name at commit only when name does not exist
// yet, and refuses with ErrRefExists when it does, so a reference this writes
// never replaces one somebody else wrote. The check and the write are one git
// update-ref given the empty old value, which is git's own create-only
// compare-and-swap. commit is resolved through ResolveCommit first, so the
// object has to be in this repository already: nothing is fetched.
//
// The name is given in full, as refs/..., on UpdateRef's terms.
func (r *Repository) CreateRef(ctx context.Context, name, commit string) error {
	if err := checkArg("ref", name); err != nil {
		return err
	}
	if !strings.HasPrefix(name, "refs/") {
		return &argumentError{what: "ref", value: name, reason: "is not a full reference name, which begins refs/"}
	}
	id, err := r.ResolveCommit(ctx, commit)
	if err != nil {
		return err
	}
	if _, err := r.run(ctx, "update-ref-create", "update-ref", "--no-deref", "--end-of-options", name, id, ""); err != nil {
		if existing, readErr := r.ResolveCommit(ctx, name); readErr == nil {
			return errors.Join(&refExistsError{name: name, names: existing}, err)
		}
		return err
	}
	return nil
}

// refExistsError reports a reference CreateRef found already written.
type refExistsError struct{ name, names string }

func (e *refExistsError) Error() string {
	return "vcs: " + e.name + " already exists and names " + e.names
}

// Unwrap makes the refusal match ErrRefExists.
func (e *refExistsError) Unwrap() error { return ErrRefExists }

// IsShallow reports whether this repository's history is cut short, which
// is git rev-parse --is-shallow-repository. In a shallow repository a commit
// the repository cannot resolve may still be an ancestor of one it can, so a
// caller that reads "not here" as "not contained" asks this first.
func (r *Repository) IsShallow(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, "is-shallow", "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, &outputError{op: "is-shallow", detail: "expected true or false, got " + string(out)}
}
