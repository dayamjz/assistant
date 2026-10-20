//go:build unix

package journey_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// stopMargin is how long past the bound a stopped service is given to have
// exited. It covers the process's own exit and this harness's reaping, and it
// is under the adapter's own termination grace, agents.DefaultTerminationGrace,
// which is how long a service with no bound of its own waits on an agent that
// ignores the polite signal: a service that waits that out cannot pass.
const stopMargin = 2500 * time.Millisecond

// stopWalk is one stop of a service whose shutdown is held: what the service
// recorded about the agent it was running, and what became of the process and
// of that agent's group once it was signalled.
type stopWalk struct {
	// bound is how long the walk allows: stop_grace for a single signal, and
	// none of it for a second one, which forces at once.
	bound time.Duration
	// groups are the agent process groups the service recorded for the run
	// while the agent ran, read from the run process record.
	groups []int
	// exited is whether the service exited within bound plus stopMargin, and
	// took how long it took to.
	exited bool
	took   time.Duration
	// left are the recorded groups still holding a process once the service
	// had exited.
	left []int
	// log is what the service wrote about its stop.
	log string
}

// heldStop serves a home whose configuration sets stop_grace to grace, starts
// a run whose review agent ignores SIGTERM and holds, and once the agent is
// running sends the service SIGTERM, and a second one after second when that
// is positive. It reports what the walk observed.
func heldStop(t *testing.T, grace time.Duration, second time.Duration) stopWalk {
	t.Helper()
	scenario, path := cloned(t)
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Hang().WithIgnoredTerminate(),
	}}})
	j := open(t, scenario, func(o *journey.Options) { o.Dir = path; o.AgentArguments = agent.Arguments() })
	if err := j.WriteConfiguration(map[string]any{"stop_grace": grace.String()}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	started := make(chan journey.Answer, 1)
	go func() {
		started <- j.CommandBounded(j.Dir(), 2*time.Minute,
			"--skip", pipeline.StagePR.String(), "--intent", "a run whose review agent will not stop politely")
	}()
	groups := recordedGroups(t, j, agent)

	if err := j.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("%v", err)
	}
	signalled := time.Now()
	walk := stopWalk{bound: grace, groups: groups}
	if second > 0 {
		time.Sleep(second)
		if err := j.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("the second SIGTERM: %v", err)
		}
		walk.bound = second
	}
	exited, how, err := j.AwaitExit(walk.bound + stopMargin - time.Since(signalled))
	walk.took = time.Since(signalled)
	if err != nil {
		t.Errorf("%v", err)
	}
	walk.exited = exited
	if !exited {
		// The reaper still holds nothing, so the walk ends the service itself
		// and the clauses report that it had to.
		if err := j.Kill(); err != nil {
			t.Errorf("killing the service that did not stop: %v", err)
		}
	} else {
		t.Logf("the service exited %v after the first SIGTERM: %s", walk.took.Round(time.Millisecond), how)
	}
	walk.left = stillHeld(groups, 2*time.Second)
	walk.log = j.ServiceLog()
	for line := range strings.SplitSeq(walk.log, "\n") {
		if strings.Contains(line, "by force") {
			t.Logf("the service log: %s", line)
		}
	}
	if answer := <-started; answer.Code == machine.ExitOK {
		t.Logf("the run's start answered after the service stopped: %s", answer)
	}
	return walk
}

