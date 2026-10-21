package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Request is what the coordinator asks intake for. Choosing the shape,
// writing the instructions and claiming an overlap are its judgment (PRD
// section 7); everything the program decides from them is Create's.
type Request struct {
	// ID is the identifier to record the task under, for a caller that
	// reserved it with home.NewTaskID before intake because it holds the
	// task's copy ready at home.TaskCopy; empty means intake mints one. An
	// identifier home.ValidTaskID refuses is refused by the store.
	ID string
	// Shape is the task's shape as text, decoded against store.TaskShape's
	// closed set at this edge and refused outside it.
	Shape string
	// Project is what the task is for.
	Project string
	// Instructions is what the worker is launched with. It is written under
	// the task's directory and refused empty: a task with nothing to do is
	// not one.
	Instructions string
	// Mode is how the worker is to be launched, in the caller's notation. It
	// is recorded as given and decides nothing here. It is the launch
	// notation and not the delivery posture, which is Delivery.
	Mode string
	// Delivery is a delivery posture the coordinator asks to record in place
	// of the project's, as text decoded by config.ParseDelivery, or empty to
	// record the project's own. It may be stricter than the project's and
	// never lighter, which is a *PostureError. It is refused on an
	// investigation task, which has no posture.
	Delivery string
	// Repository names the repository the task works in. A delivery task
	// must name one; an investigation task may, so its copy is cut from the
	// right primary, and may name none.
	Repository string
	// Branch is the branch a delivery task works on. It is required for a
	// delivery task and refused on an investigation task, which creates no
	// branch.
	Branch string
	// Overlaps is the coordinator's claim that this task overlaps the named
	// task's live work, or empty for a claim of independence. A named task is
	// queued behind, whatever its state; the floor Create applies can queue a
	// task past a claim of independence but never dispatch one past a claim
	// of overlap: the record holds one blocker, and when both apply Release
	// waits on whichever is still live before it dispatches.
	Overlaps string
	// Role is the configured role the worker runs under. Empty means the
	// worker role. review, fix, a name that is not a role name, and a name
	// no configured role has are refused and the task is not created.
	Role string
	// Domain is the domain the task is written into, PRD section 8's domain
	// column, and empty for the coordinator's own. It is never on the wire:
	// internal/service sets it from the caller's placement when the caller
	// is a domain's subordinate coordinator, and no request field names it,
	// which is what keeps a task's domain the service's fact and not a
	// caller's claim. A name home.ValidDomain refuses is refused with
	// ErrRequest.
	Domain string
}

// Cause says why a task was queued.
type Cause string

const (
	// CauseIndependent is the task was dispatched: nothing queued it.
	CauseIndependent Cause = "independent"
	// CauseClaimed is the coordinator named the blocker.
	CauseClaimed Cause = "claimed"
	// CauseFloor is a live task held the branch, whatever the coordinator
	// claimed.
	CauseFloor Cause = "floor"
)

// Created is what intake recorded and why.
type Created struct {
	// Task is the record as stored. Its BlockerTask is known exactly when
	// the task was queued.
	Task store.Task
	// Cause says whether it was queued and by whose decision.
	Cause Cause
	// BlockerState is the blocker's state as intake read it, so a coordinator
	// told its task is queued can see what it waits on, a stuck Unknown
	// blocker among them. It is empty for an independent task.
	BlockerState taskstate.State
	// Instructions is the file the instructions were written to.
	Instructions string
}

