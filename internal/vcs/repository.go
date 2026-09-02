package vcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Kind says how a repository is addressed on the command line, which is the
// only thing about it this package needs to know.
type Kind int

const (
	// KindBare is a bare repository, addressed with --git-dir so that it works
	// under safe.bareRepository=explicit.
	KindBare Kind = iota + 1
	// KindWorktree is a working copy, addressed with -C naming its root. A
	// linked worktree created by AddWorktree is this kind.
	KindWorktree
)

// String names the kind.
func (k Kind) String() string {
	switch k {
	case KindBare:
		return "bare"
	case KindWorktree:
		return "worktree"
	default:
		return "unknown"
	}
}

// Repository is a handle on one git repository. It holds no state beyond how
// to address the repository and how to run git against it, so it is safe for
// concurrent use: every method is one or more independent git invocations.
//
// Concurrent use is safe in the sense that this package adds no shared mutable
// state. It does not serialize git's own locks, so two concurrent operations
// that both write the index or a ref can still meet git's "unable to create
// .git/index.lock" and fail. Read operations do not take those locks.
type Repository struct {
	path string
	kind Kind
	set  settings
}

// Path returns the absolute path this handle addresses: the bare repository
// directory, or the root of the working copy.
func (r *Repository) Path() string { return r.path }

// Kind returns how this repository is addressed.
func (r *Repository) Kind() Kind { return r.kind }

// addressing returns the git-level arguments that name this repository. A bare
// repository is named with --git-dir rather than discovered from the working
// directory, which is what makes it work under safe.bareRepository=explicit.
func (r *Repository) addressing() []string {
	if r.kind == KindBare {
		return []string{"--git-dir=" + r.path}
	}
	return []string{"-C", r.path}
}

// InitBare creates a bare repository at path and returns a handle on it. The
// parent directory of path must already exist.
//
// Running it again on a repository it created succeeds, so an initialization
// repeated to repair a home does not fail on the repositories that were
// already fine.
//
// It refuses with ErrNotARepository when path already holds something that is
// not a bare repository, an empty directory apart. Git would otherwise write
// a bare repository's files into a directory that is already a working copy,
// leaving one directory that is two repositories. That refusal says so in its
// message, because a message about the path alone reads as a typo. When path
// holds a bare repository git itself refuses to use, the refusal is returned
// as the *CommandError git described it with rather than as
// ErrNotARepository. Nothing is written in either case.
//
// Which template the repository is created from is worth stating exactly,
// because the template supplies the hooks it is born with. An inherited
// GIT_TEMPLATE_DIR is removed from the environment along with the rest of
// redirectingVars, so that direct channel is closed. It is not the only one:
// git also reads init.templateDir from configuration, and the location of the
// configuration file is itself something GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM can move, and those two are deliberately kept. An
// ancestor process that can set them can therefore still choose this
// repository's hooks. That is the same trust category as PATH and the git
// binary, which such a process also controls, so this refuses to pretend
// otherwise.
func InitBare(ctx context.Context, path string, opts ...Option) (*Repository, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return nil, err
	}
	if occupied, err := isOccupiedByOtherThanABareRepo(ctx, abs, opts); err != nil {
		return nil, err
	} else if occupied {
		return nil, &occupiedError{path: abs}
	}
	// git init addresses its target as an argument, so the handle used to run
	// it does not point at a repository yet. Address the parent directory and
	// let git create the target.
	seed := &Repository{path: filepath.Dir(abs), kind: KindWorktree, set: newSettings(opts)}
	if _, err := seed.run(ctx, "init-bare", "init", "--bare", "--", abs); err != nil {
		return nil, err
	}
	return OpenBare(ctx, abs, opts...)
}

// OpenBare returns a handle on the bare repository at path. It verifies that
// path is a bare repository and returns ErrNotARepository when it is not, so a
// mistyped path fails here rather than as a confusing failure several
// operations later.
//
// A path that holds a bare repository git refuses to use is a different
// answer, and is returned as the *CommandError carrying git's own message. See
// classifyOpenFailure for exactly what is distinguished and what is not.
func OpenBare(ctx context.Context, path string, opts ...Option) (*Repository, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return nil, err
	}
	if !isDirectory(abs) {
		return nil, &openError{path: abs, kind: KindBare}
	}
	r := &Repository{path: abs, kind: KindBare, set: newSettings(opts)}
	out, err := r.run(ctx, "open-bare", "rev-parse", "--is-bare-repository")
	if err != nil {
		return nil, classifyOpenFailure(err, abs, KindBare)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, &openError{path: abs, kind: KindBare}
	}
	return r, nil
}

