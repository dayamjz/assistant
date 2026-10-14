package journey_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// taskWalk is what one task did from creation to the run its worker's push
// started, read off the binary's answers, the stand-in's own record of the
// worker it was started as, the home's records, and the repositories with git.
type taskWalk struct {
	// branch is the branch the task was created on.
	branch string
	// created is what task.create answered.
	created machine.TaskCreated
	// copyPath is the task's copy as internal/home lays it out, and primary
	// the working copy the gate was created for.
	copyPath, primary string
	// workers are the stand-in processes started as a worker.
	workers []standin.Call
	// workerInCopy is whether the worker's working directory is the task's
	// copy, by file identity.
	workerInCopy bool
	// ownObjects is whether the copy's common git directory is its own
	// .git, and copyIsPrimary whether its top level is the primary's.
	ownObjects, copyIsPrimary bool
	// remotes are the copy's remotes, and remoteIsGate whether the one named
	// for the gate reaches the gate this home created, by file identity.
	remotes      []string
	remoteIsGate bool
	// running is the task as the binary answered it with the worker live and
	// before any push, and runningTail the newest event its history held then.
	running     machine.Task
	runningTail string
	// started is the commit the copy stood at before the worker's commit.
	started string
	// pushed is the commit made in the copy, admitted whether the gate
	// admitted the push made from the worker's process group, and pushOutput
	// what git said.
	pushed     string
	admitted   bool
	pushOutput string
	// strayRefused is whether the same group's push of the same commit to a
	// branch that is not the task's was refused, and strayOutput what git
	// said; strayLanded whether that other branch exists in the gate after.
	strayRefused bool
	strayOutput  string
	strayLanded  bool
	// gateBranch is where the gate's branch stood after the push.
	gateBranch string
	// primaryHolds is whether the primary checkout holds the pushed commit.
	primaryHolds bool
	// runs are the repository's runs after the push, and run the one the
	// push started as the binary answered it once it stopped executing.
	runs []store.Run
	run  machine.Run
	// after is the task as the binary answered it once the run had stopped.
	after machine.Task
	// reviews counts review invocations the script answered, and unscripted
	// those it did not.
	reviews, unscripted int
}

// clonedRuns copies w far enough that a counterfeit can change the run list
// without writing through the slice the real observation holds.
func (w taskWalk) clonedRuns() taskWalk {
	w.runs = slices.Clone(w.runs)
	return w
}

// workerInstructions are what the task is created with. The stand-in records
// every line its worker reads, so the token is how the walk tells the
// instructions arrived at the worker rather than some other text.
const workerInstructions = "journey task walk: commit one file on this branch and push it to the assistant remote."

