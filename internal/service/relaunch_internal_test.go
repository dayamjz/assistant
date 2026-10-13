package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// providerProbe is the probe a service would be given once #91's last PR
// wires it: the liveness the service's own session provider answers, built
// through driverFor so the provider is the one the launches go through. A
// service that cannot resolve an agent observes nothing.
func providerProbe(s *Service) func(ctx context.Context, ref string) taskstate.Liveness {
	return func(ctx context.Context, ref string) taskstate.Liveness {
		built, err := s.driverFor(ctx)
		if err != nil {
			return taskstate.Unobserved
		}
		return built.sessions.Probe(ctx, ref).Liveness
	}
}

// relaunchSubject is a launchSubject whose first attempt's worker was
// launched and then ended, so its session reference probes Dead, standing
// in a service that has since been replaced: the second service is built
// over the same home, store and log with no slot and no session, which is
// what a restart leaves, and carries the probe over its own provider.
type relaunchSubject struct {
	*launchSubject
	first    *session.Session
	restart  *Service
	restartA *standin.Agent
}

// newRelaunchSubject dispatches and launches the task, ends its worker, and
// builds the restarted service.
func newRelaunchSubject(t *testing.T) *relaunchSubject {
	t.Helper()
	ctx := t.Context()
	sub := newLaunchSubject(t, workerStandIn(t))
	sub.service.probe = providerProbe(sub.service)
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	first, err := sub.service.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	provider, _, _ := sub.service.sessionOf(sub.task)
	for _, err := range sub.service.closeSessions() {
		t.Fatalf("ending the first worker: %v", err)
	}
	waitLiveness(t, provider, first.Reference().String(), taskstate.Dead)
	restart, agent := restartedService(t, sub)
	return &relaunchSubject{launchSubject: sub, first: first, restart: restart, restartA: agent}
}

// restartedService is a second hand-built service over the subject's home,
// store and log, resolving a worker-capable stand-in of its own, which it
// returns beside the service, with the probe over its own provider and a
// stop context for the launches it starts in the background.
func restartedService(t *testing.T, sub *launchSubject) (*Service, *standin.Agent) {
	t.Helper()
	agent := workerStandIn(t)
	s := &Service{
		home: sub.service.home, store: sub.service.store, log: sub.service.log,
		catalog: agents.NewCatalog(fixedRunner{runner: agent.Runner()}), registry: newRegistry(),
	}
	s.cfg = sub.service.cfg
	s.probe = providerProbe(s)
	s.stopCtx, s.stopCancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		s.stopWork()
		s.work.Wait()
		for _, err := range s.closeSessions() {
			t.Errorf("closing the restarted service's sessions: %v", err)
		}
		s.stopCancel()
	})
	return s, agent
}

// eventKinds is the task's history as a list of kinds, in sequence order.
func eventKinds(t *testing.T, records *store.Store, id string) []string {
	t.Helper()
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// endsWith reports whether kinds ends with the given suffix.
func endsWith(kinds []string, suffix ...string) bool {
	if len(kinds) < len(suffix) {
		return false
	}
	for i, want := range suffix {
		if kinds[len(kinds)-len(suffix)+i] != want {
			return false
		}
	}
	return true
}

// PRD principle P6 at the relaunch claim: two relaunchers of one dead
// terminal, held between their read of the attempts and their claim so the
// test is of the anchor and not of scheduling, begin one attempt between
// them. The loser is refused with ErrTaskMoved before it appends an event or
// asserts the copy, and the records show one new attempt and one relaunched
// note. Run under the race detector, which make test does.
func TestTwoRelaunchersOfOneDeadTerminalBeginOneAttempt(t *testing.T) {
	principles.Cite(t, principles.P6)
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	var arrived sync.WaitGroup
	arrived.Add(2)
	sub.restart.betweenAttemptReadAndClaim = func() {
		arrived.Done()
		arrived.Wait()
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	outs := make([]relaunched, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i], errs[i] = sub.restart.relaunchClaimed(ctx, sub.task)
		}()
	}
	wg.Wait()
	won, lost := -1, 0
	for i, err := range errs {
		switch {
		case err == nil:
			won = i
		case errors.Is(err, store.ErrTaskMoved):
			lost++
		default:
			t.Fatalf("a relauncher failed for a reason other than the claim: %v", err)
		}
	}
	if won < 0 || lost != 1 {
		t.Fatalf("the relaunchers answered %v, want one winner and one refused claim", errs)
	}
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the first and one relaunch", attempts, err)
	}
	if outs[won].launch.attempt != 2 || outs[won].superseded != sub.first.Reference().String() {
		t.Fatalf("the winner established %+v, want attempt 2 superseding %s", outs[won], sub.first.Reference())
	}
	kinds := eventKinds(t, sub.records, sub.task)
	relaunches := 0
	for _, k := range kinds {
		if k == relaunchedKind {
			relaunches++
		}
	}
	if relaunches != 1 || !endsWith(kinds, relaunchedKind, store.DispatchedEventKind) {
		t.Fatalf("the history is %v, want exactly one relaunched note followed by dispatched", kinds)
	}
}

