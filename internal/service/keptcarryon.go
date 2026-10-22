package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
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
// of the task it outlived. A refusal on the remote set or HEAD is tolerated
// only as far as the reshape below repairs it, each read on its own: for a
// delivery copy, a missing gate remote or one whose one URL is elsewhere,
// which it points at the gate, and HEAD off the task's branch while no
// branch of that name stands, which it creates at HEAD; for an
// investigation, an attached HEAD, which it detaches. Anything else on
// either, an investigation's remote, a delivery copy's second remote, a
// push URL or a second URL, or the task's branch standing away from HEAD,
// refuses the carry on with nothing recorded (unreshapeableRemotes,
// unreshapeableHead): the reshape removes no remote and moves no branch,
// since either can leave a commit with no name, and P6 has the program
// refuse rather than risk that, so the person repairs the copy or answers
// otherwise. So does any other refusal, a copy that is not a clone of its
// own, one that is the primary or inside it, among them. A copy that is
// gone refuses it too. After the reshape the copy is asserted again at its
// new path on the new task's expectation, as dispatch will assert it, and
// a refusal there leaves the entry owed rather than marked
// (assertReshaped).
//
// A run's copy is refused before that: it is a linked worktree of the gate,
// which a task's copy cannot be, and a person's branch, which intake's branch
// rule refuses a task. Carrying a run's copy on is therefore not in this
// build, and the refusal says so.
//
// # A failed adoption, and every entry of the copy
//
// The answer names the new task, and until the task owns the copy it is
// held on the entries it carries on, by internal/store's hold predicate: an
// adoption that fails, a rename the old task's directory refuses among the
// ways, leaves the answer owed and the task undispatched, so no dispatch
// cuts it a fresh clone without the kept work. Completion takes a copy that
// already stands at the new task's path only when it is the kept one
// (verifyAdopted): the copy the answer was given for by its file identity,
// tasks.CopyIdentity, which the answer records, with the old path gone and
// every commit the entry recorded still contained in it. Containment alone
// is not identity: a fresh clone of an upstream that took the recorded
// commits up contains them all and lacks the copy's own work. The kept
// copy is checked to be that copy before it is moved, too. Carrying on moves the whole
// copy, so the answer carries on every open entry of the same copy, the copy
// kept again at another state, in the same write, and the mark marks them
// all.
//
// # The order, and what a stop leaves
//
// The identifier is minted first and the answer recorded with it,
// store.AnswerKeptWorkCarryOn, which closes the entry, so the new task is
// not held by it. completeCarryOn then takes the task's dispatch slot for
// that identifier before anything else, so no dispatch pass cuts a fresh
// clone at the path the copy is about to stand at; between the answer and
// the slot no task row exists yet, so no pass can find the task to
// dispatch. Then, in the order intake keeps, the task is created with that
// identifier, the copy is moved to the task's own path and reshaped as
// dispatch shapes a fresh clone, the note is appended to the instructions
// file, and the entry is marked carried on. Every step after the answer is
// idempotent on what it finds (the row stands, the instructions file
// stands, the copy stands at the new path, the note is in the file), so a
// service that stops anywhere in it is completed by the next call or the
// next start from the entry alone, which names the task:
// completeCarryOnsAtOpen reads every carry-on not marked. A stop before the
// task's row is written leaves an entry naming a task that does not exist
// yet, which the completion creates; a stop between the row and the
// instructions file, inside intake, leaves a row with no file, which
// intake's own CompleteIntake writes before the copy is moved; a stop after
// the move leaves the copy at the new path, which it finds. What is not
// recovered: a copy gone from both paths, which is reported and left.
//
// Once the slot is given back a dispatch pass is asked for, and the task
// dispatches over its standing copy on dispatch.go's terms: swept, asserted
// again at its own path, and launched with the instructions file, note
// included. Other open entries of the project still hold it, as they hold
// any new task.

