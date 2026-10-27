package vcs

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Errors Incorporated reports. Test with errors.Is.
var (
	// ErrInconclusive matches every failure Incorporated reports once its
	// arguments have passed: a commit that does not resolve, a git below the
	// floor the operation needs, a git that failed, and a conflict between
	// the commits and the tip alike. It is what keeps a failure to compute
	// apart from a clean false, and the cause stays matchable alongside it.
	ErrInconclusive = errors.New("vcs: whether the commits are incorporated could not be established")
	// ErrUnsupportedGit is returned when the git binary is below the floor
	// an operation states, or reports a version this package cannot read.
	// It is a refusal before the operation runs, never a degraded answer.
	ErrUnsupportedGit = errors.New("vcs: the git binary is below the floor this operation needs")
)

// incorporatedFloor is the git version Incorporated needs, as major and
// minor. It is above the package floor doc.go states, which is why
// Incorporated checks it rather than relying on the Requirements section.
var incorporatedFloor = [2]int{2, 38}

// Incorporated reports whether integrating commits into the commit named by
// into would change nothing: true when every one of them is already present
// in into, by content, and false when integrating at least one of them would
// change what into holds.
//
// It is defined by that result and by nothing about how it is reached. What
// the definition buys a caller deciding whether work has landed is two
// things patch identity cannot give. A textually identical edit applied
// somewhere else in a file is a different change, so it reads false rather
// than as "already landed", which is the insufficiency PRD section 9 names.
// And a branch of several commits squashed into one is recognized as one
// change already present, so it reads true. A conflict between a commit and
// the tip is not an answer either way and is reported as an error, never as
// false.
//
// A non-nil error is never a clean false. Every error after the arguments
// have passed matches ErrInconclusive, with the cause matchable alongside
// it: ErrRefNotFound for a revision that does not resolve locally,
// ErrUnsupportedGit for a git below 2.38, which this operation needs and
// checks for itself, and a *CommandError for a git that failed. An empty
// commit list is ErrInvalidArgument, because "integrating nothing changes
// nothing" is a true sentence a caller never means.
//
// into and every commit are resolved in the local repository first, as
// CommitsNotIn resolves its revisions, so a revision that only exists on a
// remote is an error rather than an answer. into may be a reference name or
// a commit identifier, and a caller that wants no moving reference inside
// the operation passes the identifier it already checked against RemoteRefs
// and fetched: the answer is then about that one commit, whatever the
// reference does meanwhile. The repository is read and nothing in it is
// written.
func (r *Repository) Incorporated(ctx context.Context, commits []string, into string) (bool, error) {
	if len(commits) == 0 {
		return false, &argumentError{what: "commits", value: "", reason: "must name at least one commit"}
	}
	for _, c := range commits {
		if err := checkArg("commit", c); err != nil {
			return false, err
		}
	}
	if err := checkArg("into", into); err != nil {
		return false, err
	}
	if err := r.requireGitFloor(ctx, incorporatedFloor); err != nil {
		return false, &inconclusiveError{into: into, cause: err}
	}
	tip, err := r.ResolveCommit(ctx, into)
	if err != nil {
		return false, &inconclusiveError{into: into, cause: err}
	}
	want, err := r.treeOf(ctx, tip)
	if err != nil {
		return false, &inconclusiveError{into: into, cause: err}
	}
	for _, c := range commits {
		id, err := r.ResolveCommit(ctx, c)
		if err != nil {
			return false, &inconclusiveError{commit: c, into: into, cause: err}
		}
		got, err := r.integratedTree(ctx, tip, id)
		if err != nil {
			return false, &inconclusiveError{commit: c, into: into, cause: err}
		}
		if got != want {
			return false, nil
		}
	}
	return true, nil
}