// A relaunch reuses the clone the first attempt worked in: a file the first
// worker left in it is still there when the second worker starts, no second
// clone is cut, the start snapshot is the dispatch's, the relaunched note
// precedes the dispatched entry, and the launched worker reads the
// instructions file followed by the program's note. Afterwards the task
// reads Dispatched from its new attempt and not Unknown from the dead
// terminal, and a second pass of reconciliation relaunches nothing.
func TestARelaunchReusesTheStandingCopyAndASecondPassDoesNotRelaunchAgain(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	left := filepath.Join(sub.copy, "left-by-the-first-worker.txt")
	if err := os.WriteFile(left, []byte("progress\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := sub.records.Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sub.restart.tasks().Task(ctx, sub.task); err != nil ||
		got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess || got.Basis.Process != taskstate.Dead {
		t.Fatalf("before the relaunch the task resolves %+v (%v), want Unknown from the dead terminal", got, err)
	}

	sub.restart.reconcileTasks(ctx)
	sub.restart.work.Wait()

	_, launched, ok := sub.restart.sessionOf(sub.task)
	if !ok {
		t.Fatal("the restarted service holds no session after the relaunch")
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("the file the first worker left is gone, so the copy was not reused: %v", err)
	}
	after, err := sub.records.Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", after.StartSnapshot) != fmt.Sprintf("%+v", before.StartSnapshot) {
		t.Fatalf("the start snapshot moved from %+v to %+v; a relaunch records none", before.StartSnapshot, after.StartSnapshot)
	}
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 2 || attempts[1].SessionRef.Or("") != launched.Reference().String() {
		t.Fatalf("the task has attempts %+v (%v), want two with the second carrying %s", attempts, err, launched.Reference())
	}
	if kinds := eventKinds(t, sub.records, sub.task); !endsWith(kinds, relaunchedKind, store.DispatchedEventKind) {
		t.Fatalf("the history is %v, want it to end relaunched then dispatched", kinds)
	}
	got, err := sub.restart.tasks().Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskstate.Running || got.Source != taskstate.SourceProcess || got.Basis.Process != taskstate.Live {
		t.Fatalf("after the relaunch the task resolves %+v, want Running from the live terminal the restarted service holds", got)
	}
	// The worker read the instructions file and then the note, which is
	// not in the file.
	var worker standin.Call
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, call := range sub.restartA.Calls() {
			if call.Worker {
				worker = call
			}
		}
		if len(worker.Lines) > 0 && strings.Contains(strings.Join(worker.Lines, "\n"), "This is attempt 2 of this task") {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the relaunched worker never read the note; it read %v", worker.Lines)
		}
		time.Sleep(time.Millisecond)
	}
	text := strings.Join(worker.Lines, "\n")
	if !strings.Contains(text, "git rev-parse --show-toplevel") || !strings.Contains(text, "This is attempt 2 of this task") {
		t.Fatalf("the worker read:\n%s\nwant the instructions with the second check and then the note for attempt 2", text)
	}
	instructions, _ := sub.service.home.TaskInstructions(sub.task)
	if raw, _ := os.ReadFile(instructions); strings.Contains(string(raw), "Relaunched by the program") {
		t.Fatal("the note was written into the instructions file")
	}

	// A second pass: the task is Running from a live terminal, so nothing
	// is relaunched and no attempt is added.
	sub.restart.reconcileTasks(ctx)
	sub.restart.work.Wait()
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("after a second pass the task has attempts %+v (%v), want still two", attempts, err)
	}
}

