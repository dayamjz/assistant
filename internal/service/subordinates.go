package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Subordinate coordinators over a named domain: PRD section 2's subordinate
// coordinator and domain, section 9's two delegation methods and the grant
// a subordinate holds, section 8's delegation, delegation attempt and
// escalation records, and the placement that decides all of it. This file
// is the delegation half; workers.go's placement is what every grant below
// turns on.
//
// # What a subordinate is
//
// A host session this service starts for one domain, the way launch.go
// starts a worker: through the driver's session provider, as
// session.Host.Delegate, under agents.WorkerEnvironment, in the domain's
// own directory under the home (home.Domain) rather than an isolated copy,
// because a subordinate coordinates and edits nothing. Its instructions
// are the coordinator's text from the request, written to home's
// DomainInstructions once at delegation and read at every start, followed
// by subordinateSection, which tells it what it is placed as and which of
// the coordinator client's subcommands it holds. Its route is
// Options.CoordinatorRoute, the coordinator client, so the grant
// internal/agents writes allows exactly the commands this service then
// decides on placement; a subcommand it does not hold is refused here,
// never by the grant. The session's reference is recorded on the
// delegation attempt, store.SetDelegationAttemptSession, and the session
// is held per domain in delegated, which Close ends with the workers.
//
// # The grant is decided per method from placement
//
// placeRequest places a request's peer as a task's worker, a domain's
// subordinate, or neither, from the kernel's credentials and the parent
// chain, on workers.go's terms; nothing a request carries reaches it.
// Within its domain a subordinate is admitted to task.create (its domain
// written from the placement, its posture taken from the delegation),
// task.message, task.stop, task.peek, task.relaunch, task.relay, task.get,
// tasks.list, wakes.list, wakes.peek, wakes.wait and wakes.acknowledge for
// its domain's rows, and supervision.assert; scopeTask is the one check
// that a named task is inside its domain, read off the task's own row, and
// its refusal is ipc.ErrOutsideDomain. It is refused with
// ipc.ErrSubordinate, by refuseSubordinate, every method section 9 lists:
// task.promote, task.respond, run.respond, catchup.mark, run.start,
// run.rerun, run.cancel, branch.sync, service.stop, service.restart,
// domain.delegate and domain.end, and task.interrupt beside them; gate.admit
// refuses its push too, because a subordinate has no branch to move.
// Where placement cannot tell, each rule errs toward the side that claims
// less: a chain that cannot be read refuses, as it does for a worker.
//
// # An escalation is a refused decision, recorded
//
// PRD section 8's escalation is "a decision a subordinate hands up because
// answering it is one of the commands it does not hold", and that sentence
// is the mechanism: when a subordinate asks task.promote or task.respond of
// a task inside its domain, the request is refused with ipc.ErrSubordinate
// as section 9 says, and the ask is recorded by store.RecordEscalation,
// naming the task and carrying the method with the request's own words,
// redacted. The supervisor loop raises each record once as
// supervisor.KindSubordinateEscalated, a decision-class kind that the
// routing hands the first coordinator and never the subordinate, and the
// record travels in the task's answer as machine.Task.Escalations. A
// refusal of a task outside its domain records nothing, because that
// decision was never the subordinate's to hand up, and a refusal of a
// service or run verb records nothing either, since those are about no
// task of the domain.
//
// # Wake routing
//
// Which coordinator a wake reaches is its kind's row in internal/supervisor's
// table, supervisor.SubordinateActable, passed to internal/store as words in
// store.WakeRoute, and the task's domain read in the drain's own transaction
// (P14): the subordinate's drains, waits and listings are made on the route
// of its domain and the coordinator's on the zero route, so a queued row is
// on offer to exactly one of them while the delegation stands and to the
// coordinator once it has ended. wakeRouteOf composes the route from the
// placement and nothing else.
//
// # A restart
//
// A restart holds no session, so reconcileSubordinates re-probes every
// standing delegation's latest attempt through the service's Terminals on
// reconcile.go's terms: Dead is relaunched as a new delegation attempt
// anchored to the attempt history read, bounded by relaunch_attempts
// counted from the attempts, Unobserved is left alone, and past the bound
// the delegation is left standing and the log says so. Ownership never
// transfers: the delegation stands until domain.end writes its one
// revision, and its tasks stay in its domain whatever became of its
// session.
//
// # Residual gaps
//
//   - The queue's row names a task, and a subordinate is not one, so a
//     subordinate's terminal gone has no wake row to be raised on: the kind
//     is in the table with no producer, and a dead subordinate reaches the
//     coordinator only through the delegation's attempts and this service's
//     log until the follow-up this gap names decides how a wake about a
//     domain is carried. For the same reason the relaunch bound holds no
//     hold for a person yet: past it the delegation stands and nothing
//     tells the coordinator but the log.
//   - wakes.subscribe is one publisher per method in internal/ipc, and a
//     subscriber is pushed every row the loop appends and counts toward the
//     one attachment the coordinator's handouts are held under, so a
//     subordinate holding it would be pushed the coordinator's rows and
//     pin them. This slice refuses a subordinate the stream with
//     ipc.ErrSubordinate and the subordinate waits with wakes.wait, which
//     is routed; a per-domain publisher is the follow-up.
//   - The placement's gaps are workers.go's: a chain the kernel rewrote
//     when a process between a subordinate and its tool command exited
//     places that command as a person's, which the PRD's decision 20 names.
//   - The subordinate's posture is held to intake's rule at each task it
//     creates, against that task's project, since a delegation names no
//     repository; a delegation whose posture is lighter than a project's
//     refuses every task of that project rather than being refused at
//     delegation.
//   - Nothing bounds the number of domains, as decision 20 states.

