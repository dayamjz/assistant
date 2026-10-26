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
		config.DeliveryGated, "Correct the drift the report found.", events[len(events)-1].Sequence, "promoted"); err != nil {
		t.Fatalf("promoting pass %s: %v", id, err)
	}
}

// passRun records a run of the pass's branch, after its attempt began, in
// status. A passed run is driven through the real pipeline over the durable
// checkpoint store first, with a rebase stage that writes the empty-diff
// short circuit when nothingLeft is set, so the position the retirement
// reads is what the executor wrote and not one stated here.
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
	if status == store.RunPassed {
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

// awaitRetired waits for the next on-proof retirement's end.
func (s *hkSubject) awaitRetired(t *testing.T) hkRetired {
	t.Helper()
	select {
	case r := <-s.retired:
		return r
	case <-time.After(loopWait):
		t.Fatal("no on-proof retirement reported")
		return hkRetired{}
	}
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

// PRD section 13's housekeeping row: a pass whose run rebased to nothing is
// retired on that proof, read from the run's recorded position, with no
// hold registered, and the pass still resolves Ready; a later pass can then
// be created. The worker's end is verified by the probe reading it dead.
func TestAPassWhoseRunRebasedToNothingIsRetiredOnProof(t *testing.T) {
	t.Parallel()
	const pass = "taskhkproofa"
	var run store.Run
	stall := supervisor.StallHold{Task: pass, Attempt: 1, Epoch: 1, Surface: 3}
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-proof")
		sub.terminals.dead("pane-proof")
		run = passRun(t, records, "run-proof", store.RunPassed, true)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: stall.Key(), TaskID: store.Known(pass),
			Subject: "whether to end a stalled worker", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	if got := sub.awaitRetired(t); got.task != pass || got.run != run.ID || got.err != nil {
		t.Fatalf("the on-proof retirement ended %+v, want pass %s retired on run %s", got, pass, run.ID)
	}
	record, found := sub.retirement(t, pass)
	if !found || !record.OnProof() || record.Run != store.Known(run.ID) {
		t.Fatalf("the retirement record is %+v (%v), want one naming run %s", record, found, run.ID)
	}
	snap, err := sub.records(t).TaskSnapshot(t.Context(), pass)
	if err != nil {
		t.Fatal(err)
	}
	// The one hold the pass carries is the stall hold planted above, which
	// the retirement's probe reading the worker dead closed as the
	// program's (PRD section 6's "the probe a housekeeping retirement takes
	// among them"); nothing was registered.
	if len(snap.Holds) != 1 || snap.Holds[0].Key != stall.Key() || snap.Holds[0].Open() ||
		snap.Holds[0].Resolution != store.Known(supervisor.StallClosedWorkerDead) {
		t.Fatalf("the on-proof retirement left the holds %+v, want only the stall hold, closed on the dead reading", snap.Holds)
	}
	if got := sub.observedTask(t, pass); got.State != taskstate.Ready {
		t.Fatalf("the retired pass resolves %s, want Ready, which the retirement does not change", got.State)
	}
	if err := sub.createPass(t); err != nil {
		t.Fatalf("a pass after the retired one was refused: %v", err)
	}
}

// The same run whose rebase found something to deliver is no proof: the
// pass is not retired, its run's position having said nothing of the kind.
// This is the control on the position, beside the one on the probe below.
func TestAPassedRunWithSomethingLeftIsNoProof(t *testing.T) {
	t.Parallel()
	const pass = "taskhkproofb"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-left")
		sub.terminals.dead("pane-left")
		passRun(t, records, "run-left", store.RunPassed, false)
	})
	if got := sub.awaitRetired(t); !errors.Is(got.err, errNothingLeftUnread) {
		t.Fatalf("a run with something left ended the on-proof retirement %+v, want it to read no proof", got)
	}
	if _, found := sub.retirement(t, pass); found {
		t.Fatal("a pass whose run delivered something was retired")
	}
}

