package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repository is the authoritative record of a project the gate validates.
//
// URLs are stored with credentials removed, per PRD section 8; the credentialed
// URL is recovered from the gate at run time and never rests here. What comes
// back from any accessor is the redacted form, so a caller cannot accidentally
// use a stored URL as if it still carried a credential.
type Repository struct {
	// ID is the caller's stable identifier for the repository.
	ID string `json:"id"`
	// WorkingPath is the primary checkout this repository stands for. It is
	// unique across repositories: UpsertRepository refuses with
	// ErrWorkingPathTaken when a different identifier claims a path one already
	// holds, and a UNIQUE index on the column stands behind that refusal so the
	// invariant survives a path that forgets to ask.
	WorkingPath string `json:"working_path"`
	// UpstreamURL is the remote the change is destined for, redacted.
	UpstreamURL string `json:"upstream_url"`
	// ForkURL is the fork pushed to when one is used, redacted. It is unknown
	// when no fork is involved.
	ForkURL Optional[string] `json:"fork_url"`
	// DefaultBranch is the branch PRD principle P7 reads trusted configuration
	// from.
	DefaultBranch string `json:"default_branch"`
	// CreatedAt is when the record was first written.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is when it was last written.
	UpdatedAt time.Time `json:"updated_at"`
}

// PushURL is the remote a run forwards its validated commit to: the fork when
// the record names one, and the upstream otherwise. It is the one answer to
// that question, so a reader verifying what a run pushed reads the same
// remote the run pushed to. Like both fields it reads, it is the redacted
// form.
func (r Repository) PushURL() string {
	if fork, ok := r.ForkURL.Get(); ok {
		return fork
	}
	return r.UpstreamURL
}

