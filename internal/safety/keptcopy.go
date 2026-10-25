package safety

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/vcs"
)

// The third question: whether a kept copy is the one its registry
// recorded, asked before anything of it is read or written. A kept copy
// is a task's clone or a run's linked worktree that a person answers
// (land, carry on, discard) and the program sets aside and puts back; its
// registry is the record that names it, the task's copy path for a clone
// and the gate's worktree registry for a run's worktree. Every path that
// reads or writes a kept copy asks VerifyKeptCopy first, and acts on
// nothing it refuses (round 28: the same gap had been closed path by path,
// and the next review found it open in another path).
//
// What it verifies, each through git or the file system as it stands:
//
//   - the copy's working tree is a directory, not a symbolic link;
//   - the recorded repository directory is a directory, not a link, its
//     parent path resolved;
//   - git reads the copy (vcs.Observe: rev-parse --show-toplevel
//     --absolute-git-dir) with the working tree the copy itself and the
//     repository directory the one recorded, each compared once links are
//     resolved, so a .git that is a link or a file pointing at another
//     repository or another worktree's record, a .git git does not take
//     for a repository, and another repository's core.worktree naming the
//     copy are each refused;
//   - no symbolic link stands anywhere in that repository directory, so
//     no reference, reflog, object or index is read or written in another
//     repository through one;
//   - no symbolic link in the working tree resolves to a directory, so no
//     path inside the copy passes through one into another directory.
//
// What it does not cover: it is a check at one moment, and a link put in
// place after it, between the check and a write, is followed by that
// write; so is a link that resolves to nothing at the check and to a
// directory made after it. A symbolic link to a file, or to nothing, is
// content and is allowed.

// ErrCopyNotVerified is what every refusal of VerifyKeptCopy matches: the
// kept copy is not verified to be the one its registry recorded, so
// nothing of it is to be read or written.
var ErrCopyNotVerified = errors.New("safety: the kept copy is not verified to be the one its registry recorded")

// KeptCopy is what a registry recorded of a kept copy: its working tree,
// and the repository directory git must read for it, a task's clone's own
// .git or the record the gate's registry keeps for a run's worktree.
type KeptCopy struct {
	Worktree string
	GitDir   string
}

// CopyRefusal is VerifyKeptCopy's refusal, naming the copy and why. It
// matches ErrCopyNotVerified.
type CopyRefusal struct {
	Worktree string
	Reason   string
}

// Error is the copy and why it was refused.
func (r *CopyRefusal) Error() string {
	return "safety: the kept copy at " + r.Worktree + " " + r.Reason + ", so nothing of it was read or written"
}

// Unwrap makes every refusal match ErrCopyNotVerified.
func (r *CopyRefusal) Unwrap() error { return ErrCopyNotVerified }

// VerifyKeptCopy answers the third question on this file's terms: nil when
// the copy is the one want records, and a *CopyRefusal naming why
// otherwise. opts are the options the copy is read with.
func VerifyKeptCopy(ctx context.Context, want KeptCopy, opts ...vcs.Option) error {
	refuse := func(why string) error { return &CopyRefusal{Worktree: want.Worktree, Reason: why} }
	info, err := os.Lstat(want.Worktree)
	if err != nil {
		return refuse("could not be read: " + err.Error())
	}
	if !info.IsDir() {
		return refuse("is not a directory")
	}
	root, err := filepath.EvalSymlinks(want.Worktree)
	if err != nil {
		return refuse("could not be read: " + err.Error())
	}
	// The recorded repository directory is resolved through its parent
	// only, and must itself be a directory: a .git that is a link reaches
	// another repository's, which is then not the one recorded.
	parent, err := filepath.EvalSymlinks(filepath.Dir(want.GitDir))
	if err != nil {
		return refuse("has no repository directory at " + want.GitDir + ": " + err.Error())
	}
	gitDir := filepath.Join(parent, filepath.Base(want.GitDir))
	if info, err := os.Lstat(gitDir); err != nil || !info.IsDir() {
		return refuse("has no repository directory of its own at " + want.GitDir)
	}
	obs, err := vcs.Observe(ctx, want.Worktree, opts...)
	if err != nil {
		return refuse("is not a repository git reads: " + err.Error())
	}
	if top, err := filepath.EvalSymlinks(obs.Toplevel); err != nil || top != root {
		return refuse("is read by git as part of the working tree at " + obs.Toplevel)
	}
	if read, err := filepath.EvalSymlinks(obs.GitDir); err != nil || read != gitDir {
		return refuse("is read by git through the repository directory " + obs.GitDir + ", not the recorded " + want.GitDir)
	}
	if link, err := firstLink(gitDir, func(string) bool { return true }); err != nil {
		return refuse("has a repository directory that could not be read: " + err.Error())
	} else if link != "" {
		return refuse("has a symbolic link in its repository directory, at " + link)
	}
	passable := func(p string) bool {
		target, err := os.Stat(p)
		return err == nil && target.IsDir()
	}
	if link, err := firstLink(root, passable); err != nil {
		return refuse("has a working tree that could not be read: " + err.Error())
	} else if link != "" {
		return refuse("has a symbolic link a path inside it could pass through, at " + link)
	}
	return nil
}

// firstLink is the first symbolic link under root, root itself aside, for
// which counts reports true, walked without following any; "" when there
// is none.
func firstLink(root string, counts func(string) bool) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && d.Type()&fs.ModeSymlink != 0 && counts(p) {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}
