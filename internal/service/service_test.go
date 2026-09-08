package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// This is what the command surface exists for: a run that a separate
// invocation of the binary can start, report on, answer, and carry on, with
// the process that started it gone in between.
//
// The service is closed and opened again between every step, so nothing about
// where the run stands can be held in this process: the position comes back
// out of the durable checkpoint history, which is what internal/checkpoints
// landed for.
func TestARunSurvivesTheProcessThatStartedItAndIsAnsweredByAnother(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var runID string
	withService(t, h, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		holdingAt(t, run, pendingStage(t, 0))
		runID = run.Record.ID
		if run.Record.Status != store.RunHeld {
			t.Fatalf("a run holding for a decision is recorded as %s", run.Record.Status)
		}
	})
	// The restart is the claim everything below rests on, so it is checked
	// rather than assumed: nothing holds the home, which means the service
	// that started the run closed its database and let the home go.
	homeIsFree(t, h)

	// A second process reports on it, and reports the same decision, without
	// ever having executed anything.
	withService(t, h, func(running serviceUnderTest) {
		var run machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run); err != nil {
			t.Fatalf("reading the run: %v", err)
		}
		holdingAt(t, run, pendingStage(t, 0))
		if run.Steps == 0 {
			t.Fatal("the run reports no steps spent, so its position did not survive")
		}
	})

	homeIsFree(t, h)

	// A third answers it, and the run carries on from where the first left it
	// rather than from the beginning.
	withService(t, h, func(running serviceUnderTest) {
		answered := pendingStage(t, 0)
		run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
		holdingAt(t, run, pendingStage(t, 1))
		if got := stageView(t, run, answered); got.Outcome != pipeline.OutcomeApproved {
			t.Fatalf("the %s stage reports %s after being approved", answered, got.Outcome)
		}
	})
}

// A run answered through to the end completes, and what it ends as is one of
// the outcomes a driving agent is written against.
//
// The run skips the review stage, because that stage's body reads the run's
// isolated copy and this service creates none: it fails on opening it rather
// than holding, so a run that took it could not reach the end however it was
// answered. What the skip costs this test is that one stage, and what it keeps
// is everything after it, which is the part no other test reaches.
func TestARunAnsweredThroughToTheEndCompletes(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		for run.Outcome == machine.OutcomeDecision {
			run = answer(t, running.client, run.Record.ID, string(pipeline.OutcomeApproved))
		}
		if run.Outcome != machine.OutcomeChecksPassed {
			t.Fatalf("a run approved at every stage ends as %s: %s", run.Outcome, run.Reason)
		}
		if run.Record.Status != store.RunPassed {
			t.Fatalf("a completed run is recorded as %s", run.Record.Status)
		}
		if run.Position != "" {
			t.Fatalf("a completed run stands at %q", run.Position)
		}
	})
}

// A run ended from outside its own execution reports that it ended, not the
// decision its last checkpoint still carries. Reading the checkpoint alone
// would offer an answer to a run that is over.
func TestARunEndedWhileHoldingReportsThatItEnded(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		started := startRun(t, running.client, subject)
		holdingAt(t, started, pendingStage(t, 0))

		var ended machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunCancel,
			machine.CancelRequest{Run: started.Record.ID}, &ended); err != nil {
			t.Fatalf("ending the run: %v", err)
		}
		if ended.Record.Status != store.RunTerminated {
			t.Fatalf("the ended run is recorded as %s", ended.Record.Status)
		}
		if ended.Outcome != machine.OutcomeCancelled {
			t.Fatalf("the ended run reports %s, want cancelled", ended.Outcome)
		}
		// Answering it is refused, which is the failure the outcome would
		// otherwise invite a driving agent into.
		var answered machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunRespond,
			machine.RespondRequest{Run: started.Record.ID, Answer: string(pipeline.OutcomeApproved)}, &answered)
		if err == nil {
			t.Fatal("a run that has ended accepted an answer")
		}
	})
}

