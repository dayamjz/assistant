package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RunStatus is where a run stands. It is the authoritative status, so it is
// written by the program that owns the run and read back by everyone else.
type RunStatus string

// The run statuses. A run is created pending, runs, and ends in exactly one of
// passed, failed, or terminated. Held is the state a run sits in while a hold
// waits on a person.
const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunHeld       RunStatus = "held"
	RunPassed     RunStatus = "passed"
	RunFailed     RunStatus = "failed"
	RunTerminated RunStatus = "terminated"
)

// runStatuses is the closed set Recognized answers from, in the order
// RunStatuses reports. A status outside it is one no reader can interpret, so
// it is refused where a run would be moved to it rather than stored and read
// back later as a word nobody defined.
var runStatuses = []RunStatus{
	RunPending, RunRunning, RunHeld, RunPassed, RunFailed, RunTerminated,
}

// RunStatuses returns the closed set in the order above. The result is a copy,
// so a caller cannot add to the set by writing to it.
func RunStatuses() []RunStatus { return slices.Clone(runStatuses) }

// Recognized reports whether s is one of the statuses this package defines.
func (s RunStatus) Recognized() bool { return slices.Contains(runStatuses, s) }

// String renders the status as it is stored.
func (s RunStatus) String() string { return string(s) }

// Run is the authoritative record of one validation of one branch.
//
// The fields that are Optional are the ones a run does not have when it is
// created and may never acquire. A run that failed before it pushed has no push
// binding, and that is not the same fact as a push binding of the empty string.
type Run struct {
	// ID is the caller's identifier for this run.
	ID string
	// RepositoryID names the repository the run validates.
	RepositoryID string
	// Branch is the branch that was pushed.
	Branch string
	// SubmittedHead is the commit the push carried, which is what P1 makes the
	// consent boundary.
	SubmittedHead string
	// Base is the commit the branch is measured against.
	Base string
	// CurrentHead is the branch tip as the run last observed it. It is unknown
	// until the run has observed one.
	CurrentHead Optional[string]
	// Status is where the run stands.
	Status RunStatus
	// ApprovedCommit is the commit a completed review approved. It is unknown
	// until a review completes, and it is deliberately a separate fact from the
	// current head, because approving one commit says nothing about a later
	// one.
	ApprovedCommit Optional[string]
	// PushBinding is what the run pushed and where, in the caller's own
	// notation. It is unknown until the run pushes. It is stored exactly as
	// given: the redactor runs on the repository URL columns and on nothing
	// else, so a caller that writes a credentialed remote here has stored the
	// credential.
	PushBinding Optional[string]
	// PullRequest is the pull request the run opened, in the caller's own
	// notation. It is unknown until one exists.
	PullRequest Optional[string]
	// FixerSession is the agent's opaque handle for the one durable fixer
	// session this run keeps across its fix rounds. It is unknown until a fix
	// round has reported one, and a run whose configuration asks for no
	// session reuse never acquires one.
	//
	// It is a reference rather than content: what it names lives with the
	// agent, and this column is what lets a restarted service continue the
	// same conversation instead of starting the run's fixer blind. It is
	// stored exactly as given, like every column but the repository URLs.
	FixerSession Optional[string]
	// Intent is what the run was for.
	Intent string
	// IntentSource says where that intent came from, so a report can say who
	// asked rather than only what was asked.
	IntentSource string
	// Build is the software that produced this run.
	Build Build
	// ConfigDigest identifies the configuration the run resolved, so a
	// surprising verdict can be traced to the settings that reached it as well
	// as to the code.
	ConfigDigest string
	// CreatedAt is when the run was recorded, which PRD section 8 requires to
	// precede the creation of its directory.
	CreatedAt time.Time
	// UpdatedAt is when the record last changed.
	UpdatedAt time.Time
}