// UpsertRepository writes r and returns the record as stored, which for its two
// URL fields is the redacted form: UpstreamURL and ForkURL each pass through the
// redactor Open was given before they are bound to a statement, and nothing
// else is ever written to those two columns. The remaining fields are stored as
// supplied. Writing the same identifier again updates the row and keeps its
// original CreatedAt, including when it names the working path it already
// holds. A different identifier claiming that path is refused with
// ErrWorkingPathTaken, and the refusal writes nothing.
//
// This package does not decide what a credential looks like, per P14. What it
// guarantees is that the redactor runs over those two fields on the way in, and
// Open has already established that the redactor is not inert.
func (s *Store) UpsertRepository(ctx context.Context, r Repository) (Repository, error) {
	if strings.TrimSpace(r.ID) == "" {
		return Repository{}, fmt.Errorf("store: repository has no identifier")
	}
	if strings.TrimSpace(r.WorkingPath) == "" {
		return Repository{}, fmt.Errorf("store: repository %s has no working path", r.ID)
	}
	if strings.TrimSpace(r.DefaultBranch) == "" {
		return Repository{}, fmt.Errorf("store: repository %s has no default branch", r.ID)
	}

	upstream, err := s.safeURL(r.UpstreamURL, "upstream_url")
	if err != nil {
		return Repository{}, err
	}
	r.UpstreamURL = upstream

	if fork, ok := r.ForkURL.Get(); ok {
		safe, err := s.safeURL(fork, "fork_url")
		if err != nil {
			return Repository{}, err
		}
		r.ForkURL = Known(safe)
	}

	now := nowUTC()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var holder string
		switch err := tx.QueryRowContext(ctx,
			`SELECT id FROM repository WHERE working_path = ?`, r.WorkingPath).Scan(&holder); {
		case isNoRows(err):
		case err != nil:
			return err
		case holder != r.ID:
			return fmt.Errorf("%w: %s is the checkout of repository %s",
				ErrWorkingPathTaken, r.WorkingPath, holder)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO repository (id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				working_path   = excluded.working_path,
				upstream_url   = excluded.upstream_url,
				fork_url       = excluded.fork_url,
				default_branch = excluded.default_branch,
				updated_at     = excluded.updated_at`,
			r.ID, r.WorkingPath, r.UpstreamURL, r.ForkURL, r.DefaultBranch,
			encodeTime(now), encodeTime(now))
		return err
	})
	if err != nil {
		return Repository{}, fmt.Errorf("store: writing repository %s: %w", r.ID, err)
	}
	return s.Repository(ctx, r.ID)
}

// safeURL is the one path the repository URL columns take into this database.
// It is one of the three places the redactor runs; the other two are the
// message text and refusal columns, and doc.go names all four. Every other
// column is bound exactly as the caller supplied it.
func (s *Store) safeURL(raw, column string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("store: %s is empty", column)
	}
	return s.redact.Redact(raw), nil
}

// Repository returns the repository with the given identifier, or ErrNotFound.
func (s *Store) Repository(ctx context.Context, id string) (Repository, error) {
	if err := s.live(); err != nil {
		return Repository{}, err
	}
	row := s.read.QueryRowContext(ctx, `
		SELECT id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at
		FROM repository WHERE id = ?`, id)
	r, err := scanRepository(row)
	if err != nil {
		return Repository{}, fmt.Errorf("store: reading repository %s: %w", id, errNoRows(err))
	}
	return r, nil
}

// RepositoryAt returns the repository whose primary checkout is workingPath,
// or ErrNotFound. The column is unique, so at most one record can answer.
//
// It is the one place "which repository is this working copy" is asked, so the
// service that resolves a run's repository and any surface that reports
// whether a run could start cannot answer differently. The path is matched as
// it was stored: this package resolves nothing, and a caller that has two
// spellings of one directory settles that before it asks.
func (s *Store) RepositoryAt(ctx context.Context, workingPath string) (Repository, error) {
	if err := s.live(); err != nil {
		return Repository{}, err
	}
	row := s.read.QueryRowContext(ctx, `
		SELECT id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at
		FROM repository WHERE working_path = ?`, workingPath)
	r, err := scanRepository(row)
	if err != nil {
		return Repository{}, fmt.Errorf("store: reading the repository at %s: %w", workingPath, errNoRows(err))
	}
	return r, nil
}

// Repositories returns every repository, ordered by working path.
func (s *Store) Repositories(ctx context.Context) ([]Repository, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at
		FROM repository ORDER BY working_path`)
	if err != nil {
		return nil, fmt.Errorf("store: listing repositories: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Repository
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing repositories: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing repositories: %w", err)
	}
	return out, nil
}

// scanner is what a *sql.Row and a *sql.Rows have in common, so one scan
// function serves both the single read and the listing.
type scanner interface {
	Scan(dest ...any) error
}

func scanRepository(sc scanner) (Repository, error) {
	var r Repository
	var created, updated string
	if err := sc.Scan(&r.ID, &r.WorkingPath, &r.UpstreamURL, &r.ForkURL, &r.DefaultBranch, &created, &updated); err != nil {
		return Repository{}, err
	}
	var err error
	if r.CreatedAt, err = decodeTime(created); err != nil {
		return Repository{}, err
	}
	if r.UpdatedAt, err = decodeTime(updated); err != nil {
		return Repository{}, err
	}
	return r, nil
}

// ForgetRepository removes a repository and everything recorded against its
// runs, in one transaction, and reports how many runs went with it.
//
// PRD section 9's eject removes a gate and its records, and this is the second
// half of that: internal/gate removes the repository on disk and the working
// copy's binding, and this removes what the gate recorded. What a run's
// records are is runRecords, and every table that keeps rows per run is either
// there or one of the refusals below; forgetrows_test.go derives that set from
// the catalog, so a table a later migration adds fails until it is placed.
//
// It refuses with ErrRepositoryInUse when a task names one of those runs.
// A task is fleet work with a life of its own, and a row pointing at a run
// that no longer exists is a record that has quietly stopped meaning anything;
// removing the task instead would be this accessor deciding the fate of
// something it does not own. The refusal names the task, so a caller can deal
// with it and ask again.
//
// It refuses with ErrRunActive when one of those runs has not finished, because
// a run in flight is one a service is still driving and the records it is about
// to write would land against a repository that is gone.
//
// It refuses with ErrRunProcessRecorded when a run process record of one of
// those runs is still there: an agent group the run started whose end was
// never recorded, and the record is the only thing a later recovery reaps it
// from.
//
// It refuses with *KeptWorkOpenError while a kept work entry of the
// repository is open, and with ErrKeptWorkWakeQueued while a kept-work wake
// raised about one is unacknowledged, closed entry or not, because the wake
// is actionable until the coordinator acknowledges it.
//
// Every refusal is decided before anything is removed, and the removal is one
// transaction, so a forget either takes the whole repository or changes
// nothing. It removes records and nothing on disk: a run cleanup record that
// says a copy was kept goes with its run, and whether that copy still stands
// is not something this package reads.
//
// A repository that is not there is not an error: the removal has nothing left
// to do, which is the same answer internal/gate gives for a remote that is
// already gone.
func (s *Store) ForgetRepository(ctx context.Context, id string) (int, error) {
	removed := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		runIDs, err := repositoryRunIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := refuseUnlessForgettable(ctx, tx, id, runIDs); err != nil {
			return err
		}
		// The written-once records may be removed only once their run row is
		// gone (forgetRunRecordsMigration), and run_cleanup also references that row, so
		// the two orders the triggers and the foreign keys ask for are
		// opposite. The foreign keys are checked at commit instead, which
		// still refuses a commit that would leave a row pointing at a run that
		// is gone; the setting ends with the transaction.
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return fmt.Errorf("store: forgetting repository %s: %w", id, err)
		}
		for _, runID := range runIDs {
			for _, r := range runRecords {
				if r.afterRun {
					continue
				}
				if err := r.forget(ctx, tx, id, runID); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM run WHERE id = ?`, runID); err != nil {
				return fmt.Errorf("store: forgetting run %s of repository %s: %w", runID, id, err)
			}
			for _, r := range runRecords {
				if !r.afterRun {
					continue
				}
				if err := r.forget(ctx, tx, id, runID); err != nil {
					return err
				}
			}
			removed++
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM repository_removal WHERE repository_id = ?`, id); err != nil {
			return fmt.Errorf("store: forgetting repository %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM repository WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: forgetting repository %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// runRecord is one table that keeps rows per run and goes with the run when
// its repository is forgotten.
type runRecord struct {
	// table is the table's name in the catalog.
	table string
	// remove deletes one run's rows; its one parameter is the run.
	remove string
	// afterRun is a written-once record whose seal lets a row go only once
	// its run row is gone, so it is removed after the run rather than before.
	afterRun bool
}

func (r runRecord) forget(ctx context.Context, tx *sql.Tx, repositoryID, runID string) error {
	if _, err := tx.ExecContext(ctx, r.remove, runID); err != nil {
		return fmt.Errorf("store: forgetting the %s rows of run %s of repository %s: %w", r.table, runID, repositoryID, err)
	}
	return nil
}

// runRecords is every table whose rows a run takes with it when its
// repository is forgotten, in the order they are removed. Every table that
// keeps rows per run is here or in refusedRunRecords, and
// forgetrows_test.go holds both lists to the catalog.
//
// merge_record and run_cleanup are written once and sealed against deletion,
// as is seen_mark, whose merge kind is keyed by the run. Their seals exist so
// no write makes a row say other than what the read, the cleanup decision or
// the mark saw, and PRD section 8's "appended" is rows added and never
// edited; neither says such a row outlives its run. forgetRunRecordsMigration
// narrows each delete seal to "while its run (or the merge record it marks)
// stands", which keeps refused every deletion that would leave a record
// disagreeing with what it describes, and this is the one path that removes
// the run.
//
// checkpoint is migration 6's sealed table and holds no row; its delete is
// here so the table is accounted for rather than skipped.
var runRecords = []runRecord{
	{table: "hold", remove: `DELETE FROM hold WHERE run_id = ?`},
	{table: "round", remove: `DELETE FROM round WHERE run_id = ?`},
	{table: "stage_result", remove: `DELETE FROM stage_result WHERE run_id = ?`},
	{table: "checkpoint", remove: `DELETE FROM checkpoint WHERE run_id = ?`},
	{table: "graph_checkpoint", remove: `DELETE FROM graph_checkpoint WHERE run = ?`},
	{table: "run_copy_sweep", remove: `DELETE FROM run_copy_sweep WHERE run = ?`},
	{table: "stage_execution", remove: `DELETE FROM stage_execution WHERE run = ?`},
	// A follow-up is keyed by the answer it follows, so it goes first.
	{table: "decision_followup", remove: `DELETE FROM decision_followup WHERE answer IN (SELECT seq FROM decision_answer WHERE run = ?)`},
	{table: "decision_answer", remove: `DELETE FROM decision_answer WHERE run = ?`},
	{table: "checks_wait", remove: `DELETE FROM checks_wait WHERE run = ?`},
	// An agent invocation names the run it served where it served one, and
	// goes with that run like the rest of its records; a call that named no
	// run is not this repository's to forget, and stays.
	{table: "agent_invocation", remove: `DELETE FROM agent_invocation WHERE run = ?`},
	{table: "merge_record", remove: `DELETE FROM merge_record WHERE run = ?`, afterRun: true},
	{table: "run_cleanup", remove: `DELETE FROM run_cleanup WHERE run = ?`, afterRun: true},
	{table: "run_rebase", remove: `DELETE FROM run_rebase WHERE run = ?`, afterRun: true},
	// A kept work entry about a run's copy goes with the run only once it is
	// answered or closed on a proof: refuseUnlessForgettable refuses a
	// repository with an open one before anything is removed, and the
	// delete's own filter and the table's seal hold the same line again.
	// The kept-work wake raised about such an entry, and its notification,
	// go with it and before it, on the same filter, which their seals hold
	// too, and only once the wake is acknowledged: refuseUnlessForgettable
	// refuses a repository with one still queued.
	{table: "kept_work_notification", remove: `DELETE FROM kept_work_notification WHERE kept_work_wake_id IN (
		SELECT w.id FROM kept_work_wake w JOIN kept_work k ON k.id = w.kept_work
		WHERE k.run_id = ? AND (k.answer IS NOT NULL OR k.closure IS NOT NULL))`, afterRun: true},
	{table: "kept_work_wake", remove: `DELETE FROM kept_work_wake WHERE kept_work IN (
		SELECT id FROM kept_work WHERE run_id = ? AND (answer IS NOT NULL OR closure IS NOT NULL))`, afterRun: true},
	{table: "kept_work", remove: `DELETE FROM kept_work WHERE run_id = ? AND (answer IS NOT NULL OR closure IS NOT NULL)`, afterRun: true},
	// A seen mark of a merge record is keyed by the run through its kind,
	// which the catalog cannot say, so it is named here; it goes after the
	// merge record it marks, which its seal requires.
	{table: "seen_mark", remove: `DELETE FROM seen_mark WHERE kind = 'merge' AND key = ?`, afterRun: true},
}

// refusedRunRecords is every table that keeps rows per run and whose row
// stops a repository being forgotten rather than going with it, with the
// refusal refuseUnlessForgettable answers for it. Nothing reachable only
// through one of these tables is ever forgotten, because the refusal stands
// in front of it.
var refusedRunRecords = map[string]error{
	"task":        ErrRepositoryInUse,
	"run_process": ErrRunProcessRecorded,
}

// RepositoryRemovable reports what would stop ForgetRepository, and removes
// nothing. It returns the same ErrRepositoryInUse, ErrRunActive,
// ErrRunProcessRecorded, *KeptWorkOpenError and ErrKeptWorkWakeQueued
// refusals, against the same rows, so a caller with something of its own to destroy can
// find out first rather than destroy it and then be refused.
//
// It is a question and not a reservation. Nothing is held between the answer
// and a later ForgetRepository, so a run started in between is refused there,
// which is where the refusal is authoritative: this reports what is true now.
//
// A repository that is not there is removable, on the same terms
// ForgetRepository takes it: there is nothing left to do.
func (s *Store) RepositoryRemovable(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		runIDs, err := repositoryRunIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		return refuseUnlessForgettable(ctx, tx, id, runIDs)
	})
}

// RemovalOwner is the process holding a repository's removal reservation:
// its identifier and the platform's reading of its start time, the pair
// internal/agents' Identity carries, so a service opening the home can tell
// a live eject from one that died. A zero Start is a platform that could read
// none, which no later check can verify, and such a reservation is released
// at the next open.
type RemovalOwner struct {
	PID   int
	Start uint64
}

// RepositoryRemoval is one reservation ReserveRepositoryRemoval wrote.
type RepositoryRemoval struct {
	Repository string
	Owner      RemovalOwner
	ReservedAt time.Time
}

// ReserveRepositoryRemoval is eject's preflight when it means to act: it
// asks refuseUnlessForgettable and, in the same transaction, reserves the
// repository's removal for owner, so CreateRun refuses a run of it from then
// on until ForgetRepository removes the reservation with the repository,
// ReleaseRepositoryRemoval clears it, or a service opening the home finds
// its owner gone. An entry committed first refuses the reservation with
// *KeptWorkOpenError; no new run copy can appear once it stands. It returns
// the same refusals RepositoryRemovable does, takes the reservation over
// for owner when one stands, and answers nil with nothing written for a
// repository that is not there.
func (s *Store) ReserveRepositoryRemoval(ctx context.Context, id string, owner RemovalOwner) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM repository WHERE id = ?`, id).Scan(&one); {
		case isNoRows(err):
			return nil
		case err != nil:
			return fmt.Errorf("store: reserving the removal of repository %s: %w", id, err)
		}
		runIDs, err := repositoryRunIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := refuseUnlessForgettable(ctx, tx, id, runIDs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repository_removal (repository_id, owner_pid, owner_start, reserved_at)
			VALUES (?, ?, ?, ?) ON CONFLICT (repository_id) DO UPDATE SET
				owner_pid = excluded.owner_pid, owner_start = excluded.owner_start, reserved_at = excluded.reserved_at`,
			id, owner.PID, int64(owner.Start), encodeTime(nowUTC())); err != nil {
			return fmt.Errorf("store: reserving the removal of repository %s: %w", id, err)
		}
		return nil
	})
}