// integratedTree returns the tree that integrating commit into tip produces,
// and ErrMergeConflict when the two cannot be integrated cleanly.
func (r *Repository) integratedTree(ctx context.Context, tip, commit string) (string, error) {
	// git merge-tree --write-tree answers with its exit status: 0 a clean
	// result whose first line names the tree, 1 a conflict. Any other
	// status is a failure and has already been returned as one.
	out, code, err := r.runExpecting(ctx, "merge-tree", []int{1},
		"merge-tree", "--write-tree", "--no-messages", tip, commit)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", ErrMergeConflict
	}
	tree, _, _ := strings.Cut(string(out), "\n")
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return "", &outputError{op: "merge-tree", detail: "expected a tree identifier on the first line, got " + string(out)}
	}
	return tree, nil
}

// ErrMergeConflict is the cause carried by an ErrInconclusive result when a
// commit and the tip could not be integrated cleanly. Nothing is written when
// it is reported.
var ErrMergeConflict = errors.New("vcs: the commit and the tip conflict")

// treeOf returns the tree identifier of a commit already resolved here.
func (r *Repository) treeOf(ctx context.Context, commit string) (string, error) {
	out, err := r.run(ctx, "rev-parse-tree", "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", &outputError{op: "rev-parse-tree", detail: "expected a tree identifier for " + commit}
	}
	return tree, nil
}

// requireGitFloor refuses with ErrUnsupportedGit when the git binary this
// handle runs reports a version below floor, or one this package cannot
// read. An unreadable version is refused rather than assumed adequate,
// because the operation asking is one whose answer a caller acts on.
func (r *Repository) requireGitFloor(ctx context.Context, floor [2]int) error {
	out, err := r.run(ctx, "version", "--version")
	if err != nil {
		return err
	}
	major, minor, ok := parseGitVersion(string(out))
	if !ok {
		return &floorError{reported: strings.TrimSpace(string(out)), floor: floor}
	}
	if major < floor[0] || (major == floor[0] && minor < floor[1]) {
		return &floorError{reported: strings.TrimSpace(string(out)), floor: floor}
	}
	return nil
}

// gitAtLeast reports whether the git binary this handle runs reports a
// version at floor or later. A version this package cannot read is an error,
// never assumed one side or the other of the floor.
func (r *Repository) gitAtLeast(ctx context.Context, floor [2]int) (bool, error) {
	out, err := r.run(ctx, "version", "--version")
	if err != nil {
		return false, err
	}
	major, minor, ok := parseGitVersion(string(out))
	if !ok {
		return false, &floorError{reported: strings.TrimSpace(string(out)), floor: floor}
	}
	return major > floor[0] || (major == floor[0] && minor >= floor[1]), nil
}

// parseGitVersion reads the major and minor numbers out of a "git version
// X.Y.Z" line. Anything after the minor number is ignored, so a suffix a
// distribution appends does not make a version unreadable. ok is false when
// the line does not carry two numbers in that shape.
func parseGitVersion(line string) (major, minor int, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return 0, 0, false
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(leadingDigits(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(leadingDigits(parts[1]))
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// leadingDigits returns the run of decimal digits s begins with.
func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// floorError reports a git below an operation's floor, naming what git
// reported and what was needed.
type floorError struct {
	reported string
	floor    [2]int
}

func (e *floorError) Error() string {
	return "vcs: git reported " + strconv.Quote(e.reported) + ", and this operation needs " +
		strconv.Itoa(e.floor[0]) + "." + strconv.Itoa(e.floor[1]) + " or newer"
}

// Unwrap makes every floor refusal match ErrUnsupportedGit.
func (e *floorError) Unwrap() error { return ErrUnsupportedGit }

// inconclusiveError reports an Incorporated call that could not answer. It
// names the commit the failure was about, when there was one, and the tip,
// and unwraps to ErrInconclusive and to the cause together.
type inconclusiveError struct {
	commit string
	into   string
	cause  error
}

func (e *inconclusiveError) Error() string {
	var b strings.Builder
	b.WriteString("vcs: whether ")
	if e.commit != "" {
		b.WriteString(e.commit + " is")
	} else {
		b.WriteString("the commits are")
	}
	b.WriteString(" incorporated into " + e.into + " could not be established: ")
	b.WriteString(e.cause.Error())
	return b.String()
}

// Unwrap makes the result match ErrInconclusive and its cause alike.
func (e *inconclusiveError) Unwrap() []error { return []error{ErrInconclusive, e.cause} }