// PRD principle P11 at the relaunch: the standing copy is asserted through
// the one assertion dispatch and launch share, and a copy that has become
// the primary checkout is refused with the primary-checkout token, recorded
// on the relaunch's own attempt after the relaunched note, so the task
// reads Failed from the refusal and no worker starts. The control is the
// same subject untouched, which TestARelaunchReusesTheStandingCopy
// launches. The retarget is the copy path replaced by a link to the
// primary, which is the shape the principle names.
func TestARelaunchRefusesACopyThatBecameThePrimary(t *testing.T) {
	principles.Cite(t, principles.P11)
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	if err := os.RemoveAll(sub.copy); err != nil {
		t.Fatal(err)
	}
	primary, _, _ := subjectPrimary(t, sub)
	if err := os.Symlink(primary, sub.copy); err != nil {
		t.Fatal(err)
	}

	_, err := sub.restart.relaunchTask(ctx, sub.task)
	var refused *isolationRefused
	if !errors.As(err, &refused) || !errors.Is(err, tasks.ErrNotIsolated) {
		t.Fatalf("the relaunch answered %v, want the typed isolation refusal", err)
	}
	if refused.Reason != taskstate.RefusedPrimaryCheckout || refused.Attempt != 2 {
		t.Fatalf("the refusal is %+v, want primary-checkout on attempt 2", refused)
	}
	if kinds := eventKinds(t, sub.records, sub.task); !endsWith(kinds, relaunchedKind, isolationRefusedKind) {
		t.Fatalf("the history is %v, want it to end relaunched then isolation-refused", kinds)
	}
	got, err := sub.restart.tasks().Task(ctx, sub.task)
	if err != nil || got.State != taskstate.Failed || got.Source != taskstate.SourceEvent {
		t.Fatalf("the task resolves %+v (%v), want Failed from the refusal", got, err)
	}
	if _, _, ok := sub.restart.sessionOf(sub.task); ok {
		t.Fatal("the restarted service holds a session for a refused relaunch")
	}
	for _, call := range sub.restartA.Calls() {
		if call.Worker {
			t.Fatalf("a worker was started for the refused relaunch: %s", call)
		}
	}
	// The table leaves the refused task alone: Failed from the event tier
	// with no terminal to probe is not a relaunch row.
	sub.restart.reconcileTasks(ctx)
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("reconciliation added an attempt past the refusal: %+v (%v)", attempts, err)
	}
}

