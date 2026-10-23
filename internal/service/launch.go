package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Launching a dispatched task: the second assertion, capability-aware
// routing, the session, and the reference.
//
// PRD section 5's lifecycle moves a task from Dispatched to Running only when
// isolation is proven, and this file is the launch half of that edge, over
// the proof dispatch.go ended at. It is PRD section 8's "the supervisor owns
// terminal sessions, one per worker": each launch builds a session.Host over
// the adapter agents.Route selected for that worker (which may differ from
// the run agent), and the sessions it creates are held here per task, one
// per attempt, until Close ends them.
//
// # The proof is made again at launch
//
// dispatch.go's assertion is as of the moment the copy was observed, and
// internal/session's Create takes a proof and compares nothing itself, which
// that package's doc names as the time-of-check gap: between the two the
// copy can be retargeted, by a remote added or a link swapped under it. So
// the launch observes the copy again, through internal/vcs, and asserts it
// again, through tasks.AssertIsolated under the same
// agents.WorkerEnvironment(os.Environ(), nil) dispatch asserted under, and
// hands Create the proof that read produced and never the one in the slot.
// The slot's proof is what says the launch is of a dispatched task; the
// launch's own proof is what the worker starts under. A copy that fails the
// second assertion is refused the way dispatch's is: the "isolation-refused"
// event is recorded, carrying internal/taskstate's token through
// tasks.Refusal and anchored to the history the dispatch was decided from,
// and the task resolves Failed from it. What the second assertion does not
// close is the same window one step later - the copy can change between
// this assertion and the process starting in it - and nothing here claims
// otherwise; the second check in the instructions is what runs after that,
// and it is detection by an obedient worker.
//
// # Capability-aware routing (GAP-02)
//
// Before Create, agents.Route selects a worker adapter from the operator's
// agent list and the worker capability need (worker_sessions, plus
// terminal_workers when session_provider is tmux). A single named entry that
// is not auto is an operator pin (Override): missing Need is a typed refuse
// with no fallthrough. A longer list is ordered preference with capability
// filtering. The selection is recorded on the attempt through
// store.SetAttemptRoute before Create, so every launch that reaches a
// process names the adapter, the reason, and every entry routing passed
// over with the stable reason agents gave for it. A routing refusal is recorded as
// "route-refused" and the task resolves Failed rather than staying
// Dispatched forever. Grants are unchanged: the selected worker still starts
// under GrantWorker through the ordinary session Create path.
//
// # Records precede processes, and what that leaves open
//
// The attempt row is dispatch's claim, and it exists before Create is asked
// for anything; the route is recorded on it before Create. Once Create
// returns, the session is put in the launched map under a pending mark,
// Session.Reference is recorded through store.SetAttemptSession, which is
// the one record of which terminal a task's attempt runs in and the
// reference internal/taskstate's probe reads, and the mark is then settled:
// the entry kept when the record was written and removed when it was not.
// mu is taken for the hold and for the settlement and never across the
// write, so a store that is slow to take it holds no reader of mu, and in
// particular not Shutdown, whose knownGroups reads the map under mu before
// it starts Close or waits on its deadline (#546's first round held mu
// across the write, and a forced stop then waited on the store past every
// bound P15 sets).
//
// The two kinds of reader are each answered from a settled fact:
//
//   - A reader that reads the reference off the record and then asks this
//     service about that worker - a probe through heldTerminals, a message,
//     a peek, a stop, the landed pass - finds the session, because it was
//     held before the write began. Recorded first and held after, as this
//     file did until #538, each of those answered in between as for a
//     worker this service did not start: the probe Unobserved over a live
//     worker, the message and the peek refused, a stop ending the group by
//     identity as an earlier service's worker (on every launch whose slot
//     was settled before it, which is all but workerfallback.go's handoff),
//     a landed worker left running.
//   - A reader that asks by task alone through sessionOf, with no reference
//     to compare, is answered once the record's write has returned, so a
//     session it reports held is one the record names. Held first with no
//     such wait, a reader could find a session whose record then failed, as
//     a launch Close cancels does, which is how a reorder alone fails.
//
// What stays open: a reader that read the record before the write sees the
// attempt with no reference and answers as for a worker not yet launched;
// sessionOf's wait is as long as the write, bounded by the launch's context
// and the store and not by anything here, so a status read about a task
// whose launch is recording can wait on a slow store as its own reads of
// that store would; and knownGroups and closeSessions read a pending entry
// without waiting, so a stop forces and ends a worker whose record may not
// be written. A reference that cannot be recorded leaves the session no
// longer held and ended before the failure is reported, so no process this
// service started stands behind an attempt whose row does not name it.
// Between Create returning and the row being written, a crash
// leaves the process internal/session's doc describes: one no record names.
// The dispatch that retries the task sweeps the standing copy before its
// claim, on sweep.go's terms, which finds such a process by its working
// directory on Linux and macOS and is agents.ErrSweepUnsupported elsewhere,
// where the retry is refused and holds for a person, and the window stays
// open only behind a retry answer to that hold.
//
// # What the launch moves, and what it does not
//
// The task's resolved state is not moved here, and it moves anyway: Running
// is answered by internal/taskstate's process tier from a Live probe of the
// recorded reference, every reader this service builds probes through its
// Terminals, which in production looks up the per-task host when this
// service still holds the session, and that provider answers Live for a
// session it holds whose process is running. So a launched task reads
// Running from the process tier on the task surface, the queue,
// reconciliation and the loop from the moment the reference is recorded
// until the worker exits, and then what precedence gives with Dead: Unknown
// from the process tier when no run is attributed, or the run's state.
// Nothing here writes an event to say Running: that would be a second record
// of a fact the process tier owns.
//
// One launch per attempt per service, and a launched task is never
// dispatched again. A restart holds no session and no slot, and what it
// does about a task whose latest attempt names a session is reconcile.go's
// question and not dispatch's: the terminal is probed, a dead one is
// relaunched in place by relaunch.go as a new attempt over the standing
// copy with the same instructions plus a note, and an unobserved one is
// left alone, which is PRD section 11's ambiguous liveness. dispatchDue
// passes such a task over and dispatchClaimed refuses it with
// errTaskLaunched, so a restart cannot start a second worker beside one it
// cannot see. The earlier attempt's process, if it is still running, is an
// orphan the new service holds no handle to and does not end; its reference
// probes Unobserved while it stands and Dead once it is gone, and only then
// is the task relaunched, after the sweep sweep.go reads has been asked of
// the copy.