// OpenWorktree returns a handle on the working copy rooted at path. It
// verifies that path is inside a working copy and returns ErrNotARepository
// when it is not.
//
// The handle addresses path itself, not the working copy root git discovers
// from it, so opening a subdirectory addresses that subdirectory. Operations
// here take revisions and repository-relative paths rather than working
// directory paths, so that distinction does not change their results.
//
// As with OpenBare, a working copy git refuses to use is returned as the
// *CommandError carrying git's own message rather than as ErrNotARepository.
func OpenWorktree(ctx context.Context, path string, opts ...Option) (*Repository, error) {
	abs, err := absolutePath(path)
	if err != nil {
		return nil, err
	}
	if !isDirectory(abs) {
		return nil, &openError{path: abs, kind: KindWorktree}
	}
	r := &Repository{path: abs, kind: KindWorktree, set: newSettings(opts)}
	out, err := r.run(ctx, "open-worktree", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return nil, classifyOpenFailure(err, abs, KindWorktree)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, &openError{path: abs, kind: KindWorktree}
	}
	return r, nil
}

// isOccupiedByOtherThanABareRepo reports whether path already holds something
// that InitBare must not write a bare repository over. A path that does not
// exist, an empty directory, and an existing bare repository are all fine.
func isOccupiedByOtherThanABareRepo(ctx context.Context, path string, opts []Option) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	if _, err := OpenBare(ctx, path, opts...); err != nil {
		if errors.Is(err, ErrNotARepository) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// classifyOpenFailure decides whether a failed open probe means path holds no
// repository of that kind, or means git found one and refused to use it.
//
// Git answers both with exit status 128 and separates them only in its English
// message, so this makes its own distinction from something a caller can check
// independently: whether the marker git discovers a repository from is on
// disk. For a bare repository that marker is the HEAD, objects, and refs
// entries in the directory itself; for a working copy it is a .git entry in
// the directory or in one of its ancestors.
//
// What this distinguishes, and nothing more: a 128 against a path carrying no
// marker becomes ErrNotARepository. A 128 against a path that does carry one
// is returned unchanged, so dubious ownership, an object store that cannot be
// read, and a HEAD git will not parse all reach the caller as the
// *CommandError git described them with, rather than as a mistyped path.
//
// It never claims a path is not a repository on evidence it does not have: a
// lookup that fails for any reason other than the entry not existing counts as
// a marker that may be present, and the *CommandError is returned.
func classifyOpenFailure(err error, path string, kind Kind) error {
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Err != nil || ce.ExitCode != 128 {
		return err
	}
	if marked, known := hasRepositoryMarker(path, kind); known && !marked {
		return &openError{path: path, kind: kind}
	}
	return err
}

// hasRepositoryMarker reports whether path carries the on-disk marker git
// discovers a repository of the given kind from. known is false when a lookup
// failed for a reason other than the entry not existing, and then marked says
// nothing.
func hasRepositoryMarker(path string, kind Kind) (marked, known bool) {
	if kind == KindBare {
		for _, entry := range []string{"HEAD", "objects", "refs"} {
			if _, err := os.Lstat(filepath.Join(path, entry)); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return false, true
				}
				return false, false
			}
		}
		return true, true
	}
	// Git discovers a working copy by walking up from the directory it was
	// pointed at, so the marker may be at any ancestor.
	for dir := path; ; {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			return true, true
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, true
		}
		dir = parent
	}
}

// isDirectory reports whether path is a directory. Every invocation runs with
// its working directory set to the repository, so a path that is not a
// directory cannot be a repository of either kind and is refused here, where
// the answer is ErrNotARepository, rather than as a failure to start git.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// openError reports a path that is not a repository of the kind it was opened
// as.
type openError struct {
	path string
	kind Kind
}

func (e *openError) Error() string {
	return "vcs: " + e.path + " is not a " + e.kind.String() + " repository"
}

// Unwrap makes every open refusal match ErrNotARepository.
func (e *openError) Unwrap() error { return ErrNotARepository }

// occupiedError reports a path InitBare refused because something that is not
// a bare repository is already there. It names the reason rather than the
// path, so that a refusal is not read as a mistyped argument.
type occupiedError struct {
	path string
}

func (e *occupiedError) Error() string {
	return "vcs: refusing to initialize a bare repository at " + e.path +
		": something that is not a bare repository is already there, and writing over it would leave one directory that is two repositories"
}

// Unwrap makes the occupied refusal match ErrNotARepository, which is what it
// has always reported, so a caller testing for that sentinel is unaffected.
func (e *occupiedError) Unwrap() error { return ErrNotARepository }

// WorktreeSpec describes a linked worktree to create.
type WorktreeSpec struct {
	// Path is where the worktree is created. It must not already exist.
	Path string
	// Commit is the revision checked out into it. It must resolve to a commit
	// in the repository the worktree is created from.
	Commit string
	// Branch, when set, is a new branch created at Commit and checked out. It
	// must not already exist. When it is empty the worktree is checked out at
	// a detached HEAD, which is what a disposable copy of one run wants.
	Branch string
}

