package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Discarding kept work: PRD section 6's "you name the copy, the program
// shows exactly what will be lost, and it removes the copy only on that
// answer". This file is the two halves of that the copy itself decides:
// what would be lost, read from the copy and the entry now, and the removal,
// made only when the copy still holds exactly what was shown. Who may answer
// discard, and the record of who did, are internal/service's and
// internal/store's; nothing here records anything.
//
// # What would be lost
//
// Loss is what the copy holds that is nowhere safe as far as this program
// can see: the commits the default branch lacks, which the entry's basis
// states (store.KeptWork.Lacks) and which this does not recompute, and the
// three indicators vcs.Inventory reads of the copy now, a tracked change, a
// file no commit records, and a stash. An entry whose basis is not
// established says what the copy stands on and nothing about what of it is
// at risk, so it is refused with ErrLossUnestablished rather than shown a
// list that may overstate or understate; a later cleanup of the unchanged
// copy establishes it. A copy that is gone is ErrCopyGone.
//
// The digest is over everything the discard would destroy, as shown: the
// copy's path, the commit HEAD stands on, every reference tip of a task's
// clone and the stash of either kind, every entry of the index at every
// stage (git's own object identity for the staged content of every tracked
// path, and of each side of a conflicted one), every path git reports
// changed or untracked with the content it holds in the working tree (a
// hash of its bytes, or that it is gone; for an untracked directory, which
// is how git reports a nested repository, every file under it by the
// filesystem, its own .git included, since git will not look inside), the
// commits lacking, and the three indicators. The same copy in the same state
// digests the same, and a commit, a reference moved, an edit to a tracked
// file, a change staged, a file added, edited or removed, or a stash made
// or dropped since changes it, which is what lets Discard refuse a copy that
// no longer holds exactly what was shown (ErrLossChanged). Names and counts
// alone would not: an edit that keeps a file's name is a change to what
// would be lost, so the content is read. It is a confirmation and not a
// secret, and a file git is configured to ignore is not read, as it is not
// an indicator and not among the paths git reports.
//
// # The removal
//
// Discard removes a task's clone with os.RemoveAll and a run's linked
// worktree through vcs.RemoveWorktreeDiscardingChanges under the gate's
// copies lock, the discarding removal cleanup.go names as reachable and
// unused: this is its one use, and what stands behind it is the digest
// match against the person's answer rather than a P12 proof, which a
// discard by its nature has none of. A copy already gone is not refused:
// the answer and the digest it carried say the removal was wanted, and
// nothing stands to lose.

// Loss is what discarding a kept copy would lose, as read now.
type Loss struct {
	// Head is the commit the copy's HEAD stands on.
	Head string
	// Lacks is the commits the default branch lacks, from the entry's basis.
	Lacks []string
	// Dirty, Untracked and Stash are the inventory's indicators of the copy
	// as it stands now: a tracked change, a file no commit records, and a
	// stash, none of which a commit the default branch could lack records.
	Dirty, Untracked, Stash bool
	// Digest is the confirmation of exactly this: the copy's state as shown.
	Digest string
}

// ErrLossUnestablished is returned by Loss and Discard for an entry whose
// basis is not established: what would be lost cannot be shown exactly.
var ErrLossUnestablished = errors.New("tasks: what the copy holds that the default branch lacks is not established, so what a discard would lose cannot be shown")

// ErrCopyGone is returned by Loss for an entry whose copy is not standing.
var ErrCopyGone = errors.New("tasks: the kept copy is gone")

// ErrLossChanged is returned by Discard when the copy no longer holds
// exactly what was shown: the digest the answer carried is not the copy's.
var ErrLossChanged = errors.New("tasks: the copy no longer holds exactly what was shown, so it was not removed")

