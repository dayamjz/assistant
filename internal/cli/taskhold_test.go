package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
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
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A task past relaunch_attempts holds for a person, and only that person's
// answer, given the task through --answer with --task, relaunches it, once.
// Everything is driven through the command surface and a real service over
// a real home: the task's worker is the worker-capable stand-in the service
// launches, a terminal is gone because the service that held it closed, and
// every relaunch is the service's own anchored one.
//
// With the bound at zero, the first start after the worker is gone begins
// no attempt and registers a hold naming the task. An answer the hold does
// not offer is refused and leaves it open. "retry" closes it, recorded as
// the machine interface since nothing on the socket witnessed a person, and
// begins exactly one attempt, in the same copy, whose note says the bound
// was passed on that answer. When that attempt's worker is gone too, the
// next start holds again on a new hold for the new attempt rather than
// relaunching on the spent answer. "stop" closes that one, and a later start
// neither relaunches nor holds again, and with nothing open a further answer
// is refused. While each hold is open the task reads Held from PRD section
// 6's hold tier off the command surface, naming that hold, and the line a
// person reads says it waits on them and how to answer; once both are
// answered it reads what its records give it again, with nothing appended
// to its history saying so, which is P8's half. P6 is the anchor that makes
// the answer good for one attempt; P13 is the human rendering of the answer
// and of the held task, held to the derived forbidden set.
func TestATaskPastItsRelaunchBoundHoldsUntilAnAnswerRetriesItOnce(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	vocabtest.Control(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"relaunch_attempts": 0, "supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The gate, then a dispatched task the next start launches.
	stop := serveWorkers(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	stop()
	id := recordDispatchedTask(t, h, subject)
	stop = serveWorkers(t, h)
	waitSessions(t, h, id, 1)
	stop()

	// Past the bound: held, not relaunched.
	stop = serveWorkers(t, h)
	first := openHoldsOf(t, h, id)
	if len(first) != 1 || first[0].RunID.IsKnown() {
		t.Fatalf("the task past its bound has open holds %+v, want one naming it and no run", first)
	}
	if n := len(attemptsOf(t, h, id)); n != 1 {
		t.Fatalf("the task past its bound has %d attempts, want the first alone", n)
	}
	heldBy(t, h, subject, id, first[0].Key)

	// An answer the hold does not offer is the caller's mistake: refused as
	// an invalid request, exiting as incorrect usage, and recording nothing.
	refusedAs(t, run(t, h, subject, "--json", "--task", id, "--answer", "approve"), ipc.CodeInvalidRequest, machine.ExitUsage)
	if still := openHoldsOf(t, h, id); len(still) != 1 || still[0].Key != first[0].Key {
		t.Fatalf("a refused answer moved the task's holds to %+v", still)
	}

	// Retry: one attempt, recorded as the answer it rested on.
	retried := answerTask(t, h, subject, id, machine.AnswerRetry)
	if retried.Effect != machine.AnswerRelaunched || retried.Relaunch == nil || retried.Relaunch.Attempt != 2 {
		t.Fatalf("retry answered %+v, want attempt 2 relaunched", retried)
	}
	resolved := holdOf(t, h, first[0].Key)
	if resolved.Resolution.Or("") != machine.AnswerRetry || resolved.ResolvedBy.Or(store.Resolver{}) != store.ResolvedByMachineInterface() {
		t.Fatalf("the hold after retry is %+v, want resolved retry by the machine interface", resolved)
	}
	if retried.Hold.Key != first[0].Key {
		t.Fatalf("the answer names hold %s, want %s", retried.Hold.Key, first[0].Key)
	}
	waitSessions(t, h, id, 2)
	if detail := lastRelaunched(t, h, id); !strings.Contains(detail, "attempt 2") ||
		!strings.Contains(detail, "relaunch_attempts is 0") || !strings.Contains(detail, "answered "+machine.AnswerRetry) {
		t.Fatalf("the relaunched note says %q, want attempt 2, the bound and the answer", detail)
	}
	stop()

	// The retried attempt's terminal is gone too: held again, on a new hold.
	stop = serveWorkers(t, h)
	second := openHoldsOf(t, h, id)
	if len(second) != 1 || second[0].Key == first[0].Key {
		t.Fatalf("after the retried attempt ended the open holds are %+v, want one new hold beside the answered %s", second, first[0].Key)
	}
	if n := len(attemptsOf(t, h, id)); n != 2 {
		t.Fatalf("the spent retry relaunched again: %d attempts, want 2", n)
	}
	heldBy(t, h, subject, id, second[0].Key)

	// Stop, read as a person reads it.
	plain := run(t, h, subject, "--task", id, "--answer", machine.AnswerStop)
	if plain.code != machine.ExitOK {
		t.Fatalf("answering stop exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(plain.stdout), "\n") {
		vocabtest.Assert(t, line)
	}
	if got := holdOf(t, h, second[0].Key); got.Resolution.Or("") != machine.AnswerStop {
		t.Fatalf("the second hold after stop is %+v, want resolved stop", got)
	}
	stop()

	// Settled: a later start neither relaunches nor holds again.
	stop = serveWorkers(t, h)
	if open := openHoldsOf(t, h, id); len(open) != 0 {
		t.Fatalf("a task answered stop holds again: %+v", open)
	}
	if task := taskOf(t, h, subject, id); task.State.State == taskstate.Held || task.State.Basis.Hold != "" {
		t.Fatalf("a task whose holds are all answered resolves %s from %s on hold %q, want what its records give it",
			task.State.State, task.State.Source, task.State.Basis.Hold)
	}
	if n := len(attemptsOf(t, h, id)); n != 2 {
		t.Fatalf("a task answered stop was relaunched: %d attempts", n)
	}
	// With nothing open the answer is refused against the records, which is
	// an operational failure and the control that only an invalid request
	// moved to incorrect usage.
	refusedAs(t, run(t, h, subject, "--json", "--task", id, "--answer", machine.AnswerRetry), ipc.CodeRefused, machine.ExitFailure)
	stop()
}

// PRD section 11's "Recovering an investigation": a decision an
// investigation's report registered is closed by the same answer that
// closes a run's hold, given the task, with no run anywhere. Two holds are
// answered oldest first, each recorded as given and by the machine
// interface, and with both closed a third answer is refused. No working
// copy is needed: the answer is given from a directory that is not one.
func TestAnInvestigationsHoldIsAnsweredGivenTheTask(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()

	id := "investigat23"
	records := openStore(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	raw := `{"summary":"two decisions","findings":[` +
		`{"severity":"warning","action":"ask","description":"Whether the old index may be dropped."},` +
		`{"severity":"warning","description":"Whether the cache should move."}]}`
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
	if len(holds) != 2 {
		t.Fatalf("the report holds %d findings, want both: one ask and one with no action, which P3 makes an ask", len(holds))
	}
	if _, _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: raw, Report: encoded}, holds); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	var answered []string
	for _, answer := range []string{"approved", "skipped"} {
		got := answerTask(t, h, elsewhere, id, answer)
		if got.Effect != machine.AnswerRecorded || got.Hold.Resolution.Or("") != answer {
			t.Fatalf("answering %q reports %+v, want the hold recorded as answered", answer, got)
		}
		// Who resolved is read from the record: a store.Resolver does not
		// travel, which is internal/store's design and not a gap here.
		if stored := holdOf(t, h, got.Hold.Key); stored.Resolution.Or("") != answer ||
			stored.ResolvedBy.Or(store.Resolver{}) != store.ResolvedByMachineInterface() {
			t.Fatalf("answering %q left the hold %+v, want it resolved as given by the machine interface", answer, stored)
		}
		answered = append(answered, got.Hold.Key)
	}
	if answered[0] == answered[1] {
		t.Fatalf("both answers closed hold %s", answered[0])
	}
	if open := openHoldsOf(t, h, id); len(open) != 0 {
		t.Fatalf("after two answers the investigation still holds %+v", open)
	}
	if got := run(t, h, elsewhere, "--json", "--task", id, "--answer", "approved"); got.code != machine.ExitFailure {
		t.Fatalf("a third answer exited %s, want failure:\n%s", got.code, got.stdout)
	}
}

// Issue #279: a task with three report holds can answer the middle one by
// name (finding id), with a note, and the note is on the hold's record.
// Oldest-first is no longer the only path.
func TestANamedTaskHoldIsAnsweredWithANote(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)
	elsewhere := t.TempDir()

	id := "namedholds23"
	records := openStore(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/named",
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	raw := `{"summary":"three decisions","findings":[` +
		`{"severity":"warning","action":"ask","description":"Whether the floor is decided."},` +
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
	held := report.Held()
	if len(held) != 3 {
		t.Fatalf("the report holds %d findings, want three", len(held))
	}
	var holds []store.Hold
	for _, f := range held {
		holds = append(holds, store.Hold{Key: tasks.HoldKey(id, f.ID), TaskID: store.Known(id), Subject: f.Description, Detail: f.Description})
	}
	if _, _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: raw, Report: encoded}, holds); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	middle := held[1]
	note := "the floor is not decided, and no name on it"
	got := run(t, h, elsewhere, "--json", "--task", id, "--hold", middle.ID, "--answer", "approved", "--note", note)
	if got.code != machine.ExitOK {
		t.Fatalf("answering the middle hold exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var answered machine.TaskAnswered
	if err := json.Unmarshal([]byte(got.stdout), &answered); err != nil {
		t.Fatalf("decoding: %v\n%s", err, got.stdout)
	}
	wantKey := tasks.HoldKey(id, middle.ID)
	if answered.Hold.Key != wantKey || answered.Hold.Resolution.Or("") != "approved" || answered.Effect != machine.AnswerRecorded {
		t.Fatalf("the answer reports %+v, want hold %s recorded approved", answered, wantKey)
	}
	if gotNote, ok := answered.Hold.ResolutionNote.Get(); !ok || gotNote != note {
		t.Fatalf("the answer carries resolution note %+v, want %q", answered.Hold.ResolutionNote, note)
	}
	stored := holdOf(t, h, wantKey)
	if gotNote, ok := stored.ResolutionNote.Get(); !ok || gotNote != note {
		t.Fatalf("the hold record carries note %+v, want %q", stored.ResolutionNote, note)
	}
	open := openHoldsOf(t, h, id)
	if len(open) != 2 {
		t.Fatalf("after naming the middle hold the task still holds %+v, want the other two", open)
	}
	for _, remaining := range open {
		if remaining.Key == wantKey {
			t.Fatalf("the named hold is still open: %+v", open)
		}
	}
}

// A note is free text a person types, so a credential pasted into one is
// removed before it is stored, the way a message to a worker's is: an
// answer given --note through the command line reads back, in its own
// answer and in the record, with the words kept and the credential gone.
func TestATaskHoldsNoteIsStoredRedacted(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serve(t, h)

	id := "redactnote23"
	records := openStore(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/redact",
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	raw := `{"summary":"one decision","findings":[{"severity":"warning","action":"ask","description":"Whether the mirror may move."}]}`
	report, err := findings.ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	f := report.Held()[0]
	key := tasks.HoldKey(id, f.ID)
	if _, _, err := records.StoreTaskReport(t.Context(), store.TaskReport{TaskID: id, Body: raw, Report: encoded},
		[]store.Hold{{Key: key, TaskID: store.Known(id), Subject: f.Description, Detail: f.Description}}); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	got := run(t, h, t.TempDir(), "--json", "--task", id, "--hold", f.ID, "--answer", "approved",
		"--note", "move it to https://mirror:hunter2@example.invalid/repo.git next week")
	if got.code != machine.ExitOK {
		t.Fatalf("the answer exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout, "hunter2") {
		t.Fatalf("the answer printed the credential:\n%s", got.stdout)
	}
	var answered machine.TaskAnswered
	if err := json.Unmarshal([]byte(got.stdout), &answered); err != nil {
		t.Fatalf("decoding: %v\n%s", err, got.stdout)
	}
	for _, hold := range []store.Hold{answered.Hold, holdOf(t, h, key)} {
		note, ok := hold.ResolutionNote.Get()
		if !ok || strings.Contains(note, "hunter2") || !strings.Contains(note, "example.invalid/repo.git next week") {
			t.Fatalf("the hold reads back the note %q (known %v), want the words kept and the credential removed", note, ok)
		}
	}
}

// --task is read only with --answer or --standing-consent and takes a task,
// and it acts on one thing at a time. Each is incorrect usage, decided before
// any service is asked. A fix round given --task is not among them: it goes
// to the task's run, which taskrun_test.go drives, and neither is consent
// given a task alone, which consent_test.go drives.
func TestTheTaskFlagIsReadOnlyWithAnAnswerOrConsent(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	dir := t.TempDir()
	for _, args := range [][]string{
		{"--task", "abcdefghij23"},
		{"--task", "abcdefghij23", "--standing-consent=false"},
		{"--task", "", "--answer", "retry"},
		{"--task", "", "--standing-consent"},
		{"--task", "abcdefghij23", "--answer", "retry", "--cancel"},
		{"--hold", "finding-1", "--answer", "approved"},
		{"--run", "--answer", "approved"},
		{"--task", "abcdefghij23", "--hold", "finding-1", "--run", "--answer", "approved"},
		{"--task", "abcdefghij23", "--hold", "", "--answer", "approved"},
		{"--task", "abcdefghij23", "--hold", "finding-1", "--answer", string(pipeline.OutcomeFixRequested)},
		{"--task", "abcdefghij23", "--run", "--answer", "approved", "--note", "ignored"},
		{"--task", "abcdefghij23", "--answer", "retry", "--intent", "something"},
		{"--task", "abcdefghij23", "--answer", "retry", "--standing-consent"},
		{"--task", "abcdefghij23", "--standing-consent", "--intent", "something"},
		{"--task", "abcdefghij23", "--standing-consent", "--skip", "lint"},
		{"--task", "abcdefghij23", "--standing-consent", "--cancel"},
	} {
		if got := run(t, h, dir, args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s", strings.Join(args, " "), got.code, got.stderr)
		}
	}
}

// refusedAs holds a --json invocation to a refusal that crossed the wire as
// code and exited with exit.
func refusedAs(t *testing.T, got invocation, code ipc.Code, exit machine.Code) {
	t.Helper()
	if got.code != exit {
		t.Fatalf("the answer exited %s, want %s:\n%s%s", got.code, exit, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not a failure document: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(code) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, code, failure.Error)
	}
}

// heldBy reads the task off the command surface while key is open and holds
// it to the hold tier: the structured rendering resolves Held from the hold
// and names key, and the line a person reads says the work waits on them and
// how to answer it, with no word the derived forbidden set keeps from them.
func heldBy(t *testing.T, h *home.Home, dir, id, key string) {
	t.Helper()
	task := taskOf(t, h, dir, id)
	if task.State.State != taskstate.Held || task.State.Source != taskstate.SourceHold || task.State.Basis.Hold != key {
		t.Fatalf("the task with %s open resolves %s from %s on hold %q, want held from the hold", key,
			task.State.State, task.State.Source, task.State.Basis.Hold)
	}
	plain := run(t, h, dir, "tasks", id)
	if plain.code != machine.ExitOK {
		t.Fatalf("assistant tasks %s exited %s:\n%s%s", id, plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stdout, "waiting on an answer from you") || !strings.Contains(plain.stdout, "assistant --task") ||
		!strings.Contains(plain.stdout, "--answer") {
		t.Fatalf("the task reads %q, want it to say it waits on you and how to answer", plain.stdout)
	}
	for _, line := range strings.Split(strings.TrimSpace(plain.stdout), "\n") {
		vocabtest.Assert(t, line)
	}
}

// taskOf reads one task's structured answer off the command surface.
func taskOf(t *testing.T, h *home.Home, dir, id string) machine.Task {
	t.Helper()
	got := run(t, h, dir, "--json", "tasks", id)
	if got.code != machine.ExitOK {
		t.Fatalf("assistant --json tasks %s exited %s:\n%s%s", id, got.code, got.stdout, got.stderr)
	}
	var task machine.Task
	if err := json.Unmarshal([]byte(got.stdout), &task); err != nil {
		t.Fatalf("decoding the task: %v\n%s", err, got.stdout)
	}
	return task
}

// answerTask answers a task's hold through the surface and decodes what it
// reported.
func answerTask(t *testing.T, h *home.Home, dir, id, answer string) machine.TaskAnswered {
	t.Helper()
	got := run(t, h, dir, "--json", "--task", id, "--answer", answer)
	if got.code != machine.ExitOK {
		t.Fatalf("answering task %s %q exited %s:\n%s%s", id, answer, got.code, got.stdout, got.stderr)
	}
	var answered machine.TaskAnswered
	if err := json.Unmarshal([]byte(got.stdout), &answered); err != nil {
		t.Fatalf("decoding: %v\n%s", err, got.stdout)
	}
	return answered
}

// serveWorkers serves the home with the worker-capable stand-in as the
// resolved agent, the one adapter in this build that declares worker
// sessions, and returns what stops it. The worker greets and lingers until
// the service ends it.
func serveWorkers(t *testing.T, h *home.Home) func() {
	t.Helper()
	agent := standin.NewWorker(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{
			Greeting:  "worker ready",
			Exchanges: []standin.Exchange{{Contains: "This is attempt", Reply: "the relaunch note was read"}},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	return serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))
}

// recordDispatchedTask writes, where a coordinator's intake would have, a
// delivery task on the subject's repository that resolves Dispatched: the
// record with its copy reserved at home.TaskCopy and the instructions file.
// Nothing in this build creates a task through the surface.
func recordDispatchedTask(t *testing.T, h *home.Home, subject string) string {
	t.Helper()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	repository, err := records.RepositoryAt(t.Context(), subject)
	if err != nil {
		t.Fatalf("reading the repository init recorded: %v", err)
	}
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known("task/one"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	instructions, err := h.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(instructions), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte("Do the work the coordinator asked for.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

// waitSessions waits until the task has n attempts and the newest records
// the session its worker was launched in, which the launch writes once the
// process has started.
func waitSessions(t *testing.T, h *home.Home, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts := attemptsOf(t, h, id)
		if len(attempts) == n && attempts[n-1].SessionRef.IsKnown() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("task %s never had %d attempts with the newest launched: %+v", id, n, attempts)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func attemptsOf(t *testing.T, h *home.Home, id string) []store.TaskAttempt {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

// openHoldsOf is every open hold naming the task.
func openHoldsOf(t *testing.T, h *home.Home, id string) []store.Hold {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	open, err := records.OpenHolds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Hold
	for _, hold := range open {
		if hold.TaskID.Or("") == id {
			out = append(out, hold)
		}
	}
	return out
}

func holdOf(t *testing.T, h *home.Home, key string) store.Hold {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	hold, err := records.Hold(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return hold
}

// lastRelaunched is the detail of the task's newest "relaunched" note.
func lastRelaunched(t *testing.T, h *home.Home, id string) string {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "relaunched" {
			return events[i].Detail
		}
	}
	t.Fatalf("task %s has no relaunched note: %+v", id, events)
	return ""
}

// quietGit turns git's automatic maintenance off for every repository the
// test's processes touch, so a background gc never races a copy the sweep
// is asked to clear.
func quietGit(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
}

// requireSweepClears skips where a relaunch cannot clear a copy at all: the
// relaunch asks agents.Sweeper of the standing copy before it claims, and on
// a host with a process whose working directory the sweep cannot read, every
// sweep refuses to clear, as P6 requires, so no relaunch is made there. It is
// internal/service's guard of the same name, asked of the same sweeper.
func requireSweepClears(t *testing.T) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := agents.NewSweeper(h, 0, nil).Sweep(t.Context(), dir)
	switch {
	case errors.Is(err, agents.ErrSweepUnsupported), err == nil && r.Clear():
		return
	case err != nil:
		t.Fatalf("sweeping an empty copy for the host's baseline: %v", err)
	}
	t.Skipf("this host has process(es) whose working directory the sweep cannot read (%s), so no relaunch clears a copy here",
		agents.DescribeBlockers(r.UnreadableBlocking))
}
