package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P12: removing an isolated copy requires positive proof that
// its work is reachable from a remote branch, or its pull request merged
// with a head containing the work, or its content is already present in an
// up-to-date default branch; anything inconclusive refuses, and a refusal
// means stop and investigate, never a flag. This file is the decision, over
// a run's copy and a task's copy alike, and the two removals the decision
// gates. proof.go is the three proofs, and report.go is the stored report
// the investigation half of the decision verifies against.
//
// # One decision, two mechanisms
//
// A run's copy is a linked worktree of the gate's repository, cut by
// internal/service at worktrees/<repository>/<run>; its work is the commit
// its HEAD stands on, and it is removed through vcs.RemoveWorktree, the
// non-discarding one, so a modified or untracked file git finds there is
// git's own refusal behind this package's. A task's copy is a full clone at
// tasks/<id>/copy; its work is every commit reachable from every local
// reference and from HEAD, read through vcs.Inventory, and removing it is
// removing a directory, which is not git and so is this package's, behind
// the proof value. The two are never one removal: a clone has no worktree
// record to remove and a worktree holds no references of its own.
//
// # The proof is a value nothing but the decision produces
//
// Removal is the proof, on tasks.Isolation's and safety.Observation's terms:
// its field is unexported, only this file's decision sets it, and both
// removal functions refuse one Proven does not report. A caller holding a
// Cleaner can ask for a cleanup and cannot hand one a proof, and the two
// request types carry no field that stands for one. What that does not
// close is named rather than implied: vcs.RemoveWorktreeDiscardingChanges
// is exported and reachable by any importer, and os.RemoveAll by every one,
// so only this package calling either behind a Removal is convention, and
// nothing in the type graph stops a caller elsewhere from removing a
// directory it never proved. A terminal liveness is an input too, and a
// caller that states Dead of a terminal it never probed has lied about an
// observation, which no type here can tell from the truth.
//
// # The record is the claim
//
// The cleanup record, store.TaskCleanup for a task and store.RunCleanup for
// a run, is written with the proof before the copy is removed and marked
// removed after, so a crash between the two leaves a record that says what
// happened: the work was proven landed and the copy may still be standing.
// A second call completes it, on an absent copy at once and on a standing
// one after re-reading it, so the removal is idempotent. For a task copy the
// record's write is also where the shape is claimed: store.RecordTaskCleanup
// reads the task's shape in the transaction that writes the row and refuses
// a shape other than the one the decision was made over, which is what
// keeps a decision over an investigation copy from being claimed against a
// task promoted to delivery meanwhile (#109). The window that leaves is
// stated below.
//
// # The pre-removal recheck is the inventory unchanged
//
// A proof is about commits the decision read, and a worker can commit
// between the proof and the removal. The inventory, references, HEAD, the
// tree's state and the stash, and never the remote-tracking references the
// proof's own fetch writes, is read before the proof and again before the
// removal, and a difference refuses. That bounds the window to the time
// between the last read and the removal, which is why a task copy is
// cleaned up only when its terminal probed Dead: a worker still running has
// that window as its whole life, and no re-read closes it.

// ErrCleanupRefused matches every *CleanupRefusal: the copy was not removed,
// because what would have made the removal safe could not be established.
// It is a result a caller handles and reports, never a warning execution
// continues past, and there is no Cleanup inside it.
var ErrCleanupRefused = errors.New("tasks: the isolated copy was kept, because its work is not yet safely elsewhere")

// ErrCleanupRecord reports a record a cleanup cannot be decided from as
// written: a task with no copy path, one whose reserved copy is not where
// this home puts it, or a run of a repository with no record. Nothing is
// examined and nothing is written; the record is the thing to fix.
var ErrCleanupRecord = errors.New("tasks: the record cannot be cleaned up as written")

// CleanupReason is why a cleanup refused. Every reason is a fact that could
// not be established or a state that was found, never a preference, and the
// set is closed.
type CleanupReason string

