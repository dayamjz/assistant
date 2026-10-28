package store

import (
	"context"
	"database/sql"
	"fmt"
)

// The launch-retirement exclusion.
//
// PRD section 6 gives a housekeeping pass two kinds of write that never
// overlap: the writes that begin a worker in it (an attempt's claim, the
// launch's claim, the record of the worker's session) and the writes that
// end its claim on its repository (the offer of its retirement hold and the
// answer of retire). The invariant, decided by exclusionTx and nowhere
// else:
//
//   - no retirement hold is offered while a launch of the pass is under way:
//     its latest attempt's launch is claimed (ClaimAttemptLaunch, the last
//     write before a worker's process starts) and that attempt's session is
//     not recorded (SetAttemptSession);
//   - no attempt is claimed, no launch is claimed and no session is recorded
//     while the pass's retirement hold is open or once its retirement is
//     recorded;
//   - a retirement is written only in answer to the pass's retirement hold,
//     resolving it in the same transaction only while it is open.
//
// So the hold is offered only while no launch is under way, no launch is
// claimed while it stands, and the retirement is written only while it
// stands: no retirement is written while a launch is under way, and no
// launch is claimed after one. Every write in passWrite asks exclusionTx in
// its own transaction on the one writer connection, so any two of them are
// wholly ordered and the later reads what the earlier wrote: they have
// exactly one loser in either order.
//
// What it leaves: a launch under way is read on the pass's latest attempt.
// A launch of an earlier attempt, still under way once a later attempt was
// claimed, reads as nothing, so a hold can be offered while it runs; its
// session record is then refused (ErrPassHeld or ErrPassRetired), and its
// caller ends the worker it started, which runs with no record naming it
// until then. And a launch claimed whose worker never recorded a session
// cannot be told from one still starting, so it reads as under way, and
// the pass is offered no retirement hold, until a later attempt is claimed.

// passWrite is a write the launch-retirement exclusion orders. Each one asks
// exclusionTx in its own transaction.
type passWrite int

const (
	// claimsAttempt is every attempt claim: BeginFirstTaskAttempt,
	// BeginDispatchAttempt and BeginTaskAttempt, through anchorTaskAttemptTx.
	claimsAttempt passWrite = iota + 1
	// claimsLaunch is ClaimAttemptLaunch.
	claimsLaunch
	// recordsSession is SetAttemptSession.
	recordsSession
	// offersRetirement is OfferRetirementHold.
	offersRetirement
	// answersRetire is RetireHousekeepingPass.
	answersRetire
)

// exclusionTx refuses w on task, read in w's transaction, on the
// exclusion's terms: a write that begins a worker with ErrPassRetired once
// the retirement is recorded and ErrPassHeld while the retirement hold is
// open; the offer with ErrPassRetired once the retirement is recorded and
// ErrPassMoves while a launch is under way; and the retirement, whose
// answered hold is hold (empty for every other write), with ErrNotFound for
// a hold that does not exist and ErrNotRetirementHold for one that is not
// the task's retirement hold as the offer marked it (requireRetirementHoldTx
// says where an answered one is refused). A task that is not a pass has no retirement and no
// retirement hold, so it is refused nothing to begin a worker.
func exclusionTx(ctx context.Context, tx *sql.Tx, task string, w passWrite, hold string) error {
	switch w {
	case claimsAttempt, claimsLaunch, recordsSession:
		if err := refuseRetiredTx(ctx, tx, task); err != nil {
			return err
		}
		return refuseHeldTx(ctx, tx, task)
	case offersRetirement:
		if err := refuseRetiredTx(ctx, tx, task); err != nil {
			return err
		}
		return refuseLaunchUnderWayTx(ctx, tx, task)
	case answersRetire:
		return requireRetirementHoldTx(ctx, tx, task, hold)
	}
	return fmt.Errorf("write %d is not one the launch-retirement exclusion orders", w)
}

// refuseHeldTx refuses, with ErrPassHeld, a task whose retirement hold, the
// one OfferRetirementHold marked, is open.
func refuseHeldTx(ctx context.Context, tx *sql.Tx, task string) error {
	var key string
	err := tx.QueryRowContext(ctx,
		`SELECT key FROM hold WHERE task_id = ? AND resolution IS NULL AND housekeeping_retirement = 1 LIMIT 1`, task).Scan(&key)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading whether task %s holds on its retirement: %w", task, err)
	}
	return fmt.Errorf("%w: task %s holds on %s", ErrPassHeld, task, key)
}

// refuseLaunchUnderWayTx refuses, with ErrPassMoves, a task whose latest
// attempt's launch is claimed and whose session is not recorded.
func refuseLaunchUnderWayTx(ctx context.Context, tx *sql.Tx, task string) error {
	var sequence int64
	var claimed, session Optional[string]
	err := tx.QueryRowContext(ctx,
		`SELECT sequence, launch_claimed_at, session_ref FROM task_attempt WHERE task_id = ? ORDER BY sequence DESC LIMIT 1`, task).
		Scan(&sequence, &claimed, &session)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading whether a launch of task %s is under way: %w", task, err)
	}
	if claimed.IsKnown() && !session.IsKnown() {
		return fmt.Errorf("%w: the launch of its latest attempt %d is claimed and its session is not recorded, so its worker may be starting",
			ErrPassMoves, sequence)
	}
	return nil
}

// requireRetirementHoldTx refuses a retirement of task answering hold unless
// hold is the task's retirement hold, the one OfferRetirementHold registered
// and marked: no other hold of the pass, a report's decision or a
// relaunch's, releases its claim on its repository. That the hold is still
// open is the answer's own write's: RetireHousekeepingPass resolves it only
// while it is open, in the same transaction, and refuses with
// ErrHoldResolved otherwise.
func requireRetirementHoldTx(ctx context.Context, tx *sql.Tx, task, hold string) error {
	var owner Optional[string]
	var marked Optional[int64]
	switch err := tx.QueryRowContext(ctx, `SELECT task_id, housekeeping_retirement FROM hold WHERE key = ?`, hold).
		Scan(&owner, &marked); {
	case isNoRows(err):
		return fmt.Errorf("hold %s: %w", hold, ErrNotFound)
	case err != nil:
		return err
	}
	if owner.Or("") != task || marked.Or(0) != 1 {
		return fmt.Errorf("%w: hold %s is not the retirement hold OfferRetirementHold registered for pass %s", ErrNotRetirementHold, hold, task)
	}
	return nil
}
