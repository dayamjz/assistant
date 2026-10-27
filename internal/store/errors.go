package store

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned by every accessor that reads one record by its
// identifier when no such record exists. It is a typed result a caller handles,
// not a condition to log and continue past.
var ErrNotFound = errors.New("store: record not found")

// ErrNoRedactor is returned by Open when no [vcs.Redactor] was supplied. PRD
// section 8 gives credential removal one owner, so this package refuses to open
// rather than invent a second implementation of it.
var ErrNoRedactor = errors.New("store: no redactor supplied, and this package does not implement one")

// ErrRedactorInert is returned by Open when the supplied redactor leaves the
// credential in a probe URL of the shape PRD section 8 requires removed. It is
// what a caller gets when its redactor was never wired up, or was replaced by
// something that does nothing. See the package comment for what the probe does
// and does not establish.
var ErrRedactorInert = errors.New("store: the supplied redactor left a credential in the probe URL untouched")

// ErrBuildIdentityMissing is returned by CreateRun when the run carries no
// usable build identity. PRD section 8 requires a surprising result to be
// traceable to the exact build and configuration that produced it, so a run
// that cannot name either is refused at creation rather than recorded and
// discovered later.
var ErrBuildIdentityMissing = errors.New("store: run has no build identity")

// ErrConfigDigestMissing is returned by CreateRun when the run names no
// configuration. It is the other half of what ErrBuildIdentityMissing covers.
var ErrConfigDigestMissing = errors.New("store: run has no configuration digest")

// ErrWorkingPathTaken is returned by UpsertRepository when the working path it
// was given already belongs to a different repository identifier. A checkout
// stands for one repository record, and registering an existing one under a
// second identifier is an ordinary thing for an operator to try, so it is a
// typed result a caller handles rather than a failed write it cannot tell apart
// from any other. The error names the path and the identifier that holds it.
var ErrWorkingPathTaken = errors.New("store: working path already belongs to another repository")

// ErrRepositoryInUse is returned by ForgetRepository when a task still names
// one of the repository's runs. Removing it would leave that task pointing at
// a run that no longer exists, and which of the two goes is not this package's
// call.
var ErrRepositoryInUse = errors.New("store: a task still names a run of this repository")

// ErrRunActive is returned by ForgetRepository when a run has not finished. A
// run that may still move is one a service may still be driving, and the
// records it is about to write would land against rows that had been removed.
var ErrRunActive = errors.New("store: run has not finished")

// ErrRunProcessRecorded is returned by ForgetRepository and
// RepositoryRemovable when a run of the repository still has a run process
// record: an agent group it started whose end was never recorded. The record
// is the only thing a later service's recovery reaps that group from, so
// forgetting it would leave a process nothing could find again. A recovery
// that verifies the group ended or gone clears the record, after which the
// repository can be forgotten; one it cannot verify stands, and that refusal
// stands with it.
var ErrRunProcessRecorded = errors.New("store: a run's agent process has no recorded end")

// ErrKeptWorkWakeQueued is returned by ForgetRepository, RepositoryRemovable
// and ReserveRepositoryRemoval while a kept-work wake about a kept work
// entry of the repository is unacknowledged. PRD section 8 keeps such a row
// actionable until the coordinator acknowledges it, and forgetting the
// repository would delete it with its entry, and its notification with it,
// though its entry is closed. Acknowledging the wake clears the refusal;
// the closed wake and its notification then go with the closed entry.
var ErrKeptWorkWakeQueued = errors.New("store: a kept-work wake about the repository's kept work is unacknowledged")

// ErrNoResolution is returned by ResolveHold when no resolution text was given.
// PRD section 8 says a hold is closed only by an explicit resolution, and an
// empty one is not explicit.
var ErrNoResolution = errors.New("store: a hold is closed only by an explicit resolution")

// ErrNoResolver is returned by ResolveHold when the resolution names nobody,
// which is the zero Resolver. PRD section 8 requires every hold resolution to
// record who made it, so a resolution with nobody attached is refused rather
// than stored as one nobody can account for afterwards.
var ErrNoResolver = errors.New("store: a hold resolution records who made it")

