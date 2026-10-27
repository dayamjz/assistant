package taskstate

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// nothingIn is a nothing-to-deliver record of run, in the shape
// store.TransitionRunOnNothingToDeliver writes.
func nothingIn(run string) store.NothingToDeliver {
	return store.NothingToDeliver{Run: run, DefaultCommit: "dddd", Submitted: "aaaa", Examined: []string{"aaaa"},
		Answer: store.NothingToDeliverAnswer, Checkpoint: 3, RecordedAt: epoch}
}

// PRD section 6's "nothing left to deliver" in the terminal tier (P8): a
// run's nothing-to-deliver record lands the task the run is attributed to,
// on the merge record's terms. The control is the same passed run with no
// record, which resolves Ready from the run tier forever. Landed from the
// record outranks a later run and an open hold, Complete follows from it and
// a removal, the detail says the change was already on the default branch and
// never that anything merged, and Basis names the run and which record it
// was. A record naming a run outside Runs lands nothing, and where a merge
// record and a nothing-to-deliver record land two runs the newest decides.
func TestANothingToDeliverRecordLandsTheTask(t *testing.T) {
	principles.Cite(t, principles.P8)
	delivery := store.Task{ID: "t", Shape: store.TaskDelivery}
	passed := []store.Run{runIn("r1", store.RunPassed, 1)}

	if got := Resolve(Inputs{Task: delivery, Runs: passed}); got.State != Ready || got.Source != SourceRun {
		t.Fatalf("control: a passed run with no record resolved %s from %s, want Ready from the run", got.State, got.Source)
	}

	got := Resolve(Inputs{Task: delivery, Runs: passed, NothingToDeliver: []store.NothingToDeliver{nothingIn("r1")}})
	if got.State != Landed || got.Source != SourceRecord || got.Basis.LandedRun != "r1" || !got.Basis.NothingToDeliver {
		t.Fatalf("a run that left nothing to deliver resolved %+v, want Landed from its record", got)
	}
	detail, _ := got.Detail.Get()
	if !strings.Contains(detail, "already on the default branch") || strings.Contains(detail, "merged") {
		t.Fatalf("the detail says %q, want the change already on the default branch and nothing merged", detail)
	}

	later := append([]store.Run{runIn("r2", store.RunFailed, 2)}, passed...)
	open := []store.Hold{{Key: "h", TaskID: store.Known("t"), Subject: "a decision", OpenedAt: epoch}}
	if got := Resolve(Inputs{Task: delivery, Runs: later, Holds: open, NothingToDeliver: []store.NothingToDeliver{nothingIn("r1")}}); got.State != Landed ||
		got.Source != SourceRecord {
		t.Fatalf("a later failed run and an open hold displaced the landing: %+v", got)
	}

	got = Resolve(Inputs{Task: delivery, Runs: passed, NothingToDeliver: []store.NothingToDeliver{nothingIn("r1")}, Cleanup: cleanup(true)})
	if got.State != Complete || got.Source != SourceRecord || !got.Basis.NothingToDeliver {
		t.Fatalf("landed and removed resolved %+v, want Complete from the record", got)
	}
	if detail, _ := got.Detail.Get(); strings.Contains(detail, "merged") {
		t.Fatalf("the Complete detail says %q", detail)
	}

	if got := Resolve(Inputs{Task: delivery, Runs: passed, NothingToDeliver: []store.NothingToDeliver{nothingIn("other")}}); got.State != Ready ||
		got.Basis.LandedRun != "" || got.Basis.NothingToDeliver {
		t.Fatalf("a record naming a run outside the task's resolved %+v, want Ready from the run", got)
	}

	both := []store.Run{runIn("r1", store.RunPassed, 1), runIn("r2", store.RunPassed, 2)}
	got = Resolve(Inputs{Task: delivery, Runs: both, MergeRecords: []store.MergeRecord{merged("r1", 7)},
		NothingToDeliver: []store.NothingToDeliver{nothingIn("r2")}})
	if got.Basis.LandedRun != "r2" || !got.Basis.NothingToDeliver {
		t.Fatalf("the newer run's nothing-to-deliver record did not decide: %+v", got.Basis)
	}
	got = Resolve(Inputs{Task: delivery, Runs: both, MergeRecords: []store.MergeRecord{merged("r2", 7)},
		NothingToDeliver: []store.NothingToDeliver{nothingIn("r1")}})
	if got.Basis.LandedRun != "r2" || got.Basis.NothingToDeliver {
		t.Fatalf("the newer run's merge record did not decide: %+v", got.Basis)
	}
	if detail, _ := got.Detail.Get(); !strings.Contains(detail, "merged") {
		t.Fatalf("the merge record's landing says %q", detail)
	}
}
