package service

import (
	"context"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/store"
)

// invocationRecorder feeds the store's agent invocation history, which is PRD
// section 8's record of what each agent call cost. It is the recorder the
// default catalog's adapters are built with, so every invocation a run makes
// through them, failed and cancelled ones included, lands one row; a catalog
// the caller supplied builds its own adapters, so what those record is that
// caller's arrangement and not this type's.
//
// It holds the service rather than the store because the catalog is settled
// before the store is open: adapters call it only once a run invokes one,
// which is after Open finished, and by then the store is the open one. The
// translation below is the whole of what this type decides; the record's
// contents are agents.Record's contract, and each Count crosses as a known
// value only when the agent reported one, which is the distinction the
// store's nullable columns exist to keep.
//
// Recording is best-effort by the shape of the seam, and that is stated
// rather than hidden: agents.Recorder returns nothing, so a row that cannot
// be written is logged to the service log and dropped, and the history
// understates what was spent. It is history, not an authoritative record; no
// decision reads it, so a dropped row costs visibility and nothing else.
type invocationRecorder struct {
	s *Service
}

// RecordInvocation stores one invocation's cost. It runs on the invoking
// goroutine under the service's stop context, so a service that is closing
// does not block an agent's last report.
func (r invocationRecorder) RecordInvocation(rec agents.Record) {
	st := r.s.store
	if st == nil {
		return
	}
	ctx := r.s.stopCtx
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := st.AppendAgentInvocation(ctx, store.AgentInvocation{
		Purpose:             string(rec.Purpose),
		Agent:               rec.Agent,
		Model:               rec.Model,
		SessionUse:          string(rec.Session),
		Started:             rec.Started,
		Duration:            rec.Duration,
		Failure:             string(rec.Failure),
		InputTokens:         optionalCount(rec.Usage.InputTokens),
		OutputTokens:        optionalCount(rec.Usage.OutputTokens),
		CacheReadTokens:     optionalCount(rec.Usage.CacheReadTokens),
		CacheCreationTokens: optionalCount(rec.Usage.CacheCreationTokens),
		Turns:               optionalCount(rec.Usage.Turns),
	})
	if err != nil && r.s.log != nil {
		r.s.log.Printf("dropping an agent invocation record: %v", err)
	}
}

// optionalCount carries a reported count across as known and an unreported
// one as unknown, so a count the agent never reported cannot arrive in the
// history as a zero.
func optionalCount(c agents.Count) store.Optional[int64] {
	n, ok := c.Value()
	if !ok {
		return store.Unknown[int64]()
	}
	return store.Known(n)
}
