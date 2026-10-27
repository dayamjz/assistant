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
// (land, carry on, discard); its registry is the record that names it, the
// task's copy path for a clone and the gate's worktree registry for a
// run's worktree. Every path that answers a kept copy asks VerifyKeptCopy
// before it reads or writes anything of it, and acts on nothing it refuses
// (round 28: the same gap had been closed path by path, and the next
// review found it open in another path): the loss a discard shows and a
// land reads, carry on, and a promotion, whose flip answers its copy's
// entries carry on. internal/tasks' Keep, which records a copy as
// the cleanup decision keeps it, is not one of them: it reads the copy as
// git reads it there, so a copy whose .git was made to point at another
// repository is recorded with what git reads through it, and every answer
// to that entry is then refused here.
//
// What it verifies, each through git or the file system as it stands:
//
//   - the copy's working tree is a directory, not a symbolic link;
//   - the recorded repository directory is a directory, not a link, its
//     parent path resolved;
//   - the recorded shared repository directory, the one the copy's
//     objects and references are read from and written to, is a
//     directory, not a link, its parent path resolved: the clone's own
//     .git again for a task's clone, and the gate's repository for a
//     run's worktree;
//   - git reads the copy (vcs.Observe: rev-parse --show-toplevel
//     --absolute-git-dir --git-common-dir) with the working tree the copy
//     itself, the repository directory the one recorded and the shared
//     repository directory the one recorded, each compared once links are
//     resolved, so a .git that is a link or a file pointing at another
//     repository or another worktree's record, a .git git does not take
//     for a repository, another repository's core.worktree naming the
//     copy, and a worktree record whose commondir file names another
//     repository, so that every object and reference would be read and
//     written there, are each refused;
//   - no symbolic link stands anywhere in the recorded repository
//     directory, so no reference, reflog, object or index is read or
//     written in another repository through one. For a run's worktree
//     that directory is its record; the gate's shared directory, which
//     holds the references and objects, is bound above and not walked;
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
	// CommonDir is the repository directory the copy shares with every
	// other worktree of its repository, where its objects and references
	// are read and written: GitDir again for a task's clone, and the
	// gate's repository for a run's worktree, which the worktree's record
	// names in its commondir file. A record that names none is refused.
	CommonDir string
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
	// The shared repository directory is resolved the same way: for a run's
	// worktree it is the gate's repository, and a record whose commondir
	// names another would have git read and write every object and
	// reference there.
	if want.CommonDir == "" {
		return refuse("is recorded with no shared repository directory")
	}
	commonParent, err := filepath.EvalSymlinks(filepath.Dir(want.CommonDir))
	if err != nil {
		return refuse("has no shared repository directory at " + want.CommonDir + ": " + err.Error())
	}
	commonDir := filepath.Join(commonParent, filepath.Base(want.CommonDir))
	if info, err := os.Lstat(commonDir); err != nil || !info.IsDir() {
		return refuse("has no shared repository directory at " + want.CommonDir)
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
	if read, err := filepath.EvalSymlinks(obs.CommonDir); err != nil || read != commonDir {
		return refuse("is read by git as sharing the repository directory " + obs.CommonDir + ", not the recorded " + want.CommonDir)
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
