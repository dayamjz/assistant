package service

import (
	"context"
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
		copyPath = unreportedPassCopy(t, records, sub, pass, "pane-unreported")
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

// PRD section 6: the retirement hold is offered to an open pass that
// nothing moves, decided where the hold is registered. A reported
// investigation pass the loop read open, whose findings the coordinator
// records relayed between that read and the registration, is in the
// terminal tier by then, so it is offered nothing, since a retire answer
// could only be refused, and the loop reports no failure. A run of the pass
// started in between moves it, and is refused the same way. The positive
// control is the same pass with nothing done in between, which is offered
// the hold. A pass the loop read with its attempt not yet launched, whose
// launch records that attempt's session in between, is moved by that
// launch and refused the same way: the offer is anchored to the session
// the loop read, none, as the retirement is.
func TestARetirementHoldIsOfferedOnlyToAPassStillOpenAndStillWhenItIsRegistered(t *testing.T) {
	t.Parallel()
	reported := func(id, ref string) hkPlant {
		return func(t *testing.T, records *store.Store, sub *hkSubject) {
			passTask(t, records, id)
			launched(t, records, id, "", ref)
			sub.terminals.dead(ref)
			if _, _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: "nothing drifted",
				Report: []byte(`{"summary":"nothing drifted"}`)}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	// unlaunched is a pass whose first attempt is claimed and has no
	// session, as one reads while its launch is under way.
	unlaunched := func(id string) hkPlant {
		return func(t *testing.T, records *store.Store, sub *hkSubject) {
			passTask(t, records, id)
			launched(t, records, id, "", "")
		}
	}
	for name, c := range map[string]struct {
		plant   func(id string) hkPlant
		between func(t *testing.T, sub *hkSubject, id string)
	}{
		"relayed": {func(id string) hkPlant { return reported(id, "pane-offer") }, func(t *testing.T, sub *hkSubject, id string) {
			ctx := context.Background()
			events, err := sub.records(t).TaskEvents(ctx, id, 0, 0)
			if err == nil {
				_, _, err = sub.records(t).RecordTaskRelay(ctx, id, events[len(events)-1].Sequence)
			}
			if err != nil {
				t.Errorf("relaying pass %s: %v", id, err)
			}
		}},
		"a session recorded": {unlaunched, func(t *testing.T, sub *hkSubject, id string) {
			if err := sub.records(t).SetAttemptSession(context.Background(), id, 1, "pane-launched", store.SessionHost{}); err != nil {
				t.Errorf("recording the session of pass %s's launch: %v", id, err)
			}
			sub.terminals.live("pane-launched", "working\n")
		}},
		"a run started": {func(id string) hkPlant { return reported(id, "pane-offer") }, func(t *testing.T, sub *hkSubject, id string) {
			// The loop's goroutine runs this, so a failure is reported
			// with Errorf and the assertions after the pass fail too.
			ctx := context.Background()
			records := sub.records(t)
			events, err := records.TaskEvents(ctx, id, 0, 0)
			if err == nil {
				_, _, err = records.TransitionTaskShape(ctx, id, store.TaskInvestigation, store.TaskDelivery, hkRepository, hkBranch,
					config.DeliveryGated, "Correct the drift the report found.", events[len(events)-1].Sequence, "promoted")
			}
			var build store.Build
			if err == nil {
				build, err = store.CurrentBuild()
			}
			if err == nil {
				_, err = records.CreateRun(ctx, store.Run{ID: "run-" + id, RepositoryID: hkRepository, Branch: hkBranch,
					SubmittedHead: "aaaa", Base: "main", Intent: "correct the drift", IntentSource: "task", ConfigDigest: "cfg", Build: build})
			}
			if err != nil {
				t.Errorf("promoting pass %s and starting its run: %v", id, err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const pass = "taskhkoffera"
			sub := openHousekeeping(t, housekeepingConfig, c.plant(pass))
			sub.beforeOffer(func(task string) {
				if task == pass {
					c.between(t, sub, task)
				}
			})
			sub.clock.Advance(time.Hour)
			r := sub.pass(t)
			if err := r.Housekeeping[hkRepository].Err; err != nil {
				t.Fatalf("the pass reported a failure over the repository: %v", err)
			}
			if holds := sub.retireHolds(t, pass); len(holds) != 0 {
				_, answered := sub.answer(t, pass, holds[0].Key, machine.AnswerRetire)
				t.Fatalf("the pass carries %+v, want no retirement hold; answering it retire = %v", holds, answered)
			}
		})
	}

	control := openHousekeeping(t, housekeepingConfig, reported("taskhkofferb", "pane-offer-control"))
	control.clock.Advance(time.Hour)
	control.pass(t)
	if holds := control.retireHolds(t, "taskhkofferb"); len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("with nothing done between the read and the registration the pass carries %+v, want one open retirement hold", holds)
	}
}

// The retirement hold is offered under the pass's slot, which a dispatch or
// relaunch holds unsettled through the launch that follows it: while it is
// held, a pass that reads as nothing moving (its attempt claimed and no
// session yet, which is how a launch under way reads) is offered no hold,
// since the hold would resolve it Held and take it out of flight while its
// worker is being created (P10); once the slot is released the next pass
// offers the hold, which is the control that the read looks at the slot.
func TestTheRetirementHoldIsNotOfferedWhileThePassesSlotIsHeld(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	const pass = "taskhkslotaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		passTask(t, records, pass)
		launched(t, records, pass, "", "")
	})
	release, err := sub.service.claimSteer(pass)
	if err != nil {
		t.Fatalf("taking the pass's slot as a launch does: %v", err)
	}
	sub.clock.Advance(time.Hour)
	if r := sub.pass(t); r.Housekeeping[hkRepository].Err != nil {
		t.Fatalf("the pass reported a failure over the repository: %v", r.Housekeeping[hkRepository].Err)
	}
	if holds := sub.retireHolds(t, pass); len(holds) != 0 {
		t.Fatalf("with its slot held the pass was offered %+v, want no retirement hold", holds)
	}
	release()
	sub.pass(t)
	if holds := sub.retireHolds(t, pass); len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("with its slot released the pass carries %+v, want one open retirement hold", holds)
	}
}

