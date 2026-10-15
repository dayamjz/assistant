package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// PRD section 9's answer given a task, reaching the task's run: a task with
// no hold of its own open has --task answer the hold on its current run, so
// a coordinator answers a task's run without checking the task's branch out
// anywhere. Here the person's primary working copy has gone back to its
// default branch and stays there, and the task's own copy is never bound to
// a gate, which is what running init in it would have done to the isolation
// P11 asserts at launch.
//
// The run is the one --answer reaches from a checkout of the branch, read
// off status there before the checkout moves away. A fix round asked for
// with --task carries --select, --note and --finding to the fixer as it
// does on a branch's run, and the run holds at the same stage again; an
// answer given from a directory that is no working copy at all then moves
// the run to the next stage that holds. Nothing in the primary moves: the
// branch it stands on, its head and its worktree are as they were.
func TestATasksRunIsAnsweredGivenTheTaskFromOffItsBranch(t *testing.T) {
	principles.Cite(t, principles.P11)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text("did what was asked")},
	}})
	serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	first, id := heldTaskRun(t, h, subject)
	holds, next := firstHoldingStages(t)
	if first.Decision == nil || first.Decision.Stage != holds.String() || len(first.Decision.Findings) == 0 {
		t.Fatalf("the task's run is not holding at %s on a finding to select: %+v", holds, first.Decision)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}

	// The primary goes back to its default branch, which is where a person's
	// checkout stands while a fleet works on task branches.
	git(t, subject, "checkout", "--quiet", "main")
	before := primaryOf(t, subject)
	if before.branch != "main" {
		t.Fatalf("the primary stands on %q, want main, off the task's branch", before.branch)
	}

	// A fix round, from the primary on main.
	fixed := run(t, h, subject, "--json", "--task", id, "--answer", string(pipeline.OutcomeFixRequested),
		"--select", first.Decision.Findings[0].ID,
		"--note", "keep the public name",
		"--finding", "also say why in the commit")
	if fixed.code != machine.ExitOK {
		t.Fatalf("asking the task's run for a fix round exited %s:\n%s%s", fixed.code, fixed.stdout, fixed.stderr)
	}
	second := decodeRun(t, fixed.stdout)
	if second.Record.ID != first.Record.ID {
		t.Fatalf("the fix round moved run %s, want the task's run %s", second.Record.ID, first.Record.ID)
	}
	if second.Decision == nil || second.Decision.Stage != holds.String() {
		t.Fatalf("after the round the task's run is not holding at %s again: %+v", holds, second.Decision)
	}
	var rounds []standin.Call
	for _, call := range agent.Calls() {
		if strings.HasPrefix(call.Prompt, "You are fixing a change") {
			rounds = append(rounds, call)
		}
	}
	if len(rounds) != 1 {
		t.Fatalf("the fixer was invoked %d time(s), want the one requested round", len(rounds))
	}
	for _, want := range []string{"keep the public name", "also say why in the commit", first.Decision.Findings[0].Description} {
		if !strings.Contains(rounds[0].Prompt, want) {
			t.Errorf("the fixer was not given %q:\n%s", want, rounds[0].Prompt)
		}
	}

	// A fix round's flags with any other answer are refused as they are on a
	// branch's run, before anything is asked.
	if got := run(t, h, subject, "--task", id, "--answer", string(pipeline.OutcomeApproved), "--note", "ignored"); got.code != machine.ExitUsage {
		t.Fatalf("a note with an approval given the task exited %s, want incorrect usage:\n%s", got.code, got.stderr)
	}

	// An approval, from a directory that is not a working copy.
	elsewhere := t.TempDir()
	approved := run(t, h, elsewhere, "--json", "--task", id, "--answer", string(pipeline.OutcomeApproved))
	if approved.code != machine.ExitOK {
		t.Fatalf("approving the task's run exited %s:\n%s%s", approved.code, approved.stdout, approved.stderr)
	}
	third := decodeRun(t, approved.stdout)
	if third.Record.ID != first.Record.ID || third.Decision == nil || third.Decision.Stage != next.String() {
		t.Fatalf("the approval left run %s at %+v, want run %s holding at %s", third.Record.ID, third.Decision, first.Record.ID, next)
	}

	if after := primaryOf(t, subject); after != before {
		t.Fatalf("answering the task's run moved the primary from %+v to %+v", before, after)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	// The control: the primary, which init bound, reads its binding, so the
	// read below is one that finds a binding where there is one.
	if _, err := records.GateBinding(t.Context(), subject); err != nil {
		t.Fatalf("the primary reads no gate binding: %v", err)
	}
	if _, err := records.GateBinding(t.Context(), copyPath); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the task's copy reads a gate binding (%v), want none: answering its run binds nothing", err)
	}
}

