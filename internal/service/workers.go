package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
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
// # Two relations over one input, and a third for one hold
//
// A peer reaching this service is placed by two separate relations over the
// same credentials. The containment registry in contain.go answers whether
// the peer is inside a validation stage, and internal/ipc applies it to
// every restricted method before a handler runs. The worker group set
// answers which task the peer is the worker of, and the handlers whose
// answer depends on it apply it here; foreground.go is a third relation
// over the same credentials, whether the peer is in the foreground of its
// controlling terminal, asked by task.respond for the local landing's
// approval hold alone and after the worker rule. task.event admits only a worker and
// attributes the entry to its task; wakes.list, wakes.peek, wakes.acknowledge,
// wakewait.go's wakes.wait and wakes.subscribe, supervision.assert, coordinator.go's task.create, task.promote,
// task.relaunch and task.relay, steer.go's task.message, task.stop and task.interrupt,
// taskholds.go's task.respond and catchup.go's catchup.mark are the
// coordinator's and refuse a worker, task.respond so that no worker answers a
// hold, its own task's included, and catchup.mark so that no worker moves
// what a person was shown;
// gate.admit holds a worker's push to admit_workers.go's rule, admits a
// shell's, and refuses a push placed as neither, on the terms below; and
// gate.notify records the run a worker's push of its own task's branch
// starts with the task's intent, which pushIntent below decides. The set is
// internal/session's, produced by the provider's Create, session.Pipe's or
// session.Tmux's, for the worker process's lifetime, and this service reaches it through the driver that
// holds the provider. session.Workers' doc says why it is not a second
// meaning of the containment registry.
//
// A third answer over the same credentials is which domain the peer is the
// subordinate coordinator of, derived by the same walk over
// internal/session's subordinate set, which subordinates.go's placeRequest
// composes with this file's answer: the worker sets are asked first, so a
// peer in both is the worker, the side that claims less, and a chain that
// cannot be read refuses as it does here. Which methods turn on that answer,
// and which way each goes, is subordinates.go's.
//
// # The derivation
//
// Credentials name a process, read off the socket by internal/ipc and never
// from a frame. placement.go walks that process's parent chain, as the
// operating system reports it, to the first process inside a worker group
// the set holds, that is a worker the set holds, or that is the host of a
// worker's terminal the set holds, and the set names the task. No step
// reads a request: a task identifier in a body would be the caller's
// claim, and no body here has a field for one.
//
// The chain rather than the peer's own group is what places a worker's
// descendants whatever process group they run in: a command the Claude Code
// CLI runs in a group of its own, as #225 observed of its Bash tool, is
// still a descendant of the worker while the processes between them stand,
// so the hook a worker's push runs is placed by the worker's process. The
// host is what places a tmux worker's windows: the worker's own server is
// reachable from its pane through the socket tmux names in its
// environment, and a window it opens there is the server's child and not
// the pane's, so the membership carries the server's identity and a push
// from any window of that server, the worker's or a person's, is held to
// the worker's rule rather than admitted as a person's; a person pushes
// as a person from a shell of their own.
// placement.go says why that and not anything the worker's environment
// carries, what it reads on each platform, and the descendant it loses. On
// Windows, and on every platform but Linux and macOS, nothing is read:
// internal/ipc identifies no peer there and refuses every restricted method
// first, the rules here refuse an unidentified peer, and gate.notify records
// no intent for one, so with a worker launched a worker's push cannot be
// told from a person's and neither is admitted. That is a stated limit and
// not parity.
//
// # Which way each rule goes when placement cannot tell
//
// Placement has three answers and an error. The answers are a worker of a
// named task; a shell at a terminal, which is a chain with no worker on it
// and a process in the foreground of its controlling terminal on it; and
// nobody, a chain read to its end with neither. The error is a chain it
// could not read and never stands for any answer. Each rule takes the
// error toward the side that claims less: gate.admit refuses the push with
// ipc.ErrUnavailable, so admission does not open on a fact it could not
// establish; the coordinator's methods refuse; task.event refuses; and
// gate.notify records the run with no intent.
//
// The two rules read the answers differently, on purpose. The coordinator's
// methods and task.event ask one question, worker or not, through workerOf:
// a shell at a terminal and nobody are both "not a worker" there, so the
// coordinator is served from a host's tool command with no terminal, from a
// script, and from a process the kernel reparented alike, and no coordinator
// method is refused for the shape of its chain. gate.admit asks the whole
// three-way question through placePusher, because a push is the one request
// that cannot be taken back once served: a worker's push is held to
// admit_workers.go's rule, a shell's push is admitted on the terms every
// push met before the worker rule existed, and a push placed as nobody's is
// refused with ipc.ErrUnplaceable before any reference moves, naming the
// chain that was read. That is PRD section 9's "authority comes from
// authenticated process ancestry" applied to the one surface where "not a
// worker" used to be read as "a person": the chain of a daemon, of a cron
// job, of an ssh session with no pseudo-terminal, and of git-receive-pack
// under sshd all end at init with no terminal on them, and each used to be
// admitted as a person's.
//
// Nobody is also what placement.go's named shape now produces for a real
// worker: a chain the kernel rewrote when a process between the peer and
// the worker exited. Such a push used to be admitted as a person's,
// unconfined by admit_workers.go's rule, and started a run with no intent;
// it is refused now, which is the safe side and not a regression. A refused
// push loses nothing (P6): the commit stands in the worker's copy, the
// refusal says what was read, and a push whose chain stands, one the worker
// runs and waits for rather than backgrounds, is admitted under the worker
// rule. An admitted stranger's push cannot be undone, and a worker's push
// admitted as a person's was unconfined. Such a process may still call the
// coordinator's methods, and task.event still refuses it; the coordinator's
// refusal is the one rule this leaves open, and only for a process the
// worker's own commands took out of its tree.
//
// # Residual gaps
//
//   - The chain placement.go cannot follow, above, and the rest of what that
//     file says it does not cover, a rewritten chain that still carries a
//     terminal's foreground among them.
//   - The set is this service's memory. A worker an earlier service of this
//     home launched is in no set here, so its push is placed by its chain
//     alone, as a shell's if a terminal's foreground is above it and as
//     nobody's otherwise, and its events are refused; launch.go says such a
//     worker is an orphan.
//   - With no worker launched the set is empty and workerOf reads no chain,
//     so a peer whose chain cannot be read is refused by the coordinator's
//     methods only while a worker is live; that is the same shape contain.go
//     gives the registry. placePusher reads the chain for every push,
//     worker or none, so the gate refuses an unreadable chain always.
//   - The containment registry is a separate relation and still reads the
//     peer's own process group, so a command a stage's agent runs in a group
//     of its own is not inside that stage; contain_unix.go's processGroup
//     says so for itself.