// An on-proof retirement whose worker end cannot be verified writes nothing:
// a worker this service does not hold, which reads live, is ended only by a
// stop, so the pass is left to the hold and the escalation.
func TestAnOnProofRetirementWhoseWorkerEndCannotBeVerifiedWritesNothing(t *testing.T) {
	t.Parallel()
	const pass = "taskhkproofc"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-other")
		sub.terminals.live("pane-other", "still here\n")
		passRun(t, records, "run-other", store.RunPassed, true)
	})
	got := sub.awaitRetired(t)
	if got.err == nil || !strings.Contains(got.err.Error(), "could not be verified") {
		t.Fatalf("the on-proof retirement ended %+v, want its worker's end unverified", got)
	}
	if _, found := sub.retirement(t, pass); found {
		t.Fatal("a pass whose worker's end was not verified was retired")
	}
}

// With a stop's hold open on the pass, no on-proof retirement is written:
// the program never answers a hold that waits on a person, and that hold's
// retry would begin the worker again.
func TestAStopsHoldOpenKeepsAPassFromItsOnProofRetirement(t *testing.T) {
	t.Parallel()
	const pass = "taskhkproofd"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-stopped")
		sub.terminals.dead("pane-stopped")
		passRun(t, records, "run-stopped", store.RunPassed, true)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: stoppedKey(pass, 1), TaskID: store.Known(pass),
			Subject: "whether to begin the work again", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	sub.pass(t)
	if len(sub.retired) != 0 {
		t.Fatalf("an on-proof retirement was tried with the stop's hold open: %+v", <-sub.retired)
	}
	if _, found := sub.retirement(t, pass); found {
		t.Fatal("a pass with a stop's hold open was retired on proof")
	}
}

// PRD section 13: a pass whose run was cancelled stays open until a
// retirement hold is answered retire, and keep leaves it open, offering the
// hold again only after another interval; the answered retirement releases
// the claim so a later pass can be created.
func TestACancelledPassStaysOpenUntilItsRetirementHoldIsAnsweredRetire(t *testing.T) {
	t.Parallel()
	const pass = "taskhkcancel"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-cancel")
		sub.terminals.dead("pane-cancel")
		passRun(t, records, "run-cancel", store.RunTerminated, false)
	})
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("after an interval the cancelled pass carries %+v, want one open retirement hold", holds)
	}
	kept, err := sub.answer(t, pass, holds[0].Key, machine.AnswerKeep)
	if err != nil || kept.Effect != machine.AnswerRecorded {
		t.Fatalf("keep answered %+v, %v", kept, err)
	}
	var open *store.PassOpenError
	if err := sub.createPass(t); !errors.As(err, &open) || open.Pass != pass {
		t.Fatalf("a pass after one kept open = %v, want it refused naming %s", err, pass)
	}
	sub.pass(t)
	if again := sub.retireHolds(t, pass); len(again) != 1 {
		t.Fatalf("within the same interval the hold was offered again: %+v", again)
	}
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	holds = sub.retireHolds(t, pass)
	var offered store.Hold
	for _, h := range holds {
		if h.Open() {
			offered = h
		}
	}
	if len(holds) != 2 || !offered.Open() {
		t.Fatalf("another interval on, the pass carries %+v, want the hold offered again", holds)
	}
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
	if record, found := sub.retirement(t, pass); !found || record.Hold != store.Known(offered.Key) {
		t.Fatalf("the retirement record is %+v (%v), want one naming %s", record, found, offered.Key)
	}
	if err := sub.createPass(t); err != nil {
		t.Fatalf("a pass after the retired one was refused: %v", err)
	}
}

