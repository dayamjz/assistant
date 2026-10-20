package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
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
// Options.CoordinatorRoute, the coordinator client, and it starts under
// agents.GrantSubordinate, which session.Host.Delegate selects through
// agents.OpenSubordinate: editing and git denied and the route allowed, so
// what it reaches the program with is the commands this service then
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
// tasks.list, wakes.list, wakes.peek, wakes.wait, wakes.subscribe and
// wakes.acknowledge for its domain's rows, and supervision.assert; scopeTask is the one check
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
// counted from the attempts, Unobserved and Live are left alone, and past
// the bound the delegation holds for a person on holdDomainBound's hold,
// keyed by the delegation and the attempt that reached the bound, which
// the supervisor loop raises the same way while the service runs
// (delegations.go), so a delegation never stands silently past the bound.
// A subordinate left alone is one that may have outlived the service: its
// attempt's reference is adopted into session.Survivors, the placer that
// answers from the record and the kernel's presence read, so a request from
// its process is placed as its domain's subordinate and never as the first
// coordinator by the held set being empty, and domain.end ends it by that
// identity through agents.EndGroup. placeRequest reads no chain only while
// no worker, no subordinate and no survivor is held, which is when there is
// nobody a peer could be mistaken for. The hold is answered through task.respond given
// the domain, respondDomain: a retry begins the subordinate once more as a
// new attempt anchored to the one the hold names, which spends the answer
// (P6), and a stop ends the delegation with store.EndedByAnswer. Ownership
// never transfers: the delegation stands until domain.end or that answer
// writes its one revision, and its tasks stay in its domain whatever became
// of its session. A subordinate's own terminal gone is the loop's wake,
// supervisor.KindSubordinateGone on the queue's second shape, which
// delegations.go owns.
//
// # Residual gaps
//
//   - The placement's gaps are workers.go's: a chain the kernel rewrote
//     when a process between a subordinate and its tool command exited
//     places that command as a person's, which the PRD's decision 20 names.
//     A survivor's placement adds session.Survivors' own: on a platform
//     reading no start time it cannot tell a reused identifier, of the
//     survivor or of its terminal server, from the survivor, and places
//     that process as the subordinate until the delegation ends.
//   - A subordinate's grant denies editing and git and allows its route;
//     any other command is what its agent's own settings allow, which
//     internal/agents names for GrantSubordinate. The Claude Code adapter
//     refuses a terminal-hosted subordinate (agents.ErrTerminalGrantUnobserved),
//     so under session_provider tmux a delegation's start fails, logged,
//     and the subordinate runs only under the pipe provider.
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
// them. No chain is read while no set holds anybody and no survivor is
// adopted, on workerOf's terms: the coordinator is then the only peer
// there is, and a chain read against empty sets could place nothing else.
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
// driver's provider's, each delegated host's, and the survivors adopted at
// recovery, on workerSets' terms. The held sets come first, so a group a
// held session leads is answered from its handle and a survivor only from
// the record and the presence read.
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
	if s.survivors != nil {
		sets = append(sets, s.survivors)
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

// subordinatePlacerSet is one set the service asks: placement.go's
// subordinatePlacer that can also say whether it holds anybody, which
// *session.Subordinates and *session.Survivors both are.
type subordinatePlacerSet interface {
	subordinatePlacer
	Empty() bool
}

// subordinateSets is the placer over every subordinate set this service
// holds, on workerSets' terms.
type subordinateSets []subordinatePlacerSet

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
//
// A relaunch-bound hold still open on the delegation is answered by the
// end, in the end's transaction: that hold asks whether to begin the
// domain's coordinator again or to end the delegation, and domain.end is
// the second answer, given explicitly by the caller, so the hold is
// resolved stop under the resolver internal/ipc derives for the request,
// on the same terms respondDomain records a stop, and never as the
// program. An end that left it open would leave a hold nothing can answer,
// because respondDomain answers only a standing delegation's hold.
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
	attempts, err := s.store.DelegationAttempts(ctx, latest.ID)
	if err != nil {
		return machine.DelegationEnded{}, err
	}
	closing := store.DelegationEndClosure{
		Resolution: machine.AnswerStop,
		Note:       "answered by domain.end, which ended the delegation",
		By:         req.HoldResolver(),
	}
	for _, a := range attempts {
		closing.Keys = append(closing.Keys, domainBoundKey(latest, a.Sequence))
	}
	ended, closed, err := s.store.EndDelegation(ctx, latest.ID, store.EndedByCoordinator(), closing)
	if err != nil {
		if errors.Is(err, store.ErrDelegationEnded) {
			return machine.DelegationEnded{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
		}
		return machine.DelegationEnded{}, err
	}
	out := machine.DelegationEnded{Record: ended}
	for _, key := range closed {
		h, err := s.store.Hold(ctx, key)
		if err != nil {
			s.log.Printf("the delegation of domain %s answered hold %s and it could not be read back: %v", r.Domain, key, err)
			continue
		}
		s.publishHoldState(h)
		out.Holds = append(out.Holds, h)
	}
	out.SessionEnded = s.endSubordinate(r.Domain, ended.ID)
	s.log.Printf("the delegation of domain %s (delegation %d) was ended by %s, answering %d hold(s); its subordinate's session held here: %v",
		r.Domain, ended.ID, peerOf(req), len(closed), out.SessionEnded)
	return out, nil
}

// launchedSubordinate is one entry of the service's delegated map: the
// session a launch started for a domain, the host holding it, and the
// delegation it was started for, which is what keeps one delegation's
// launch or end from touching another's session.
type launchedSubordinate struct {
	host       session.Host
	session    *session.Session
	delegation int64
}

// endSubordinate ends and forgets the subordinate session this service
// holds for a domain's delegation, and the survivor it adopted for the
// domain at recovery, reporting whether it ended either. A held session is
// ended only when it was started for that delegation, so an end of an
// older delegation leaves a newer one's session alone. A held session is
// ended through its host; a survivor is ended by the identity it was held
// to, through agents.EndGroup, which signals nothing until the identity is
// verified present, and is left running when the identity records no
// start time, since nothing could then verify it. A Close or an end that
// fails is logged; the session and the survivor are forgotten either way,
// so a domain ended twice ends nothing twice.
func (s *Service) endSubordinate(domain string, delegation int64) bool {
	s.mu.Lock()
	held, ok := s.delegated[domain]
	ok = ok && held.delegation == delegation
	if ok {
		delete(s.delegated, domain)
	}
	s.mu.Unlock()
	ended := false
	if ok && held.host != nil && held.session != nil {
		switch err := held.host.Close(held.session); {
		case errors.Is(err, session.ErrNotHeld):
			// Ended already through the handle the service observes it
			// by, which session.Provider.Close says a second Close is.
			s.log.Printf("the subordinate coordinator of domain %s had already ended", domain)
		case err != nil:
			s.log.Printf("the subordinate coordinator of domain %s was not ended cleanly: %v", domain, err)
		}
		ended = true
	}
	if identity, found := s.survivors.Forget(domain); found {
		if identity.Start == 0 {
			s.log.Printf("the surviving subordinate coordinator of domain %s (%s) records no start time and is left running, unverified", domain, identity)
			return ended
		}
		end, err := agents.EndGroup(identity, stopGrace)
		if err != nil {
			s.log.Printf("the surviving subordinate coordinator of domain %s (%s) was not ended: %v", domain, identity, err)
			return ended
		}
		s.log.Printf("the surviving subordinate coordinator of domain %s (%s) was ended: %s", domain, identity, end)
		ended = true
	}
	return ended
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
// the file comment's terms: it reads the delegation again and refuses one
// that has ended since the attempt was claimed, reads the instructions
// file, builds the driver for the provider, starts the session through
// session.Host.Delegate, holds the session under its delegation, and
// records the reference on the attempt with the identity of the process
// hosting its terminal, which a restart's survivor placement reads. A
// reference that cannot be recorded ends the session before the failure
// is reported, as launch.go does for a worker, and one refused because the
// delegation ended while the session started is that case:
// store.SetDelegationAttemptSession reads the delegation in the recording
// transaction. The session is held before it is recorded so that a
// domain.end landing between the two finds it to end: either the end's
// record precedes the session's, and the record is refused and the launch
// ends the session, or it follows, and the end's endSubordinate finds the
// session held; there is no order in which a session lands that nothing
// ends. The entry is the delegation's own: a launch finding the domain
// held for a later delegation holds nothing and ends its own session, a
// launch replaces only an entry of its own or an earlier delegation, and
// a launch that ends its session removes the entry only while the entry
// is that session, so a launch paused across an end and a new delegation
// can neither replace nor erase the newer delegation's handle.
func (s *Service) launchSubordinate(ctx context.Context, d store.Delegation, attempt int64) error {
	if current, err := s.store.DelegationByID(ctx, d.ID); err != nil {
		return fmt.Errorf("service: reading delegation %d before starting its subordinate coordinator: %w", d.ID, err)
	} else if !current.Standing() {
		return fmt.Errorf("%w: delegation %d of domain %s ended before its subordinate coordinator started", store.ErrDelegationEnded, d.ID, d.Domain)
	}
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
	reference, err := launched.Reference().Encode()
	if err != nil {
		if closeErr := built.sessions.Close(launched); closeErr != nil {
			s.log.Printf("the subordinate coordinator of domain %s, whose reference could not be encoded, was not ended either: %v",
				d.Domain, closeErr)
		}
		return fmt.Errorf("service: the session of delegation %d attempt %d: %w", d.ID, attempt, err)
	}
	// The entry is the launch's own delegation's: a launch never replaces
	// the session of a later delegation of the domain, which a launch
	// paused across an end and a new delegation would otherwise do and
	// then remove, leaving the newer session held by nothing.
	s.mu.Lock()
	if s.delegated == nil {
		s.delegated = make(map[string]launchedSubordinate)
	}
	newer := false
	if current, ok := s.delegated[d.Domain]; ok && current.delegation > d.ID {
		newer = true
	} else {
		s.delegated[d.Domain] = launchedSubordinate{host: built.sessions, session: launched, delegation: d.ID}
	}
	s.mu.Unlock()
	if newer {
		s.closeOwnSubordinate(d, built.sessions, launched)
		return fmt.Errorf("%w: delegation %d of domain %s was succeeded by a later delegation while its subordinate coordinator started",
			store.ErrDelegationEnded, d.ID, d.Domain)
	}
	s.survivors.Forget(d.Domain)
	host := launched.Host()
	if err := s.store.SetDelegationAttemptSession(ctx, d.ID, attempt, reference,
		store.SessionHost{PID: host.PID, Start: host.Start}); err != nil {
		s.closeOwnSubordinate(d, built.sessions, launched)
		return fmt.Errorf("service: recording the session of delegation %d attempt %d: %w", d.ID, attempt, err)
	}
	s.log.Printf("the subordinate coordinator of domain %s was started as delegation %d attempt %d in terminal session %s",
		d.Domain, d.ID, attempt, reference)
	return nil
}

// closeOwnSubordinate ends a session a launch started and could not keep,
// through its own handle, and forgets the domain's entry only when the
// entry is that session, so the session of any other launch of the domain
// is left held.
func (s *Service) closeOwnSubordinate(d store.Delegation, host session.Host, launched *session.Session) {
	s.mu.Lock()
	if current, ok := s.delegated[d.Domain]; ok && current.session == launched {
		delete(s.delegated, d.Domain)
	}
	s.mu.Unlock()
	if err := host.Close(launched); err != nil && !errors.Is(err, session.ErrNotHeld) {
		s.log.Printf("the subordinate coordinator of domain %s (delegation %d) that this launch could not keep was not ended cleanly: %v",
			d.Domain, d.ID, err)
	}
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
	b.WriteString("Within your domain you hold: create, tasks, message, stop, peek, relaunch, relayed, wakes, await, " +
		"acknowledge, listen and supervision, and tasks lists your domain's tasks alone. You are refused: promote, answer, " +
		"delegate and end-delegation, marking the catch-up read, starting, rerunning or cancelling a run, " +
		"synchronizing a branch, and stopping or restarting the service. A promotion (promote) or a hold's " +
		"answer (answer) you ask for is refused and recorded as a decision handed up to the coordinator, with your " +
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
// closeSessions' terms, except that a session the provider no longer holds
// is one already ended through its handle, which session.Provider.Close
// answers ErrNotHeld for a second Close of, and is not a failure to end.
func (s *Service) closeSubordinates() []error {
	s.mu.Lock()
	held := s.delegated
	s.delegated = make(map[string]launchedSubordinate)
	s.mu.Unlock()
	var errs []error
	for domain, launched := range held {
		if launched.host == nil {
			errs = append(errs, fmt.Errorf("service: domain %s holds a session and no provider", domain))
			continue
		}
		if err := launched.host.Close(launched.session); err != nil && !errors.Is(err, session.ErrNotHeld) {
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
					s.adoptSurvivor(d, attempts[n-1], ref, liveness)
					continue
				}
				reason = fmt.Sprintf("its subordinate coordinator's terminal %s is gone", ref)
			} else {
				reason = fmt.Sprintf("attempt %d never recorded a session", latest)
			}
		}
		if relaunches := max(len(attempts)-1, 0); relaunches >= s.cfg.RelaunchAttempts {
			// Past the bound the delegation holds for a person, on
			// taskholds.go's terms for a task: the hold is keyed by the
			// attempt that reached the bound, so a retry answered on it is
			// spent by the attempt the retry begins. A retry already
			// answered relaunches once more here; a stop answered ended
			// the delegation at the answer, so it is not standing.
			answer, err := s.relaunchAnswerOf(ctx, domainBoundKey(d, latest))
			if err != nil {
				s.log.Printf("reconciliation: the hold of domain %s could not be read: %v", d.Domain, err)
				continue
			}
			if answer != boundRetry {
				past := fmt.Sprintf("%s, it has been relaunched %d times and relaunch_attempts is %d", reason, relaunches, s.cfg.RelaunchAttempts)
				if h, err := s.holdDomainBound(ctx, d, latest, past); err != nil {
					s.log.Printf("reconciliation: the subordinate coordinator of domain %s is not started again (%s), and its hold could not be registered: %v",
						d.Domain, past, err)
				} else {
					s.log.Printf("reconciliation: the subordinate coordinator of domain %s is not started again (%s); it holds for a person on %s",
						d.Domain, past, h.Key)
				}
				continue
			}
			reason += ", and its hold at the bound was answered " + machine.AnswerRetry
		}
		attempt, err := s.store.BeginDelegationAttempt(ctx, d.ID, latest)
		if err != nil {
			s.log.Printf("reconciliation: the subordinate coordinator of domain %s was not relaunched: %v", d.Domain, err)
			continue
		}
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s is relaunched as attempt %d: %s",
			d.Domain, attempt.Sequence, reason)
		s.acknowledgeSupersededDomain(ctx, d, latest)
		s.launchSubordinateInBackground(d, attempt.Sequence)
	}
}

// adoptSurvivor is the restart's answer for a standing delegation whose
// session did not probe Dead: the attempt's reference is adopted into the
// survivors placer with the identity of the process hosting its terminal
// as the attempt recorded it, on session.Survivors.Adopt's terms, so a
// process that outlived the service is placed as its domain's subordinate,
// and so is a process its terminal server started, a window it opened
// there among them. A reference whose process is proven gone is not
// adopted, which the next pass of the loop surfaces; one adopted
// unverified is logged with why.
func (s *Service) adoptSurvivor(d store.Delegation, attempt store.DelegationAttempt, ref string, liveness taskstate.Liveness) {
	host := agents.Identity{PID: attempt.Host.PID, Start: attempt.Host.Start}
	placed, adopted, err := s.survivors.Adopt(session.HomeMarker(s.home), ref, host)
	switch {
	case !adopted && err != nil:
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s (session %s) reads %s and could not be placed as a survivor: %v",
			d.Domain, ref, liveness, err)
	case !adopted:
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s (session %s) reads %s and its process is gone; it is left for the loop",
			d.Domain, ref, liveness)
	case err != nil:
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s (session %s) reads %s and is placed as the survivor leading group %d, unverified: %v",
			d.Domain, ref, liveness, placed.Group, err)
	default:
		s.log.Printf("reconciliation: the subordinate coordinator of domain %s (session %s) reads %s and is placed as the survivor leading group %d",
			d.Domain, ref, liveness, placed.Group)
	}
}

