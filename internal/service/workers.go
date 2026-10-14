package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The worker group set and the authority it derives: which task a peer is
// the worker of, decided from the kernel's credentials and nothing a caller
// states, and the methods whose answer turns on it.
//
// # Two relations over one input
//
// A peer reaching this service is placed by two separate relations over the
// same credentials. The containment registry in contain.go answers whether
// the peer is inside a validation stage, and internal/ipc applies it to
// every restricted method before a handler runs. The worker group set
// answers which task the peer is the worker of, and the handlers whose
// answer depends on it apply it here: task.event admits only a worker and
// attributes the entry to its task; wakes.list, wakes.acknowledge,
// supervision.assert, coordinator.go's task.create, task.promote and
// task.relaunch, steer.go's task.message, task.stop and task.interrupt,
// taskholds.go's task.respond and catchup.go's catchup.mark are the
// coordinator's and refuse a worker, task.respond so that no worker answers a
// hold, its own task's included, and catchup.mark so that no worker moves
// what a person was shown;
// gate.admit holds a worker's push to admit_workers.go's rule; and
// gate.notify records the run a worker's push of its own task's branch
// starts with the task's intent, which pushIntent below decides. The set is
// internal/session's, produced by session.Pipe.Create for the worker
// process's lifetime, and this service reaches it through the driver that
// holds the provider. session.Workers' doc says why it is not a second
// meaning of the containment registry.
//
// # The derivation
//
// Credentials name a process, read off the socket by internal/ipc and never
// from a frame. The process belongs to a process group, read from the
// operating system by processGroup, the same read containment makes. The
// group is looked up in the set, which names the task. No step reads a
// request: a task identifier in a body would be the caller's claim, and no
// body here has a field for one.
//
// Per platform the derivation is containment's. On unix internal/agents
// starts a worker as the leader of a new process group, and what it starts
// stays in that group unless it leaves it, so the hook a worker's push runs
// is placed by the worker's group. On Windows contain_windows.go's
// processGroup refuses, since that platform reports no process group for a
// peer, so no peer is ever placed in a worker group there: with a worker
// launched, every method here is refused for want of the fact rather than
// answered on an assumption, and that refusal is already upstream of it,
// because internal/ipc identifies no peer there and refuses every restricted
// method first. That is a stated limit and not parity: on Windows a worker's
// push cannot be told from a person's, and neither can be admitted.
//
// # Residual gaps
//
//   - A descendant that leaves its process group is outside the relation,
//     which is the gap containment names for itself.
//   - The set is this service's memory. A worker an earlier service of this
//     home launched is in no set here, so its push is a person's push and its
//     events are refused; launch.go says such a worker is an orphan.
//   - With no worker launched the set is empty and the group is not read, so
//     a peer whose group cannot be read is refused only while a worker is
//     live; that is the same shape contain.go gives the registry.

// workerOf derives the task a peer is the worker of, on the terms above. The
// second result is whether the peer is in any worker group; an error is a
// fact that could not be established, which the caller refuses on rather
// than treating as "not a worker".
func (s *Service) workerOf(c ipc.Credentials) (session.Membership, bool, error) {
	s.mu.Lock()
	built := s.built
	s.mu.Unlock()
	if built == nil || built.workers.Empty() {
		return session.Membership{}, false, nil
	}
	group, err := processGroup(c.PID)
	if err != nil {
		return session.Membership{}, false, fmt.Errorf("reading the process group of %s: %w", c, err)
	}
	m, ok := built.workers.Member(group)
	return m, ok, nil
}

// workerPeer is workerOf over a request's peer. An unidentified peer is
// ipc.ErrUnidentifiedPeer, and a group that could not be read is
// ipc.ErrUnavailable, both wrapped, so neither reads as a person.
func (s *Service) workerPeer(req ipc.Request) (session.Membership, bool, error) {
	creds, err := req.Peer.Credentials()
	if err != nil {
		return session.Membership{}, false, err
	}
	m, ok, err := s.workerOf(creds)
	if err != nil {
		return session.Membership{}, false, fmt.Errorf("%w: whether %s is a task's worker could not be determined: %w",
			ipc.ErrUnavailable, creds, err)
	}
	return m, ok, nil
}

// refuseWorker refuses a request from a peer inside a worker group, for the
// coordinator's methods. It is ipc.ErrWorker naming the task.
func (s *Service) refuseWorker(req ipc.Request) error {
	m, ok, err := s.workerPeer(req)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%w: %s is the worker of task %s, and %s is the coordinator's", ipc.ErrWorker,
			peerOf(req), m.Task, req.Method)
	}
	return nil
}

