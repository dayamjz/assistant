package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Carrying kept work on: PRD section 6's "a worker is relaunched in that same
// copy with a note saying what is already there", on the owner's decision for
// #411. The worker is a new task's, of the same project, shape and branch as
// the task the copy outlived, created through intake's own path
// (tasks.Queue.Create: the record first, then the directory, under the branch
// rule and the floor), with the kept copy adopted as its copy in place of a
// fresh clone. The finished task stays finished, since section 6 says work
// that continues on it is a new task, and the entry closes once the new task
// owns the copy.
//
// # What is checked before anything is written
//
// The copy is asserted with tasks.AssertIsolated under the worker's
// environment before anything is written into it, against the expectation
// of the task it outlived, and read for the commits and objects the entry
// recorded it standing on (holdsKeptWork, tasks.Cleaner.Unheld: each a
// value HEAD, a reference, a root reference or a reflog entry records, or
// a commit one reaches), the one reading of what the copy holds that the
// answer, the completion before the move, the completion after it and
// the completion right before its mark all make, so a copy whose kept
// commit was amended away but stands in its reflog or in ORIG_HEAD is
// carried on by each, and one that has lost it altogether is refused by
// each. A refusal on the remote set or HEAD is tolerated
// only as far as the reshape below repairs it, each read on its own: for a
// delivery copy, a missing gate remote or one whose one URL is elsewhere,
// which it points at the gate, and HEAD off the task's branch while no
// branch the copy holds has that name, is under it or is a name it sits
// under, which it creates at HEAD; for an investigation, an attached HEAD,
// which it detaches. Anything else on either, an investigation's remote, a
// delivery copy's second remote, a push URL or a second URL, the task's
// branch standing away from HEAD, or a branch in the way of its name,
// refuses the carry on with nothing recorded (unreshapeableRemotes,
// unreshapeableHead): the reshape removes no remote and moves or renames
// no branch, since any of that can leave a commit with no name, and P6 has
// the program refuse rather than risk that, so the person repairs the copy
// or answers otherwise. So does a core.worktree git reads for the copy,
// which the move would leave naming the old path, and so does a linked
// worktree of the copy, which names the copy's repository by the path it
// stands at and which the move would leave unable to read it: the program
// writes into no worktree it did not make, so the person removes it (git
// worktree remove) or lands the copy (round 47). So does any other refusal, a copy that is not a
// clone of its own, one that is the primary or inside it, among them. A
// copy that is gone refuses it too. After the reshape the copy is asserted
// again at its new path on the new task's expectation, as dispatch will
// assert it, and a refusal there leaves the entry owed rather than marked
// (assertReshaped).
//
// A run's copy is refused before that: it is a linked worktree of the gate,
// which a task's copy cannot be, and a person's branch, which intake's branch
// rule refuses a task. Carrying a run's copy on is therefore not in this
// build, and the refusal says so.
//
// # A failed adoption, and every entry of the copy
//
// The claim names the new task, and until the task owns the copy the entry
// stays open, listed and holding its project's dispatch, the new task's
// among it, by internal/store's hold predicate: an adoption that fails, a
// rename the old task's directory refuses among the ways, leaves the claim
// owed and the task undispatched, so no dispatch cuts it a fresh clone
// without the kept work. Completion takes a copy that
// already stands at the new task's path only when it is the kept one
// (verifyAdopted): the copy the answer was given for by its file identity,
// tasks.CopyIdentity, which the answer records, with the old path gone and
// every commit the entry recorded still held in it, on holdsKeptWork's
// terms. Holding them alone is not identity: a fresh clone of an upstream
// that took the recorded commits up holds them all and lacks the copy's
// own work. The kept copy is checked to be that copy, and to hold those
// commits, before it is moved, too. Carrying on
// moves the whole copy, so the mark answers and marks every open entry of
// the same copy, the copy kept again at another state, in its one write,
// one kept after the claim and before the move included, since its state
// moved with the copy; and the copy is held to what every one of those
// entries recorded, not the answered one alone, by each reading of it
// (holdsKeptWork over store.OpenKeptWorkOfCopy), so no entry is closed
// over work the copy has since lost. The last reading is made right
// before the mark, where the copy stands, with the reshape, the assertion
// and the note behind it, so an entry is closed only over a copy read
// holding its work after every step of the completion (round 47: before,
// the mark took the set read before the move, and work pruned from the
// moved copy in between was closed over). What stands between that
// reading and the write is the moment itself. The mark is given the
// entries that reading held the copy to and closes those and no other: an
// entry kept after that reading refuses the mark (store.ErrKeptWorkUnverified),
// the answer is refused with the copy at the task's path and the claim
// standing, and the next answer or start reads that entry too and marks
// then, so nothing closes an entry nothing verified (the carry-on audit of
// b448115). The mark is made only once
// verifyAdopted, or the move itself, has the copy at the new path and gone
// from its own, so every entry naming that path is of the copy that moved.
// The residual: a directory put at the old path after that and kept there
// before the mark would be answered with the copy too; nothing in this
// build puts one there, since the task that path belongs to has ended.
// A claim is refused while another
// entry of the copy holds an answer not yet carried out, as every answer
// is (store.ErrKeptWorkCopyOwed).
//
// # The order, and what a stop leaves
//
// A carry on is recorded as two facts, so the entry is never answered before
// the task it names owns the copy. The identifier is minted first and claimed
// on the entry with the copy's identity and who asked
// (store.ClaimKeptWorkCarryOn), which leaves the entry open: status, the
// catch-up read and the supervisor loop list it, and the project's new tasks,
// the one it names among them, still wait on it. completeCarryOn then takes the
// task's dispatch slot for that identifier before anything else, so no dispatch
// pass cuts a fresh clone at the path the copy is about to stand at; between
// the claim and the slot no task row exists yet, so no pass can find the task
// to dispatch. Once the row exists a pass can hold the slot while it reads that
// the claim holds the task; an answer that finds claiming[id] set is
// refused at once with machine.PhraseCarryOnSlotBusy (claimCarryOnSlot),
// which states only what that fact establishes and never names a
// carry-on under way that is not (#707, #731). Then, in the order
// intake keeps, the task is created with that identifier, with the outlived
// task's delivery posture as the floor intake records it under unless the
// project's, read once there, is stricter (carriedPosture), the copy is moved
// to the task's own path and reshaped as dispatch shapes a fresh clone, the
// note is appended to the instructions file, and the entry is answered
// carry-on and marked in one write (store.MarkKeptWorkCarriedOn), which is
// when it closes. An intake that refuses the task, a role the configuration no
// longer has among the ways, has created nothing and moved nothing, so the
// claim is withdrawn (store.WithdrawKeptWorkCarryOn, refused by the store once
// the task's row exists) and the answer is refused naming why, with the entry
// open as it was.
// The completion carries out the claim the entry holds, read again under the
// task's dispatch slot, never one the answer read before it: a claim another
// call withdrew, and a retry claimed again for another task in between, is
// the retry's to carry out, and the mark takes the task it carries out for
// and refuses another (store.MarkKeptWorkCarriedOn). Only a call holding a
// task's slot withdraws its claim, so a claim that names the task under the
// slot stands until that call marks or withdraws it.
// Every step after the claim is idempotent on what it finds (the row stands,
// the instructions file stands, the copy stands at the new path, the whole
// note is in the file: tasks.AppendNote completes a note an earlier append
// left partial), so a service that stops anywhere in it is completed by the
// next call or the next start from the entry alone, which names the task:
// completeCarryOnsAtOpen reads every claim not marked. A stop before the task's
// row is written leaves an open entry naming a task that does not exist yet,
// which the completion creates, or withdraws if intake refuses it; a stop
// between the row and the instructions file, inside intake, leaves a row with
// no file, which intake's own CompleteIntake writes before the copy is moved; a
// stop after the move leaves the copy at the new path, which it finds. What is
// not recovered: a copy gone from both paths, which is reported and left; and a
// claim whose task was created and whose copy then cannot be adopted, which
// stays owed, the entry open and listed and no other answer taken on the copy,
// until a later call or start adopts it.
//
// Once the slot is given back a dispatch pass is asked for, and the task
// dispatches over its standing copy on dispatch.go's terms: recognized at
// its first claim by the identity the answer recorded, swept once that
// claim has won, asserted again at its own path, and launched with the
// instructions file, note included. Other open entries of the project still hold it, as they hold
// any new task.

