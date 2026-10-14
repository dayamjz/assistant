package taskstate

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// taskHold is an open hold naming the task "t" and no run, the shape the
// relaunch bound, the no-sweep relaunch and an investigation's report
// register.
func taskHold(key string, age int) store.Hold {
	return store.Hold{Key: key, TaskID: store.Known("t"), Subject: "the question " + key,
		OpenedAt: epoch.Add(time.Duration(age) * time.Minute)}
}

func answered(h store.Hold) store.Hold {
	h.Resolution, h.ResolvedAt = store.Known("retry"), store.Known(epoch.Add(time.Hour))
	return h
}

// The hold tier's place in PRD section 6's precedence: an open hold naming the
// task resolves Held from the hold, above every run whatever its status and
// above the terminal and the log, and below the terminal record. Each row
// states what the tiers below would answer without the hold, and the control
// for that row resolves the same inputs with the hold answered, so the hold's
// openness is what decided and not something else in the row. A hold that
// names another task, or only a run, is not this tier's.
func TestTheHoldTierOutranksTheRunAndYieldsToTheRecord(t *testing.T) {
	delivery := store.Task{ID: "t", Shape: store.TaskDelivery}
	hold := taskHold("task/t/relaunch-bound/1", 1)
	cases := []struct {
		name    string
		in      Inputs
		without State
		withSrc Source
	}{
		{"over a failed run", Inputs{Runs: []store.Run{runIn("r", store.RunFailed, 1)}}, Failed, SourceRun},
		{"over a terminated run", Inputs{Runs: []store.Run{runIn("r", store.RunTerminated, 1)}}, Failed, SourceRun},
		{"over a running run", Inputs{Runs: []store.Run{runIn("r", store.RunRunning, 1)}, Advancing: true}, Validating, SourceRun},
		{"over a pending run", Inputs{Runs: []store.Run{runIn("r", store.RunPending, 1)}}, Validating, SourceRun},
		{"over a passed run", Inputs{Runs: []store.Run{runIn("r", store.RunPassed, 1)}}, Ready, SourceRun},
		{"over a run in no status", Inputs{Runs: []store.Run{runIn("r", "invented", 1)}}, Unknown, SourceRun},
		{"over a dead terminal with no run", Inputs{Process: Dead, Events: log("", "started")}, Unknown, SourceProcess},
		{"over a live terminal", Inputs{Process: Live}, Running, SourceProcess},
		{"over a failed event", Inputs{Events: log("", "started", "failed")}, Failed, SourceEvent},
		{"over a report", Inputs{Events: log("", "started", "report-ready")}, Reported, SourceEvent},
		{"over an empty history", Inputs{}, Unknown, SourceNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.Task = delivery
			c.in.Holds = []store.Hold{hold}
			got := Resolve(c.in)
			if got.State != Held || got.Source != SourceHold {
				t.Fatalf("resolved %s from %s, want held from the hold", got.State, got.Source)
			}
			if got.Basis.Hold != hold.Key || got.Detail.Or("") != hold.Subject {
				t.Fatalf("basis names hold %q with detail %q, want %q and its subject", got.Basis.Hold, got.Detail.Or(""), hold.Key)
			}
			if got.Progress {
				t.Fatal("a task waiting on a person reads as progress")
			}
			// Control: the hold answered is not read, and the tiers below
			// answer as they would with no hold at all.
			c.in.Holds = []store.Hold{answered(hold)}
			back := Resolve(c.in)
			if back.State != c.without || back.Source != c.withSrc || back.Basis.Hold != "" {
				t.Fatalf("with the hold answered resolved %s from %s (hold %q), want %s from %s",
					back.State, back.Source, back.Basis.Hold, c.without, c.withSrc)
			}
		})
	}

	// The terminal record outranks the hold: Landed and Complete still win,
	// and the open hold is recorded in the basis without deciding.
	runs := []store.Run{runIn("r", store.RunPassed, 1)}
	landed := Resolve(Inputs{Task: delivery, Runs: runs, MergeRecords: []store.MergeRecord{merged("r", 3)}, Holds: []store.Hold{hold}})
	if landed.State != Landed || landed.Source != SourceRecord || landed.Basis.Hold != hold.Key {
		t.Fatalf("a landed task with an open hold resolved %s from %s (hold %q), want landed from the record with the hold recorded",
			landed.State, landed.Source, landed.Basis.Hold)
	}
	complete := Resolve(Inputs{Task: delivery, Runs: runs, MergeRecords: []store.MergeRecord{merged("r", 3)},
		Cleanup: cleanup(true), Holds: []store.Hold{hold}})
	if complete.State != Complete || complete.Source != SourceRecord {
		t.Fatalf("a complete task with an open hold resolved %s from %s, want complete from the record", complete.State, complete.Source)
	}

	// What does not name the task is not the tier's: another task's hold,
	// and a hold naming only the run, which the run tier reads for a held
	// run's detail and nothing else. A hold naming the task and a run as
	// well names the task.
	failed := []store.Run{runIn("r", store.RunFailed, 1)}
	other := store.Hold{Key: "o", TaskID: store.Known("another"), Subject: "not this task's", OpenedAt: epoch}
	runOnly := store.Hold{Key: "q", RunID: store.Known("r"), Subject: "the run's", OpenedAt: epoch}
	both := store.Hold{Key: "b", TaskID: store.Known("t"), RunID: store.Known("r"), Subject: "both", OpenedAt: epoch}
	if got := Resolve(Inputs{Task: delivery, Runs: failed, Holds: []store.Hold{other, runOnly}}); got.State != Failed || got.Source != SourceRun || got.Basis.Hold != "" {
		t.Fatalf("holds not naming the task resolved %s from %s (hold %q), want failed from the run", got.State, got.Source, got.Basis.Hold)
	}
	if got := Resolve(Inputs{Task: delivery, Runs: failed, Holds: []store.Hold{both}}); got.State != Held || got.Source != SourceHold {
		t.Fatalf("a hold naming the task and its run resolved %s from %s, want held from the hold", got.State, got.Source)
	}
}