// Create is intake: it records a new task with the disposition PRD section 6
// gives it, Queued behind a blocker or Dispatched, and writes its
// instructions. It cuts no copy and launches nothing; the copy path it records
// is a reservation, home.TaskCopy, that internal/service's dispatch cuts and
// asserts.
//
// The disposition is the coordinator's claim with one mechanical floor. A
// delivery task whose repository and branch equal a live task's is queued
// behind the newest live one, whatever req.Overlaps says, because two live
// tasks on one branch share their runs and neither's state could then be
// trusted. Live is what Live says. The floor is decided and written as one
// step: the read of the branch's tasks and the creation run under Exclude,
// and the creation is anchored to the newest task row the read saw, so an
// intake that read the branch cannot be overtaken between the two (P6). A
// claim of overlap on any task is honoured as given, including a task that
// has already cleared, which Release then dispatches past. The record names
// one blocker: the claimed task while it is live, otherwise the floor's, and
// Release re-runs the floor before it dispatches, so a claim and the floor
// are both waited on when both apply.
//
// A delivery task's branch must be a task's to take: a branch no task has
// ever worked on that already exists in the gate, or already has a run, is a
// person's, and the intake is refused with a *BranchTakenError. That check
// runs only for a branch no task names, because a branch a task has worked on
// has runs and a gate ref by design; a person who pushes on a task's branch
// is outside the floor, and nothing here tells their push from the task's.
//
// The record is written before the instructions file and the task's
// directory, which is the order PRD section 8 gives a run's row and its
// directory. A crash between the two leaves a recorded task with no
// instructions file, which the dispatch finds rather than this package; the
// reverse, a directory with no record, cannot happen. Until the file is
// written, a reader of the records sees the same gap without any crash: a
// task recorded independent already resolves Dispatched, so a caller that
// dispatches from the records keeps its read out of its own intakes.
//
// Refusals: a malformed request wraps ErrRequest; an unknown repository or
// overlap task wraps store.ErrNotFound; a branch moved under the intake is a
// *store.TaskMovedError, which the exclusion makes unreachable from two
// intakes of one service and which a caller then reads again and decides
// again.
func (q *Queue) Create(ctx context.Context, req Request) (Created, error) {
	shape, err := decodeShape(req.Shape)
	if err != nil {
		return Created{}, err
	}
	if strings.TrimSpace(req.Project) == "" {
		return Created{}, fmt.Errorf("%w: no project", ErrRequest)
	}
	if strings.TrimSpace(req.Instructions) == "" {
		return Created{}, fmt.Errorf("%w: no instructions", ErrRequest)
	}
	if _, err := q.roles.ForTask(req.Role); err != nil {
		return Created{}, fmt.Errorf("%w: %w", ErrRequest, err)
	}
	if req.Domain != "" {
		if err := home.ValidDomain(req.Domain); err != nil {
			return Created{}, fmt.Errorf("%w: %w", ErrRequest, err)
		}
	}
	branch := strings.TrimSpace(req.Branch)
	switch shape {
	case store.TaskDelivery:
		if strings.TrimSpace(req.Repository) == "" {
			return Created{}, fmt.Errorf("%w: a delivery task names a repository", ErrRequest)
		}
		if branch == "" {
			return Created{}, fmt.Errorf("%w: a delivery task names a branch", ErrRequest)
		}
		if reason := branchNameRefusal(branch); reason != "" {
			return Created{}, fmt.Errorf("%w: branch %q %s", ErrRequest, branch, reason)
		}
	case store.TaskInvestigation:
		if branch != "" {
			return Created{}, fmt.Errorf("%w: an investigation task creates no branch, and %q was asked for", ErrRequest, branch)
		}
		if strings.TrimSpace(req.Delivery) != "" {
			return Created{}, fmt.Errorf("%w: an investigation task delivers nothing and has no delivery posture, and %q was asked for",
				ErrRequest, req.Delivery)
		}
	}
	var requested config.Delivery
	if text := strings.TrimSpace(req.Delivery); text != "" {
		if requested, err = config.ParseDelivery(text); err != nil {
			return Created{}, fmt.Errorf("%w: %w", ErrRequest, err)
		}
	}
	var repository store.Repository
	if req.Repository != "" {
		if repository, err = q.store.Repository(ctx, req.Repository); err != nil {
			return Created{}, fmt.Errorf("tasks: the repository the task works in: %w", err)
		}
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		if id, err = home.NewTaskID(); err != nil {
			return Created{}, err
		}
	}
	copyPath, err := q.home.TaskCopy(id)
	if err != nil {
		return Created{}, err
	}
	task := store.Task{
		ID: id, Shape: shape, Project: req.Project, Mode: req.Mode, Role: strings.TrimSpace(req.Role),
		WorktreePath: copyPath, Domain: req.Domain,
	}
	if req.Repository != "" {
		task.RepositoryID = store.Known(repository.ID)
	}
	if shape == store.TaskDelivery {
		task.Branch = store.Known(branch)
		posture, err := q.recordedPosture(ctx, repository, id, requested)
		if err != nil {
			return Created{}, err
		}
		task.Delivery = store.Known(posture)
	}

	var created store.Task
	var cause Cause
	var blockerState taskstate.State
	decide := func() error {
		resolved, err := q.reader.All(ctx)
		if err != nil {
			return err
		}
		disposition, c, state, err := q.dispose(ctx, task, req.Overlaps, repository, resolved)
		if err != nil {
			return err
		}
		if q.betweenReadAndWrite != nil {
			q.betweenReadAndWrite()
		}
		created, err = q.store.CreateTask(ctx, task, disposition)
		cause, blockerState = c, state
		return err
	}
	if shape == store.TaskDelivery {
		release, err := q.hold(ctx, repository.ID, branch)
		if err != nil {
			return Created{}, err
		}
		err = decide()
		release()
		if err != nil {
			return Created{}, err
		}
	} else if err := decide(); err != nil {
		return Created{}, err
	}

	if q.betweenRecordAndInstructions != nil {
		if err := q.betweenRecordAndInstructions(); err != nil {
			return Created{}, err
		}
	}
	instructions, err := q.writeInstructions(id, req.Instructions)
	if err != nil {
		return Created{}, err
	}
	return Created{Task: created, Cause: cause, BlockerState: blockerState, Instructions: instructions}, nil
}

