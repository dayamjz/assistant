package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Steering a running worker: PRD section 6's message and stop, two of the
// coordinator's service methods section 9 puts beside the command table, and
// the interrupt that section refuses as undeclared.
//
// # Who may ask
//
// All three are the coordinator's on coordinator.go's terms: internal/ipc
// declares them restricted, and refuseWorker refuses a caller the worker
// group set places in any task's worker group before anything is read or
// written, so no worker steers itself or a sibling. Who sent a message is
// ipc.Request.MessageSender, derived from the surface and never read from
// the request, so no value meaning a person reaches the record.
//
// # A message is a record first
//
// What follows is deliverMessage's, which task.message and a promotion's
// brief (coordinator.go's briefPromoted) both send through.
//
// task.message reads the task's latest attempt, appends the message record
// naming that attempt through store.AppendMessage, which removes credentials
// from the text, and then sends what the record holds, under a heading
// naming the record's identifier (framedMessage), through the session
// provider's Send, which the Claude Code adapter frames as one stream-json
// user message, and which the tmux provider types into the worker's terminal
// through the adapter's terminal framing, which refuses a line longer than it
// may type, so such a message is recorded refused. The heading is how a
// worker knows which number to acknowledge, since nothing else on its input
// names the record. The send's answer is recorded once: written when Send
// returned, refused with its reason when it failed or when this service
// holds no session for that attempt's reference. Nothing resends, here or
// anywhere: a refusal is answered to the caller as *messageRefused with the
// record, and a record a crash left with neither fact reads as unknown. The
// text is bounded by machine.MaxMessageBytes and refused above it rather
// than truncated, before anything is recorded.
//
// What the record proves is the record's own sentence: written is that the
// bytes reached the worker's input, never that the worker read them. That is
// the acknowledgement, which the worker appends through task.event as
// store.MessageAcknowledgedEventKind naming the message; workers.go hands
// that kind to store.AcknowledgeMessage, which writes the event and the
// fact on the record in one transaction (P8). A message written and not
// acknowledged within escalation_window surfaces once as
// supervisor.KindMessageUnacknowledged, which supervise.go detects from the
// record.
//
// # A stop is a hold, proven before it is registered
//
// task.stop ends the worker of the task's latest attempt and touches no
// copy, so what the worker wrote stays where it is (P6). A session this
// service holds is ended through the provider's Close; one it does not hold,
// a worker an earlier service of this home launched, through agents.EndGroup,
// which verifies the leader's identity before it signals anything, and only
// for a reference this home wrote with a start time to verify against. Then
// the terminal is probed through the service's Terminals, the one producer of
// liveness, and only a Dead answer lets the stop go on: anything else is
// *stopUnverified and no hold is registered. The ending's own answer is
// never read as the proof.
//
// Once the probe answered Dead the program registers the stop's hold, keyed
// by the attempt it stopped, stoppedKey. Registration is idempotent by key,
// so a second stop of the same attempt finds the same hold. While it is open
// the task resolves Held from internal/taskstate's hold tier, and
// reconcile.go's table relaunches no Held task, so neither a restart nor the
// coordinator's task.relaunch undoes a stop. Answered retry, taskholds.go
// relaunches through relaunchTask, the one anchored relaunch, which counts
// it toward relaunch_attempts like every other; answered stop, it leaves the
// task where its records put it, which decideReconciliation reads.
//
// A task whose work is finished is owed no relaunch, so before registering
// the hold the stop reads the task through the service's reader, and a task
// resolving in internal/taskstate's terminal tier, Landed or Complete,
// registers none: the answer carries the state as TaskStopped.Finished and
// a zero hold. A Landed one is then asked internal/tasks' cleanup decision
// with the terminal that read resolved, on landed.go's terms, so the copy is
// removed only on P12's proof and never because the stop ended the worker.
// A read that fails registers the hold, as for a live task, because a
// stopped worker with neither a hold nor a terminal record would read
// Unknown over a dead terminal, which a restart relaunches.
//
// The stop takes the task's slot for its duration, claimSteer, so a relaunch
// or a second stop in this service is refused while it runs rather than
// interleaving with the ending and the hold.
//
// # What a relaunch after a stop carries
//
// A relaunch whose superseded attempt's stop hold was answered retry is
// started with the relaunch note, then the note the answer gave, read from
// the hold it is recorded on, whichever relaunch it is: the answer's own, or
// one a later start or the bound's retry makes on that recorded answer
// (#604), then the text of every message to that attempt
// the worker never acknowledged, read from the records, oldest first, each
// under its identifier so the new worker can acknowledge it. That is no
// resend: the stopped worker is gone, and the text reaches a new worker as
// part of what it is started with.
//
// # Interrupt is undeclared
//
// task.interrupt is refused with *agents.CapabilityError naming
// capabilityInterrupt, which internal/agents' table has no row for and so no
// declaration satisfies: ending a turn while keeping the process needs proof
// the turn ended, which nothing reads, and PRD section 12 defers it. The
// path is stop, then relaunch with a note.
//
// # Residual gaps
//
//   - Written proves the worker's input took the bytes. A send that failed
//     partway through a write is recorded refused although some bytes may
//     have reached the worker; the adapter frames a message as one line, so
//     a partial line is what such a worker would read.
//   - The text sent is the text recorded, with credentials removed. A
//     credential the coordinator meant the worker to have does not reach it
//     through a message.
//   - A stop of a worker this service does not hold rests on the reference's
//     identity, which narrows process identifier reuse and does not close it,
//     on internal/agents' terms; a reference with no start time is refused.
//   - The probe's Dead is as of the probe. A descendant that left the
//     worker's process group is not what a Dead reading is about.
//   - A stopped worker's terminal is dead, so the supervisor loop surfaces a
//     terminal-gone wake about it beside the hold-opened one; a retry's
//     relaunch acknowledges the former as the program, as every relaunch does.
//   - The note a relaunch carries is the one recorded, with credentials
//     removed, as a message's text is, so a credential the coordinator meant
//     the worker to have does not reach it through a note either.
//   - No method reads a message record back; the record travels in
//     task.message's answer, and an unacknowledged one surfaces as a wake.