const (
	// ReasonTerminalUnobserved is the task's terminal was not observed, so
	// whether a worker is still writing to the copy is unknown: the probe
	// could not say, which internal/session's reasons list, or the caller
	// observed nothing at all, and a caller that passes
	// taskstate.Unobserved is saying exactly that.
	ReasonTerminalUnobserved CleanupReason = "terminal-unobserved"
	// ReasonTerminalLive is the task's terminal is live, so a worker may
	// still commit to the copy after any proof is read.
	ReasonTerminalLive CleanupReason = "terminal-live"
	// ReasonCopyDirty is the copy holds a tracked file that differs from
	// HEAD, which is work no commit records.
	ReasonCopyDirty CleanupReason = "copy-dirty"
	// ReasonCopyUntracked is the copy holds a file no commit records and
	// git is not configured to ignore.
	ReasonCopyUntracked CleanupReason = "copy-untracked"
	// ReasonCopyStash is the copy holds a stash, which is work on a
	// reference no proof reads.
	ReasonCopyStash CleanupReason = "copy-stash"
	// ReasonCopyChanged is the copy's inventory differed between two reads,
	// or from what a recorded proof examined, so the proof is of a copy
	// that no longer stands.
	ReasonCopyChanged CleanupReason = "copy-changed"
	// ReasonUnproven is none of P12's proofs could be established. The
	// refusal's Attempts say what each one found.
	ReasonUnproven CleanupReason = "unproven"
	// ReasonReportUnstored is an investigation task has no stored report,
	// so the decisions it surfaced cannot have been registered.
	ReasonReportUnstored CleanupReason = "report-unstored"
	// ReasonHoldUnregistered is a decision the stored report surfaced has
	// no hold row, which PRD section 11 has cleanup refuse on.
	ReasonHoldUnregistered CleanupReason = "hold-unregistered"
	// ReasonCopyMissing is nothing stands at the task copy's path and no
	// cleanup record accounts for its removal, so what became of it is not
	// known.
	ReasonCopyMissing CleanupReason = "copy-missing"
	// ReasonShapeMoved is the task's shape changed between the decision and
	// the claim, which store.RecordTaskCleanup refused.
	ReasonShapeMoved CleanupReason = "shape-moved"
)

// ProofAttempt is what one of P12's proofs found when it could not be
// established, so a refusal says why each proof fell short rather than only
// that none held.
type ProofAttempt struct {
	// Proof is the proof attempted.
	Proof store.CleanupProof
	// Detail is what kept it from being established, in a sentence a caller
	// can report without adding to it. Commit identifiers in it are
	// evidence.
	Detail string
}

// CleanupRefusal reports a cleanup that did not remove the copy. It is the
// result a caller handles: there is no Cleanup inside it, and the copy
// stands where it stood.
type CleanupRefusal struct {
	// Task names the task, for a task copy, and is empty for a run copy.
	Task string
	// Run names the run, for a run copy, and is empty for a task copy.
	Run string
	// Reason is the category of refusal.
	Reason CleanupReason
	// Detail states what was found, in a sentence a caller can report
	// without adding to it.
	Detail string
	// Attempts is what each proof found, for ReasonUnproven, and empty for
	// every other reason.
	Attempts []ProofAttempt
	// Examined is the commits the copy was found to hold, when it was read.
	Examined []string
	// Cause is the underlying failure, when the refusal came from one.
	Cause error
}

// Error names the copy, the reason and the detail, and each proof attempt
// after them.
func (r *CleanupRefusal) Error() string {
	var b strings.Builder
	b.WriteString("tasks: the isolated copy of ")
	if r.Run != "" {
		b.WriteString("run " + r.Run)
	} else {
		b.WriteString("task " + r.Task)
	}
	b.WriteString(" was kept (" + string(r.Reason) + "): " + r.Detail)
	for _, a := range r.Attempts {
		b.WriteString("; " + string(a.Proof) + ": " + a.Detail)
	}
	return b.String()
}

// Unwrap makes every refusal match ErrCleanupRefused, and exposes the
// failure it came from alongside, so errors.Is against a mechanism error
// still matches.
func (r *CleanupRefusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{ErrCleanupRefused}
	}
	return []error{ErrCleanupRefused, r.Cause}
}