// ErrUnknownResolver is what a read of a hold returns when its resolved_by
// column holds a value outside the closed set Resolver defines. The set is
// closed, so a row outside it was not written by this package's accessors, and
// reporting it as "nobody was recorded" would turn a row nobody can account for
// into a fact about the resolution.
var ErrUnknownResolver = errors.New("store: hold resolver is outside the closed set")

// ErrUnknownPullRequestEnd is what a read of a merge record returns when its
// end column holds a value outside the closed set PullRequestEnd defines, and
// what TransitionRunOnPullRequest returns when asked to record one. The set
// is closed, so a row outside it was not written by this package's accessor,
// and reading it back as "no end was recorded" would turn a row nobody can
// account for into a fact about how the run ended.
var ErrUnknownPullRequestEnd = errors.New("store: pull request end is outside the closed set")

// ErrRunStatus is the class every *RunStatusError belongs to. It is returned
// by TransitionRun when the run was not in any of the statuses the caller said
// the move was legal out of, which is the answer a caller acts on rather than
// a failure of the write.
var ErrRunStatus = errors.New("store: run is not in a status this transition may be made from")

// RunStatusError reports a refused transition and names what the run was
// actually in, so a caller reports the state it found rather than only that it
// was surprised by one.
type RunStatusError struct {
	// Run is the run's identifier.
	Run string
	// Expected is every status the caller said the move was legal out of.
	Expected []RunStatus
	// Actual is the status the run was in.
	Actual RunStatus
	// To is the status the move was to.
	To RunStatus
}

// Error names the run, where it stands, where it was to go, and what would
// have allowed the move.
func (e *RunStatusError) Error() string {
	expected := make([]string, len(e.Expected))
	for i, status := range e.Expected {
		expected[i] = string(status)
	}
	return fmt.Sprintf("store: run %s is %s, so it cannot move to %s, which is legal only from %s",
		e.Run, e.Actual, e.To, strings.Join(expected, ", "))
}

// Unwrap makes every refused transition match ErrRunStatus.
func (e *RunStatusError) Unwrap() error { return ErrRunStatus }

// ErrGraphRunExists is returned by AppendGraphCheckpoint when an append that
// claims a run as new finds the run already has a checkpoint history, and by
// CopyGraphCheckpoints when its destination does. Writing over either would
// discard positions a run actually stood in, so it is a typed result a caller
// branches on rather than a failed write.
var ErrGraphRunExists = errors.New("store: run already has a checkpoint history")

// ErrGraphAnchor is the class every *GraphAnchorError belongs to. It is
// returned by AppendGraphCheckpoint when the run moved after the caller read
// it, so the append was decided against a position the run no longer stands
// in. Read the run again and decide again from what it says now; retrying the
// same append would record a decision made against a run that has moved on.
var ErrGraphAnchor = errors.New("store: the run moved since the checkpoint this append is anchored to")

// GraphAnchorError reports a refused append and names where the run actually
// stands, so a caller reports the position it found rather than only that it
// was surprised by one.
type GraphAnchorError struct {
	// Run is the run the append was for.
	Run string
	// AnchorRun is the run the anchor named, which is some other run when that
	// is what was wrong with it.
	AnchorRun string
	// AnchorSeq is the sequence the anchor named.
	AnchorSeq int
	// Stands is the sequence the run's history actually reaches, and is zero
	// when the run has none.
	Stands int
}

// Error names the run, the anchor the append was decided against, and the
// position the run is actually at.
func (e *GraphAnchorError) Error() string {
	return fmt.Sprintf("store: run %s stands at checkpoint %d, so an append anchored to %s#%d is refused",
		e.Run, e.Stands, e.AnchorRun, e.AnchorSeq)
}

// Unwrap makes every refused append match ErrGraphAnchor.
func (e *GraphAnchorError) Unwrap() error { return ErrGraphAnchor }

// ErrClosed is returned by every accessor called after Close.
var ErrClosed = errors.New("store: database is closed")

