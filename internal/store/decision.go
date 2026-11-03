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
// no stage, no check, or no model, because an answer nothing is attributed to
// answers no question the record exists for, and it refuses a row that is
// both an answer and a failure, or neither, because the record would then say
// two things about one call or nothing at all, and a rate PricedAt refuses.
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
				rate_input_usd_per_million_tokens, rate_output_usd_per_million_tokens, rate_source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append([]any{a.Run, a.Stage, a.Check, a.Path, a.Model, a.Probability, a.Cutoff,
				a.Failure, a.Finding, encodeTime(a.Recorded)}, measureArgs(a)...)...)
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
			COALESCE(rate_output_usd_per_million_tokens, 0), COALESCE(rate_source, '')
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
			&priced, &r.InputUSDPerMillionTokens, &r.OutputUSDPerMillionTokens, &r.Source); err != nil {
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