// Cancelling at a hold ends the run without undoing anything, and says so.
func TestCancellingAtAHoldEndsTheRun(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		run := startRun(t, running.client, subject)
		run = answer(t, running.client, run.Record.ID, string(pipeline.OutcomeCancelled))
		if run.Outcome != machine.OutcomeCancelled {
			t.Fatalf("a cancelled run reports %s", run.Outcome)
		}
		if run.Record.Status != store.RunTerminated {
			t.Fatalf("a cancelled run is recorded as %s", run.Record.Status)
		}
	})
}

// Attaching to a branch that already has a run does not start a second one.
// PRD section 9's bare command attaches to this branch's active run and starts
// one only when there is none.
func TestAttachingTwiceDoesNotStartASecondRun(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		first := startRun(t, running.client, subject)
		second := startRun(t, running.client, subject)
		if first.Record.ID != second.Record.ID {
			t.Fatalf("attaching started a second run: %s then %s", first.Record.ID, second.Record.ID)
		}
		var runs machine.Runs
		if err := running.client.Call(t.Context(), ipc.MethodRunsList, machine.RunsRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &runs); err != nil {
			t.Fatalf("listing runs: %v", err)
		}
		if len(runs.Runs) != 1 {
			t.Fatalf("the repository has %d runs, want 1", len(runs.Runs))
		}
	})
}

// A working copy with no repository record is a working copy nobody has run
// assistant init in, and starting a run there is refused rather than served
// against a repository this service invented.
func TestARunCannotStartInAWorkingCopyWithNoGate(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)

	withService(t, h, func(running serviceUnderTest) {
		var run machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &run)
		if err == nil {
			t.Fatal("a run started in a working copy with no repository record")
		}
	})
}

// Reads are open to a caller the service cannot identify, and a build on a
// platform with no peer credentials serves them while refusing everything that
// drives a run.
func TestReadingIsServedWhoeverIsAsking(t *testing.T) {
	h := newHome(t)
	withService(t, h, func(running serviceUnderTest) {
		var health machine.Health
		if err := running.client.Call(t.Context(), ipc.MethodHealth, nil, &health); err != nil {
			t.Fatalf("asking for readiness: %v", err)
		}
		if !health.Ready {
			t.Fatal("the service answered a readiness check with not ready")
		}
		if health.Home != h.Root() {
			t.Fatalf("the service reports home %s, want %s", health.Home, h.Root())
		}
		var tasks machine.Tasks
		if err := running.client.Call(t.Context(), ipc.MethodTasksList, machine.TaskRequest{}, &tasks); err != nil {
			t.Fatalf("listing tasks: %v", err)
		}
		if len(tasks.Tasks) != 0 {
			t.Fatalf("a fresh home reports %d tasks", len(tasks.Tasks))
		}
	})
}

// The containment guard fires. PRD section 9 contains an agent running inside
// a validation stage: it may inspect, fix, and return its own stage, and
// nothing else.
//
// The relation is the process group, and this registers this test process's
// own, which is exactly what a stage launcher will register for the group it
// starts an agent in. Nothing in this build calls StageStarted, so this is the
// only place the guard is shown to fire at all.
func TestACallerInsideAnActiveStageIsRefused(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		group, err := processGroupOfThisProcess()
		if err != nil {
			t.Fatalf("reading this process's group: %v", err)
		}
		done := running.service.StageStarted("run-under-validation", "review", group)

		var run machine.Run
		err = running.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &run)
		if !errors.Is(err, ipc.ErrContained) {
			t.Fatalf("starting a run from inside a stage = %v, want ErrContained", err)
		}
		// Returning its own stage is what such a caller is there to do, so the
		// open method is still reachable. It refuses for its own reason, which
		// is that nothing is running a stage to return.
		var nothing struct{}
		err = running.client.Call(t.Context(), ipc.MethodStageReport, machine.StageReportRequest{
			Run: "run-under-validation", Stage: "review",
		}, &nothing)
		if errors.Is(err, ipc.ErrContained) {
			t.Fatalf("returning its own stage was refused for containment: %v", err)
		}

		// Once the stage ends, the same caller may drive again.
		done()
		if _, err := startRunErr(t, running.client, subject); err != nil {
			t.Fatalf("starting a run after the stage ended: %v", err)
		}
	})
}

