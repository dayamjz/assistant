package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GateBinding records that one working copy is bound to one gate. It is
// authoritative: one row per working copy, rewritten in place.
//
// PRD section 8 files a gate under a hash of the working copy's path, which
// makes "where is this working copy's gate" answerable from the path alone and
// leaves "which working copies are bound to this gate" answerable from nothing
// at all. That second question is the one an operation asks before it adopts a
// gate or deletes one, and this table is what turns it from an inference into a
// read.
//
// A binding is a pointer rather than history. It says a working copy was bound
// at some point and has not been unbound since; it does not say the working
// copy is still there, still names the gate, or ever existed after the row was
// written. A caller that needs the present tense checks the working copy, and
// this record tells it which working copies are worth checking.
type GateBinding struct {
	// WorkingPath is the working copy the binding belongs to, as the caller
	// spelled it. It is the primary key: a working copy is bound to at most
	// one gate, so binding it again replaces the row.
	WorkingPath string
	// GateID is the gate's identifier, which is the name its repository
	// directory is filed under. Two bindings may name one gate at once, which
	// is what a working copy that moved without being unbound looks like.
	GateID string
	// BoundAt is when this working copy was first bound to any gate.
	BoundAt time.Time
	// UpdatedAt is when the binding was last written.
	UpdatedAt time.Time
}

// BindGate records that the working copy at workingPath is bound to the gate
// with the given identifier, and returns the binding as stored. Binding a
// working copy that is already bound replaces the gate it names and keeps its
// original BoundAt, so a working copy never holds two bindings.
//
// It writes what it is given. Whether the gate exists, whether the working copy
// exists, and whether either is what the caller thinks are questions this
// package does not own.
func (s *Store) BindGate(ctx context.Context, workingPath, gateID string) (GateBinding, error) {
	if strings.TrimSpace(workingPath) == "" {
		return GateBinding{}, fmt.Errorf("store: gate binding has no working path")
	}
	if strings.TrimSpace(gateID) == "" {
		return GateBinding{}, fmt.Errorf("store: gate binding for %s has no gate identifier", workingPath)
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO gate_binding (working_path, gate_id, bound_at, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(working_path) DO UPDATE SET
				gate_id    = excluded.gate_id,
				updated_at = excluded.updated_at`,
			workingPath, gateID, encodeTime(now), encodeTime(now))
		return err
	})
	if err != nil {
		return GateBinding{}, fmt.Errorf("store: binding %s to gate %s: %w", workingPath, gateID, err)
	}
	return s.GateBinding(ctx, workingPath)
}

// UnbindGate removes a working copy's binding. It returns ErrNotFound when
// there was no binding to remove, rather than reporting success over a row it
// never saw: a caller that has just deleted a gate needs to be able to tell
// "the index let go" from "the index never knew", and only the caller knows
// which of those is ordinary for it.
func (s *Store) UnbindGate(ctx context.Context, workingPath string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM gate_binding WHERE working_path = ?`, workingPath)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: unbinding %s: %w", workingPath, err)
	}
	return nil
}

// GateBinding returns the binding for a working copy, or ErrNotFound.
func (s *Store) GateBinding(ctx context.Context, workingPath string) (GateBinding, error) {
	if err := s.live(); err != nil {
		return GateBinding{}, err
	}
	row := s.read.QueryRowContext(ctx, gateBindingColumns+` FROM gate_binding WHERE working_path = ?`, workingPath)
	b, err := scanGateBinding(row)
	if err != nil {
		return GateBinding{}, fmt.Errorf("store: reading the gate binding of %s: %w", workingPath, errNoRows(err))
	}
	return b, nil
}

// GateBindings returns every working copy bound to the gate with this
// identifier, ordered by working path. An identifier nothing is bound to
// returns no bindings and no error: nobody being bound is an answer rather than
// a missing record.
func (s *Store) GateBindings(ctx context.Context, gateID string) ([]GateBinding, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		gateBindingColumns+` FROM gate_binding WHERE gate_id = ? ORDER BY working_path`, gateID)
	if err != nil {
		return nil, fmt.Errorf("store: listing the bindings of gate %s: %w", gateID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []GateBinding
	for rows.Next() {
		b, err := scanGateBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing the bindings of gate %s: %w", gateID, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing the bindings of gate %s: %w", gateID, err)
	}
	return out, nil
}

const gateBindingColumns = `SELECT working_path, gate_id, bound_at, updated_at`

func scanGateBinding(sc scanner) (GateBinding, error) {
	var b GateBinding
	var bound, updated string
	if err := sc.Scan(&b.WorkingPath, &b.GateID, &bound, &updated); err != nil {
		return GateBinding{}, err
	}
	var err error
	if b.BoundAt, err = decodeTime(bound); err != nil {
		return GateBinding{}, err
	}
	if b.UpdatedAt, err = decodeTime(updated); err != nil {
		return GateBinding{}, err
	}
	return b, nil
}
