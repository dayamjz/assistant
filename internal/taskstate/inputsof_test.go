package taskstate

import (
	"reflect"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

// InputsOf carries every field of a store.TaskSnapshot that Inputs has a
// field of the same name for, so a record the snapshot gains reaches the
// resolver once Inputs names it, and no reader resolving from records alone
// drops it (P14). Every such field of the snapshot is filled before the
// comparison, so a field InputsOf leaves out reads as its zero value and
// fails; the control is that at least one field is compared, the
// nothing-to-deliver records among them.
func TestInputsOfCarriesEverySnapshotFieldTheResolverReads(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	snap := store.TaskSnapshot{
		Task:             store.Task{ID: "t", Shape: store.TaskDelivery},
		Runs:             []store.Run{runIn("r1", store.RunPassed, 1)},
		Holds:            []store.Hold{{Key: "h", Subject: "a decision", OpenedAt: at}},
		Events:           []store.TaskEvent{{TaskID: "t", Sequence: 1, Kind: "note", At: at}},
		MergeRecords:     []store.MergeRecord{merged("r1", 7)},
		NothingToDeliver: []store.NothingToDeliver{nothingIn("r1")},
		Cleanup:          cleanup(true),
		LocalLanding:     store.Known(store.LocalLanding{Task: "t", Commit: "c"}),
		ReportStoredAt:   store.Known(at),
		Relay:            store.Known(store.TaskRelay{TaskID: "t", RecordedAt: at}),
	}
	in := reflect.ValueOf(InputsOf(snap))
	from := reflect.ValueOf(snap)
	compared := map[string]bool{}
	for i := range from.NumField() {
		name := from.Type().Field(i).Name
		field := in.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		if from.Field(i).IsZero() {
			t.Fatalf("the test left the snapshot's %s empty, so it would not see InputsOf drop it", name)
		}
		if !reflect.DeepEqual(field.Interface(), from.Field(i).Interface()) {
			t.Fatalf("InputsOf carries %s as %v, want the snapshot's %v", name, field.Interface(), from.Field(i).Interface())
		}
		compared[name] = true
	}
	if !compared["NothingToDeliver"] || !compared["MergeRecords"] {
		t.Fatalf("compared %v, which misses the landing records", compared)
	}
	if got := InputsOf(snap); got.Process != Unobserved || got.Advancing {
		t.Fatalf("InputsOf asked a live input: process %s, advancing %v", got.Process, got.Advancing)
	}
}
