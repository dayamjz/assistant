package vcs

import (
	"context"
	"strconv"
	"strings"
)

// ChangeStatus is what happened to one path between two commits.
type ChangeStatus string

const (
	// StatusAdded means the path exists only in the later commit.
	StatusAdded ChangeStatus = "added"
	// StatusModified means the path exists in both and its content differs.
	StatusModified ChangeStatus = "modified"
	// StatusDeleted means the path exists only in the earlier commit.
	StatusDeleted ChangeStatus = "deleted"
	// StatusRenamed means the path moved, and OldPath holds where it was.
	StatusRenamed ChangeStatus = "renamed"
	// StatusCopied means the path is a copy of OldPath, which still exists.
	StatusCopied ChangeStatus = "copied"
	// StatusTypeChanged means the path changed between a regular file, a
	// symbolic link, and a submodule.
	StatusTypeChanged ChangeStatus = "type-changed"
	// StatusUnmerged means the path has an unresolved conflict recorded in the
	// index. It cannot appear in a comparison of two commits.
	StatusUnmerged ChangeStatus = "unmerged"
)

// FileChange is one path changed between two commits.
type FileChange struct {
	// Status is what happened to the path.
	Status ChangeStatus
	// Path is the path in the later commit, except for a deletion, where the
	// later commit has no path and this is the path in the earlier one.
	Path string
	// OldPath is the path in the earlier commit for a rename or a copy, and is
	// empty for every other status.
	OldPath string
	// Similarity is git's similarity score, 0 to 100, for a rename or a copy.
	// It is 0 for every other status.
	Similarity int
}

// diffArgs are the options every diff-shaped invocation carries.
//
// The two --no- options are a trust boundary, not a preference. A .gitattributes
// file on the branch being examined can name an external diff command or a
// textconv filter, and git would then run that program while diffing. PRD
// principle P7 says the branch under validation does not choose what runs, so
// neither is allowed here.
var diffArgs = []string{
	"--no-color",
	"--no-ext-diff",
	"--no-textconv",
	"--find-renames",
	"--find-copies",
}