// A task's own hold keeps the precedence it had: while one is open, --task
// answers it and leaves the task's run held where it was, and a fix round,
// which only a run takes, is refused naming the hold that comes first
// rather than reaching past it. Once the task's own holds are answered the
// same flag reaches its run. The task is an investigation promoted to
// delivery in place with its report's decisions still open, which promotion
// allows, so the shape is one the coordinator's commands produce.
//
// The refusal is typed on the wire and carries internal/machine's row,
// which P13 holds to the vocabulary every phrase is.
func TestATasksOwnHoldIsAnsweredBeforeItsRun(t *testing.T) {
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	runBefore := startTaskBranchRun(t, h, subject)
	holds, next := firstHoldingStages(t)
	id := promotedWithDecisions(t, h, subject, taskBranch)
	git(t, subject, "checkout", "--quiet", "main")
	elsewhere := t.TempDir()

	own := openHoldsOf(t, h, id)
	if len(own) != 2 {
		t.Fatalf("the promoted task holds %+v, want its report's two decisions", own)
	}

	// A fix round is a run's, and the task's own decisions come first.
	refused := run(t, h, elsewhere, "--json", "--task", id, "--answer", string(pipeline.OutcomeFixRequested),
		"--select", runBefore.Decision.Findings[0].ID)
	failure := refusal(t, refused)
	if !strings.Contains(failure.Error, machine.PhraseTaskOwnHoldFirst.String()) || !strings.Contains(failure.Error, own[0].Key) {
		t.Fatalf("the refusal says %q, want it to name hold %s and carry the row for answering it first", failure.Error, own[0].Key)
	}
	vocabtest.Control(t)
	vocabtest.Assert(t, machine.PhraseTaskOwnHoldFirst.String())
	assertHeldAt(t, h, elsewhere, runBefore.Record.ID, holds)

	// Each answer closes the task's own oldest hold, and the run stays put.
	for _, answer := range []string{"approved", "skipped"} {
		got := answerTask(t, h, elsewhere, id, answer)
		if got.Effect != machine.AnswerRecorded || got.Hold.Resolution.Or("") != answer {
			t.Fatalf("answering %q given the task reports %+v, want the task's own hold recorded", answer, got)
		}
		assertHeldAt(t, h, elsewhere, runBefore.Record.ID, holds)
	}

	// With none of its own open, the same flag reaches the run.
	moved := run(t, h, elsewhere, "--json", "--task", id, "--answer", string(pipeline.OutcomeApproved))
	if moved.code != machine.ExitOK {
		t.Fatalf("approving the task's run exited %s:\n%s%s", moved.code, moved.stdout, moved.stderr)
	}
	after := decodeRun(t, moved.stdout)
	if after.Record.ID != runBefore.Record.ID || after.Decision == nil || after.Decision.Stage != next.String() {
		t.Fatalf("the approval left run %s at %+v, want run %s holding at %s", after.Record.ID, after.Decision, runBefore.Record.ID, next)
	}
}

// An answer given a task that neither holds itself nor has a run waiting is
// a typed refusal carrying the row that says nothing was answered and what
// to read: a task that names no branch, a task whose branch has no run, and
// a task whose run was ended. None records anything.
func TestAnAnswerGivenATaskNothingWaitsOnIsRefused(t *testing.T) {
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	elsewhere := t.TempDir()
	vocabtest.Control(t)
	vocabtest.Assert(t, machine.PhraseTaskNothingWaiting.String())

	nothing := func(what, id string) {
		t.Helper()
		got := run(t, h, elsewhere, "--json", "--task", id, "--answer", string(pipeline.OutcomeApproved))
		if failure := refusal(t, got); !strings.Contains(failure.Error, machine.PhraseTaskNothingWaiting.String()) {
			t.Fatalf("answering %s says %q, want the row that nothing waits", what, failure.Error)
		}
	}

	records := openStore(t, h)
	investigation := "investigat23"
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: investigation, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	nothing("a task naming no branch", investigation)

	id := recordDispatchedTask(t, h, subject)
	nothing("a task whose branch has no run", id)

	started := startTaskBranchRun(t, h, subject)
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the task's run exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	nothing("a task whose run was ended", id)
	if got := runOf(t, h, elsewhere, started.Record.ID); got.Record.Status == store.RunHeld {
		t.Fatalf("the ended run reads %s", got.Record.Status)
	}
}

// taskBranch is the branch recordDispatchedTask records its task on.
const taskBranch = "task/one"

