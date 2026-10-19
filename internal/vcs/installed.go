package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// Installed reports the version line of the git this package runs under
// opts, which is "git" resolved through this process's PATH unless
// WithGitBinary names another, by running git --version outside any
// repository. It is what a caller asks before it can ask anything of a
// repository, so that a git that cannot be started is reported as that and
// not as a directory that is not a working copy.
//
// It establishes that the binary starts and answers in the shape git's own
// version line takes, and nothing about whether that version is new enough
// for an operation that states a floor of its own. A line that is not in
// that shape is an error rather than a version, so a binary that starts and
// is not git is not reported as installed.
func Installed(ctx context.Context, opts ...Option) (string, error) {
	r := &Repository{path: os.TempDir(), kind: KindWorktree, set: newSettings(opts)}
	out, err := r.run(ctx, "version", "--version")
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if _, _, ok := parseGitVersion(line); !ok {
		return "", &versionError{reported: line}
	}
	return line, nil
}

// GitNotFound reports whether err is an invocation this package made of a
// git binary that could not be found to start: the name it runs was not on
// PATH, or the path WithGitBinary named does not exist. A git that started
// and failed is not this, whatever it said, and neither is an error this
// package did not produce by invoking git.
func GitNotFound(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Err == nil {
		return false
	}
	return errors.Is(ce.Err, exec.ErrNotFound) || errors.Is(ce.Err, os.ErrNotExist)
}

// versionError is Installed's refusal of a binary whose answer is not a git
// version line.
type versionError struct {
	reported string
}

func (e *versionError) Error() string {
	return "vcs: the git binary answered " + quoteForError(e.reported) + ", which is not a git version line"
}

// quoteForError bounds and quotes what a binary answered for a message.
func quoteForError(s string) string {
	const limit = 200
	if len(s) > limit {
		s = s[:limit] + "..."
	}
	return "\"" + strings.ReplaceAll(s, "\"", "'") + "\""
}
