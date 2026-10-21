package tasks

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P15's "kept work is never silent" and section 6's kept work:
// a copy kept because its work has not landed is recorded as a kept work
// entry, store.KeptWork, the moment it is kept. This file is the one writer
// of that record, beside the cleanup record it already writes, because the
// decision that keeps a copy is this package's: Keep turns a kept copy into
// its entry, and KeptOf says which cleanup refusals are a copy kept for its
// work.
//
// What the entry says the copy holds is read from the copy by Keep, not
// taken from the caller: the commits its references and HEAD reach for a
// task's clone, and the commit HEAD stands on for a run's worktree, the same
// sets the cleanup decision examines. So an entry about a copy that is not
// standing is refused rather than written from what a caller remembered.

// KeptPromotionWindow is the reason an investigation's copy is kept once its
// worker is ended at the end of promotion_window, PRD section 6's "when the
// window passes, the worker is ended and its copy is kept". It answers to
// #438, which ends that worker and raises the entry through internal/service;
// nothing in this build produces it yet.
const KeptPromotionWindow = "promotion-window-passed"

// Kept names a copy kept because its work has not landed: its one owner, a
// task or a run, and why it was kept. It carries nothing about what the copy
// holds, which Keep reads, or about the project and branch, which the
// owner's row holds.
type Kept struct {
	// Task names the task whose copy was kept, for a task's copy; Run names
	// the run, for a run's copy. Exactly one is set.
	Task string
	Run  string
	// Reason is the word the entry records for why: a refusal's reason, as
	// KeptOf gives it, or KeptPromotionWindow.
	Reason string
	// Detail is the account the entry carries as evidence, redacted by the
	// store before it is written.
	Detail string
}

// Unlanded reports whether the refusal is a copy kept because its work has
// not landed: none of P12's proofs held, or the copy holds a change, a file
// or a stash no commit records. Every other reason keeps the copy for a fact
// that was not established about the copy rather than for work in it: a
// worker that may still be writing, a copy that changed under the proof, a
// report or hold not yet registered, a shape that moved, or a copy that is
// not there. Those are asked again by the next cleanup and are not kept work.
func (r *CleanupRefusal) Unlanded() bool {
	switch r.Reason {
	case ReasonUnproven, ReasonCopyDirty, ReasonCopyUntracked, ReasonCopyStash:
		return true
	}
	return false
}

// KeptOf is the kept copy an unlanded refusal describes, with the refusal's
// reason and its account, each proof's attempt included. It answers false
// for a refusal Unlanded does not report.
func KeptOf(r *CleanupRefusal) (Kept, bool) {
	if r == nil || !r.Unlanded() {
		return Kept{}, false
	}
	detail := r.Detail
	for _, a := range r.Attempts {
		detail += "; " + string(a.Proof) + ": " + a.Detail
	}
	return Kept{Task: r.Task, Run: r.Run, Reason: string(r.Reason), Detail: detail}, true
}

// KeptEntry is what Keep did: the entry as it stands, and whether this call
// wrote it.
type KeptEntry struct {
	// Entry is the kept work entry.
	Entry store.KeptWork
	// Recorded is whether this call wrote it; false is an entry already
	// recorded for the copy and the commits it holds.
	Recorded bool
}

// Keep records the kept work entry for a kept copy and reports whether this
// call wrote it: a copy holding the commits an entry already records is that
// entry, on store.RecordKeptWork's terms, so asking again of an unchanged
// copy writes nothing. It reads what the copy holds from the copy, and
// refuses with ErrCleanupRecord a request naming no owner or both, a task
// whose reserved copy is not where this home puts it, and a copy that is not
// standing.
func (c *Cleaner) Keep(ctx context.Context, k Kept) (KeptEntry, error) {
	if (k.Task == "") == (k.Run == "") {
		return KeptEntry{}, fmt.Errorf("%w: a kept copy names exactly one owner, a task or a run", ErrCleanupRecord)
	}
	var path string
	if k.Task != "" {
		task, err := c.store.Task(ctx, k.Task)
		if err != nil {
			return KeptEntry{}, err
		}
		if path, err = c.home.TaskCopy(task.ID); err != nil {
			return KeptEntry{}, err
		}
		if filepath.Clean(task.WorktreePath) != path {
			return KeptEntry{}, fmt.Errorf("%w: task %s reserved its copy at %s and this home puts it at %s",
				ErrCleanupRecord, task.ID, task.WorktreePath, path)
		}
	} else {
		run, err := c.store.Run(ctx, k.Run)
		if err != nil {
			return KeptEntry{}, err
		}
		path = c.home.Worktree(run.RepositoryID, run.ID)
	}
	standing, err := c.standing(path)
	if err != nil {
		return KeptEntry{}, err
	}
	if !standing {
		return KeptEntry{}, fmt.Errorf("%w: nothing stands at %s, so no copy was kept there", ErrCleanupRecord, path)
	}
	copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return KeptEntry{}, fmt.Errorf("tasks: opening the kept copy at %s: %w", path, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return KeptEntry{}, fmt.Errorf("tasks: reading the kept copy at %s: %w", path, err)
	}
	holds := inventoryCommits(inv)
	if k.Run != "" {
		holds = []string{inv.Head.Commit}
	}
	entry, wrote, err := c.store.RecordKeptWork(ctx, store.KeptCopy{
		TaskID: k.Task, RunID: k.Run, CopyPath: path, Holds: holds, Reason: k.Reason, Detail: k.Detail,
	})
	if err != nil {
		return KeptEntry{}, err
	}
	return KeptEntry{Entry: entry, Recorded: wrote}, nil
}
