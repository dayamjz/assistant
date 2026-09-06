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

// safeURL is the one path the repository URL columns take into this database,
// and it is the only place the redactor runs. It says nothing about any other
// column: everything else this package stores is bound exactly as the caller
// supplied it.
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
// copy's binding, and this removes what the gate recorded.
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
		for _, runID := range runIDs {
			if err := refuseIfRunIsHeldByATask(ctx, tx, id, runID); err != nil {
				return err
			}
			if err := refuseIfRunIsActive(ctx, tx, id, runID); err != nil {
				return err
			}
		}
		// The order is the reference order reversed: every table that names a
		// run goes before the runs, and the runs go before the repository, so
		// nothing is ever left pointing at a row that has been removed.
		for _, runID := range runIDs {
			for _, statement := range []string{
				`DELETE FROM hold WHERE run_id = ?`,
				`DELETE FROM round WHERE run_id = ?`,
				`DELETE FROM stage_result WHERE run_id = ?`,
				`DELETE FROM graph_checkpoint WHERE run = ?`,
			} {
				if _, err := tx.ExecContext(ctx, statement, runID); err != nil {
					return fmt.Errorf("store: forgetting run %s of repository %s: %w", runID, id, err)
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM run WHERE id = ?`, runID); err != nil {
				return fmt.Errorf("store: forgetting run %s of repository %s: %w", runID, id, err)
			}
			removed++
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