// remedies is the one statement of what gets a caller out of a refusal,
// keyed by its reason. Every remedy is an investigation and none is a
// flag: P12 says a refusal is a result that means stop and investigate,
// and a test holds that no entry here tells anyone to retry with force.
var remedies = map[CleanupReason]string{
	ReasonTerminalUnobserved: "Nothing observed the worker's terminal, so the copy is kept until a probe reports it stopped; " +
		"look at the copy before removing anything by hand.",
	ReasonTerminalLive: "The worker is still running in the copy. Let it finish, or stop it, and the next cleanup reads the copy again.",
	ReasonCopyDirty: "The copy holds changes no commit records. Open it and commit or discard them yourself; " +
		"nothing here decides that for you.",
	ReasonCopyUntracked: "The copy holds files no commit records. Open it and commit or remove them yourself; " +
		"nothing here decides that for you.",
	ReasonCopyStash: "The copy holds a stash. Open it and apply or drop the stash yourself; nothing here decides that for you.",
	ReasonCopyChanged: "The copy changed while it was being examined. The next cleanup reads it again; " +
		"if it keeps changing, something is still writing to it.",
	ReasonUnproven: "The work in the copy is not yet on a remote branch, in a merged pull request, or in the default branch. " +
		"Push it, merge it, or confirm it is not wanted and remove the copy yourself; each attempt above says what it found.",
	ReasonReportUnstored: "The investigation's report has not been stored, so the decisions it raised cannot have been registered. " +
		"Store the report first; the copy is kept until then.",
	ReasonHoldUnregistered: "A decision the report raised has no hold registered for it, so the report cannot be treated as complete. " +
		"Register the missing hold; the copy is kept until then.",
	ReasonCopyMissing: "Nothing stands where the copy should be and no record says it was removed. Find out what removed it before " +
		"treating the task as cleaned up.",
	ReasonShapeMoved: "The task's shape changed while the cleanup was being decided. The next cleanup decides again over the new shape.",
}

// Remedy states what gets a caller out of this refusal, as text it can
// report without adding to it. Every reason has one, and none of them names
// a flag.
func (r *CleanupRefusal) Remedy() string { return remedies[r.Reason] }

// Removal is the proof an isolated copy may be removed on: which of P12's
// proofs was established and the commits it examined. Only this package's
// decision produces one that Proven reports true; the zero value is not a
// proof and reports false, and both removal functions here refuse it.
type Removal struct {
	proof    store.CleanupProof
	examined []string
	proven   bool
}

// Proven reports whether this value came out of the decision.
func (r Removal) Proven() bool { return r.proven }

// Proof is the proof that was established.
func (r Removal) Proof() store.CleanupProof { return r.proof }

// Examined returns the commit identifiers the proof examined, in the order
// it read them, as a fresh slice.
func (r Removal) Examined() []string { return slices.Clone(r.examined) }

// CleanupResult is what a cleanup did.
type CleanupResult string

const (
	// CleanupRemoved is the copy was removed on a proof recorded first, and
	// the removal is marked on the record.
	CleanupRemoved CleanupResult = "removed"
	// CleanupAlreadyRemoved is the record already carried a removal, so
	// nothing was examined and nothing changed.
	CleanupAlreadyRemoved CleanupResult = "already-removed"
	// CleanupAbsent is nothing stood at a run copy's path and no record
	// claimed it, which is the ordinary state of a run refused before a
	// copy was cut for it. Nothing was written. A task copy answers
	// ReasonCopyMissing instead, because a task's copy is cut at dispatch
	// and a task that reached cleanup had one.
	CleanupAbsent CleanupResult = "absent"
)

// Cleanup is what a cleanup established and did.
type Cleanup struct {
	// Task names the task, for a task copy, and is empty for a run copy.
	Task string
	// Run names the run, for a run copy, and is empty for a task copy.
	Run string
	// Result says what was done.
	Result CleanupResult
	// Proof is the proof the removal rested on, from the record, and empty
	// for CleanupAbsent.
	Proof store.CleanupProof
	// Examined is what the proof examined, from the record.
	Examined []string
}

// TaskCopy names a task's copy to clean up and what the caller observed of
// its terminal. It carries nothing that stands for a proof, and a test walks
// its type to hold that.
type TaskCopy struct {
	// Task is the task whose copy is to be removed.
	Task string
	// Terminal is what the task's terminal looked like when the caller
	// asked, which must be taskstate.Dead for the copy to be examined at
	// all. The service passes the liveness its own task reader resolved,
	// which is the session provider's probe through its terminal seam, and
	// this package believes what it is handed; doc.go states that gap.
	Terminal taskstate.Liveness
}

// RunCopy names a run's copy to clean up.
type RunCopy struct {
	// Run is the run's record.
	Run store.Run
	// Gate is the path of the gate's bare repository the copy is a linked
	// worktree of, which internal/service locates and this package does
	// not.
	Gate string
}

// CleanupDeps is what a Cleaner is built from. Every field is required.
type CleanupDeps struct {
	// Home says where a task's copy and a run's copy stand.
	Home *home.Home
	// Store holds the records: the task, the run, the repository, the
	// merge record, the stored report, the holds, and the cleanup record
	// this writes.
	Store *store.Store
	// Forge is the code host the merged-pull-request proof reads, opened on
	// the specifier forge.RepositoryOf reads out of the upstream; the service
	// passes its forge.Hosts, so a gitlab.com upstream is read on GitLab.
	Forge forge.Host
}