// RepositoryRemovals returns every reservation standing, for the service
// that releases the ones whose owner is gone when it opens.
func (s *Store) RepositoryRemovals(ctx context.Context) ([]RepositoryRemoval, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT repository_id, owner_pid, owner_start, reserved_at FROM repository_removal ORDER BY repository_id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the repository removals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RepositoryRemoval
	for rows.Next() {
		var r RepositoryRemoval
		var start int64
		var at string
		if err := rows.Scan(&r.Repository, &r.Owner.PID, &start, &at); err != nil {
			return nil, fmt.Errorf("store: reading the repository removals: %w", err)
		}
		r.Owner.Start = uint64(start)
		if r.ReservedAt, err = decodeTime(at); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the repository removals: %w", err)
	}
	return out, nil
}

// ReleaseRepositoryRemoval clears the reservation owner holds of the
// repository's removal, for an eject that did not complete or whose owner is
// gone, and reports whether it cleared one. It is anchored on the owner: the
// one statement deletes the row only while it still names that process, so
// a reservation another eject took over since the caller read it stands.
// Clearing one that is not there, or not that owner's, is not an error.
func (s *Store) ReleaseRepositoryRemoval(ctx context.Context, id string, owner RemovalOwner) (bool, error) {
	released := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM repository_removal WHERE repository_id = ? AND owner_pid = ? AND owner_start = ?`,
			id, owner.PID, int64(owner.Start))
		if err != nil {
			return fmt.Errorf("store: releasing the removal of repository %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		released = n == 1
		return err
	})
	return released, err
}

// refuseUnlessForgettable is the whole of what stops a repository being
// forgotten. It is one function so that the question asked before a removal
// and the check made inside it cannot answer differently.
func refuseUnlessForgettable(ctx context.Context, tx *sql.Tx, id string, runIDs []string) error {
	// A task attributed to the repository by its branch resolves from its
	// runs, so removing them would leave it resolving from its log alone.
	var taskID string
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM task WHERE repository_id = ? LIMIT 1`, id).Scan(&taskID); {
	case err == nil:
		return fmt.Errorf("%w: task %s works on a branch of repository %s", ErrRepositoryInUse, taskID, id)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("store: checking which tasks work on repository %s: %w", id, err)
	}
	if err := refuseIfKeptWorkIsOpen(ctx, tx, id); err != nil {
		return err
	}
	if err := refuseIfAKeptWorkWakeIsQueued(ctx, tx, id); err != nil {
		return err
	}
	for _, runID := range runIDs {
		if err := refuseIfRunIsHeldByATask(ctx, tx, id, runID); err != nil {
			return err
		}
		if err := refuseIfRunIsActive(ctx, tx, id, runID); err != nil {
			return err
		}
		if err := refuseIfRunHasAProcessRecord(ctx, tx, id, runID); err != nil {
			return err
		}
	}
	return nil
}

