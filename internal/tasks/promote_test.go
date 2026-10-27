package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/store/storetest"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The promotion tests run over real repositories, on the cleanup tests'
// terms: an upstream the repository record names, a primary checkout cloned
// from it, which is the working copy the record names and the assertion
// holds a copy distinct from, a gate repository under the home carrying the
// primary's main, and a task copy cut from the primary the way a dispatch
// cuts an investigation copy. The worker's own entries are appended through
// the store, which is the path a worker's event reaches it by; no session is
// started, because a promotion launches nothing.

// promotionSubject is a home, a store, the repositories and the queue a
// promotion is run over, with the cleaner the race test runs beside it.
type promotionSubject struct {
	home      *home.Home
	store     *store.Store
	queue     *Queue
	cleaner   *Cleaner
	primary   string
	gate      string
	inGate    map[string]bool
	exclusion *keyedExclusion
	// posture is what the posture reader answers for the project, and
	// postureErr, when set, fails it instead, as an unreadable trusted copy
	// does. postureAsked counts the reads; it is atomic because the race test
	// promotes on a goroutine of its own.
	posture      config.Delivery
	postureErr   error
	postureAsked atomic.Int32
}

func newPromotionSubject(t *testing.T) *promotionSubject {
	t.Helper()
	ctx := context.Background()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n"+
		"[init]\n\tdefaultBranch = main\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"+
		"[url \""+filepath.ToSlash(upstream)+"\"]\n\tinsteadOf = "+upstreamURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	if err := os.Mkdir(upstream, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, upstream, "init", "-q", "--bare", "-b", "main")
	primary := filepath.Join(t.TempDir(), "checkout")
	rawGit(t, filepath.Dir(primary), "clone", "-q", upstream, primary)
	if err := os.WriteFile(filepath.Join(primary, "f.txt"), []byte(twelveLines), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, primary, "add", "f.txt")
	rawGit(t, primary, "commit", "-q", "-m", "base")
	rawGit(t, primary, "push", "-q", "origin", "main")

	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("home.Open: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("home.Create: %v", err)
	}
	gatePath := filepath.Join(root, "repos", "subject.git")
	if err := os.MkdirAll(filepath.Dir(gatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.InitBare(ctx, gatePath); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	rawGit(t, primary, "push", "-q", gatePath, "main:main")

	// Seeded for time alone, as internal/store/storetest's comment says.
	storetest.Seed(t, h.Database())
	s, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: cleanupRepository, WorkingPath: primary, UpstreamURL: upstreamURL, DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	ps := &promotionSubject{home: h, store: s, primary: primary, gate: gatePath,
		inGate: map[string]bool{}, exclusion: &keyedExclusion{locks: map[string]*sync.Mutex{}},
		posture: config.DeliveryGated}
	ps.queue, err = New(Deps{
		Home: h, Store: s, Reader: taskstate.NewReader(s, nil, nil), Exclude: ps.exclusion.hold,
		BranchInGate: func(_ context.Context, r store.Repository, b string) (bool, error) {
			return ps.inGate[r.ID+"\x00"+b], nil
		},
		GatePath: func(context.Context, store.Repository) (string, error) { return gatePath, nil },
		Posture: func(context.Context, store.Repository, string) (config.Delivery, error) {
			ps.postureAsked.Add(1)
			return ps.posture, ps.postureErr
		},
		GitDeadline: vcs.DefaultInvocationDeadline,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ps.cleaner, err = NewCleaner(CleanupDeps{Home: h, Store: s, Forge: forgetest.New(t, nil).Host(),
		GitDeadline: vcs.DefaultInvocationDeadline})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}
	return ps
}

// investigation records an investigation task of the repository with its
// first attempt begun and its copy cut, and appends the worker's "started"
// entry so the task resolves Running.
func (s *promotionSubject) investigation(t *testing.T) task {
	t.Helper()
	tk := s.cut(t, store.Unknown[string]())
	s.workerSays(t, tk.id, "started", "here")
	return tk
}

// cut records an investigation task of the repository, queued behind the
// blocker when one is named, begins its first attempt, cuts its copy as a
// dispatch does, a clone of the primary with no remote detached at main,
// records the start snapshot and writes the instructions file. No worker
// entry is appended, so the task resolves Dispatched or Queued.
func (s *promotionSubject) cut(t *testing.T, blocker store.Optional[string]) task {
	t.Helper()
	ctx := context.Background()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := s.home.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(cleanupRepository),
	}, store.Disposition{Blocker: blocker}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.store.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.Clone(ctx, s.primary, copyPath, ""); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, copyPath, "switch", "-q", "--detach")
	if err := s.store.SetTaskStartSnapshot(ctx, id, readSnapshot(t, copyPath)); err != nil {
		t.Fatalf("SetTaskStartSnapshot: %v", err)
	}
	instructions, err := s.home.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte("look into the thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return task{id: id, copy: copyPath}
}

// workerSays appends a worker's entry, the way a worker's event path does.
func (s *promotionSubject) workerSays(t *testing.T, id, kind, detail string) {
	t.Helper()
	if _, err := s.store.AppendTaskEvent(context.Background(), id, kind, detail); err != nil {
		t.Fatalf("AppendTaskEvent: %v", err)
	}
}

// request is a well-formed promotion of the task onto topic.
func request(id string) Promotion {
	return Promotion{By: store.ResolvedByMachineInterface(), Task: id, Repository: cleanupRepository, Branch: "topic", Intent: "make the change the report found",
		Environment: cleanEnv()}
}

// promote is Promote or a fatal failure.
func (s *promotionSubject) promote(t *testing.T, req Promotion) Promoted {
	t.Helper()
	got, err := s.queue.Promote(context.Background(), req)
	if err != nil {
		t.Fatalf("Promote(%+v): %v", req, err)
	}
	return got
}

// record reads the task's record.
func (s *promotionSubject) record(t *testing.T, id string) store.Task {
	t.Helper()
	got, err := s.store.Task(context.Background(), id)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	return got
}

// history reads the task's whole history, for accounting and never for
// its state.
func (s *promotionSubject) history(t *testing.T, id string) []store.TaskEvent {
	t.Helper()
	events, err := s.store.TaskEvents(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	return events
}

// resolve reads the task's state through the resolver.
func (s *promotionSubject) resolve(t *testing.T, id string) taskstate.Resolution {
	t.Helper()
	got, err := taskstate.NewReader(s.store, nil, nil).Task(context.Background(), id)
	if err != nil {
		t.Fatalf("resolving %s: %v", id, err)
	}
	return got
}

// untouched asserts the task is still the investigation task it was, with
// no entry past the n it had.
func (s *promotionSubject) untouched(t *testing.T, tk task, n int) {
	t.Helper()
	record := s.record(t, tk.id)
	if record.Shape != store.TaskInvestigation || record.Branch.IsKnown() || record.DeliveryIntent.IsKnown() {
		t.Fatalf("the refused promotion wrote the record: %+v", record)
	}
	if history := s.history(t, tk.id); len(history) != n {
		t.Fatalf("the refused promotion appended: %+v", history)
	}
	if remotes := rawGit(t, tk.copy, "remote"); remotes != "" {
		t.Fatalf("the refused promotion wrote a remote into the copy: %q", remotes)
	}
}

// A promotion flips the shape, the repository and the branch in place and
// shapes the standing copy: afterwards the copy has exactly one remote,
// assistant at this home's gate, HEAD is on the task's branch at the commit
// the investigation had reached, the assertion passes on the delivery
// expectation, the intent is on the task's record and in the instructions,
// and the promoted entry is in the history. A second promotion refuses on the shape, a task in a
// state outside the table refuses with nothing written, and an anchor the
// history moved past refuses with nothing written.
func TestPromoteFlipsTheTaskInPlaceAndShapesItsCopy(t *testing.T) {
	ctx := context.Background()

	t.Run("a running investigation with its own commit", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		work := commit(t, tk.copy, "g.txt", "found while investigating\n", "investigation's commit")
		before := len(s.history(t, tk.id))

		got := s.promote(t, request(tk.id))
		if got.Task.Shape != store.TaskDelivery || got.Task.RepositoryID.Or("") != cleanupRepository || got.Task.Branch.Or("") != "topic" {
			t.Fatalf("the task reads %+v, want delivery on %s topic", got.Task, cleanupRepository)
		}
		if stored := s.record(t, tk.id); stored.DeliveryIntent != store.Known("make the change the report found") {
			t.Fatalf("the stored task records the delivery intent %v, want the request's", stored.DeliveryIntent)
		}
		if got.Event.Kind != store.PromotedEventKind || got.Event.Sequence != int64(before+1) ||
			!strings.Contains(got.Event.Detail, "no report stored") {
			t.Fatalf("the promoted entry is %+v", got.Event)
		}
		if got.Head != work {
			t.Errorf("the branch was created at %s, want the investigation's HEAD %s", got.Head, work)
		}
		if got.Report.IsKnown() {
			t.Errorf("a promotion with no report stored points at %+v", got.Report)
		}
		if !got.Isolation.Proven() || got.Isolation.Shape != store.TaskDelivery || got.Isolation.Branch != "topic" {
			t.Errorf("the proof is %+v, want proven for delivery on topic", got.Isolation)
		}
		obs := observe(t, tk.copy)
		if len(obs.Remotes) != 1 || obs.Remotes[0].Name != gate.RemoteName || len(obs.Remotes[0].URLs) != 1 {
			t.Fatalf("the copy's remotes are %+v, want exactly %s", obs.Remotes, gate.RemoteName)
		}
		if resolved, _ := filepath.EvalSymlinks(s.gate); filepath.Clean(obs.Remotes[0].URLs[0]) != s.gate && filepath.Clean(obs.Remotes[0].URLs[0]) != resolved {
			t.Errorf("the remote is at %s, want the gate %s", obs.Remotes[0].URLs[0], s.gate)
		}
		if obs.Detached || obs.Branch != "topic" {
			t.Errorf("HEAD is on %q (detached %v), want topic", obs.Branch, obs.Detached)
		}
		if head := rawGit(t, tk.copy, "rev-parse", "HEAD"); head != work {
			t.Errorf("HEAD is %s, want %s", head, work)
		}
		want, line, err := Expect(s.home, tk.id, store.TaskDelivery, s.gate, s.primary, "topic")
		if err != nil {
			t.Fatalf("Expect: %v", err)
		}
		if _, err := AssertIsolated(obs, cleanEnv(), want); err != nil {
			t.Fatalf("the promoted copy fails the assertion: %v", err)
		}
		text, err := os.ReadFile(got.Instructions)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(text), "look into the thing") || !strings.Contains(string(text), PromotionHeading) ||
			!strings.Contains(string(text), "make the change the report found") || !strings.Contains(string(text), line) {
			t.Errorf("the instructions read:\n%s\nwant the intake text, the heading, the intent and the second check", text)
		}
		history := s.history(t, tk.id)
		if history[len(history)-1].Kind != store.PromotedEventKind {
			t.Errorf("the history ends on %q, want the promoted entry", history[len(history)-1].Kind)
		}
		// The promoted entry is a note, so the event tier declines until the
		// worker's next entry, which is the resolver's rule for every note.
		if r := s.resolve(t, tk.id); r.State != taskstate.Unknown || r.Source != taskstate.SourceNone {
			t.Errorf("after the promotion the task resolves %s from %s, want unknown from none", r.State, r.Source)
		}

		// A second promotion refuses on the shape, and writes nothing.
		_, err = s.queue.Promote(ctx, Promotion{By: store.ResolvedByMachineInterface(), Task: tk.id, Repository: cleanupRepository, Branch: "other",
			Intent: "again", Environment: cleanEnv()})
		var notPromotable *NotPromotableError
		if !errors.As(err, &notPromotable) || !errors.Is(err, ErrNotPromotable) || notPromotable.Shape != store.TaskDelivery {
			t.Fatalf("a second promotion = %v, want a *NotPromotableError naming the delivery shape", err)
		}
		if again := s.record(t, tk.id); again.Branch.Or("") != "topic" {
			t.Errorf("the second promotion moved the branch: %+v", again)
		}
		if after := s.history(t, tk.id); len(after) != len(history) {
			t.Errorf("the second promotion appended: %+v", after)
		}
	})

	t.Run("a reported investigation with its report stored points at the row", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		s.workerSays(t, tk.id, "report-ready", "done")
		stored, err := s.cleaner.StoreReport(ctx, tk.id, aReport)
		if err != nil {
			t.Fatalf("StoreReport: %v", err)
		}
		got := s.promote(t, request(tk.id))
		pointer, ok := got.Report.Get()
		if !ok || pointer.Task != tk.id || !pointer.RecordedAt.Equal(stored.Record.RecordedAt) {
			t.Fatalf("the promotion points at %+v, want the report row of %s recorded at %s", got.Report, tk.id, stored.Record.RecordedAt)
		}
		if !strings.Contains(got.Event.Detail, "is evidence") || strings.Contains(got.Event.Detail, "looked at the thing") {
			t.Errorf("the entry's detail is %q, want the pointer and none of the report's text", got.Event.Detail)
		}
		text, _ := os.ReadFile(got.Instructions)
		if strings.Contains(string(text), "looked at the thing") || strings.Contains(string(text), "typo") {
			t.Errorf("the report's text reached the instructions:\n%s", text)
		}
	})

	t.Run("a state outside the table refuses with nothing written", func(t *testing.T) {
		s := newPromotionSubject(t)
		for _, state := range []taskstate.State{taskstate.Failed, taskstate.Unknown, taskstate.Dispatched, taskstate.Queued} {
			if Promotable(state) {
				t.Fatalf("%s is in the table", state)
			}
			var tk task
			switch state {
			case taskstate.Failed:
				tk = s.investigation(t)
				s.workerSays(t, tk.id, "failed", "gave up")
			case taskstate.Unknown:
				tk = s.investigation(t)
				s.workerSays(t, tk.id, "progress-note", "a note at the tail")
			case taskstate.Dispatched:
				tk = s.cut(t, store.Unknown[string]())
			case taskstate.Queued:
				blocker := s.investigation(t)
				tk = s.cut(t, store.Known(blocker.id))
			}
			n := len(s.history(t, tk.id))
			if r := s.resolve(t, tk.id); r.State != state {
				t.Fatalf("the task built for %s resolves %s", state, r.State)
			}
			_, err := s.queue.Promote(ctx, request(tk.id))
			var notPromotable *NotPromotableError
			if !errors.As(err, &notPromotable) || notPromotable.State != state {
				t.Fatalf("promoting a %s task = %v, want a *NotPromotableError naming %s", state, err, state)
			}
			s.untouched(t, tk, n)
		}
		if got := PromotableStates(); len(got) != 4 {
			t.Errorf("the table is %v, want the four states", got)
		}
	})

	t.Run("a history that moved past the read refuses with nothing written", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		s.queue.betweenReadAndWrite = func() { s.workerSays(t, tk.id, "working", "still at it") }
		n := len(s.history(t, tk.id))
		_, err := s.queue.Promote(ctx, request(tk.id))
		var moved *store.TaskMovedError
		if !errors.As(err, &moved) || !errors.Is(err, store.ErrTaskMoved) || moved.Anchor != store.AnchoredEvent {
			t.Fatalf("a promotion the history outran = %v, want a *store.TaskMovedError on the event anchor", err)
		}
		s.untouched(t, tk, n+1)
		s.queue.betweenReadAndWrite = nil
		// Read again and decide again: the worker's entry says Running, and
		// the promotion anchored to it goes through.
		s.promote(t, request(tk.id))
	})
}