// launchedWorker is one task's held session host and session. The host is
// built over the routed adapter for that launch. recording is non-nil while
// the session's reference is being recorded, and is closed once the entry
// shows how that ended: settled with recording nil when the record was
// written, gone when it was not.
type launchedWorker struct {
	host      session.Host
	session   *session.Session
	recording chan struct{}
}

// settleRecording ends launched's pending mark once its record's write has
// returned: when it was written the entry is kept, no longer pending, and
// when it was not the entry is removed. Either is done only while the entry
// is still that session, so an entry a stop, a supersession or Close took
// while the record was written is left as they left it.
func (s *Service) settleRecording(id string, launched *session.Session, recorded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.launched[id]
	if !ok || current.session != launched {
		return
	}
	if !recorded {
		delete(s.launched, id)
		return
	}
	current.recording = nil
	s.launched[id] = current
}

// launchTask launches the worker for a dispatch that reached proven: it
// asserts the copy again, routes a worker adapter, records the route,
// creates the terminal session with the task's instructions and the route
// back to this service, on route.go's terms, holds the session pending,
// records its reference on the attempt and settles the hold, on the file
// comment's terms. A copy that fails the assertion is an *isolationRefused,
// recorded; a routing refusal is a *routeRefused, recorded; a session whose
// reference could not be recorded is no longer held and is ended before the
// error is reported.
func (s *Service) launchTask(ctx context.Context, d dispatched) (*session.Session, error) {
	instructions, err := os.ReadFile(d.instructions)
	if err != nil {
		return nil, fmt.Errorf("service: reading the instructions of task %s: %w", d.id, err)
	}
	fresh, err := s.assertAtLaunch(ctx, d)
	if err != nil {
		return nil, err
	}
	task, err := s.store.Task(ctx, d.id)
	if err != nil {
		return nil, fmt.Errorf("service: reading task %s to route its worker: %w", d.id, err)
	}
	project, err := s.launchKnowledge(ctx, task, d)
	if err != nil {
		return nil, err
	}
	selection, role, err := s.routeTaskWorker(ctx, task, d.attempt)
	if err != nil {
		return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
	}
	model := ""
	if role.Provider != "" || role.ContextWindow != "" {
		model, err = agents.Translate(selection.Resolution.Name, role.Provider, role.Type, role.Version, role.ContextWindow)
		if err != nil {
			return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, fmt.Errorf("service: the %s role: %w", config.TaskRoleName(task.Role), err))
		}
	}
	if err := agents.AcceptsEffort(selection.Resolution.Name, role.Effort); err != nil {
		return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
	}
	if err := s.store.SetAttemptRoute(ctx, d.id, d.attempt, selection.Resolution.Name, selection.Reason, store.AttemptLaunch{
		Role: task.Role, Model: model, Effort: role.Effort, Skipped: skippedAgents(selection.Resolution.Skipped),
	}); err != nil {
		return nil, fmt.Errorf("service: recording the route of task %s attempt %d: %w", d.id, d.attempt, err)
	}
	firstTurn := string(instructions) + d.note + knowledgeSection(project) + s.routeSection(d.want.Shape)
	if err := s.refuseKnowledgeIfLaunchTooLarge(ctx, d, selection.Resolution.Runner, fresh, firstTurn, project, model, role.Effort); err != nil {
		return nil, err
	}
	if err := s.store.SetAttemptKnowledge(ctx, d.id, d.attempt, store.AttemptKnowledge{
		Commit: project.Commit(), Digest: project.Digest(),
	}); err != nil {
		return nil, fmt.Errorf("service: recording the knowledge revision of task %s attempt %d: %w", d.id, d.attempt, err)
	}
	host := s.sessionHost(selection.Resolution.Runner)
	launched, err := host.Create(s.registry.hurrying(ctx), session.Spec{
		TaskID:       d.id,
		Isolation:    fresh,
		Instructions: firstTurn,
		Route:        s.workerRoute,
		Model:        model,
		Effort:       role.Effort,
	})
	if err != nil {
		if role.Fallback != nil && ctx.Err() == nil && agents.FallbackAllowed(err) {
			failure := agents.FailureUnavailable
			var protocol *agents.WorkerError
			if errors.As(err, &protocol) {
				failure = protocol.Failure
			}
			return s.switchWorker(ctx, task, d.attempt, nil, nil, selection.Resolution.Name, role, failure)
		}
		var undeclared *agents.CapabilityError
		if errors.As(err, &undeclared) {
			return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
		}
		return nil, fmt.Errorf("service: launching task %s attempt %d: %w", d.id, d.attempt, err)
	}
	reference := launched.Reference().String()
	// Held before it is recorded, under a pending mark, and settled after,
	// on the file comment's terms; mu is never held across the write.
	recording := make(chan struct{})
	s.mu.Lock()
	if s.launched == nil {
		s.launched = make(map[string]launchedWorker)
	}
	s.launched[d.id] = launchedWorker{host: host, session: launched, recording: recording}
	s.mu.Unlock()
	if s.beforeSessionRecord != nil {
		s.beforeSessionRecord(d.id, reference)
	}
	// Every task this build dispatches is on this machine, whose worker's
	// server the store does not record.
	recordErr := s.store.SetAttemptSession(ctx, d.id, d.attempt, reference, store.SessionHost{})
	if recordErr == nil && s.afterSessionRecorded != nil {
		s.afterSessionRecorded(d.id, reference)
	}
	s.settleRecording(d.id, launched, recordErr == nil)
	close(recording)
	if recordErr != nil {
		// ErrNotHeld is a session something else ended while it was held
		// pending, Close's closeSessions among them, which leaves nothing
		// here to end.
		if closeErr := host.Close(launched); closeErr != nil && !errors.Is(closeErr, session.ErrNotHeld) {
			s.log.Printf("the worker of task %s, whose session could not be recorded, was not ended either: %v",
				d.id, closeErr)
		}
		return nil, fmt.Errorf("service: recording the session of task %s attempt %d: %w", d.id, d.attempt, recordErr)
	}
	s.log.Printf("task %s attempt %d was routed to %s (%s)", d.id, d.attempt, selection.Resolution.Name, selection.Reason)
	s.watchWorkerFallback(task, d.attempt, host, launched, selection.Resolution.Name)
	return launched, nil
}

