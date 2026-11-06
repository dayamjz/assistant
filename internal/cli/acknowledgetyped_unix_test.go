//go:build unix

package cli_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// Each shape of the wake queue numbers its rows apart, so a task wake and a
// domain wake can both be 1. A bare number used to name the task's wake
// whatever else carried it, so a coordinator that meant domain wake 1 and
// typed 1 closed the task's wake instead. Now a bare number that a row of
// another shape also carries is refused as incorrect usage, closing
// nothing, and the refusal names the typed forms; --domain 1 closes the
// domain wake alone and --task 1 the task wake alone. A closed row of
// another shape still makes the number ambiguous, because the number can
// still be the one meant. The positive control is a second task wake whose
// number no other shape carries: named bare, it closes.
func TestABareNumberTwoShapesCarryIsRefusedAndTheTypedFormsCloseOneRowEach(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	records := openStore(t, h)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{ID: "subject", WorkingPath: t.TempDir(),
		UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	// The task is complete and its copy removed, so the loop raises
	// nothing more about it and the queue holds the rows this test put there.
	landTask(t, records, "subject", "twinnedtasks", "the indexer", true)
	inc, err := records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasked, err := records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{TaskID: "twinnedtasks",
		Kind: string(supervisor.KindTerminalGone), Marker: "twinned:1"}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	alone, err := records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{TaskID: "twinnedtasks",
		Kind: string(supervisor.KindTerminalGone), Marker: "twinned:2"}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := records.Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	domained, err := records.AppendDomainWakeWithMarkers(ctx, inc,
		store.DomainWakeRequest{Delegation: d.ID, Kind: string(supervisor.KindSubordinateGone), Marker: "gone:1"},
		store.DelegationMarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	// Ended, so the service's open does not start a subordinate for it and
	// acknowledge its wakes as superseded; the domain wake stays queued for
	// the first coordinator.
	if _, _, err := records.EndDelegation(ctx, d.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if tasked.Wake.ID != domained.Wake.ID || alone.Wake.ID == domained.Wake.ID {
		t.Fatalf("the task wakes are %d and %d and the domain wake %d; the test needs the first two shapes numbered alike",
			tasked.Wake.ID, alone.Wake.ID, domained.Wake.ID)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	serve(t, h)
	twin := strconv.FormatInt(tasked.Wake.ID, 10)

	queued := func() (tasks, domains []int64) {
		t.Helper()
		var drained machine.Wakes
		coordinate(t, h, subject, &drained, "wakes")
		for _, row := range drained.Wakes {
			tasks = append(tasks, row.ID)
		}
		for _, row := range drained.DomainWakes {
			domains = append(domains, row.ID)
		}
		return tasks, domains
	}
	refusedAmbiguous := func(when string) {
		t.Helper()
		got := run(t, h, subject, "--json", "coordinator", "acknowledge", twin)
		refusedAs(t, got, ipc.CodeInvalidRequest, machine.ExitUsage)
		var failure machine.Failure
		if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
			t.Fatal(err)
		}
		for _, typed := range []string{"--task " + twin, "--domain " + twin} {
			if !strings.Contains(failure.Error, typed) {
				t.Fatalf("%s, the refusal of a bare %s does not name %q: %s", when, twin, typed, failure.Error)
			}
		}
	}

	refusedAmbiguous("with both rows queued")
	if tasks, domains := queued(); !slices.Equal(tasks, []int64{tasked.Wake.ID, alone.Wake.ID}) || !slices.Equal(domains, []int64{domained.Wake.ID}) {
		t.Fatalf("after the refusal the queue holds task wakes %v and domain wakes %v, want every row still queued", tasks, domains)
	}

	var acknowledged machine.Acknowledged
	coordinate(t, h, subject, &acknowledged, "acknowledge", "--domain", twin)
	if !slices.Equal(acknowledged.DomainWakes, []int64{domained.Wake.ID}) || len(acknowledged.Wakes) != 0 {
		t.Fatalf("acknowledge --domain %s answered %+v, want the domain wake alone", twin, acknowledged)
	}
	if tasks, domains := queued(); !slices.Equal(tasks, []int64{tasked.Wake.ID, alone.Wake.ID}) || len(domains) != 0 {
		t.Fatalf("after acknowledge --domain %s the queue holds task wakes %v and domain wakes %v, want both task wakes alone", twin, tasks, domains)
	}

	refusedAmbiguous("with the domain wake closed")

	acknowledged = machine.Acknowledged{}
	coordinate(t, h, subject, &acknowledged, "acknowledge", "--task", twin)
	if !slices.Equal(acknowledged.Wakes, []int64{tasked.Wake.ID}) || len(acknowledged.DomainWakes) != 0 {
		t.Fatalf("acknowledge --task %s answered %+v, want the task wake alone", twin, acknowledged)
	}

	acknowledged = machine.Acknowledged{}
	coordinate(t, h, subject, &acknowledged, "acknowledge", strconv.FormatInt(alone.Wake.ID, 10))
	if !slices.Equal(acknowledged.Wakes, []int64{alone.Wake.ID}) {
		t.Fatalf("a bare %d no other shape carries answered %+v, want that task wake closed", alone.Wake.ID, acknowledged)
	}
	if tasks, domains := queued(); len(tasks)+len(domains) != 0 {
		t.Fatalf("task wakes %v and domain wakes %v are still queued, want none", tasks, domains)
	}
}