// Loss reads what discarding the entry's copy would lose, on this file's
// terms.
func (c *Cleaner) Loss(ctx context.Context, entry store.KeptWork) (Loss, error) {
	if !entry.Established() {
		return Loss{}, fmt.Errorf("%w: %s", ErrLossUnestablished, entry.Unestablished.Or("the entry was recorded before the store kept its basis"))
	}
	standing, err := c.standing(entry.CopyPath)
	if err != nil {
		return Loss{}, err
	}
	if !standing {
		return Loss{}, fmt.Errorf("%w: nothing stands at %s", ErrCopyGone, entry.CopyPath)
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: append([]string{}, entry.Lacks...), Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: inv.Stash}
	var b strings.Builder
	b.WriteString(entry.CopyPath + "\n" + inv.Head.Commit + "\n")
	for _, ref := range inv.Refs {
		// A task's clone has references of its own; a run's worktree reads
		// the gate's, which are not the copy's state, so of those only the
		// stash, which is the copy's, is read.
		if entry.TaskID.IsKnown() || ref.Name == "refs/stash" {
			b.WriteString(ref.Name + "=" + ref.Commit + "\n")
		}
	}
	entries, err := copied.IndexEntries(ctx)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "index %s %s %d %s\n", e.Mode, e.Object, e.Stage, e.Path)
	}
	changes, err := copied.Changes(ctx)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, p := range changes.Paths {
		content, err := contentOf(filepath.Join(entry.CopyPath, filepath.FromSlash(p.Path)))
		if err != nil {
			return Loss{}, fmt.Errorf("tasks: reading %s in the kept copy at %s: %w", p.Path, entry.CopyPath, err)
		}
		fmt.Fprintf(&b, "%s untracked=%v gone=%v %s\n", p.Path, p.Untracked, p.Gone, content)
	}
	b.WriteString(strings.Join(loss.Lacks, ",") + "\n")
	fmt.Fprintf(&b, "%v %v %v\n", inv.Dirty, inv.Untracked, inv.Stash)
	sum := sha256.Sum256([]byte(b.String()))
	loss.Digest = hex.EncodeToString(sum[:])[:12]
	return loss, nil
}

// contentOf is a hash of what the working tree holds at path: "gone" for a
// path no longer there, "link" plus its target for a symbolic link, which
// is what git would record of it, and for a directory a hash over every
// entry under it, walked by the filesystem, each file by its bytes and each
// link by its target, so an untracked directory git reports as one path, a
// nested repository among them, is covered down to its contents.
func contentOf(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "gone", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return "link " + target, nil
	}
	if !info.IsDir() {
		return hashFile(path)
	}
	h := sha256.New()
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			_, _ = fmt.Fprintf(h, "dir %s\n", filepath.ToSlash(rel))
			return nil
		}
		content, err := contentOf(p)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s %s\n", filepath.ToSlash(rel), content)
		return nil
	})
	if err != nil {
		return "", err
	}
	return "tree " + hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile is a hash of the file's bytes.
func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Discard removes the entry's copy when it still holds exactly what digest
// was shown for, on this file's terms, and returns what was lost. gate is
// the gate repository a run's copy is a worktree of, and is not read for a
// task's. A copy already gone returns the zero Loss and no error.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, digest, gatePath string) (Loss, error) {
	loss, err := c.Loss(ctx, entry)
	if errors.Is(err, ErrCopyGone) {
		return Loss{}, nil
	}
	if err != nil {
		return Loss{}, err
	}
	if loss.Digest != digest {
		return Loss{}, fmt.Errorf("%w: it was shown as %s and is %s now", ErrLossChanged, digest, loss.Digest)
	}
	if entry.TaskID.IsKnown() {
		if err := os.RemoveAll(entry.CopyPath); err != nil {
			return Loss{}, fmt.Errorf("tasks: discarding the copy at %s: %w", entry.CopyPath, err)
		}
		return loss, nil
	}
	bare, err := vcs.OpenBare(ctx, gatePath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: opening the gate repository at %s: %w", gatePath, err)
	}
	lock, err := gate.LockCopies(ctx, bare.Path())
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: waiting to discard the copy at %s: %w", entry.CopyPath, err)
	}
	defer func() { _ = lock.Release() }()
	if err := bare.RemoveWorktreeDiscardingChanges(ctx, entry.CopyPath); err != nil {
		return Loss{}, fmt.Errorf("tasks: discarding the copy at %s: %w", entry.CopyPath, err)
	}
	return loss, nil
}