// Cleaner is the cleanup decision over one home and one store. It is safe
// for concurrent use; what serializes two cleanups of one copy is the
// record's write-once claim.
type Cleaner struct {
	home  *home.Home
	store *store.Store
	forge forge.Host
	// betweenProofAndRemoval runs after the proof is recorded and before
	// the copy is re-read and removed. It is nil in production and set only
	// by this package's tests, which use it to change the copy inside the
	// window and to stand in for a crash there.
	betweenProofAndRemoval func()
	// betweenDecisionAndClaim runs after a task copy's proof is decided and
	// before the record claims it. It is nil in production and set only by
	// this package's tests, which use it to hold a cleanup at the point
	// where a promotion of the same task is held, so the two claims race.
	betweenDecisionAndClaim func()
}

// NewCleaner builds a Cleaner, refusing with ErrIncomplete when any
// dependency is nil.
func NewCleaner(d CleanupDeps) (*Cleaner, error) {
	switch {
	case d.Home == nil:
		return nil, fmt.Errorf("%w: no home", ErrIncomplete)
	case d.Store == nil:
		return nil, fmt.Errorf("%w: no store", ErrIncomplete)
	case d.Forge == nil:
		return nil, fmt.Errorf("%w: no code host", ErrIncomplete)
	}
	return &Cleaner{home: d.Home, store: d.Store, forge: d.Forge}, nil
}

// CleanUp decides whether a task's copy may be removed and removes it when
// it may, on the terms this file's opening comment states, and returns what
// it did or a *CleanupRefusal. It is idempotent: a task whose record already
// carries a removal answers CleanupAlreadyRemoved, and one whose record
// carries a proof and no removal is completed, at once when nothing stands
// at the path and after re-reading the copy when something does.
//
// For a task cleanup has not reached it refuses, in this order, a terminal
// that is not Dead, nothing standing at the copy's path, a dirty or
// untracked tree, a stash, and then decides by shape. An investigation task
// is held to PRD section 11 first: its report must be stored and every
// decision the report surfaced must have a hold row, by the keys the store
// recorded with the report, and an open hold does not block, because the
// hold survives in the store. Its copy then skips the proofs only when its
// inventory equals the snapshot its dispatch recorded and its record names
// no run and no pull request; an absent snapshot is read as a copy with
// work, because a copy nobody snapshotted cannot be shown unchanged. Every
// other copy is held to P12's three proofs, which proof.go states. The
// proof is recorded with the task's shape as the claim, the inventory is
// re-read, the directory is removed, and the removal is marked.
//
// An error that is no refusal is a record that cannot be read or written,
// or a repository that could not be examined; nothing is removed on one.
func (c *Cleaner) CleanUp(ctx context.Context, req TaskCopy) (Cleanup, error) {
	task, err := c.store.Task(ctx, req.Task)
	if err != nil {
		return Cleanup{}, err
	}
	copyPath, err := c.home.TaskCopy(task.ID)
	if err != nil {
		return Cleanup{}, err
	}
	if filepath.Clean(task.WorktreePath) != copyPath {
		return Cleanup{}, fmt.Errorf("%w: task %s reserved its copy at %s and this home puts it at %s",
			ErrCleanupRecord, task.ID, task.WorktreePath, copyPath)
	}
	refuse := func(reason CleanupReason, detail string, examined []string, cause error) (Cleanup, error) {
		return Cleanup{}, &CleanupRefusal{Task: task.ID, Reason: reason, Detail: detail, Examined: examined, Cause: cause}
	}

	record, err := c.store.TaskCleanup(ctx, task.ID)
	switch {
	case err == nil && record.RemovedAt.IsKnown():
		return Cleanup{Task: task.ID, Result: CleanupAlreadyRemoved, Proof: record.Proof, Examined: record.Examined}, nil
	case err == nil:
		return c.resumeTask(ctx, task, record, copyPath, req.Terminal)
	case !errors.Is(err, store.ErrNotFound):
		return Cleanup{}, err
	}

	if req.Terminal != taskstate.Dead {
		reason := ReasonTerminalUnobserved
		if req.Terminal == taskstate.Live {
			reason = ReasonTerminalLive
		}
		return refuse(reason, fmt.Sprintf("the task's terminal was %s, and the copy is examined only once it is %s",
			livenessWord(req.Terminal), taskstate.Dead), nil, nil)
	}
	standing, err := c.standing(copyPath)
	if err != nil {
		return Cleanup{}, err
	}
	if !standing {
		return refuse(ReasonCopyMissing, "nothing stands at "+copyPath+" and no cleanup record accounts for it", nil, nil)
	}
	copied, err := vcs.OpenWorktree(ctx, copyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the copy of task %s: %w", task.ID, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: reading the copy of task %s: %w", task.ID, err)
	}
	work := inventoryCommits(inv)
	if reason, detail, refused := treeRefusal(inv); refused {
		return refuse(reason, detail, work, nil)
	}

	var removal Removal
	switch task.Shape {
	case store.TaskInvestigation:
		if refusal := c.verifyReport(ctx, task.ID, work); refusal != nil {
			return Cleanup{}, refusal
		}
		if scratch(task, inv) {
			removal = Removal{proof: store.CleanupProofScratch, examined: work, proven: true}
		}
	case store.TaskDelivery:
	default:
		return Cleanup{}, fmt.Errorf("%w: task %s has shape %q", ErrCleanupRecord, task.ID, task.Shape)
	}
	if !removal.proven {
		l, err := c.taskLanding(ctx, task, copied, work)
		if err != nil {
			return Cleanup{}, err
		}
		var attempts []ProofAttempt
		removal, attempts = c.prove(ctx, l)
		if !removal.proven {
			detail := "none of the three proofs could be established"
			if l.local {
				detail = "the content check against the local default branch could not be established"
			}
			return Cleanup{}, &CleanupRefusal{Task: task.ID, Reason: ReasonUnproven,
				Detail: detail, Attempts: attempts, Examined: work}
		}
	}

	again, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: re-reading the copy of task %s: %w", task.ID, err)
	}
	if !inventoryEqual(inv, again) {
		return refuse(ReasonCopyChanged, "the copy's references, HEAD, tree or stash changed while the proof was being read",
			inventoryCommits(again), nil)
	}
	if c.betweenDecisionAndClaim != nil {
		c.betweenDecisionAndClaim()
	}
	record, err = c.store.RecordTaskCleanup(ctx, store.TaskCleanup{
		TaskID: task.ID, Proof: removal.proof, Examined: removal.examined,
	}, task.Shape)
	switch {
	case errors.Is(err, store.ErrTaskShapeMoved):
		return refuse(ReasonShapeMoved, err.Error(), work, err)
	case err != nil:
		return Cleanup{}, err
	}
	if c.betweenProofAndRemoval != nil {
		c.betweenProofAndRemoval()
	}
	return c.finishTask(ctx, task.ID, record, copied, inv, removal)
}

