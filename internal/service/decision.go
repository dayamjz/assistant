package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// decisionRecorder feeds the store's decision-model record, which is PRD
// section 5's record of every answer the checks got, and reads it back for
// the one question the review stage asks of it. It is the recorder and the
// history the service's judge.Client is built with, so every answer a review
// stage records through that client lands one row.
//
// It holds the service rather than the store for the reason
// invocationRecorder does: the client is built before the store is open, and
// it records only once a run reviews something, by which time the store is
// the open one.
//
// A row the store refuses is logged and returned, and the review stage names
// it in a note on the run's report, so the record understating is visible
// where the run is read and not only in the service log. It is not retried,
// and no decision a run makes reads the record.
type decisionRecorder struct {
	s *Service
}

// RecordAnswer stores one answer. It runs on the reviewing goroutine under the
// service's stop context, so a service that is closing does not block a
// review's last record.
func (r decisionRecorder) RecordAnswer(rec judge.Record) error {
	st := r.s.store
	if st == nil {
		return fmt.Errorf("service: the store is not open")
	}
	probability := store.Unknown[float64]()
	if rec.Answered {
		probability = store.Known(rec.Probability)
	}
	_, err := st.AppendDecisionAnswer(r.s.recordCtx(), store.DecisionAnswer{
		Run:         rec.Run,
		Stage:       rec.Stage,
		Check:       string(rec.Check),
		Path:        rec.Path,
		Model:       rec.Model,
		Probability: probability,
		Cutoff:      rec.Cutoff,
		Failure:     string(rec.Failure),
		Finding:     rec.Finding,
	})
	if err != nil && r.s.log != nil {
		r.s.log.Printf("dropping a decision-model answer record: %v", err)
	}
	return err
}

// Firings answers judge.History from the store: the finished runs of the
// repository run validates, other than run, in which check fired, newest
// first. Which statuses are finished is internal/runs' answer, read rather
// than listed here.
func (r decisionRecorder) Firings(ctx context.Context, run string, check judge.Check) ([]judge.RunFirings, error) {
	st := r.s.store
	if st == nil {
		return nil, fmt.Errorf("service: the store is not open")
	}
	record, err := st.Run(ctx, run)
	if err != nil {
		return nil, err
	}
	rows, err := st.DecisionFirings(ctx, record.RepositoryID, string(check))
	if err != nil {
		return nil, err
	}
	var out []judge.RunFirings
	for _, row := range rows {
		if row.Run == run || !runs.Finished(row.Status) {
			continue
		}
		out = append(out, judge.RunFirings{Run: row.Run, Fired: row.Fired, Followed: row.Followed})
	}
	return out, nil
}

// recordCtx is the context the records this service keeps beside a run are
// written under: the service's stop context, so a service that is closing
// does not block a stage's last record, and the background otherwise.
func (s *Service) recordCtx() context.Context {
	if s.stopCtx == nil {
		return context.Background()
	}
	return s.stopCtx
}
