package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strconv"

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
// can see, read from the copy as it stands when it is shown: the commits
// its references and HEAD reach that the default branch, read fresh as Keep
// reads it (lacking), lacks, and the three indicators vcs.Inventory reads,
// a tracked change, a file no commit records, and a stash. The entry's
// recorded basis is not what is shown, because the copy may hold more
// since it was kept and a discard destroys the copy as it is. A default
// branch that cannot be read now refuses the preview with
// ErrLossUnestablished rather than showing a list that may overstate or
// understate. A copy that is gone is ErrCopyGone.
//
// The digest is over everything the discard would destroy, as shown, each
// piece a framed record (digest), so no name, target or content can stand
// for a boundary: the
// copy's path, the commit HEAD stands on, every reference tip of a task's
// clone and the stash of either kind, every entry of the index at every
// stage (git's own object identity for the staged content of every tracked
// path, and of each side of a conflicted one, with the mode git keeps),
// every path git reports changed or untracked with what it holds in the
// working tree (its type and permission bits and a hash of its bytes or
// its link's target, or that it is gone; for an untracked directory, which
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
	// Lacks is the commits the default branch lacks, read from the copy
	// as it stands, the way Keep reads it (HEAD and every reference of a
	// task's clone, HEAD of a run's worktree), never the entry's record of
	// it when it was kept.
	Lacks []string
	// Dirty, Untracked and Stash are the inventory's indicators of the copy
	// as it stands now: a tracked change, a file no commit records, and a
	// stash, none of which a commit the default branch could lack records.
	Dirty, Untracked, Stash bool
	// Digest is the confirmation of exactly this: the copy's state as shown.
	Digest string
}

// ErrLossUnestablished is returned by Loss and Discard when what the
// default branch lacks cannot be read now: what would be lost cannot be
// shown exactly.
var ErrLossUnestablished = errors.New("tasks: what the copy holds that the default branch lacks is not established, so what a discard would lose cannot be shown")

// ErrCopyGone is returned by Loss for an entry whose copy is not standing.
var ErrCopyGone = errors.New("tasks: the kept copy is gone")

// ErrLossChanged is returned by Discard when the copy no longer holds
// exactly what was shown: the digest the answer carried is not the copy's.
var ErrLossChanged = errors.New("tasks: the copy no longer holds exactly what was shown, so it was not removed")

// Loss reads what discarding the entry's copy would lose, on this file's
// terms.
func (c *Cleaner) Loss(ctx context.Context, entry store.KeptWork) (Loss, error) {
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
	// What the default branch lacks is read from the copy now, the way Keep
	// reads it, so what is shown is what the copy holds when it is shown
	// and the digest seals exactly that, never the entry's record of the
	// copy when it was kept.
	var l landing
	reaches := committedTips(inv)
	if taskID, ok := entry.TaskID.Get(); ok {
		task, err := c.store.Task(ctx, taskID)
		if err != nil {
			return Loss{}, err
		}
		if l, err = c.taskLanding(ctx, task, copied, nil); err != nil {
			return Loss{}, err
		}
	} else {
		run, err := c.store.Run(ctx, entry.RunID.Or(""))
		if err != nil {
			return Loss{}, err
		}
		reaches = []string{inv.Head.Commit}
		if l, err = c.runLanding(ctx, run, copied, nil); err != nil {
			return Loss{}, err
		}
	}
	held := lacking(ctx, l, reaches)
	if !held.established() {
		return Loss{}, fmt.Errorf("%w: %s", ErrLossUnestablished, held.unestablished)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: held.lacks, Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: inv.Stash}
	d := newDigest()
	d.record("copy", entry.CopyPath, inv.Head.Commit)
	d.record("against", held.against)
	for _, ref := range inv.Refs {
		// A task's clone has references of its own; a run's worktree reads
		// the gate's, which are not the copy's state, so of those only the
		// stash, which is the copy's, is read.
		if entry.TaskID.IsKnown() || ref.Name == "refs/stash" {
			d.record("ref", ref.Name, ref.Commit)
		}
	}
	entries, err := copied.IndexEntries(ctx)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, e := range entries {
		d.record("index", e.Mode, e.Object, strconv.Itoa(e.Stage), e.Path)
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
		d.record("path", p.Path, strconv.FormatBool(p.Untracked), strconv.FormatBool(p.Gone), content)
	}
	d.record(append([]string{"lacks"}, loss.Lacks...)...)
	d.record("indicators", strconv.FormatBool(inv.Dirty), strconv.FormatBool(inv.Untracked), strconv.FormatBool(inv.Stash))
	loss.Digest = d.sum()[:12]
	return loss, nil
}

// digest is the framing every digest here is written in: each record is a
// count of its fields and then each field as its length and its bytes, so
// no byte string in a name, a link's target or a content's hash can be read
// as a boundary between fields or records, and two different sequences of
// records never write the same bytes.
type digest struct{ h hash.Hash }

func newDigest() *digest { return &digest{h: sha256.New()} }

// record writes one record of fields.
func (d *digest) record(fields ...string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(fields)))
	_, _ = d.h.Write(n[:])
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		_, _ = d.h.Write(n[:])
		_, _ = d.h.Write([]byte(f))
	}
}

// sum is the digest of every record written, in hexadecimal.
func (d *digest) sum() string { return hex.EncodeToString(d.h.Sum(nil)) }

// contentOf is what the working tree holds at path, as a string that is
// itself a digest's field: "gone" for a path no longer there, and otherwise
// the entry's type and permission bits (os.FileMode's type bits and its nine
// permission bits, the executable bits among them, which git records of a
// file and a checkout restores, and the rest of which a discard destroys
// too) followed by, for a symbolic link, its target, which is what git
// would record of it, for a file a hash of its bytes, and for a directory a
// digest over every entry under it, walked by the filesystem, each one a
// framed record of its path relative to the directory and its own
// contentOf, so an untracked directory git reports as one path, a nested
// repository among them with its own .git, is covered down to every file's
// bytes, every link's target and every entry's mode.
func contentOf(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "gone", nil
	}
	if err != nil {
		return "", err
	}
	d := newDigest()
	mode := strconv.FormatUint(uint64(info.Mode().Type()|info.Mode().Perm()), 8)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		d.record("link", mode, target)
	case !info.IsDir():
		sum, err := hashFile(path)
		if err != nil {
			return "", err
		}
		d.record("file", mode, sum)
	default:
		d.record("dir", mode)
		err = filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == path {
				return nil
			}
			rel, err := filepath.Rel(path, p)
			if err != nil {
				return err
			}
			if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
				d.record("entry", filepath.ToSlash(rel), "dir", strconv.FormatUint(uint64(fi.Mode().Type()|fi.Mode().Perm()), 8))
				return nil
			}
			content, err := contentOf(p)
			if err != nil {
				return err
			}
			d.record("entry", filepath.ToSlash(rel), content)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return d.sum(), nil
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
