package vcs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Errors a caller is expected to handle. Each is a typed result rather than a
// warning execution continues past. Test with errors.Is.
var (
	// ErrNotARepository is returned when a path opened as a repository is not
	// one of the kind it was opened as: OpenBare on something that is not a
	// bare repository, or OpenWorktree on something that is not a working
	// copy.
	ErrNotARepository = errors.New("vcs: path is not a repository of the expected kind")
	// ErrRefNotFound is returned when a revision does not resolve to a commit
	// in this repository.
	ErrRefNotFound = errors.New("vcs: revision does not resolve to a commit")
	// ErrPathNotFound is returned when a path does not exist at the requested
	// commit. A path that exists but is not a regular file resolves here and
	// then fails in cat-file, which reports a *CommandError instead.
	ErrPathNotFound = errors.New("vcs: path does not exist at that commit")
	// ErrRemoteNotFound is returned when a repository has no remote of the
	// requested name.
	ErrRemoteNotFound = errors.New("vcs: no such remote")
	// ErrDetachedHead is returned by HeadBranch when HEAD is not on a branch.
	ErrDetachedHead = errors.New("vcs: HEAD is not on a branch")
	// ErrNoMergeBase is returned when two commits have no common ancestor.
	ErrNoMergeBase = errors.New("vcs: commits have no common ancestor")
	// ErrInvalidArgument is returned when an argument would be read by git as
	// an option, is empty where a value is required, or contains a byte that
	// cannot survive the wire format the operation parses. The refusal happens
	// before git is invoked.
	ErrInvalidArgument = errors.New("vcs: argument is not usable as a git argument")
	// ErrOutputTooLarge is returned when a git invocation produced more
	// standard output than the configured limit allows. The output is
	// discarded rather than truncated, because a truncated diff read as a
	// whole one is worse than no diff.
	ErrOutputTooLarge = errors.New("vcs: git produced more output than the configured limit")
	// ErrUnknownStatus is returned when git reported a change status this
	// package does not recognize. It refuses rather than guessing, because a
	// silently dropped change is a change nobody reviewed.
	ErrUnknownStatus = errors.New("vcs: unrecognized change status from git")
	// ErrMalformedOutput is returned when git's output does not have the shape
	// the requested format promises.
	ErrMalformedOutput = errors.New("vcs: git output did not have the expected shape")
)

// CommandError reports a git invocation that failed. It names which operation
// failed and what git said about it. Args and Stderr have passed through the
// repository's Redactor, so neither carries the userinfo of a URL.
type CommandError struct {
	// Op is the vcs operation that ran the command, such as "fetch".
	Op string
	// Repo is the repository path the command was addressed at.
	Repo string
	// Args is the full argument vector passed to git, redacted.
	Args []string
	// ExitCode is git's exit status, or -1 when git could not be started or
	// was killed before reporting one.
	ExitCode int
	// Stderr is git's standard error, redacted, and bounded as it was read
	// rather than afterwards. When git wrote more than the bound allows, the
	// text kept ends with "[git message truncated]". It is empty when git
	// wrote nothing there.
	Stderr string
	// Err is the underlying error from starting or waiting on the process. It
	// is nil when git ran and exited non-zero, which is the ordinary case.
	Err error
}

// Error renders the operation, the repository, the exit status, and git's
// message.
func (e *CommandError) Error() string {
	var b strings.Builder
	b.WriteString("vcs: " + e.Op + " failed in " + e.Repo)
	if e.ExitCode >= 0 {
		b.WriteString(": git exited " + strconv.Itoa(e.ExitCode))
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	if e.Stderr != "" {
		b.WriteString(": " + e.Stderr)
	}
	if e.Stderr == "" && e.Err == nil && e.ExitCode < 0 {
		b.WriteString(": git reported no exit status and no message")
	}
	return b.String()
}

// Unwrap returns the underlying process error, if there was one.
func (e *CommandError) Unwrap() error { return e.Err }

// argumentError reports an argument refused before git was invoked. It wraps
// ErrInvalidArgument and names which argument and why.
type argumentError struct {
	what   string
	value  string
	reason string
}

func (e *argumentError) Error() string {
	return fmt.Sprintf("vcs: %s %q: %s", e.what, e.value, e.reason)
}

// Unwrap makes every argument refusal match ErrInvalidArgument.
func (e *argumentError) Unwrap() error { return ErrInvalidArgument }
