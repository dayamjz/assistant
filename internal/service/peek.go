package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
)

// Peeking at a worker: task.peek, the coordinator's read of a running
// task's worker's recent output, and the way a person learns how to attach
// to the worker's terminal where the provider has one.
//
// # Who may ask
//
// The coordinator, on coordinator.go's terms: internal/ipc declares the
// method restricted, and refuseWorker refuses a caller the worker group set
// places in any task's worker group before anything is read, so no worker
// reads its own or a sibling's terminal through the service.
//
// # What is read
//
// The task's latest attempt names the session, as it does for a message,
// and the read is of the session this service's provider holds under that
// reference: the capture and the attach command answer out of the provider's
// held set, the one Probe answers Live from, and the liveness beside them is
// that Probe's, through the service's Terminals. A worker this service does
// not hold, an earlier service's orphan, is refused rather than read, on the
// terms the provider refuses it: nothing here reaches a terminal by any
// other route. A held worker that has exited is still read, which is what
// the provider still holds of it: a pipe worker's retained output, and a
// tmux worker's last screen.
//
// # Read as a transcript, redacted, abbreviated, then bounded
//
// The capture is read as events by agents.ReadTranscript, in the format the
// worker's adapter reported, so a Claude Code worker's stream-json reads as
// its text, its tool calls and their results, with the counters and status
// lines left out and counted, and anything the decoder has no row for shown
// as a one-line marker rather than dropped. Each event's text then passes
// through internal/redact, is abbreviated by Abbreviated, and the most
// recent events whose rendering fits in machine.MaxPeekBytes are kept, the
// rest counted in Earlier. The order is the point: a cut made before
// redaction could leave half of a credential in what is kept, in a shape the
// redactor no longer recognizes. The bound is on what is shown; what is read
// is the provider's capture, bounded by the agent runner's output limit,
// with what it let go counted in Dropped.
//
// # Residual gaps
//
//   - internal/redact recognizes what it says it recognizes and nothing
//     else, so a credential in another shape reaches the answer.
//   - The output is the coordinator's to read. The person-facing rendering
//     carries none of it, which internal/machine's PeekPhrase states, but a
//     caller reading the structured answer gets it as the worker wrote it,
//     after redaction and abbreviation.
//   - A credential that redaction would recognize only across two events,
//     or across the text and the tool of one, is not recognized, because
//     each string is redacted on its own.

// errNotPeekable is task.peek's refusal of a worker this service holds no
// session for under the latest attempt's reference.
var errNotPeekable = errors.New("service: this service holds no session for the worker of the task's latest attempt")

// peekTask is task.peek, on the file comment's terms.
func (s *Service) peekTask(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.TaskPeek, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.TaskPeek{}, err
	}
	if strings.TrimSpace(r.Task) == "" {
		return machine.TaskPeek{}, fmt.Errorf("%w: a peek names the task whose worker it reads", ipc.ErrInvalidRequest)
	}
	latest, ok, err := s.latestAttempt(ctx, r.Task)
	if err != nil {
		return machine.TaskPeek{}, err
	}
	ref, launched := latest.SessionRef.Get()
	if !ok || !launched {
		return machine.TaskPeek{}, fmt.Errorf("%w: %w: task %s", ipc.ErrRefused, errNoWorker, r.Task)
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		return machine.TaskPeek{}, fmt.Errorf("%w: %w", ErrTerminalsUnavailable, err)
	}
	captured, err := built.sessions.CaptureReference(ctx, ref)
	if errors.Is(err, session.ErrNotHeld) {
		return machine.TaskPeek{}, fmt.Errorf("%w: %w (session %s)", ipc.ErrRefused, errNotPeekable, ref)
	}
	if err != nil {
		return machine.TaskPeek{}, err
	}
	attach, err := built.sessions.AttachReference(ref)
	switch {
	case errors.Is(err, session.ErrNotAttachable):
		attach = nil
	case err != nil:
		return machine.TaskPeek{}, fmt.Errorf("%w: %w (session %s)", ipc.ErrRefused, errNotPeekable, ref)
	}
	transcript := agents.ReadTranscript(captured.Text, captured.Format, captured.Dropped > 0)
	events, earlier := boundEvents(shownEvents(transcript.Events), machine.MaxPeekBytes)
	return machine.TaskPeek{
		Task:      r.Task,
		Attempt:   latest.Sequence,
		Session:   ref,
		Liveness:  string(s.terminals.Probe(ctx, ref).Liveness),
		Output:    renderEvents(events),
		Events:    events,
		Earlier:   earlier,
		Omitted:   transcript.Omitted,
		Dropped:   captured.Dropped,
		Uncounted: captured.Uncounted,
		Attach:    attach,
	}, nil
}

// shownEvents is every event redacted and then abbreviated, in that order,
// on the file comment's terms.
func shownEvents(events []agents.TranscriptEvent) []agents.TranscriptEvent {
	redactor := redact.New()
	out := make([]agents.TranscriptEvent, len(events))
	for i, e := range events {
		e.Tool = redactor.Redact(e.Tool)
		e.Text = redactor.Redact(e.Text)
		out[i] = e.Abbreviated()
	}
	return out
}

// boundEvents keeps the most recent events whose rendering, one line each
// as renderEvents writes it, fits in limit bytes, and reports how many
// earlier ones it left out.
func boundEvents(events []agents.TranscriptEvent, limit int) ([]agents.TranscriptEvent, int) {
	size := 0
	start := len(events)
	for start > 0 {
		next := len(events[start-1].Line()) + 1
		if size+next > limit {
			break
		}
		size += next
		start--
	}
	return events[start:], start
}

// renderEvents is the events as a person reads them, each Line followed by
// a newline.
func renderEvents(events []agents.TranscriptEvent) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Line())
		b.WriteByte('\n')
	}
	return b.String()
}