// refuseIfAKeptWorkWakeIsQueued stops a removal that would delete an
// unacknowledged kept-work wake, and its notification, about a kept work
// entry of the repository: one whose project is the repository or whose run
// is one of its runs. The entry may be closed, its work landed, while the
// wake about it has not been acted on, and PRD section 8 keeps the wake
// actionable until it is acknowledged.
func refuseIfAKeptWorkWakeIsQueued(ctx context.Context, tx *sql.Tx, repositoryID string) error {
	var wake, entry int64
	err := tx.QueryRowContext(ctx, `SELECT w.id, k.id FROM kept_work_wake w JOIN kept_work k ON k.id = w.kept_work
		WHERE w.handled_at IS NULL AND (k.repository_id = ? OR k.run_id IN (SELECT id FROM run WHERE repository_id = ?))
		ORDER BY w.id LIMIT 1`, repositoryID, repositoryID).Scan(&wake, &entry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: checking the kept-work wakes of repository %s: %w", repositoryID, err)
	}
	return fmt.Errorf("%w: kept-work wake %d, raised about kept work entry %d of repository %s, is still queued; "+
		"once the coordinator acknowledges it, the repository's records can be removed",
		ErrKeptWorkWakeQueued, wake, entry, repositoryID)
}

// repositoryRunIDs returns the identifiers of every run of one repository.
func repositoryRunIDs(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM run WHERE repository_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("store: listing the runs of repository %s: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("store: listing the runs of repository %s: %w", id, err)
		}
		ids = append(ids, runID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing the runs of repository %s: %w", id, err)
	}
	return ids, nil
}