// carryOnHeading is the heading the carry-on note is appended to the
// instructions under, and the marker that keeps the append idempotent.
const carryOnHeading = "## Carried on, written by the program"

// carryOnKeptWork is the carry-on answer, on this file's terms.
func (s *Service) carryOnKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptCarryOn {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	// A claim this answer finds standing, one a stopped service or a failed
	// adoption left, is completed rather than claimed again.
	if entry.Open() && !entry.CarryOnClaimed() {
		plan, err := s.carryOnOf(ctx, entry)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := s.verifyCarriedCopy(ctx, entry, entry.CopyPath, plan); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := s.assertKeptCopy(ctx, entry, plan); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if _, err := s.holdsKeptWork(ctx, entry, entry.CopyPath); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		kept, err := tasks.IdentifyCopy(entry.CopyPath)
		if err != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not carried on: %w", ipc.ErrRefused, entry.ID, err)
		}
		id, err := home.NewTaskID()
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		claimed, err := s.store.ClaimKeptWorkCarryOn(ctx, entry.ID, id, kept.String(), req.HoldResolver())
		switch {
		case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed) ||
			errors.Is(err, store.ErrKeptWorkCarryOnClaimed):
			// Answered, closed or claimed by another call in between: a
			// carry on it reads is completed, anything else refused.
			again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				return machine.KeptWorkAnswered{}, readErr
			}
			if again.Answer.Or("") != store.KeptCarryOn && !again.CarryOnClaimed() {
				return machine.KeptWorkAnswered{}, refusedAnswered(again)
			}
			entry = again
		case errors.Is(err, store.ErrKeptWorkCopyOwed):
			return machine.KeptWorkAnswered{}, refusedOwed(entry, err)
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			entry = claimed
		}
	}
	if s.betweenCarryOnClaimAndCompletion != nil {
		if err := s.betweenCarryOnClaimAndCompletion(); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
	}
	return s.completeCarryOn(ctx, entry)
}

