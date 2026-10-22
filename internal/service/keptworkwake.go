package service

import (
	"errors"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The supervisor loop's pass over the kept work no task names: PRD section
// 8's kept-work wake, the queue's third shape, which carries PRD section
// 6's "it surfaces at once ... through the private channel" for an entry
// about a run no task names, such as a person's own push, since the wake
// table's row names a task and such an entry is on none.
//
// # What a pass does
//
// After the tasks and the delegations, the pass takes the open entries that
// keptWorkByTask placed on no task and raises each once, as
// supervisor.KindKeptWork through supervisor.Classify, on
// store.AppendKeptWorkWake, which records when the entry surfaced in the
// wake's own transaction. An entry whose surfaced instant is already
// recorded is not raised again, whichever shape raised it, so an entry
// raised on a task's wake and then attributed to no task is raised once.
// An entry answered or closed between the pass's read and the append is
// owed no wake, and the store refuses it with nothing written. A row
// appended rings wakeBell, so an armed wakes.wait and the notifier read
// the queue again, and is pushed to the coordinator's wake stream.
//
// # Which coordinator, and what closes it
//
// Every kept-work wake reaches the first coordinator: its kind is
// decision-class, and the entry names no task and so no domain, so
// internal/store offers a subordinate's drain none. The coordinator that
// drained it acknowledges it after acting on it, which closes the wake and
// answers nothing: the entry stays open, and status and the catch-up read
// list it, until it is answered or closed on a proof.

// superviseUntaskedKeptWork raises each open entry no task names that has
// not surfaced, once, and reports each write keyed by entry. It returns nil
// when there was nothing to raise.
func (s *Service) superviseUntaskedKeptWork(c *incarnation, untasked []store.KeptWork) map[int64]taskReport {
	var out map[int64]taskReport
	for _, k := range untasked {
		if k.SurfacedAt.IsKnown() {
			continue
		}
		wake, _ := supervisor.Of(supervisor.KindKeptWork)
		det := detection{
			Kind:    supervisor.KindKeptWork,
			Marker:  s.sup.redactor.Redact(keptWorkMarker(k.ID)),
			Verdict: supervisor.Classify(wake, supervisor.NoEvidence, supervisor.WindowOpen, false, supervisor.Stall{}),
		}
		if out == nil {
			out = make(map[int64]taskReport)
		}
		out[k.ID] = s.recordUntaskedKeptWork(c, k, det)
	}
	return out
}

// recordUntaskedKeptWork makes the pass's write for one entry, on record's
// terms: a surfaced detection is appended under the incarnation's fence,
// and a row appended rings the bell and is pushed.
func (s *Service) recordUntaskedKeptWork(c *incarnation, k store.KeptWork, det detection) taskReport {
	out := taskReport{Watched: true, Detections: []detection{det}}
	if !det.Verdict.Surfaces() {
		return out
	}
	ctx, cancel := s.sup.bound(c.ctx)
	appended, err := s.store.AppendKeptWorkWake(ctx, c.inc,
		store.KeptWorkWakeRequest{KeptWork: k.ID, Kind: string(det.Kind), Marker: det.Marker})
	cancel()
	switch {
	case errors.Is(err, store.ErrKeptWorkAnswered), errors.Is(err, store.ErrKeptWorkClosed):
		// Decided since the pass read it: nothing is owed.
		return taskReport{Watched: true}
	case err != nil:
		out.Err = err
		return out
	}
	out.Detections[0].Disposition = appended.Disposition
	if appended.Disposition == store.WakeAppended {
		s.wakeBell.ring()
		s.publishKeptWorkWake(appended.Wake)
	}
	return out
}
