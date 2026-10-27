package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
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

// retired is the pass's retirement record, and false while it has none.
func (s *hkSubject) retirement(t *testing.T, id string) (store.HousekeepingRetirement, bool) {
	t.Helper()
	r, found, err := s.records(t).RetirementOf(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r, found
}

// retireHolds is the pass's retirement holds, open or answered.
func (s *hkSubject) retireHolds(t *testing.T, id string) []store.Hold {
	t.Helper()
	snap, err := s.records(t).TaskSnapshot(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Hold
	for _, h := range snap.Holds {
		if _, ok := parseRetireHold(h.Key, id); ok {
			out = append(out, h)
		}
	}
	return out
}

// createPass is the coordinator's marked create of the repository's next
// pass, through intake under the service's own trusted read.
func (s *hkSubject) createPass(t *testing.T) error {
	t.Helper()
	_, err := s.service.queue.Create(t.Context(), tasks.Request{Project: "one", Repository: hkRepository, Housekeeping: true})
	return err
}

// answer answers the pass's hold through the hold answer, as the coordinator
// would with assistant --task --answer.
func (s *hkSubject) answer(t *testing.T, id, hold, answer string) (machine.TaskAnswered, error) {
	t.Helper()
	return s.service.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: id, Hold: hold, Answer: answer})
}

// PRD section 13: a pass whose run was cancelled stays open until a
// retirement hold is answered retire, and keep leaves it open; the
// answered retirement releases the claim so a later pass can be created.
// Each hold is registered here keyed as the offer keys one, which is the
// next link's.
func TestACancelledPassStaysOpenUntilItsRetirementHoldIsAnsweredRetire(t *testing.T) {
	t.Parallel()
	const pass = "taskhkcancel"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-cancel")
		sub.terminals.dead("pane-cancel")
		passRun(t, records, "run-cancel", store.RunTerminated, false)
	})
	first := sub.holdRetirement(t, pass, 1)
	kept, err := sub.answer(t, pass, first.Key, machine.AnswerKeep)
	if err != nil || kept.Effect != machine.AnswerRecorded {
		t.Fatalf("keep answered %+v, %v", kept, err)
	}
	if h, err := sub.records(t).Hold(t.Context(), first.Key); err != nil || h.Open() || h.Resolution != store.Known(machine.AnswerKeep) {
		t.Fatalf("the kept hold reads %+v (%v), want it answered keep", h, err)
	}
	var open *store.PassOpenError
	if err := sub.createPass(t); !errors.As(err, &open) || open.Pass != pass {
		t.Fatalf("a pass after one kept open = %v, want it refused naming %s", err, pass)
	}
	if _, found := sub.retirement(t, pass); found {
		t.Fatal("keep wrote the retirement record")
	}
	offered := sub.holdRetirement(t, pass, 2)
	if _, err := sub.answer(t, pass, offered.Key, "maybe"); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("an answer outside retire and keep = %v, want it refused", err)
	}
	// A worker the probe cannot read still moves the pass, so a retire
	// given then is refused with the hold left open.
	sub.terminals.forget("pane-cancel")
	if _, err := sub.answer(t, pass, offered.Key, machine.AnswerRetire); !errors.Is(err, ipc.ErrRefused) || !errors.Is(err, store.ErrPassMoves) {
		t.Fatalf("retire with the worker unobservable = %v, want it refused", err)
	}
	if h, err := sub.records(t).Hold(t.Context(), offered.Key); err != nil || !h.Open() {
		t.Fatalf("the refused retire left %+v (%v), want the hold open", h, err)
	}
	sub.terminals.dead("pane-cancel")
	retired, err := sub.answer(t, pass, offered.Key, machine.AnswerRetire)
	if err != nil || retired.Effect != machine.AnswerRetired {
		t.Fatalf("retire answered %+v, %v", retired, err)
	}
	if record, found := sub.retirement(t, pass); !found || record.Hold != offered.Key {
		t.Fatalf("the retirement record is %+v (%v), want one naming %s", record, found, offered.Key)
	}
	if err := sub.createPass(t); err != nil {
		t.Fatalf("a pass after the retired one was refused: %v", err)
	}
}