// PRD principle P11: a worker does not launch unless the resolved path is a
// real isolated copy, distinct from the primary checkout, and being a linked
// copy is not proof on its own. What the citation claims is the assertion's
// reach into a promotion: a copy that was pointed at the primary's .git is
// refused before a byte is written into it, so the person's checkout is
// never given the remote; a copy that is gone is refused as missing; and a
// copy a worker gave a second remote is refused after the shaping, on the
// shape. Each refusal is in the task's history as the isolation-refused
// entry anchored to the promoted one, so the task is a delivery task that
// resolves Failed, and the copy is where and as it was found. Every
// observation is vcs.Observe's of a repository the test built.
func TestPromoteRefusesACopyThatIsNotIsolatedAndRecordsIt(t *testing.T) {
	principles.Cite(t, principles.P11)
	ctx := context.Background()

	refused := func(t *testing.T, err error, reason taskstate.IsolationRefusal, written bool) *PromotionRefusal {
		t.Helper()
		var refusal *PromotionRefusal
		if !errors.As(err, &refusal) || !errors.Is(err, ErrPromotionRefused) {
			t.Fatalf("got %v, want a *PromotionRefusal", err)
		}
		if refusal.Reason != reason || refusal.Written != written {
			t.Fatalf("refused as %s (written %v): %v; want %s, written %v", refusal.Reason, refusal.Written, refusal.Cause, reason, written)
		}
		return refusal
	}
	recorded := func(t *testing.T, s *promotionSubject, id string, refusal *PromotionRefusal) {
		t.Helper()
		if record := s.record(t, id); record.Shape != store.TaskDelivery {
			t.Fatalf("after the refusal the task is %+v, want a delivery task", record)
		}
		history := s.history(t, id)
		last := history[len(history)-1]
		if last.Kind != isolationRefusedKind || last.Detail != string(refusal.Reason) || last.Sequence != refusal.Event.Sequence ||
			history[len(history)-2].Kind != store.PromotedEventKind {
			t.Fatalf("the history ends %+v, want the refusal right after the promoted entry", history[len(history)-2:])
		}
		if r := s.resolve(t, id); r.State != taskstate.Failed || r.Source != taskstate.SourceEvent {
			t.Errorf("the task resolves %s from %s, want failed from the event", r.State, r.Source)
		}
	}

	t.Run("a copy pointed at the primary's .git refuses before the primary is written to", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		if err := os.RemoveAll(filepath.Join(tk.copy, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tk.copy, ".git"), []byte("gitdir: "+filepath.Join(s.primary, ".git")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.queue.Promote(ctx, request(tk.id))
		refusal := refused(t, err, taskstate.RefusedNotIsolated, false)
		if !errors.Is(err, ErrNotIsolated) {
			t.Errorf("%v does not match ErrNotIsolated", err)
		}
		recorded(t, s, tk.id, refusal)
		if remotes := rawGit(t, s.primary, "remote"); remotes != "origin" {
			t.Fatalf("the primary's remotes are %q after the refusal, want its own origin and nothing else", remotes)
		}
		if branches := rawGit(t, s.primary, "branch", "--list", "topic"); branches != "" {
			t.Fatalf("the primary gained the task's branch: %q", branches)
		}
	})

	t.Run("a copy that is gone refuses as missing", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		if err := os.RemoveAll(tk.copy); err != nil {
			t.Fatal(err)
		}
		_, err := s.queue.Promote(ctx, request(tk.id))
		recorded(t, s, tk.id, refused(t, err, taskstate.RefusedCopyMissing, false))
	})

	t.Run("a copy the worker gave a second remote refuses after the shaping, on the shape", func(t *testing.T) {
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		rawGit(t, tk.copy, "remote", "add", "elsewhere", s.primary)
		_, err := s.queue.Promote(ctx, request(tk.id))
		recorded(t, s, tk.id, refused(t, err, taskstate.RefusedWrongShape, true))
		if remotes := rawGit(t, tk.copy, "remote"); remotes != gate.RemoteName+"\nelsewhere" {
			t.Fatalf("the copy's remotes are %q, want the gate's beside the worker's, as found", remotes)
		}
	})
}