// The positive control on retirement: a landed pass needs none. Its merge
// record puts it in the terminal tier, so it is offered no retirement hold
// however long ago it was created, and the next pass can be created.
func TestALandedPassNeedsNoRetirement(t *testing.T) {
	t.Parallel()
	const pass = "taskhklanded"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-landed")
		sub.terminals.dead("pane-landed")
		passRun(t, records, "run-landed", store.RunPassed, false)
		if _, err := records.RecordMergeOfPassedRun(t.Context(), "run-landed",
			store.PullRequestRead{PullRequest: 7, End: store.PullRequestMerged}); err != nil {
			t.Fatal(err)
		}
	})
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	if got := sub.retireHolds(t, pass); len(got) != 0 {
		t.Fatalf("a landed pass was offered retirement: %+v", got)
	}
	if err := sub.createPass(t); err != nil {
		t.Fatalf("a pass after the landed one was refused: %v", err)
	}
}

// PRD section 13: a promoted pass with an open report decision and a run
// still advancing resolves Held and gets no retirement hold; once that run
// ends and the worker reads dead, it gets one (the positive control); and an
// answer of retire after a run attributed to the pass has started is
// refused with the hold left open.
func TestARetireAfterARunStartedIsRefusedAndAHeldPassThatMovesIsNotOffered(t *testing.T) {
	t.Parallel()
	const pass = "taskhkheldaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-held")
		sub.terminals.live("pane-held", "validating\n")
		passRun(t, records, "run-held", store.RunRunning, false)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: tasks.HoldKey(pass, "f1"), TaskID: store.Known(pass),
			Subject: "whether the README's rule should be reworded", Detail: "the proposed wording"}); err != nil {
			t.Fatal(err)
		}
	})
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	if got := sub.observedTask(t, pass); got.State != taskstate.Held {
		t.Fatalf("the pass resolves %s, want Held from its open report decision", got.State)
	}
	if got := sub.retireHolds(t, pass); len(got) != 0 {
		t.Fatalf("a held pass whose run validates was offered retirement: %+v", got)
	}

	if _, err := sub.records(t).TransitionRun(t.Context(), "run-held", []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	sub.terminals.dead("pane-held")
	sub.pass(t)
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("once nothing moves the pass it carries %+v, want one open retirement hold", holds)
	}
	passRun(t, sub.records(t), "run-later", store.RunRunning, false)
	_, err := sub.answer(t, pass, holds[0].Key, machine.AnswerRetire)
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
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 {
		t.Fatalf("the pass carries %+v, want one retirement hold", holds)
	}
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