// subjectPrimary reads the repository the subject's task works in.
func subjectPrimary(t *testing.T, sub *relaunchSubject) (primary, upstream, defaultBranch string) {
	t.Helper()
	task, err := sub.records.Task(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sub.records.Repository(t.Context(), task.RepositoryID.Or(""))
	if err != nil {
		t.Fatal(err)
	}
	return repository.WorkingPath, repository.UpstreamURL, repository.DefaultBranch
}

// A copy that no longer stands is refused with the copy-missing token and
// never cut again: PRD section 11's relaunch is in that same copy.
func TestARelaunchRefusesAMissingCopyRatherThanCuttingOne(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	if err := os.RemoveAll(sub.copy); err != nil {
		t.Fatal(err)
	}
	_, err := sub.restart.relaunchTask(ctx, sub.task)
	var refused *isolationRefused
	if !errors.As(err, &refused) || refused.Reason != taskstate.RefusedCopyMissing {
		t.Fatalf("the relaunch answered %v, want the copy-missing refusal", err)
	}
	if _, err := os.Lstat(sub.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something stands at the copy path after the refusal: %v", err)
	}
}

// The floor is re-run at the relaunch: a live task now holding the branch,
// here a second task on the same repository and branch whose worker reports
// itself working, holds the relaunch, which is refused naming the holder
// with no attempt begun and no event appended.
func TestARelaunchWaitsOnALiveTaskHoldingTheBranch(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	task, err := sub.records.Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	holderID, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	holderCopy, _ := sub.service.home.TaskCopy(holderID)
	if _, err := sub.records.CreateTask(ctx, store.Task{
		ID: holderID, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: holderCopy,
		RepositoryID: task.RepositoryID, Branch: task.Branch,
	}, store.Disposition{Observed: task.Row}); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.records.AppendTaskEvent(ctx, holderID, "working", "the successor's worker says so"); err != nil {
		t.Fatal(err)
	}
	kindsBefore := eventKinds(t, sub.records, sub.task)

	_, err = sub.restart.relaunchTask(ctx, sub.task)
	var held *relaunchHeld
	if !errors.As(err, &held) || held.Holder != holderID || held.State != taskstate.Running {
		t.Fatalf("the relaunch answered %v, want it held by %s, which is running", err, holderID)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 1 {
		t.Fatalf("the held relaunch began an attempt: %+v (%v)", attempts, err)
	}
	if kinds := eventKinds(t, sub.records, sub.task); strings.Join(kinds, ",") != strings.Join(kindsBefore, ",") {
		t.Fatalf("the held relaunch appended to the history: %v", kinds)
	}
}

// A task Failed from a failed run with its terminal dead is relaunched: the
// new attempt is the attribution cutoff, so the task reads Dispatched from
// its new attempt rather than Failed from the old run, and a second pass of
// reconciliation does not relaunch it again. Past relaunch_attempts the task
// surfaces instead, once, as a terminal-gone wake naming the bound, with no
// attempt begun; a later pass finds the wake queued and appends no second.
func TestAFailedRunIsRelaunchedUpToTheBoundAndThenSurfaced(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	sub.restart.cfg.RelaunchAttempts = 1
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	task, err := sub.records.Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	failRun := func(id string) {
		t.Helper()
		if _, err := sub.records.CreateRun(ctx, store.Run{
			ID: id, RepositoryID: task.RepositoryID.Or(""), Branch: task.Branch.Or(""), SubmittedHead: "0000000000000000000000000000000000000001",
			Base: "0000000000000000000000000000000000000000", Status: store.RunFailed, Build: build, ConfigDigest: "digest",
		}); err != nil {
			t.Fatal(err)
		}
	}
	failRun("run-one")
	if got, err := sub.restart.tasks().Task(ctx, sub.task); err != nil ||
		got.State != taskstate.Failed || got.Source != taskstate.SourceRun || got.Basis.Process != taskstate.Dead {
		t.Fatalf("the task resolves %+v (%v), want Failed from the run with a dead terminal", got, err)
	}

	// Within the bound: relaunched, and the task reads from its new attempt.
	sub.restart.reconcileTasks(ctx)
	sub.restart.work.Wait()
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the relaunch's second", attempts, err)
	}
	got, err := sub.restart.tasks().Task(ctx, sub.task)
	if err != nil || got.Source == taskstate.SourceRun || (got.State != taskstate.Dispatched && got.State != taskstate.Running) {
		t.Fatalf("after the relaunch the task resolves %+v (%v), want Dispatched or Running from the new attempt, not Failed from the old run", got, err)
	}
	sub.restart.reconcileTasks(ctx)
	sub.restart.work.Wait()
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("a second pass relaunched again: %+v (%v)", attempts, err)
	}

	// The second worker dies and its run fails too: the bound is reached.
	provider, launched, ok := sub.restart.sessionOf(sub.task)
	if !ok {
		t.Fatal("the restarted service holds no session after the relaunch")
	}
	for _, err := range sub.restart.closeSessions() {
		t.Fatal(err)
	}
	waitLiveness(t, provider, launched.Reference().String(), taskstate.Dead)
	failRun("run-two")
	sub.restart.reconcileTasks(ctx)
	sub.restart.work.Wait()
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 2 {
		t.Fatalf("the task was relaunched past the bound: %+v (%v)", attempts, err)
	}
	wakes, err := sub.records.TaskWakes(ctx, sub.task)
	if err != nil || len(wakes) != 1 {
		t.Fatalf("the task has wakes %+v (%v), want the one surface", wakes, err)
	}
	want := terminalGoneMarker(launched.Reference().String(), "died:"+surfacedRelaunchBound)
	if wakes[0].Kind != string(supervisor.KindTerminalGone) || wakes[0].Marker != want || !wakes[0].Queued() {
		t.Fatalf("the surface is %+v, want a queued terminal-gone wake with marker %s", wakes[0], want)
	}
	sub.restart.reconcileTasks(ctx)
	if wakes, err := sub.records.TaskWakes(ctx, sub.task); err != nil || len(wakes) != 1 {
		t.Fatalf("a later pass surfaced the task again: %+v (%v)", wakes, err)
	}
}