// domainBoundPrefix is the key space of the holds the relaunch bound raises
// for a delegation, beside the task holds' key spaces.
const domainBoundPrefix = "/relaunch-bound/"

// domainBoundKey is the key of the hold the relaunch bound raises for a
// domain's delegation attempt, on relaunchBoundKey's terms: keyed by the
// delegation and the attempt that reached the bound, so the same attempt
// found past the bound at every later pass is the same hold, a later
// attempt is a new key, and a later delegation of the same domain reaching
// the same attempt number is a new key too, so an answer given to one
// delegation's hold is never read as an answer to another's.
func domainBoundKey(d store.Delegation, attempt int64) string {
	return "domain/" + d.Domain + domainBoundPrefix + strconv.FormatInt(d.ID, 10) + "/" + strconv.FormatInt(attempt, 10)
}

// domainOfBoundKey is the domain a hold key domainBoundKey wrote names, and
// false for every other key, so a hold that names neither a task nor a run
// can still say how it is answered: given --domain.
func domainOfBoundKey(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "domain/")
	if !ok {
		return "", false
	}
	domain, tail, ok := strings.Cut(rest, domainBoundPrefix)
	if !ok || domain == "" || home.ValidDomain(domain) != nil {
		return "", false
	}
	id, attempt, ok := strings.Cut(tail, "/")
	if !ok {
		return "", false
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", false
	}
	if _, err := strconv.ParseInt(attempt, 10, 64); err != nil {
		return "", false
	}
	return domain, true
}