// capabilityInterrupt is the capability an interrupt needs. It is not a row
// of internal/agents' table, by PRD section 12's deferral: a row needs a
// conformance probe for proof a turn ended, which nothing has, so
// Capabilities.Has answers false for it whatever an adapter declares.
const capabilityInterrupt agents.Capability = "interrupt"

// messageSendBound bounds a Send: a worker that is not reading its input
// holds the write until it does, and a coordinator's request is not held on
// it longer than this. Chosen, not measured.
const messageSendBound = 10 * time.Second

// stopProbeBound bounds the probe that proves a stop. It is
// reconcileProbeTimeout's role at a stop, and the same length.
const stopProbeBound = reconcileProbeTimeout

// stopGrace is how long agents.EndGroup waits between asking a group
// politely and ending it, for a worker this service does not hold. It is
// the reap's grace, since both end a group no handle reaches.
const stopGrace = reapGrace

// errSteerInFlight reports a stop asked of a task whose slot a dispatch, a
// launch, a relaunch or another stop in this service holds unsettled.
// Nothing is recorded.
var errSteerInFlight = errors.New("service: a dispatch, a launch, a relaunch or a stop of the task is in flight in this service")

// errNoWorker reports a message or a stop asked of a task with no attempt
// whose worker could be addressed: none was ever recorded, or the latest
// recorded no session. Nothing is recorded.
var errNoWorker = errors.New("service: the task's latest attempt names no worker")

// messageRefused reports a message whose send was refused and recorded so:
// Message is the record, refused with the reason, and nothing resends it.
type messageRefused struct {
	Message store.Message
}

// Error names the message and the recorded reason.
func (e *messageRefused) Error() string {
	return fmt.Sprintf("service: message %d to task %s was not written to its worker and is recorded refused: %s",
		e.Message.ID, e.Message.TaskID, e.Message.Refusal.Or("no reason recorded"))
}

// stopRefused reports a stop that ended nothing, because the worker could
// not be ended on the terms the file comment states. No hold is registered.
type stopRefused struct {
	Task    string
	Session string
	Reason  string
}

// Error names the task, the session and why.
func (e *stopRefused) Error() string {
	return fmt.Sprintf("service: the worker of task %s (session %s) was not stopped: %s", e.Task, e.Session, e.Reason)
}

// stopUnverified reports a stop whose probe did not answer Dead after the
// worker was asked to end. No hold is registered.
type stopUnverified struct {
	Task        string
	Session     string
	Observation session.Observation
}

// Error names the task, the session and what the probe answered.
func (e *stopUnverified) Error() string {
	return fmt.Sprintf("service: the worker of task %s (session %s) could not be shown to have ended: the probe answered %s (%s: %s), so no hold was registered",
		e.Task, e.Session, e.Observation.Liveness, e.Observation.Reason, e.Observation.Detail)
}