// workerOf derives the task a peer is the worker of, on the terms above. The
// second result is whether the peer is placed as any task's worker; an error is a
// fact that could not be established, which the caller refuses on rather
// than treating as "not a worker". With no worker in any set no chain is
// read, which the residual gaps above state.
func (s *Service) workerOf(c ipc.Credentials) (session.Membership, bool, error) {
	sets := s.workerSets()
	empty := true
	for _, set := range sets {
		if set != nil && !set.Empty() {
			empty = false
			break
		}
	}
	if empty {
		return session.Membership{}, false, nil
	}
	m, ok, err := placeByAncestry(c.PID, readLineage, sets)
	if err != nil {
		return session.Membership{}, false, fmt.Errorf("placing %s: %w", c, err)
	}
	return m, ok, nil
}

// placeOf is placement.go's three-way answer for a peer, over every worker
// set this service holds. The chain is read whether or not any set holds a
// worker, because the answer a push turns on, a terminal's foreground on
// the chain, is there to read with no worker launched at all. An error is a
// chain that could not be read, which the caller refuses on.
func (s *Service) placeOf(c ipc.Credentials) (placement, error) {
	p, err := placeAlongChain(c.PID, readLineage, s.workerSets(), s.subordinateSets())
	if err != nil {
		return placement{}, fmt.Errorf("placing %s: %w", c, err)
	}
	return p, nil
}

// workerSets gathers every worker set this service holds: the driver's and
// each launched host's.
func (s *Service) workerSets() workerSets {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sets workerSets
	if s.built != nil && s.built.workers != nil {
		sets = append(sets, s.built.workers)
	}
	for _, launched := range s.launched {
		if launched.host != nil {
			sets = append(sets, launched.host.Workers())
		}
	}
	return sets
}