// ErrSettingNotApplied is returned by Open when a connection does not report
// the journal mode, synchronous level, busy timeout, or foreign key
// enforcement the store was opened asking for. The error names the setting, the
// value asked for, and the value in effect. What the package comment says about
// durability and about concurrent writers is true of a database configured that
// way and of no other, so a store that cannot confirm the configuration refuses
// to open rather than describing one it does not have.
var ErrSettingNotApplied = errors.New("store: a connection setting is not the one the store was opened with")

// MigrationError names the migration that failed and what went wrong. The
// database is unchanged by the failed migration: each migration is applied in
// one transaction together with the row recording it.
type MigrationError struct {
	// Version is the migration's version number.
	Version int
	// Name is the migration's name.
	Name string
	// Err is the underlying failure.
	Err error
}

// Error implements error.
func (e *MigrationError) Error() string {
	return fmt.Sprintf("store: migration %d (%s): %v", e.Version, e.Name, e.Err)
}

// Unwrap returns the underlying failure.
func (e *MigrationError) Unwrap() error { return e.Err }

// ErrNotAdditive is the failure a migration carries when it would change the
// shape of a table that already exists in a way old rows cannot survive. See
// the package comment for the rule and why it is checked against the database
// rather than against the migration text.
var ErrNotAdditive = errors.New("store: migration is not additive")

// ErrSchemaAhead is returned by Open when the database has been migrated by a
// newer build than this one. Running an older binary against it would read
// columns it does not know about and write rows a newer one would misread, so
// opening refuses rather than proceeding on a schema it cannot account for.
var ErrSchemaAhead = errors.New("store: database schema is newer than this build")

// ErrSchemaChanged is returned by Open when a migration already recorded in the
// database does not match the one in this build's list. A shipped migration has
// already run on somebody's machine and will not run again there, so editing
// one silently gives two databases different shapes under the same version
// number. A schema change is a new migration.
//
// What is compared is the migration's name, its statement count, and a digest
// of its statement text, so an edit made in place is caught rather than only a
// renamed or resized migration. The one row this cannot speak for is a
// migration recorded before this build's predecessor wrote digests: its digest
// is unknown, and for it the comparison is name and count alone.
var ErrSchemaChanged = errors.New("store: a recorded migration differs from this build's copy")

// ErrHoldResolved is returned by ResolveHold when the hold already carries a
// resolution. The second decision on a closed hold is a new decision and
// belongs to a new key, so this is a typed result a caller branches on rather
// than a failure of the write.
var ErrHoldResolved = errors.New("store: hold is already resolved")

// ErrMigrationsMalformed is returned when the migration list itself is wrong:
// versions that do not start at one or do not increase by one. It is a
// programming error in this package rather than a condition of the database.
var ErrMigrationsMalformed = errors.New("store: migration list is malformed")

// ErrTaskMoved is the class every *TaskMovedError belongs to. It is returned
// by the task writes that take an anchor - AppendTaskEventAfter,
// BeginTaskAttempt and CreateTask - when the record the anchor names has moved
// since the caller read it, so the write was decided against a position the
// record no longer stands at. Read again and decide again from what the record
// says now; retrying the same write would act on a decision the record has
// already outrun.
var ErrTaskMoved = errors.New("store: the task's record moved since the position this write is anchored to")

// TaskAnchor names which of a task's records a refused write was anchored to.
// It is a closed set: one value per accessor that takes an anchor.
type TaskAnchor string

const (
	// AnchoredEvent is an anchor on the task's event history, named by the
	// highest sequence the caller had seen.
	AnchoredEvent TaskAnchor = "event"
	// AnchoredAttempt is an anchor on the task's attempt history, named by
	// the highest attempt sequence the caller had seen.
	AnchoredAttempt TaskAnchor = "attempt"
	// AnchoredBranch is an anchor on the tasks of one repository and branch,
	// named by the highest Task.Row the caller had seen among them.
	AnchoredBranch TaskAnchor = "branch"
)

// TaskMovedError reports a refused anchored write and names where the record
// actually stands, so a caller reports the position it found rather than only
// that it was surprised by one.
type TaskMovedError struct {
	// Task is the task the write was for.
	Task string
	// Anchor is which record the write was anchored to.
	Anchor TaskAnchor
	// Expected is the position the caller said it had observed.
	Expected int64
	// Stands is the position the record actually reaches, and is zero when
	// the record is empty.
	Stands int64
}

