package journey_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// freshSession is what a coordinator session that kept no memory of the
// fleet read from one assistant coordinator catch-up, what it did from that
// read alone, and what the home's records and a second read said after.
type freshSession struct {
	// arm is the supervision verdict the read carried.
	arm supervisor.ArmOutcome
	// wakeIDs are the names of the wakes the read listed, and aboutStopped
	// whether one of them names the stopped task.
	wakeIDs      []string
	aboutStopped bool
	// handoutsBefore and handoutsAfter are the stopped task's wakes' handout
	// counts, read off the records just before and just after the read.
	handoutsBefore, handoutsAfter []int64
	// holdAnswer is the answer command the read listed for the stopped
	// task's hold, and holdDetail that hold's detail.
	holdAnswer []string
	holdDetail string
	// stopped and ready are the states the read listed for the stopped task
	// and for the task whose run passed.
	stopped, ready taskstate.State
	// answered and acknowledged are whether the acts composed from the read
	// exited successfully.
	answered, acknowledged bool
	// holdsAfter counts the holds naming the stopped task in a second read,
	// and wakesAfter the acknowledged names a second read still lists.
	holdsAfter int
	wakesAfter []string
}

// TestAFreshSessionActsFromCatchUpAlone is issue #766's acceptance, driven
// through the binary that ships: a coordinator session reset to nothing runs
// assistant coordinator catch-up once and, from that document alone, acts
// correctly on a queued wake, a task's resolved state and an open hold.
//
// One task's worker is launched for real and stopped through the client,
// which opens a hold on it; a drain made by the session before the reset is
// handed the wake the loop queues about it, and leaves it unacknowledged.
// A second task is attributed to a run that passed, so it resolves Ready
// from the run (P8): nothing about it is owed an act, and a session that
// relaunched or stopped it would be acting on stale memory.
//
// The fresh session then reads catch-up and nothing else: it answers the
// hold with the command the read lists for it, acknowledges every wake by
// the name the read lists, and leaves the ready task alone. What establishes
// that it acted correctly is read off the records and a second read: the
// hold is closed, the wakes are acknowledged, and the read itself handed
// nothing out.
func TestAFreshSessionActsFromCatchUpAlone(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P8)

	scenario, primary := cloned(t)
	agent := standin.New(t, standin.Script{
		Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: workerInstructions, Reply: "on it"}}},
	})
	j := open(t, scenario, func(o *journey.Options) { o.Dir = primary; o.AgentArguments = agent.Arguments() })
	if err := j.WriteConfiguration(map[string]any{"supervision_beacon_interval": "1s"}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	var initialized machine.Init
	if err := succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch)).Decode(&initialized); err != nil {
		t.Fatalf("reading what init answered: %v", err)
	}
	serve(t, j)
	written := records(t, j)

	// The task whose worker is stopped, and the hold the stop opens.
	var created machine.TaskCreated
	if err := succeeds(t, j.Command("coordinator", "create", "--shape", "delivery", "--project", "catchup",
		"--repository", initialized.Repository.ID, "--branch", "task/catchup-stopped",
		"--instructions", workerInstructions)).Decode(&created); err != nil {
		t.Fatalf("reading what create answered: %v", err)
	}
	stoppedID := created.Record.ID
	awaitTask(t, j, stoppedID, func(task machine.Task) bool { return task.State.Source == taskstate.SourceProcess })
	var stopped machine.TaskStopped
	if err := succeeds(t, j.Command("coordinator", "stop", stoppedID)).Decode(&stopped); err != nil {
		t.Fatalf("reading what stop answered: %v", err)
	}
	if !stopped.Hold.Open() {
		t.Fatalf("the stop opened no hold: %+v", stopped)
	}

	// The session before the reset drains, and is handed the wake about the
	// stopped task, which it never acknowledges.
	deadline := time.Now().Add(60 * time.Second)
	for {
		var drained machine.Wakes
		if err := succeeds(t, j.Command("coordinator", "wakes")).Decode(&drained); err != nil {
			t.Fatalf("reading the drain: %v", err)
		}
		if slices.ContainsFunc(drained.Wakes, func(w store.Wake) bool { return w.TaskID == stoppedID }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no wake about task %s was queued:\n%s", stoppedID, j.ServiceLog())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The task whose run passed, which resolves Ready from the run.
	const readyID, readyBranch = "catchupready", "journey/catchup-ready"
	if _, err := written.UpsertRepository(t.Context(), store.Repository{
		ID: "catchup-repo", WorkingPath: t.TempDir(), UpstreamURL: "https://example.test/catchup.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	if _, err := written.CreateTask(t.Context(), store.Task{
		ID: readyID, Shape: store.TaskDelivery, Project: "catchup", Mode: "journey", WorktreePath: j.Dir(),
		RepositoryID: store.Known("catchup-repo"), Branch: store.Known(readyBranch),
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	if _, err := written.BeginFirstTaskAttempt(t.Context(), readyID, store.OnThisMachine()); err != nil {
		t.Fatalf("recording the task's attempt: %v", err)
	}
	if _, err := written.CreateRun(t.Context(), store.Run{
		ID: "catchup-run", RepositoryID: "catchup-repo", Branch: readyBranch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "journey", Revision: "0123456789abcdef", Go: "go"},
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	for _, move := range []struct{ from, to store.RunStatus }{
		{store.RunPending, store.RunRunning},
		{store.RunRunning, store.RunPassed},
	} {
		if _, err := written.TransitionRun(t.Context(), "catchup-run", []store.RunStatus{move.from}, move.to); err != nil {
			t.Fatalf("moving the run to %s: %v", move.to, err)
		}
	}

	handouts := func() []int64 {
		t.Helper()
		wakes, err := written.TaskWakes(t.Context(), stoppedID)
		if err != nil {
			t.Fatalf("reading the stopped task's wakes: %v", err)
		}
		var out []int64
		for _, w := range wakes {
			out = append(out, w.Handouts.Or(0))
		}
		return out
	}

	// The reset: from here on the session knows only what catch-up says.
	var observed freshSession
	observed.handoutsBefore = handouts()
	var read machine.CoordinatorCatchUp
	if err := succeeds(t, j.Command("coordinator", "catch-up")).Decode(&read); err != nil {
		t.Fatalf("reading the catch-up document: %v", err)
	}
	observed.handoutsAfter = handouts()
	observed.arm = read.Supervision.Outcome
	for _, w := range read.Wakes {
		observed.wakeIDs = append(observed.wakeIDs, w.Acknowledge)
		observed.aboutStopped = observed.aboutStopped || w.Task == stoppedID
	}
	for _, task := range read.Tasks {
		switch task.ID {
		case stoppedID:
			observed.stopped = task.State
		case readyID:
			observed.ready = task.State
		}
	}
	for _, h := range read.Holds {
		if h.Task == stoppedID {
			observed.holdAnswer, observed.holdDetail = h.Answer.Args, h.Detail
		}
	}

	// The acts, each composed from the read alone.
	if len(observed.holdAnswer) > 0 {
		observed.answered = j.Command(append(slices.Clone(observed.holdAnswer), machine.AnswerStop)...).Code == machine.ExitOK
	}
	if len(observed.wakeIDs) > 0 {
		observed.acknowledged = j.Command(append([]string{"coordinator", "acknowledge"}, observed.wakeIDs...)...).Code == machine.ExitOK
	}

	var after machine.CoordinatorCatchUp
	if err := succeeds(t, j.Command("coordinator", "catch-up")).Decode(&after); err != nil {
		t.Fatalf("reading the second catch-up document: %v", err)
	}
	for _, h := range after.Holds {
		if h.Task == stoppedID {
			observed.holdsAfter++
		}
	}
	for _, w := range after.Wakes {
		if slices.Contains(observed.wakeIDs, w.Acknowledge) {
			observed.wakesAfter = append(observed.wakesAfter, w.Acknowledge)
		}
	}

	check := journey.Check[freshSession]{
		What: "#766: a fresh coordinator session acts from catch-up alone",
		Clauses: []journey.Clause[freshSession]{
			{States: "the read says the service's watch over the fleet is live", Holds: func(r freshSession) error {
				if r.arm != supervisor.ArmAttached && r.arm != supervisor.ArmStarted {
					return fmt.Errorf("the read's supervision verdict is %q", r.arm)
				}
				return nil
			}},
			{States: "the read lists the queued wake about the stopped task, by a name acknowledge takes", Holds: func(r freshSession) error {
				if !r.aboutStopped || len(r.wakeIDs) == 0 {
					return fmt.Errorf("the read lists wakes %v and none about the stopped task", r.wakeIDs)
				}
				return nil
			}},
			{States: "the read handed nothing out", Holds: func(r freshSession) error {
				if len(r.handoutsBefore) == 0 || !slices.Equal(r.handoutsBefore, r.handoutsAfter) {
					return fmt.Errorf("the stopped task's wakes were handed out %v times before the read and %v after",
						r.handoutsBefore, r.handoutsAfter)
				}
				return nil
			}},
			{States: "the read lists each task's resolved state: the stopped one held, the one whose run passed ready", Holds: func(r freshSession) error {
				if r.stopped != taskstate.Held || r.ready != taskstate.Ready {
					return fmt.Errorf("the read lists the stopped task %q and the passed one %q", r.stopped, r.ready)
				}
				return nil
			}},
			{States: "the read lists the stopped task's hold with the command that answers it and the answers it takes", Holds: func(r freshSession) error {
				if !slices.Equal(r.holdAnswer, machine.TaskAnswering(stoppedID).Args) || !strings.Contains(r.holdDetail, `"`+machine.AnswerStop+`"`) {
					return fmt.Errorf("the read lists the hold's answer as %v with detail %q", r.holdAnswer, r.holdDetail)
				}
				return nil
			}},
			{States: "answering the hold with the listed command and acknowledging the listed names exited successfully", Holds: func(r freshSession) error {
				if !r.answered || !r.acknowledged {
					return fmt.Errorf("the answer exited successfully: %t, the acknowledgement: %t", r.answered, r.acknowledged)
				}
				return nil
			}},
			{States: "a second read lists no hold on the stopped task", Absence: true,
				Possible: func(r freshSession) error {
					if len(r.holdAnswer) == 0 {
						return fmt.Errorf("the first read listed no hold on the stopped task, so there was none to close")
					}
					return nil
				},
				Holds: func(r freshSession) error {
					if r.holdsAfter != 0 {
						return fmt.Errorf("a second read lists %d holds on the stopped task", r.holdsAfter)
					}
					return nil
				}},
			{States: "a second read lists none of the wakes the first listed", Absence: true,
				Possible: func(r freshSession) error {
					if len(r.wakeIDs) == 0 {
						return fmt.Errorf("the first read listed no wake, so there was none to close")
					}
					return nil
				},
				Holds: func(r freshSession) error {
					if len(r.wakesAfter) != 0 {
						return fmt.Errorf("a second read still lists %v", r.wakesAfter)
					}
					return nil
				}},
		},
		Counterfeits: []journey.Counterfeit[freshSession]{
			{Named: "the read said nothing is watching the fleet", Break: func(r freshSession) freshSession {
				r.arm = supervisor.ArmOutcome("failed")
				return r
			}},
			{Named: "the read left out the wake about the stopped task", Break: func(r freshSession) freshSession {
				r.aboutStopped = false
				return r
			}},
			{Named: "the read handed the wakes out", Break: func(r freshSession) freshSession {
				r.handoutsAfter = slices.Clone(r.handoutsAfter)
				r.handoutsAfter[0]++
				return r
			}},
			{Named: "the read listed the ready task as the stale state a session remembers", Break: func(r freshSession) freshSession {
				r.ready = taskstate.Running
				return r
			}},
			{Named: "the read listed no way to answer the hold", Break: func(r freshSession) freshSession {
				r.holdAnswer = nil
				return r
			}},
			{Named: "the answer composed from the read was refused", Break: func(r freshSession) freshSession {
				r.answered = false
				return r
			}},
			{Named: "the hold stayed open after the answer", Break: func(r freshSession) freshSession {
				r.holdsAfter = 1
				return r
			}},
			{Named: "a listed wake stayed queued after the acknowledgement", Break: func(r freshSession) freshSession {
				r.wakesAfter = r.wakeIDs[:1]
				return r
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v\n%s", err, j.ServiceLog())
	}
}
