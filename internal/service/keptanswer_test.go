package service_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's land answer, through the real socket: a run's copy kept on
// an unpushed commit is an open entry holding a new task of its project. An
// answer of carry-on, a discard confirmed with a digest that is not the
// copy's, and a land naming the wrong owner are each refused and record
// nothing. Land then records the answer, brings the
// copy's commit into the gate and starts a run of the entry's branch at it
// the way a push does, names that run on the entry, and lifts the hold, so
// the waiting task dispatches. The same answer again names the same run and
// starts no second one.
func TestLandingKeptWorkStartsARunOfItsBranchAndLiftsTheHold(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		runByID(t, under.client, started.Record.ID)
		status := statusOf(t, under.client, subject)
		if len(status.KeptWork) != 1 {
			t.Fatalf("status lists kept work %+v, want the run's one entry", status.KeptWork)
		}
		entry := status.KeptWork[0]
		var created machine.TaskCreated
		if err := under.client.Call(ctx, ipc.MethodTaskCreate, machine.TaskCreateRequest{
			Shape: "delivery", Project: "one", Instructions: "Do the work.\n", Mode: "interactive",
			Repository: repository.ID, Branch: "task/new",
		}, &created); err != nil {
			t.Fatal(err)
		}
		waitLog(t, h, "task "+created.Record.ID+" waits: ")

		answer := func(r machine.KeptWorkAnswerRequest) (machine.KeptWorkAnswered, error) {
			var out machine.KeptWorkAnswered
			err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, r, &out)
			return out, err
		}
		for _, refused := range []machine.KeptWorkAnswerRequest{
			{Run: started.Record.ID, Hold: entry.Key(), Answer: string(store.KeptCarryOn)},
			{Run: started.Record.ID, Hold: entry.Key(), Answer: string(store.KeptDiscard), Confirm: "not-the-digest"},
			{Run: "some-other-run", Hold: entry.Key(), Answer: string(store.KeptLand)},
		} {
			if _, err := answer(refused); !errors.Is(err, ipc.ErrRefused) {
				t.Fatalf("answering %+v = %v; want it refused", refused, err)
			}
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after refused answers the entry reads %+v, %v; want it open", still, err)
		}

		landed, err := answer(machine.KeptWorkAnswerRequest{Run: started.Record.ID, Hold: entry.Key(), Answer: string(store.KeptLand)})
		if err != nil {
			t.Fatalf("landing the entry: %v", err)
		}
		if landed.Effect != machine.KeptWorkLanded || landed.Run == "" || landed.Run == started.Record.ID ||
			landed.Entry.Answer.Or("") != store.KeptLand || landed.Entry.LandedRun.Or("") != landed.Run {
			t.Fatalf("the landing answered %+v; want a new run named on the entry answered land", landed)
		}
		run, err := recordsOf(t, h).Run(ctx, landed.Run)
		if err != nil || run.Branch != "work" || run.SubmittedHead != submitted || run.RepositoryID != repository.ID {
			t.Fatalf("the landing's run reads %+v, %v; want a run of work at %s", run, err, submitted)
		}
		waitAttempts(t, h, created.Record.ID)

		again, err := answer(machine.KeptWorkAnswerRequest{Run: started.Record.ID, Hold: entry.Key(), Answer: string(store.KeptLand)})
		if err != nil || again.Run != landed.Run {
			t.Fatalf("landing again = %+v, %v; want the same run named", again, err)
		}
		runs, err := recordsOf(t, h).RunsForRepository(ctx, repository.ID)
		if err != nil || len(runs) != 2 {
			t.Fatalf("the repository has runs %d (%v); want the kept one and the landing's", len(runs), err)
		}
	})
}