// workerSets is the placer over every worker set this service holds: the
// first set answering for a group or a host answers for all.
type workerSets []*session.Workers

// empty reports whether no set holds a worker at all.
func (sets workerSets) empty() bool {
	for _, set := range sets {
		if set != nil && !set.Empty() {
			return false
		}
	}
	return true
}

// Member is session.Workers.Member over every set.
func (sets workerSets) Member(group int) (session.Membership, bool) {
	for _, set := range sets {
		if set == nil {
			continue
		}
		if m, ok := set.Member(group); ok {
			return m, true
		}
	}
	return session.Membership{}, false
}

// Hosted is session.Workers.Hosted over every set.
func (sets workerSets) Hosted(host agents.Identity) (session.Membership, bool) {
	for _, set := range sets {
		if set == nil {
			continue
		}
		if m, ok := set.Hosted(host); ok {
			return m, true
		}
	}
	return session.Membership{}, false
}

// workerPeer is workerOf over a request's peer. An unidentified peer is
// ipc.ErrUnidentifiedPeer, and an ancestry that could not be read is
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

// placePusher is placeOf over a push's peer, for gate.admit. An
// unidentified peer is ipc.ErrUnidentifiedPeer, and an ancestry that could
// not be read is ipc.ErrUnavailable, both wrapped, so neither reads as any
// of the three answers.
func (s *Service) placePusher(req ipc.Request) (placement, error) {
	creds, err := req.Peer.Credentials()
	if err != nil {
		return placement{}, err
	}
	p, err := s.placeOf(creds)
	if err != nil {
		return placement{}, fmt.Errorf("%w: whether %s is a task's worker or a shell at a terminal could not be determined: %w",
			ipc.ErrUnavailable, creds, err)
	}
	return p, nil
}