// Diff returns the unified diff from one commit to another, as git writes it.
// Both revisions are resolved to commits first, so an unknown one fails with
// ErrRefNotFound.
//
// The whole diff is read into memory and is refused with ErrOutputTooLarge
// past the limit set by WithMaxOutput, because the branch being examined
// chooses how large its own diff is.
func (r *Repository) Diff(ctx context.Context, from, to string) (string, error) {
	a, b, err := r.resolvePair(ctx, from, to)
	if err != nil {
		return "", err
	}
	args := append([]string{"diff"}, diffArgs...)
	args = append(args, a, b, "--")
	out, err := r.run(ctx, "diff", args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ChangedFiles lists the paths that differ between two commits, with what
// happened to each. It reports renames and copies as such, with the earlier
// path in OldPath, rather than as a deletion and an addition.
//
// It returns an error wrapping ErrUnknownStatus if git reports a status this
// package does not recognize, rather than dropping the change: a change nobody
// hears about is a change nobody reviews.
func (r *Repository) ChangedFiles(ctx context.Context, from, to string) ([]FileChange, error) {
	a, b, err := r.resolvePair(ctx, from, to)
	if err != nil {
		return nil, err
	}
	args := append([]string{"diff"}, diffArgs...)
	args = append(args, "--name-status", "-z", a, b, "--")
	out, err := r.run(ctx, "changed-files", args...)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(string(out))
}

// parseNameStatus reads the NUL-separated output of diff --name-status -z. A
// record is a status field followed by one path, or by two paths when the
// status carries a similarity score, which is what a rename and a copy do.
func parseNameStatus(out string) ([]FileChange, error) {
	fields := strings.Split(out, "\x00")
	// The output ends with a trailing separator, so the final field is empty.
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	var changes []FileChange
	for i := 0; i < len(fields); {
		code := fields[i]
		if code == "" {
			return nil, &outputError{op: "changed-files", detail: "empty status field"}
		}
		status, score, err := parseStatusCode(code)
		if err != nil {
			return nil, err
		}
		twoPaths := status == StatusRenamed || status == StatusCopied
		want := 1
		if twoPaths {
			want = 2
		}
		if i+want >= len(fields) {
			return nil, &outputError{
				op:     "changed-files",
				detail: "status " + code + " is missing " + strconv.Itoa(want) + " path field(s)",
			}
		}
		change := FileChange{Status: status, Similarity: score}
		if twoPaths {
			change.OldPath = fields[i+1]
			change.Path = fields[i+2]
		} else {
			change.Path = fields[i+1]
		}
		changes = append(changes, change)
		i += want + 1
	}
	return changes, nil
}

// parseStatusCode reads one status field, which is a letter optionally
// followed by a similarity score.
func parseStatusCode(code string) (ChangeStatus, int, error) {
	letter, digits := code[:1], code[1:]
	var status ChangeStatus
	switch letter {
	case "A":
		status = StatusAdded
	case "M":
		status = StatusModified
	case "D":
		status = StatusDeleted
	case "R":
		status = StatusRenamed
	case "C":
		status = StatusCopied
	case "T":
		status = StatusTypeChanged
	case "U":
		status = StatusUnmerged
	default:
		return "", 0, &statusError{code: code}
	}
	if digits == "" {
		return status, 0, nil
	}
	score, err := strconv.Atoi(digits)
	if err != nil || score < 0 || score > 100 {
		return "", 0, &outputError{op: "changed-files", detail: "status " + code + " has no usable similarity score"}
	}
	return status, score, nil
}

// FileAt returns the contents of a repository path as of a commit. path is
// repository-relative and uses forward slashes, whatever the host's own
// separator is.
//
// It returns ErrPathNotFound when the commit has no such path. A path that
// exists but is not a file whose bytes can be read, such as a directory,
// resolves and then fails in git, which is reported as a *CommandError. For a
// symbolic link the bytes are the link target rather than the target's
// contents, because that is what the repository stores.
func (r *Repository) FileAt(ctx context.Context, rev, path string) ([]byte, error) {
	commit, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	if err := checkArg("path", path); err != nil {
		return nil, err
	}
	spec := commit + ":" + path
	out, code, err := r.runExpecting(ctx, "file-at", []int{1},
		"rev-parse", "--verify", "--quiet", "--end-of-options", spec)
	if err != nil {
		return nil, err
	}
	object := strings.TrimSpace(string(out))
	if code != 0 || object == "" {
		return nil, &pathError{path: path, rev: commit}
	}
	return r.run(ctx, "file-at", "cat-file", "blob", object)
}

// resolvePair resolves both ends of a comparison before either reaches a diff
// command line, so neither can be read as an option. Its callers follow the
// pair with --, which is what keeps git from reading either as a path: an
// object identifier is a plausible file name, and git refuses an argument that
// is both rather than choosing.
func (r *Repository) resolvePair(ctx context.Context, from, to string) (string, string, error) {
	a, err := r.ResolveCommit(ctx, from)
	if err != nil {
		return "", "", err
	}
	b, err := r.ResolveCommit(ctx, to)
	if err != nil {
		return "", "", err
	}
	return a, b, nil
}

// pathError reports a path that does not exist at a commit.
type pathError struct {
	path string
	rev  string
}

func (e *pathError) Error() string {
	return "vcs: path " + e.path + " does not exist at " + e.rev
}

// Unwrap makes every missing path match ErrPathNotFound.
func (e *pathError) Unwrap() error { return ErrPathNotFound }

// statusError reports a change status git emitted that this package does not
// recognize.
type statusError struct {
	code string
}

func (e *statusError) Error() string {
	return "vcs: git reported change status " + e.code + ", which this package does not recognize"
}

// Unwrap makes every unrecognized status match ErrUnknownStatus.
func (e *statusError) Unwrap() error { return ErrUnknownStatus }