// PRD section 13: an answer of retire after a run attributed to the pass
// has started is refused with the hold left open, naming the run, and
// writes no record.
func TestARetireAfterARunStartedIsRefused(t *testing.T) {
	t.Parallel()
	const pass = "taskhkheldaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-held")
		sub.terminals.dead("pane-held")
		passRun(t, records, "run-held", store.RunFailed, false)
	})
	hold := sub.holdRetirement(t, pass, 1)
	passRun(t, sub.records(t), "run-later", store.RunRunning, false)
	_, err := sub.answer(t, pass, hold.Key, machine.AnswerRetire)
	if !errors.Is(err, ipc.ErrRefused) || !errors.Is(err, store.ErrPassMoves) || !strings.Contains(err.Error(), "run-later") {
		t.Fatalf("retire after a run started = %v, want it refused naming the run", err)
	}
	if after := sub.retireHolds(t, pass); len(after) != 1 || !after[0].Open() {
		t.Fatalf("the refused retire left %+v, want the hold open", after)
	}
	if _, found := sub.retirement(t, pass); found {
		t.Fatal("the refused retire wrote the record")
	}
}

// PRD section 13: a retire answer closes the pass's stop hold, and every
// other hold whose retry would begin its worker again, as stop under the
// same resolver as the retire, and leaves the pass's report decision open
// for the next pass.
func TestARetireAnswersStopOnTheHoldsThatWouldRelaunchTheWorker(t *testing.T) {
	t.Parallel()
	const pass = "taskhkstopaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-stop")
		sub.terminals.dead("pane-stop")
		for _, key := range []string{stoppedKey(pass, 1), relaunchBoundKey(pass, 1), tasks.HoldKey(pass, "f1")} {
			if _, err := records.RegisterHold(t.Context(), store.Hold{Key: key, TaskID: store.Known(pass), Subject: "s", Detail: "d"}); err != nil {
				t.Fatal(err)
			}
		}
	})
	holds := []store.Hold{sub.holdRetirement(t, pass, 1)}
	answered, err := sub.answer(t, pass, holds[0].Key, machine.AnswerRetire)
	if err != nil || answered.Effect != machine.AnswerRetired {
		t.Fatalf("retire answered %+v, %v", answered, err)
	}
	records := sub.records(t)
	retire, err := records.Hold(t.Context(), holds[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{stoppedKey(pass, 1), relaunchBoundKey(pass, 1)} {
		h, err := records.Hold(t.Context(), key)
		if err != nil || h.Resolution != store.Known(machine.AnswerStop) || h.ResolvedBy != retire.ResolvedBy || !retire.ResolvedBy.IsKnown() {
			t.Fatalf("hold %s reads %+v (%v), want it answered stop by the retire's resolver %v", key, h, err, retire.ResolvedBy)
		}
	}
	if finding, err := records.Hold(t.Context(), tasks.HoldKey(pass, "f1")); err != nil || !finding.Open() {
		t.Fatalf("the report decision reads %+v (%v), want it left open", finding, err)
	}
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

// retireOnAnswer registers the pass's retirement hold, keyed as the offer
// keys one, and answers it retire, as the coordinator relaying a person's
// answer does.
func (s *hkSubject) retireOnAnswer(t *testing.T, pass string) {
	t.Helper()
	hold := s.holdRetirement(t, pass, 1)
	if answered, err := s.answer(t, pass, hold.Key, machine.AnswerRetire); err != nil || answered.Effect != machine.AnswerRetired {
		t.Fatalf("retire answered %+v, %v", answered, err)
	}
}

// holdRetirement registers the pass's retirement hold for the interval
// epoch, keyed as the offer keys one against the latest attempt and the
// newest run the store records, and returns it.
func (s *hkSubject) holdRetirement(t *testing.T, pass string, epoch int64) store.Hold {
	t.Helper()
	snap, err := s.records(t).TaskSnapshot(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	rh := retireHold{Task: pass, Epoch: epoch, Attempt: latestSequence(snap.Attempts)}
	if len(snap.Runs) > 0 {
		rh.Run = snap.Runs[0].ID
	}
	hold, err := s.records(t).RegisterHold(t.Context(), store.Hold{Key: rh.key(), TaskID: store.Known(pass),
		Subject: "whether to retire the pass", Detail: "d"})
	if err != nil {
		t.Fatalf("registering pass %s's retirement hold: %v", pass, err)
	}
	return hold
}

// settleQueue takes a pass, waits for the due check it starts, and then
// hands out and acknowledges every queued wake of every shape, as a
// coordinator that acted on all of them does, so that what keeps the home in
// flight afterwards is the tasks and nothing the queue holds.
func (s *hkSubject) settleQueue(t *testing.T) {
	t.Helper()
	s.checkStarted(t, s.pass(t), false)
	ctx := t.Context()
	records := s.records(t)
	for {
		drained, err := records.DrainAll(ctx, 100, store.WakeHandout{To: store.AcknowledgedByCoordinator(), Under: "settle"})
		if err != nil {
			t.Fatalf("draining the queue: %v", err)
		}
		var wakes, domain, kept, housekeeping []int64
		for _, w := range drained.Wakes {
			wakes = append(wakes, w.ID)
		}
		for _, w := range drained.DomainWakes {
			domain = append(domain, w.ID)
		}
		for _, w := range drained.KeptWorkWakes {
			kept = append(kept, w.ID)
		}
		for _, w := range drained.HousekeepingWakes {
			housekeeping = append(housekeeping, w.ID)
		}
		if len(wakes)+len(domain)+len(kept)+len(housekeeping) == 0 {
			return
		}
		if err := records.AcknowledgeAll(ctx, wakes, domain, kept, housekeeping, store.AcknowledgedByCoordinator()); err != nil {
			t.Fatalf("acknowledging the queue: %v", err)
		}
	}
}

// underWay reports whether the catch-up read lists the task under way.
func (s *hkSubject) underWay(t *testing.T, id string) bool {
	t.Helper()
	read, err := s.service.catchUp(t.Context())
	if err != nil {
		t.Fatalf("reading the catch-up: %v", err)
	}
	for _, task := range read.UnderWay.Tasks {
		if task.Record.ID == id {
			return true
		}
	}
	return false
}

// stopGuardWithTheServiceStopped stops the service and reads the stop guard
// as the coordinator's host hook reads it with no service holding the home:
// the instance file, the beacon, the lock and the record, read only, through
// feed.Guard.
func (s *hkSubject) stopGuardWithTheServiceStopped(t *testing.T) supervisor.StopVerdict {
	t.Helper()
	if err := s.service.Close(); err != nil {
		t.Fatalf("stopping the service: %v", err)
	}
	return feed.Guard(t.Context(), s.h, supervisor.FreshnessFor(time.Second), time.Now())
}

// The reviewer's case of PRD section 13's housekeeping row: a pass retired
// on an answer while its attempt never had a session resolves Dispatched,
// which is in flight on its state alone, though dispatch and every relaunch
// refuse it; it is not in flight, so it is not watched, the catch-up read
// does not list it under way, and with the service stopped the stop guard
// reads nothing in flight. A session that attempt's launch would record
// after the retirement is refused, so the pass stays one no worker moves;
// TestALaunchUnderWayWhenItsPassIsRetiredRecordsNoSessionAndEndsItsWorker
// is that launch through the real one, and
// TestARetiredPassWithARunNotEndedIsInFlight the positive control.
func TestAPassRetiredBeforeItsWorkerHadASessionIsNotInFlight(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	const pass = "taskhkflybbb"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		passTask(t, records, pass)
		launched(t, records, pass, "", "")
	})
	sub.retireOnAnswer(t, pass)
	sub.settleQueue(t)
	if got, err := sub.service.tasks().Task(t.Context(), pass); err != nil || got.State != taskstate.Dispatched {
		t.Fatalf("the retired pass resolves %+v (%v), want Dispatched, which is in flight on its state alone", got, err)
	}
	if r := sub.pass(t); r.Tasks[pass].Watched {
		t.Error("the pass retired before its worker had a session was watched")
	}
	if sub.underWay(t, pass) {
		t.Error("the catch-up read lists the pass retired before its worker had a session under way")
	}

	if err := sub.records(t).SetAttemptSession(t.Context(), pass, 1, "pane-late", store.SessionHost{}); !errors.Is(err, store.ErrPassRetired) {
		t.Fatalf("recording the late launch's session = %v, want ErrPassRetired", err)
	}
	if r := sub.pass(t); r.Tasks[pass].Watched {
		t.Error("the retired pass whose late launch was refused its record was watched")
	}
	if verdict := sub.stopGuardWithTheServiceStopped(t); verdict.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the service stopped the stop guard reads %s, want %s", verdict.Reason, supervisor.StopNothingInFlight)
	}
}

