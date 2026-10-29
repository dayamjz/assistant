package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// selectWorkerChoice routes a worker backup's own agent and checks that the
// agent declares the backup's triple, context window, and effort. A triple
// or window it has not declared is refused naming roleName, the role the
// backup belongs to, as launchTask names it for the primary (PRD section 10).
func (s *Service) selectWorkerChoice(ctx context.Context, roleName string, role config.Role) (agents.Selection, error) {
	selection, err := agents.Route(ctx, s.catalog, nil, agents.NeedForWorker(s.cfg.SessionProvider == config.SessionProviderTmux), role.Agent)
	if err != nil {
		return selection, err
	}
	if role.Provider != "" || role.ContextWindow != "" {
		if _, err = agents.Translate(selection.Resolution.Name, role.Provider, role.Type, role.Version, role.ContextWindow); err != nil {
			return agents.Selection{}, fmt.Errorf("service: the %s role: %w", roleName, err)
		}
	}
	if err = agents.AcceptsEffort(selection.Resolution.Name, role.Effort); err != nil {
		return agents.Selection{}, err
	}
	return selection, nil
}
func (s *Service) routeTaskWorker(ctx context.Context, task store.Task, attempt int64) (agents.Selection, config.Role, error) {
	role, err := s.cfg.Roles.ForRecorded(task.Role)
	if err != nil {
		return agents.Selection{}, role, err
	}
	if role.Fallback != nil {
		if _, err := s.selectWorkerChoice(ctx, config.TaskRoleName(task.Role), *role.Fallback); err != nil && !agents.FallbackAllowed(err) {
			return agents.Selection{}, role, err
		}
	}
	selected, has, err := s.store.TaskFallback(ctx, task.ID)
	if err != nil {
		return agents.Selection{}, role, err
	}
	if has {
		if role.Fallback == nil || selected.Primary != role.Agent || selected.Backup != role.Fallback.Agent {
			return agents.Selection{}, role, errors.New("service: the recorded worker backup is no longer configured")
		}
		backup := *role.Fallback
		selection, err := s.selectWorkerChoice(ctx, config.TaskRoleName(task.Role), backup)
		if err != nil {
			return agents.Selection{}, backup, err
		}
		if selection.Reason, err = agents.FallbackReason(agents.Failure(selected.Reason)); err != nil {
			return agents.Selection{}, backup, err
		}
		return selection, backup, nil
	}
	selection, _, err := s.routeWorker(ctx, task.Role)
	if err == nil || role.Fallback == nil || !agents.FallbackAllowed(err) {
		return selection, role, err
	}
	backup := *role.Fallback
	next, nextErr := s.selectWorkerChoice(ctx, config.TaskRoleName(task.Role), backup)
	if nextErr != nil {
		return agents.Selection{}, role, errors.Join(err, nextErr)
	}
	if err = s.store.RecordTaskFallback(ctx, task.ID, store.TaskFallback{Primary: role.Agent, Backup: backup.Agent, Reason: string(agents.FailureUnavailable), Attempt: attempt}); err != nil {
		return agents.Selection{}, role, err
	}
	next.Reason = agents.ReasonFallbackUnavailable
	return next, backup, nil
}

// watchWorkerFallback observes only this service's held worker. A recognized
// failure seals the selection before stopping it; the existing relaunch path
// then proves absence, sweeps and reasserts the same copy, and spends its
// existing relaunch bound. It never launches beside the primary. A restart
// reads the sealed selection instead of replaying a quota event from a log.
func (s *Service) watchWorkerFallback(task store.Task, attempt int64, host session.Host, worker *session.Session, adapter string) {
	role, err := s.cfg.Roles.ForRecorded(task.Role)
	if err != nil || role.Fallback == nil || (adapter != role.Agent && adapter != role.Fallback.Agent) || s.stopCtx == nil {
		return
	}
	if !s.startWork() {
		return
	}
	go func() {
		defer s.work.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.stopCtx.Done():
				return
			case <-tick.C:
			}
			failure := worker.AvailabilityFailure()
			if failure == agents.FailureNone {
				if host.Probe(s.stopCtx, worker.Reference().String()).Liveness == taskstate.Dead {
					return
				}
				continue
			}
			if _, err := s.switchWorker(s.stopCtx, task, attempt, host, worker, adapter, role, failure, nil); err != nil {
				s.log.Printf("task %s worker handoff: %v", task.ID, err)
			}
			return
		}
	}()
}

const workerFallbackHoldPrefix = "/worker-fallback/"

func workerFallbackHoldKey(id string, attempt int64) string {
	return "task/" + id + workerFallbackHoldPrefix + strconv.FormatInt(attempt, 10)
}
func isWorkerFallbackHold(key, id string) bool {
	return strings.HasPrefix(key, "task/"+id+workerFallbackHoldPrefix)
}

