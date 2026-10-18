package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/redact"
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
// process names the adapter and reason. A routing refusal is recorded as
// "route-refused" and the task resolves Failed rather than staying
// Dispatched forever. Grants are unchanged: the selected worker still starts
// under GrantWorker through the ordinary session Create path.
//
// # Records precede processes, and what that leaves open
//
// The attempt row is dispatch's claim, and it exists before Create is asked
// for anything; the route is recorded on it before Create, and
// Session.Reference is recorded through store.SetAttemptSession right after
// Create returns, which is the one record of which terminal a task's attempt
// runs in and the reference internal/taskstate's probe reads. A reference
// that cannot be recorded ends the session before the failure is reported,
// so no process this service started stands behind an attempt whose row does
// not name it. Between Create returning and the row being written, a crash
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
// built over the routed adapter for that launch.
type launchedWorker struct {
	host    session.Host
	session *session.Session
}

// launchTask launches the worker for a dispatch that reached proven: it
// asserts the copy again, routes a worker adapter, records the route,
// creates the terminal session with the task's instructions and the route
// back to this service, on route.go's terms, records the session's
// reference on the attempt, and holds the session. A copy that fails the
// assertion is an *isolationRefused, recorded; a routing refusal is a
// *routeRefused, recorded; a session whose reference could not be recorded
// is ended before the error is reported.
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
	selection, role, err := s.routeTaskWorker(ctx, task, d.attempt)
	if err != nil {
		return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
	}
	model := ""
	if role.Provider != "" {
		model, err = agents.Translate(selection.Resolution.Name, role.Provider, role.Type, role.Version)
		if err != nil {
			return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
		}
	}
	if err := agents.AcceptsEffort(selection.Resolution.Name, role.Effort); err != nil {
		return nil, s.refuseRoute(ctx, d.id, d.attempt, d.anchor, err)
	}
	if err := s.store.SetAttemptRoute(ctx, d.id, d.attempt, selection.Resolution.Name, selection.Reason, store.AttemptLaunch{
		Role: task.Role, Model: model, Effort: role.Effort,
	}); err != nil {
		return nil, fmt.Errorf("service: recording the route of task %s attempt %d: %w", d.id, d.attempt, err)
	}
	host := s.sessionHost(selection.Resolution.Runner)
	launched, err := host.Create(ctx, session.Spec{
		TaskID:       d.id,
		Isolation:    fresh,
		Instructions: string(instructions) + d.note + s.routeSection(d.want.Shape),
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
	if err := s.store.SetAttemptSession(ctx, d.id, d.attempt, reference); err != nil {
		if closeErr := host.Close(launched); closeErr != nil {
			s.log.Printf("the worker of task %s, whose session could not be recorded, was not ended either: %v",
				d.id, closeErr)
		}
		return nil, fmt.Errorf("service: recording the session of task %s attempt %d: %w", d.id, d.attempt, err)
	}
	s.mu.Lock()
	if s.launched == nil {
		s.launched = make(map[string]launchedWorker)
	}
	s.launched[d.id] = launchedWorker{host: host, session: launched}
	s.mu.Unlock()
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
	obs, err := vcs.Observe(ctx, copyPath, vcs.WithRedactor(redact.New()))
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
// host holding it, and whether it launched one. It is the one reader of
// the launched map outside Close.
func (s *Service) sessionOf(id string) (session.Host, *session.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	launched, ok := s.launched[id]
	if !ok || launched.host == nil || launched.session == nil {
		return nil, nil, false
	}
	return launched.host, launched.session, true
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