// carryOn is what a carry on of an entry creates, read from the task the
// copy outlived.
type carryOn struct {
	old        store.Task
	repository store.Repository
	gatePath   string
	branch     string
}

// carryOnOf reads what a carry on of the entry would create, refusing with
// ipc.ErrRefused what this file's opening comment refuses.
func (s *Service) carryOnOf(ctx context.Context, entry store.KeptWork) (carryOn, error) {
	refuse := func(why string) (carryOn, error) {
		return carryOn{}, fmt.Errorf("%w: kept work entry %d was not carried on: %s", ipc.ErrRefused, entry.ID, why)
	}
	oldID, ok := entry.TaskID.Get()
	if !ok {
		return refuse("it is the copy of a run, a linked worktree of the gate on a person's branch, and carrying a run's copy on " +
			"is not in this build; land it, or push its commit yourself")
	}
	old, err := s.store.Task(ctx, oldID)
	if err != nil {
		return carryOn{}, err
	}
	repositoryID, ok := old.RepositoryID.Get()
	if !ok {
		return refuse("the task it outlived names no repository, so no copy can be asserted for a task carrying it on")
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return carryOn{}, err
	}
	gatePath, err := s.gatePathOf(ctx, repository)
	if err != nil {
		return carryOn{}, err
	}
	return carryOn{old: old, repository: repository, gatePath: gatePath, branch: old.Branch.Or("")}, nil
}

// verifyCarriedCopy holds the kept copy standing at path, the kept path or
// the reserved task's once moved, to the one it is recorded as
// (tasks.Cleaner.VerifyKeptCopy, on internal/safety's terms), before
// anything of it is read or written, and refuses with ipc.ErrRefused
// naming why, so a carry on never reads or adopts a copy git reads through
// another repository or a link. A path where nothing stands is left to the
// caller, which refuses a copy that is gone.
func (s *Service) verifyCarriedCopy(ctx context.Context, entry store.KeptWork, path string, plan carryOn) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	at := entry
	at.CopyPath = path
	if err := s.cleaner.VerifyKeptCopy(ctx, at, plan.gatePath); err != nil {
		return fmt.Errorf("%w: kept work entry %d was not carried on: %w", ipc.ErrRefused, entry.ID, err)
	}
	return nil
}

// assertKeptCopy asserts the kept copy where it stands against the
// expectation of the task it outlived, on this file's terms, and refuses
// with ipc.ErrRefused a copy that is gone or that fails any comparison but
// the two the reshape writes. Its callers hold the copy to its record
// first (verifyCarriedCopy).
func (s *Service) assertKeptCopy(ctx context.Context, entry store.KeptWork, plan carryOn) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: kept work entry %d was not carried on: %s", ipc.ErrRefused, entry.ID, why)
	}
	want, _, err := tasks.Expect(s.home, plan.old.ID, plan.old.Shape, plan.gatePath, plan.repository.WorkingPath, plan.branch)
	if err != nil {
		return err
	}
	if filepath.Clean(entry.CopyPath) != want.Copy {
		return refuse(fmt.Sprintf("its copy at %s is not where this home keeps task %s's copy, %s", entry.CopyPath, plan.old.ID, want.Copy))
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, s.git()...)
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			if _, statErr := os.Lstat(entry.CopyPath); errors.Is(statErr, os.ErrNotExist) {
				return refuse("its copy at " + entry.CopyPath + " is gone")
			}
			return refuse(fmt.Sprintf("its copy at %s is not a repository: %v", entry.CopyPath, err))
		}
		return fmt.Errorf("service: observing the kept copy at %s: %w", entry.CopyPath, err)
	}
	// Carrying on moves the copy, and a core.worktree git reads for it,
	// naming where it stands now, would have git read the moved copy at
	// the old path, so the reshape after the move fails and the claim is
	// stranded (the carry-on audit of b448115): it is refused here, before
	// anything is claimed or moved, wherever the setting is read from.
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, s.git()...)
	if err != nil {
		return fmt.Errorf("service: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	if worktree, set, err := copied.ConfigValue(ctx, "core.worktree"); err != nil {
		return fmt.Errorf("service: reading core.worktree of the kept copy at %s: %w", entry.CopyPath, err)
	} else if set {
		return refuse(fmt.Sprintf("git reads core.worktree as %q for its copy, and carrying on moves the copy, which git would then "+
			"read at that path; unset it (git config --unset core.worktree in the copy) and answer again, or land the copy", worktree))
	}
	// A linked worktree of the copy names the copy's repository by the
	// path it stands at, so the move would leave the worktree unable to
	// read it (round 47). The program writes into no worktree it did not
	// make, so it is refused here, before anything is claimed or moved, as
	// git lists it (vcs.ListWorktrees), the copy's own entry aside.
	worktrees, err := copied.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("service: listing the worktrees of the kept copy at %s: %w", entry.CopyPath, err)
	}
	own, err := filepath.EvalSymlinks(entry.CopyPath)
	if err != nil {
		return fmt.Errorf("service: resolving the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, w := range worktrees {
		at, err := filepath.EvalSymlinks(w.Path)
		if err != nil {
			return refuse(fmt.Sprintf("it has a linked worktree git lists at %s that could not be read: %v; remove it (git worktree remove) and answer again, or land the copy", w.Path, err))
		}
		if at != own {
			return refuse(fmt.Sprintf("it has a linked worktree at %s, which names the copy by the path it stands at and which carrying on would move "+
				"out from under it; remove the worktree (git worktree remove in the copy) and answer again, or land the copy", w.Path))
		}
	}
	if _, err := tasks.AssertIsolated(obs, agents.WorkerEnvironment(os.Environ(), nil), want); err != nil {
		var refused *tasks.IsolationError
		if !errors.As(err, &refused) {
			return fmt.Errorf("service: asserting the kept copy at %s: %w", entry.CopyPath, err)
		}
		if refused.Comparison != tasks.ComparisonRemotes && refused.Comparison != tasks.ComparisonHead {
			return refuse(fmt.Sprintf("its copy is not one a task's worker may be started in (%s): %s", refused.Comparison, refused.Detail))
		}
		// The remote set and HEAD are what the reshape writes, so each is
		// read on its own here: the assertion stops at the first.
		if why := unreshapeableRemotes(obs, plan); why != "" {
			return refuse(fmt.Sprintf("its copy is not one a task's worker may be started in (%s): %s", tasks.ComparisonRemotes, why))
		}
		if why, err := s.unreshapeableHead(ctx, entry.CopyPath, obs, plan); err != nil {
			return fmt.Errorf("service: reading the branches of the kept copy at %s: %w", entry.CopyPath, err)
		} else if why != "" {
			return refuse(fmt.Sprintf("its copy is not one a task's worker may be started in (%s): %s", tasks.ComparisonHead, why))
		}
	}
	return nil
}

