package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/session"
)

// This file is PRD section 8's "stopping is bounded" and the stop half of
// principle P15: on a stop request or SIGTERM the service stops admitting
// work, ends the process groups it registered, and exits within stop_grace,
// ending them by force once the grace is spent.
//
// Close is the orderly half: it refuses new work, cancels every run it is
// advancing, which asks each agent group to end politely, waits for its
// background work, waits for the supervisor loop until stopDeadline, and
// ends its workers and subordinate coordinators. Shutdown is Close under a bound. When the bound
// is reached first, or the caller forces the stop, it logs which step of
// Close was still waiting, sends every group it knows of the forceful signal
// through agents.ForceGroup, hurries the termination of every agent
// invocation, worker and subordinate coordinator it started through
// agents.WithHurry, gives Close forceSettle to finish now that
// nothing it waits on should be running, and returns *StopForcedError so the
// caller exits rather than waiting any longer.
//
// The groups it forces are the ones this service registered and can name by
// a verified identity: every agent invocation containRunner registered, read
// both when the stop began and when it was forced, and the leader of every
// worker and subordinate coordinator session it launched, read when the stop
// began, because Close forgets a session before it ends it.
//
// # Residual gaps
//
//   - A process that left its group, a configured command, a notify command,
//     and the provider command register nothing, so nothing here forces them;
//     the configured commands are ended by their own context when the run
//     they serve is cancelled, on internal/stages' terms.
//   - A group whose leader's identity could not be read when it registered is
//     passed over rather than signalled by a bare number.
//   - A group whose leader already ended and whose members remain is not
//     signalled here, because nothing can tie its identifier back to the
//     registration (agents.ForceGroup says why). It is ended by the
//     termination of the handle that holds its leader, an invocation's, a
//     worker's or a subordinate coordinator's, which the forced stop hurries
//     through agents.WithHurry; the hurry reaches every one this service
//     started, because its wrappers, its launch and its delegation all start
//     them on the registry's hurry channel. The service gives them
//     forceSettle and then exits whether or not they are done.
//   - A tmux-hosted worker is forced by its session's reference and by its
//     server's identity; anything else the server started is not reached.
//   - The exit is within stop_grace plus forceSettle, not within stop_grace:
//     the forceful step and the last wait for Close come after the grace.
//   - What Close was still doing is abandoned with the process, so the store
//     may be left without its final writes; the recovery the next open runs is
//     what answers for that, on the terms doc.go states for a killed service.

// forceSettle is how long Shutdown waits, after the forceful step, for Close
// to finish before it gives up on it. Chosen, not measured: long enough for a
// killed group's members to be reaped and Close to release the store and the
// lock, short enough that the overrun past stop_grace is small.
const forceSettle = time.Second

// StopForcedError is Shutdown's answer when Close had not finished when
// stop_grace ran out, or when the caller forced the stop first. Waiting is
// the step Close was in, and Forced the groups sent the forceful signal. The
// caller exits on it: Close may still be running, and nothing it holds is
// safe to wait on any longer.
type StopForcedError struct {
	// Grace is the stop_grace the stop was held to.
	Grace time.Duration
	// Early reports that the caller forced the stop before the grace ran out.
	Early bool
	// Waiting is the step Close was in when the stop was forced.
	Waiting string
	// Forced is how many groups still running were sent the forceful signal.
	Forced int
}

func (e *StopForcedError) Error() string {
	return "service: " + e.account()
}

// account is the sentence the error and the service log share.
func (e *StopForcedError) account() string {
	why := fmt.Sprintf("stop_grace %s ran out", e.Grace)
	if e.Early {
		why = fmt.Sprintf("the stop was forced before stop_grace %s ran out", e.Grace)
	}
	return fmt.Sprintf("%s while the service was still %s, and %d process group(s) it registered and still running were ended by force",
		why, e.Waiting, e.Forced)
}

// ErrStopForced matches every *StopForcedError through errors.Is.
var ErrStopForced = errors.New("service: the stop was forced")

// Is reports whether target is ErrStopForced.
func (e *StopForcedError) Is(target error) bool { return target == ErrStopForced }