// AddWorktree creates a linked worktree as described by spec and returns a
// handle on it. The receiver may be bare or a working copy.
//
// It creates the worktree and nothing else: it does not decide where
// disposable copies live, and it does not remove one afterwards. Both belong
// to the caller.
func (r *Repository) AddWorktree(ctx context.Context, spec WorktreeSpec) (*Repository, error) {
	abs, err := absolutePath(spec.Path)
	if err != nil {
		return nil, err
	}
	commit, err := r.ResolveCommit(ctx, spec.Commit)
	if err != nil {
		return nil, err
	}
	args := []string{"worktree", "add"}
	if spec.Branch != "" {
		if err := checkArg("branch", spec.Branch); err != nil {
			return nil, err
		}
		args = append(args, "-b", spec.Branch)
	} else {
		args = append(args, "--detach")
	}
	args = append(args, "--", abs, commit)
	if _, err := r.run(ctx, "worktree-add", args...); err != nil {
		return nil, err
	}
	return OpenWorktree(ctx, abs, r.optionsForChild()...)
}

// RemoveWorktree removes the linked worktree at path along with its
// administrative record. Git refuses when the worktree holds modified or
// untracked files, and that refusal is returned as a *CommandError; use
// RemoveWorktreeDiscardingChanges only when the caller has established that
// losing those files is acceptable.
func (r *Repository) RemoveWorktree(ctx context.Context, path string) error {
	return r.removeWorktree(ctx, path, false)
}

// RemoveWorktreeDiscardingChanges removes the linked worktree at path even
// when it holds modified or untracked files, which are lost.
//
// Nothing in this package establishes that the work in a worktree is
// recoverable. PRD principle P12 requires positive proof before an isolated
// copy is removed, and that proof belongs to the caller: this method is the
// mechanism it uses once it has one, not a shortcut past needing one.
func (r *Repository) RemoveWorktreeDiscardingChanges(ctx context.Context, path string) error {
	return r.removeWorktree(ctx, path, true)
}

func (r *Repository) removeWorktree(ctx context.Context, path string, discard bool) error {
	abs, err := absolutePath(path)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove"}
	if discard {
		args = append(args, "--force")
	}
	args = append(args, "--", abs)
	_, err = r.run(ctx, "worktree-remove", args...)
	return err
}

// PruneWorktrees removes administrative records for worktrees whose
// directories are gone. It does not delete any directory.
func (r *Repository) PruneWorktrees(ctx context.Context) error {
	_, err := r.run(ctx, "worktree-prune", "worktree", "prune")
	return err
}

// WorktreeInfo describes one worktree attached to a repository.
type WorktreeInfo struct {
	// Path is the worktree's directory. For the entry describing a bare
	// repository it is the repository directory and Bare is true.
	Path string
	// Commit is the commit HEAD points at, empty when the worktree has none
	// yet.
	Commit string
	// Branch is the short branch name HEAD is on, empty when HEAD is detached
	// or the entry is the bare repository.
	Branch string
	// Detached reports that HEAD is not on a branch.
	Detached bool
	// Bare reports that this entry is the bare repository itself rather than a
	// checked-out worktree.
	Bare bool
	// Locked reports that the worktree is locked against pruning.
	Locked bool
	// Prunable reports that git considers the record removable.
	Prunable bool
}

// ListWorktrees returns every worktree attached to this repository, including
// the entry for the repository itself.
func (r *Repository) ListWorktrees(ctx context.Context) ([]WorktreeInfo, error) {
	out, err := r.run(ctx, "worktree-list", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var (
		list    []WorktreeInfo
		current *WorktreeInfo
	)
	for _, field := range strings.Split(string(out), "\x00") {
		if field == "" {
			// The empty field between records ends the current one.
			if current != nil {
				list = append(list, *current)
				current = nil
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			if current != nil {
				list = append(list, *current)
			}
			current = &WorktreeInfo{Path: value}
		case "HEAD":
			if current != nil {
				current.Commit = value
			}
		case "branch":
			if current != nil {
				current.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "bare":
			if current != nil {
				current.Bare = true
			}
		case "detached":
			if current != nil {
				current.Detached = true
			}
		case "locked":
			if current != nil {
				current.Locked = true
			}
		case "prunable":
			if current != nil {
				current.Prunable = true
			}
		}
	}
	if current != nil {
		list = append(list, *current)
	}
	return list, nil
}

// optionsForChild reproduces this handle's configuration for a handle derived
// from it, so a worktree created from a repository runs git the same way.
func (r *Repository) optionsForChild() []Option {
	return []Option{
		WithGitBinary(r.set.git),
		WithRedactor(r.set.redactor),
		WithMaxOutput(r.set.maxOutput),
	}
}

// absolutePath rejects an empty path and one carrying a NUL byte, then makes
// it absolute, so that neither the process working directory nor the -C this
// package passes can change what it means, and so that a path beginning with a
// dash reaches git with a leading separator rather than as an option. It does
// not refuse an option-shaped path; it makes one harmless.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", &argumentError{what: "path", value: path, reason: "must not be empty"}
	}
	if strings.ContainsRune(path, 0) {
		return "", &argumentError{what: "path", value: path, reason: "must not contain a NUL byte"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.Join(&argumentError{what: "path", value: path, reason: "cannot be made absolute"}, err)
	}
	return abs, nil
}