// heldTaskRun starts a run of taskBranch from the primary and records a
// task on that branch, returning the run as it held and the task.
func heldTaskRun(t *testing.T, h *home.Home, subject string) (machine.Run, string) {
	t.Helper()
	held := startTaskBranchRun(t, h, subject)
	return held, recordDispatchedTask(t, h, subject)
}

// startTaskBranchRun checks taskBranch out in the primary with a change on
// it, and starts that branch's run there, returning it as it held. Its
// status there names the same run, which is the run --answer reaches from a
// checkout of the branch.
func startTaskBranchRun(t *testing.T, h *home.Home, subject string) machine.Run {
	t.Helper()
	git(t, subject, "checkout", "--quiet", "-b", taskBranch)
	if err := os.WriteFile(filepath.Join(subject, "change.txt"), []byte("a change on the task's branch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "the change under validation")
	started := run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated")
	if started.code != machine.ExitOK {
		t.Fatalf("starting the task branch's run exited %s:\n%s%s", started.code, started.stdout, started.stderr)
	}
	held := decodeRun(t, started.stdout)
	if held.Outcome != machine.OutcomeDecision || held.Decision == nil || len(held.Decision.Findings) == 0 {
		t.Fatalf("the task branch's run is not holding on a finding: %+v", held)
	}
	return held
}

// promotedWithDecisions records, where a coordinator's commands would have,
// an investigation in the subject's repository whose report registered two
// decisions, then promotes it in place to delivery on branch with both still
// open, through the one write promotion makes.
func promotedWithDecisions(t *testing.T, h *home.Home, subject, branch string) string {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	repository, err := records.RepositoryAt(t.Context(), subject)
	if err != nil {
		t.Fatal(err)
	}
	id := "promotedtask"
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/promoted",
		RepositoryID: store.Known(repository.ID),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.AppendTaskEvent(t.Context(), id, "report-ready", "the findings are filed"); err != nil {
		t.Fatal(err)
	}
	raw := `{"summary":"two decisions","findings":[` +
		`{"severity":"warning","action":"ask","description":"Whether the old index may be dropped."},` +
		`{"severity":"warning","action":"ask","description":"Whether the cache should move."}]}`
	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var holds []store.Hold
	for _, f := range report.Held() {
		holds = append(holds, store.Hold{Key: tasks.HoldKey(id, f.ID), TaskID: store.Known(id), Subject: f.Description, Detail: f.Description})
	}
	if _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: raw, Report: encoded}, holds); err != nil {
		t.Fatal(err)
	}
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("reading the investigation's history: %v (%d entries)", err, len(events))
	}
	if _, _, err := records.TransitionTaskShape(t.Context(), id, store.TaskInvestigation, store.TaskDelivery,
		repository.ID, branch, config.DeliveryGated, events[len(events)-1].Sequence, "promoted with its decisions open"); err != nil {
		t.Fatalf("promoting the investigation: %v", err)
	}
	return id
}

// primary is what a person's checkout stands on: the branch, the head, and
// what its worktree holds.
type primary struct{ branch, head, worktree string }

func primaryOf(t *testing.T, dir string) primary {
	t.Helper()
	return primary{
		branch:   git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"),
		head:     git(t, dir, "rev-parse", "HEAD"),
		worktree: git(t, dir, "status", "--porcelain", "--untracked-files=all"),
	}
}

// refusal decodes a structured failure and holds it to a refusal the
// service decided against its records.
func refusal(t *testing.T, got invocation) machine.Failure {
	t.Helper()
	if got.code != machine.ExitFailure {
		t.Fatalf("exited %s, want failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("decoding the failure: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) {
		t.Fatalf("the failure is %+v, want a refusal", failure)
	}
	return failure
}

// runOf reads one run off the command surface.
func runOf(t *testing.T, h *home.Home, dir, id string) machine.Run {
	t.Helper()
	got := run(t, h, dir, "--json", "runs", id)
	if got.code != machine.ExitOK {
		t.Fatalf("assistant runs %s exited %s:\n%s%s", id, got.code, got.stdout, got.stderr)
	}
	return decodeRun(t, got.stdout)
}

// assertHeldAt holds the run to still waiting at stage.
func assertHeldAt(t *testing.T, h *home.Home, dir, id string, stage pipeline.Stage) {
	t.Helper()
	got := runOf(t, h, dir, id)
	if got.Record.Status != store.RunHeld || got.Decision == nil || got.Decision.Stage != stage.String() {
		t.Fatalf("run %s reads %s at %+v, want it still held at %s", id, got.Record.Status, got.Decision, stage)
	}
}