// A promotion and a cleanup of one task race on the store's one writer
// connection and exactly one loses: both are held after their decisions and
// before their claims, released together, and afterwards the copy is either
// promoted and standing, with the cleanup refused on the shape, or removed
// with its record, with the promotion refused on the record; never both. The
// two orders are then each driven on their own, so the test does not rest on
// which the scheduler picked.
func TestPromoteRacingCleanupHasExactlyOneLoser(t *testing.T) {
	ctx := context.Background()
	// scratch is an investigation task a cleanup proves scratch: its copy
	// unchanged since its dispatch, its report stored, its terminal dead.
	scratch := func(t *testing.T) (*promotionSubject, task) {
		t.Helper()
		s := newPromotionSubject(t)
		tk := s.investigation(t)
		s.workerSays(t, tk.id, "report-ready", "done")
		if _, err := s.cleaner.StoreReport(ctx, tk.id, aReport); err != nil {
			t.Fatalf("StoreReport: %v", err)
		}
		return s, tk
	}
	promotedAndStanding := func(t *testing.T, s *promotionSubject, tk task, cleanupErr error) {
		t.Helper()
		refusedWith(t, cleanupErr, ReasonShapeMoved)
		stillStands(t, tk)
		if record := s.record(t, tk.id); record.Shape != store.TaskDelivery || record.Branch.Or("") != "topic" {
			t.Fatalf("the task reads %+v, want promoted", record)
		}
		if _, err := s.store.TaskCleanup(ctx, tk.id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a refused cleanup left a record: %v", err)
		}
		if remotes := rawGit(t, tk.copy, "remote"); remotes != gate.RemoteName {
			t.Fatalf("the promoted copy's remotes are %q", remotes)
		}
	}
	removedAndRecorded := func(t *testing.T, s *promotionSubject, tk task, promoteErr error) {
		t.Helper()
		if !errors.Is(promoteErr, store.ErrCleanupRecorded) {
			t.Fatalf("the losing promotion = %v, want store.ErrCleanupRecorded", promoteErr)
		}
		if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the copy still stands after the cleanup: %v", err)
		}
		record, err := s.store.TaskCleanup(ctx, tk.id)
		if err != nil || record.Proof != store.CleanupProofScratch || !record.RemovedAt.IsKnown() {
			t.Fatalf("the cleanup record is %+v (%v), want scratch with the removal marked", record, err)
		}
		if task := s.record(t, tk.id); task.Shape != store.TaskInvestigation || task.Branch.IsKnown() {
			t.Fatalf("the losing promotion wrote the record: %+v", task)
		}
		for _, e := range s.history(t, tk.id) {
			if e.Kind == store.PromotedEventKind {
				t.Fatalf("the losing promotion appended: %+v", e)
			}
		}
	}

	t.Run("held at their claims and released together", func(t *testing.T) {
		s, tk := scratch(t)
		arrive := barrier(2)
		s.queue.betweenReadAndWrite = arrive
		s.cleaner.betweenDecisionAndClaim = arrive
		var promoteErr, cleanupErr error
		var cleanup Cleanup
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, promoteErr = s.queue.Promote(ctx, request(tk.id))
		}()
		go func() {
			defer wg.Done()
			cleanup, cleanupErr = s.cleaner.CleanUp(ctx, TaskCopy{Task: tk.id, Terminal: taskstate.Dead})
		}()
		wg.Wait()
		switch {
		case promoteErr == nil && cleanupErr != nil:
			promotedAndStanding(t, s, tk, cleanupErr)
		case promoteErr != nil && cleanupErr == nil:
			if cleanup.Result != CleanupRemoved {
				t.Fatalf("the winning cleanup = %+v, want removed", cleanup)
			}
			removedAndRecorded(t, s, tk, promoteErr)
		default:
			t.Fatalf("promotion %v and cleanup %v: want exactly one loser", promoteErr, cleanupErr)
		}
	})

	t.Run("the promotion claims first", func(t *testing.T) {
		s, tk := scratch(t)
		s.cleaner.betweenDecisionAndClaim = func() { s.promote(t, request(tk.id)) }
		_, err := s.cleaner.CleanUp(ctx, TaskCopy{Task: tk.id, Terminal: taskstate.Dead})
		promotedAndStanding(t, s, tk, err)
	})

	t.Run("the cleanup claims first", func(t *testing.T) {
		s, tk := scratch(t)
		s.queue.betweenReadAndWrite = func() {
			got, err := s.cleaner.CleanUp(ctx, TaskCopy{Task: tk.id, Terminal: taskstate.Dead})
			if err != nil || got.Result != CleanupRemoved {
				t.Errorf("the cleanup inside the window = %+v, %v; want removed", got, err)
			}
		}
		_, err := s.queue.Promote(ctx, request(tk.id))
		removedAndRecorded(t, s, tk, err)
	})
}