// CreateRun records a new run and returns it as stored.
//
// It refuses a run that names no build with ErrBuildIdentityMissing and one
// that names no configuration with ErrConfigDigestMissing. PRD section 8
// requires a surprising result to be traceable to both, and a run recorded
// without them is a report nobody can trace, discovered long after the run that
// would have explained it.
func (s *Store) CreateRun(ctx context.Context, r Run) (Run, error) {
	if strings.TrimSpace(r.ID) == "" {
		return Run{}, fmt.Errorf("store: run has no identifier")
	}
	if strings.TrimSpace(r.Branch) == "" {
		return Run{}, fmt.Errorf("store: run %s has no branch", r.ID)
	}
	if strings.TrimSpace(r.SubmittedHead) == "" {
		return Run{}, fmt.Errorf("store: run %s has no submitted head", r.ID)
	}
	if err := r.Build.Validate(); err != nil {
		return Run{}, fmt.Errorf("store: run %s: %w", r.ID, err)
	}
	if strings.TrimSpace(r.ConfigDigest) == "" {
		return Run{}, fmt.Errorf("store: run %s: %w", r.ID, ErrConfigDigestMissing)
	}
	if r.Status == "" {
		r.Status = RunPending
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO run (
				id, repository_id, branch, submitted_head, base, current_head, status,
				approved_commit, push_binding, pull_request, fixer_session, intent, intent_source,
				build_version, build_revision, build_modified, build_go, config_digest,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.RepositoryID, r.Branch, r.SubmittedHead, r.Base, r.CurrentHead, string(r.Status),
			r.ApprovedCommit, r.PushBinding, r.PullRequest, r.FixerSession, r.Intent, r.IntentSource,
			r.Build.Version, r.Build.Revision, boolToInt(r.Build.Modified), r.Build.Go, r.ConfigDigest,
			encodeTime(now), encodeTime(now))
		return err
	})
	if err != nil {
		return Run{}, fmt.Errorf("store: creating run %s: %w", r.ID, err)
	}
	return s.Run(ctx, r.ID)
}

// Run returns the run with the given identifier, or ErrNotFound.
func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	if err := s.live(); err != nil {
		return Run{}, err
	}
	row := s.read.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id)
	r, err := scanRun(row)
	if err != nil {
		return Run{}, fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
	}
	return r, nil
}

// RunsForRepository returns every run of one repository, newest first.
func (s *Store) RunsForRepository(ctx context.Context, repositoryID string) ([]Run, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		runColumns+` FROM run WHERE repository_id = ? ORDER BY created_at DESC, id DESC`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
	}
	return out, nil
}