// resumeTask completes a task cleanup whose record carries a proof and no
// removal: the copy is gone, or it still stands and still holds nothing the
// proof did not examine.
func (c *Cleaner) resumeTask(ctx context.Context, task store.Task, record store.TaskCleanup, copyPath string,
	terminal taskstate.Liveness) (Cleanup, error) {
	standing, err := c.standing(copyPath)
	if err != nil {
		return Cleanup{}, err
	}
	removal := Removal{proof: record.Proof, examined: record.Examined, proven: true}
	if !standing {
		if err := c.store.MarkCleanupRemoved(ctx, task.ID, time.Now()); err != nil {
			return Cleanup{}, err
		}
		return Cleanup{Task: task.ID, Result: CleanupRemoved, Proof: record.Proof, Examined: record.Examined}, nil
	}
	if terminal != taskstate.Dead {
		reason := ReasonTerminalUnobserved
		if terminal == taskstate.Live {
			reason = ReasonTerminalLive
		}
		return Cleanup{}, &CleanupRefusal{Task: task.ID, Reason: reason, Detail: fmt.Sprintf(
			"the task's terminal was %s, and a copy standing on a recorded proof is examined only once it is %s",
			livenessWord(terminal), taskstate.Dead)}
	}
	copied, err := vcs.OpenWorktree(ctx, copyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the copy of task %s: %w", task.ID, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: reading the copy of task %s: %w", task.ID, err)
	}
	return c.finishTask(ctx, task.ID, record, copied, inv, removal)
}