// unreshapeableRemotes is why reshapeAdoptedCopy cannot leave the copy's
// remote set as dispatch asserts it, or "" when it can: it points the
// gate's remote at the gate, adding it when it is missing and setting its
// one URL when it stands, and removes no remote, so for a delivery copy
// any other remote, a push URL or a second URL stays, and for an
// investigation, which has none, every remote does. Removing one would
// delete its remote-tracking references, which can be the only name a
// commit in the copy has, so the person removes it or answers otherwise.
func unreshapeableRemotes(obs vcs.Observation, plan carryOn) string {
	if len(obs.Remotes) == 0 {
		return ""
	}
	names := make([]string, len(obs.Remotes))
	for i, r := range obs.Remotes {
		names[i] = r.Name
	}
	remove := "; carrying on removes no remote, since that deletes its remote-tracking references, so remove it from the copy " +
		"yourself and answer again, or land the copy"
	if plan.old.Shape == store.TaskInvestigation {
		return "an investigation's copy has no remote, and this one has " + strings.Join(names, ", ") + remove
	}
	only := obs.Remotes[0]
	if len(obs.Remotes) == 1 && only.Name == gate.RemoteName && len(only.URLs) == 1 && len(only.PushURLs) == 0 {
		return ""
	}
	return "a delivery copy has the one remote " + gate.RemoteName + " with one URL and no push URL, and this one has " +
		strings.Join(names, ", ") + remove
}

// unreshapeableHead is why reshapeAdoptedCopy cannot leave the copy's HEAD
// as dispatch asserts it, or "" when it can: an investigation's is
// detached wherever it stands, and a delivery copy's HEAD off the task's
// branch is put on it by creating the branch at HEAD. It is decided from
// every branch the copy holds: one of that name already standing refuses
// it, since moving it would leave behind whatever it holds, and so does
// one under that name or one that name sits under (task/kept/side or task
// beside task/kept), beside which the branch cannot be created, since
// carrying on renames no branch.
func (s *Service) unreshapeableHead(ctx context.Context, path string, obs vcs.Observation, plan carryOn) (string, error) {
	if plan.old.Shape != store.TaskDelivery || (!obs.Detached && obs.Branch == plan.branch) {
		return "", nil
	}
	copied, err := vcs.OpenWorktree(ctx, path, s.git()...)
	if err != nil {
		return "", err
	}
	refs, err := copied.ListRefs(ctx, "refs/heads/")
	if err != nil {
		return "", err
	}
	name := "refs/heads/" + plan.branch
	for _, ref := range refs {
		switch {
		case ref.Name == name:
			return "HEAD is not on the task's branch " + plan.branch + ", which stands at " + ref.Commit + ", and carrying on " +
				"moves no branch; put HEAD on it yourself and answer again, or land the copy", nil
		case strings.HasPrefix(ref.Name, name+"/") || strings.HasPrefix(name, ref.Name+"/"):
			return "HEAD is not on the task's branch " + plan.branch + ", which carrying on creates at HEAD, and the copy's " +
				ref.Name + " stands in the way of that name, and carrying on renames no branch; rename it yourself and answer " +
				"again, or land the copy", nil
		}
	}
	return "", nil
}