// handoffNotStarted reports a relaunch refusal that stopped before the backup
// was asked. The sealed selection is the record of the handoff, and the next
// relaunch reads it. A refusal that already registered its own hold, the
// no-sweep hold among them, is included so this path does not open a second.
func handoffNotStarted(err error) bool {
	var unswept *copyUnswept
	var held *relaunchHeld
	var platform *relaunchUnswept
	return errors.As(err, &unswept) || errors.As(err, &held) || errors.As(err, &platform) ||
		errors.Is(err, errRelaunchNotDead) || errors.Is(err, errRelaunchNotDue) || errors.Is(err, errRelaunchInFlight)
}

// forgetLaunched drops a session this service closed and did not replace.
func (s *Service) forgetLaunched(id string, session *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.launched[id]
	if ok && held.session == session {
		delete(s.launched, id)
	}
}

func (s *Service) holdWorkerFallback(ctx context.Context, id string, cause error) error {
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil {
		return errors.Join(cause, err)
	}
	if len(attempts) == 0 {
		return cause
	}
	attempt := attempts[len(attempts)-1].Sequence
	return s.holdWorkerFallbackAttempt(ctx, id, attempt, cause)
}

func (s *Service) holdWorkerFallbackAttempt(ctx context.Context, id string, attempt int64, cause error) error {
	h, err := s.store.RegisterTaskAttemptHold(ctx, id, attempt, store.Hold{
		Key:     workerFallbackHoldKey(id, attempt),
		Subject: "whether to retry this work after its configured backup could not take over",
		Detail:  "Work is preserved in its existing copy. Correct the agent availability or configuration, then answer retry; answer stop to leave the work as it stands. Nothing starts another worker while this decision is open.",
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	s.publishHoldState(h)
	return cause
}

// switchWorker replaces a task's worker with its role's backup: it records
// the handoff, ends the worker when one runs, begins the backup's attempt
// and launches it, under the task's slot. slot is the slot the caller
// already holds unsettled, which launchTask hands over for a fallback it
// makes itself (dispatched.handSlot), so the slot is never settled between
// the primary's failure and the backup's claim; nil claims one, as a
// fallback from a live worker's failure does (watchWorkerFallback). Either
// way the slot is settled here once, with the backup's proof, after its
// launch has settled its record or failed.
func (s *Service) switchWorker(ctx context.Context, task store.Task, attempt int64, host session.Host, worker *session.Session, adapter string, role config.Role, failure agents.Failure, slot func(tasks.Isolation)) (*session.Session, error) {
	if s.beforeFallbackClaim != nil {
		s.beforeFallbackClaim(task.ID)
	}
	settle := slot
	if settle == nil {
		var err error
		if settle, err = s.claimRelaunch(task.ID); err != nil {
			return nil, err
		}
	}
	var proof tasks.Isolation
	// A handoff kept work refused is not a worker failure: the slot is
	// given back rather than settled, so the task dispatches on its own
	// once the entry is answered, and no fallback hold is raised.
	kept := false
	defer func() {
		if !kept {
			settle(proof)
		}
	}()
	fail := func(err error) (*session.Session, error) { return nil, s.holdWorkerFallback(ctx, task.ID, err) }
	attempts, err := s.store.TaskAttempts(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 || attempts[len(attempts)-1].Sequence != attempt {
		return nil, store.ErrTaskMoved
	}
	if adapter == role.Fallback.Agent {
		reported := worker.AvailabilityError()
		err := host.Close(worker)
		return fail(errors.Join(fmt.Errorf("both configured workers are unavailable; backup %s reported %s", adapter, failure), reported, err))
	}
	handoff := store.TaskFallback{Primary: role.Agent, Backup: role.Fallback.Agent, Reason: string(failure), Attempt: attempt}
	if err = s.store.RecordTaskFallback(ctx, task.ID, handoff); err != nil {
		if errors.Is(err, store.ErrTaskMoved) {
			return nil, err
		}
		return fail(err)
	}
	var launch dispatched
	if worker == nil {
		// Start refused after cleaning up its process. No session was recorded;
		// dispatch owns the proof for reclaiming that standing copy.
		answered, readErr := s.relaunchAnswersOf(ctx, task.ID, attempts)
		if readErr != nil {
			return fail(readErr)
		}
		if decision, limited := relaunchBoundDecision(attempts, s.cfg.RelaunchAttempts, answered); limited && decision.action != reconcileRelaunch {
			_, err := s.holdAtBound(ctx, task.ID, attempts, decision)
			return nil, errors.Join(errRelaunchNotDue, err)
		}
		launch, err = s.dispatchClaimed(ctx, task.ID)
	} else {
		if err = host.Close(worker); err != nil {
			return fail(err)
		}
		// The session is ended. Forgetting it here keeps a refused relaunch
		// from being closed a second time, and from being read as a worker
		// this service still holds.
		s.forgetLaunched(task.ID, worker)
		var next relaunched
		deadline := time.Now().Add(2 * time.Second)
		for {
			next, err = s.relaunchClaimed(ctx, task.ID)
			if err == nil || !errors.Is(err, errRelaunchNotDead) || ctx.Err() != nil || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		launch = next.launch
		if err == nil {
			s.releaseSuperseded(task.ID, next.superseded)
		}
	}
	proof = launch.proof
	if err != nil {
		if errors.Is(err, store.ErrDispatchHeld) {
			// Kept work recorded in the project since the first launch holds
			// the backup's attempt as it holds any new task (dispatch.go);
			// the sealed selection stays, the slot is given back, and the
			// dispatch that follows the entry's answer reads the selection.
			kept = true
			s.releaseHeldSlot(ctx, task.ID)
			s.log.Printf("task %s worker fallback from %s to %s was recorded and the backup was not started, because kept work holds the task: %v",
				task.ID, role.Agent, role.Fallback.Agent, err)
			return nil, err
		}
		// A sweep that did not clear, a terminal that did not read dead, and
		// a task the relaunch table leaves alone have not asked the backup.
		// Holding here would resolve the task as held, and the next start
		// would leave that hold alone instead of trying the backup. The
		// sealed selection stays, and the next relaunch reads it.
		if handoffNotStarted(err) {
			s.log.Printf("task %s worker fallback from %s to %s was recorded and the backup was not started: %v",
				task.ID, role.Agent, role.Fallback.Agent, err)
			return nil, err
		}
		return fail(err)
	}
	pending, err := s.unacknowledgedOf(ctx, task.ID, attempt)
	if err != nil {
		return fail(err)
	}
	launch.note += steeringNote("Continue from the work already present after the previous worker became unavailable.", pending)
	s.log.Printf("task %s worker fallback from %s to %s: %s", task.ID, role.Agent, role.Fallback.Agent, failure)
	launched, err := s.launchTask(ctx, launch)
	if err != nil {
		if errors.Is(err, store.ErrDispatchHeld) {
			// The launch claim refused the backup for kept work and gave
			// the slot back itself; the same stance as above.
			kept = true
			s.log.Printf("task %s worker fallback from %s to %s was recorded and the backup was not started, because kept work holds the task: %v",
				task.ID, role.Agent, role.Fallback.Agent, err)
			return nil, err
		}
		return fail(err)
	}
	return launched, nil
}

// A failed launch with no session is retried through dispatch's standing-copy
// sweep. It is never given a fabricated Dead observation to enable relaunch.
func (s *Service) answerWorkerFallbackRetry(ctx context.Context, r machine.TaskRespondRequest, hold store.Hold, out *machine.TaskAnswered) {
	refuse := func(err error) { out.Effect = retryNotBegun(err); out.NotRelaunched = err.Error() }
	attempts, err := s.store.TaskAttempts(ctx, r.Task)
	if err != nil {
		refuse(err)
		return
	}
	if len(attempts) == 0 {
		refuse(errRelaunchNotDue)
		return
	}
	latest := attempts[len(attempts)-1]
	if latest.SessionRef.IsKnown() {
		s.answerStopRetry(ctx, r, hold, out)
		return
	}
	answered, err := s.relaunchAnswersOf(ctx, r.Task, attempts)
	if err != nil {
		refuse(err)
		return
	}
	if decision, limited := relaunchBoundDecision(attempts, s.cfg.RelaunchAttempts, answered); limited && decision.action != reconcileRelaunch {
		if decision.action != reconcileHold {
			refuse(errRelaunchNotDue)
			return
		}
		h, err := s.holdAtBound(ctx, r.Task, attempts, decision)
		if err != nil {
			refuse(err)
			return
		}
		out.Effect = machine.AnswerRetryAtBound
		out.NotRelaunched = "the existing relaunch bound requires an answer on " + h.Key
		return
	}
	observed, err := s.tasks().ObserveTask(ctx, r.Task)
	if err != nil {
		refuse(err)
		return
	}
	state := observed.State
	dispatchable := state.State == taskstate.Dispatched || (state.State == taskstate.Failed && state.Basis.EventKind == routeRefusedKind)
	if state.Source != taskstate.SourceEvent || !dispatchable {
		refuse(errRelaunchNotDue)
		return
	}
	events, err := s.store.TaskEvents(ctx, r.Task, 0, 0)
	if err != nil {
		refuse(err)
		return
	}
	if len(events) == 0 {
		refuse(errRelaunchNotDue)
		return
	}
	if _, err = s.store.AppendTaskEventAfter(ctx, r.Task, events[len(events)-1].Sequence, store.DispatchedEventKind, "retrying the configured backup after an explicit answer"); err != nil {
		refuse(err)
		return
	}
	d, err := s.redispatchTask(ctx, r.Task)
	if err != nil {
		refuse(err)
		return
	}
	if s.afterBackupRedispatch != nil {
		s.afterBackupRedispatch(r.Task)
	}
	pending, err := s.unacknowledgedOf(ctx, r.Task, latest.Sequence)
	if err != nil {
		// The redispatch claimed its attempt and nothing launches it, so
		// the slot it took is settled here, with no session recorded: a
		// stop or a relaunch asked next must find the slot free rather
		// than a launch in flight until the service restarts.
		d.settleSlot()
		_ = s.holdWorkerFallback(ctx, r.Task, err)
		refuse(err)
		return
	}
	d.note += steeringNote("Continue from the preserved work after a backup could not start.", pending)
	out.Effect = machine.AnswerRedispatched
	out.Relaunch = &machine.RelaunchBegun{Attempt: d.attempt}
	s.launchInBackground(d)
}