// Error names the task, the record, the anchor the write was decided against,
// and where the record actually stands.
func (e *TaskMovedError) Error() string {
	return fmt.Sprintf("store: task %s: its %s record stands at %d, so a write anchored to %d is refused",
		e.Task, e.Anchor, e.Stands, e.Expected)
}

// Unwrap makes every refused anchored write match ErrTaskMoved.
func (e *TaskMovedError) Unwrap() error { return ErrTaskMoved }

// ErrAttemptSessionRecorded is returned by SetAttemptSession when the attempt
// already names a session. An attempt is one process, and its reference is
// filled once after that process starts; a second reference on the same
// attempt would be a relaunch, which is a new attempt and not a rewrite of
// this one's record.
var ErrAttemptSessionRecorded = errors.New("store: the attempt already names a session")

// ErrRemoteSessionWithoutHost is returned by SetAttemptSession for a session
// of a task on a remote host that names no herdr server hosting it. PRD
// section 8 records that server beside the reference, as the one record of
// it, and a reference recorded without it would leave a server no record
// names. Nothing is written.
var ErrRemoteSessionWithoutHost = errors.New("store: a session of a task on a remote host is recorded with the herdr server hosting it")

// ErrSessionHostOnThisMachine is returned by SetAttemptSession for a session
// of a task on this machine that names a host. The task record holds a
// session's host only for a worker on a remote host, so a local worker's
// server is recorded nowhere here. Nothing is written.
var ErrSessionHostOnThisMachine = errors.New("store: a session host is recorded only for a task on a remote host")

// ErrMachineAtIntake is returned by CreateTask for a task that states a
// machine. A task's machine is written at its dispatch, by
// BeginFirstTaskAttempt, and never at intake. Nothing is written.
var ErrMachineAtIntake = errors.New("store: a task's machine is written at its dispatch, never at intake")

// ErrFirstAttemptIsDispatch is returned by BeginTaskAttempt anchored to no
// attempt. A task's first attempt is its dispatch, which places it, so it
// is BeginFirstTaskAttempt's, and BeginTaskAttempt begins only the attempts
// after it, on the machine the first one recorded. Nothing is written.
var ErrFirstAttemptIsDispatch = errors.New("store: a task's first attempt is its dispatch, which BeginFirstTaskAttempt records with its machine")

// ErrNotCarriedOn is returned by BeginFirstTaskAttempt for a placement by
// CarriedOnFrom that names an entry the task cannot be carrying on: one not
// answered carry on, one of the task's own copy, or one of another project
// or branch. Nothing is written.
var ErrNotCarriedOn = errors.New("store: the task is not carrying on that kept work entry")

// ErrAttemptRouteRecorded is returned by SetAttemptRoute when the attempt
// already names a routed adapter. The selection is filled once before the
// process starts; a second selection on the same attempt would be a relaunch,
// which is a new attempt.
var ErrAttemptRouteRecorded = errors.New("store: the attempt already names a route")

// ErrRunAgentResolved is returned by SetRunAgentResolution when the run
// already names the agent it resolved. A run resolves its agent once, when
// it begins; a second resolution would describe a run that did not happen.
var ErrRunAgentResolved = errors.New("store: the run already names its resolved agent")

// ErrAttemptKnowledgeRecorded is returned by SetAttemptKnowledge when the
// attempt already names the knowledge revision it was launched with. What a
// worker was told is recorded once, before the process starts; a launch told
// something else is a new attempt.
var ErrAttemptKnowledgeRecorded = errors.New("store: the attempt already names its knowledge revision")

// ErrCleanupRecorded is returned by RecordTaskCleanup when the task already
// has a cleanup record. PRD section 8 says the record is written once, so a
// second write is refused rather than overwriting the proof the first was made
// from. TransitionTaskShape returns it too, for a task whose copy a cleanup
// has claimed: a promotion of that task would move a copy proven on an
// investigation's terms under a delivery task, so the claim is read in the
// transaction that would flip the shape.
var ErrCleanupRecorded = errors.New("store: the task already has a cleanup record")