// The positive control on PRD section 13's housekeeping row for a retired
// pass: a run of the pass's branch started after its retirement, as a push
// the gate admitted while the retirement held the branch does, keeps the
// retired pass in flight while the run has not ended. It is watched, the catch-up
// read lists it under way, and with the service stopped the stop guard
// blocks, which shows the reading TestARetiredPassNothingMovesIsNotInFlight
// takes can fail.
func TestARetiredPassWithARunNotEndedIsInFlight(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	const pass = "taskhkflyccc"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-flightc")
		sub.terminals.dead("pane-flightc")
		passRun(t, records, "run-flightc", store.RunTerminated, false)
	})
	sub.retireOnAnswer(t, pass)
	passRun(t, sub.records(t), "run-flightc-after", store.RunRunning, false)
	sub.settleQueue(t)
	if r := sub.pass(t); !r.Tasks[pass].Watched {
		t.Error("the retired pass with a run not ended was not watched")
	}
	if !sub.underWay(t, pass) {
		t.Error("the catch-up read does not list the retired pass with a run not ended under way")
	}
	sub.settleQueue(t)
	if verdict := sub.stopGuardWithTheServiceStopped(t); verdict.Reason != supervisor.StopLockFree {
		t.Fatalf("with the service stopped the stop guard reads %s, want %s", verdict.Reason, supervisor.StopLockFree)
	}
}