// refuseIfRunIsHeldByATask stops a removal that would leave a task pointing at
// a run that no longer exists.
func refuseIfRunIsHeldByATask(ctx context.Context, tx *sql.Tx, repositoryID, runID string) error {
	var taskID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM task WHERE run_id = ? LIMIT 1`, runID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: checking what holds run %s: %w", runID, err)
	}
	return fmt.Errorf("%w: task %s names run %s of repository %s",
		ErrRepositoryInUse, taskID, runID, repositoryID)
}

// refuseIfRunIsActive stops a removal that would take a run a service is still
// driving.
func refuseIfRunIsActive(ctx context.Context, tx *sql.Tx, repositoryID, runID string) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM run WHERE id = ?`, runID).Scan(&status); err != nil {
		return fmt.Errorf("store: reading the status of run %s: %w", runID, err)
	}
	switch RunStatus(status) {
	case RunPending, RunRunning, RunHeld:
		return fmt.Errorf("%w: run %s of repository %s is %s", ErrRunActive, runID, repositoryID, status)
	case RunPassed, RunFailed, RunTerminated:
		return nil
	default:
		// A status this build does not define is one nothing here can say has
		// finished, so it is treated as a run that may still move.
		return fmt.Errorf("%w: run %s of repository %s is %q, which this build does not recognize",
			ErrRunActive, runID, repositoryID, status)
	}
}

// refuseIfRunHasAProcessRecord stops a removal that would forget an agent
// group a run started and whose end was never recorded. The run process
// record is what a later recovery reaps that group from (migration 19), so
// removing it would leave a process nothing here can find again. A record
// stands only until a recovery verifies its process ended or gone, so the
// refusal names the group for the caller to see to first.
func refuseIfRunHasAProcessRecord(ctx context.Context, tx *sql.Tx, repositoryID, runID string) error {
	var group int
	err := tx.QueryRowContext(ctx, `SELECT group_id FROM run_process WHERE run_id = ? ORDER BY group_id LIMIT 1`, runID).Scan(&group)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: checking the process records of run %s: %w", runID, err)
	}
	return fmt.Errorf("%w: process group %d of run %s of repository %s", ErrRunProcessRecorded, group, runID, repositoryID)
}