// A land answer recorded by a service that stopped before it carried it out
// is completed by the next start: the entry answered land with no landing
// run, written straight to the store the way the stopped service left it,
// names a run of its branch at the copy's commit once the next service has
// opened. A landing whose copy holds a file no commit records is refused
// and records nothing, since a landing carries commits only. Without the
// completion at open the entry stays answered with no run.
func TestALandAnswerAStoppedServiceDidNotCarryOutIsCompletedAtTheNextStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")
	entry := keptEntryOf(t, h, subject)

	stray := entry.CopyPath + "/stray.txt"
	if err := os.WriteFile(stray, []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withService(t, h, func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptLand)}, &out)
		if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "no commit records") {
			t.Fatalf("landing a copy with an untracked file = %v; want it refused", err)
		}
	})
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}

	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if still, err := records.KeptWorkEntry(t.Context(), entry.ID); err != nil || !still.Open() {
		t.Fatalf("after the refused landing the entry reads %+v, %v; want it open", still, err)
	}
	if _, err := records.AnswerKeptWork(t.Context(), entry.ID, store.KeptLand, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
			if err == nil {
				if run, ok := got.LandedRun.Get(); ok {
					landed, err := recordsOf(t, h).Run(t.Context(), run)
					if err != nil || landed.Branch != "work" || landed.SubmittedHead != submitted {
						t.Fatalf("the completed landing names run %+v, %v; want a run of work at %s", landed, err, submitted)
					}
					return
				}
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not complete the landing; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// A landing a stopped service started and did not record is named rather
// than started twice: the land answer is made through the socket and the
// service stops at the seam between the landing's run and the entry's
// record of it, once that run has started validation, the state a stop
// there leaves. A run created after the claim and never started, a pending
// run with no submitted reference and no checkpoint, is then recorded
// beside it, the state a create stopped before its reference leaves. The
// next start names the run the landing started, not the unstarted one, and
// starts none. Without the look for the run the next start records a
// second run; without the started rule it may name the unstarted one.
func TestALandingWhoseRunWasStartedBeforeAStopIsNamedAndNotStartedAgain(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")
	entry := keptEntryOf(t, h, subject)

	stopped := errors.New("the service stopped between the landing's run and its record")
	var mu sync.Mutex
	var started string
	seam := func(run string) error {
		mu.Lock()
		started = run
		mu.Unlock()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := recordsOf(t, h).LatestGraphCheckpoint(t.Context(), run); err == nil {
				return stopped
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("run %s recorded no checkpoint: %w", run, stopped)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	withServiceOptions(t, service.WithLandingInterruptForTest(t, options(t, h), seam), func(under serviceUnderTest) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptLand)}, &out)
		if err == nil || !strings.Contains(err.Error(), "stopped between") || strings.Contains(err.Error(), "no checkpoint") {
			t.Fatalf("the landing past the stopped seam = %+v, %v; want the stop, with its run started", out, err)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if started == "" {
		t.Fatal("the landing reached no seam")
	}
	if got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || got.LandedRun.IsKnown() || got.Answer.Or("") != store.KeptLand {
		t.Fatalf("after the stop the entry reads %+v, %v; want it answered land with no run named", got, err)
	}

	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	const unstarted = "run-created-and-never-started"
	if _, err := records.CreateRun(t.Context(), store.Run{
		ID: unstarted, RepositoryID: repository.ID, Branch: "work", SubmittedHead: submitted,
		Base: submitted, Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := records.RunsForRepository(t.Context(), repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
			if run, ok := got.LandedRun.Get(); err == nil && ok {
				if run != started {
					t.Fatalf("the completed landing names run %s; want %s, the one the stopped landing started", run, started)
				}
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not complete the landing; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		after, err := recordsOf(t, h).RunsForRepository(t.Context(), repository.ID)
		if err != nil || len(after) != len(before) {
			t.Fatalf("the repository has %d runs (%v) after the completion; want %d, none started", len(after), err, len(before))
		}
	})
}

// A landing whose claimed runs never started validation is started again:
// the entry is answered land and claimed, and two runs of its branch at the
// copy's commit are recorded since, one terminated with no checkpoint and
// one still pending with no submitted reference and no checkpoint, the
// states a create stopped before its reference leaves. The next start names
// neither: it starts another, which executes, and names it. Without the
// started rule a run nothing of the work reached is named and the work
// never reaches validation.
func TestALandingWhoseRunEndedBeforeItExecutedIsStartedAgain(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")
	entry := keptEntryOf(t, h, subject)

	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.AnswerKeptWork(t.Context(), entry.ID, store.KeptLand, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := records.ClaimKeptWorkLanding(t.Context(), entry.ID); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	const ended = "run-ended-before-it-executed"
	if _, err := records.CreateRun(t.Context(), store.Run{
		ID: ended, RepositoryID: repository.ID, Branch: "work", SubmittedHead: submitted,
		Base: submitted, Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(t.Context(), ended, []store.RunStatus{store.RunPending}, store.RunTerminated); err != nil {
		t.Fatal(err)
	}
	const unstarted = "run-created-and-never-started"
	if _, err := records.CreateRun(t.Context(), store.Run{
		ID: unstarted, RepositoryID: repository.ID, Branch: "work", SubmittedHead: submitted,
		Base: submitted, Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err := recordsOf(t, h).KeptWorkEntry(t.Context(), entry.ID)
			if run, ok := got.LandedRun.Get(); err == nil && ok {
				if run == ended || run == unstarted {
					t.Fatalf("the completed landing names %s, a run that never started validation", run)
				}
				landed, err := recordsOf(t, h).Run(t.Context(), run)
				if err != nil || landed.Branch != "work" || landed.SubmittedHead != submitted || landed.Status == store.RunTerminated {
					t.Fatalf("the completed landing names run %+v, %v; want a run of work at %s that executes", landed, err, submitted)
				}
				return
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the next start did not complete the landing; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// keepTaskCopy asks the real cleanup decision of the task's copy, with its
// terminal dead, and records what it refuses as kept work through the real
// Keep, returning the entry Keep wrote or found for the copy as it stands.
func keepTaskCopy(t *testing.T, h *home.Home, id string) store.KeptWork {
	t.Helper()
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	cleaner, err := tasks.NewCleaner(tasks.CleanupDeps{Home: h, Store: records,
		Forge: forge.NewGitHubHost(redact.New(), forge.WithBinary(filepath.Join(t.TempDir(), "absent-gh")))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: taskstate.Dead})
	var refused *tasks.CleanupRefusal
	if !errors.As(err, &refused) {
		t.Fatalf("the cleanup of task %s's copy = %v, want a refusal over work on no remote", id, err)
	}
	kept, ok := tasks.KeptOf(refused)
	if !ok {
		t.Fatalf("the refusal %v is not kept work", refused)
	}
	got, err := cleaner.Keep(ctx, kept)
	if err != nil {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	return got.Entry
}

// A land answer carries HEAD, so an entry whose default branch lacks a
// commit HEAD does not reach is refused naming that commit, and records
// nothing. The entries are the real cleanup's, through Keep, over a
// delivery task's clone of the upstream holding its own commit on its
// branch and a second commit on another branch, HEAD on the first. Kept
// with the upstream unreadable, the entry is not established and a land is
// refused naming that; kept again with the upstream back, the same entry
// is established lacking both commits and a land is refused naming the
// one HEAD does not reach; with that branch deleted the copy is kept as a
// new entry lacking HEAD's commit alone, and that entry lands. Without the
// check the entry is answered and the other commit is never carried.
func TestALandingIsRefusedWhenHEADLeavesAHeldCommitBehind(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	upstream := repository.UpstreamURL
	const branch = "task/kept"
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "the parser", Mode: "headless", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known(branch),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginTaskAttempt(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
	if err := records.SetAttemptSession(t.Context(), id, 1, "pane-gone"); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(copyPath), "clone", "--quiet", upstream, copyPath)
	git(t, copyPath, "checkout", "--quiet", "-b", branch)
	if err := writeFile(filepath.Join(copyPath, "own.txt"), "the task's own work\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "the task's own commit")
	own := git(t, copyPath, "rev-parse", "HEAD")
	git(t, copyPath, "checkout", "--quiet", "-b", "aside")
	if err := writeFile(filepath.Join(copyPath, "aside.txt"), "work on another branch\n"); err != nil {
		t.Fatal(err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "work on another branch")
	aside := git(t, copyPath, "rev-parse", "HEAD")
	git(t, copyPath, "checkout", "--quiet", branch)

	if err := os.Rename(upstream, upstream+".gone"); err != nil {
		t.Fatal(err)
	}
	unestablished := keepTaskCopy(t, h, id)
	if err := os.Rename(upstream+".gone", upstream); err != nil {
		t.Fatal(err)
	}
	if unestablished.Established() {
		t.Fatalf("the copy kept with the upstream unreadable reads %+v; want it not established", unestablished)
	}
	land := func(under serviceUnderTest, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
		var out machine.KeptWorkAnswered
		err := under.client.Call(t.Context(), ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
			Task: id, Hold: entry.Key(), Answer: string(store.KeptLand)}, &out)
		return out, err
	}
	withService(t, h, func(under serviceUnderTest) {
		if _, err := land(under, unestablished); !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "not established") {
			t.Fatalf("landing an unestablished entry = %v; want it refused as not established", err)
		}
	})

	both := keepTaskCopy(t, h, id)
	if both.ID != unestablished.ID || !both.Established() || !slices.Contains(both.Lacks, own) || !slices.Contains(both.Lacks, aside) {
		t.Fatalf("the copy kept again reads %+v; want entry %d established lacking %s and %s", both, unestablished.ID, own, aside)
	}
	withService(t, h, func(under serviceUnderTest) {
		if _, err := land(under, both); !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "holding "+aside+", which") {
			t.Fatalf("landing an entry HEAD does not reach all of = %v; want it refused naming %s alone", err, aside)
		}
		if still, err := recordsOf(t, h).KeptWorkEntry(t.Context(), both.ID); err != nil || !still.Open() {
			t.Fatalf("after the refusal the entry reads %+v, %v; want it open", still, err)
		}
	})

	git(t, copyPath, "branch", "--quiet", "-D", "aside")
	reaches := keepTaskCopy(t, h, id)
	if reaches.ID == both.ID || !slices.Equal(reaches.Lacks, []string{own}) {
		t.Fatalf("the copy kept without the other branch reads %+v; want a new entry lacking %s alone", reaches, own)
	}
	withService(t, h, func(under serviceUnderTest) {
		if out, err := land(under, reaches); err != nil || out.Run == "" {
			t.Fatalf("landing the entry HEAD reaches all of = %+v, %v; want a run", out, err)
		}
	})
}

// Two land answers to one entry start one run between them: both are made
// at once, each ends as the run named or as refused while the other was
// landing, the entry names exactly one run, the repository has exactly one
// run of the branch beside the kept one, and a later answer names that run.
// Without the claim both start a run and one supersedes the other.
func TestTwoLandAnswersStartOneRunBetweenThem(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptLand)}
		type outcome struct {
			out machine.KeptWorkAnswered
			err error
		}
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				client, err := ipc.Dial(ctx, under.service.Socket(), ipc.ClientConfig{})
				if err != nil {
					results <- outcome{err: err}
					return
				}
				defer func() { _ = client.Close() }()
				var out machine.KeptWorkAnswered
				err = client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
				results <- outcome{out: out, err: err}
			}()
		}
		named := ""
		for range 2 {
			r := <-results
			switch {
			case r.err == nil && r.out.Run != "":
				if named != "" && named != r.out.Run {
					t.Fatalf("the two answers named runs %s and %s", named, r.out.Run)
				}
				named = r.out.Run
			case errors.Is(r.err, ipc.ErrRefused):
			default:
				t.Fatalf("an answer = %+v, %v; want the run named or a refusal while the other was landing", r.out, r.err)
			}
		}
		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Run == "" {
			t.Fatalf("a later answer = %+v, %v; want the run named", again, err)
		}
		if named != "" && again.Run != named {
			t.Fatalf("a later answer names %s, want %s", again.Run, named)
		}
		runs, err := recordsOf(t, h).RunsForRepository(ctx, repository.ID)
		if err != nil || len(runs) != 2 {
			t.Fatalf("the repository has %d runs (%v); want the kept one and the landing's", len(runs), err)
		}
	})
}

// A second land answer made while one is between its claim and its run is
// refused rather than starting a second run: the first answer is held at
// the seam after its claim, a second answer is made there and refused as
// being landed now with no run started, and the first then starts the one
// run, which a later answer names. Without the in-process slot the second
// answer finds the claim written and no run, and starts one of its own.
func TestALandAnswerDuringAnothersStartIsRefused(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)

	withService(t, h, func(under serviceUnderTest) {
		ctx := t.Context()
		request := machine.KeptWorkAnswerRequest{Run: entry.RunID.Or(""), Hold: entry.Key(), Answer: string(store.KeptLand)}
		inWindow := false
		under.service.SetBetweenLandingClaimAndStartForTest(func() {
			if inWindow {
				return
			}
			inWindow = true
			client, err := ipc.Dial(ctx, under.service.Socket(), ipc.ClientConfig{})
			if err != nil {
				t.Errorf("dialing inside the window: %v", err)
				return
			}
			defer func() { _ = client.Close() }()
			var out machine.KeptWorkAnswered
			err = client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &out)
			if !errors.Is(err, ipc.ErrRefused) || !strings.Contains(err.Error(), "being landed") {
				t.Errorf("a second answer inside the window = %+v, %v; want it refused as being landed now", out, err)
			}
			if runs, err := recordsOf(t, h).RunsForRepository(ctx, repository.ID); err != nil || len(runs) != 1 {
				t.Errorf("inside the window the repository has %d runs (%v); want the kept one alone", len(runs), err)
			}
		})
		var first machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &first); err != nil || first.Run == "" {
			t.Fatalf("the first answer = %+v, %v", first, err)
		}
		if !inWindow {
			t.Fatal("the first answer never reached the window between its claim and its run")
		}
		var again machine.KeptWorkAnswered
		if err := under.client.Call(ctx, ipc.MethodKeptWorkAnswer, request, &again); err != nil || again.Run != first.Run {
			t.Fatalf("a later answer = %+v, %v; want run %s named", again, err, first.Run)
		}
		if runs, err := recordsOf(t, h).RunsForRepository(ctx, repository.ID); err != nil || len(runs) != 2 {
			t.Fatalf("the repository has %d runs (%v); want the kept one and the landing's", len(runs), err)
		}
	})
}
