package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Placement is where a dispatch puts a task: PRD section 8's "the machine it
// runs on, written once at dispatch". It is one of three, each made by its
// constructor: this machine, a remote host by name, or the machine of the
// kept copy a carried-on task continues. The zero value is none of them, and
// BeginFirstTaskAttempt refuses it, so a dispatch that decided nothing is
// never recorded as one that chose this machine.
//
// Which machine a task should go to is not decided here: section 8 makes
// that the program's rule at dispatch, from readings and limits. This is the
// record of the outcome.
type Placement struct {
	kind     placementKind
	host     string
	keptWork int64
}

// placementKind is which of the three a Placement is; the zero kind is none.
type placementKind int

const (
	_ placementKind = iota // none: the zero Placement
	placedHere
	placedOnRemoteHost
	placedWithKeptCopy
)

// OnThisMachine is a dispatch onto this machine, the home's own, which the
// task record holds as no machine at all.
func OnThisMachine() Placement { return Placement{kind: placedHere} }

// OnRemoteHost is a dispatch onto the remote host the global remote_hosts
// setting names name. BeginFirstTaskAttempt refuses an empty name, which
// would read back as this machine, and one holding whitespace or a control
// character, so a name reads back as one word and renders as it was given.
// Whether the setting names such a host is the dispatcher's to know; the
// store records the name and does not read the configuration.
func OnRemoteHost(name string) Placement { return Placement{kind: placedOnRemoteHost, host: name} }

// CarriedOnFrom is the dispatch of a task carrying on a kept work entry: PRD
// section 8's "carry on gives the new task the kept copy's machine rather
// than placing it". The caller names the entry and never a machine: in the
// transaction that records the first attempt, the machine is read off the
// row of the task the entry outlived, that task's own record of where it
// ran. A run's copy stands under this home's worktrees, so for a run's
// entry it is this machine.
//
// The entry must be answered carry on and must be of another owner than the
// task, with the task's project and branch, or the dispatch is refused with
// ErrNotCarriedOn. The project is the repository the entry names, as for
// every kept work entry, and an entry naming none is refused, since nothing
// then shows that the task is of its project; Task.Project, what the task
// is for, is not compared. An entry naming no branch matches only a task
// naming none. An entry whose carry on names the task it created
// (carried_on_task, keptWorkCarryOnMigration) is refused for any other
// task. One answered carry on before that column existed names none, and
// for it this is as far as the store can tie the task to the entry: two
// tasks of that project and branch could both name it, and each would get
// its machine.
func CarriedOnFrom(entry int64) Placement {
	return Placement{kind: placedWithKeptCopy, keptWork: entry}
}

// validRemoteHost refuses a remote host name the task record would not hold
// as one: empty, which is this machine's, or holding whitespace, a control
// character, or bytes that are not UTF-8.
func validRemoteHost(name string) error {
	if name == "" {
		return errors.New("an empty remote host name is this machine's, which is recorded as no machine")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("remote host name %q is not UTF-8", name)
	}
	if strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("remote host name %q holds whitespace or a control character", name)
	}
	return nil
}