// CompleteIntake is intake's second step, the instructions file under the
// task's directory, for a task whose row Create wrote and whose file a stop
// left unwritten: it writes text there when no file stands and leaves a
// file that does as it is, and returns the file's path. A file that stands
// is whole, because intake writes it through writeWhole, to a partial file
// of a fresh name and then a rename, so a stop inside the write leaves
// nothing under the file's name and this writes it; a file under the name
// is never a partial one, and every partial file beside it is removed here,
// as a name and never through a link. An instructions file that stands as a
// symbolic link is refused with an *InstructionsLinkError, neither accepted
// nor written through nor renamed over, and nothing is written. A task with no row is store.ErrNotFound, wrapped, since the
// record comes first and this writes no record. Its one caller is internal/service's carry on of kept
// work, which creates the task under an identifier it reserved and
// completes a stopped intake from the entry at the next start.
func (q *Queue) CompleteIntake(ctx context.Context, id, text string) (string, error) {
	if _, err := q.store.Task(ctx, id); err != nil {
		return "", err
	}
	path, err := q.home.TaskInstructions(id)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return "", &InstructionsLinkError{Path: path}
	case err == nil:
		// The file is whole; a partial file a stop left beside it is
		// removed, since nothing else will.
		if err := removePartials(path); err != nil {
			return "", fmt.Errorf("tasks: removing the partial instructions of task %s: %w", id, err)
		}
		return path, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("tasks: reading the instructions of task %s: %w", id, err)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%w: no instructions", ErrRequest)
	}
	return q.writeInstructions(id, text)
}

// recordedPosture is the posture a delivery task is recorded under, at
// intake or at promotion, which are its two callers: the project's, read
// through the posture reader, or the coordinator's request when it asked for
// one no lighter. The project's is read whether or not a request was made,
// because "never lighter" is a comparison against it and a request cannot
// stand in for a read that failed: an unreadable trusted copy refuses with
// ErrPostureUnreadable, and a request no stricter than nothing is not one
// this can establish.
func (q *Queue) recordedPosture(ctx context.Context, repository store.Repository, id string,
	requested config.Delivery) (config.Delivery, error) {
	project, err := q.posture(ctx, repository, id)
	if err != nil {
		return "", fmt.Errorf("%w: repository %s: %w", ErrPostureUnreadable, repository.ID, err)
	}
	if !project.Recognized() {
		return "", fmt.Errorf("%w: repository %s declares %q, which is not a posture this build defines",
			ErrPostureUnreadable, repository.ID, project)
	}
	if requested == "" {
		return project, nil
	}
	if requested.Lighter(project) {
		return "", &PostureError{Requested: requested, Project: project}
	}
	return requested, nil
}

