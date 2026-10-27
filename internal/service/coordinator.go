package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The coordinator's task methods: task.create, task.promote and
// task.relaunch, PRD section 9's three service methods beside the command
// table (#95 lines A, E and J), and task.relay, the coordinator's record of
// section 6's "findings relayed" (#278). Its paragraph's two steering methods,
// task.message and task.stop, and the refused task.interrupt are steer.go's,
// on this file's terms for who may ask and how a refusal crosses the wire.
//
// # None is a second implementation
//
// Each method is a caller of the library entry that already owns the act:
// task.create calls internal/tasks' Queue.Create, task.promote calls its
// Queue.Promote, and task.relaunch calls relaunchTask, the one anchored
// relaunch reconcile.go's start-time pass calls, and then hands the launch
// to launchInBackground exactly as that pass does; and task.relay calls
// store.RecordTaskRelay, the one writer of the findings relayed record,
// anchored to the resolution it read. A promotion then sends the task's live
// worker its brief through steer.go's deliverMessage, the route task.message
// takes, rather than a send of its own. What this file adds is who may ask
// and how an answer or a refusal crosses the wire.
//
// # Who may ask
//
// Each is a coordinator's, on two checks over the caller's credentials and
// nothing the caller says about itself. internal/ipc declares all four
// restricted, so a caller it cannot identify is refused with
// ipc.ErrUnidentifiedPeer and a caller contained by a validation stage with
// ipc.ErrContained before this file runs. Then the placement
// subordinates.go derives, the one the wakes methods and supervision.assert
// read too, decides who is asking before anything is read or written: a
// caller the worker group set places in any task's worker group is refused
// with ipc.ErrWorker, which is what keeps an investigation from promoting
// itself, since its worker's own process group is in the set while it runs
// and the request is refused whatever task it names. A caller the
// subordinate set places as a domain's subordinate coordinator is admitted
// to three of the four within that domain, PRD section 9's grant:
// task.create, whose task is written into the domain from the placement
// and takes the delegation's posture, and task.relaunch and task.relay,
// each refused with ipc.ErrOutsideDomain for a task whose row is outside
// the domain. task.promote is the one it is refused, with
// ipc.ErrSubordinate, and the refused ask is recorded as its escalation;
// subordinates.go owns that and the rest of the grant. No second placement
// check is built here.
//
// # How a refusal crosses the wire
//
// A refusal stays a typed result, on refusalOf's terms: a request intake or
// promotion cannot read is ipc.ErrInvalidRequest, a request the records do
// not allow is ipc.ErrRefused, and a fact the service could not establish is
// ipc.ErrUnavailable. The typed error the library returned is wrapped beside
// the code, so this package's own callers still match it with errors.As;
// over the wire the code and the message travel, which is internal/ipc's
// rule for every failure.
//
// # Residual gaps
//
//   - The placement is workers.go's and carries its gaps: a descendant that
//     leaves its worker's process group is outside it, a worker an earlier
//     service of this home launched is in no set here, and with no worker
//     launched the group is not read at all.
//   - Where internal/ipc reads no local socket peer credentials, every
//     restricted method is refused, these three included, so on such a
//     platform no coordinator can call them either: that is the platform
//     guard internal/ipc states, failing closed, and not parity.
//   - A caller outside every worker group is taken as the coordinator. The
//     service has no relation that names the coordinator positively, so a
//     person's own client on the socket, which is the same user, is
//     admitted on the same terms; PRD section 9's rule is that no worker can
//     call these, and that is what is enforced. The one request decided the
//     other way round, the local landing's approval hold that PRD section 6
//     gives the person, is not here: taskholds.go's task.respond holds it to
//     foreground.go's relation, which keeps a host's tool command out
//     without naming the coordinator either.
//   - task.create and task.relaunch answer once their records are written:
//     the copy a created task is cut and the worker a relaunch starts are
//     the service's work afterwards, so their outcome is read from the
//     task and its attempts rather than from this answer.

