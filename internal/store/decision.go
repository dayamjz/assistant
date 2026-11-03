package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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
	// Batch is the batch of the review that asked, empty only on a row
	// appended before migration 71 kept it. A DecisionBatch of that name
	// exists only once the review finished its checks.
	Batch string
	// Measures is what the call measured, unknown on a row appended before
	// migration 70 kept it and on a path the stage could not ask about.
	Measures Optional[DecisionMeasures]
	// Rate is the rate the call was priced at, unknown when it was not
	// priced; AppendDecisionAnswer keeps one only on a priced row. The price
	// is the reported tokens at it, so no column repeats it.
	Rate Optional[DecisionRate]
	// Recorded is when the row was appended, set by this package.
	Recorded time.Time
}

// DecisionMeasures is what one decision-model call measured, each field as
// its namesake in internal/judge's Record and Usage, stored as handed over.
type DecisionMeasures struct {
	Requests, ReportedRequests, UnreportedRequests                                          int
	ReportedInputTokens, ReportedOutputTokens, UnreportedSentBytes, UnreportedReceivedBytes int64
	Elapsed                                                                                 time.Duration
}

// DecisionRate is the rate a call was priced at, as internal/judge's Rate has
// it: US dollars per million input and output tokens, and their source.
type DecisionRate struct {
	InputUSDPerMillionTokens, OutputUSDPerMillionTokens float64
	Source                                              string
}

// PricedAt says why a call measured m is not priced at r, or nil when it is.
// It is the rule's one statement: internal/judge asks it before giving a
// record a rate, and AppendDecisionAnswer refuses a rate it does not accept.
func PricedAt(m DecisionMeasures, r DecisionRate) error {
	in, out := r.InputUSDPerMillionTokens, r.OutputUSDPerMillionTokens
	switch {
	case m.ReportedRequests != m.Requests || m.UnreportedRequests != 0:
		return errors.New("store: a request of the call reported no tokens")
	case strings.TrimSpace(r.Source) == "":
		return errors.New("store: the rate names no source")
	case !(in >= 0 && out >= 0) || math.IsInf(in+out, 1):
		return errors.New("store: a rate's figure is not finite and at or above zero")
	}
	return nil
}

// measureArgs is a's measure and rate columns, each group all NULL when it is
// unknown.
func measureArgs(a DecisionAnswer) []any {
	args := make([]any, 11)
	if m, ok := a.Measures.Get(); ok {
		copy(args, []any{m.Requests, m.ReportedRequests, m.ReportedInputTokens, m.ReportedOutputTokens,
			m.UnreportedRequests, m.UnreportedSentBytes, m.UnreportedReceivedBytes, int64(m.Elapsed)})
	}
	if r, ok := a.Rate.Get(); ok {
		copy(args[8:], []any{r.InputUSDPerMillionTokens, r.OutputUSDPerMillionTokens, r.Source})
	}
	return args
}