// dispose decides a new task's disposition from the tasks it read. The claim
// is checked first, so an unknown one is refused whatever the branch holds;
// then one blocker is chosen, since the record holds one: the claimed task
// while it is live, otherwise the newest live task on the branch, otherwise
// the claimed task as given. Release re-checks the branch before it
// dispatches, which is what keeps the choice from dropping the other.
func (q *Queue) dispose(ctx context.Context, task store.Task, claim string, repository store.Repository,
	resolved []taskstate.Resolved) (store.Disposition, Cause, taskstate.State, error) {
	var d store.Disposition
	var claimed *taskstate.Resolved
	if claim != "" {
		for i := range resolved {
			if resolved[i].Record.ID == claim {
				claimed = &resolved[i]
				break
			}
		}
		if claimed == nil {
			return d, "", "", fmt.Errorf("tasks: the task %q claimed as overlapping: %w", claim, store.ErrNotFound)
		}
	}
	var floor *taskstate.Resolved
	if branch, ok := task.Branch.Get(); ok {
		onBranch := 0
		for i := range resolved {
			r := &resolved[i]
			if r.Record.RepositoryID != task.RepositoryID || r.Record.Branch.Or("") != branch {
				continue
			}
			onBranch++
			d.Observed = max(d.Observed, r.Record.Row)
			if Live(r.State.State) && (floor == nil || r.Record.Row > floor.Record.Row) {
				floor = r
			}
		}
		if onBranch == 0 {
			if err := q.refuseAPersonsBranch(ctx, repository, branch); err != nil {
				return d, "", "", err
			}
		}
	}
	switch {
	case claimed != nil && Live(claimed.State.State):
		d.Blocker = store.Known(claim)
		return d, CauseClaimed, claimed.State.State, nil
	case floor != nil:
		d.Blocker = store.Known(floor.Record.ID)
		return d, CauseFloor, floor.State.State, nil
	case claimed != nil:
		d.Blocker = store.Known(claim)
		return d, CauseClaimed, claimed.State.State, nil
	}
	return d, CauseIndependent, "", nil
}

// refuseAPersonsBranch refuses a branch that exists in the gate or has a run.
// It is asked only about a branch no task names.
func (q *Queue) refuseAPersonsBranch(ctx context.Context, repository store.Repository, branch string) error {
	taken, err := q.inGate(ctx, repository, branch)
	if err != nil {
		return fmt.Errorf("tasks: reading whether %s is in the gate of %s: %w", branch, repository.ID, err)
	}
	if taken {
		return &BranchTakenError{Repository: repository.ID, Branch: branch, Held: HeldByGate}
	}
	runs, err := q.store.RunsForRepository(ctx, repository.ID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Branch == branch {
			return &BranchTakenError{Repository: repository.ID, Branch: branch, Held: HeldByRun, Run: run.ID}
		}
	}
	return nil
}

// writeInstructions writes the instructions under the task's directory and
// returns the file's path.
func (q *Queue) writeInstructions(id, text string) (string, error) {
	path, err := q.home.TaskInstructions(id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("tasks: creating the directory of task %s: %w", id, err)
	}
	if err := writeWhole(path, []byte(text)); err != nil {
		return "", fmt.Errorf("tasks: writing the instructions of task %s: %w", id, err)
	}
	return path, nil
}

// ErrInstructionsLink matches every *InstructionsLinkError.
var ErrInstructionsLink = errors.New("tasks: a task's instructions file is a symbolic link")

// InstructionsLinkError is the refusal to write or accept a task's
// instructions file that stands as a symbolic link: writing through it
// would write where the link points, and renaming over it would put the
// instructions where a link was planted, so neither is done and nothing is
// written.
type InstructionsLinkError struct {
	// Path is the instructions file's path.
	Path string
}

// Error names the path.
func (e *InstructionsLinkError) Error() string {
	return fmt.Sprintf("tasks: %s is a symbolic link, and a task's instructions are written to a file of their own and never through one", e.Path)
}