// carryOnHeading is the heading the carry-on note is appended to the
// instructions under, and the marker that keeps the append idempotent.
const carryOnHeading = "## Carried on, written by the program"

// carryOnKeptWork is the carry-on answer, on this file's terms.
func (s *Service) carryOnKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptCarryOn {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if entry.Open() {
		plan, err := s.carryOnOf(ctx, entry)
		if err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := s.assertKeptCopy(ctx, entry, plan); err != nil {
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
		answered, err := s.store.AnswerKeptWorkCarryOn(ctx, entry.ID, id, kept.String(), req.HoldResolver())
		switch {
		case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed):
			again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
			if readErr != nil {
				return machine.KeptWorkAnswered{}, readErr
			}
			if again.Answer.Or("") != store.KeptCarryOn {
				return machine.KeptWorkAnswered{}, refusedAnswered(again)
			}
			entry = again
		case err != nil:
			return machine.KeptWorkAnswered{}, err
		default:
			entry = answered
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

// assertKeptCopy asserts the kept copy where it stands against the
// expectation of the task it outlived, on this file's terms, and refuses
// with ipc.ErrRefused a copy that is gone or that fails any comparison but
// the two the reshape writes.
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
	obs, err := vcs.Observe(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			if _, statErr := os.Lstat(entry.CopyPath); errors.Is(statErr, os.ErrNotExist) {
				return refuse("its copy at " + entry.CopyPath + " is gone")
			}
			return refuse(fmt.Sprintf("its copy at %s is not a repository: %v", entry.CopyPath, err))
		}
		return fmt.Errorf("service: observing the kept copy at %s: %w", entry.CopyPath, err)
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
		if why, err := unreshapeableHead(ctx, entry.CopyPath, obs, plan); err != nil {
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
		"yourself and answer again, or land or discard the copy"
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
// branch is put on it by creating the branch at HEAD, which a branch of
// that name already standing refuses, since moving it would leave behind
// whatever it holds.
func unreshapeableHead(ctx context.Context, path string, obs vcs.Observation, plan carryOn) (string, error) {
	if plan.old.Shape != store.TaskDelivery || (!obs.Detached && obs.Branch == plan.branch) {
		return "", nil
	}
	copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return "", err
	}
	refs, err := copied.ListRefs(ctx, "refs/heads/"+plan.branch)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+plan.branch {
			return "HEAD is not on the task's branch " + plan.branch + ", which stands at " + ref.Commit + ", and carrying on " +
				"moves no branch; put HEAD on it yourself and answer again, or land or discard the copy", nil
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
	obs, err := vcs.Observe(ctx, path, vcs.WithRedactor(redact.New()))
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

// completeCarryOn carries out a carry-on answer already recorded on the
// entry, on this file's terms, from whatever an earlier call left.
func (s *Service) completeCarryOn(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	id, ok := entry.CarriedOnTask.Get()
	if !ok {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: kept work entry %d is answered carry-on and names no task", entry.ID)
	}
	done := func(k store.KeptWork) machine.KeptWorkAnswered {
		return machine.KeptWorkAnswered{Entry: k, Effect: machine.KeptWorkCarriedOn, Task: id}
	}
	// Read again: another entry of the same copy completed may have marked
	// this one with it.
	if again, err := s.store.KeptWorkEntry(ctx, entry.ID); err == nil {
		entry = again
	} else {
		return machine.KeptWorkAnswered{}, err
	}
	if entry.CarriedOnAt.IsKnown() {
		return done(entry), nil
	}
	settle, err := s.claimDispatch(id)
	if err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d is being carried on by task %s now", ipc.ErrRefused, entry.ID, id)
	}
	_ = settle
	defer func() {
		s.mu.Lock()
		delete(s.isolated, id)
		delete(s.claiming, id)
		s.mu.Unlock()
		s.dispatchInBackground()
	}()
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
			ID: id, Shape: string(plan.old.Shape), Project: plan.old.Project, Mode: plan.old.Mode, Role: plan.old.Role,
			Domain: plan.old.Domain, Repository: plan.repository.ID, Branch: plan.branch,
			Instructions: instructionsText,
		}); err != nil {
			return machine.KeptWorkAnswered{}, refusalOf(err)
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
	if _, err := os.Lstat(newPath); errors.Is(err, os.ErrNotExist) {
		if err := s.assertKeptCopy(ctx, entry, plan); err != nil {
			return machine.KeptWorkAnswered{}, err
		}
		if err := s.isCarriedOnCopy(entry, entry.CopyPath); err != nil {
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
	} else if err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: reading the copy of task %s at %s: %w", id, newPath, err)
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
	if err := appendOnce(instructions, carryOnHeading, carryOnNote(entry, plan.old)); err != nil {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: writing the carry-on note into the instructions of task %s: %w", id, err)
	}
	marked, err := s.store.MarkKeptWorkCarriedOn(ctx, entry.ID)
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
// which a rename of it leaves; and the copy at the new path still contains
// every commit the entry recorded the kept copy's references and HEAD
// standing on, each reachable from its HEAD or one of its references.
// Containment alone proves nothing about which copy this is: a fresh clone of
// an upstream that has since received those commits contains them all and
// still lacks the kept copy's own work, which is why identity is checked
// first. Reachable and not a tip, since the kept copy may have gone on to
// commits past those after it was kept, and its references moving forward is
// the copy carrying on, not another copy. Any of the three failing is refused
// with ipc.ErrRefused, the entry stays owed and the task stays held on it.
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
	adopted, err := vcs.OpenWorktree(ctx, newPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return refuse(fmt.Sprintf("what stands at %s is not a copy: %v", newPath, err))
	}
	inv, err := adopted.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("service: reading the copy at %s: %w", newPath, err)
	}
	tips := []string{}
	if inv.Head.Commit != "" {
		tips = append(tips, inv.Head.Commit)
	}
	for _, ref := range inv.Refs {
		tips = append(tips, ref.Commit)
	}
	for _, h := range entry.Holds {
		contained := false
		for _, tip := range tips {
			reaches, err := adopted.IsAncestor(ctx, h, tip)
			if err != nil && !errors.Is(err, vcs.ErrRefNotFound) {
				return fmt.Errorf("service: reading whether the copy at %s contains %s: %w", newPath, h, err)
			}
			if err == nil && reaches {
				contained = true
				break
			}
		}
		if !contained {
			return refuse(fmt.Sprintf("the copy at %s does not contain %s, which the kept copy stood on, so it is not the kept work", newPath, h))
		}
	}
	return nil
}

// reshapeAdoptedCopy gives the adopted copy what dispatch gives a fresh
// clone and nothing else: for a delivery task the gate as its one remote
// and the task's branch at HEAD, and for an investigation a detached HEAD.
// A copy already shaped so is left as it is.
func (s *Service) reshapeAdoptedCopy(ctx context.Context, path string, plan carryOn) error {
	copied, err := vcs.OpenWorktree(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return fmt.Errorf("service: opening the adopted copy at %s: %w", path, err)
	}
	obs, err := vcs.Observe(ctx, path, vcs.WithRedactor(redact.New()))
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

// carryOnInstructions is what the new task is created with: the text the
// coordinator wrote at the outlived task's intake, as intakeInstructions
// reads it, or a line saying what the task is for when that cannot be read.
func (s *Service) carryOnInstructions(old store.Task) string {
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

// appendOnce appends text to the file at path unless the file already
// carries heading at the start of a line.
func appendOnce(path, heading, text string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.HasPrefix(string(raw), heading) || strings.Contains(string(raw), "\n"+heading) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n\n" + text); err != nil {
		return err
	}
	return f.Sync()
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
			s.log.Printf("kept work entry %d was answered carry-on and its carry on could not be completed: %v", entry.ID, err)
		}
	}
}