// createTask records a new task through internal/tasks' intake, for the
// coordinator or, within its domain and under its delegation's posture, for
// a subordinate coordinator, and then dispatches what is due in the background, as
// releaseQueued does after a release: a task intake recorded independent
// resolves Dispatched and is cut its copy by dispatch.go.
//
// The intake runs under the write side of the service's intake exclusion,
// because intake writes the task's record before its instructions file and
// the record of an independent task already reads Dispatched: a dispatch
// pass that read the tasks between the two would refuse the task for having
// no instructions and keep its slot. dispatchDue reads under the read side
// and skips its pass while an intake holds or is taking the write side, so
// the pass that follows here is run whether or not the intake was refused:
// it is the pass a skipped one left its tasks to.
func (s *Service) createTask(ctx context.Context, req ipc.Request, r machine.TaskCreateRequest) (machine.TaskCreated, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.TaskCreated{}, err
	}
	request := tasks.Request{
		Shape: r.Shape, Project: r.Project, Instructions: r.Instructions, Mode: r.Mode,
		Repository: r.Repository, Branch: r.Branch, Overlaps: r.Overlaps, Delivery: r.Delivery,
		Role: r.Role, Housekeeping: r.Housekeeping,
	}
	if p.isSubordinate && r.Housekeeping {
		// PRD section 9: a subordinate coordinator is refused creating a
		// housekeeping pass, which is about a repository and so in no
		// domain: it is the first coordinator's to create.
		return machine.TaskCreated{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and creating a housekeeping pass is one it does not hold",
			ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain)
	}
	if p.isSubordinate {
		// PRD section 6: a subordinate records no posture, and section 9:
		// the domain is written from the placement and named by no field.
		// The delivery posture is the delegation's, read now and held to
		// intake's rule against the task's project by intake itself; a
		// request asking for one of its own is refused rather than
		// compared, and an investigation takes none, as intake says.
		if strings.TrimSpace(r.Delivery) != "" {
			return machine.TaskCreated{}, fmt.Errorf("%w: the subordinate coordinator of domain %s records no delivery posture; "+
				"its tasks take the delegation's", ipc.ErrInvalidRequest, p.subordinate.Domain)
		}
		d, found, err := s.store.Delegation(ctx, p.subordinate.Domain)
		if err != nil {
			return machine.TaskCreated{}, err
		}
		if !found || !d.Standing() {
			return machine.TaskCreated{}, fmt.Errorf("%w: %w: domain %s", ipc.ErrRefused, errNotDelegated, p.subordinate.Domain)
		}
		request.Domain = p.subordinate.Domain
		if store.TaskShape(strings.TrimSpace(r.Shape)) == store.TaskDelivery {
			request.Delivery = string(d.Delivery)
		}
	}
	s.intake.Lock()
	created, err := s.queue.Create(ctx, request)
	s.intake.Unlock()
	if err == nil {
		s.log.Printf("task %s was created by %s (%s)%s", created.Task.ID, peerOf(req), created.Cause, inDomain(created.Task.Domain))
	}
	// Refused or not, this is the pass a pass skipped during the intake left
	// its tasks to.
	s.dispatchInBackground()
	if err != nil {
		return machine.TaskCreated{}, refusalOf(err)
	}
	return machine.TaskCreated{
		Record: created.Task, Cause: string(created.Cause), BlockerState: created.BlockerState,
		Instructions: created.Instructions,
	}, nil
}

// promoteTask flips an investigation task to delivery through internal/tasks'
// promotion, for the coordinator. The posture the promoted task is recorded
// under is read by that promotion through projectPosture, the reader intake
// uses, so a trusted copy this service cannot read refuses it as
// ipc.ErrUnavailable on refusalOf's terms. The environment the copy is
// asserted under is the one every worker of this service is started with,
// the same agents.WorkerEnvironment(os.Environ(), nil) assertCopy uses, and
// never one the caller states.
func (s *Service) promoteTask(ctx context.Context, req ipc.Request, r machine.TaskPromoteRequest) (machine.TaskPromoted, error) {
	if _, err := s.refuseDecision(ctx, req, r.Task, fmt.Sprintf("asked to promote task %s to a delivery on %s in repository %s: %s",
		r.Task, r.Branch, r.Repository, r.Intent)); err != nil {
		return machine.TaskPromoted{}, err
	}
	promoted, err := s.queue.Promote(ctx, tasks.Promotion{
		Task: r.Task, Repository: r.Repository, Branch: r.Branch, Intent: r.Intent, Delivery: r.Delivery,
		By:          req.HoldResolver(),
		Environment: agents.WorkerEnvironment(os.Environ(), nil),
	})
	// The flip answers the copy's open kept work, if any, as carry on, which
	// no longer holds the project's new tasks, so a dispatch pass is asked
	// for as it is after any answer to kept work. It is asked whatever the
	// promotion returned: one that failed after its flip, creating the
	// branch among the ways, has answered the entries all the same, and a
	// pass over nothing freed dispatches nothing.
	s.dispatchInBackground()
	if err != nil {
		return machine.TaskPromoted{}, refusalOf(err)
	}
	s.log.Printf("task %s was promoted to delivery on %s by %s", r.Task, r.Branch, peerOf(req))
	out := machine.TaskPromoted{
		Record: promoted.Task, Event: promoted.Event, Head: promoted.Head, Instructions: promoted.Instructions,
	}
	if report, ok := promoted.Report.Get(); ok {
		at := report.RecordedAt
		out.ReportRecordedAt = &at
	}
	out.Brief = s.briefPromoted(ctx, req, r.Task, promoted.Brief)
	return out, nil
}