// taskEvent appends an entry to the history of the task the caller is the
// worker of. The task is the derivation's and never the request's; the kind
// is held to internal/taskstate's table, refusing with ipc.ErrWorker a kind
// whose writer is the program and with ipc.ErrInvalidRequest a kind the
// table has no row for or a detail its decoder refuses. A peer outside every
// worker group is ipc.ErrNotWorker. The one kind with a record beside the
// history, store.MessageAcknowledgedEventKind, is written through
// acknowledgeMessage instead, so its event and the message record are one
// transaction. The entry is published as ipc.TypeTaskEvent once it is
// recorded.
func (s *Service) taskEvent(ctx context.Context, req ipc.Request, r machine.TaskEventRequest) (machine.Appended, error) {
	m, ok, err := s.workerPeer(req)
	if err != nil {
		return machine.Appended{}, err
	}
	if !ok {
		return machine.Appended{}, fmt.Errorf("%w: %s is in no worker group, so there is no task to append to",
			ipc.ErrNotWorker, peerOf(req))
	}
	row, known := taskstate.Lookup(r.Kind)
	if !known {
		return machine.Appended{}, fmt.Errorf("%w: %q is not an event kind this build gives a meaning to",
			ipc.ErrInvalidRequest, r.Kind)
	}
	if row.Writer != taskstate.WriterWorker {
		return machine.Appended{}, fmt.Errorf("%w: %q is a kind only the program appends, and the worker of task %s may not claim it",
			ipc.ErrWorker, r.Kind, m.Task)
	}
	if row.Detail != nil {
		if err := row.Detail(r.Detail); err != nil {
			return machine.Appended{}, fmt.Errorf("%w: the detail of a %q event: %w", ipc.ErrInvalidRequest, r.Kind, err)
		}
	}
	if r.Kind == store.MessageAcknowledgedEventKind {
		return s.acknowledgeMessage(ctx, m.Task, r.Detail)
	}
	event, err := s.store.AppendTaskEvent(ctx, m.Task, r.Kind, r.Detail)
	if err != nil {
		return machine.Appended{}, err
	}
	s.publishTaskEvent(event)
	return machine.Appended{Event: event}, nil
}