// A crash between the flip and the copy leaves a delivery task whose copy
// has no remote and a detached HEAD, which is safe: the record says
// delivery, the copy pushes nowhere, and a cleanup holds it to the three
// proofs like any delivery copy rather than treating it as scratch, which
// is visible in the refusal it answers: unproven over the three attempts,
// and not the investigation path's report rule.
func TestACrashBetweenTheFlipAndTheCopyLeavesASafeDeliveryTask(t *testing.T) {
	ctx := context.Background()
	s := newPromotionSubject(t)
	tk := s.investigation(t)
	commit(t, tk.copy, "g.txt", "found while investigating\n", "investigation's commit")
	s.queue.betweenFlipAndCopy = func() { panic("the process died here") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the promotion ran past the planted crash")
			}
		}()
		_, _ = s.queue.Promote(ctx, request(tk.id))
	}()
	if record := s.record(t, tk.id); record.Shape != store.TaskDelivery || record.Branch.Or("") != "topic" {
		t.Fatalf("after the crash the task reads %+v, want delivery on topic", record)
	}
	if history := s.history(t, tk.id); history[len(history)-1].Kind != store.PromotedEventKind {
		t.Fatalf("after the crash the history ends on %q", history[len(history)-1].Kind)
	}
	if remotes := rawGit(t, tk.copy, "remote"); remotes != "" {
		t.Fatalf("the copy has remotes %q, want none", remotes)
	}
	if obs := observe(t, tk.copy); !obs.Detached {
		t.Fatalf("the copy's HEAD is on %q, want detached", obs.Branch)
	}
	_, err := s.cleaner.CleanUp(ctx, TaskCopy{Task: tk.id, Terminal: taskstate.Dead})
	refusal := refusedWith(t, err, ReasonUnproven)
	if len(refusal.Attempts) != 3 {
		t.Fatalf("the refusal tried %d proofs, want the delivery copy's three: %+v", len(refusal.Attempts), refusal.Attempts)
	}
	stillStands(t, tk)
	// And the half-promoted task is not promoted again: it is a delivery
	// task now.
	s.queue.betweenFlipAndCopy = nil
	if _, err := s.queue.Promote(ctx, request(tk.id)); !errors.Is(err, ErrNotPromotable) {
		t.Fatalf("promoting the half-promoted task = %v, want ErrNotPromotable", err)
	}
}

