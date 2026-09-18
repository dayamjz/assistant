package vcs

import (
	"context"
	"errors"
	"strings"
)

// ErrNothingToCommit reports that the working copy held no change to commit, so
// CommitAll wrote nothing and the repository still stands where it did.
//
// It is a typed answer rather than a silent success because the difference
// matters to every caller there is: an agent asked to fix something and
// changing no file is the case a fix loop has to converge on, and a commit
// operation that reported success on it would leave that loop unable to tell
// progress from none.
var ErrNothingToCommit = errors.New("vcs: the working copy holds no change to commit")

// CommitAll records every change in the working copy - modified, deleted, and
// untracked alike - as one commit, and returns the commit it wrote.
//
// It returns ErrNothingToCommit when there is nothing to record, having written
// nothing.
//
// # What "every change" takes in
//
// Untracked files are included. A fix that adds a file is a fix, and a commit
// operation that silently left new files out of the change it claims to have
// recorded would produce a commit that does not contain the work, which every
// stage after it then validates the absence of.
//
// What is never included is anything git is configured to ignore. That is
// git's own rule and this does not override it: a caller wanting an ignored
// file recorded has a repository configuration question, not a commit
// question.
//
// # The author, and why it is not an argument
//
// The commit is made under whatever identity the environment this package
// builds provides. Nothing here takes an author, because a caller that could
// set one could attribute a commit to a person who did not make it, and the
// product this serves runs agents under one person's own credentials.
//
// # What it does not do
//
// It does not push, does not move a branch other than the one HEAD is on, and
// does not amend. A detached HEAD is committed onto exactly as a branch is:
// the commit is written and HEAD moves to it, which is what an isolated copy
// checked out detached wants.
func (r *Repository) CommitAll(ctx context.Context, subject string) (string, error) {
	if r.kind != KindWorktree {
		return "", ErrNotARepository
	}
	if err := checkArg("subject", subject); err != nil {
		return "", err
	}
	// Staging first is what makes the emptiness check and the commit agree:
	// both then read the index, so a file that arrived between the two cannot
	// make one see work the other does not.
	if _, err := r.run(ctx, "add-all", "add", "--all", "--"); err != nil {
		return "", err
	}
	staged, err := r.stagedChange(ctx)
	if err != nil {
		return "", err
	}
	if !staged {
		return "", ErrNothingToCommit
	}
	if _, err := r.run(ctx, "commit", "commit", "--quiet", "--no-verify", "--message", subject); err != nil {
		return "", err
	}
	return r.ResolveCommit(ctx, "HEAD")
}

// stagedChange reports whether the index holds anything the next commit would
// record.
//
// It asks git whether the index differs from HEAD rather than parsing a status
// listing, so the answer is git's own and not this package's reading of text.
// A repository with no commits yet has no HEAD to compare against, and there
// the question is whether the index holds anything at all.
func (r *Repository) stagedChange(ctx context.Context) (bool, error) {
	if _, err := r.ResolveCommit(ctx, "HEAD"); err != nil {
		if !errors.Is(err, ErrRefNotFound) {
			return false, err
		}
		out, listErr := r.run(ctx, "ls-files-staged", "ls-files", "--cached")
		if listErr != nil {
			return false, listErr
		}
		return len(strings.TrimSpace(string(out))) > 0, nil
	}
	_, code, err := r.runExpecting(ctx, "diff-index", []int{1},
		"diff-index", "--quiet", "--cached", "HEAD", "--")
	if err != nil {
		return false, err
	}
	// git diff-index --quiet answers with its exit status: 0 no difference,
	// 1 a difference. Any other status is a failure and has already been
	// returned as one.
	return code == 1, nil
}
