package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// The digest is over the copy's state as a discard would destroy it
// (copyState), each piece a framed record (digest), so no name, target or
// content can stand for a boundary, and the commits shown as lost (seal).
// It is decided from what the copy holds, never from what git reports
// changed, so neither git's ignore rules nor its configuration can keep a
// file out of it:
//
//   - the copy's path and the commit HEAD stands on;
//   - every entry under the copy, walked by the filesystem, a file git
//     ignores and a nested repository's own .git among them: its path, its
//     type, its full mode, and a hash of a file's bytes or a link's target;
//   - the repository directory the removal deletes with it, a task's
//     clone's .git or the directory the gate keeps for a run's worktree,
//     walked the same way (its configuration, hooks, and any operation in
//     progress), except the entries git rewrites without changing what they
//     hold, which Loss's own fetch writes to (gitDirSealedByContent), sealed
//     instead by what they hold: every reference and the object it names,
//     every entry of every reflog, the stash list whole among them, and
//     every entry of the index at every stage by git's own object identity;
//     for a run's worktree the references are its own and the stash
//     (runWorktreeRefs) and the reflogs HEAD's and theirs, since everything
//     else it reads is the gate's, shared with every other run;
//   - the three indicators.
//
// The default branch's tip is not in it: it is not the copy's, and a
// discard answered and stopped before its removal would otherwise be
// refused for good by an unrelated commit landing upstream. The same copy
// in the same state digests the same, and anything the removal would
// destroy that changes since moves it, which is what lets Discard refuse a
// copy that no longer holds exactly what was shown (ErrLossChanged). It is
// a confirmation and not a secret.
//
// What it does not cover, and what that costs. An object nothing names, no
// reference, reflog entry or index entry, is not sealed: one written since
// the discard was shown leaves the digest as it was, and a stash dropped
// before it and not yet pruned, which only git fsck finds, is destroyed
// with the copy and named by neither the digest nor the list. What is shown is narrower than what
// is sealed: the commits lacking and the three indicators, so a commit only
// a reflog names, a file git ignores and the repository directory's own
// files are destroyed by a discard without being listed, though a change
// to any of them since it was shown refuses the confirmation. A run's
// worktree's stash is the gate's, which the removal leaves standing; it is
// sealed all the same, so a stash made in another worktree of the gate in
// between refuses the confirmation, which is shown again. And git's own
// maintenance, which a fetch may start, can rewrite what is sealed by what
// it holds, expiring an old reflog entry among other things, which refuses
// the confirmation the same way: each of these fails toward keeping.
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
	// Digest is the confirmation of exactly this: the copy's state and the
	// commits lacking, as shown (seal).
	Digest string
	// state is copyState's digest of the copy's state, which the digest
	// seals with the commits shown as lost.
	state string
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
	// The default branch is fetched under a namespace of this read's own,
	// never the cleanup decision's: a cleanup of the same copy may run at
	// once (recovery asks it of every finished run's standing copy), and
	// its forget would delete the reference this read is about to resolve.
	if l.namespace, err = discardNamespace(entry); err != nil {
		return Loss{}, err
	}
	held := lacking(ctx, l, reaches)
	if !held.established() {
		return Loss{}, fmt.Errorf("%w: %s", ErrLossUnestablished, held.unestablished)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: held.lacks, Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: inv.Stash}
	if loss.state, err = copyState(ctx, entry, copied, inv); err != nil {
		return Loss{}, err
	}
	loss.Digest = seal(loss.state, loss.Lacks)
	return loss, nil
}

// gitDirSealedByContent is the entries of a repository directory whose
// bytes copyState does not read, because git rewrites them without changing
// what they hold, and Loss's own read writes some of them: the object
// database, which the read's fetch adds to and git's maintenance repacks;
// the references and their logs, in whichever form the repository keeps
// them, which that fetch writes a reference under for the read's length
// and maintenance may pack; and the index, whose recorded file times a
// person's own git status refreshes. Each is read by what it holds
// instead: every reference and its object, every entry of every reflog,
// and every entry of the index. What the object database holds that none
// of those name is the residual gap the header states.
var gitDirSealedByContent = []string{"objects", "refs", "packed-refs", "logs", "reftable", "index"}