// finishTask is the removal half of a task cleanup, over a recorded proof:
// the copy is re-read, refused if it changed or holds work the proof did not
// examine, removed, and the removal marked.
func (c *Cleaner) finishTask(ctx context.Context, id string, record store.TaskCleanup, copied *vcs.Repository,
	inv vcs.Inventory, removal Removal) (Cleanup, error) {
	again, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: re-reading the copy of task %s: %w", id, err)
	}
	work := inventoryCommits(again)
	if reason, detail, refused := treeRefusal(again); refused {
		return Cleanup{}, &CleanupRefusal{Task: id, Reason: reason, Detail: detail, Examined: work}
	}
	if !inventoryEqual(inv, again) || !subset(work, removal.examined) {
		return Cleanup{}, &CleanupRefusal{Task: id, Reason: ReasonCopyChanged, Examined: work,
			Detail: "the copy holds a reference, a HEAD or a commit the recorded proof did not examine"}
	}
	if err := removeTaskCopy(copied.Path(), removal); err != nil {
		return Cleanup{}, err
	}
	if err := c.store.MarkCleanupRemoved(ctx, id, time.Now()); err != nil {
		return Cleanup{}, err
	}
	return Cleanup{Task: id, Result: CleanupRemoved, Proof: record.Proof, Examined: record.Examined}, nil
}

// removeTaskCopy removes a task's clone. It is the one directory removal in
// this package, it is not git, and it takes the proof: a Removal nobody
// proved is refused before anything is touched.
func removeTaskCopy(path string, r Removal) error {
	if !r.proven {
		return fmt.Errorf("tasks: refusing to remove %s on a removal nobody proved", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("tasks: removing the copy at %s: %w", path, err)
	}
	return nil
}

// CleanUpRun decides whether a run's copy may be removed and removes it when
// it may, on the same terms as CleanUp with the differences a linked
// worktree makes: its work is the commit HEAD stands on, no terminal is
// asked about because the run has settled, the proofs read the gate's
// repository and land their fetches there, and the removal is
// vcs.RemoveWorktree, so a tree git refuses to remove stays. A run with
// nothing at its copy's path and no record answers CleanupAbsent.
//
// Where CleanUp writes no record on a refusal, this records one: a run's
// copy is cleaned up when its run settles, and a refusal there is what the
// run's status has to say about a copy left standing, so the refusal's
// reason and what the copy held are written to store.RecordRunCleanupRefusal
// and the typed refusal is returned to the caller as well. A proof replaces
// a refusal on the record; a refusal never replaces a proof.
func (c *Cleaner) CleanUpRun(ctx context.Context, req RunCopy) (Cleanup, error) {
	run := req.Run
	path := c.home.Worktree(run.RepositoryID, run.ID)
	record, err := c.store.RunCleanup(ctx, run.ID)
	switch {
	case err == nil && record.RemovedAt.IsKnown():
		return Cleanup{Run: run.ID, Result: CleanupAlreadyRemoved, Proof: record.Proof.Or(""), Examined: record.Examined}, nil
	case err == nil && record.Proof.IsKnown():
		return c.resumeRun(ctx, run, record, path, req.Gate)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return Cleanup{}, err
	}
	standing, err := c.standing(path)
	if err != nil {
		return Cleanup{}, err
	}
	if !standing {
		return Cleanup{Run: run.ID, Result: CleanupAbsent}, nil
	}
	bare, err := vcs.OpenBare(ctx, req.Gate, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the gate repository of run %s: %w", run.ID, err)
	}
	copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the copy of run %s: %w", run.ID, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: reading the copy of run %s: %w", run.ID, err)
	}
	work := []string{inv.Head.Commit}
	if reason, detail, refused := treeRefusal(inv); refused {
		return c.refuseRun(ctx, &CleanupRefusal{Run: run.ID, Reason: reason, Detail: detail, Examined: work})
	}
	l, err := c.runLanding(ctx, run, bare, work)
	if err != nil {
		return Cleanup{}, err
	}
	removal, attempts := c.prove(ctx, l)
	if !removal.proven {
		return c.refuseRun(ctx, &CleanupRefusal{Run: run.ID, Reason: ReasonUnproven, Attempts: attempts, Examined: work,
			Detail: "none of the three proofs could be established"})
	}
	again, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: re-reading the copy of run %s: %w", run.ID, err)
	}
	if !inventoryEqual(inv, again) {
		return c.refuseRun(ctx, &CleanupRefusal{Run: run.ID, Reason: ReasonCopyChanged, Examined: []string{again.Head.Commit},
			Detail: "the copy's HEAD, tree or stash changed while the proof was being read"})
	}
	record, err = c.store.RecordRunCleanup(ctx, store.RunCleanup{
		Run: run.ID, Proof: store.Known(removal.proof), Examined: removal.examined,
	})
	if err != nil {
		return Cleanup{}, err
	}
	if c.betweenProofAndRemoval != nil {
		c.betweenProofAndRemoval()
	}
	return c.finishRun(ctx, run.ID, record, bare, copied, inv, removal)
}