// routeWorker selects the adapter a task worker launches with, on
// agents.Route's terms, and the role that selection was asked for. A task
// that names no role uses the worker role. A role that names an agent is a
// pin of that one agent. A role that names none uses the agent list: a
// single named entry that is not auto is an operator pin, and a longer list
// is ordered preference with capability filtering. Grants are not decided
// here. A role the configuration no longer has is refused, and nothing else
// is started in its place.
func (s *Service) routeWorker(ctx context.Context, roleName string) (agents.Selection, config.Role, error) {
	role, err := s.cfg.Roles.ForTask(roleName)
	if err != nil {
		return agents.Selection{}, config.Role{}, err
	}
	entries := s.cfg.Agent
	override := workerOverride(entries)
	if role.Agent != "" {
		// The role's agent is the pin, including a name that is not an
		// adapter. workerOverride would treat auto as the fallback list;
		// a role has no fallback, so the name is the override as written.
		entries = []string{role.Agent}
		override = role.Agent
	}
	tmux := s.cfg.SessionProvider == config.SessionProviderTmux
	selection, err := agents.Route(ctx, s.catalog, entries, agents.NeedForWorker(tmux), override)
	if err != nil {
		return agents.Selection{}, config.Role{}, err
	}
	return selection, role, nil
}

// skippedAgents is the record of what a selection passed over: each entry's
// adapter name, the stable skip reason internal/agents gave it, and, for a
// missing capability, which one. The entry's arguments and the error text
// are not carried, so nothing an operator wrote into an entry and nothing an
// adapter printed reaches the record.
func skippedAgents(skipped []agents.Unavailability) []store.SkippedAgent {
	out := make([]store.SkippedAgent, 0, len(skipped))
	for _, u := range skipped {
		entry := store.SkippedAgent{Agent: u.Name, Reason: u.Reason}
		var undeclared *agents.CapabilityError
		if u.Reason == agents.SkipMissingCapability && errors.As(u.Err, &undeclared) {
			entry.Capability = string(undeclared.Capability)
		}
		out = append(out, entry)
	}
	return out
}