// Shutdown is Close bounded by stop_grace, on this file's terms. It returns
// Close's own result when Close finishes within the grace and before force
// is closed, and a *StopForcedError otherwise. A nil force is never closed.
func (s *Service) Shutdown(force <-chan struct{}) error {
	grace := s.cfg.StopGrace
	deadline := s.stopDeadline()
	known := s.knownGroups()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	early := false
	select {
	case err := <-done:
		return err
	case <-timer.C:
	case <-force:
		early = true
	}
	waiting, _ := s.closing.Load().(string)
	if waiting == "" {
		waiting = "beginning to close"
	}
	settled := time.NewTimer(forceSettle)
	defer settled.Stop()
	s.registry.hurryTerminations()
	forced := s.forceGroups(append(known, s.registry.leaders()...))
	stop := &StopForcedError{Grace: grace, Early: early, Waiting: waiting, Forced: forced}
	s.log.Printf("%s", stop.account())
	select {
	case err := <-done:
		return errors.Join(stop, err)
	case <-settled.C:
		if held := s.registry.held(); held > 0 {
			s.log.Printf("%d agent invocation(s) had not finished ending their process trees when the service gave up on them", held)
		}
		return stop
	}
}

// knownGroups is every group this service can name by a verified identity
// when the stop begins, on this file's terms.
func (s *Service) knownGroups() []agents.Identity {
	known := s.registry.leaders()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, launched := range s.launched {
		known = append(known, sessionLeaders(launched.session)...)
	}
	for _, launched := range s.delegated {
		known = append(known, sessionLeaders(launched.session)...)
	}
	return known
}

// sessionLeaders is the agent process a session's reference names and the
// host process it runs under, where either is known.
func sessionLeaders(held *session.Session) []agents.Identity {
	if held == nil {
		return nil
	}
	var out []agents.Identity
	if ref := held.Reference(); ref.PID > 0 && ref.Start != 0 {
		out = append(out, agents.Identity{PID: ref.PID, Start: ref.Start})
	}
	if host := held.Host(); host.PID > 0 && host.Start != 0 {
		out = append(out, host)
	}
	return out
}

// forceGroups sends every distinct group whose verified leader still runs the
// forceful signal at once, waits up to forceSettle for them together, logs
// each one that did not end or could not be reached, and reports how many
// were signalled. A group whose leader is gone, or whose leader's identifier
// was reused, is not signalled here and not counted, on ForceGroup's terms.
func (s *Service) forceGroups(groups []agents.Identity) int {
	seen := map[agents.Identity]bool{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string
	signalled := 0
	for _, id := range groups {
		if seen[id] {
			continue
		}
		seen[id] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			end, err := agents.ForceGroup(id, forceSettle)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, agents.ErrGroupSurvived):
				signalled++
				failed = append(failed, fmt.Sprintf("%s: %v", id, err))
			case err != nil:
				failed = append(failed, fmt.Sprintf("%s: %v", id, err))
			case end == agents.GroupEnded:
				signalled++
			case end == agents.GroupGone || end == agents.GroupReplaced:
			default:
				// A leader gone with something of its group maybe left:
				// ForceGroup cannot tie the group back to the registration,
				// so an invocation's survivor is its own hurried
				// termination's, and anything else is said.
				failed = append(failed, fmt.Sprintf("%s: %s, nothing signalled here", id, end))
			}
		}()
	}
	wg.Wait()
	if len(failed) > 0 {
		s.log.Printf("ending by force did not end every group: %s", strings.Join(failed, "; "))
	}
	return signalled
}

// stopDeadline is the instant stop_grace runs out for this stop, fixed the
// first time Shutdown or Close asks: Shutdown forces the stop at it, and
// Close abandons the supervisor loop at it, so the two are one instant with
// one owner rather than two clocks started a moment apart. A later call
// returns the same instant.
func (s *Service) stopDeadline() time.Time {
	s.stopBy.once.Do(func() { s.stopBy.at = time.Now().Add(s.cfg.StopGrace) })
	return s.stopBy.at
}

// closingAt records the step Close is in, for Shutdown to name.
func (s *Service) closingAt(step string) { s.closing.Store(step) }