// The wake rows a relaunch supersedes, terminal-gone rows about the session
// it replaced, are acknowledged by the program; a terminal-gone row about
// another session and a row of another kind stay queued for the
// coordinator.
func TestARelaunchAcknowledgesOnlyTheWakesItSuperseded(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	ref := sub.first.Reference().String()
	append := func(kind, marker string) store.Wake {
		t.Helper()
		out, err := sub.records.AppendWakeWithMarkers(ctx, store.WakeRequest{TaskID: sub.task, Kind: kind, Marker: marker}, store.MarkerAdvance{})
		if err != nil {
			t.Fatal(err)
		}
		return out.Wake
	}
	superseded := append(string(supervisor.KindTerminalGone), terminalGoneMarker(ref, "died"))
	otherSession := append(string(supervisor.KindTerminalGone), terminalGoneMarker(ref+"0", "died"))
	otherKind := append(string(supervisor.KindHoldOpened), "hold:one")

	if _, err := sub.restart.relaunchTask(ctx, sub.task); err != nil {
		t.Fatalf("relaunching: %v", err)
	}
	wakes, err := sub.records.TaskWakes(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]store.Wake{}
	for _, w := range wakes {
		byID[w.ID] = w
	}
	if got := byID[superseded.ID]; got.Queued() || got.AcknowledgedBy.Or(store.Acknowledger{}) != store.AcknowledgedByProgram() {
		t.Fatalf("the superseded wake is %+v, want acknowledged by the program", got)
	}
	for name, w := range map[string]store.Wake{"another session's": otherSession, "another kind's": otherKind} {
		if got := byID[w.ID]; !got.Queued() {
			t.Errorf("%s wake was acknowledged: %+v", name, got)
		}
	}
	if queued, err := sub.records.DrainWakes(ctx, 10); err != nil || len(queued) != 2 {
		t.Fatalf("the queue holds %+v (%v), want the two rows the relaunch did not supersede", queued, err)
	}
}

// A second home on the same machine with a colliding task identifier: the
// task's session carries the other home's marker, so this home's provider
// answers Unobserved for it and never Dead, reconciliation leaves it alone,
// and a relaunch asked for directly is refused as not due. Nothing is
// recorded in either direction.
func TestATaskWhoseSessionIsAnotherHomesIsNotReconciled(t *testing.T) {
	ctx := t.Context()
	other, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = records.Close() })
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, _ := h.TaskCopy(id)
	if _, err := records.CreateTask(ctx, store.Task{ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	attempt, err := records.BeginTaskAttempt(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The other home's reference, naming a process that is not running, so
	// the one thing keeping it from reading Dead is the home marker.
	foreign := session.Reference{Home: session.HomeMarker(other), Task: id, PID: 1 << 30, Start: 1}
	if err := records.SetAttemptSession(ctx, id, attempt.Sequence, foreign.String()); err != nil {
		t.Fatal(err)
	}
	agent := workerStandIn(t)
	s := &Service{home: h, store: records, log: log, catalog: agents.NewCatalog(fixedRunner{runner: agent.Runner()}), registry: newRegistry()}
	s.probe = providerProbe(s)
	s.stopCtx, s.stopCancel = context.WithCancel(context.Background())
	t.Cleanup(s.stopCancel)

	got, err := s.tasks().Task(ctx, id)
	if err != nil || got.Basis.Process != taskstate.Unobserved {
		t.Fatalf("the task resolves %+v (%v), want its terminal unobserved", got, err)
	}
	s.reconcileTasks(ctx)
	s.work.Wait()
	if attempts, err := records.TaskAttempts(ctx, id); err != nil || len(attempts) != 1 {
		t.Fatalf("reconciliation relaunched another home's task: %+v (%v)", attempts, err)
	}
	if _, err := s.relaunchTask(ctx, id); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a relaunch of another home's task answered %v, want not due", err)
	}
	if kinds := eventKinds(t, records, id); !endsWith(kinds, store.DispatchedEventKind) || len(kinds) != 2 {
		t.Fatalf("the history is %v, want only the intake's two entries", kinds)
	}
}

// A service with no probe relaunches nothing and refuses a relaunch asked
// of it, because it cannot read a terminal as dead itself; and dispatch
// passes over a task whose latest attempt names a session, so a restart
// without a probe leaves the launched task alone rather than starting a
// second worker beside one it cannot see.
func TestWithoutAProbeNothingIsRelaunchedOrDispatchedAgain(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	sub.restart.probe = nil
	sub.restart.reconcileTasks(ctx)
	sub.restart.dispatchDue(ctx)
	sub.restart.work.Wait()
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 1 {
		t.Fatalf("a service with no probe began an attempt: %+v (%v)", attempts, err)
	}
	if _, held := sub.restart.isolationOf(sub.task); held {
		t.Fatal("dispatch took the slot of a task it passed over, which would keep a later relaunch out")
	}
	if _, err := sub.restart.relaunchTask(ctx, sub.task); !errors.Is(err, errRelaunchUnprobed) {
		t.Fatalf("a relaunch with no probe answered %v, want the unprobed refusal", err)
	}
	if _, err := sub.restart.dispatchClaimed(ctx, sub.task); !errors.Is(err, errTaskLaunched) {
		t.Fatalf("a dispatch of a launched task answered %v, want the launched refusal", err)
	}
}