// A stage result cannot be returned when nothing is running a stage. It is the
// honest answer for a build whose stages launch no agent, and it is a refusal
// rather than a record written against a stage that never ran.
func TestAStageResultIsRefusedWhenNoStageIsRunning(t *testing.T) {
	h := newHome(t)
	withService(t, h, func(running serviceUnderTest) {
		var nothing struct{}
		err := running.client.Call(t.Context(), ipc.MethodStageReport, machine.StageReportRequest{
			Run: "any", Stage: "review", Report: "{}",
		}, &nothing)
		if !errors.Is(err, ipc.ErrUnavailable) {
			t.Fatalf("returning a stage result = %v, want ErrUnavailable", err)
		}
	})
}

// Stopping refuses while runs are active, lists them, and takes an explicit
// force. PRD section 9 makes that refusal the default and requires the flag to
// be for this act rather than a general agreement to everything.
func TestStoppingRefusesWhileRunsAreActiveAndForceCarriesIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	running, err := serviceWithClient(t, h)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	run := startRun(t, running.client, subject)

	var refused machine.Lifecycle
	if err := running.client.Call(t.Context(), ipc.MethodServiceStop, machine.LifecycleRequest{}, &refused); err != nil {
		t.Fatalf("asking the service to stop: %v", err)
	}
	if refused.Accepted {
		t.Fatal("the service accepted a stop while a run was active")
	}
	if len(refused.Active) != 1 || refused.Active[0].ID != run.Record.ID {
		t.Fatalf("the refusal lists %v, want the active run %s", refused.Active, run.Record.ID)
	}

	var accepted machine.Lifecycle
	if err := running.client.Call(t.Context(), ipc.MethodServiceStop, machine.LifecycleRequest{Force: true}, &accepted); err != nil {
		t.Fatalf("forcing the stop: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("the forced stop was refused: %s", accepted.Detail)
	}
	// Every invocation, forced or not, is recorded with who called it.
	waitFor(t, func() bool {
		log, err := os.ReadFile(h.ServiceLog())
		return err == nil && strings.Contains(string(log), "stop requested by pid")
	}, "the service log to record who asked it to stop")
}

// Recovery is what a restart owes a run. A record left saying running while
// the checkpoint says the run is waiting for a decision is a run nobody can
// answer, because responding is refused for a run that is not held.
func TestRecoveryReconcilesARecordAgainstItsCheckpoint(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var runID string
	withService(t, h, func(running serviceUnderTest) {
		runID = startRunSkipping(t, running.client, subject, pipeline.StageReview).Record.ID
	})

	// Put the record back where a service that died between the halt and the
	// record would have left it.
	records := openRecords(t, h)
	if _, err := records.TransitionRun(t.Context(), runID, []store.RunStatus{store.RunHeld}, store.RunRunning); err != nil {
		t.Fatalf("moving the run back to running: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	withService(t, h, func(running serviceUnderTest) {
		waitFor(t, func() bool {
			var run machine.Run
			if err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run); err != nil {
				return false
			}
			return run.Record.Status == store.RunHeld
		}, "recovery to put the run back where its checkpoint says it stands")
		// And it is answerable, which is the point of reconciling at all.
		run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
		holdingAt(t, run, pendingStage(t, 1))
	})
}

// A checkpoint history is what a resume reads, so a run reports the same
// position through a service that never executed it.
func TestTheReportedPositionComesFromTheDurableCheckpoint(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var before machine.Run
	withService(t, h, func(running serviceUnderTest) {
		before = startRun(t, running.client, subject)
	})
	homeIsFree(t, h)
	withService(t, h, func(running serviceUnderTest) {
		var after machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: before.Record.ID}, &after); err != nil {
			t.Fatalf("reading the run: %v", err)
		}
		if after.Position != before.Position {
			t.Fatalf("the run stands at %q, want %q", after.Position, before.Position)
		}
		if after.Progress == nil || *after.Progress != graph.StatusHalted {
			t.Fatalf("the run reports progress %v, want halted", after.Progress)
		}
		if after.Steps != before.Steps {
			t.Fatalf("the run has spent %d steps, want %d", after.Steps, before.Steps)
		}
	})
}