// briefPromoted sends a promoted task's live worker its brief, the text the
// promotion appended to the task's instructions, through deliverMessage,
// the route task.message takes, so the brief is a message record the worker
// acknowledges like any other. It sends only to the latest attempt's worker
// when this service's Terminals probe it Live, which is answered only for a
// session this service holds whose process has not exited; for any other
// worker nothing is recorded and Unsent says why, because a record appended
// for a worker nothing can reach would be refused by the send anyway, and
// the instructions file already carries the brief to whatever worker is
// begun on the task next. A brief over machine.MaxMessageBytes is not sent
// either, on task.message's terms. Nothing here undoes or refuses the
// promotion, which stands whatever became of the brief: the answer carries
// what did, typed.
//
// The probe and the send are two steps: a worker that exits between them is
// sent nothing, and the record says refused with the reason, as a message to
// it would.
func (s *Service) briefPromoted(ctx context.Context, req ipc.Request, task, brief string) machine.Briefing {
	unsent := func(format string, args ...any) machine.Briefing {
		b := machine.Briefing{Unsent: fmt.Sprintf(format, args...)}
		s.log.Printf("the brief for promoted task %s was not sent: %s", task, b.Unsent)
		return b
	}
	if len(brief) > machine.MaxMessageBytes {
		return unsent("the brief is %d bytes, over the message limit of %d", len(brief), machine.MaxMessageBytes)
	}
	latest, ok, err := s.latestAttempt(ctx, task)
	if err != nil {
		return unsent("the task's attempts could not be read: %v", err)
	}
	ref, launched := latest.SessionRef.Get()
	if !ok || !launched {
		return unsent("the task's latest attempt names no worker")
	}
	if s.terminals == nil {
		return unsent("this service has no terminal hookup to probe the worker with")
	}
	bound, cancel := context.WithTimeout(ctx, stopProbeBound)
	observed := s.terminals.Probe(bound, ref)
	cancel()
	if observed.Liveness != taskstate.Live {
		return unsent("the worker of attempt %d (session %s) is not one this service can reach: the probe answered %s (%s)",
			latest.Sequence, ref, observed.Liveness, observed.Detail)
	}
	m, err := s.deliverMessage(ctx, req, req.MessageSender(), task, latest.Sequence, ref, brief)
	if err != nil {
		b := machine.Briefing{Unsent: err.Error()}
		if m.ID != 0 {
			b.Message = &m
		}
		return b
	}
	return machine.Briefing{Message: &m}
}

// relaunchTaskFor relaunches a task whose terminal is gone, for the
// coordinator or for the subordinate coordinator of the task's domain,
// through relaunchTask and launchInBackground: the claim, the records and
// the launch reconciliation makes, and nothing else. A worker is refused, a
// subordinate naming a task outside its domain is refused
// ipc.ErrOutsideDomain, and a task the reconciliation table does not
// relaunch, a live terminal among them, is refused there and not here.
func (s *Service) relaunchTaskFor(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.TaskRelaunched, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.TaskRelaunched{}, err
	}
	if r.Task == "" {
		return machine.TaskRelaunched{}, fmt.Errorf("%w: a relaunch names no task", ipc.ErrInvalidRequest)
	}
	if err := s.scopeTask(ctx, req, p, r.Task); err != nil {
		return machine.TaskRelaunched{}, err
	}
	out, err := s.relaunchTask(ctx, r.Task)
	if err != nil {
		return machine.TaskRelaunched{}, refusalOf(err)
	}
	s.log.Printf("task %s was relaunched in place as attempt %d by %s, superseding session %s; %s",
		r.Task, out.launch.attempt, peerOf(req), out.superseded, out.swept)
	s.launchInBackground(out.launch)
	return machine.TaskRelaunched{
		Task: r.Task, Attempt: out.launch.attempt, Superseded: out.superseded, Swept: out.swept,
	}, nil
}

