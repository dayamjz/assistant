package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
// # Redacted, then bounded
//
// The capture passes through internal/redact first and is cut to
// machine.MaxPeekBytes from the end afterwards, at a character boundary.
// The order is the point: a cut made first could leave half of a
// credential at the front of what is kept, in a shape the redactor no
// longer recognizes. What was cut is counted in Dropped beside what the
// provider's own bound let go.
//
// # Residual gaps
//
//   - internal/redact recognizes what it says it recognizes and nothing
//     else, so a credential in another shape reaches the answer.
//   - The output is the coordinator's to read. The person-facing rendering
//     carries none of it, which internal/machine's PeekPhrase states, but a
//     caller reading the structured answer gets it as the worker wrote it,
//     after redaction.

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
	output, cut := boundTail(redact.New().Redact(captured.Text), machine.MaxPeekBytes)
	return machine.TaskPeek{
		Task:      r.Task,
		Attempt:   latest.Sequence,
		Session:   ref,
		Liveness:  string(s.terminals.Probe(ctx, ref).Liveness),
		Output:    output,
		Dropped:   captured.Dropped + cut,
		Uncounted: captured.Uncounted,
		Attach:    attach,
	}, nil
}

// boundTail keeps at most limit bytes from the end of text, starting at a
// character boundary, and reports how many bytes it cut.
func boundTail(text string, limit int) (string, int64) {
	if len(text) <= limit {
		return text, 0
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:], int64(start)
}