// stoppedPrefix is the key space of the holds a stop registers.
const stoppedPrefix = "/stopped/"

// stoppedKey is the key of the hold a stop registers for a task's attempt.
// It is a key space of its own beside the relaunch holds', so answering one
// never answers another, and it is keyed by the attempt so it is the same
// hold across a restart and a new one for a later attempt.
func stoppedKey(task string, attempt int64) string {
	return "task/" + task + stoppedPrefix + strconv.FormatInt(attempt, 10)
}

// isStopped reports whether key is a stop hold of the task.
func isStopped(key, task string) bool {
	return strings.HasPrefix(key, "task/"+task+stoppedPrefix)
}

// latestAttempt reads the task, which must exist, and its latest attempt.
func (s *Service) latestAttempt(ctx context.Context, id string) (store.TaskAttempt, bool, error) {
	if _, err := s.store.Task(ctx, id); err != nil {
		return store.TaskAttempt{}, false, err
	}
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil || len(attempts) == 0 {
		return store.TaskAttempt{}, false, err
	}
	return attempts[len(attempts)-1], true, nil
}

// messageTask is task.message, on the file comment's terms.
func (s *Service) messageTask(ctx context.Context, req ipc.Request, r machine.TaskMessageRequest) (machine.TaskMessaged, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.TaskMessaged{}, err
	}
	switch {
	case strings.TrimSpace(r.Task) == "":
		return machine.TaskMessaged{}, fmt.Errorf("%w: a message names the task whose worker it is for", ipc.ErrInvalidRequest)
	case strings.TrimSpace(r.Text) == "":
		return machine.TaskMessaged{}, fmt.Errorf("%w: a message to task %s carries no text", ipc.ErrInvalidRequest, r.Task)
	case len(r.Text) > machine.MaxMessageBytes:
		return machine.TaskMessaged{}, fmt.Errorf("%w: a message to task %s is %d bytes, over the limit of %d, and is refused rather than cut",
			ipc.ErrInvalidRequest, r.Task, len(r.Text), machine.MaxMessageBytes)
	case !utf8.ValidString(r.Text):
		return machine.TaskMessaged{}, fmt.Errorf("%w: a message to task %s is not valid UTF-8", ipc.ErrInvalidRequest, r.Task)
	}
	if err := s.scopeTask(ctx, req, p, r.Task); err != nil {
		return machine.TaskMessaged{}, err
	}
	latest, ok, err := s.latestAttempt(ctx, r.Task)
	if err != nil {
		return machine.TaskMessaged{}, err
	}
	ref, launched := latest.SessionRef.Get()
	if !ok || !launched {
		return machine.TaskMessaged{}, fmt.Errorf("%w: %w: task %s", ipc.ErrRefused, errNoWorker, r.Task)
	}
	m, err := s.deliverMessage(ctx, req, s.senderOf(req, p), r.Task, latest.Sequence, ref, r.Text)
	var refused *messageRefused
	if errors.As(err, &refused) {
		return machine.TaskMessaged{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	if err != nil {
		return machine.TaskMessaged{}, err
	}
	return machine.TaskMessaged{Message: m, Delivery: m.Delivery(), SentBy: m.SentBy.String()}, nil
}

// deliverMessage is a message's record and send, on the file comment's
// terms, for every caller that sends a worker text: task.message, and a
// promotion's brief. It appends the record naming the attempt, with by as
// who sent it, which the caller derived from the surface and the placement
// through senderOf, sends what the record holds framed by framedMessage to
// the worker ref names, and records the send's answer once. It returns the
// record written, or the record refused inside a *messageRefused, or an
// error from a record that could not be read or written, beside whatever
// record it had by then.
func (s *Service) deliverMessage(ctx context.Context, req ipc.Request, by store.Sender, task string, attempt int64, ref, text string) (store.Message, error) {
	m, err := s.store.AppendMessage(ctx, task, attempt, text, by)
	if err != nil {
		return store.Message{}, err
	}
	if sendErr := s.sendMessage(ctx, task, ref, framedMessage(m)); sendErr != nil {
		refused, err := s.store.MarkMessageRefused(ctx, m.ID, sendErr.Error())
		if err != nil {
			return m, fmt.Errorf("service: message %d to task %s was not written (%w) and that could not be recorded: %w",
				m.ID, task, sendErr, err)
		}
		s.log.Printf("message %d to task %s attempt %d from %s was refused: %v", m.ID, task, attempt, peerOf(req), sendErr)
		return refused, &messageRefused{Message: refused}
	}
	written, err := s.store.MarkMessageWritten(ctx, m.ID)
	if err != nil {
		return m, fmt.Errorf("service: message %d to task %s was written and that could not be recorded: %w", m.ID, task, err)
	}
	s.log.Printf("message %d was written to the worker of task %s attempt %d by %s", m.ID, task, attempt, peerOf(req))
	return written, nil
}

// sendMessage writes text to the worker this service holds for the task,
// when that worker is the one ref names, bounded by messageSendBound. A
// worker this service does not hold, or holds under another reference, is
// refused: its input is reachable through no handle here.
func (s *Service) sendMessage(ctx context.Context, id, ref, text string) error {
	provider, held, ok := s.sessionOf(id)
	if !ok || held.Reference().String() != ref {
		return fmt.Errorf("this service holds no session for the worker of the task's latest attempt (session %s)", ref)
	}
	bound, cancel := context.WithTimeout(ctx, messageSendBound)
	defer cancel()
	return provider.Send(bound, held, text)
}

// claimSteer marks the task's slot taken for a stop, or another write
// that must not interleave with one, leaving whatever proof it holds, and
// returns the function that releases it. A slot a dispatch or a relaunch
// holds unsettled, which it does through the launch that follows it
// (dispatch.go), or another stop holds, is errSteerInFlight.
//
// After a keep resolves a retirement hold, either the settled slot is freed
// and the pass redispatched, or a holder of the steer is recorded as owing
// that redispatch, and the check and the record are one critical section
// (freeSettledSlotForKeep). The release here runs that owed redispatch: it
// gives the settled slot back and asks dispatchDue, so no refuse-and-release
// of this claim can fall between keep's check and its record.
func (s *Service) claimSteer(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slotMaps()
	if s.claiming[id] {
		return nil, fmt.Errorf("%w: task %s", errSteerInFlight, id)
	}
	s.claiming[id] = true
	s.steering[id] = true
	return func() {
		s.mu.Lock()
		s.endClaim(id)
		need := s.redispatchWhenFree[id]
		delete(s.redispatchWhenFree, id)
		if need {
			s.giveBackSettledSlotLocked(id)
		}
		s.mu.Unlock()
		if need {
			s.dispatchInBackground()
		}
	}, nil
}

// stopTask is task.stop, on the file comment's terms.
func (s *Service) stopTask(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.TaskStopped, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.TaskStopped{}, err
	}
	if strings.TrimSpace(r.Task) == "" {
		return machine.TaskStopped{}, fmt.Errorf("%w: a stop names the task whose worker it ends", ipc.ErrInvalidRequest)
	}
	if err := s.scopeTask(ctx, req, p, r.Task); err != nil {
		return machine.TaskStopped{}, err
	}
	if s.terminals == nil {
		return machine.TaskStopped{}, fmt.Errorf("%w: %w", ipc.ErrUnavailable, errRelaunchUnprobed)
	}
	release, err := s.claimSteer(r.Task)
	if err != nil {
		return machine.TaskStopped{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	defer release()
	latest, ok, err := s.latestAttempt(ctx, r.Task)
	if err != nil {
		return machine.TaskStopped{}, err
	}
	if !ok {
		return machine.TaskStopped{}, fmt.Errorf("%w: %w: task %s", ipc.ErrRefused, errNoWorker, r.Task)
	}
	stopped, err := s.stopClaimed(ctx, req, p, r.Task, latest, "")
	if err != nil {
		var unverified *stopUnverified
		var refused *stopRefused
		if errors.As(err, &unverified) || errors.As(err, &refused) || errors.Is(err, errNoWorker) {
			return machine.TaskStopped{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		return machine.TaskStopped{}, err
	}
	return stopped, nil
}

// stopClaimed is the stop of the worker of the task's latest attempt, on the
// file comment's terms, for a caller that holds the task's slot: task.stop,
// and a stop or retry answered to a stall hold. It ends the worker, proves
// the end by the probe reading it dead, closes every open stall hold of that
// attempt as the program on that reading but the one named answering, which
// the caller is answering itself, and registers the stop's hold, or none for
// a task in the terminal tier. An attempt with no session is errNoWorker, an
// end refused is *stopRefused and an end the probe does not prove is
// *stopUnverified, each with nothing registered or closed.
func (s *Service) stopClaimed(ctx context.Context, req ipc.Request, p placedPeer, task string, latest store.TaskAttempt,
	answering string) (machine.TaskStopped, error) {
	ref, launched := latest.SessionRef.Get()
	if !launched {
		return machine.TaskStopped{}, fmt.Errorf("%w: task %s", errNoWorker, task)
	}
	ended, err := s.endWorker(task, ref)
	if err != nil {
		return machine.TaskStopped{}, err
	}
	bound, cancel := context.WithTimeout(ctx, stopProbeBound)
	observed := s.terminals.Probe(bound, ref)
	cancel()
	if observed.Liveness != taskstate.Dead {
		s.log.Printf("the worker of task %s (session %s) was asked to end by %s and the probe answered %s: %s",
			task, ref, peerOf(req), observed.Liveness, observed.Detail)
		return machine.TaskStopped{}, &stopUnverified{Task: task, Session: ref, Observation: observed}
	}
	s.closeStallsOnDead(ctx, task, latest.Sequence, ref, answering, observed)
	if finished, ok := s.stoppedFinished(ctx, task); ok {
		s.log.Printf("the worker of task %s attempt %d (session %s) was stopped by %s: %s; the task is %s, so no hold was registered",
			task, latest.Sequence, ref, peerOf(req), ended, finished.State.State)
		s.askLandedCleanup(ctx, finished)
		return machine.TaskStopped{Task: task, Attempt: latest.Sequence, Session: ref, Ended: ended,
			Finished: finished.State.State}, nil
	}
	h, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     stoppedKey(task, latest.Sequence),
		TaskID:  store.Known(task),
		Subject: "whether to begin this work again after its worker was stopped",
		Detail: fmt.Sprintf("task %s: the %s stopped attempt %d's worker (session %s), which %s, and its copy was left as it was. "+
			"Answer %q to relaunch it once in the same copy, optionally with a note, or %q to leave it where it stopped.",
			task, s.senderOf(req, p), latest.Sequence, ref, ended, machine.AnswerRetry, machine.AnswerStop),
	})
	if err != nil {
		return machine.TaskStopped{}, fmt.Errorf("service: the worker of task %s was stopped and its hold could not be registered: %w", task, err)
	}
	s.publishHoldState(h)
	s.log.Printf("the worker of task %s attempt %d (session %s) was stopped by %s: %s; it holds on %s",
		task, latest.Sequence, ref, peerOf(req), ended, h.Key)
	return machine.TaskStopped{Task: task, Attempt: latest.Sequence, Session: ref, Ended: ended, Hold: h}, nil
}

// endWorker ends the worker ref names: through the provider's Close when
// this service holds its session, which it then forgets, and otherwise
// through agents.EndGroup on the reference's identity, refused as
// *stopRefused for a reference of another home, one with no start time to
// verify against, or one that does not decode. It reports how, in words.
func (s *Service) endWorker(id, ref string) (string, error) {
	ended, err := s.endHeldWorker(id, ref)
	if err != nil {
		return "", err
	}
	if ended {
		return "this service ended through the session it holds", nil
	}
	decoded, err := session.ParseReference(ref)
	switch {
	case err != nil:
		return "", &stopRefused{Task: id, Session: ref, Reason: err.Error()}
	case decoded.Home != session.HomeMarker(s.home):
		return "", &stopRefused{Task: id, Session: ref, Reason: "the reference belongs to another home"}
	case decoded.Start == 0:
		return "", &stopRefused{Task: id, Session: ref,
			Reason: "the reference records no start time, so the process it names cannot be verified before anything is signalled"}
	}
	end, err := agents.EndGroup(agents.Identity{PID: decoded.PID, Start: decoded.Start}, stopGrace)
	if err != nil {
		return "", &stopRefused{Task: id, Session: ref, Reason: fmt.Sprintf("ending process group %d: %v", decoded.PID, err)}
	}
	return fmt.Sprintf("an earlier service launched and this one ended by its verified identity (%s)", end), nil
}

// stoppedFinished reads the task through the service's reader after its
// stop's probe answered Dead, and reports the read when it resolves in
// internal/taskstate's terminal tier, where no relaunch is owed and so no
// hold is registered. A read that fails reports false, so the stop holds
// for a person as it would for a live task: a stopped worker with neither a
// hold nor a terminal record would read Unknown over a dead terminal, which
// reconcile.go relaunches at the next start.
func (s *Service) stoppedFinished(ctx context.Context, id string) (taskstate.Observed, bool) {
	o, err := s.tasks().ObserveTask(ctx, id)
	if err != nil {
		s.log.Printf("the worker of task %s was stopped and the task could not be read, so its stop holds: %v", id, err)
		return taskstate.Observed{}, false
	}
	return o, landedEnds(o.State)
}

// endHeldWorker is endWorker's held half: when this service holds the
// session ref names for the task, it forgets the session and ends it
// through the provider's Close, reporting true, or a *stopRefused when the
// Close failed, by which point the session is already forgotten. It reports
// false and does nothing when this service holds no such session.
func (s *Service) endHeldWorker(id, ref string) (bool, error) {
	s.mu.Lock()
	held, ok := s.launched[id]
	if ok && held.session != nil && held.host != nil && held.session.Reference().String() == ref {
		delete(s.launched, id)
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := held.host.Close(held.session); err != nil {
		return false, &stopRefused{Task: id, Session: ref, Reason: fmt.Sprintf("closing its session: %v", err)}
	}
	return true, nil
}

// interruptTask is task.interrupt: refused, for the coordinator, with the
// capability nothing declares named, on the file comment's terms. A worker
// is refused first, so the answer a worker reads is the one every
// coordinator-only method gives it.
func (s *Service) interruptTask(ctx context.Context, req ipc.Request, r machine.TaskRequest) (struct{}, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
		return struct{}{}, err
	}
	if strings.TrimSpace(r.Task) == "" {
		return struct{}{}, fmt.Errorf("%w: an interrupt names the task whose worker it is for", ipc.ErrInvalidRequest)
	}
	refusal := &agents.CapabilityError{Capability: capabilityInterrupt, Path: "interrupting a worker's turn"}
	if built, err := s.driverFor(ctx); err == nil {
		refusal.Agent = built.agent.Name
		if built.agent.Capabilities.Has(capabilityInterrupt) {
			// Unreachable while the capability has no row: Has answers
			// false for a capability the table does not recognize. It is
			// refused rather than served, because nothing here interrupts.
			return struct{}{}, fmt.Errorf("%w: %s declares %s and this build implements no interrupt", ipc.ErrRefused, built.agent.Name, capabilityInterrupt)
		}
	}
	return struct{}{}, fmt.Errorf("%w: %w; stop the worker and answer the stop's hold %q with a note instead",
		ipc.ErrRefused, refusal, machine.AnswerRetry)
}

// steeringNote is what a relaunch of a stopped attempt adds after the
// relaunch note: the answer's note when there is one, and the text of every
// message to the stopped attempt its worker never acknowledged, oldest
// first, each under its identifier. It is empty when there is neither.
func steeringNote(note string, unacknowledged []store.Message) string {
	if strings.TrimSpace(note) == "" && len(unacknowledged) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Stopped by the coordinator\n\n")
	b.WriteString("The coordinator stopped the previous worker on this task and asked for the work to be begun again.\n")
	if strings.TrimSpace(note) != "" {
		b.WriteString("\nIts note for you:\n\n")
		b.WriteString(note)
		b.WriteString("\n")
	}
	if len(unacknowledged) > 0 {
		b.WriteString("\nMessages it sent the previous worker that were never acknowledged, oldest first. " +
			"Acknowledge each one you act on, by its number.\n")
		for _, m := range unacknowledged {
			b.WriteString("\n" + framedMessage(m) + "\n")
		}
	}
	return b.String()
}

// framedMessage is a message as a worker reads it: the record's text under a
// heading naming the record's identifier, which is the number the worker
// acknowledges it by. A live send and a relaunch's note both frame a message
// this way, so a worker is told one shape for both.
func framedMessage(m store.Message) string {
	return fmt.Sprintf("### Message %d\n\n%s", m.ID, m.Text)
}

// unacknowledgedOf is every message to the task's attempt whose
// acknowledgement is not recorded, oldest first, whatever its delivery: a
// refused message never reached a worker, and one whose send never reported
// may not have, so each is carried to the next worker the same way.
func (s *Service) unacknowledgedOf(ctx context.Context, id string, attempt int64) ([]store.Message, error) {
	all, err := s.store.TaskMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []store.Message
	for _, m := range all {
		if m.Attempt == attempt && !m.Acknowledged() {
			out = append(out, m)
		}
	}
	return out, nil
}