// A hold relays the stage's findings in full, unsummarized and unjudged, which
// is what PRD section 9 requires of a finding that needs a decision.
func TestADecisionCarriesTheFindingsVerbatim(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		run := startRun(t, running.client, subject)
		held := pendingStage(t, 0)
		decision := holdingAt(t, run, held)
		if len(decision.Findings) == 0 {
			t.Fatal("the decision carries no findings")
		}
		if len(decision.Options) == 0 {
			t.Fatal("the decision offers no options")
		}
		reported := stageView(t, run, held).Report
		if reported == nil {
			t.Fatal("the stage that held recorded no report")
		}
		if len(reported.Findings) != len(decision.Findings) {
			t.Fatalf("the decision carries %d of the stage's %d findings",
				len(decision.Findings), len(reported.Findings))
		}
		for i, finding := range decision.Findings {
			if finding.Description != reported.Findings[i].Description {
				t.Fatalf("finding %d was rewritten on the way to the decision", i)
			}
		}
	})
}

// waitFor polls until a condition holds, and fails saying what it was waiting
// for. Recovery happens on a goroutine of its own so that a home with several
// interrupted runs serves immediately, which is why this is a poll.
func waitFor(t *testing.T, holds func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if holds() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A run that was recorded and never executed has no position to carry on
// from, and attaching to it says so rather than resuming something that is not
// there. The run's inputs are on its record, but the stages it was told to
// skip are not, so starting it again from the record would silently drop that
// choice.
//
// It has not failed, and the answer may not say it has. Nothing about this run
// went wrong: it was recorded and nothing walked it, which is a run whose
// execution has not finished rather than one that ended without a verdict. The
// same read a moment after a start would find the same shape from a run a
// segment is inside, and calling either failed is the surface asserting the
// opposite of what is true. What tells them apart is whether anything is
// advancing the run, and it is the action rather than the outcome that differs.
func TestARunThatNeverExecutedIsReportedRatherThanResumed(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)

	// A record with no checkpoint is what a service that died between
	// recording a run and walking its first node leaves behind.
	records := openRecords(t, h)
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	created, err := records.CreateRun(t.Context(), store.Run{
		ID:            "never-executed",
		RepositoryID:  repository.ID,
		Branch:        "main",
		SubmittedHead: "0000000000000000000000000000000000000000",
		Intent:        "recorded and then nothing",
		IntentSource:  "supplied",
		Build:         build,
		ConfigDigest:  "digest",
	})
	if err != nil {
		t.Fatalf("recording a run: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	withService(t, h, func(running serviceUnderTest) {
		attached := startRun(t, running.client, subject)
		if attached.Record.ID != created.ID {
			t.Fatalf("attaching started a new run %s rather than reporting %s", attached.Record.ID, created.ID)
		}
		if attached.Progress != nil {
			t.Fatalf("a run that never executed reports progress %v", attached.Progress)
		}
		if attached.Outcome == machine.OutcomeFailed {
			t.Fatal("a run that was recorded and never walked reports that it failed")
		}
		if attached.Outcome != machine.OutcomeExecuting {
			t.Fatalf("a run that never executed reports %s, want executing", attached.Outcome)
		}
		if attached.Advancing {
			t.Fatal("a run nothing has started reports that a segment is inside it")
		}
		if attached.NextAction() == "" {
			t.Fatal("a run that cannot be carried on says nothing about what to do")
		}
		if attached.NextAction() == machine.OutcomeExecuting.NextActionFor(true) {
			t.Fatalf("a run nothing is advancing is told to wait: %s", attached.NextAction())
		}
		if attached.NextAction() == machine.OutcomeExecuting.NextActionFor(false) {
			t.Fatalf("a run with no position is told to attach and carry it on: %s", attached.NextAction())
		}
	})
}