// resumeRun completes a run cleanup whose record carries a proof and no
// removal, on resumeTask's terms.
func (c *Cleaner) resumeRun(ctx context.Context, run store.Run, record store.RunCleanup, path, gate string) (Cleanup, error) {
	standing, err := c.standing(path)
	if err != nil {
		return Cleanup{}, err
	}
	removal := Removal{proof: record.Proof.Or(""), examined: record.Examined, proven: true}
	if !standing {
		if err := c.store.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); err != nil {
			return Cleanup{}, err
		}
		return Cleanup{Run: run.ID, Result: CleanupRemoved, Proof: removal.proof, Examined: record.Examined}, nil
	}
	bare, err := vcs.OpenBare(ctx, gate, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the gate repository of run %s: %w", run.ID, err)
	}
	copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: opening the copy of run %s: %w", run.ID, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: reading the copy of run %s: %w", run.ID, err)
	}
	return c.finishRun(ctx, run.ID, record, bare, copied, inv, removal)
}

// finishRun is the removal half of a run cleanup, over a recorded proof.
func (c *Cleaner) finishRun(ctx context.Context, id string, record store.RunCleanup, bare, copied *vcs.Repository,
	inv vcs.Inventory, removal Removal) (Cleanup, error) {
	again, err := copied.Inventory(ctx)
	if err != nil {
		return Cleanup{}, fmt.Errorf("tasks: re-reading the copy of run %s: %w", id, err)
	}
	work := []string{again.Head.Commit}
	if reason, detail, refused := treeRefusal(again); refused {
		return Cleanup{}, &CleanupRefusal{Run: id, Reason: reason, Detail: detail, Examined: work}
	}
	if !inventoryEqual(inv, again) || !subset(work, removal.examined) {
		return Cleanup{}, &CleanupRefusal{Run: id, Reason: ReasonCopyChanged, Examined: work,
			Detail: "the copy's HEAD is on a commit the recorded proof did not examine"}
	}
	if err := removeRunCopy(ctx, bare, copied.Path(), removal); err != nil {
		return Cleanup{}, err
	}
	if err := c.store.MarkRunCleanupRemoved(ctx, id, time.Now()); err != nil {
		return Cleanup{}, err
	}
	return Cleanup{Run: id, Result: CleanupRemoved, Proof: record.Proof.Or(""), Examined: record.Examined}, nil
}

// removeRunCopy removes a run's linked worktree through the non-discarding
// vcs.RemoveWorktree, behind the proof: a Removal nobody proved is refused
// before git is asked. A tree git refuses to remove is returned as git's own
// error, and the copy stays.
func removeRunCopy(ctx context.Context, bare *vcs.Repository, path string, r Removal) error {
	if !r.proven {
		return fmt.Errorf("tasks: refusing to remove %s on a removal nobody proved", path)
	}
	// The removal is made under the gate's copies lock, held exclusively, so
	// a push or a fetch into the gate that takes the lock waits for it rather
	// than meeting the copy's entry half-removed. Only the removal is held:
	// the proof before it can fetch from the upstream, and a push would wait
	// on the network for no reason.
	lock, err := gate.LockCopies(ctx, bare.Path())
	if err != nil {
		return fmt.Errorf("tasks: waiting to remove the copy at %s: %w", path, err)
	}
	defer func() { _ = lock.Release() }()
	if err := bare.RemoveWorktree(ctx, path); err != nil {
		return fmt.Errorf("tasks: removing the copy at %s: %w", path, err)
	}
	return nil
}

// refuseRun records a run cleanup's refusal and returns it.
func (c *Cleaner) refuseRun(ctx context.Context, r *CleanupRefusal) (Cleanup, error) {
	detail := r.Detail
	for _, a := range r.Attempts {
		detail += "; " + string(a.Proof) + ": " + a.Detail
	}
	if _, err := c.store.RecordRunCleanupRefusal(ctx, r.Run, string(r.Reason), detail, r.Examined); err != nil {
		return Cleanup{}, fmt.Errorf("tasks: recording the refused cleanup of run %s: %w", r.Run, err)
	}
	return Cleanup{}, r
}