// errNotDelegated reports a domain.end for a domain with no standing
// delegation. Nothing is recorded.
var errNotDelegated = errors.New("service: the domain has no standing delegation")

// placed is what placeRequest derived about a request's peer: a task's
// worker, a domain's subordinate coordinator, or neither, which is the
// coordinator's own client or a person's. At most one of the two is set.
type placedPeer struct {
	worker      session.Membership
	isWorker    bool
	subordinate session.Placement
	// isSubordinate is whether the peer is placed as a domain's subordinate
	// coordinator; the domain is subordinate.Domain.
	isSubordinate bool
}

// domain is the domain the peer is the subordinate of, and empty for every
// other peer.
func (p placedPeer) domain() string {
	if p.isSubordinate {
		return p.subordinate.Domain
	}
	return ""
}

// placeRequest places a request's peer on the file comment's terms, through
// placement.go's one walk, placeOf: the worker sets are asked of each
// process first and the subordinate sets second, so a peer in both, which
// no launch here produces, is the worker, the side that claims less, and a
// shell at a terminal and nobody are both neither here, as workerOf reads
// them. No chain is read while no set holds anybody, on workerOf's terms.
// An unidentified peer is ipc.ErrUnidentifiedPeer and an ancestry that
// could not be read is ipc.ErrUnavailable, both wrapped, so neither reads
// as the coordinator.
func (s *Service) placeRequest(req ipc.Request) (placedPeer, error) {
	creds, err := req.Peer.Credentials()
	if err != nil {
		return placedPeer{}, err
	}
	if s.workerSets().empty() && s.subordinateSets().empty() {
		return placedPeer{}, nil
	}
	p, err := s.placeOf(creds)
	if err != nil {
		return placedPeer{}, fmt.Errorf("%w: whether %s is a task's worker or a domain's subordinate coordinator could not be determined: %w",
			ipc.ErrUnavailable, creds, err)
	}
	switch p.kind {
	case placedWorker:
		return placedPeer{worker: p.worker, isWorker: true}, nil
	case placedSubordinate:
		return placedPeer{subordinate: p.subordinate, isSubordinate: true}, nil
	}
	return placedPeer{}, nil
}

