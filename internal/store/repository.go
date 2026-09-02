package store

import (
	"context"
	"database/sql"
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
	ID string
	// WorkingPath is the primary checkout this repository stands for. It is
	// unique across repositories.
	WorkingPath string
	// UpstreamURL is the remote the change is destined for, redacted.
	UpstreamURL string
	// ForkURL is the fork pushed to when one is used, redacted. It is unknown
	// when no fork is involved.
	ForkURL Optional[string]
	// DefaultBranch is the branch PRD principle P7 reads trusted configuration
	// from.
	DefaultBranch string
	// CreatedAt is when the record was first written.
	CreatedAt time.Time
	// UpdatedAt is when it was last written.
	UpdatedAt time.Time
}

// UpsertRepository writes r and returns the record as stored, which is the
// redacted form: every URL passes through the redactor Open was given before it
// is bound to a statement, and nothing else is ever written to a URL column.
// Writing the same identifier again updates the row and keeps its original
// CreatedAt.
//
// This package does not decide what a credential looks like, per P14. What it
// guarantees is that the redactor runs on the way in, and Open has already
// established that the redactor is not inert.
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

// safeURL is the one path a URL takes into this database.
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