// ErrMergeRecorded is returned by RecordMergeOfPassedRun when the run
// already has a merge record. PRD section 8 has the record written once, so
// a second read of the same merge is refused rather than recorded again.
var ErrMergeRecorded = errors.New("store: the run already has a merge record")

// ErrUnknownDelivery is returned by CreateTask and TransitionTaskShape for a
// delivery posture outside config.Delivery's closed set, and by a read of a task whose stored posture
// is outside it, which no accessor here wrote.
var ErrUnknownDelivery = errors.New("store: the delivery posture is not one this build defines")

// ErrLocalLandingRecorded is returned by RecordLocalLanding when the task
// already has a local landing record. PRD section 8 has the record written
// once, so a second write is refused rather than overwriting what the first
// fast-forward recorded.
var ErrLocalLandingRecorded = errors.New("store: the task already has a local landing record")

// ErrNotLocal is returned by RecordLocalLanding for a task that is not a
// delivery task recorded under config.DeliveryLocal: only a local task is
// delivered by a fast-forward, so a landing record on any other task would
// say it landed in a way its posture never allowed.
var ErrNotLocal = errors.New("store: the task was not recorded under the local delivery posture")

// ErrCleanupRemoved is returned by MarkCleanupRemoved when the task's cleanup
// record already says when the copy was removed. The removal happens once,
// so a second mark is a caller re-reporting what the record already holds
// rather than a fact to overwrite.
var ErrCleanupRemoved = errors.New("store: the cleanup record already records the copy's removal")

// ErrCleanupRemovalFirst is returned by RecordTaskCleanup when the record it
// is handed already says when the copy was removed. The record is two
// writes in a fixed order, the proof and then the removal mark, so a first
// write that carries a removal would be a record of a removal that preceded
// its proof.
var ErrCleanupRemovalFirst = errors.New("store: a cleanup record is written with its proof and marked removed afterwards")

// ErrUnknownCleanupProof is what a read of a cleanup record returns when its
// proof column holds a value outside the closed set CleanupProof defines, and
// what RecordTaskCleanup returns when asked to record one. The set is closed,
// so a row outside it was not written by this package's accessors, and reading
// it back as some proof it is not would turn a row nobody can account for into
// a fact about what cleanup established.
var ErrUnknownCleanupProof = errors.New("store: cleanup proof is outside the closed set")

// ErrNoAcknowledger is returned by AcknowledgeWakes when the acknowledgement
// names nobody, which is the zero Acknowledger. A wake's acknowledgement
// records who made it, so one with nobody attached is refused rather than
// stored as one nobody can account for afterwards.
var ErrNoAcknowledger = errors.New("store: a wake acknowledgement records who made it")

// ErrUnknownAcknowledger is what a read of a wake returns when its
// acknowledged_by column holds a value outside the closed set Acknowledger
// defines. The set is closed, so a row outside it was not written by this
// package's accessor, and reporting it as "nobody was recorded" would turn a
// row nobody can account for into a fact about the acknowledgement.
var ErrUnknownAcknowledger = errors.New("store: wake acknowledger is outside the closed set")

// ErrWakeHandled is returned by AppendWakeWithMarkers when the wake it was
// asked to append has the task, kind and marker of a row already
// acknowledged. A replay of a detection whose row is still queued is one
// wake and reads back as already queued; the same key arriving after the row
// was handled means the marker the loop composed did not advance for a
// surface that is new, which is a defect in the marker and not a wake to
// ignore. Nothing is written, the markers included.
var ErrWakeHandled = errors.New("store: a wake with this task, kind and marker was already acknowledged")

// ErrWakeUnknown is the reason a *WakeIDError carries when AcknowledgeWakes
// was given an identifier no wake row has. Nothing in the batch is written.
var ErrWakeUnknown = errors.New("store: no wake has this identifier")

// ErrWakeAcknowledged is the reason a *WakeIDError carries when
// AcknowledgeWakes was given a wake already acknowledged. The first
// acknowledgement stands and nothing in the batch is written, so a caller
// retrying a batch whose commit it did not see learns that the commit landed
// rather than writing over who acknowledged.
var ErrWakeAcknowledged = errors.New("store: wake is already acknowledged")