// subordinateSets gathers every subordinate set this service holds: the
// driver's provider's and each delegated host's, on workerSets' terms.
func (s *Service) subordinateSets() subordinateSets {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sets subordinateSets
	if s.built != nil && s.built.sessions != nil {
		sets = append(sets, s.built.sessions.Subordinates())
	}
	for _, launched := range s.delegated {
		if launched.host != nil {
			sets = append(sets, launched.host.Subordinates())
		}
	}
	return sets
}

// empty reports whether no set holds a subordinate at all.
func (sets subordinateSets) empty() bool {
	for _, set := range sets {
		if set != nil && !set.Empty() {
			return false
		}
	}
	return true
}

// subordinateSets is the placer over every subordinate set this service
// holds, on workerSets' terms.
type subordinateSets []*session.Subordinates

// Member is session.Subordinates.Member over every set.
func (sets subordinateSets) Member(group int) (session.Placement, bool) {
	for _, set := range sets {
		if set == nil {
			continue
		}
		if p, ok := set.Member(group); ok {
			return p, true
		}
	}
	return session.Placement{}, false
}

// Hosted is session.Subordinates.Hosted over every set.
func (sets subordinateSets) Hosted(host agents.Identity) (session.Placement, bool) {
	for _, set := range sets {
		if set == nil {
			continue
		}
		if p, ok := set.Hosted(host); ok {
			return p, true
		}
	}
	return session.Placement{}, false
}

// refuseSubordinate refuses a request from a peer placed as a task's worker
// with ipc.ErrWorker and one placed as a domain's subordinate coordinator
// with ipc.ErrSubordinate: the check every coordinator-only method makes,
// for the methods PRD section 9 refuses a subordinate. It returns the
// placement for a caller that goes on to use it.
func (s *Service) refuseSubordinate(req ipc.Request) (placedPeer, error) {
	p, err := s.placeRequest(req)
	if err != nil {
		return placedPeer{}, err
	}
	if p.isWorker {
		return placedPeer{}, fmt.Errorf("%w: %s is the worker of task %s, and %s is the coordinator's", ipc.ErrWorker,
			peerOf(req), p.worker.Task, req.Method)
	}
	if p.isSubordinate {
		return placedPeer{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and %s is one it does not hold",
			ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain, req.Method)
	}
	return p, nil
}

// admitted refuses a worker and admits the coordinator and a subordinate,
// for the methods a subordinate holds over its domain; the placement says
// which the caller is.
func (s *Service) admitted(req ipc.Request) (placedPeer, error) {
	p, err := s.placeRequest(req)
	if err != nil {
		return placedPeer{}, err
	}
	if p.isWorker {
		return placedPeer{}, fmt.Errorf("%w: %s is the worker of task %s, and %s is the coordinator's", ipc.ErrWorker,
			peerOf(req), p.worker.Task, req.Method)
	}
	return p, nil
}

// scopeTask holds a task-naming request to the caller's domain: a
// subordinate naming a task whose row is outside its domain is refused
// with ipc.ErrOutsideDomain, and the coordinator is held to nothing. The
// task is read from its row, never from the request, and a task that
// cannot be read is the read's failure.
func (s *Service) scopeTask(ctx context.Context, req ipc.Request, p placedPeer, taskID string) error {
	if !p.isSubordinate {
		return nil
	}
	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Domain != p.subordinate.Domain {
		return fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and task %s is %s",
			ipc.ErrOutsideDomain, peerOf(req), p.subordinate.Domain, taskID, domainWords(task.Domain))
	}
	return nil
}

// domainWords says which domain a task is in, for a refusal.
func domainWords(domain string) string {
	if domain == "" {
		return "in no domain"
	}
	return "in domain " + domain
}

// acknowledgerOf is who a drain hands rows to and who an acknowledgement
// records, from the placement: the subordinate's member for a subordinate,
// which internal/store says this service is the one producer of, and
// otherwise what internal/ipc derives from the surface.
func (s *Service) acknowledgerOf(req ipc.Request, p placedPeer) store.Acknowledger {
	if p.isSubordinate {
		return store.AcknowledgedBySubordinate(p.subordinate.Domain)
	}
	return req.WakeAcknowledger()
}