// verifyReport is PRD section 11's rule for an investigation copy: the
// report is stored and every hold the store registered from it has a row.
// An open hold does not block. The detail counts rather than quotes, because
// a hold's text is the worker's and is evidence, not text to relay.
func (c *Cleaner) verifyReport(ctx context.Context, id string, work []string) error {
	report, err := c.store.TaskReport(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &CleanupRefusal{Task: id, Reason: ReasonReportUnstored, Examined: work,
			Detail: "the investigation's report has not been stored, so no decision it surfaced can have been registered"}
	case err != nil:
		return err
	}
	missing := 0
	for _, key := range report.HoldKeys {
		switch _, err := c.store.Hold(ctx, key); {
		case errors.Is(err, store.ErrNotFound):
			missing++
		case err != nil:
			return err
		}
	}
	if missing > 0 {
		return &CleanupRefusal{Task: id, Reason: ReasonHoldUnregistered, Examined: work,
			Detail: fmt.Sprintf("%d of the %d decisions the stored report surfaced have no hold registered", missing, len(report.HoldKeys))}
	}
	return nil
}

// scratch reports whether an investigation copy may skip the proofs: its
// inventory equals the snapshot its dispatch recorded and its record names
// no run and no pull request. An absent snapshot is a copy with work.
func scratch(task store.Task, inv vcs.Inventory) bool {
	if task.RunID.IsKnown() || task.PullRequest.IsKnown() {
		return false
	}
	snapshot, ok := task.StartSnapshot.Get()
	if !ok {
		return false
	}
	return snapshotMatches(inv, snapshot)
}

// standing reports whether something is at path. A path that cannot be
// read is an error, never an absence.
func (c *Cleaner) standing(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("tasks: reading %s: %w", path, err)
	}
}

// treeRefusal reads the three indicators off an inventory, in the order
// the refusals are made.
func treeRefusal(inv vcs.Inventory) (CleanupReason, string, bool) {
	switch {
	case inv.Dirty:
		return ReasonCopyDirty, "the copy holds a tracked file that differs from HEAD", true
	case inv.Untracked:
		return ReasonCopyUntracked, "the copy holds a file no commit records", true
	case inv.Stash:
		return ReasonCopyStash, "the copy holds a stash", true
	}
	return "", "", false
}

// inventoryCommits is the work set of an inventory: every commit a
// reference reaches and the commit HEAD stands on, each once, references
// first in their order and HEAD last when no reference reaches it.
func inventoryCommits(inv vcs.Inventory) []string {
	var out []string
	for _, ref := range inv.Refs {
		if !slices.Contains(out, ref.Commit) {
			out = append(out, ref.Commit)
		}
	}
	if !slices.Contains(out, inv.Head.Commit) {
		out = append(out, inv.Head.Commit)
	}
	return out
}

// inventoryEqual reports whether two inventories read the same: the same
// references at the same commits, the same HEAD, and the same three
// indicators.
func inventoryEqual(a, b vcs.Inventory) bool {
	if a.Head != b.Head || a.Dirty != b.Dirty || a.Untracked != b.Untracked || a.Stash != b.Stash || len(a.Refs) != len(b.Refs) {
		return false
	}
	for i := range a.Refs {
		if a.Refs[i].Name != b.Refs[i].Name || a.Refs[i].Commit != b.Refs[i].Commit {
			return false
		}
	}
	return true
}

// snapshotMatches reports whether an inventory equals the snapshot a
// dispatch recorded, over the set the inventory reads: refs/heads/,
// refs/tags/ and refs/stash, and HEAD. The snapshot lists every reference
// the copy had, so it is narrowed to that set before the comparison; a
// remote-tracking reference the snapshot recorded is not work and is not
// compared.
func snapshotMatches(inv vcs.Inventory, snapshot store.CopySnapshot) bool {
	if inv.Head.Commit != snapshot.Head || inv.Head.Branch != snapshot.Branch {
		return false
	}
	var recorded []store.SnapshotRef
	for _, r := range snapshot.Refs {
		if strings.HasPrefix(r.Name, "refs/heads/") || strings.HasPrefix(r.Name, "refs/tags/") || r.Name == "refs/stash" {
			recorded = append(recorded, r)
		}
	}
	if len(recorded) != len(inv.Refs) {
		return false
	}
	for _, ref := range inv.Refs {
		if !slices.ContainsFunc(recorded, func(r store.SnapshotRef) bool { return r.Name == ref.Name && r.Commit == ref.Commit }) {
			return false
		}
	}
	return true
}

// subset reports whether every element of have is in examined.
func subset(have, examined []string) bool {
	for _, h := range have {
		if !slices.Contains(examined, h) {
			return false
		}
	}
	return true
}

// livenessWord renders a liveness for a refusal, reading anything outside
// the set as unobserved, which is how internal/taskstate reads it.
func livenessWord(l taskstate.Liveness) string {
	if l == taskstate.Live || l == taskstate.Dead {
		return string(l)
	}
	return string(taskstate.Unobserved)
}