// WakeIDError reports a refused acknowledgement and names the identifier that
// refused it and why, so a caller reports the row it tripped on rather than
// only that the batch failed.
type WakeIDError struct {
	// ID is the wake identifier the batch named.
	ID int64
	// Reason is ErrWakeUnknown or ErrWakeAcknowledged.
	Reason error
}

// Error names the identifier and the reason.
func (e *WakeIDError) Error() string {
	return fmt.Sprintf("store: acknowledging wake %d: %v", e.ID, e.Reason)
}

// Unwrap makes the error match its Reason.
func (e *WakeIDError) Unwrap() error { return e.Reason }

// ErrReadOnly is returned by every accessor that writes when the store was
// opened by OpenReadOnly. A read-only store has no writer connection at all,
// so the refusal is this package's own condition rather than whatever the
// driver would say.
var ErrReadOnly = errors.New("store: the store is open read-only")

// ErrUnreadable is the class every *UnreadableError belongs to: OpenReadOnly
// could not read the database as one this build's accessors can answer
// from. It is the typed result a caller feeds a reading that could not be
// taken, which for the stop guard means unknown and never none.
var ErrUnreadable = errors.New("store: the database cannot be read")

// UnreadableError reports why OpenReadOnly could not read a database: the
// file is missing or not a database, its schema is behind this build, which
// a read-only open may not migrate, ahead of it, or recorded differently, or
// a connection did not come back carrying the settings the package comment's
// guarantees rest on.
type UnreadableError struct {
	// Path is the absolute path that was opened.
	Path string
	// Err is what went wrong.
	Err error
}

// Error names the path and what went wrong.
func (e *UnreadableError) Error() string {
	return fmt.Sprintf("store: %s cannot be read: %v", e.Path, e.Err)
}

// Unwrap makes every unreadable database match ErrUnreadable, and the
// underlying cause match too through Is and As.
func (e *UnreadableError) Unwrap() []error { return []error{ErrUnreadable, e.Err} }

// ErrSchemaBehind is returned, wrapped in an *UnreadableError, by
// OpenReadOnly when the database lacks a migration this build has. A
// read-only open applies nothing, so it cannot bring the database forward,
// and reading a schema that lacks a table an accessor selects from would
// fail per call rather than once at the open.
var ErrSchemaBehind = errors.New("store: database schema is behind this build and a read-only open cannot migrate it")

// ErrPrimarySnapshotRecorded is returned by RecordPrimarySnapshot when the
// task's attempt already has a primary snapshot: the record is of one read,
// written once, and the first stands.
var ErrPrimarySnapshotRecorded = errors.New("store: the attempt already has a primary snapshot")

// ErrTaskShapeMoved is returned by RecordTaskCleanup when the task's shape is
// not the one the cleanup was decided for, and by TransitionTaskShape when
// it is not the one the transition is from. The shape decides what a cleanup
// may skip, so a record decided over one shape and written against another
// would remove a delivery copy on an investigation copy's terms; the
// comparison is made in the transaction that claims the record, which is why
// a caller passes the shape it decided from rather than reading it again.
// The transition reads the same row under the same serialization, which is
// what makes a second promotion of one task refuse here.
var ErrTaskShapeMoved = errors.New("store: the task's shape is not the one the cleanup was decided for")

// ErrTaskReportStored is returned by StoreTaskReport when the task already
// has a stored report. PRD section 11 makes the report the record that
// survives cleanup, written once, so a second report is refused rather than
// overwriting the holds the first was registered with.
var ErrTaskReportStored = errors.New("store: the task already has a stored report")

// ErrNoSender is returned by AppendMessage for the zero Sender: a message
// records who sent it, on Hold's terms for who resolved one.
var ErrNoSender = errors.New("store: a message records who sent it")

// ErrUnknownSender is returned when a sender is outside the closed set
// sender.go names, on ErrUnknownAcknowledger's terms.
var ErrUnknownSender = errors.New("store: message sender is outside the closed set")

