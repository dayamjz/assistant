package service

import (
	"context"

	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/store"
)

// decisionRecorder feeds the store's decision-model record, which is PRD
// section 5's record of every answer the checks got. It is the recorder the
// service's judge.Client is built with, so every answer a review stage records
// through that client lands one row.
//
// It holds the service rather than the store for the reason
// invocationRecorder does: the client is built before the store is open, and
// it records only once a run reviews something, by which time the store is
// the open one.
//
// Recording is best-effort by the shape of the seam, and that is stated
// rather than hidden: judge.Recorder returns nothing, so a row that cannot be
// written is logged to the service log and dropped, and the record
// understates. No decision reads it.
type decisionRecorder struct {
	s *Service
}

// RecordAnswer stores one answer. It runs on the reviewing goroutine under the
// service's stop context, so a service that is closing does not block a
// review's last record.
func (r decisionRecorder) RecordAnswer(rec judge.Record) {
	st := r.s.store
	if st == nil {
		return
	}
	ctx := r.s.stopCtx
	if ctx == nil {
		ctx = context.Background()
	}
	probability := store.Unknown[float64]()
	if rec.Answered {
		probability = store.Known(rec.Probability)
	}
	_, err := st.AppendDecisionAnswer(ctx, store.DecisionAnswer{
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
}
