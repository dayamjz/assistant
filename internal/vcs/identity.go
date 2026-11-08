package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoUserIdentity is returned when git's configuration names no user.name
// or no user.email for this process. It is the typed refusal UserIdentity
// makes, so a caller can tell a missing identity from a git that would not
// run.
var ErrNoUserIdentity = errors.New("vcs: git's configuration names no user identity")

// UserIdentity is the name and email git's configuration gives this process
// for commits when no repository-local identity overrides them: user.name
// and user.email as ConfigValue reports them under the process environment
// (global and system configuration, and any GIT_CONFIG_GLOBAL the
// environment names). Either key unset is ErrNoUserIdentity. It does not
// settle a host-derived identity: a process with no configured name or
// email is refused even where git var would invent one from the user and
// host, which is the answer a caller that wants only a configured identity
// to carry elsewhere needs.
func UserIdentity(ctx context.Context, opts ...Option) (name, email string, err error) {
	dir, err := os.MkdirTemp("", "assistant-user-identity-*")
	if err != nil {
		return "", "", fmt.Errorf("vcs: making a scratch directory for UserIdentity: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	repo, err := InitBare(ctx, filepath.Join(dir, "bare.git"), opts...)
	if err != nil {
		return "", "", err
	}
	name, nameSet, err := repo.ConfigValue(ctx, "user.name")
	if err != nil {
		return "", "", err
	}
	email, emailSet, err := repo.ConfigValue(ctx, "user.email")
	if err != nil {
		return "", "", err
	}
	if !nameSet || name == "" || !emailSet || email == "" {
		return "", "", ErrNoUserIdentity
	}
	return name, email, nil
}

// Identity is the author and the committer git names for a commit made in a
// repository, each as git writes it into a commit's header without the
// timestamp: a name and an email address in angle brackets.
type Identity struct {
	// Author is who a commit made now would be attributed to.
	Author string
	// Committer is who a commit made now would be recorded as committed by.
	Committer string
}

// CommitIdentity asks git which author and committer a commit made in this
// repository would be recorded under, and returns git's answer, or the
// *CommandError git refused with when it cannot name one.
//
// It is the read assistant doctor uses to report a missing identity before a
// fix round meets it, so what it buys is agreement with CommitAll on the
// inputs this package controls: it is asked under the same environment and the
// same repository addressing every invocation here gets, which are the ones
// CommitAll commits under, so a change to either reaches both. It asks with
// git var, and that git var and git commit settle an identity from the same
// sources is git's to keep, not this package's; identity_test.go checks the
// case a caller relies on, that an identity this refuses is one CommitAll
// refuses too, and that an identity it names is the one CommitAll records.
//
// It does not say why git refused. The failure is returned as git described
// it rather than read for a reason, so a caller reports it and does not
// branch on its text.
//
// What it answers is the environment of the process that asked. A commit made
// by another process, with its own environment, can be recorded under another
// identity or refused where this answered, because two inputs come from the
// caller's environment and not from anything this package writes: the
// configuration file locations, which the package doc says are kept on
// purpose, and the identity variables such as GIT_AUTHOR_NAME, which are not
// on redirectingVars.
func (r *Repository) CommitIdentity(ctx context.Context) (Identity, error) {
	author, err := r.identityOf(ctx, "author-ident", "GIT_AUTHOR_IDENT")
	if err != nil {
		return Identity{}, err
	}
	committer, err := r.identityOf(ctx, "committer-ident", "GIT_COMMITTER_IDENT")
	if err != nil {
		return Identity{}, err
	}
	return Identity{Author: author, Committer: committer}, nil
}

// identityOf reads one of git var's identity variables and returns it without
// the timestamp that follows the closing angle bracket.
func (r *Repository) identityOf(ctx context.Context, op, variable string) (string, error) {
	out, err := r.run(ctx, op, "var", variable)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	end := strings.LastIndexByte(line, '>')
	if end < 0 {
		return "", fmt.Errorf("%w: git var %s answered %q, which names no email address", ErrMalformedOutput, variable, line)
	}
	return line[:end+1], nil
}