// assertReshaped asserts the adopted copy where it now stands, at the new
// task's path, on the new task's expectation, as dispatch will before it
// launches the worker, so a carry on is never marked over a copy dispatch
// would refuse: a refusal is returned with ipc.ErrRefused, and the entry
// stays owed and the task held on it.
func (s *Service) assertReshaped(ctx context.Context, entry store.KeptWork, id, path string, plan carryOn) error {
	want, _, err := tasks.Expect(s.home, id, plan.old.Shape, plan.gatePath, plan.repository.WorkingPath, plan.branch)
	if err != nil {
		return err
	}
	obs, err := vcs.Observe(ctx, path, s.git()...)
	if err != nil {
		return fmt.Errorf("%w: kept work entry %d was not carried on: the copy at %s cannot be read as a repository: %w",
			ipc.ErrRefused, entry.ID, path, err)
	}
	if _, err := tasks.AssertIsolated(obs, agents.WorkerEnvironment(os.Environ(), nil), want); err != nil {
		var refused *tasks.IsolationError
		if !errors.As(err, &refused) {
			return fmt.Errorf("service: asserting the carried-on copy at %s: %w", path, err)
		}
		return fmt.Errorf("%w: kept work entry %d was not carried on: once shaped, its copy at %s is not one task %s's worker "+
			"may be started in (%s): %s", ipc.ErrRefused, entry.ID, path, id, refused.Comparison, refused.Detail)
	}
	return nil
}

