package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Launching a dispatched task: the second assertion, the session, and the
// reference.
//
// PRD section 5's lifecycle moves a task from Dispatched to Running only when
// isolation is proven, and this file is the launch half of that edge, over
// the proof dispatch.go ended at. It is PRD section 8's "the supervisor owns
// terminal sessions, one per worker": the service holds one session.Provider,
// built in driverFor over the agent as resolved, and the sessions it creates
// are held here per task, one per attempt, until Close ends them.
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
// # Records precede processes, and what that leaves open
//
// The attempt row is dispatch's claim, and it exists before Create is asked
// for anything; Session.Reference is recorded on it through
// store.SetAttemptSession right after Create returns, which is the one
// record of which terminal a task's attempt runs in and the reference
// internal/taskstate's probe reads. A reference that cannot be recorded ends
// the session before the failure is reported, so no process this service
// started stands behind an attempt whose row does not name it. Between
// Create returning and the row being written, a crash leaves the process
// internal/session's doc describes: one no record names, which only #112's
// sweep closes.
//
// # What the launch moves, and what it does not
//
// The task's resolved state is not moved here. Running is answered by
// internal/taskstate's process tier from a Live probe of the recorded
// reference, and this service still builds its Reader with no probe, so
// after a launch the task resolves what the record gives it, which is
// Dispatched, until the probe is wired, which is #91's last PR. Nothing here
// writes an event to say Running: that would be a second record of a fact
// the process tier owns.
//
// One launch per attempt per service. A restart holds no session and no
// slot, so it dispatches the task again, which is a new attempt that
// observes and asserts the standing copy, and launches that attempt: by the
// record's lights that is PRD section 11's relaunch in place, with the same
// copy and the same instructions. The earlier attempt's process, if it is
// still running, is an orphan the new service holds no handle to and does
// not end; its reference probes Unobserved while it stands and Dead once it
// is gone, and the sweep that would account for it is #112's.
//
// # An adapter without worker sessions dispatches and never launches
//
// Create asks the resolved agent for a worker through agents.OpenWorker,
// which refuses an adapter that has not declared
// agents.CapabilityWorkerSessions with a *agents.CapabilityError. In this
// build only the scripted adapter declares it, so a service resolved to the
// shipped claude adapter dispatches every task and launches none: the
// refusal is logged, the task stays Dispatched with its slot and its attempt
// standing, and no reference is recorded. Nothing here turns that into a
// recorded refusal, because internal/taskstate's refusal tokens are about
// isolation and this is not an isolation failure; whether the real adapter
// runs as a stdin-driven worker is #95 line K, and until an adapter declares
// the capability a fleet over it is a fleet that dispatches and never
// launches. That is stated rather than widened.

// launchTask launches the worker for a dispatch that reached proven: it
// asserts the copy again, creates the terminal session with the task's
// instructions, records the session's reference on the attempt, and holds
// the session. A copy that fails the assertion is an *isolationRefused,
// recorded; an agent without worker sessions is a *agents.CapabilityError,
// wrapped, with nothing recorded; a session whose reference could not be
// recorded is ended before the error is reported.
func (s *Service) launchTask(ctx context.Context, d dispatched) (*session.Session, error) {
	built, err := s.driverFor(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: resolving the agent to launch task %s with: %w", d.id, err)
	}
	instructions, err := os.ReadFile(d.instructions)
	if err != nil {
		return nil, fmt.Errorf("service: reading the instructions of task %s: %w", d.id, err)
	}
	fresh, err := s.assertAtLaunch(ctx, d)
	if err != nil {
		return nil, err
	}
	launched, err := built.sessions.Create(ctx, session.Spec{
		TaskID:       d.id,
		Isolation:    fresh,
		Instructions: string(instructions),
	})
	if err != nil {
		return nil, fmt.Errorf("service: launching task %s attempt %d: %w", d.id, d.attempt, err)
	}
	reference := launched.Reference().String()
	if err := s.store.SetAttemptSession(ctx, d.id, d.attempt, reference); err != nil {
		if closeErr := built.sessions.Close(launched); closeErr != nil {
			s.log.Printf("the worker of task %s, whose session could not be recorded, was not ended either: %v",
				d.id, closeErr)
		}
		return nil, fmt.Errorf("service: recording the session of task %s attempt %d: %w", d.id, d.attempt, err)
	}
	s.mu.Lock()
	if s.launched == nil {
		s.launched = make(map[string]*session.Session)
	}
	s.launched[d.id] = launched
	s.mu.Unlock()
	return launched, nil
}

// assertAtLaunch observes the copy the dispatch proved and asserts it again,
// under the environment the worker is started with, and returns the fresh
// proof. A refusal is recorded on the terms refuseIsolation states, anchored
// to the history the dispatch was decided from.
func (s *Service) assertAtLaunch(ctx context.Context, d dispatched) (tasks.Isolation, error) {
	env := agents.WorkerEnvironment(os.Environ(), nil)
	obs, err := vcs.Observe(ctx, d.proof.Copy, vcs.WithRedactor(redact.New()))
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			return tasks.Isolation{}, s.refuseIsolation(ctx, d.id, d.attempt, d.anchor, taskstate.RefusedNotIsolated, err)
		}
		return tasks.Isolation{}, fmt.Errorf("service: observing the copy of task %s at launch: %w", d.id, err)
	}
	fresh, err := tasks.AssertIsolated(obs, env, d.want)
	if err != nil {
		reason, refused := tasks.Refusal(err)
		if !refused {
			return tasks.Isolation{}, fmt.Errorf("service: asserting the copy of task %s at launch: %w", d.id, err)
		}
		return tasks.Isolation{}, s.refuseIsolation(ctx, d.id, d.attempt, d.anchor, reason, err)
	}
	return fresh, nil
}

// sessionOf reports the session this service launched for a task and the
// provider holding it, and whether it launched one. It is the one reader of
// the launched map outside Close.
func (s *Service) sessionOf(id string) (session.Provider, *session.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	launched, ok := s.launched[id]
	if !ok || s.built == nil {
		return nil, nil, false
	}
	return s.built.sessions, launched, true
}

// closeSessions ends every session this service launched and still holds,
// on the terms session.Provider.Close states, and forgets them. It is
// Close's, and it ends nothing this service's provider did not create: a
// worker an earlier service of this home launched is an orphan this one
// never held.
func (s *Service) closeSessions() []error {
	s.mu.Lock()
	held := s.launched
	s.launched = make(map[string]*session.Session)
	built := s.built
	s.mu.Unlock()
	var errs []error
	for id, launched := range held {
		if built == nil {
			// A session is held only once a driver was built, so this is
			// unreachable; it is refused rather than dereferenced.
			errs = append(errs, fmt.Errorf("service: task %s holds a session and no provider", id))
			continue
		}
		if err := built.sessions.Close(launched); err != nil {
			errs = append(errs, fmt.Errorf("service: ending the worker of task %s: %w", id, err))
		}
	}
	return errs
}