// workerOverride is the operator pin: a single named agent entry that is not
// auto. An empty list, auto alone, and a multi-entry preference list each
// yield no override so Route expands and filters in order.
func workerOverride(entries []string) string {
	if len(entries) != 1 {
		return ""
	}
	entry := strings.TrimSpace(entries[0])
	if entry == "" || entry == agents.AutoEntry {
		return ""
	}
	return entry
}

// refuseRoute records the refusal as the task's "route-refused" event,
// anchored to the history the dispatch was decided from, and returns the
// typed refusal. The task resolves Failed from it so a routing miss does
// not leave the attempt Dispatched forever.
func (s *Service) refuseRoute(ctx context.Context, id string, attempt, anchor int64, cause error) error {
	detail := cause.Error()
	s.log.Printf("task %s attempt %d was refused a worker by routing: %v", id, attempt, cause)
	event, err := s.store.AppendTaskEventAfter(ctx, id, anchor, routeRefusedKind, detail)
	if err != nil {
		return fmt.Errorf("service: recording the routing refusal of task %s: %w", id, err)
	}
	return &routeRefused{Task: id, Attempt: attempt, Event: event, Cause: cause}
}

// routeRefusedKind is the kind internal/taskstate gives a routing refusal.
const routeRefusedKind = "route-refused"