// Unwrap makes the refusal match ErrInstructionsLink.
func (e *InstructionsLinkError) Unwrap() error { return ErrInstructionsLink }

// writeWhole writes content to path whole and never through a link. It
// refuses a path that stands as a symbolic link with an
// *InstructionsLinkError. Otherwise it creates a partial file beside path
// with os.CreateTemp, which opens a fresh name exclusively (O_CREATE and
// O_EXCL), so no file or link planted beside path is ever opened; it writes
// and syncs that file and renames it over path, which the filesystem makes
// one step and which replaces a link's name rather than following it. So a
// stop at any point leaves either no file under path or the whole content
// there, never part of it. A stop between the write and the rename leaves a
// partial file beside path, under partialPattern; the next write over the
// same path and CompleteIntake remove every one, through removePartials,
// which removes each name itself and never follows it. The Lstat before the
// rename and the rename are two steps, so a link planted at path between
// them is replaced by the rename, never written through.
func writeWhole(path string, content []byte) error {
	if err := refuseLink(path); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), partialPrefix(path)+"*")
	if err != nil {
		return err
	}
	partial := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	if _, err := f.Write(content); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return removePartials(path)
}

// refuseLink refuses a path that stands as a symbolic link, and reports
// nothing for one that is a file or is not there.
func refuseLink(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return &InstructionsLinkError{Path: path}
	}
	return nil
}

// partialPrefix is the name every partial file of path begins with.
func partialPrefix(path string) string { return "." + filepath.Base(path) + ".partial" }

// removePartials removes every partial file of path a stop left beside it,
// found by name, each removed as a name with os.Remove, which removes a
// link itself and never what it points to.
func removePartials(path string) error {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), partialPrefix(path)) {
			continue
		}
		if err := os.Remove(filepath.Join(filepath.Dir(path), e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// branchNameRefusal says why a branch name is refused, or nothing when it is
// not. It is not git's reference-name rule, which git owns and internal/vcs
// deliberately does not copy; it is the subset that would otherwise reach
// BranchInGate as a for-each-ref pattern or git as an option: an option
// prefix, whitespace or a control byte, the glob and revision characters
// ~ ^ : ? * [ \, a ".." or "@{" element, a bare "@", and a component that is
// empty, begins or ends with a dot, or ends in ".lock". A name past this
// check can still be one git refuses, and that refusal then arrives from the
// gate read as an error rather than as a task recorded under a name no branch
// can carry.
func branchNameRefusal(branch string) string {
	switch {
	case strings.HasPrefix(branch, "-"):
		return "would be read as an option"
	case strings.ContainsAny(branch, "~^:?*[\\"):
		return "carries a character git refuses in a reference name"
	case strings.Contains(branch, ".."), strings.Contains(branch, "@{"):
		return "carries a revision element"
	case strings.HasPrefix(branch, "/"), strings.HasSuffix(branch, "/"), strings.HasPrefix(branch, "."), strings.HasSuffix(branch, ".lock"):
		return "is not a branch name git accepts"
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return "carries whitespace or a control character"
		}
	}
	if branch == "@" {
		return "is not a branch name git accepts"
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return "has a component git refuses in a reference name"
		}
	}
	return ""
}

// decodeShape reads a shape off the wire, refusing text outside the closed
// set rather than recording a shape nobody defined. store.TaskShape is the
// one owner of the set; this is its decoder at the intake edge.
func decodeShape(text string) (store.TaskShape, error) {
	switch shape := store.TaskShape(strings.TrimSpace(text)); shape {
	case store.TaskDelivery, store.TaskInvestigation:
		return shape, nil
	default:
		return "", fmt.Errorf("%w: shape %q is neither %q nor %q", ErrRequest, text, store.TaskDelivery, store.TaskInvestigation)
	}
}

// IsRefusal reports whether err is one of this package's typed refusals of an
// intake, as opposed to a failure to read or write: a malformed request, a
// person's branch, an unknown record, or a posture lighter than the project's.
// A caller relaying the result to the coordinator reports a refusal as an
// answer and a failure as a failure.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrRequest) || errors.Is(err, ErrBranchTaken) || errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, ErrLighterPosture)
}