// holdDomainBound registers the bound's hold for a delegation whose latest
// attempt is past relaunch_attempts, on holdAtBound's terms, and publishes
// it as it stands. Registering a key already there returns that hold
// unchanged, open or answered.
func (s *Service) holdDomainBound(ctx context.Context, d store.Delegation, attempt int64, past string) (store.Hold, error) {
	h, err := s.store.RegisterHold(ctx, store.Hold{
		Key:     domainBoundKey(d, attempt),
		Subject: "whether to begin this domain's coordinator again after it ended as many times as the program begins it on its own",
		Detail: fmt.Sprintf("domain %s (delegation %d): %s. Answer %q with --domain to begin its coordinator once more, "+
			"or %q to end the delegation and take the domain's work back.",
			d.Domain, d.ID, past, machine.AnswerRetry, machine.AnswerStop),
	})
	if err != nil {
		return store.Hold{}, err
	}
	s.publishHoldState(h)
	return h, nil
}

// acknowledgeSupersededDomain acknowledges, as the program, every queued
// subordinate-gone domain wake about the delegation's attempt a relaunch
// replaced, on acknowledgeSuperseded's terms, and leaves every other row.
func (s *Service) acknowledgeSupersededDomain(ctx context.Context, d store.Delegation, attempt int64) {
	queued, err := s.store.DelegationWakes(ctx, d.ID)
	if err != nil {
		s.log.Printf("the wakes superseded by relaunching the subordinate coordinator of domain %s could not be read: %v", d.Domain, err)
		return
	}
	var ids []int64
	for _, w := range queued {
		if w.Queued() && w.Kind == string(supervisor.KindSubordinateGone) && subordinateGoneAbout(w.Marker, attempt) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := s.store.AcknowledgeDomainWakes(ctx, ids, store.AcknowledgedByProgram()); err != nil {
		s.log.Printf("the %d domain wake(s) superseded by relaunching the subordinate coordinator of domain %s could not be acknowledged: %v",
			len(ids), d.Domain, err)
		return
	}
	s.log.Printf("relaunching the subordinate coordinator of domain %s superseded %d queued domain wake(s) about attempt %d, acknowledged by the program",
		d.Domain, len(ids), attempt)
}

// respondDomain answers the hold the relaunch bound raised for a domain's
// delegation, task.respond given TaskRespondRequest.Domain: the
// coordinator's, refused for a worker and for a subordinate, since a hold
// waits on a person and a subordinate answers none. The hold is the one
// keyed by the delegation's latest attempt; a domain with no standing
// delegation, or one holding on nothing, is ipc.ErrRefused, and an answer
// outside retry and stop is ipc.ErrInvalidRequest. The resolution is
// recorded first, with the resolver internal/ipc derives, then acted on: a
// retry begins the subordinate once more as a new attempt anchored to the
// one the hold names, which spends the answer (P6), and acknowledges the
// subordinate-gone rows about the attempt it replaced as the program; a
// stop ends the delegation with store.EndedByAnswer, its one revision,
// and ends the session this service holds for it.
func (s *Service) respondDomain(ctx context.Context, req ipc.Request, r machine.TaskRespondRequest) (machine.TaskAnswered, error) {
	if _, err := s.refuseSubordinate(req); err != nil {
		return machine.TaskAnswered{}, err
	}
	switch {
	case strings.TrimSpace(r.Answer) == "":
		return machine.TaskAnswered{}, fmt.Errorf("%w: an answer to domain %s's hold is empty", ipc.ErrInvalidRequest, r.Domain)
	case r.Answer != machine.AnswerRetry && r.Answer != machine.AnswerStop:
		return machine.TaskAnswered{}, fmt.Errorf("%w: domain %s's hold is answered %q or %q, not %q",
			ipc.ErrInvalidRequest, r.Domain, machine.AnswerRetry, machine.AnswerStop, r.Answer)
	case len(r.Note) > machine.MaxMessageBytes:
		return machine.TaskAnswered{}, fmt.Errorf("%w: the note is %d bytes, over the limit of %d, and is refused rather than cut",
			ipc.ErrInvalidRequest, len(r.Note), machine.MaxMessageBytes)
	}
	d, found, err := s.store.Delegation(ctx, r.Domain)
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	if !found || !d.Standing() {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w: domain %q", ipc.ErrRefused, errNotDelegated, r.Domain)
	}
	attempts, err := s.store.DelegationAttempts(ctx, d.ID)
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	var latest int64
	if n := len(attempts); n > 0 {
		latest = attempts[n-1].Sequence
	}
	key := domainBoundKey(d, latest)
	hold, err := s.store.Hold(ctx, key)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !hold.Open()) {
		return machine.TaskAnswered{}, fmt.Errorf("%w: domain %s holds on nothing: its delegation's latest attempt %d has no open hold",
			ipc.ErrRefused, r.Domain, latest)
	}
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	resolved, err := s.store.ResolveHoldNoted(ctx, key, r.Answer, r.Note, req.HoldResolver())
	if errors.Is(err, store.ErrHoldResolved) {
		return machine.TaskAnswered{}, fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	if err != nil {
		return machine.TaskAnswered{}, err
	}
	s.publishHoldState(resolved)
	out := machine.TaskAnswered{Hold: resolved, Effect: machine.AnswerRecorded, Domain: r.Domain}
	if r.Answer == machine.AnswerStop {
		ended, _, err := s.store.EndDelegation(ctx, d.ID, store.EndedByAnswer(), store.DelegationEndClosure{})
		if err != nil {
			return machine.TaskAnswered{}, fmt.Errorf("service: domain %s's hold was answered %q and the delegation could not be ended: %w",
				r.Domain, r.Answer, err)
		}
		s.endSubordinate(r.Domain, ended.ID)
		s.log.Printf("the delegation of domain %s (delegation %d) was ended by the answer %q to hold %s", r.Domain, ended.ID, r.Answer, key)
		out.Effect = machine.AnswerStopped
		return out, nil
	}
	attempt, err := s.store.BeginDelegationAttempt(ctx, d.ID, latest)
	if err != nil {
		out.NotRelaunched = err.Error()
		out.Effect = machine.AnswerRetryDeferred
		s.log.Printf("domain %s was answered %q on hold %s and its coordinator was not begun again: %v", r.Domain, r.Answer, key, err)
		return out, nil
	}
	s.acknowledgeSupersededDomain(ctx, d, latest)
	s.launchSubordinateInBackground(d, attempt.Sequence)
	s.log.Printf("the subordinate coordinator of domain %s is begun again as attempt %d on the answer %q to hold %s",
		r.Domain, attempt.Sequence, r.Answer, key)
	out.Effect = machine.AnswerRelaunched
	out.Relaunch = &machine.RelaunchBegun{Attempt: attempt.Sequence}
	return out, nil
}

// publishDomainWake pushes a domain wake the loop appended to the
// coordinator's wake stream, on publishWake's terms, as
// ipc.TypeDomainWakeQueued with the row's own identifier as its revision.
func (s *Service) publishDomainWake(w store.DomainWake) {
	if s.wakes == nil {
		return
	}
	payload, err := json.Marshal(w)
	if err != nil {
		s.log.Printf("could not encode domain wake %d of domain %s: %v", w.ID, w.Domain, err)
		return
	}
	if err := s.wakes.Publish(ipc.Event{Type: ipc.TypeDomainWakeQueued, Revision: uint64(w.ID), Payload: payload}); err != nil {
		s.log.Printf("could not push domain wake %d of domain %s: %v", w.ID, w.Domain, err)
	}
}