// TransitionRun moves a run to a status, and does it anchored to what the
// caller believed the run's status was: from names every status the move is
// legal out of, and the move is refused when the run is in none of them.
//
// Reading the status and writing the new one happen in one transaction on the
// single writer connection, so two callers moving one run cannot both find it
// where they expected it and both write. That is what makes this the mechanism
// a lifecycle rule can be built on: a caller that read the status first and
// then wrote would be deciding against a status that may already have changed.
// Which moves are legal is not decided here. This package records what a run
// is and does not decide what a run may do next, so the set of statuses a move
// may be made out of comes from the caller with the move.
//
// A run already in the status it is being moved to is left exactly as it
// stands: nothing is written, updated_at does not move, and the run comes back
// unchanged with no error. So a transition retried after a failure that had
// already committed reports the state it established rather than a refusal,
// and from never has to name the destination to say so.
//
// It refuses an unknown run with ErrNotFound, a destination that is not one of
// the statuses above, an empty from, and a run in a status from does not name.
// That last refusal is a *RunStatusError naming what the run was actually in,
// so a caller can report the state it found rather than only that it was
// surprised.
func (s *Store) TransitionRun(ctx context.Context, id string, from []RunStatus, to RunStatus) (Run, error) {
	if !to.Recognized() {
		return Run{}, fmt.Errorf("store: run %s: %q is not a run status", id, to)
	}
	if len(from) == 0 {
		return Run{}, fmt.Errorf("store: run %s: a transition to %s names no status it may be made from", id, to)
	}
	var moved Run
	// Every refusal below says what it is on its own, so nothing wraps this
	// call as a whole: a *RunStatusError already names the run, where it
	// stands, and where it was to go, and wrapping it would print all three
	// twice.
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := scanRun(tx.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
		}
		if current.Status == to {
			moved = current
			return nil
		}
		if !slices.Contains(from, current.Status) {
			return &RunStatusError{Run: id, Expected: slices.Clone(from), Actual: current.Status, To: to}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE run SET status = ?, updated_at = ? WHERE id = ?`,
			string(to), encodeTime(nowUTC()), id); err != nil {
			return fmt.Errorf("store: moving run %s to %s: %w", id, to, err)
		}
		// Read back rather than assembling the answer, so what a caller is
		// handed is the row as it now stands and not this function's account
		// of what it should be.
		if moved, err = scanRun(tx.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id)); err != nil {
			return fmt.Errorf("store: reading run %s after moving it to %s: %w", id, to, err)
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return moved, nil
}

// SetRunFixerSession records the agent's handle for the run's durable fixer
// session, which is what lets a later fix round of the same run continue the
// same conversation after the process that opened it is gone.
//
// It records a reference the caller was given. Nothing here opens, validates,
// or reasons about a session: what the handle means is the agent adapter's,
// and which invocations may carry one is internal/agents' type split.
func (s *Store) SetRunFixerSession(ctx context.Context, id, reference string) error {
	return s.updateRun(ctx, id, "fixer_session", reference)
}

// SetRunHead records the branch tip the run has observed.
func (s *Store) SetRunHead(ctx context.Context, id, head string) error {
	return s.updateRun(ctx, id, "current_head", head)
}

// SetRunApprovedCommit records the commit a completed review approved.
func (s *Store) SetRunApprovedCommit(ctx context.Context, id, commit string) error {
	return s.updateRun(ctx, id, "approved_commit", commit)
}

// SetRunPushBinding records what the run pushed and where.
func (s *Store) SetRunPushBinding(ctx context.Context, id, binding string) error {
	return s.updateRun(ctx, id, "push_binding", binding)
}

// SetRunPullRequest records the pull request the run opened.
func (s *Store) SetRunPullRequest(ctx context.Context, id, pullRequest string) error {
	return s.updateRun(ctx, id, "pull_request", pullRequest)
}

// updateRun writes one column of one run. The column name is never a caller's
// string: it comes from the fixed set above, so no accessor composes SQL from
// input.
func (s *Store) updateRun(ctx context.Context, id, column, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("store: run %s: %s is empty", id, column)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE run SET `+column+` = ?, updated_at = ? WHERE id = ?`,
			value, encodeTime(nowUTC()), id)
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("store: updating run %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

const runColumns = `SELECT
	id, repository_id, branch, submitted_head, base, current_head, status,
	approved_commit, push_binding, pull_request, fixer_session, intent, intent_source,
	build_version, build_revision, build_modified, build_go, config_digest,
	created_at, updated_at`

func scanRun(sc scanner) (Run, error) {
	var r Run
	var status, created, updated string
	var modified int
	if err := sc.Scan(
		&r.ID, &r.RepositoryID, &r.Branch, &r.SubmittedHead, &r.Base, &r.CurrentHead, &status,
		&r.ApprovedCommit, &r.PushBinding, &r.PullRequest, &r.FixerSession, &r.Intent, &r.IntentSource,
		&r.Build.Version, &r.Build.Revision, &modified, &r.Build.Go, &r.ConfigDigest,
		&created, &updated,
	); err != nil {
		return Run{}, err
	}
	r.Status = RunStatus(status)
	r.Build.Modified = modified != 0
	var err error
	if r.CreatedAt, err = decodeTime(created); err != nil {
		return Run{}, err
	}
	if r.UpdatedAt, err = decodeTime(updated); err != nil {
		return Run{}, err
	}
	return r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