// recordedGroups waits for the stand-in to have taken the review invocation
// and for the service to have recorded the run's agent group, and returns the
// recorded groups. Both are needed: the call is what says SIGTERM is now
// ignored, and the record is the service's own account of what it started.
func recordedGroups(t *testing.T, j *journey.Journey, agent *standin.Agent) []int {
	t.Helper()
	database, err := j.Database()
	if err != nil {
		t.Fatalf("%v", err)
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if len(agent.Calls()) > 0 {
			if groups := processGroups(t, database); len(groups) > 0 {
				return groups
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the review agent never ran under a recorded process group:\n%s", j.ServiceLog())
	return nil
}

// processGroups reads the run process record out of the home's database.
func processGroups(t *testing.T, database string) []int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	records, err := store.OpenReadOnly(ctx, database)
	if err != nil {
		return nil
	}
	defer func() { _ = records.Close() }()
	held, err := records.ProcessRecords(ctx)
	if err != nil {
		return nil
	}
	var groups []int
	for _, record := range held {
		groups = append(groups, record.Group)
	}
	return groups
}

// stillHeld returns the groups that still hold a process within wait, asked
// by signal zero to the group, whose only answer of absence is ESRCH. A member
// the service's exit orphaned is reaped by whatever adopts it, which is what
// the wait covers.
func stillHeld(groups []int, wait time.Duration) []int {
	deadline := time.Now().Add(wait)
	for {
		var left []int
		for _, group := range groups {
			if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
				left = append(left, group)
			}
		}
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopCheck is the check both walks are held to; forced is how the service
// log must say the stop came to be forced, which differs between the walk the
// grace ran out on and the walk a second signal forced.
func stopCheck(forced string) journey.Check[stopWalk] {
	clauses := []journey.Clause[stopWalk]{
		{
			States: "the service exited within the bound plus the margin",
			Holds: func(w stopWalk) error {
				if !w.exited || w.took > w.bound+stopMargin {
					return fmt.Errorf("the service exited=%v after %v, want an exit within %v", w.exited, w.took, w.bound+stopMargin)
				}
				return nil
			},
		},
		{
			States:  "no process of the agent groups the service registered is left",
			Absence: true,
			Possible: func(w stopWalk) error {
				if len(w.groups) == 0 {
					return errors.New("the service recorded no agent group, so none could have been left")
				}
				return nil
			},
			Holds: func(w stopWalk) error {
				if len(w.left) > 0 {
					return fmt.Errorf("process groups %v still hold a process after the service exited", w.left)
				}
				return nil
			},
		},
		{
			States: "the service log says " + forced + ", names the step still waiting, and that groups were ended by force",
			Holds: func(w stopWalk) error {
				for _, want := range []string{forced, "while the service was still", "ended by force"} {
					if !strings.Contains(w.log, want) {
						return fmt.Errorf("the service log does not say %q:\n%s", want, w.log)
					}
				}
				return nil
			},
		},
	}
	return journey.Check[stopWalk]{
		What:    "P15: a stop whose shutdown is held",
		Clauses: clauses,
		Counterfeits: []journey.Counterfeit[stopWalk]{
			{Named: "the service waited out the agent's own grace", Break: func(w stopWalk) stopWalk {
				w.took = w.bound + 5*time.Second
				return w
			}},
			{Named: "the service never exited and had to be killed", Break: func(w stopWalk) stopWalk {
				w.exited = false
				return w
			}},
			{Named: "the service exited and left its agent running", Break: func(w stopWalk) stopWalk {
				w.left = slices.Clone(w.groups)
				return w
			}},
			{Named: "the service exited without saying what it forced", Break: func(w stopWalk) stopWalk {
				w.log = strings.ReplaceAll(w.log, forced, "")
				return w
			}},
		},
	}
}

// TestAStopWhoseShutdownIsHeldEndsWithinTheGraceThroughTheBinary drives the
// stop half of PRD principle P15 and section 8's "stopping is bounded"
// through the binary: a service serving in the foreground, with stop_grace
// set in the home's own file, is running a run whose review agent ignores
// SIGTERM and would hold for a minute, so the service's own shutdown is held
// by a process group it registered. It is sent SIGTERM.
//
// What it claims, and nothing more: the service exits within stop_grace plus
// stopMargin, no process of the agent groups the run process record names is
// left once it has, and the service log says the groups were ended by force
// and names the part of the shutdown still waiting when the grace ran out.
// The margin is under the adapter's termination grace, so a service that
// waited for the agent on the adapter's terms fails. It does not claim the
// rest of P15: what a restart or a killed service leaves, a worker's, a
// delegation's or a notify command's processes, a descendant that left its
// group, or the kept-work record.
func TestAStopWhoseShutdownIsHeldEndsWithinTheGraceThroughTheBinary(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	walk := heldStop(t, time.Second, 0)
	if err := stopCheck("stop_grace 1s ran out").Verify(walk); err != nil {
		t.Fatal(err)
	}
}

// TestASecondSIGTERMEndsAHeldStopAtOnceThroughTheBinary drives the other half
// of the same rule: a second signal is never swallowed. The walk is the one
// above with the default stop_grace of 30s, so the grace cannot be what ends
// it, and a second SIGTERM half a second after the first. It claims that the
// service exits within stopMargin of the second signal, that no process of
// the recorded agent groups is left, and that the log says the stop was
// forced before the grace ran out. Before #407 the first signal's handler stayed installed
// for the whole shutdown, so the second was taken and nothing happened.
func TestASecondSIGTERMEndsAHeldStopAtOnceThroughTheBinary(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	walk := heldStop(t, 30*time.Second, 500*time.Millisecond)
	if err := stopCheck("the stop was forced before stop_grace 30s ran out").Verify(walk); err != nil {
		t.Fatal(err)
	}
}