// ErrDomainDelegated is returned by Delegate when the domain's latest
// delegation still stands: a domain is delegated to one subordinate
// coordinator at a time, and the standing delegation ends only by its own
// revision, so a second is refused rather than recorded beside it.
var ErrDomainDelegated = errors.New("store: the domain's delegation still stands")

// ErrDelegationEnded is returned by EndDelegation for a delegation already
// ended, and by BeginDelegationAttempt for one that no longer stands: the
// end is a delegation's one revision, and nothing starts a subordinate for
// a domain nobody delegates any more.
var ErrDelegationEnded = errors.New("store: the delegation has ended")

// ErrNoDelegationEnd is returned by EndDelegation for the zero
// DelegationEnd: a delegation's end records why, on Hold's terms for who
// resolved one.
var ErrNoDelegationEnd = errors.New("store: a delegation's end records why it ended")

// ErrUnknownDelegationEnd is returned when an end reason is outside the
// closed set delegation.go names, on ErrUnknownAcknowledger's terms.
var ErrUnknownDelegationEnd = errors.New("store: delegation end reason is outside the closed set")

// ErrDelegationMoved is the class every *DelegationMovedError belongs to,
// on ErrTaskMoved's terms: Delegate and BeginDelegationAttempt are anchored
// to what the caller had read of a domain's delegations or a delegation's
// attempts, and refuse when that record has moved since.
var ErrDelegationMoved = errors.New("store: the domain's delegation record moved since the position this write is anchored to")

// DelegationMovedError reports an anchored delegation write refused because
// the record moved, naming where it actually stands.
type DelegationMovedError struct {
	// Domain is the domain the write was for.
	Domain string
	// Attempt is whether the anchor was on the delegation's attempts rather
	// than on the domain's delegations.
	Attempt bool
	// Expected is the position the caller said it had observed.
	Expected int64
	// Stands is the position the record actually reaches.
	Stands int64
}

// Error names the domain, the record and both positions.
func (e *DelegationMovedError) Error() string {
	record := "delegations"
	if e.Attempt {
		record = "delegation attempts"
	}
	return fmt.Sprintf("store: domain %s: its %s stand at %d, and this write was anchored to %d",
		e.Domain, record, e.Stands, e.Expected)
}

// Unwrap makes every refused anchored delegation write match
// ErrDelegationMoved.
func (e *DelegationMovedError) Unwrap() error { return ErrDelegationMoved }

// ErrMessageDelivery is returned by MarkMessageWritten and
// MarkMessageRefused for a message whose delivery is already recorded,
// written or refused: that fact is recorded once and the first record
// stands.
var ErrMessageDelivery = errors.New("store: the message's delivery is already recorded")

// ErrMessageAcknowledged is returned by AcknowledgeMessage for a message
// already acknowledged. Nothing is appended.
var ErrMessageAcknowledged = errors.New("store: the message is already acknowledged")

// ErrMessageRefused is returned by AcknowledgeMessage for a message whose
// send was refused: it never reached a worker, so an acknowledgement of it
// acknowledges nothing. Nothing is appended.
var ErrMessageRefused = errors.New("store: the message was refused and never reached a worker")

// ErrUnknownSeenKind is the reason a *SeenRecordError carries when MarkSeen
// was named a record of a kind outside SeenKinds. Nothing in the batch is
// written.
var ErrUnknownSeenKind = errors.New("store: not a kind of record the seen mark names")

// ErrNotLanded is the reason a *SeenRecordError carries when MarkSeen was
// named a record that is not a landed one: no merge record for the run, one
// read as closed rather than merged, no cleanup record for the task, or one
// whose removal is not recorded. Nothing in the batch is written.
var ErrNotLanded = errors.New("store: no landed record has this key")

// SeenRecordError reports a refused seen mark and names the record that
// refused it and why, so a caller reports the record it tripped on rather
// than only that the batch failed.
type SeenRecordError struct {
	// Record is the record the batch named.
	Record SeenRecord
	// Reason is ErrUnknownSeenKind or ErrNotLanded.
	Reason error
}

// Error names the record and the reason.
func (e *SeenRecordError) Error() string {
	return fmt.Sprintf("store: marking %s %s seen: %v", e.Record.Kind, e.Record.Key, e.Reason)
}