// acknowledgeMessage is the worker's acknowledgement of a message to its
// task, PRD section 6's: the row's decoder has already held the detail to a
// message identifier, and store.AcknowledgeMessage appends the event and
// records the acknowledgement on the message in one transaction, so the
// history and the record cannot disagree (P8). A message that is not the
// task's, one already acknowledged and one refused are each
// ipc.ErrInvalidRequest with nothing appended. The entry is published as
// every worker entry is.
func (s *Service) acknowledgeMessage(ctx context.Context, task, detail string) (machine.Appended, error) {
	id, err := store.ParseMessageID(detail)
	if err != nil {
		return machine.Appended{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	event, _, err := s.store.AcknowledgeMessage(ctx, task, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrMessageAcknowledged), errors.Is(err, store.ErrMessageRefused):
		return machine.Appended{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	case err != nil:
		return machine.Appended{}, err
	}
	s.publishTaskEvent(event)
	return machine.Appended{Event: event}, nil
}

// publishTaskEvent tells subscribers a history grew. It is a state event on
// ipc's terms, so it carries a revision.
func (s *Service) publishTaskEvent(event store.TaskEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := s.events.Publish(ipc.Event{Type: ipc.TypeTaskEvent, Revision: s.revision.Add(1), Payload: payload}); err != nil {
		s.log.Printf("could not publish event %d of task %s: %v", event.Sequence, event.TaskID, err)
	}
}

// listWakes drains the queue for the coordinator, on store.DrainWakes'
// terms: oldest first, bounded, acknowledging nothing. A worker is refused.
func (s *Service) listWakes(ctx context.Context, req ipc.Request, r machine.WakesRequest) (machine.Wakes, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.Wakes{}, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = machine.DefaultWakesLimit
	}
	wakes, err := s.store.DrainWakes(ctx, limit)
	if err != nil {
		return machine.Wakes{}, err
	}
	return machine.Wakes{Wakes: wakes}, nil
}

// acknowledgeWakes records that the coordinator handled the named wakes.
// Who acknowledged is req.WakeAcknowledger, derived from the surface and
// read from nothing the caller sent. A worker is refused; a batch the store
// refuses, for an unknown or an already acknowledged identifier, writes
// nothing and the refusal is the store's.
func (s *Service) acknowledgeWakes(ctx context.Context, req ipc.Request, r machine.AcknowledgeRequest) (machine.Acknowledged, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.Acknowledged{}, err
	}
	if len(r.Wakes) == 0 {
		return machine.Acknowledged{}, fmt.Errorf("%w: an acknowledgement names no wakes", ipc.ErrInvalidRequest)
	}
	by := req.WakeAcknowledger()
	if err := s.store.AcknowledgeWakes(ctx, r.Wakes, by); err != nil {
		var refused *store.WakeIDError
		if errors.As(err, &refused) {
			return machine.Acknowledged{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
		}
		return machine.Acknowledged{}, err
	}
	return machine.Acknowledged{Wakes: r.Wakes, By: by.String()}, nil
}

// assertSupervision is PRD section 5's arm verification over this home,
// answered by feed.VerifyArm from the beacon, the instance file and the lock
// under the configured beacon interval. It reads what a command with the
// service down would read, so a loop that is not writing is reported as
// such from inside the service that should be running it. A worker is
// refused. A context that ends before the verdict settles is answered as
// the failed arm VerifyArm gives with the context's error.
func (s *Service) assertSupervision(ctx context.Context, req ipc.Request) (supervisor.Arm, error) {
	if err := s.refuseWorker(req); err != nil {
		return supervisor.Arm{}, err
	}
	return feed.VerifyArm(ctx, s.home, s.cfg.SupervisionBeaconInterval, time.Now)
}

// pushedIntent is the intent a run a push starts is recorded with, and where
// that intent came from.
type pushedIntent struct {
	text   string
	source string
}

// noPushedIntent is what every push but a task worker's own carries: no
// intent, recorded as absent.
var noPushedIntent = pushedIntent{source: intentSourceAbsent}

// pushIntent decides the intent of the run a push's update starts, from the
// records and the pusher's credentials and from nothing the push says.
//
// A run whose update is a task's own branch, in the repository the gate
// resolved to, pushed from inside that task's worker group, takes its
// intent from the task: the task record's project as the first line and the
// task's instructions, as the coordinator wrote them at intake, as the body.
// It is recorded under intentSourceTask, never intentSourceSupplied, so the
// run starts with pipeline.Start.IntentSupplied false and every stage frames
// it as a hint rather than as acceptance criteria a person stated.
//
// Everything else carries no intent, as every push did before: a person's
// push, a push whose pusher's group could not be read (followed as a
// person's, which is the path that claims less), and a worker's update to
// anything but its own task's branch, which admission refuses before this
// runs. A task record that cannot be read is logged and the run carries no
// intent, because the intent is a hint the run can do without and a push
// already admitted cannot be refused from here.
//
// The text passes through internal/redact before it is recorded, because it
// reaches the run record and, through the pull request stage, a code host.
//
// Residual gaps: the instructions file is a file under the home that the
// worker's own user can write, so what it holds when the push arrives is as
// trustworthy as that directory is against the worker, which is why the
// intent is a hint and not a contract; and a task's instructions name what
// the coordinator asked the worker for, which PRD section 5's "what the gate
// does not establish" leaves where it was: the lens measures the change
// against that text and never asks where it came from.
func (s *Service) pushIntent(ctx context.Context, from ipc.Request, found subject, update gate.RefUpdate) pushedIntent {
	m, worker, err := s.workerPeer(from)
	if err != nil || !worker {
		return noPushedIntent
	}
	task, err := s.store.Task(ctx, m.Task)
	if err != nil {
		s.log.Printf("the worker of task %s pushed %s and the task could not be read, so its run carries no intent: %v",
			m.Task, update.Ref, err)
		return noPushedIntent
	}
	if task.Shape != store.TaskDelivery || task.RepositoryID.Or("") != found.repository.ID ||
		task.Branch.Or("") != update.Branch() {
		return noPushedIntent
	}
	return pushedIntent{text: s.taskIntent(task), source: intentSourceTask}
}

// taskIntent composes a task's intent: its project, then its instructions as
// intakeInstructions reads them. Instructions that cannot be read leave the
// project alone, which is logged; intake refuses an empty project, so the
// text is never empty for a task intake recorded.
func (s *Service) taskIntent(task store.Task) string {
	project := strings.TrimSpace(task.Project)
	body := ""
	path, err := s.home.TaskInstructions(task.ID)
	if err == nil {
		var raw []byte
		if raw, err = os.ReadFile(path); err == nil {
			body = intakeInstructions(string(raw))
		}
	}
	if err != nil {
		s.log.Printf("the instructions of task %s could not be read, so its run's intent is its project alone: %v",
			task.ID, err)
	}
	text := project
	if body != "" {
		text = project + "\n\n" + body
	}
	return redact.New().Redact(strings.TrimSpace(text))
}

// intakeInstructions is the part of an instructions file the coordinator
// wrote at intake: everything before the first section the program appended,
// the isolation check dispatch appends (secondCheckHeading) or the
// promotion section internal/tasks appends (tasks.PromotionHeading), each
// recognized only at the start of a line. Those sections are the program's
// words to the worker, and they carry a shell line naming paths under the
// home, so they are not what the work is for and do not reach a code host.
//
// What that leaves out is a promoted task's delivery intent, which is in the
// promotion section: such a task's run carries its project and its
// investigation instructions. And text the coordinator wrote that happens to
// begin a line with one of those headings ends its own instructions there,
// which can only shorten the intent and never add to it.
func intakeInstructions(text string) string {
	end := len(text)
	for _, heading := range []string{secondCheckHeading, tasks.PromotionHeading} {
		for offset := 0; offset < end; {
			i := strings.Index(text[offset:], heading)
			if i < 0 {
				break
			}
			at := offset + i
			if at == 0 || text[at-1] == '\n' {
				end = min(end, at)
				break
			}
			offset = at + len(heading)
		}
	}
	return strings.TrimSpace(text[:end])
}
