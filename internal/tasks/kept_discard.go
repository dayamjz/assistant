package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
// The digest is over the copy's state as shown: its path, the commit HEAD
// stands on, every reference tip, the commits lacking, and the three
// indicators. The same copy in the same state digests the same, and a
// commit, a reference moved, an edit, a file added or removed, or a stash
// made or dropped since changes it, which is what lets Discard refuse a
// copy that no longer holds exactly what was shown (ErrLossChanged). It is
// a confirmation and not a secret.
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
	if entry.TaskID.IsKnown() {
		// A task's clone has references of its own; a run's worktree reads
		// the gate's, which are not the copy's state.
		for _, ref := range inv.Refs {
			b.WriteString(ref.Name + "=" + ref.Commit + "\n")
		}
	}
	b.WriteString(strings.Join(loss.Lacks, ",") + "\n")
	fmt.Fprintf(&b, "%v %v %v\n", inv.Dirty, inv.Untracked, inv.Stash)
	sum := sha256.Sum256([]byte(b.String()))
	loss.Digest = hex.EncodeToString(sum[:])[:12]
	return loss, nil
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