// Unwrap makes the error match its Reason.
func (e *SeenRecordError) Unwrap() error { return e.Reason }

// ErrPassOpen is the class every *PassOpenError belongs to: PRD section 6's
// "a repository has at most one open pass", refused by CreateTask for a
// housekeeping pass of a repository that already has one open.
var ErrPassOpen = errors.New("store: the repository already has an open housekeeping pass")

// PassOpenError is CreateTask's refusal of a second open housekeeping pass,
// decided in the transaction that would have inserted it, and it names the
// pass that is open.
type PassOpenError struct {
	// Repository is the repository both passes are of.
	Repository string
	// Pass is the open pass.
	Pass string
}

// Error names the repository and its open pass.
func (e *PassOpenError) Error() string {
	return fmt.Sprintf("store: housekeeping pass %s of repository %s is still open, so no second pass is created", e.Pass, e.Repository)
}

// Unwrap makes every *PassOpenError match ErrPassOpen.
func (e *PassOpenError) Unwrap() error { return ErrPassOpen }

// ErrPassRetired is returned, wrapped, by BeginTaskAttempt and
// BeginFirstTaskAttempt for a housekeeping pass whose retirement is
// recorded: PRD section 11's one anchored relaunch refuses a retired pass,
// whoever asks, so no worker starts in its copy once its claim on its
// repository is released. SetAttemptSession returns it for the launch of an
// attempt claimed before the retirement, so no session is recorded for an
// attempt of a retired pass after its retirement, and OfferRetirementHold
// for a pass whose claim is already released.
var ErrPassRetired = errors.New("store: the housekeeping pass is retired and is never begun again")

// ErrPassHeld is returned, wrapped, by BeginTaskAttempt and
// BeginFirstTaskAttempt for a housekeeping pass with an open retirement
// hold, the one OfferRetirementHold registered: the hold waits on a person
// about whether the pass ends, and an attempt begun under it would run a
// worker in a pass that resolves Held, which is not in flight, out of
// supervision (P10). A keep answer closes the hold, and the next claim
// begins. The claim and the offer are decided in the store with exactly
// one loser in either order: the offer refuses a pass whose attempt moved
// (ErrTaskMoved), and the claim refuses a pass whose hold is open.
var ErrPassHeld = errors.New("store: the housekeeping pass holds on its retirement decision and is not begun until it is answered")

// ErrPassMoves is returned, wrapped, by RetireHousekeepingPass when something
// still moves the pass as its transaction reads it: a run attributed to it
// that has not ended, a run or an attempt newer than the retirement was
// decided against, or a session of that attempt recorded since the
// retirement was decided against none. OfferRetirementHold returns it for a
// run or a session on the same terms. Nothing is written.
var ErrPassMoves = errors.New("store: something still moves the housekeeping pass")

// ErrPassClosed is returned, wrapped, by RetireHousekeepingPass for a pass
// already retired or whose records put it in the terminal tier: its claim
// on its repository is already released, and a second retirement would be a
// second record of one end. OfferRetirementHold returns it for a pass in the
// terminal tier, whose retire answer could only be refused.
var ErrPassClosed = errors.New("store: the housekeeping pass is no longer open")

// ErrDueCheckStale is returned, wrapped, by AppendHousekeepingWake for a
// due check's wake and by AdvanceHousekeepingMarker when the repository's
// passes moved since the check read them: the pass the check followed is no
// longer the repository's newest, or a pass is open. A due check reads its
// facts before it fetches, so a wake raised from them would say no pass was
// ever created of a repository with one open, or name a base a newer pass
// has replaced, and the coordinator acting on it would create a pass the
// newest has already made redundant. Neither the wake nor the marker is
// written, and the next supervision pass reads the passes afresh.
var ErrDueCheckStale = errors.New("store: the repository's housekeeping passes moved since the due check read them")

// ErrHousekeepingWakeQueued is returned by ForgetRepository and
// RepositoryRemovable for a repository with an unacknowledged housekeeping
// wake about it.
var ErrHousekeepingWakeQueued = errors.New("store: a housekeeping wake about the repository is unacknowledged")