// The oldest open hold naming the task is the one named, whatever order the
// holds arrive in, and an answered one older than it is passed over; the key
// breaks a tie in opening time.
func TestTheHoldTierNamesTheOldestOpenHold(t *testing.T) {
	older, newer := taskHold("b-older", 1), taskHold("a-newer", 2)
	gone := answered(taskHold("z-gone", 0))
	for _, holds := range [][]store.Hold{{older, newer, gone}, {gone, newer, older}} {
		if got := Resolve(Inputs{Task: store.Task{ID: "t"}, Holds: holds}); got.Basis.Hold != older.Key {
			t.Fatalf("named %q, want the oldest open %q", got.Basis.Hold, older.Key)
		}
	}
	tied := taskHold("a-tied", 1)
	if got := Resolve(Inputs{Task: store.Task{ID: "t"}, Holds: []store.Hold{older, tied}}); got.Basis.Hold != tied.Key {
		t.Fatalf("named %q on a tie, want the lower key %q", got.Basis.Hold, tied.Key)
	}
}

// When the hold tier decides, a newest event claiming a state of its own is
// superseded by the hold, as it would be by a run; an open decision in the
// log claims Held, which agrees, and is not.
func TestTheHoldTierSupersedesTheLogsClaim(t *testing.T) {
	in := Inputs{Task: store.Task{ID: "t"}, Holds: []store.Hold{taskHold("k", 1)}}
	in.Events = log("", "started", "working")
	got := Resolve(in)
	if want := []int64{2}; !slices.Equal(seqs(got), want) || got.Superseded[0].By != SourceHold || got.Superseded[0].Claimed != Running {
		t.Fatalf("superseded = %+v, want the working event, by the hold", got.Superseded)
	}
	in.Events = log("", "started", "decision-open")
	if got := Resolve(in); len(got.Superseded) != 0 {
		t.Fatalf("an open decision the hold agrees with was superseded: %+v", got.Superseded)
	}
}

// P8 through the store: a task's history ends on the worker saying it is
// working and its branch's run has failed, and a hold naming the task is
// registered and later answered. While the hold is open the task is Held from
// the hold, which neither the history's tail nor the run says; once it is
// answered the task is the run's again, and nothing was appended to the
// history in between, so the answer moved with the hold record and never with
// the log. The positive control is the same task before the hold existed,
// which the run decides, so the fixture shows a resolver that ignored holds
// and one that read the tail answering differently from this one.
func TestAnOpenHoldNamingTheTaskIsItsStateUntilAnswered(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	task := newTask(t, s, "taaaaaaaaaac", true)
	appendEvents(t, s, task.ID, "started", "working")
	failRun(t, s, "run-1")

	before := resolveTask(t, s, task.ID)
	if before.State != Failed || before.Source != SourceRun || before.Basis.Hold != "" {
		t.Fatalf("before any hold resolved %s from %s (hold %q), want failed from the run", before.State, before.Source, before.Basis.Hold)
	}
	if _, err := s.RegisterHold(ctx, store.Hold{
		Key: "task/" + task.ID + "/relaunch-bound/1", TaskID: store.Known(task.ID),
		Subject: "whether to begin this work again", Detail: "retry or stop",
	}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	held := resolveTask(t, s, task.ID)
	if held.State != Held || held.Source != SourceHold || held.Basis.Hold != "task/"+task.ID+"/relaunch-bound/1" {
		t.Fatalf("with the hold open resolved %s from %s (hold %q), want held from the hold", held.State, held.Source, held.Basis.Hold)
	}
	snap, err := s.TaskSnapshot(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	if tail := tailOnly(snap.Events); tail == held.State {
		t.Fatalf("a tail-only reader agrees with the resolver (%s), so this fixture shows nothing", tail)
	}

	if _, err := s.ResolveHold(ctx, "task/"+task.ID+"/relaunch-bound/1", "stop", store.ResolvedByMachineInterface()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	after := resolveTask(t, s, task.ID)
	if after.State != Failed || after.Source != SourceRun || after.Basis.Hold != "" {
		t.Fatalf("with the hold answered resolved %s from %s (hold %q), want failed from the run again", after.State, after.Source, after.Basis.Hold)
	}
	if after.Basis.EventSequence != held.Basis.EventSequence {
		t.Fatalf("the history moved from %d to %d, so the answer did not rest on the hold alone",
			held.Basis.EventSequence, after.Basis.EventSequence)
	}
}

// failRun records a run of the task's branch that failed.
func failRun(t *testing.T, s *store.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, m := range []struct{ from, to store.RunStatus }{
		{store.RunPending, store.RunRunning}, {store.RunRunning, store.RunFailed},
	} {
		if _, err := s.TransitionRun(ctx, id, []store.RunStatus{m.from}, m.to); err != nil {
			t.Fatalf("TransitionRun %s: %v", m.to, err)
		}
	}
}