// runWorktreeRefs is the references a run's worktree holds of its own,
// which the discard's removal of the worktree takes with it, and the stash
// a person makes there, which the gate keeps for every worktree of it:
// everything else a run's worktree reads is the gate's, shared with every
// other run.
var runWorktreeRefs = []string{"refs/worktree/", "refs/bisect/", "refs/rewritten/", "refs/stash"}

// copyState is the digest of the copy's state as a discard would destroy
// it, on this file's terms: every record, framed, in one stream. It is
// decided from what the copy holds, never from what git reports changed:
// the working tree is walked by the filesystem whatever git ignores or
// reports, and git is asked only for what its own files hold in a form
// they are rewritten in (gitDirSealedByContent).
func copyState(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, inv vcs.Inventory) (string, error) {
	d := newDigest()
	d.record("copy", entry.CopyPath, inv.Head.Commit)
	var refs []vcs.Ref
	var logs []vcs.ReflogEntry
	var err error
	if entry.TaskID.IsKnown() {
		// A task's clone is removed whole: every reference in it, the
		// program's own included, and every reflog it keeps.
		if refs, err = copied.ListRefs(ctx); err != nil {
			return "", fmt.Errorf("tasks: reading the references of the kept copy at %s: %w", entry.CopyPath, err)
		}
		if logs, err = copied.AllReflogs(ctx); err != nil {
			return "", fmt.Errorf("tasks: reading the reflogs of the kept copy at %s: %w", entry.CopyPath, err)
		}
	} else {
		if refs, err = copied.ListRefs(ctx, runWorktreeRefs...); err != nil {
			return "", fmt.Errorf("tasks: reading the references of the kept copy at %s: %w", entry.CopyPath, err)
		}
		names := []string{"HEAD"}
		for _, ref := range refs {
			names = append(names, ref.Name)
		}
		if logs, err = copied.Reflogs(ctx, names...); err != nil {
			return "", fmt.Errorf("tasks: reading the reflogs of the kept copy at %s: %w", entry.CopyPath, err)
		}
	}
	for _, ref := range refs {
		d.record("ref", ref.Name, ref.Object, ref.Commit)
	}
	for _, l := range logs {
		d.record("reflog", l.Ref, strconv.Itoa(l.Index), l.Object)
	}
	entries, err := copied.IndexEntries(ctx)
	if err != nil {
		return "", fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, e := range entries {
		d.record("index", e.Mode, e.Object, strconv.Itoa(e.Stage), e.Path)
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return "", fmt.Errorf("tasks: finding the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	gitDir, err := os.Stat(obs.GitDir)
	if err != nil {
		return "", fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	// The working tree, everything under the copy but the repository
	// directory standing in it, which is walked on its own terms below;
	// a nested repository's .git, a submodule's among them, is walked
	// here as files like any other.
	err = recordTree(d, "tree", entry.CopyPath, func(rel string, info os.FileInfo) bool {
		return rel == ".git" && info.IsDir() && os.SameFile(info, gitDir)
	})
	if err != nil {
		return "", fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	// The repository directory: a task's clone's .git, or the directory
	// the gate keeps for a run's worktree, which its removal deletes.
	err = recordTree(d, "gitdir", obs.GitDir, func(rel string, _ os.FileInfo) bool {
		return slices.Contains(gitDirSealedByContent, rel)
	})
	if err != nil {
		return "", fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	d.record("indicators", strconv.FormatBool(inv.Dirty), strconv.FormatBool(inv.Untracked), strconv.FormatBool(inv.Stash))
	return d.sum(), nil
}

// discardNamespace is a namespace no other read uses, under which one Loss
// fetches what it compares against: the entry's owner and a random suffix,
// so two reads of one copy at once never share a reference either.
func discardNamespace(entry store.KeptWork) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("tasks: naming the references a discard reads under: %w", err)
	}
	owner := "run/" + entry.RunID.Or("")
	if id, ok := entry.TaskID.Get(); ok {
		owner = "task/" + id
	}
	return "refs/assistant/discard/" + owner + "/" + hex.EncodeToString(suffix[:]), nil
}

// seal is the digest a discard is shown and confirmed with: the copy's
// state, copyState's digest of it, and the commits shown as lost. The
// default branch's tip is not sealed. It is not the copy's, so a commit
// landing on it that the copy does not hold changes nothing a discard
// destroys, and sealing it would refuse a recorded discard for good
// whenever the upstream moved before the removal. What it can change is
// what of the copy is lost, which Discard holds to what was shown instead.
func seal(state string, lacks []string) string {
	d := newDigest()
	d.record("state", state)
	d.record(append([]string{"lacks"}, lacks...)...)
	return d.sum()[:12]
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

// recordTree writes one record for every entry under root, root itself
// included, walked by the filesystem in lexical order and never through a
// link: its path relative to root, its type, its full mode (the type, the
// nine permission bits git keeps the executable ones of, and the rest a
// discard destroys too), and what it holds: a link's target, a regular
// file's bytes by their hash, nothing more for a directory, and nothing
// for anything else, a pipe or a socket, which has no bytes to read. An
// entry skip answers true for, by its path relative to root, is not
// recorded, and nothing under it is walked. Every record is framed on
// digest's terms, so no name, target or content can stand for a boundary.
func recordTree(d *digest, label, root string, skip func(rel string, info os.FileInfo) bool) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if p != root && skip(rel, info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mode := strconv.FormatUint(uint64(info.Mode()), 8)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			d.record(label, rel, "link", mode, target)
		case info.IsDir():
			d.record(label, rel, "dir", mode)
		case info.Mode().IsRegular():
			sum, err := hashFile(p)
			if err != nil {
				return err
			}
			d.record(label, rel, "file", mode, sum)
		default:
			d.record(label, rel, "other", mode)
		}
		return nil
	})
}

// hashFile is a hash of the file's bytes, read as a stream.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Discard removes the entry's copy when it still holds exactly what digest
// was shown for, on this file's terms, and returns what was lost. shown is
// the commits the discard was shown as lost, which digest seals with the
// copy's state: the copy read now must seal with them to digest, so it
// holds exactly what it held when shown, and what the default branch read
// now lacks of it must be among them, so a commit that would be lost now
// and was not shown is refused with ErrLossChanged. gate is the gate repository a run's
// copy is a worktree of, and is not read for a task's. A copy already gone
// returns the zero Loss and no error.
//
// What the commit check does not cover: a default branch that moved only
// forward over commits the copy does not hold changes nothing here, and one
// that took up commits the copy holds shrinks the loss, both of which
// proceed; one that was rewound or force-pushed past a commit it had can
// make the copy the only place that commit is, which is refused.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, digest string, shown []string, gatePath string) (Loss, error) {
	loss, err := c.Loss(ctx, entry)
	if errors.Is(err, ErrCopyGone) {
		return Loss{}, nil
	}
	if err != nil {
		return Loss{}, err
	}
	if sealed := seal(loss.state, shown); sealed != digest {
		return Loss{}, fmt.Errorf("%w: it was shown as %s and holds what digests as %s now", ErrLossChanged, digest, sealed)
	}
	var unshown []string
	for _, commit := range loss.Lacks {
		if !slices.Contains(shown, commit) {
			unshown = append(unshown, commit)
		}
	}
	if len(unshown) > 0 {
		return Loss{}, fmt.Errorf("%w: the default branch now lacks %s, which the discard was not shown as lost", ErrLossChanged, strings.Join(unshown, ", "))
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