// completeCarryOn carries out the carry-on claim the entry holds, on this
// file's terms, from whatever an earlier call left. The claim is the one
// the entry holds now, read under the task's dispatch slot, never one the
// caller read before: a claim withdrawn and claimed again since names
// another task, and carrying the old one out would create a task and move
// the copy for a claim that no longer stands.
func (s *Service) completeCarryOn(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	// Read first: another call may have withdrawn the claim the caller read
	// and a later one claimed another task, or another entry of the same copy
	// completed may have marked this one.
	entry, err := s.store.KeptWorkEntry(ctx, entry.ID)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	id, ok := entry.CarriedOnTask.Get()
	switch {
	case ok && entry.CarriedOnAt.IsKnown():
		return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkCarriedOn, Task: id}, nil
	case entry.Answered() || entry.Closure.IsKnown():
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	case !entry.CarryOnClaimed():
		// Withdrawn by another call in between.
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: the carry on of kept work entry %d was withdrawn; the entry is open",
			ipc.ErrRefused, entry.ID)
	}
	done := func(k store.KeptWork) machine.KeptWorkAnswered {
		return machine.KeptWorkAnswered{Entry: k, Effect: machine.KeptWorkCarriedOn, Task: id}
	}
	release, err := s.claimCarryOnSlot(id)
	if errors.Is(err, errSlotSettled) {
		// Name the entry only as the person did on --hold (entry.Key), with
		// the phrase row; no task identifier and no internal mechanic (P13).
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: %s: %s", ipc.ErrRefused, entry.Key(), machine.PhraseCarryOnAlreadyStarted)
	}
	if errors.Is(err, errCarryOnSlotBusy) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: %s: %s", ipc.ErrRefused, entry.Key(), machine.PhraseCarryOnSlotBusy)
	}
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	defer func() {
		release()
		s.dispatchInBackground()
	}()
	// Read again under the slot: only a call holding this task's slot
	// withdraws its claim, so a claim that still names the task here stands
	// until this call marks or withdraws it.
	if entry, err = s.store.KeptWorkEntry(ctx, entry.ID); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if entry.CarriedOnAt.IsKnown() && entry.CarriedOnTask.Or("") == id {
		return done(entry), nil
	}
	if !entry.CarryOnClaimed() || entry.CarriedOnTask.Or("") != id {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: the carry on of kept work entry %d by task %s no longer stands; answer again",
			ipc.ErrRefused, entry.ID, id)
	}
	plan, err := s.carryOnOf(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	// The record, through intake, unless an earlier call wrote it; and then
	// intake's second step, the instructions file, which a stop between the
	// two left unwritten, completed by intake itself on a row that stands
	// without it and left alone where it stands.
	instructionsText := s.carryOnInstructions(plan.old)
	if _, err := s.store.Task(ctx, id); errors.Is(err, store.ErrNotFound) {
		if _, err := s.queue.Create(ctx, tasks.Request{
			ID: id, Shape: string(plan.old.Shape), Project: plan.old.Project, Mode: plan.old.Mode, Role: config.CarriedRole(plan.old.Role),
			Domain: plan.old.Domain, Repository: plan.repository.ID, Branch: plan.branch, DeliveryFloor: carriedPosture(plan),
			Instructions: instructionsText,
		}); err != nil {
			// The task could not be created, so nothing was moved: the
			// claim is withdrawn and the entry is open again, listed and
			// holding its project's dispatch as before. The store refuses
			// the withdrawal if a row was written all the same, which
			// leaves the claim standing for the next call to complete.
			if _, withdrawErr := s.store.WithdrawKeptWorkCarryOn(ctx, entry.ID, id); withdrawErr != nil {
				return machine.KeptWorkAnswered{}, fmt.Errorf("%w; the carry on of kept work entry %d stands, owed: %w",
					refusalOf(err), entry.ID, withdrawErr)
			}
			s.log.Printf("kept work entry %d was not carried on: task %s could not be created (%v); the claim was withdrawn", entry.ID, id, err)
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w; kept work entry %d was not carried on, and is open again", refusalOf(err), entry.ID)
		}
		s.log.Printf("task %s was created to carry kept work entry %d on from task %s", id, entry.ID, plan.old.ID)
	} else if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if _, err := s.queue.CompleteIntake(ctx, id, instructionsText); err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: completing the intake of task %s, which carries kept work entry %d on: %w", id, entry.ID, err)
	}
	// The copy, moved to the task's own path unless an earlier call moved it,
	// and asserted where it stands before the move.
	newPath, err := s.home.TaskCopy(id)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	_, moved := os.Lstat(newPath)
	if moved != nil && !errors.Is(moved, os.ErrNotExist) {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: reading the copy of task %s at %s: %w", id, newPath, moved)
	}
	// The copy is held to its record where it stands, at the reserved
	// task's path once an earlier call moved it and at the kept path
	// before, before anything of it is read or written: the one
	// verification every path asks.
	standing := entry.CopyPath
	if moved == nil {
		standing = newPath
	}
	if err := s.verifyCarriedCopy(ctx, entry, standing, plan); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if moved != nil {
		if err := s.assertKeptCopy(ctx, entry, plan); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := s.isCarriedOnCopy(entry, entry.CopyPath); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		// Read before the move, so a copy that has lost what it was kept
		// on is refused with nothing moved; the mark's set is the last
		// reading's, below, not this one's.
		if _, err := s.holdsKeptWork(ctx, entry, entry.CopyPath); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("service: creating the directory of task %s: %w", id, err)
		}
		if err := os.Rename(entry.CopyPath, newPath); err != nil {
			return machine.KeptWorkAnswered{}, fmt.Errorf("service: moving the kept copy to task %s: %w", id, err)
		}
		if s.betweenCarryOnMoveAndMark != nil {
			if err := s.betweenCarryOnMoveAndMark(); err != nil {
				return machine.KeptWorkAnswered{}, err
			}
		}
	} else if err := s.verifyAdopted(ctx, entry, newPath); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if err := s.reshapeAdoptedCopy(ctx, newPath, plan); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if err := s.assertReshaped(ctx, entry, id, newPath, plan); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	instructions, err := s.home.TaskInstructions(id)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if err := tasks.AppendNote(instructions, carryOnHeading, carryOnNote(entry, plan.old)); err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: writing the carry-on note into the instructions of task %s: %w", id, err)
	}
	// The last reading, of what the successor holds where it stands, is
	// made right before the write that closes the entries, so every step
	// between the move and the mark, the reshape, the assertion and the
	// note, is behind it, and the mark closes only entries a copy read
	// after all of them still held (round 47, P6: before, the set was the
	// reading's before the move, and work pruned from the moved copy in
	// between was closed over). A refusal leaves the copy at the task's
	// path and the claim standing owed, as verifyAdopted's does, and the
	// next answer or start reads again. What stands between this reading
	// and the write is the moment itself, as between the flip's reading
	// and the flip in a promotion.
	verified, err := s.holdsKeptWork(ctx, entry, newPath)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	marked, err := s.store.MarkKeptWorkCarriedOn(ctx, entry.ID, id, verified)
	if errors.Is(err, store.ErrKeptWorkUnverified) {
		// An entry of the copy was kept after the last reading; the copy
		// stands at the task's path and the claim stands owed, and the next
		// answer or start reads every entry again and marks then.
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not carried on yet: %w; the copy stands at %s for task %s "+
			"and the claim stands; answer again", ipc.ErrRefused, entry.ID, err, newPath, id)
	}
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.log.Printf("kept work entry %d was carried on: task %s owns the copy at %s", entry.ID, id, newPath)
	return done(marked), nil
}