// The branch a promotion asks for is held to intake's rules: a branch no
// task has worked on that exists in the gate or has a run is a person's and
// refuses; a branch a live task holds refuses naming the holder, which is
// the floor, and the person is told to queue behind it; a malformed request,
// a repository the copy was not cut from and a missing intent refuse. None
// of them writes.
func TestPromoteHoldsTheBranchToIntakesRules(t *testing.T) {
	ctx := context.Background()
	s := newPromotionSubject(t)

	t.Run("a branch in the gate", func(t *testing.T) {
		tk := s.investigation(t)
		n := len(s.history(t, tk.id))
		s.inGate[cleanupRepository+"\x00topic"] = true
		defer delete(s.inGate, cleanupRepository+"\x00topic")
		_, err := s.queue.Promote(ctx, request(tk.id))
		var taken *BranchTakenError
		if !errors.As(err, &taken) || taken.Held != HeldByGate || !IsRefusal(err) {
			t.Fatalf("a branch in the gate = %v, want a *BranchTakenError held by the gate", err)
		}
		s.untouched(t, tk, n)
	})

	t.Run("a branch with a run", func(t *testing.T) {
		tk := s.investigation(t)
		n := len(s.history(t, tk.id))
		if _, err := s.store.CreateRun(ctx, store.Run{
			ID: "run-topic", RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
			Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		_, err := s.queue.Promote(ctx, request(tk.id))
		var taken *BranchTakenError
		if !errors.As(err, &taken) || taken.Held != HeldByRun || taken.Run != "run-topic" {
			t.Fatalf("a branch with a run = %v, want a *BranchTakenError held by run-topic", err)
		}
		s.untouched(t, tk, n)
		// Another branch is still a task's to take.
		req := request(tk.id)
		req.Branch = "elsewhere"
		s.promote(t, req)
	})

	t.Run("a branch a live task holds", func(t *testing.T) {
		holder := s.investigation(t)
		req := request(holder.id)
		req.Branch = "held"
		s.promote(t, req)
		tk := s.investigation(t)
		n := len(s.history(t, tk.id))
		req.Task = tk.id
		_, err := s.queue.Promote(ctx, req)
		var held *BranchHeldError
		if !errors.As(err, &held) || !errors.Is(err, ErrBranchHeld) || held.Holder != holder.id || held.Branch != "held" {
			t.Fatalf("a held branch = %v, want a *BranchHeldError naming %s", err, holder.id)
		}
		if !Live(held.HolderState) {
			t.Errorf("the holder's state %s is not live", held.HolderState)
		}
		s.untouched(t, tk, n)
	})

	t.Run("a malformed request", func(t *testing.T) {
		tk := s.investigation(t)
		n := len(s.history(t, tk.id))
		for name, change := range map[string]func(*Promotion){
			"no task":            func(p *Promotion) { p.Task = "" },
			"no repository":      func(p *Promotion) { p.Repository = "" },
			"no branch":          func(p *Promotion) { p.Branch = " " },
			"a bad branch name":  func(p *Promotion) { p.Branch = "-x" },
			"no intent":          func(p *Promotion) { p.Intent = "" },
			"another repository": func(p *Promotion) { p.Repository = "repo-2" },
		} {
			req := request(tk.id)
			change(&req)
			if name == "another repository" {
				if _, err := s.store.UpsertRepository(ctx, store.Repository{
					ID: "repo-2", WorkingPath: filepath.Join(t.TempDir(), "two"), UpstreamURL: "https://example.test/two.git", DefaultBranch: "main",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.queue.Promote(ctx, req); !errors.Is(err, ErrRequest) {
				t.Errorf("%s = %v, want ErrRequest", name, err)
			}
		}
		absent, err := home.NewTaskID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.queue.Promote(ctx, Promotion{By: store.ResolvedByMachineInterface(), Task: absent, Repository: cleanupRepository, Branch: "b", Intent: "i"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a task that does not exist = %v, want store.ErrNotFound", err)
		}
		s.untouched(t, tk, n)
	})
}

// A promotion over kept work verifies the copy again after its flip, just
// before it reads or writes the copy: the copy has an open kept work entry
// and verifies at the start, and between the flip and the copy its .git is
// made a link to another clone's .git. The promotion is refused as not
// isolated with nothing written, the other clone's remotes and branches are
// as they were, and the flip stands, the entry answered carry on, as a
// refusal after the flip leaves it. Without the second verification the
// promotion writes the gate's remote and the branch into the other clone.
func TestPromoteVerifiesTheKeptCopyAgainBeforeWritingIt(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P1)
	ctx := context.Background()
	s := newPromotionSubject(t)
	tk := s.investigation(t)
	entry, _, err := s.store.RecordKeptWork(ctx, store.KeptCopy{TaskID: tk.id, CopyPath: tk.copy,
		Holds: []string{rawGit(t, tk.copy, "rev-parse", "HEAD")}, Reason: string(ReasonUnproven), Against: "mmmm"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	rawGit(t, filepath.Dir(other), "clone", "-q", s.primary, other)
	remotes, branches := rawGit(t, other, "remote"), rawGit(t, other, "branch", "--list")
	own := filepath.Join(tk.copy, ".git")
	s.queue.betweenFlipAndCopy = func() {
		if err := os.Rename(own, own+".own"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Join(other, ".git"), own); err != nil {
			t.Error(err)
		}
	}

	_, err = s.queue.Promote(ctx, request(tk.id))
	var refusal *PromotionRefusal
	if !errors.As(err, &refusal) || refusal.Reason != taskstate.RefusedNotIsolated || refusal.Written ||
		!errors.Is(err, ErrLayoutUnsupported) {
		t.Fatalf("promoting a kept copy whose .git became a link after the flip = %v; want refused as not isolated, nothing written", err)
	}
	if got := rawGit(t, other, "remote"); got != remotes {
		t.Errorf("the other clone's remotes became %q, were %q", got, remotes)
	}
	if got := rawGit(t, other, "branch", "--list"); got != branches {
		t.Errorf("the other clone's branches became %q, were %q", got, branches)
	}
	if closed, err := s.store.KeptWorkEntry(ctx, entry.ID); err != nil || closed.Answer.Or("") != store.KeptCarryOn {
		t.Fatalf("after the refusal the entry reads %+v, %v; want the flip's carry-on answer standing", closed, err)
	}
}

// A copy whose .git is a symbolic link to another clone's .git is refused
// before anything is written into that clone, whether or not the copy has
// an open kept work entry (round 37). The clone linked to has no remote and
// stands on its own branch, which is the case that escaped: with no entry
// nothing asked the kept-copy verification, every identity comparison of
// the assertion followed the link, and once the shaping had written the
// gate's remote and the task's branch into that clone, it was exactly the
// delivery shape, so the assertion after shaping passed it and the
// promotion succeeded. Now the assertion before shaping refuses the link
// as the repository directory comparison, with the refusal recorded and
// the clone linked to unchanged.
func TestPromoteRefusesACopyWhoseRepositoryDirectoryIsALink(t *testing.T) {
	principles.Cite(t, principles.P11)
	principles.Cite(t, principles.P1)
	ctx := context.Background()
	s := newPromotionSubject(t)
	tk := s.investigation(t)
	other := filepath.Join(t.TempDir(), "other")
	rawGit(t, filepath.Dir(other), "clone", "-q", s.primary, other)
	rawGit(t, other, "remote", "remove", "origin")
	if remotes := rawGit(t, other, "remote"); remotes != "" {
		t.Fatalf("the other clone has remotes %q, want none", remotes)
	}
	head, branches := rawGit(t, other, "symbolic-ref", "HEAD"), rawGit(t, other, "branch", "--list")
	own := filepath.Join(tk.copy, ".git")
	if err := os.RemoveAll(own); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git"), own); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}

	_, err := s.queue.Promote(ctx, request(tk.id))
	var refusal *PromotionRefusal
	if !errors.As(err, &refusal) || refusal.Reason != taskstate.RefusedNotIsolated || refusal.Written ||
		!errors.Is(err, ErrNotIsolated) {
		t.Fatalf("promoting a copy whose .git links to another clone = %v; want refused as not isolated, nothing written", err)
	}
	var ie *IsolationError
	if !errors.As(err, &ie) || ie.Comparison != ComparisonGitDir {
		t.Errorf("refused on %v, want the repository directory comparison", err)
	}
	if got := rawGit(t, other, "remote"); got != "" {
		t.Errorf("the other clone's remotes became %q, were none", got)
	}
	if got := rawGit(t, other, "symbolic-ref", "HEAD"); got != head {
		t.Errorf("the other clone's HEAD became %s, was %s", got, head)
	}
	if got := rawGit(t, other, "branch", "--list"); got != branches {
		t.Errorf("the other clone's branches became %q, were %q", got, branches)
	}
	if record := s.record(t, tk.id); record.Shape != store.TaskDelivery {
		t.Fatalf("after the refusal the task is %+v, want a delivery task whose history says its copy was refused", record)
	}
	history := s.history(t, tk.id)
	if last := history[len(history)-1]; last.Kind != isolationRefusedKind || last.Detail != string(taskstate.RefusedNotIsolated) ||
		history[len(history)-2].Kind != store.PromotedEventKind {
		t.Fatalf("the history ends %+v, want the refusal right after the promoted entry", history[len(history)-2:])
	}
}

// A promotion refuses, before its flip and with nothing written, a copy
// that no longer holds what an open kept work entry recorded it standing
// on (the carry-on audit of b448115, P15): the entry records the copy at
// C, the copy is detached at C's parent with the reflogs expired and C
// pruned, so nothing the copy names reaches C. The flip would answer the
// entry carry on and close it out of the open listing while the work is
// gone, which a carry on of the same copy refuses, so the promotion refuses
// the same: the task stays an investigation, nothing is appended, the
// entry stays open. The control is the copy before the prune, which a
// promotion takes.
func TestPromoteRefusesACopyThatNoLongerHoldsItsKeptWork(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := newPromotionSubject(t)
	tk := s.investigation(t)
	if err := os.WriteFile(filepath.Join(tk.copy, "finding.txt"), []byte("what the worker found\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "finding.txt")
	rawGit(t, tk.copy, "commit", "-q", "-m", "the worker's own commit")
	kept := rawGit(t, tk.copy, "rev-parse", "HEAD")
	entry, _, err := s.store.RecordKeptWork(ctx, store.KeptCopy{TaskID: tk.id, CopyPath: tk.copy,
		Holds: []string{kept}, Lacks: []string{kept}, Reason: string(ReasonUnproven), Against: "mmmm"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	rawGit(t, tk.copy, "checkout", "-q", "--detach", "HEAD~1")
	rawGit(t, tk.copy, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	rawGit(t, tk.copy, "prune", "--expire=now")
	n := len(s.history(t, tk.id))

	_, err = s.queue.Promote(ctx, request(tk.id))
	var unheld *KeptWorkUnheldError
	if !errors.As(err, &unheld) || !errors.Is(err, ErrKeptWorkUnheld) || unheld.Entry != entry.ID || !slices.Equal(unheld.Unheld, []string{kept}) {
		t.Fatalf("promoting a copy that no longer holds %s = %v; want a *KeptWorkUnheldError naming entry %d and the commit", kept, err, entry.ID)
	}
	s.untouched(t, tk, n)
	if still, err := s.store.KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
		t.Fatalf("after the refusal the entry reads %+v, %v; want it open", still, err)
	}
}