// unreportedPassCopy records an investigation pass of the repository whose
// copy, a clone of the upstream, holds a commit its worker made and never
// reported, launches its first attempt on session ref and reads that
// worker dead, and returns the copy's path.
func unreportedPassCopy(t *testing.T, records *store.Store, sub *hkSubject, pass, ref string) string {
	t.Helper()
	copyPath, err := sub.h.TaskCopy(pass)
	if err != nil {
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
	launched(t, records, pass, "", ref)
	sub.terminals.dead(ref)
	return copyPath
}

// PRD principle P15 across a stop: a service that stopped after it wrote a
// pass's retirement record and before it asked the cleanup decision for the
// pass's copy leaves a retired pass, a commit nobody reported in its copy,
// and no kept work entry. The service that opens on that home next asks the
// decision again from its loop, so the copy becomes a kept work entry and
// is not work nothing surfaces. The records are planted as that stop leaves
// them: the retirement is written with the request an answered retire
// writes, and nothing after it.
func TestARetiredPassWhoseCleanupWasNeverAskedIsKeptOnceTheServiceOpensAgain(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	const pass = "taskhkstopaa"
	var copyPath string
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		ctx := t.Context()
		copyPath = unreportedPassCopy(t, records, sub, pass, "pane-stopped")
		hold := retireHold{Task: pass, Epoch: 1, Attempt: 1}.key()
		if _, err := records.RegisterHold(ctx, store.Hold{Key: hold, TaskID: store.Known(pass),
			Subject: "whether to retire a housekeeping pass", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
		r := retirementOf(pass, recordedAttempts(t, records, pass), "")
		r.Hold, r.By = hold, store.ResolvedByMachineInterface()
		if _, err := records.RetireHousekeepingPass(ctx, r); err != nil {
			t.Fatalf("retiring the pass: %v", err)
		}
		if kept, err := records.OpenKeptWork(ctx); err != nil || len(kept) != 0 {
			t.Fatalf("before the service opens the open kept work is %+v (%v), want none", kept, err)
		}
	})
	sub.awaitKeptOf(t, pass)
	if _, err := os.Stat(filepath.Join(copyPath, "AGENTS.md")); err != nil {
		t.Fatalf("the cleanup asked again removed the pass's copy: %v", err)
	}
}

// A retired pass is owed the cleanup decision for its copy while it is not
// in the terminal tier, its copy carries no removal and it was dispatched,
// since a dispatch cuts the copy after its claim; and the terminal it is
// asked with is its resolution's when its latest attempt has a session and
// Dead when it never had one. Every other task's terminal is its
// resolution's.
func TestARetiredPassIsOwedItsCleanupOnRetiredCleanupOwedsTerms(t *testing.T) {
	t.Parallel()
	retired := func(edit func(o *taskstate.Observed)) taskstate.Observed {
		o := taskstate.Observed{
			Snapshot: store.TaskSnapshot{
				Task:       store.Task{ID: "taskhkownaaa", Shape: store.TaskInvestigation, Housekeeping: true},
				Retirement: store.Known(store.HousekeepingRetirement{Task: "taskhkownaaa", Hold: store.Known("k")}),
				Attempts:   []store.TaskAttempt{{Sequence: 1, SessionRef: store.Known("pane-own")}},
			},
			State: taskstate.Resolution{State: taskstate.Unknown, Source: taskstate.SourceProcess,
				Basis: taskstate.Basis{Process: taskstate.Dead}},
		}
		if edit != nil {
			edit(&o)
		}
		return o
	}
	for name, c := range map[string]struct {
		o        taskstate.Observed
		owed     bool
		terminal taskstate.Liveness
	}{
		"retired, dead": {retired(nil), true, taskstate.Dead},
		"retired, live": {retired(func(o *taskstate.Observed) { o.State.Basis.Process = taskstate.Live }), true, taskstate.Live},
		"no session yet": {retired(func(o *taskstate.Observed) {
			o.Snapshot.Attempts[0].SessionRef = store.Optional[string]{}
			o.State.Basis.Process = taskstate.Unobserved
		}), true, taskstate.Dead},
		"never dispatched": {retired(func(o *taskstate.Observed) {
			o.Snapshot.Attempts, o.State.Basis.Process = nil, taskstate.Unobserved
		}), false, taskstate.Unobserved},
		"not retired":  {retired(func(o *taskstate.Observed) { o.Snapshot.Retirement = store.Optional[store.HousekeepingRetirement]{} }), false, taskstate.Dead},
		"copy removed": {retired(func(o *taskstate.Observed) { o.State.Basis.CleanupRemoved = true }), false, taskstate.Dead},
		"landed since": {retired(func(o *taskstate.Observed) {
			o.State.State, o.State.Source, o.State.Basis.Process = taskstate.Landed, taskstate.SourceRecord, taskstate.Unobserved
		}), false, taskstate.Unobserved},
	} {
		if got := retiredCleanupOwed(c.o); got != c.owed {
			t.Errorf("%s: retiredCleanupOwed = %v, want %v", name, got, c.owed)
		}
		if got := cleanupTerminal(c.o); got != c.terminal {
			t.Errorf("%s: cleanupTerminal = %s, want %s", name, got, c.terminal)
		}
	}
}

// awaitKeptOf waits for the open kept work to be one entry, about task,
// which a cleanup the loop asks off its goroutine writes.
func (s *hkSubject) awaitKeptOf(t *testing.T, task string) {
	t.Helper()
	deadline := time.Now().Add(loopWait)
	for {
		kept, err := s.records(t).OpenKeptWork(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) > 0 {
			if len(kept) != 1 || kept[0].TaskID != store.Known(task) {
				t.Fatalf("the open kept work is %+v, want one entry about task %s", kept, task)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no kept work entry was written for task %s; the log reads:\n%s", task, serviceLogOf(t, s.h))
		}
		time.Sleep(20 * time.Millisecond)
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

// retireOnAnswer passes an interval, so the pass, which nothing moves, is
// offered its retirement hold, and answers it retire, as the coordinator
// relaying a person's answer does.
func (s *hkSubject) retireOnAnswer(t *testing.T, pass string) {
	t.Helper()
	s.clock.Advance(time.Hour)
	s.pass(t)
	holds := s.retireHolds(t, pass)
	if len(holds) != 1 || !holds[0].Open() {
		t.Fatalf("after an interval pass %s carries %+v, want one open retirement hold", pass, holds)
	}
	if answered, err := s.answer(t, pass, holds[0].Key, machine.AnswerRetire); err != nil || answered.Effect != machine.AnswerRetired {
		t.Fatalf("retire answered %+v, %v", answered, err)
	}
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

// PRD section 13's housekeeping row and principle P10, for a retired pass
// nothing moves: an investigation pass whose worker ended before it reported
// resolves Unknown, which is in flight on its state alone, and once retired
// on an answer, with every wake acknowledged, it is not in flight. Across
// four escalation windows it is not watched and raises no terminal-gone
// wake, the catch-up read does not list it under way, and with the service
// stopped the stop guard reads nothing in flight, so a turn may end.
// TestARetiredPassWithARunNotEndedIsInFlight is the positive control.
func TestARetiredPassNothingMovesIsNotInFlight(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	const pass = "taskhkflyaaa"
	sub := openHousekeeping(t, housekeepingConfig, func(t *testing.T, records *store.Store, sub *hkSubject) {
		unreportedPassCopy(t, records, sub, pass, "pane-flight")
	})
	sub.retireOnAnswer(t, pass)
	sub.awaitKeptOf(t, pass)
	sub.settleQueue(t)
	if got := sub.observedTask(t, pass); got.State != taskstate.Unknown {
		t.Fatalf("the retired pass resolves %+v, want Unknown, which is in flight on its state alone", got)
	}
	for window := 1; window <= 4; window++ {
		sub.clock.Advance(2 * time.Minute)
		r := sub.pass(t)
		if r.Tasks[pass].Watched {
			t.Errorf("window %d: the retired pass nothing moves was watched", window)
		}
		if gone := detectionsOf(r, pass, supervisor.KindTerminalGone); len(gone) != 0 {
			t.Errorf("window %d: the retired pass nothing moves had its terminal read: %+v", window, gone)
		}
	}
	if sub.underWay(t, pass) {
		t.Error("the catch-up read lists the retired pass nothing moves under way")
	}
	if verdict := sub.stopGuardWithTheServiceStopped(t); verdict.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the service stopped the stop guard reads %s, want %s", verdict.Reason, supervisor.StopNothingInFlight)
	}
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