// refuseWorker refuses a request from a peer placed as a task's worker, for the
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
// table has no row for or a detail its decoder refuses. A peer placed as no
// task's worker is ipc.ErrNotWorker. The two kinds with a record beside the
// history are written through that record's writer instead, so the event
// and the record are one transaction: store.MessageAcknowledgedEventKind
// through acknowledgeMessage, and store.ReportReadyEventKind through
// route.go's fileReport, for which the request's detail is the report
// rather than the entry's. The entry is published as ipc.TypeTaskEvent once
// it is recorded.
func (s *Service) taskEvent(ctx context.Context, req ipc.Request, r machine.TaskEventRequest) (machine.Appended, error) {
	m, ok, err := s.workerPeer(req)
	if err != nil {
		return machine.Appended{}, err
	}
	if !ok {
		return machine.Appended{}, fmt.Errorf("%w: %s is placed as no task's worker, so there is no task to append to",
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
	switch r.Kind {
	case store.MessageAcknowledgedEventKind:
		return s.acknowledgeMessage(ctx, m.Task, r.Detail)
	case store.ReportReadyEventKind:
		return s.fileReport(ctx, m.Task, r.Detail)
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
// ipc's terms, so it carries a revision. A service with no publisher, one a
// test built by hand, publishes nothing, as publishHoldState does.
func (s *Service) publishTaskEvent(event store.TaskEvent) {
	if s.events == nil {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := s.events.Publish(ipc.Event{Type: ipc.TypeTaskEvent, Revision: s.revision.Add(1), Payload: payload}); err != nil {
		s.log.Printf("could not publish event %d of task %s: %v", event.Sequence, event.TaskID, err)
	}
}

// listWakes drains the queue for the coordinator that asks, on
// store.DrainWakes' terms and wakewait.go's: oldest first, bounded, handing
// out the rows on offer to the caller's acknowledger under the wake
// stream's open stretch, and closing none. The route is the caller's
// placement, subordinates.go's wakeRouteOf: a subordinate is handed its
// domain's actable rows and the coordinator every other row. A worker is
// refused before anything is read or written.
func (s *Service) listWakes(ctx context.Context, req ipc.Request, r machine.WakesRequest) (machine.Wakes, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.Wakes{}, err
	}
	limit, err := wakesLimit(r)
	if err != nil {
		return machine.Wakes{}, err
	}
	return s.drainBoth(ctx, limit, s.wakeHandout(req, p))
}

// peekWakes is a person's reading of the queue: every queued row, handed
// out or not, oldest first, beside the project of the task it names, read
// through store.QueuedWakesFor on the caller's route, which hands out and
// acknowledges nothing, so a subordinate coordinator lists its domain's
// rows alone and everyone else the coordinator's. A worker is refused
// before anything is read, on listWakes' terms. A row whose task cannot be
// read fails the reading rather than being shown without its project.
func (s *Service) peekWakes(ctx context.Context, req ipc.Request, r machine.WakesRequest) (machine.QueuedWakes, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.QueuedWakes{}, err
	}
	limit, err := wakesLimit(r)
	if err != nil {
		return machine.QueuedWakes{}, err
	}
	queued, err := s.store.QueuedWakesFor(ctx, limit, wakeRouteOf(p))
	if err != nil {
		return machine.QueuedWakes{}, err
	}
	var domainWakes []store.DomainWake
	if !p.isSubordinate {
		if domainWakes, err = s.store.QueuedDomainWakes(ctx, limit); err != nil {
			return machine.QueuedWakes{}, err
		}
	}
	projects := make(map[string]string)
	out := machine.QueuedWakes{Wakes: make([]machine.QueuedWake, 0, len(queued))}
	for _, w := range queued {
		project, ok := projects[w.TaskID]
		if !ok {
			task, err := s.store.Task(ctx, w.TaskID)
			if err != nil {
				return machine.QueuedWakes{}, err
			}
			project = task.Project
			projects[w.TaskID] = project
		}
		out.Wakes = append(out.Wakes, machine.QueuedWake{Wake: w, Project: project})
	}
	out.DomainWakes = domainWakes
	return out, nil
}

// wakesLimit is a request's bound with zero read as machine.DefaultWakesLimit.
// A negative limit is ipc.ErrInvalidRequest, which the command line refuses
// before it asks.
func wakesLimit(r machine.WakesRequest) (int, error) {
	switch {
	case r.Limit < 0:
		return 0, fmt.Errorf("%w: a limit of %d wakes is never negative", ipc.ErrInvalidRequest, r.Limit)
	case r.Limit == 0:
		return machine.DefaultWakesLimit, nil
	}
	return r.Limit, nil
}

// acknowledgeWakes records that the coordinator acted on the named wakes,
// which is what closes them: rows a drain handed it, or rows it learned of
// from the wake stream or a task's history, on the same terms. Who
// acknowledged is the caller's acknowledger, subordinates.go's
// acknowledgerOf: derived from the surface and the placement and read from
// nothing the caller sent. A worker is refused; a subordinate naming a row
// whose task is outside its domain, read from the task's row, is refused
// with ipc.ErrOutsideDomain and nothing is closed; a batch the store
// refuses, for an unknown or an already acknowledged identifier, writes
// nothing and the refusal is the store's.
func (s *Service) acknowledgeWakes(ctx context.Context, req ipc.Request, r machine.AcknowledgeRequest) (machine.Acknowledged, error) {
	p, err := s.admitted(req)
	if err != nil {
		return machine.Acknowledged{}, err
	}
	if len(r.Wakes)+len(r.DomainWakes) == 0 {
		return machine.Acknowledged{}, fmt.Errorf("%w: an acknowledgement names no wakes", ipc.ErrInvalidRequest)
	}
	if p.isSubordinate && len(r.DomainWakes) > 0 {
		// Every wake about a domain is about a subordinate, and the first
		// coordinator alone acts on one.
		return machine.Acknowledged{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and a wake about a domain is the first coordinator's to close",
			ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain)
	}
	if p.isSubordinate {
		// A subordinate closes the rows it could have been handed and no
		// other: its domain's, read off each wake's task row, and of a
		// kind it can act on; a decision-class row about its domain's task
		// is the first coordinator's to close, and refused as a command the
		// subordinate does not hold.
		scopes, err := s.store.WakeScopes(ctx, r.Wakes)
		if err != nil {
			var refused *store.WakeIDError
			if errors.As(err, &refused) {
				return machine.Acknowledged{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
			}
			return machine.Acknowledged{}, err
		}
		actable := wakeRouteOf(p).Actable
		for _, id := range r.Wakes {
			scope := scopes[id]
			if scope.Domain != p.subordinate.Domain {
				return machine.Acknowledged{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and wake %d is about a task %s",
					ipc.ErrOutsideDomain, peerOf(req), p.subordinate.Domain, id, domainWords(scope.Domain))
			}
			if !slices.Contains(actable, scope.Kind) {
				return machine.Acknowledged{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and wake %d is of a kind the first coordinator acts on",
					ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain, id)
			}
		}
	}
	by := s.acknowledgerOf(req, p)
	refusal := func(err error) error {
		var refused *store.WakeIDError
		if errors.As(err, &refused) {
			return fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
		}
		return err
	}
	// The two shapes are one batch, all or nothing: store.AcknowledgeAll
	// checks every row of both before it writes any, in one transaction,
	// so a refused domain row leaves every task row named open too.
	if err := s.store.AcknowledgeAll(ctx, r.Wakes, r.DomainWakes, by); err != nil {
		return machine.Acknowledged{}, refusal(err)
	}
	return machine.Acknowledged{Wakes: r.Wakes, DomainWakes: r.DomainWakes, By: by.String()}, nil
}

// assertSupervision is PRD section 5's arm verification over this home,
// answered by feed.VerifyArm from the beacon, the instance file and the lock
// under the configured beacon interval. It reads what a command with the
// service down would read, so a loop that is not writing is reported as
// such from inside the service that should be running it. A worker is
// refused. A context that ends before the verdict settles is answered as
// the failed arm VerifyArm gives with the context's error.
func (s *Service) assertSupervision(ctx context.Context, req ipc.Request) (supervisor.Arm, error) {
	if _, err := s.admitted(req); err != nil {
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
// resolved to, pushed by a process placed as that task's worker, takes its
// intent from the task, as taskIntent composes it: the task record's project
// as the first line, and as the body the delivery intent its promotion
// recorded or, for a task with none, its instructions as the coordinator
// wrote them at intake. It is recorded under intentSourceTask, never
// intentSourceSupplied, and the run starts with pipeline.Start.IntentSupplied
// set, so every stage holds the change to it. The source stays task, so the
// record still says the text was read off the task. A person's push of the
// same branch carries none of it.
//
// Everything else carries no intent, as every push did before: a person's
// push, and a worker's update to anything but its own task's branch, which
// admission refuses before this runs. A push whose pusher could not be
// placed, and a worker's push placement.go cannot follow back to the
// worker, never reach this: admission refused them. A task record that
// cannot be read is logged and the run carries no intent, and a push
// already admitted cannot be refused from here.
//
// The text passes through internal/redact before it is recorded, because it
// reaches the run record and, through the pull request stage, a code host.
//
// Residual gaps: for a task with no recorded delivery intent, the
// instructions file is a file under the home that the worker's own user can
// write, so what it holds when the push arrives is as trustworthy as that
// directory is against the worker. A promoted task's intent is read from the
// task record instead, which no surface a worker is admitted to writes; the
// database is still a file under the same home, so that is not a boundary
// against a worker that writes the home's files directly. The text is the
// acceptance criteria of the run the push starts. A task's intent names what
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

// taskIntent composes a task's intent: its project, then a body. A promoted
// task's body is the delivery intent its promotion recorded on the task row,
// store.Task.DeliveryIntent, and the instructions file is not read: what the
// delivery is to do is that record, not the investigation the coordinator
// wrote at intake. Every other task's body is its instructions as
// intakeInstructions reads them, which is also what a task promoted before
// the store recorded a delivery intent gets, since it has none to read.
// Instructions that cannot be read leave the project alone, which is logged;
// intake refuses an empty project, so the text is never empty for a task
// intake recorded.
func (s *Service) taskIntent(task store.Task) string {
	project := strings.TrimSpace(task.Project)
	if delivery, ok := task.DeliveryIntent.Get(); ok {
		return redact.New().Redact(strings.TrimSpace(project + "\n\n" + strings.TrimSpace(delivery)))
	}
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
// A promoted task's delivery intent is in the promotion section and is not
// read here: taskIntent reads it off the task record and does not call this
// for such a task. The one promoted task that reaches this is one promoted
// before the store recorded a delivery intent, whose run carries its project
// and its investigation instructions. And text the coordinator wrote that
// happens to begin a line with one of those headings ends its own
// instructions there, which can only shorten the intent and never add to it.
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
