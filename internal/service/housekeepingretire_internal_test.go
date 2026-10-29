package service

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// passBranch is the branch every promoted pass in these tests works on.
const hkBranch = "housekeeping/agent-context"

// promotePass flips a pass to delivery in place on hkBranch, as the
// coordinator's promotion does, anchored to its history.
func promotePass(t *testing.T, records *store.Store, id string) {
	t.Helper()
	ctx := t.Context()
	events, err := records.TaskEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.TransitionTaskShape(ctx, id, store.TaskInvestigation, store.TaskDelivery, hkRepository, hkBranch,
		config.DeliveryGated, "Correct the drift the report found.", events[len(events)-1].Sequence, "promoted", store.ResolvedByMachineInterface(), nil); err != nil {
		t.Fatalf("promoting pass %s: %v", id, err)
	}
}

// passRun records a run of the pass's branch, after its attempt began, in
// status. A passed run is driven through the real pipeline over the durable
// checkpoint store first, with a rebase stage that writes the empty-diff
// short circuit when nothingLeft is set, so a run that rebased to nothing
// is the shape the executor leaves and not one stated here.
func passRun(t *testing.T, records *store.Store, id string, status store.RunStatus, nothingLeft bool) store.Run {
	t.Helper()
	ctx := t.Context()
	time.Sleep(25 * time.Millisecond)
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	run, err := records.CreateRun(ctx, store.Run{ID: id, RepositoryID: hkRepository, Branch: hkBranch, SubmittedHead: "aaaa",
		Base: "main", Intent: "correct the drift", IntentSource: "task", ConfigDigest: "cfg", Build: build})
	if err != nil {
		t.Fatalf("recording run %s: %v", id, err)
	}
	if status == store.RunPending {
		return run
	}
	if status == store.RunPassed || nothingLeft {
		var all pipeline.Stages
		for _, stage := range pipeline.Order() {
			*all.At(stage) = writingStage(stage, nil)
		}
		if nothingLeft {
			*all.At(pipeline.StageRebase) = writingStage(pipeline.StageRebase,
				map[pipeline.Key]graph.Value{pipeline.KeyDiffEmpty: graph.BoolValue(true)})
		}
		p, err := pipeline.New(pipeline.Options{Stages: all, Budget: 40})
		if err != nil {
			t.Fatal(err)
		}
		initial, err := p.NewState(pipeline.Start{Repository: hkRepository, Run: id, Branch: hkBranch, Base: "main",
			Submitted: "aaaa", HousekeepingPass: true})
		if err != nil {
			t.Fatal(err)
		}
		executor, err := p.Executor(checkpoints.New(records))
		if err != nil {
			t.Fatal(err)
		}
		if result, err := executor.Run(ctx, id, initial); err != nil || result.Status != graph.StatusCompleted {
			t.Fatalf("run %s ended %s (%v), want completed", id, result.Status, err)
		}
	}
	if run, err = records.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if status == store.RunRunning {
		return run
	}
	if run, err = records.TransitionRun(ctx, id, []store.RunStatus{store.RunRunning}, status); err != nil {
		t.Fatal(err)
	}
	return run
}

// promotedPass records a pass, promotes it and launches its first attempt
// on session ref.
func promotedPass(t *testing.T, records *store.Store, id, ref string) {
	t.Helper()
	passTask(t, records, id)
	promotePass(t, records, id)
	launched(t, records, id, "", ref)
}

// advance stands run in this service's slot as a segment advancing it, which
// is the fact the reader's Advancing reads, as a run walking a stage is.
func (s *hkSubject) advance(t *testing.T, run string) {
	t.Helper()
	s.service.mu.Lock()
	defer s.service.mu.Unlock()
	s.service.slotMaps()
	s.service.advancing[run] = &slot{cancel: func() {}}
	t.Cleanup(func() {
		s.service.mu.Lock()
		defer s.service.mu.Unlock()
		delete(s.service.advancing, run)
	})
}

// acknowledgeTask acknowledges every queued wake of the task, as a
// coordinator that read them does.
func (s *hkSubject) acknowledgeTask(t *testing.T, id string) {
	t.Helper()
	acknowledgeAll(t, s.records(t), id)
}

// PRD section 13: a pass whose worker screen stays unchanged while its run
// sits in a stage surfaces once escalation_window elapses, long before
// housekeeping.interval, including a promoted pass that resolves Held from
// an open report decision with every wake acknowledged: the stage is
// positive evidence under P9 only until the window, and the pass is in
// flight whatever it resolves, so its terminal is read. The positive control
// is a pass nothing moves, resolving Held with no wake queued, whose
// terminal is left unread, as any Held task's is.
func TestAPassUnchangedInAStageSurfacesAtTheEscalationWindow(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9, principles.P15)
	const pass, still = "taskhkstagea", "taskhkstillb"
	sub := openHousekeeping(t, housekeepingOff, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-stage")
		sub.terminals.live("pane-stage", "running the checks\n")
		passRun(t, records, "run-stage", store.RunRunning, false)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: tasks.HoldKey(pass, "f1"), TaskID: store.Known(pass),
			Subject: "a report decision", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	sub.advance(t, "run-stage")
	sub.pass(t)
	sub.acknowledgeTask(t, pass)
	if got := sub.observedTask(t, pass); got.State != taskstate.Held || !got.Progress {
		t.Fatalf("the pass resolves %+v, want Held from its decision on the evidence of its run's stage", got)
	}
	first := detectionsOf(sub.pass(t), pass, supervisor.KindTerminalUnchanged)
	if len(first) != 1 || first[0].Verdict.Surfaces() {
		t.Fatalf("within the window the unchanged screen read %+v, want it absorbed on the stage's evidence", first)
	}
	sub.clock.Advance(time.Minute)
	elapsed := detectionsOf(sub.pass(t), pass, supervisor.KindTerminalUnchanged)
	if len(elapsed) != 1 || !elapsed[0].Verdict.Surfaces() || elapsed[0].Disposition != store.WakeAppended {
		t.Fatalf("once the window elapsed the unchanged screen read %+v, want it surfaced", elapsed)
	}

	control := openHousekeeping(t, housekeepingOff, func(t *testing.T, records *store.Store, sub *hkSubject) {
		passTask(t, records, still)
		launched(t, records, still, "", "pane-still")
		sub.terminals.dead("pane-still")
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: tasks.HoldKey(still, "f1"), TaskID: store.Known(still),
			Subject: "a report decision", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	control.pass(t)
	control.acknowledgeTask(t, still)
	for len(control.terminals.calls) > 0 {
		<-control.terminals.calls
	}
	r := control.pass(t)
	if r.Tasks[still].Watched {
		t.Fatalf("a held pass nothing moves was watched: %+v", r.Tasks[still])
	}
	if gone := detectionsOf(r, still, supervisor.KindTerminalGone); len(gone) != 0 {
		t.Fatalf("a held pass nothing moves had its terminal read: %+v", gone)
	}
	if n := len(control.terminals.calls); n != 0 {
		t.Fatalf("a held pass nothing moves had its screen captured %d time(s)", n)
	}
}