// Answering keep on a retirement hold offered before the pass's first
// attempt, as the offer that wins the window before a dispatcher's claim,
// or a service that opens with that hold standing, leaves it, runs the
// dispatch the answer reports: the pass resolves Dispatched and nothing
// else would begin it, since the loop dispatches at open and on intake and
// not on its own, so the answer runs the dispatch, which claims the pass's
// first attempt. Before, the pass stayed unstarted and blocked every later
// pass. The open's dispatch, refused by the hold with nothing claimed, is
// the control that the attempt is the answer's.
func TestKeepOnAHoldOfferedBeforeTheFirstAttemptDispatchesThePass(t *testing.T) {
	t.Parallel()
	const pass = "taskhkkeepaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		// The pass as intake records it, dispatchable: its copy reserved
		// at this home's path and its instructions written.
		copyPath, err := sub.h.TaskCopy(pass)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := records.CreateTask(t.Context(), store.Task{
			ID: pass, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: copyPath,
			RepositoryID: store.Known(hkRepository), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}); err != nil {
			t.Fatal(err)
		}
		instructions, err := sub.h.TaskInstructions(pass)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(instructions), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(instructions, []byte("check the agent context\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		offerRetirementHoldOf(t, records, pass)
	})
	if attempts := recordedAttempts(t, sub.records(t), pass); len(attempts) != 0 {
		t.Fatalf("the open's dispatch claimed %+v over the open retirement hold", attempts)
	}
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("the pass carries %+v, want its one open retirement hold", holds)
	}
	if kept, err := sub.answer(t, pass, holds[0].Key, machine.AnswerKeep); err != nil || kept.Effect != machine.AnswerRecorded {
		t.Fatalf("keep answered %+v, %v", kept, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if attempts := recordedAttempts(t, sub.records(t), pass); len(attempts) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keep did not dispatch the pass: no attempt was begun")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