// routeRefused reports a launch whose worker routing refused every configured
// agent. The task resolves Failed from the recorded event.
type routeRefused struct {
	Task    string
	Attempt int64
	Event   store.TaskEvent
	Cause   error
}

func (e *routeRefused) Error() string {
	return fmt.Sprintf("service: task %s attempt %d: worker routing refused: %v", e.Task, e.Attempt, e.Cause)
}

func (e *routeRefused) Unwrap() error { return e.Cause }

// assertAtLaunch observes the copy the dispatch proved and asserts it again,
// under the environment the worker is started with, and returns the fresh
// proof. A refusal is recorded on the terms refuseIsolation states, anchored
// to the history the dispatch was decided from.
func (s *Service) assertAtLaunch(ctx context.Context, d dispatched) (tasks.Isolation, error) {
	return s.assertCopy(ctx, d.id, d.attempt, d.anchor, d.proof.Copy, d.want)
}

// assertCopy is the one assertion a dispatch, a launch and a relaunch share:
// it observes the directory at copyPath through internal/vcs and asserts it
// through tasks.AssertIsolated under agents.WorkerEnvironment(os.Environ(),
// nil), which is the environment a worker is started with, and returns the
// proof. A path that is not a repository, or an assertion that refuses, is
// recorded as the task's "isolation-refused" event on refuseIsolation's
// terms, against the attempt and anchored to the history sequence given,
// and reported as the typed *isolationRefused. Every other failure is a
// failure of the read and records nothing.
func (s *Service) assertCopy(ctx context.Context, id string, attempt, anchor int64, copyPath string,
	want tasks.Expectation) (tasks.Isolation, error) {
	env := agents.WorkerEnvironment(os.Environ(), nil)
	obs, err := vcs.Observe(ctx, copyPath, s.git()...)
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			return tasks.Isolation{}, s.refuseIsolation(ctx, id, attempt, anchor, taskstate.RefusedNotIsolated, err)
		}
		return tasks.Isolation{}, fmt.Errorf("service: observing the copy of task %s: %w", id, err)
	}
	proof, err := tasks.AssertIsolated(obs, env, want)
	if err != nil {
		reason, refused := tasks.Refusal(err)
		if !refused {
			return tasks.Isolation{}, fmt.Errorf("service: asserting the copy of task %s: %w", id, err)
		}
		return tasks.Isolation{}, s.refuseIsolation(ctx, id, attempt, anchor, reason, err)
	}
	return proof, nil
}

// sessionOf reports the session this service launched for a task and the
// host holding it, and whether it launched one. A session whose reference
// is still being recorded is answered once that write has returned, outside
// mu: held when it was recorded and not when it was not, so a session it
// reports held is one the attempt's record names. That wait is as long as
// the record's write, which is bounded by the launch's context and nothing
// else here; knownGroups and closeSessions, which a stop reaches, read the
// map without it.
func (s *Service) sessionOf(id string) (session.Host, *session.Session, bool) {
	for {
		s.mu.Lock()
		launched, ok := s.launched[id]
		s.mu.Unlock()
		if !ok || launched.host == nil || launched.session == nil {
			return nil, nil, false
		}
		if launched.recording == nil {
			return launched.host, launched.session, true
		}
		if s.awaitingSessionRecord != nil {
			s.awaitingSessionRecord(id)
		}
		<-launched.recording
	}
}

// closeSessions ends every session this service launched and still holds,
// on the terms session.Provider.Close states, and forgets them. It is
// Close's, and it ends nothing this service's provider did not create: a
// worker an earlier service of this home launched is an orphan this one
// never held.
func (s *Service) closeSessions() []error {
	s.mu.Lock()
	held := s.launched
	s.launched = make(map[string]launchedWorker)
	s.mu.Unlock()
	var errs []error
	for id, launched := range held {
		if launched.host == nil {
			errs = append(errs, fmt.Errorf("service: task %s holds a session and no provider", id))
			continue
		}
		if err := launched.host.Close(launched.session); err != nil {
			errs = append(errs, fmt.Errorf("service: ending the worker of task %s: %w", id, err))
		}
	}
	return errs
}