// isCarriedOnCopy refuses the copy at path unless it is the one the
// carry-on answer read, by tasks.CopyIdentity, never by its path or what it
// holds: a copy put in the kept one's place, or at the new task's path, is
// another copy whatever commits it shares with the kept one.
func (s *Service) isCarriedOnCopy(entry store.KeptWork, path string) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: kept work entry %d was not carried on: %s", ipc.ErrRefused, entry.ID, why)
	}
	recorded, err := tasks.ParseCopyIdentity(entry.CarriedOnCopy.Or(""))
	if err != nil {
		return refuse("the answer records no copy it carries on that can be read: " + err.Error())
	}
	standing, err := tasks.IdentifyCopy(path)
	if err != nil {
		return refuse(err.Error())
	}
	if !standing.Same(recorded) {
		return refuse(fmt.Sprintf("the copy at %s is not the kept copy the answer was given for: it is another directory, whatever it holds", path))
	}
	return nil
}

// verifyAdopted refuses to take a copy already standing at the new task's
// path as the kept one unless all three hold: it is the copy the answer was
// given for by its identity (isCarriedOnCopy), which a clone put there is
// not however much history it shares; the kept copy's own path is gone,
// which a rename of it leaves; and the copy at the new path still holds
// every commit the entry recorded the kept copy's references and HEAD
// standing on, on holdsKeptWork's terms, the reading the answer and the
// completion before the move make of the copy too. Holding them alone
// proves nothing about which copy this is: a fresh clone of an upstream that
// has since received those commits holds them all and still lacks the kept
// copy's own work, which is why identity is checked first. Any of the three
// failing is refused with ipc.ErrRefused, the entry stays owed and the task
// stays held on it. The mark's set is the completion's last reading's,
// made right before the mark, not this one's.
func (s *Service) verifyAdopted(ctx context.Context, entry store.KeptWork, newPath string) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: kept work entry %d was not carried on: %s", ipc.ErrRefused, entry.ID, why)
	}
	if err := s.isCarriedOnCopy(entry, newPath); err != nil {
		return err
	}
	if _, err := os.Lstat(entry.CopyPath); err == nil {
		return refuse(fmt.Sprintf("the kept copy still stands at %s and something else stands at %s, so which is the kept work cannot be told by path",
			entry.CopyPath, newPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	_, err := s.holdsKeptWork(ctx, entry, newPath)
	return err
}

// holdsKeptWork refuses the copy at path, with ipc.ErrRefused naming the
// entry and what is missing, unless it still holds everything every open
// entry of the copy recorded it standing on, on tasks.Cleaner.Unheld's
// terms, the one reading of what a kept copy holds: each recorded commit
// or object is a value a name of the copy records, HEAD, a reference, a
// root reference or a reflog entry, or a commit one of those reaches. So
// a commit amended or reset away after the copy was kept is held while a
// reflog or ORIG_HEAD records it, a tag on a blob holds the blob, and a
// copy that has lost a recorded commit altogether, its reflog entries
// expired and the object pruned, no longer holds what it was kept on and
// is refused rather than recorded over: the person lands what the copy
// holds now or answers otherwise. Every open entry of the copy is read,
// not the answered one alone (store.OpenKeptWorkOfCopy, the entries the
// mark closes with it), since the mark answers them all with the one
// task and a copy that has lost what a later entry recorded would
// otherwise be carried on as if the successor received it (the final
// full review of ff040fc, P15). It is asked by the answer before its
// claim, by the completion before the move, by verifyAdopted after it,
// and last by the completion right before the mark, so every reading
// accepts and refuses the same copies (the final full review of feddd14,
// and the delta review of 9ef7722 for the names a reading of the copy's
// own left out). A copy that cannot be read is an error, never a
// refusal. It returns the entries it held the copy to, by identifier,
// which the last reading hands the mark (store.MarkKeptWorkCarriedOn): an
// entry kept after that reading is not among them, so the mark refuses to
// close it and the next answer reads it (the carry-on audit of b448115).
func (s *Service) holdsKeptWork(ctx context.Context, entry store.KeptWork, path string) ([]int64, error) {
	entries, err := s.store.OpenKeptWorkOfCopy(ctx, entry.CopyPath)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(entries, func(k store.KeptWork) bool { return k.ID == entry.ID }) {
		entries = append(entries, entry)
	}
	verified := make([]int64, 0, len(entries))
	for _, k := range entries {
		at := k
		at.CopyPath = path
		unheld, err := s.cleaner.Unheld(ctx, at)
		if err != nil {
			return nil, err
		}
		if len(unheld) > 0 {
			return nil, fmt.Errorf("%w: kept work entry %d was not carried on: the copy at %s no longer holds %s, which it stood on when it was "+
				"kept as entry %d, and carrying it on would close that entry too: nothing the copy names, HEAD, a reference, a root "+
				"reference or a reflog entry, is or reaches it; land what the copy holds now, or answer otherwise",
				ipc.ErrRefused, entry.ID, path, strings.Join(unheld, ", "), k.ID)
		}
		verified = append(verified, k.ID)
	}
	return verified, nil
}

// reshapeAdoptedCopy gives the adopted copy what dispatch gives a fresh
// clone and nothing else: for a delivery task the gate as its one remote
// and the task's branch at HEAD, and for an investigation a detached HEAD.
// A copy already shaped so is left as it is.
func (s *Service) reshapeAdoptedCopy(ctx context.Context, path string, plan carryOn) error {
	copied, err := vcs.OpenWorktree(ctx, path, s.git()...)
	if err != nil {
		return fmt.Errorf("service: opening the adopted copy at %s: %w", path, err)
	}
	obs, err := vcs.Observe(ctx, path, s.git()...)
	if err != nil {
		return fmt.Errorf("service: observing the adopted copy at %s: %w", path, err)
	}
	head, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("service: reading HEAD of the adopted copy at %s: %w", path, err)
	}
	switch plan.old.Shape {
	case store.TaskDelivery:
		if err := copied.SetRemote(ctx, gate.RemoteName, plan.gatePath); err != nil {
			return fmt.Errorf("service: pointing %s of the adopted copy at the gate: %w", gate.RemoteName, err)
		}
		if obs.Detached || obs.Branch != plan.branch {
			if err := copied.SwitchToNewBranch(ctx, plan.branch, head); err != nil {
				return fmt.Errorf("service: creating the branch %s at %s in the adopted copy: %w", plan.branch, head, err)
			}
		}
	case store.TaskInvestigation:
		if !obs.Detached {
			if err := copied.Detach(ctx, head); err != nil {
				return fmt.Errorf("service: detaching the adopted copy at %s: %w", head, err)
			}
		}
	}
	return nil
}

// carriedPosture is the floor the new task is asked to be recorded under,
// as intake's request text (tasks.Request.DeliveryFloor), or "" for none:
// the posture the outlived task's own push is held to, which intake
// records unless the project's, read once in the same intake, is
// stricter, in which case it records the project's. So the continued work
// is held to what the outlived task was held to and never to less, and to
// the project's when that has tightened since, decided against one read
// of the project's posture, never two (the delta review of 9ef7722: a read
// here and another in intake let a project loosened between them put the
// successor below the outlived task's). What the outlived task was held
// to is read as its push is (localPosture): local for a task recorded
// local, and gated for any other delivery task, one recorded before
// intake wrote a posture included, whose push the gate takes as gated (the
// carry-on audit of b448115: that task's successor was recorded under a
// project's local). An investigation records no posture and asks for
// none.
func carriedPosture(plan carryOn) string {
	if plan.old.Shape != store.TaskDelivery {
		return ""
	}
	if localPosture(plan.old) {
		return string(config.DeliveryLocal)
	}
	return string(config.DeliveryGated)
}

// carryOnInstructions is what the new task is created with: what the
// outlived task was last asked to do. For a promoted task that is the
// intent its promotion recorded, store.Task.DeliveryIntent, and nothing of
// the investigation's intake text, which the promotion replaced and which
// may forbid the very change the delivery is for; the new task is a
// delivery task created through intake, whose intake text is its intent,
// so its runs carry the promotion's intent as the outlived task's did
// (taskIntent). For any other task it is the text the coordinator wrote at
// intake, as intakeInstructions reads it, or a line saying what the task is
// for when that cannot be read.
func (s *Service) carryOnInstructions(old store.Task) string {
	if intent, ok := old.DeliveryIntent.Get(); ok && strings.TrimSpace(intent) != "" {
		return strings.TrimSpace(intent) + "\n"
	}
	path, err := s.home.TaskInstructions(old.ID)
	if err == nil {
		var raw []byte
		if raw, err = os.ReadFile(path); err == nil {
			if text := intakeInstructions(string(raw)); text != "" {
				return text
			}
		}
	}
	if err != nil {
		s.log.Printf("the instructions of task %s could not be read for the task carrying it on: %v", old.ID, err)
	}
	return "Carry on the work kept in this copy from task " + old.ID + "."
}

// carryOnNote is the note PRD section 6 has the worker told, saying what is
// already in the copy: which task's work it is, why it was kept, and the
// commits the entry recorded it holding.
func carryOnNote(entry store.KeptWork, old store.Task) string {
	holds := "no commit of its own"
	if len(entry.Holds) > 0 {
		holds = "the commits " + strings.Join(entry.Holds, ", ")
	}
	return carryOnHeading + "\n\n" +
		"You were started in the working copy of an earlier task, " + old.ID + ", whose work had not landed when it " +
		"ended (" + entry.Reason + "), so the copy was kept rather than discarded and you are to carry that work on. " +
		"When it was kept it held " + holds + ". Read the copy's history and tree before you change anything, and continue " +
		"from what is there rather than starting over.\n"
}

// completeCarryOnsAtOpen completes every carry-on answer a stopped service
// recorded and did not carry out, on this file's terms, and logs what it
// could not.
func (s *Service) completeCarryOnsAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkCarryOnsOwed(ctx)
	if err != nil {
		s.log.Printf("the kept work carry-ons owed could not be read at open: %v", err)
		return
	}
	for _, entry := range owed {
		if _, err := s.completeCarryOn(ctx, entry); err != nil {
			s.log.Printf("kept work entry %d holds a carry-on claim that could not be completed: %v", entry.ID, err)
		}
	}
}