// TestATaskCreatedThroughTheServiceIsWorkedAndItsPushStartsARun drives one
// delivery task from creation to a validation run, through the binary: the
// task is created with task.create on the socket the binary serves, the
// service dispatches it into its copy under the home and launches its worker
// through the Claude Code adapter's stream mode, the worker's push from that
// copy to the gate starts a run of the pushed commit, and the task is read
// off the binary as Running before the push and as the run's state after it.
//
// The worker is internal/agents/standin answering the Claude Code adapter's
// stdin stream-json mode, never a real Claude Code CLI, so this establishes
// the program's wiring around a worker and not that a real CLI answers in
// that mode; internal/agents' conformance probe is what that rests on. The
// stand-in prints and runs nothing, so the commit and the push are made by
// this test with git started inside the process group the service started
// the worker in. internal/service places a caller in a worker group by that
// group alone, read from the caller's credentials, so the push is the
// worker's to the product; it is not an edit the stand-in made.
//
// task.create has no verb: PRD section 9 names it a service method beside the
// command table, so it is called over the binary's socket from this process,
// which sits in no worker group and so is the coordinator by that method's
// rule. The run the push starts takes its review stage with the stand-in
// scripted to answer clean, and stops at the first hold its configuration
// gives; which stage that is is not asserted.
//
// P8 is cited for the reads: before the push the task answers Running from
// its terminal while the newest event in its history claims another state,
// and after it the run's state from the run while the terminal is still live.
// P11 is cited for its positive half only: the worker started in a clone of
// its own, distinct from the primary checkout, whose one remote is the gate.
// The refusal half - a copy that is not isolated is not launched - is held at
// package reach by internal/service's and internal/tasks' own tests, and
// nothing here drives it.
func TestATaskCreatedThroughTheServiceIsWorkedAndItsPushStartsARun(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P11)

	scenario, primary := cloned(t)
	const reviewStep = 0
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{
			Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
		}},
		Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: workerInstructions, Reply: "on it"}}},
	})
	j := open(t, scenario, func(o *journey.Options) { o.Dir = primary; o.AgentArguments = agent.Arguments() })
	var initialized machine.Init
	if err := succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch)).Decode(&initialized); err != nil {
		t.Fatalf("reading what init answered: %v", err)
	}
	serve(t, j)

	observed := taskWalk{branch: "task/journey-walk", primary: primary}
	opened, err := home.Open(j.Root())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	client, err := ipc.Dial(t.Context(), opened.Socket(), ipc.ClientConfig{})
	if err != nil {
		t.Fatalf("dialling the service: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Call(t.Context(), ipc.MethodTaskCreate, machine.TaskCreateRequest{
		Shape: string(store.TaskDelivery), Project: "journey", Instructions: workerInstructions,
		Mode: "journey", Repository: initialized.Repository.ID, Branch: observed.branch,
	}, &observed.created); err != nil {
		t.Fatalf("creating the task: %v\n%s", err, j.ServiceLog())
	}
	id := observed.created.Record.ID
	if observed.copyPath, err = opened.TaskCopy(id); err != nil {
		t.Fatalf("locating the task's copy: %v", err)
	}

	// Running, read off the binary, which is the launch having happened.
	observed.running = awaitTask(t, j, id, func(task machine.Task) bool {
		return task.State.Source == taskstate.SourceProcess
	})
	written := records(t, j)
	history, err := written.TaskEvents(t.Context(), id, 0, 0)
	if err != nil || len(history) == 0 {
		t.Fatalf("reading the task's history: %v (%d events)", err, len(history))
	}
	observed.runningTail = history[len(history)-1].Kind
	attempts, err := written.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("reading the task's attempts: %v (%d)", err, len(attempts))
	}
	recorded, known := attempts[len(attempts)-1].SessionRef.Get()
	if !known {
		t.Fatalf("the task reads %s from its terminal and its latest attempt names no session", observed.running.State.State)
	}
	reference, err := session.ParseReference(recorded)
	if err != nil {
		t.Fatalf("reading the attempt's session reference: %v", err)
	}
	awaitWorkerLine(t, agent)

	for _, call := range agent.Calls() {
		if call.Worker {
			observed.workers = append(observed.workers, call)
		}
	}
	if len(observed.workers) == 1 {
		observed.workerInCopy = sameFile(t, observed.workers[0].Dir, observed.copyPath)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := journey.Git(scenario, dir, args...)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return out
	}
	common := git(observed.copyPath, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(observed.copyPath, common)
	}
	observed.ownObjects = sameFile(t, common, filepath.Join(observed.copyPath, ".git"))
	observed.copyIsPrimary = sameFile(t, git(observed.copyPath, "rev-parse", "--show-toplevel"), primary)
	observed.remotes = strings.Fields(git(observed.copyPath, "remote"))
	if slices.Contains(observed.remotes, gateRemote) {
		observed.remoteIsGate = sameFile(t, git(observed.copyPath, "remote", "get-url", gateRemote), initialized.Gate.Repository)
	}

	// The worker's commit and push, from its copy and inside its group.
	if err := writeInto(observed.copyPath, "task-walk.md", "A change made in the task's copy.\n"); err != nil {
		t.Fatalf("%v", err)
	}
	observed.started = git(observed.copyPath, "rev-parse", "HEAD")
	gitAsWorker(t, scenario, reference.PID, observed.copyPath, "add", "task-walk.md")
	gitAsWorker(t, scenario, reference.PID, observed.copyPath, "commit", "--quiet", "-m", "the task's change")
	observed.pushed = git(observed.copyPath, "rev-parse", "HEAD")
	const stray = "journey-not-the-task"
	observed.strayOutput, err = tryGitAsWorker(t, scenario, reference.PID, observed.copyPath,
		"push", gateRemote, "HEAD:refs/heads/"+stray)
	observed.strayRefused = err != nil
	_, landed := journey.Git(scenario, initialized.Gate.Repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+stray)
	observed.strayLanded = landed == nil
	observed.pushOutput, err = tryGitAsWorker(t, scenario, reference.PID, observed.copyPath, "push", gateRemote, observed.branch)
	observed.admitted = err == nil
	observed.gateBranch, _ = journey.Git(scenario, initialized.Gate.Repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+observed.branch)
	_, holds := journey.Git(scenario, primary, "cat-file", "-e", observed.pushed+"^{commit}")
	observed.primaryHolds = holds == nil

	var listed machine.Runs
	if err := succeeds(t, j.Command("runs")).Decode(&listed); err != nil {
		t.Fatalf("listing the runs: %v", err)
	}
	observed.runs = listed.Runs
	if len(observed.runs) > 0 {
		observed.run = awaitRunStopped(t, j, observed.runs[0].ID)
		observed.after = awaitTask(t, j, id, func(task machine.Task) bool {
			return task.State.Source == taskstate.SourceRun && task.State.State != taskstate.Validating
		})
	}
	for _, call := range agent.Calls() {
		switch {
		case call.Worker:
		case call.Step == reviewStep:
			observed.reviews++
		case !call.Answered():
			observed.unscripted++
		}
	}
	t.Logf("the task's walk: created %s on %s, worker group %d, pushed %s, run %s ended %s at %s, task read %s from %s",
		id, observed.branch, reference.PID, observed.pushed, observed.run.Record.ID, observed.run.Outcome,
		observed.run.Record.Status, observed.after.State.State, observed.after.State.Source)

	check := journey.Check[taskWalk]{
		What: "P8, P11: one task from task.create to the run its push started",
		Clauses: []journey.Clause[taskWalk]{
			{
				States: "task.create recorded a delivery task on the branch asked, at the copy the home lays out for it",
				Holds: func(w taskWalk) error {
					r := w.created.Record
					if r.Shape != store.TaskDelivery || r.Branch.Or("") != w.branch {
						return fmt.Errorf("task.create recorded a %s task on %q, wanted a delivery task on %q",
							r.Shape, r.Branch.Or(""), w.branch)
					}
					if r.WorktreePath != w.copyPath {
						return fmt.Errorf("the task's record names the copy %q and the home lays it out at %q",
							r.WorktreePath, w.copyPath)
					}
					return nil
				},
			},
			{
				States: "exactly one worker started, through the Claude Code adapter's stream mode, and read the instructions",
				Holds: func(w taskWalk) error {
					if len(w.workers) != 1 {
						return fmt.Errorf("%d worker(s) started, wanted one", len(w.workers))
					}
					worker := w.workers[0]
					if !worker.Stream {
						return errors.New("the worker was not started in the stdin stream-json mode the Claude Code adapter starts one in")
					}
					if len(worker.Lines) == 0 || !strings.Contains(worker.Lines[0], workerInstructions) {
						return fmt.Errorf("the worker's first line was not the task's instructions: %q", worker.Lines)
					}
					return nil
				},
			},
			{
				States: "the worker started in the task's copy",
				Holds: func(w taskWalk) error {
					if !w.workerInCopy {
						return errors.New("the worker's working directory is not the task's copy")
					}
					return nil
				},
			},
			{
				States: "the copy is a clone with its own object store, distinct from the primary checkout",
				Holds: func(w taskWalk) error {
					if !w.ownObjects {
						return errors.New("the copy's common git directory is not its own, so it shares a repository")
					}
					if w.copyIsPrimary {
						return errors.New("the copy's top level is the primary checkout")
					}
					return nil
				},
			},
			{
				States: "the copy's one remote is the gate this home created",
				Holds: func(w taskWalk) error {
					if !slices.Equal(w.remotes, []string{gateRemote}) || !w.remoteIsGate {
						return fmt.Errorf("the copy's remotes are %v, and %s reaching the gate is %v",
							w.remotes, gateRemote, w.remoteIsGate)
					}
					return nil
				},
			},
			{
				States: "before the push the task read Running from a live terminal, as positive evidence",
				Holds: func(w taskWalk) error {
					s := w.running.State
					if s.State != taskstate.Running || s.Source != taskstate.SourceProcess ||
						s.Basis.Process != taskstate.Live || !s.Progress {
						return fmt.Errorf("the task read %s from %s with the terminal %s and progress %v",
							s.State, s.Source, s.Basis.Process, s.Progress)
					}
					return nil
				},
			},
			{
				States: "that newest event in the task's history claimed no Running of its own",
				Holds: func(w taskWalk) error {
					if row, ok := taskstate.Lookup(w.runningTail); ok && row.State == taskstate.Running {
						return fmt.Errorf("the newest event, %q, claims Running itself, so the read could have been the log's", w.runningTail)
					}
					return nil
				},
			},
			{
				States: "the push from the worker's group was admitted and moved the gate's branch to the commit",
				Holds: func(w taskWalk) error {
					if !w.admitted {
						return fmt.Errorf("the worker's push was refused:\n%s", w.pushOutput)
					}
					if w.gateBranch != w.pushed {
						return fmt.Errorf("the gate's %s stands at %q and the worker pushed %s", w.branch, w.gateBranch, w.pushed)
					}
					return nil
				},
			},
			{
				States: "the same group's push to a branch that is not the task's was refused, so the pusher was placed as the worker",
				Holds: func(w taskWalk) error {
					if !w.strayRefused || w.strayLanded {
						return fmt.Errorf("a push from the worker's group outside its branch was refused=%v and landed=%v:\n%s",
							w.strayRefused, w.strayLanded, w.strayOutput)
					}
					if !strings.Contains(w.strayOutput, "is not that branch") {
						return fmt.Errorf("the push outside the branch was refused without naming the worker's rule:\n%s", w.strayOutput)
					}
					return nil
				},
			},
			{
				States:  "the primary checkout does not hold the commit made in the copy",
				Absence: true,
				Possible: func(w taskWalk) error {
					if w.gateBranch != w.pushed {
						return errors.New("the commit reached nowhere outside the copy, so no repository could have held it")
					}
					return nil
				},
				Holds: func(w taskWalk) error {
					if w.primaryHolds {
						return fmt.Errorf("the primary checkout holds %s", w.pushed)
					}
					return nil
				},
			},
			{
				States: "exactly one run exists, of the task's branch at the pushed commit",
				Holds: func(w taskWalk) error {
					if len(w.runs) != 1 {
						return fmt.Errorf("the repository has %d run(s), wanted the one the push started", len(w.runs))
					}
					if r := w.runs[0]; r.Branch != w.branch || r.SubmittedHead != w.pushed {
						return fmt.Errorf("the run is of %s at %s, and the worker pushed %s at %s",
							r.Branch, r.SubmittedHead, w.branch, w.pushed)
					}
					return nil
				},
			},
			{
				States: "after the run stopped the task read the run's state from that run",
				Holds: func(w taskWalk) error {
					s := w.after.State
					if s.Source != taskstate.SourceRun || s.Basis.RunID == "" || s.Basis.RunID != w.run.Record.ID {
						return fmt.Errorf("the task read %s from %s naming run %q, and the push started %q",
							s.State, s.Source, s.Basis.RunID, w.run.Record.ID)
					}
					if want := runState(w.run.Record.Status); s.State != want {
						return fmt.Errorf("the task read %s and its run is %s, which reads %s", s.State, w.run.Record.Status, want)
					}
					return nil
				},
			},
			{
				States: "the run outranked a terminal that was still live",
				Holds: func(w taskWalk) error {
					if w.after.State.Basis.Process != taskstate.Live {
						return fmt.Errorf("the terminal read %s at the second read, so a live one was not outranked",
							w.after.State.Basis.Process)
					}
					return nil
				},
			},
			{
				States: "the run's review launched the scripted reviewer exactly once",
				Holds: func(w taskWalk) error {
					if w.reviews != 1 {
						return fmt.Errorf("the scripted reviewer answered %d review(s), wanted one", w.reviews)
					}
					return nil
				},
			},
			{
				States:  "no agent invocation went unscripted",
				Absence: true,
				Possible: func(w taskWalk) error {
					if w.reviews == 0 {
						return errors.New("no reviewer was launched at all, so none could have gone unscripted")
					}
					return nil
				},
				Holds: func(w taskWalk) error {
					if w.unscripted != 0 {
						return fmt.Errorf("%d agent invocation(s) matched nothing in the script", w.unscripted)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[taskWalk]{
			{Named: "the task was recorded as an investigation", Break: func(w taskWalk) taskWalk {
				w.created.Record.Shape = store.TaskInvestigation
				return w
			}},
			{Named: "the task's record names a copy outside the home's layout", Break: func(w taskWalk) taskWalk {
				w.created.Record.WorktreePath = w.primary
				return w
			}},
			{Named: "no worker started", Break: func(w taskWalk) taskWalk {
				w.workers = nil
				return w
			}},
			{Named: "the worker was started in line mode", Break: func(w taskWalk) taskWalk {
				w.workers = slices.Clone(w.workers)
				for i := range w.workers {
					w.workers[i].Stream = false
				}
				return w
			}},
			{Named: "the worker started in the primary checkout", Break: func(w taskWalk) taskWalk {
				w.workerInCopy = false
				return w
			}},
			{Named: "the copy is a linked worktree of the primary", Break: func(w taskWalk) taskWalk {
				w.ownObjects = false
				return w
			}},
			{Named: "the copy is the primary checkout", Break: func(w taskWalk) taskWalk {
				w.copyIsPrimary = true
				return w
			}},
			{Named: "the copy kept the primary's origin beside the gate", Break: func(w taskWalk) taskWalk {
				w.remotes = append(slices.Clone(w.remotes), "origin")
				return w
			}},
			{Named: "the task read Dispatched from its history", Break: func(w taskWalk) taskWalk {
				w.running.State.State, w.running.State.Source = taskstate.Dispatched, taskstate.SourceEvent
				w.running.State.Progress = false
				return w
			}},
			{Named: "the newest event was the worker saying it had started", Break: func(w taskWalk) taskWalk {
				w.runningTail = "started"
				return w
			}},
			{Named: "the worker's push was refused", Break: func(w taskWalk) taskWalk {
				w.admitted = false
				w.gateBranch = ""
				return w
			}},
			{Named: "the push was a person's, admitted to any branch", Break: func(w taskWalk) taskWalk {
				w.strayRefused, w.strayLanded, w.strayOutput = false, true, ""
				return w
			}},
			{Named: "the push outside the branch was refused for some other reason", Break: func(w taskWalk) taskWalk {
				w.strayOutput = "fatal: could not read from remote repository"
				return w
			}},
			{Named: "the primary checkout came to hold the commit", Break: func(w taskWalk) taskWalk {
				w.primaryHolds = true
				return w
			}},
			{Named: "the push started no run", Break: func(w taskWalk) taskWalk {
				w = w.clonedRuns()
				w.runs = nil
				return w
			}},
			{Named: "the run was of another commit", Break: func(w taskWalk) taskWalk {
				w = w.clonedRuns()
				for i := range w.runs {
					w.runs[i].SubmittedHead = w.started
				}
				return w
			}},
			{Named: "the task went on reading Running from its terminal", Break: func(w taskWalk) taskWalk {
				w.after.State.State, w.after.State.Source = taskstate.Running, taskstate.SourceProcess
				w.after.State.Basis.RunID = ""
				return w
			}},
			{Named: "the task read a state its run is not in", Break: func(w taskWalk) taskWalk {
				w.after.State.State = taskstate.Ready
				return w
			}},
			{Named: "the worker had exited before the second read", Break: func(w taskWalk) taskWalk {
				w.after.State.Basis.Process = taskstate.Dead
				return w
			}},
			{Named: "the run passed its review without asking a reviewer", Break: func(w taskWalk) taskWalk {
				w.reviews = 0
				return w
			}},
			{Named: "an agent was launched that nothing scripted", Break: func(w taskWalk) taskWalk {
				w.unscripted = 1
				return w
			}},
		},
	}
	if err := check.Verify(observed); err != nil {
		t.Fatalf("%v\nservice log:\n%s", err, j.ServiceLog())
	}
}

// runState is what internal/taskstate's run tier reads a run of this status
// as, restated from Resolve's contract so the comparison is not the package
// under validation agreeing with itself.
func runState(status store.RunStatus) taskstate.State {
	switch status {
	case store.RunPending, store.RunRunning:
		return taskstate.Validating
	case store.RunHeld:
		return taskstate.Held
	case store.RunPassed:
		return taskstate.Ready
	case store.RunFailed, store.RunTerminated:
		return taskstate.Failed
	}
	return taskstate.Unknown
}

// awaitTask reads the task off the binary until done accepts it, and fails
// after a bound naming what it last read.
func awaitTask(t *testing.T, j *journey.Journey, id string, done func(machine.Task) bool) machine.Task {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var task machine.Task
		if err := succeeds(t, j.Command("tasks", id)).Decode(&task); err != nil {
			t.Fatalf("reading the task off the surface: %v", err)
		}
		if done(task) {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task still reads %s from %s:\n%s", task.State.State, task.State.Source, j.ServiceLog())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitRunStopped reads the run off the binary until it is no longer
// executing, and fails after a bound.
func awaitRunStopped(t *testing.T, j *journey.Journey, id string) machine.Run {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		run := decodeRun(t, j.Command("runs", id))
		if run.Outcome != machine.OutcomeExecuting {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is still executing:\n%s", id, j.ServiceLog())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitWorkerLine waits until the stand-in's worker has recorded the first
// line it read, so the observation of what it read is not a race with its
// start, and fails after a bound.
func awaitWorkerLine(t *testing.T, agent *standin.Agent) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		for _, call := range agent.Calls() {
			if call.Worker && len(call.Lines) > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no worker recorded a line it read: %v", agent.Calls())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sameFile reports whether two paths name one file, by identity, and false
// when either cannot be read.
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// gitAsWorker runs git inside the worker's process group, under the
// isolation the scenario was built with, and fails the test when it fails.
func gitAsWorker(t *testing.T, scenario fixture.Scenario, group int, dir string, args ...string) string {
	t.Helper()
	out, err := tryGitAsWorker(t, scenario, group, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s as the worker: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// tryGitAsWorker is gitAsWorker returning the failure rather than failing,
// for a push whose admission is what is observed.
func tryGitAsWorker(t *testing.T, scenario fixture.Scenario, group int, dir string, args ...string) (string, error) {
	t.Helper()
	binary, env, err := scenario.GitInvocation()
	if err != nil {
		t.Fatalf("%v", err)
	}
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	joinGroup(cmd, group)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