// relayTask records, for the coordinator or for the subordinate coordinator
// of the task's domain, that a reported investigation's findings were
// relayed: PRD section 6's Reported to Complete edge. A worker is refused
// and a subordinate naming a task outside its domain is refused
// ipc.ErrOutsideDomain, on relaunchTaskFor's terms. The task
// is read through the service's reader for the history position to anchor
// to, and the record is written by store.RecordTaskRelay, which decides in
// its own transaction whether the records make the task a reported
// investigation: an investigation task with a stored report, no open hold
// naming it and no relay recorded, which are the facts behind
// internal/taskstate's Reported from the report tier and the hold tier above
// it. Anything else is refused there and crosses the wire as ipc.ErrRefused,
// and a history that moved since the read refuses the relay rather than
// recording it past what was appended (P6). The task then resolves Complete
// from the record, and the held worker and the copy are left to landed.go's
// terms for a task in the terminal tier, asked at once on a goroutine
// through settleLanded and again by the supervisor's pass.
//
// The record is the coordinator's word: nothing here witnesses a relay. The
// run tier, which sits between the hold tier and the report tier, is not
// re-checked: an investigation pushes nothing and is attributed no run by
// its branch, so in this build only a run its own row names could reach it,
// and no production path names one for an investigation.
func (s *Service) relayTask(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.TaskRelayed, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.TaskRelayed{}, err
	}
	if r.Task == "" {
		return machine.TaskRelayed{}, fmt.Errorf("%w: a relay names no task", ipc.ErrInvalidRequest)
	}
	if err := s.scopeTask(ctx, req, p, r.Task); err != nil {
		return machine.TaskRelayed{}, err
	}
	resolved, err := s.tasks().Resolved(ctx, r.Task)
	if err != nil {
		return machine.TaskRelayed{}, err
	}
	relay, event, err := s.store.RecordTaskRelay(ctx, r.Task, resolved.State.Basis.EventSequence)
	if err != nil {
		return machine.TaskRelayed{}, refusalOf(err)
	}
	s.publishTaskEvent(event)
	s.log.Printf("task %s's findings were recorded relayed by %s", r.Task, peerOf(req))
	s.settleInBackground(r.Task)
	return machine.TaskRelayed{Record: relay, Event: event}, nil
}

// inDomain is the words a log line adds for a task in a domain, and nothing
// for one in none.
func inDomain(domain string) string {
	if domain == "" {
		return ""
	}
	return " in domain " + domain
}

// settleInBackground runs settleLanded for the task on a goroutine
// registered through startWork, so the answer that wrote a terminal record
// does not wait on ending a worker or on the cleanup decision, and Close
// still waits for both. A service giving up its home leaves both to the
// supervisor's pass or the next start.
func (s *Service) settleInBackground(id string) {
	if !s.startWork() {
		s.log.Printf("task %s's worker and copy were left for the next start because this service is giving up the home", id)
		return
	}
	go func() {
		defer s.work.Done()
		s.settleLanded(s.stopCtx, id)
	}()
}

// dispatchInBackground runs dispatchDue on a goroutine registered through
// startWork, so the answer that recorded a task does not wait on cutting its
// copy and Close still waits for the dispatch.
func (s *Service) dispatchInBackground() {
	if !s.startWork() {
		s.log.Printf("dispatching was not started because this service is giving up the home; the next start dispatches")
		return
	}
	go func() {
		defer s.work.Done()
		s.dispatchDue(s.stopCtx)
	}()
}

// refusalOf classifies what a library entry returned for the wire, wrapping
// the code beside the error so errors.As still finds the typed refusal: a
// request intake or promotion could not read is ipc.ErrInvalidRequest, a
// fact the service could not establish is ipc.ErrUnavailable, and every
// typed refusal the records decided is ipc.ErrRefused. An error that is
// none of these is a failure of a read or a write and is returned as it is,
// which the wire reports as internal.
//
// It names the promotion and store refusals itself because tasks.IsRefusal is
// intake-shaped and answers for none of them.
func refusalOf(err error) error {
	var held *relaunchHeld
	var unswept *copyUnswept
	var unproven *relaunchUnswept
	switch {
	case errors.Is(err, tasks.ErrRequest):
		return fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	case errors.Is(err, errRelaunchUnprobed), errors.Is(err, tasks.ErrPostureUnreadable), errors.Is(err, tasks.ErrPassScopeUnreadable):
		return fmt.Errorf("%w: %w", ipc.ErrUnavailable, err)
	case tasks.IsRefusal(err),
		errors.Is(err, tasks.ErrNotPromotable),
		errors.Is(err, tasks.ErrBranchHeld),
		errors.Is(err, tasks.ErrPromotionRefused),
		errors.Is(err, tasks.ErrNotIsolated),
		errors.Is(err, store.ErrTaskMoved),
		errors.Is(err, store.ErrTaskShapeMoved),
		errors.Is(err, store.ErrCleanupRecorded),
		errors.Is(err, store.ErrKeptWorkCopyOwed),
		errors.Is(err, store.ErrKeptWorkUnverified),
		errors.Is(err, tasks.ErrLayoutUnsupported),
		errors.Is(err, tasks.ErrKeptWorkUnheld),
		errors.Is(err, store.ErrNotRelayable),
		errors.Is(err, store.ErrPassRetired),
		errors.Is(err, store.ErrTaskRelayed),
		errors.Is(err, errRelaunchNotDue),
		errors.Is(err, errRelaunchNotDead),
		errors.Is(err, errRelaunchInFlight),
		errors.As(err, &held),
		errors.As(err, &unswept),
		errors.As(err, &unproven):
		return fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	return err
}