// PRD section 13 and principles P6 and P15: retiring a pass whose copy
// holds unlanded work writes a kept-work entry and removes nothing.
func TestRetiringAPassWithUnlandedWorkKeepsItsCopy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6, principles.P15)
	const pass = "taskhkkeptaa"
	var copyPath string
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		var err error
		if copyPath, err = sub.h.TaskCopy(pass); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
			t.Fatal(err)
		}
		heldGit(t, filepath.Dir(copyPath), "clone", "--quiet", sub.upstream, copyPath)
		heldGit(t, copyPath, "checkout", "--quiet", "-b", hkBranch)
		writeDocument(t, copyPath, "AGENTS.md", "the corrected command\n")
		heldGit(t, copyPath, "add", "-A")
		heldGit(t, copyPath, "commit", "--quiet", "-m", "correct the drift")
		if _, err := records.CreateTask(t.Context(), store.Task{
			ID: pass, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: copyPath,
			RepositoryID: store.Known(hkRepository), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}); err != nil {
			t.Fatal(err)
		}
		promotePass(t, records, pass)
		launched(t, records, pass, "", "pane-kept")
		sub.terminals.dead("pane-kept")
		passRun(t, records, "run-kept", store.RunTerminated, false)
	})
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 {
		t.Fatalf("the pass carries %+v, want one retirement hold", holds)
	}
	if _, err := sub.answer(t, pass, holds[0].Key, machine.AnswerRetire); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if _, err := os.Stat(filepath.Join(copyPath, "AGENTS.md")); err != nil {
		t.Fatalf("retiring removed the pass's copy: %v", err)
	}
	kept, err := sub.records(t).OpenKeptWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].TaskID != store.Known(pass) {
		t.Fatalf("the open kept work is %+v, want one entry about pass %s", kept, pass)
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

// PRD principle P15, for a pass that never filed its report: an
// investigation pass whose worker committed work and ended before it
// reported is retired on an answer, and its copy, which the cleanup decision
// keeps for want of a report nothing will now store, is a kept-work entry
// rather than a copy nothing surfaces.
func TestRetiringAnUnreportedPassWithCommittedWorkKeepsItAsKeptWork(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	const pass = "taskhkunrepa"
	var copyPath string
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		var err error
		if copyPath, err = sub.h.TaskCopy(pass); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
			t.Fatal(err)
		}
		heldGit(t, filepath.Dir(copyPath), "clone", "--quiet", sub.upstream, copyPath)
		writeDocument(t, copyPath, "AGENTS.md", "a correction never reported\n")
		heldGit(t, copyPath, "add", "-A")
		heldGit(t, copyPath, "commit", "--quiet", "-m", "correct the drift")
		if _, err := records.CreateTask(t.Context(), store.Task{
			ID: pass, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: copyPath,
			RepositoryID: store.Known(hkRepository), Role: config.RoleHousekeeping, Housekeeping: true,
		}, store.Disposition{Delivered: taskstate.Delivered}); err != nil {
			t.Fatal(err)
		}
		launched(t, records, pass, "", "pane-unreported")
		sub.terminals.dead("pane-unreported")
	})
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	holds := sub.retireHolds(t, pass)
	if len(holds) != 1 {
		t.Fatalf("the pass carries %+v, want one retirement hold", holds)
	}
	if answered, err := sub.answer(t, pass, holds[0].Key, machine.AnswerRetire); err != nil || answered.Effect != machine.AnswerRetired {
		t.Fatalf("retire answered %+v, %v", answered, err)
	}
	if _, err := os.Stat(filepath.Join(copyPath, "AGENTS.md")); err != nil {
		t.Fatalf("retiring removed the pass's copy: %v", err)
	}
	kept, err := sub.records(t).OpenKeptWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].TaskID != store.Known(pass) {
		t.Fatalf("the open kept work is %+v, want one entry about pass %s", kept, pass)
	}
}

// A pass the program can retire on proof, older than an interval, is never
// left retired with its retirement hold open: the loop offers the hold and
// starts the on-proof retirement from the same reading, and whichever lands
// first, the other gives way. Retired with an open retirement hold is the
// state refused, since answering that hold could only be refused. An open
// stop hold keeps the first pass from starting the retirement, so the pass
// an interval later is the one that does both.
func TestARetiredPassIsNeverLeftWithItsRetirementHoldOpen(t *testing.T) {
	t.Parallel()
	const pass = "taskhkbothaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		promotedPass(t, records, pass, "pane-both")
		sub.terminals.dead("pane-both")
		passRun(t, records, "run-both", store.RunPassed, true)
		if _, err := records.RegisterHold(t.Context(), store.Hold{Key: stoppedKey(pass, 1), TaskID: store.Known(pass),
			Subject: "whether to begin the work again", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	if len(sub.retired) != 0 {
		t.Fatal("the first pass started an on-proof retirement with the stop's hold open")
	}
	if _, err := sub.records(t).ResolveHold(t.Context(), stoppedKey(pass, 1), "stop", store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	sub.clock.Advance(time.Hour)
	sub.pass(t)
	<-sub.retired
	_, retired := sub.retirement(t, pass)
	open := 0
	for _, h := range sub.retireHolds(t, pass) {
		if h.Open() {
			open++
		}
	}
	if retired && open != 0 {
		t.Fatalf("the pass was retired and still carries %d open retirement hold(s)", open)
	}
	if !retired && open != 1 {
		t.Fatalf("the pass was not retired and carries %d open retirement hold(s), want the one a person answers", open)
	}
}