// BeginFirstTaskAttempt records a task's dispatch: its first attempt and the
// machine it runs on, in one transaction, and returns the attempt.
//
// It is anchored to no attempt, on BeginTaskAttempt's terms: a task that
// already has one is refused with a *TaskMovedError wrapping ErrTaskMoved
// naming where its attempts stand, so two dispatchers that both found none
// record one dispatch between them, and the machine is written by the one
// that wins. Every later attempt, a relaunch or a dispatch retried after an
// attempt that never launched, is BeginTaskAttempt's, which writes no
// machine, and the schema refuses every write of the machine once an attempt
// exists, so the task stays where its dispatch put it.
//
// The machine is written only for a remote host, since this machine is
// recorded as none: OnThisMachine records the attempt alone. A placement by
// CarriedOnFrom resolves to its kept copy's machine as that says, and is
// refused with ErrNotCarriedOn, wrapped, on its terms; an entry that does not
// exist is ErrNotFound, wrapped. The zero Placement, and a remote host name
// OnRemoteHost says is refused, are refused before anything is read. A task
// that does not exist is ErrNotFound, wrapped.
//
// A dispatch is new task work, so a task kept work holds is refused with a
// *DispatchHeldError in the same transaction and nothing is written, on
// keptworkhold.go's terms, once the placement itself is found valid.
func (s *Store) BeginFirstTaskAttempt(ctx context.Context, taskID string, at Placement) (TaskAttempt, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskAttempt{}, fmt.Errorf("store: no task named")
	}
	switch at.kind {
	case placedHere, placedWithKeptCopy:
	case placedOnRemoteHost:
		if err := validRemoteHost(at.host); err != nil {
			return TaskAttempt{}, fmt.Errorf("store: task %s: %w", taskID, err)
		}
	default:
		return TaskAttempt{}, fmt.Errorf("store: task %s: a dispatch names where it places the task, and this one names nowhere", taskID)
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := anchorTaskAttemptTx(ctx, tx, taskID, 0); err != nil {
			return err
		}
		machine := at.host
		if at.kind == placedWithKeptCopy {
			var err error
			if machine, err = keptCopyMachineTx(ctx, tx, taskID, at.keptWork); err != nil {
				return err
			}
		}
		if err := refuseHeldDispatchTx(ctx, tx, taskID); err != nil {
			return err
		}
		// Before the attempt: the trigger seals the column from the first
		// attempt on.
		if machine != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE task SET machine = ? WHERE id = ?`, machine, taskID); err != nil {
				return err
			}
		}
		return insertTaskAttemptTx(ctx, tx, taskID, 1, now)
	})
	if err != nil {
		var moved *TaskMovedError
		var held *DispatchHeldError
		if errors.As(err, &moved) || errors.As(err, &held) {
			return TaskAttempt{}, err
		}
		return TaskAttempt{}, fmt.Errorf("store: dispatching task %s: %w", taskID, err)
	}
	return TaskAttempt{TaskID: taskID, Sequence: 1, StartedAt: now}, nil
}

// keptCopyMachineTx is the machine of the copy kept work entry stands for,
// as the owner it outlived records it, for task taskID carrying it on, on
// CarriedOnFrom's terms.
func keptCopyMachineTx(ctx context.Context, tx *sql.Tx, taskID string, entry int64) (string, error) {
	var owner, run, repository, branch, answer, carrier, machine Optional[string]
	err := tx.QueryRowContext(ctx, `
		SELECT k.task_id, k.run_id, k.repository_id, k.branch, k.answer, k.carried_on_task, t.machine
		FROM kept_work k LEFT JOIN task t ON t.id = k.task_id
		WHERE k.id = ?`, entry).Scan(&owner, &run, &repository, &branch, &answer, &carrier, &machine)
	if err != nil {
		return "", fmt.Errorf("kept work entry %d: %w", entry, errNoRows(err))
	}
	if got := answer.Or(""); got != string(KeptCarryOn) {
		return "", fmt.Errorf("%w: kept work entry %d is answered %q, not %q", ErrNotCarriedOn, entry, got, KeptCarryOn)
	}
	if owner.Or("") == taskID {
		return "", fmt.Errorf("%w: kept work entry %d is of the task's own copy, which it never left", ErrNotCarriedOn, entry)
	}
	if !repository.IsKnown() {
		return "", fmt.Errorf("%w: kept work entry %d names no project, so no task can be shown to be of its project", ErrNotCarriedOn, entry)
	}
	var ownRepository, ownBranch Optional[string]
	if err := tx.QueryRowContext(ctx, `SELECT repository_id, branch FROM task WHERE id = ?`, taskID).
		Scan(&ownRepository, &ownBranch); err != nil {
		return "", errNoRows(err)
	}
	if !sameOptional(ownRepository, repository) || !sameOptional(ownBranch, branch) {
		return "", fmt.Errorf("%w: kept work entry %d is of project %q branch %q, and the task of project %q branch %q",
			ErrNotCarriedOn, entry, repository.Or(""), branch.Or(""), ownRepository.Or(""), ownBranch.Or(""))
	}
	if name, known := carrier.Get(); known && name != taskID {
		return "", fmt.Errorf("%w: kept work entry %d was carried on by task %s", ErrNotCarriedOn, entry, name)
	}
	name, ok := machine.Get()
	if !ok {
		return "", nil
	}
	if err := validRemoteHost(name); err != nil {
		return "", fmt.Errorf("kept work entry %d's owner %s: column machine: %w", entry, owner.Or(run.Or("")), err)
	}
	return name, nil
}

// sameOptional reports whether a and b are both unknown or both the same
// known value.
func sameOptional(a, b Optional[string]) bool {
	x, okA := a.Get()
	y, okB := b.Get()
	return okA == okB && x == y
}