// senderOf is who a message records as its sender, on acknowledgerOf's
// terms.
func (s *Service) senderOf(req ipc.Request, p placedPeer) store.Sender {
	if p.isSubordinate {
		return store.SentBySubordinate(p.subordinate.Domain)
	}
	return req.MessageSender()
}

// wakeRouteOf is the route a drain, a wait or a listing is made on, from
// the placement: the subordinate's domain with the kinds it can act on, or
// the zero route for the coordinator, with the same kinds so the two
// partition the queue.
func wakeRouteOf(p placedPeer) store.WakeRoute {
	actable := supervisor.SubordinateActable()
	words := make([]string, len(actable))
	for i, kind := range actable {
		words[i] = string(kind)
	}
	return store.WakeRoute{Subordinate: p.domain(), Actable: words}
}

// delegateDomain is domain.delegate, on the file comment's terms: the
// coordinator's, refused for a worker and a subordinate; the posture is
// decoded and refused empty; the delegation is recorded anchored to the
// domain's latest delegation; the attempt is claimed; the instructions are
// written under the domain's directory; and the subordinate is started in
// the background on dispatchInBackground's terms, so the answer comes once
// the records are written. A domain already delegated and a record the
// store refuses cross the wire as ipc.ErrRefused.
func (s *Service) delegateDomain(ctx context.Context, req ipc.Request, r machine.DomainDelegateRequest) (machine.Delegated, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
		return machine.Delegated{}, err
	}
	if strings.TrimSpace(r.Instructions) == "" {
		return machine.Delegated{}, fmt.Errorf("%w: a delegation names no instructions for its subordinate coordinator", ipc.ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Delivery) == "" {
		return machine.Delegated{}, fmt.Errorf("%w: a delegation names the delivery posture its tasks take, since a subordinate coordinator records none", ipc.ErrInvalidRequest)
	}
	posture, err := config.ParseDelivery(strings.TrimSpace(r.Delivery))
	if err != nil {
		return machine.Delegated{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	dir, err := s.home.Domain(r.Domain)
	if err != nil {
		return machine.Delegated{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	latest, found, err := s.store.Delegation(ctx, r.Domain)
	if err != nil {
		return machine.Delegated{}, err
	}
	var expected int64
	if found {
		expected = latest.ID
	}
	d, err := s.store.Delegate(ctx, store.DelegationRequest{Domain: r.Domain, Delivery: posture, Expected: expected})
	if err != nil {
		if errors.Is(err, store.ErrDomainDelegated) || errors.Is(err, store.ErrDelegationMoved) {
			return machine.Delegated{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		return machine.Delegated{}, err
	}
	attempt, err := s.store.BeginDelegationAttempt(ctx, d.ID, 0)
	if err != nil {
		return machine.Delegated{}, fmt.Errorf("service: claiming the first attempt of delegation %d: %w", d.ID, err)
	}
	instructions, err := s.writeDomainInstructions(dir, r.Domain, r.Instructions)
	if err != nil {
		return machine.Delegated{}, err
	}
	s.log.Printf("domain %s was delegated by %s as delegation %d under the %s posture", r.Domain, peerOf(req), d.ID, posture)
	s.launchSubordinateInBackground(d, attempt.Sequence)
	return machine.Delegated{Record: d, Attempt: attempt.Sequence, Instructions: instructions}, nil
}

// writeDomainInstructions writes the coordinator's text for a domain's
// subordinate under the domain's directory and returns the file's path.
func (s *Service) writeDomainInstructions(dir, domain, text string) (string, error) {
	path, err := s.home.DomainInstructions(domain)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("service: creating the directory of domain %s: %w", domain, err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("service: writing the instructions of domain %s: %w", domain, err)
	}
	return path, nil
}

// endDelegation is domain.end: the coordinator's, refused for a worker and
// a subordinate; the domain's standing delegation is ended, its one
// revision, and the subordinate's session this service holds is ended with
// it. A domain with no standing delegation is ipc.ErrRefused.
func (s *Service) endDelegation(ctx context.Context, req ipc.Request, r machine.DomainEndRequest) (machine.DelegationEnded, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
		return machine.DelegationEnded{}, err
	}
	latest, found, err := s.store.Delegation(ctx, r.Domain)
	if err != nil {
		return machine.DelegationEnded{}, err
	}
	if !found || !latest.Standing() {
		return machine.DelegationEnded{}, fmt.Errorf("%w: %w: domain %q", ipc.ErrRefused, errNotDelegated, r.Domain)
	}
	ended, err := s.store.EndDelegation(ctx, latest.ID, store.EndedByCoordinator())
	if err != nil {
		if errors.Is(err, store.ErrDelegationEnded) {
			return machine.DelegationEnded{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		return machine.DelegationEnded{}, err
	}
	sessionEnded := s.endSubordinate(r.Domain)
	s.log.Printf("the delegation of domain %s (delegation %d) was ended by %s; its subordinate's session held here: %v",
		r.Domain, ended.ID, peerOf(req), sessionEnded)
	return machine.DelegationEnded{Record: ended, SessionEnded: sessionEnded}, nil
}

// endSubordinate ends and forgets the subordinate session this service
// holds for a domain, reporting whether it held one. A Close that fails is
// logged; the session is forgotten either way.
func (s *Service) endSubordinate(domain string) bool {
	s.mu.Lock()
	held, ok := s.delegated[domain]
	if ok {
		delete(s.delegated, domain)
	}
	s.mu.Unlock()
	if !ok || held.host == nil || held.session == nil {
		return false
	}
	if err := held.host.Close(held.session); err != nil {
		s.log.Printf("the subordinate coordinator of domain %s was not ended cleanly: %v", domain, err)
	}
	return true
}

// launchSubordinateInBackground starts a domain's subordinate on a goroutine
// registered through startWork, so the answer that recorded the delegation
// does not wait on the process and Close still waits for the start.
func (s *Service) launchSubordinateInBackground(d store.Delegation, attempt int64) {
	if !s.startWork() {
		s.log.Printf("the subordinate coordinator of domain %s was not started because this service is giving up the home; "+
			"the next start finds the attempt with no session and relaunches", d.Domain)
		return
	}
	go func() {
		defer s.work.Done()
		if err := s.launchSubordinate(s.stopCtx, d, attempt); err != nil {
			s.log.Printf("the subordinate coordinator of domain %s (delegation %d, attempt %d) was not started: %v",
				d.Domain, d.ID, attempt, err)
		}
	}()
}

// launchSubordinate starts a domain's subordinate for a claimed attempt, on
// the file comment's terms: it reads the instructions file, builds the
// driver for the provider, starts the session through session.Host.Delegate,
// records the reference on the attempt, and holds the session. A reference
// that cannot be recorded ends the session before the failure is reported,
// as launch.go does for a worker.
func (s *Service) launchSubordinate(ctx context.Context, d store.Delegation, attempt int64) error {
	path, err := s.home.DomainInstructions(d.Domain)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("service: reading the instructions of domain %s: %w", d.Domain, err)
	}
	dir := filepath.Dir(path)
	built, err := s.driverFor(ctx)
	if err != nil {
		return err
	}
	launched, err := built.sessions.Delegate(ctx, session.SubordinateSpec{
		Domain:       d.Domain,
		Dir:          dir,
		Instructions: string(raw) + s.subordinateSection(d),
		Route:        s.coordinatorRoute,
	})
	if err != nil {
		return err
	}
	reference := launched.Reference().String()
	if err := s.store.SetDelegationAttemptSession(ctx, d.ID, attempt, reference); err != nil {
		if closeErr := built.sessions.Close(launched); closeErr != nil {
			s.log.Printf("the subordinate coordinator of domain %s, whose session could not be recorded, was not ended either: %v",
				d.Domain, closeErr)
		}
		return fmt.Errorf("service: recording the session of delegation %d attempt %d: %w", d.ID, attempt, err)
	}
	s.mu.Lock()
	if s.delegated == nil {
		s.delegated = make(map[string]launchedWorker)
	}
	s.delegated[d.Domain] = launchedWorker{host: built.sessions, session: launched}
	s.mu.Unlock()
	s.log.Printf("the subordinate coordinator of domain %s was started as delegation %d attempt %d in terminal session %s",
		d.Domain, d.ID, attempt, reference)
	return nil
}

// subordinateHeading is the heading subordinateSection writes under.
const subordinateHeading = "## Your place in this home, written by the program at delegation"

// subordinateSection is what a start appends to a subordinate's
// instructions: which domain it coordinates, the posture its tasks take,
// the route it reaches the program through, and which of the coordinator
// client's subcommands it holds and which it is refused, as the service
// decides them. It is empty of a route when the service was given none,
// which a test that opens the service without internal/cli gets.
func (s *Service) subordinateSection(d store.Delegation) string {
	var b strings.Builder
	b.WriteString("\n\n" + subordinateHeading + "\n\n")
	fmt.Fprintf(&b, "You are the subordinate coordinator of the domain %q in this home. The program places you by the "+
		"process you run in and nothing you say about yourself, so every task you create is written into your domain, "+
		"and you reach only the tasks already in it. A task you create takes the %s delivery posture, which the "+
		"delegation fixed; do not ask for another. You report to the coordinator that delegated the domain and never "+
		"to the person.\n", d.Domain, d.Delivery)
	if s.coordinatorRoute == "" {
		b.WriteString("\nThis service was given no route, so no command reaches it from here.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\nYou drive your domain's work with one command, which your permissions allow when it begins exactly "+
		"with the text below:\n\n    %s SUBCOMMAND ...\n\n", s.coordinatorRoute)
	b.WriteString("Within your domain you hold: create, message, stop, peek, relaunch, relayed, wakes, await, " +
		"acknowledge and supervision, and the tasks listing shows your domain's tasks alone. You are refused: " +
		"promote, delegate and end-delegation, answering a hold, marking the catch-up read, starting, rerunning or " +
		"cancelling a run, synchronizing a branch, stopping or restarting the service, and listen. A promotion or a " +
		"hold's answer you ask for is refused and recorded as a decision handed up to the coordinator, with your " +
		"words, so ask for one when that is what you want.\n")
	return b.String()
}

// refuseDecision is refuseSubordinate for the two task decisions a
// subordinate hands up, task.promote and task.respond: a subordinate asking
// one of a task inside its domain is refused with ipc.ErrSubordinate and
// the ask is recorded as an escalation carrying text, the method with the
// request's own words; one asking of a task outside its domain, or of a
// task that cannot be read, is refused the same way and nothing is
// recorded, on the file comment's terms.
func (s *Service) refuseDecision(ctx context.Context, req ipc.Request, taskID, text string) (placedPeer, error) {
	p, err := s.placeRequest(req)
	if err != nil {
		return placedPeer{}, err
	}
	if p.isWorker {
		return placedPeer{}, fmt.Errorf("%w: %s is the worker of task %s, and %s is the coordinator's", ipc.ErrWorker,
			peerOf(req), p.worker.Task, req.Method)
	}
	if !p.isSubordinate {
		return p, nil
	}
	if task, err := s.store.Task(ctx, taskID); err == nil && task.Domain == p.subordinate.Domain {
		s.escalate(ctx, p, taskID, text)
	}
	return placedPeer{}, fmt.Errorf("%w: %s is the subordinate coordinator of domain %s, and %s is a decision it hands up rather than makes",
		ipc.ErrSubordinate, peerOf(req), p.subordinate.Domain, req.Method)
}

// escalate records a subordinate's refused decision about a task in its
// domain as PRD section 8's escalation, on the file comment's terms, and
// logs a record that could not be written rather than failing the refusal
// it accompanies.
func (s *Service) escalate(ctx context.Context, p placedPeer, task, text string) {
	if !p.isSubordinate {
		return
	}
	e, err := s.store.RecordEscalation(ctx, task, text)
	if err != nil {
		s.log.Printf("the subordinate coordinator of domain %s handed a decision about task %s up and it could not be recorded: %v",
			p.subordinate.Domain, task, err)
		return
	}
	s.log.Printf("the subordinate coordinator of domain %s handed a decision about task %s up as escalation %d",
		p.subordinate.Domain, task, e.ID)
}

// subordinateSessionOf reports the session this service holds for a
// domain's subordinate and the host holding it.
func (s *Service) subordinateSessionOf(domain string) (session.Host, *session.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.delegated[domain]
	if !ok || held.host == nil || held.session == nil {
		return nil, nil, false
	}
	return held.host, held.session, true
}

// closeSubordinates ends every subordinate session this service holds, on
// closeSessions' terms.
func (s *Service) closeSubordinates() []error {
	s.mu.Lock()
	held := s.delegated
	s.delegated = make(map[string]launchedWorker)
	s.mu.Unlock()
	var errs []error
	for domain, launched := range held {
		if launched.host == nil {
			errs = append(errs, fmt.Errorf("service: domain %s holds a session and no provider", domain))
			continue
		}
		if err := launched.host.Close(launched.session); err != nil {
			errs = append(errs, fmt.Errorf("service: ending the subordinate coordinator of domain %s: %w", domain, err))
		}
	}
	return errs
}

// reconcileSubordinates is the restart half, on the file comment's terms:
// for every standing delegation whose latest attempt names a session, the
// session is probed through the service's Terminals under the
// reconciliation bound, and a Dead reading relaunches it as the next
// attempt, bounded by relaunch_attempts counted as the attempts past the
// first; Unobserved and Live are left alone, and an attempt that never
// recorded a session is relaunched as a start that did not come up. What
// is refused or left is logged.
func (s *Service) reconcileSubordinates(ctx context.Context) {
	standing, err := s.store.StandingDelegations(ctx)
	if err != nil {
		s.log.Printf("reconciliation: the standing delegations could not be read: %v", err)
		return
	}
	if len(standing) == 0 {
		return
	}
	bound, cancel := context.WithTimeout(ctx, reconcileProbeBound)
	defer cancel()
	probe := s.boundedProbe(bound)
	for _, d := range standing {
		attempts, err := s.store.DelegationAttempts(ctx, d.ID)
		if err != nil {
			s.log.Printf("reconciliation: the attempts of delegation %d (domain %s) could not be read: %v", d.ID, d.Domain, err)
			continue
		}
		var latest int64
		reason := "its subordinate coordinator was never started"
		if n := len(attempts); n > 0 {
			latest = attempts[n-1].Sequence
			if ref, ok := attempts[n-1].SessionRef.Get(); ok {
				if probe == nil {
					s.log.Printf("reconciliation: domain %s is left alone, since this service has no terminal seam to probe %s with", d.Domain, ref)
					continue
				}
				liveness := probe(ctx, ref)
				if liveness != taskstate.Dead {
					s.log.Printf("reconciliation: the subordinate coordinator of domain %s (session %s) reads %s and is left alone",
						d.Domain, ref, liveness)
					continue
				}
				reason = fmt.Sprintf("its subordinate coordinator's terminal %s is gone", ref)
			} else {
				reason = fmt.Sprintf("attempt %d never recorded a session", latest)
			}
		}
		if relaunches := max(len(attempts)-1, 0); relaunches >= s.cfg.RelaunchAttempts {
			s.log.Printf("reconciliation: the subordinate coordinator of domain %s is not started again: %s, it has been "+
				"relaunched %d times and relaunch_attempts is %d; the delegation stands until the coordinator ends it",
				d.Domain, reason, relaunches, s.cfg.RelaunchAttempts)
			continue
		}
		attempt, err := s.store.BeginDelegationAttempt(ctx, d.ID, latest)
		if err != nil {
			s.log.Printf("reconciliation: the subordinate coordinator of domain %s was not relaunched: %v", d.Domain, err)
			continue
		}
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s is relaunched as attempt %d: %s",
			d.Domain, attempt.Sequence, reason)
		s.launchSubordinateInBackground(d, attempt.Sequence)
	}
}
