package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DecisionAnswer is one row of the decision-model record: what one check
// answered, or why it did not, for one path of one run. It is history: nothing
// reads it back to decide what a run may do, and nothing here revises a row.
//
// The vocabulary fields are text whose meaning belongs to internal/judge and
// to the review stage, the way AgentInvocation's belong to internal/agents:
// Check, Failure, and Finding are stored as handed over and decoded into
// nothing a caller could branch on here.
type DecisionAnswer struct {
	// Seq is the row's position in the record, assigned on append and counted
	// from one. A caller never supplies it.
	Seq int64
	// Run is the run the check was asked for.
	Run string
	// Stage is the stage that asked.
	Stage string
	// Check is the question asked.
	Check string
	// Path is the repository-relative path the question was about.
	Path string
	// Model is the model version the question was put to.
	Model string
	// Probability is the answer, unknown for a call that produced none.
	Probability Optional[float64]
	// Cutoff is the cutoff the answer was compared against.
	Cutoff float64
	// Failure is why the call produced no answer, empty when it produced one.
	Failure string
	// Finding is the action of the finding the answer produced, empty when it
	// produced none.
	Finding string
	// Recorded is when the row was appended, set by this package.
	Recorded time.Time
}

// AppendDecisionAnswer appends one answer to the record and returns the row
// with the sequence and time it was assigned. It refuses a row naming no run,
// no stage, no check, or no model, because an answer nothing is attributed to
// answers no question the record exists for, and it refuses a row that is
// both an answer and a failure, or neither, because the record would then say
// two things about one call or nothing at all.
func (s *Store) AppendDecisionAnswer(ctx context.Context, a DecisionAnswer) (DecisionAnswer, error) {
	for _, field := range []struct{ name, value string }{
		{"run", a.Run}, {"stage", a.Stage}, {"check", a.Check}, {"model", a.Model},
	} {
		if strings.TrimSpace(field.value) == "" {
			return DecisionAnswer{}, fmt.Errorf("store: decision answer has no %s", field.name)
		}
	}
	if a.Probability.IsKnown() == (a.Failure != "") {
		return DecisionAnswer{}, fmt.Errorf("store: a decision answer is a probability or a failure, exactly one")
	}
	a.Recorded = time.Now().UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO decision_answer (
				run, stage, check_name, path, model, probability, cutoff, failure, finding, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Run, a.Stage, a.Check, a.Path, a.Model, a.Probability, a.Cutoff,
			a.Failure, a.Finding, encodeTime(a.Recorded))
		if err != nil {
			return err
		}
		a.Seq, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return DecisionAnswer{}, fmt.Errorf("store: appending a decision answer: %w", err)
	}
	return a, nil
}

// DecisionAnswers returns the record for one run in the order it was
// appended.
func (s *Store) DecisionAnswers(ctx context.Context, run string) ([]DecisionAnswer, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT seq, run, stage, check_name, path, model, probability, cutoff, failure, finding, recorded_at
		FROM decision_answer WHERE run = ? ORDER BY seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading decision answers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DecisionAnswer
	for rows.Next() {
		var a DecisionAnswer
		var recorded string
		if err := rows.Scan(&a.Seq, &a.Run, &a.Stage, &a.Check, &a.Path, &a.Model,
			&a.Probability, &a.Cutoff, &a.Failure, &a.Finding, &recorded); err != nil {
			return nil, fmt.Errorf("store: reading decision answers: %w", err)
		}
		if a.Recorded, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: decision answer %d: %w", a.Seq, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading decision answers: %w", err)
	}
	return out, nil
}