// AppendDecisionAnswer appends one answer to the record and returns the row
// with the sequence and time it was assigned. It refuses a row naming no run,
// no stage, no batch, no check, or no model, because an answer nothing is attributed to
// answers no question the record exists for, and it refuses a row that is
// both an answer and a failure, or neither, because the record would then say
// two things about one call or nothing at all, and a rate PricedAt refuses.
func (s *Store) AppendDecisionAnswer(ctx context.Context, a DecisionAnswer) (DecisionAnswer, error) {
	for _, field := range []struct{ name, value string }{
		{"run", a.Run}, {"stage", a.Stage}, {"batch", a.Batch}, {"check", a.Check}, {"model", a.Model},
	} {
		if strings.TrimSpace(field.value) == "" {
			return DecisionAnswer{}, fmt.Errorf("store: decision answer has no %s", field.name)
		}
	}
	if a.Probability.IsKnown() == (a.Failure != "") {
		return DecisionAnswer{}, fmt.Errorf("store: a decision answer is a probability or a failure, exactly one")
	}
	if r, ok := a.Rate.Get(); ok {
		m, measured := a.Measures.Get()
		if !measured {
			return DecisionAnswer{}, errors.New("store: a decision answer's rate is on a row with no measures")
		}
		if err := PricedAt(m, r); err != nil {
			return DecisionAnswer{}, fmt.Errorf("store: a decision answer's rate is on a row that was not priced: %w", err)
		}
	}
	a.Recorded = time.Now().UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO decision_answer (
				run, stage, check_name, path, model, probability, cutoff, failure, finding, recorded_at,
				requests, reported_requests, reported_input_tokens, reported_output_tokens,
				unreported_requests, unreported_sent_bytes, unreported_received_bytes, elapsed_ns,
				rate_input_usd_per_million_tokens, rate_output_usd_per_million_tokens, rate_source, batch)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append(append([]any{a.Run, a.Stage, a.Check, a.Path, a.Model, a.Probability, a.Cutoff,
				a.Failure, a.Finding, encodeTime(a.Recorded)}, measureArgs(a)...), a.Batch)...)
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
		SELECT seq, run, stage, check_name, path, model, probability, cutoff, failure, finding, recorded_at,
			elapsed_ns IS NOT NULL, COALESCE(requests, 0), COALESCE(reported_requests, 0),
			COALESCE(reported_input_tokens, 0), COALESCE(reported_output_tokens, 0),
			COALESCE(unreported_requests, 0), COALESCE(unreported_sent_bytes, 0),
			COALESCE(unreported_received_bytes, 0), COALESCE(elapsed_ns, 0),
			rate_source IS NOT NULL, COALESCE(rate_input_usd_per_million_tokens, 0),
			COALESCE(rate_output_usd_per_million_tokens, 0), COALESCE(rate_source, ''), COALESCE(batch, '')
		FROM decision_answer WHERE run = ? ORDER BY seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading decision answers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DecisionAnswer
	for rows.Next() {
		var a DecisionAnswer
		var m DecisionMeasures
		var r DecisionRate
		var recorded string
		var measured, priced bool
		var elapsed int64
		if err := rows.Scan(&a.Seq, &a.Run, &a.Stage, &a.Check, &a.Path, &a.Model,
			&a.Probability, &a.Cutoff, &a.Failure, &a.Finding, &recorded, &measured,
			&m.Requests, &m.ReportedRequests, &m.ReportedInputTokens, &m.ReportedOutputTokens,
			&m.UnreportedRequests, &m.UnreportedSentBytes, &m.UnreportedReceivedBytes, &elapsed,
			&priced, &r.InputUSDPerMillionTokens, &r.OutputUSDPerMillionTokens, &r.Source, &a.Batch); err != nil {
			return nil, fmt.Errorf("store: reading decision answers: %w", err)
		}
		if measured {
			m.Elapsed = time.Duration(elapsed)
			a.Measures = Known(m)
		}
		if priced {
			a.Rate = Known(r)
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

// DecisionBatch is one review's pass over the decision-model checks as a
// whole, as internal/judge's Batch has it: how long they took, and whether
// they could send. It is history on DecisionAnswer's terms. A review that
// reaches the checks and is not stopped during them writes one, its checks
// on, off, or with no key, and Sends is what tells the first apart from the
// other two, not whether an answer names the batch.
// How many requests the checks sent is the kept answers' naming its Batch,
// so no column here repeats it. A Batch an answer names may have no row: the
// review was stopped during the checks, or this row was refused, and the
// record does not say which.
type DecisionBatch struct {
	// Seq is the row's position, assigned on append.
	Seq int64
	// Batch is the name the review drew, which its answers carry.
	Batch string
	// Run, Stage, and Mode are the run, the stage that asked, and its
	// decision_model mode.
	Run, Stage, Mode string
	// Sends is whether the checks were on with a key, so requests could leave.
	Sends bool
	// Elapsed is how long the checks took the stage.
	Elapsed time.Duration
	// Recorded is when the row was appended, set by this package.
	Recorded time.Time
}

// AppendDecisionBatch appends one batch and returns the row with the sequence
// and time it was assigned. It refuses a row naming no batch, run, stage, or
// mode, a batch already appended, or a negative time.
func (s *Store) AppendDecisionBatch(ctx context.Context, b DecisionBatch) (DecisionBatch, error) {
	if strings.TrimSpace(b.Batch) == "" || strings.TrimSpace(b.Run) == "" || strings.TrimSpace(b.Stage) == "" ||
		strings.TrimSpace(b.Mode) == "" {
		return DecisionBatch{}, fmt.Errorf("store: a decision batch names a batch, a run, a stage, and a mode")
	}
	b.Recorded = time.Now().UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO decision_batch (batch, run, stage, mode, sends, elapsed_ns, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			b.Batch, b.Run, b.Stage, b.Mode, b.Sends, int64(b.Elapsed), encodeTime(b.Recorded))
		if err != nil {
			return err
		}
		b.Seq, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return DecisionBatch{}, fmt.Errorf("store: appending a decision batch: %w", err)
	}
	return b, nil
}

// DecisionBatches returns one run's batches in the order they were appended.
func (s *Store) DecisionBatches(ctx context.Context, run string) ([]DecisionBatch, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT seq, batch, run, stage, mode, sends, elapsed_ns, recorded_at
		FROM decision_batch WHERE run = ? ORDER BY seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading decision batches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DecisionBatch
	for rows.Next() {
		var b DecisionBatch
		var elapsed int64
		var recorded string
		if err := rows.Scan(&b.Seq, &b.Batch, &b.Run, &b.Stage, &b.Mode, &b.Sends, &elapsed, &recorded); err != nil {
			return nil, fmt.Errorf("store: reading decision batches: %w", err)
		}
		b.Elapsed = time.Duration(elapsed)
		if b.Recorded, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: decision batch %d: %w", b.Seq, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading decision batches: %w", err)
	}
	return out, nil
}
