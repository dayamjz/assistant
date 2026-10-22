//go:build unix

package cli_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The queue's third shape through the command line, over a kept-work wake
// already in the record when the service opens: the person's listing,
// assistant coordinator wakes, names it by the branch its entry was kept on
// and phrases it, where it otherwise printed the count and nothing else;
// the coordinator's --json drain carries it under kept_work_wakes; and
// assistant coordinator acknowledge kNUMBER closes exactly it, where the
// command line otherwise refused the prefix and a bare number named a task
// wake. Naming it again is refused as an invalid request.
func TestAKeptWorkWakeInTheRecordIsListedAndAcknowledgedAsKNumber(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	const branch = "a-persons-branch"
	records := openStore(t, h)
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{ID: "subject", WorkingPath: t.TempDir(),
		UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{ID: "run-pushed", RepositoryID: "subject", Branch: branch, SubmittedHead: "aaaa",
		Base: "bbbb", Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, "run-pushed", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, "run-pushed", []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: "run-pushed", CopyPath: "/nowhere/worktrees/run-pushed",
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatal(err)
	}
	inc, err := records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := records.AppendKeptWorkWake(ctx, inc, store.KeptWorkWakeRequest{KeptWork: entry.ID,
		Kind: string(supervisor.KindKeptWork), Marker: "kept-work:" + strconv.FormatInt(entry.ID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	serve(t, h)

	line := branch + ": " + machine.QueuedKeptWorkWakePhrase(appended.Wake).String()
	if listing := run(t, h, subject, "coordinator", "wakes"); !strings.Contains(listing.stdout, line) {
		t.Fatalf("the person's listing does not read %q:\n%s%s", line, listing.stdout, listing.stderr)
	}
	var drained machine.Wakes
	coordinate(t, h, subject, &drained, "wakes")
	if len(drained.KeptWorkWakes) != 1 || drained.KeptWorkWakes[0].ID != appended.Wake.ID {
		t.Fatalf("the coordinator's drain carries kept-work wakes %+v, want wake %d", drained.KeptWorkWakes, appended.Wake.ID)
	}
	named := "k" + strconv.FormatInt(appended.Wake.ID, 10)
	var acknowledged machine.Acknowledged
	coordinate(t, h, subject, &acknowledged, "acknowledge", named)
	if len(acknowledged.KeptWorkWakes) != 1 || acknowledged.KeptWorkWakes[0] != appended.Wake.ID || len(acknowledged.Wakes)+len(acknowledged.DomainWakes) != 0 {
		t.Fatalf("acknowledge %s answered %+v, want that kept-work wake alone closed", named, acknowledged)
	}
	if listing := run(t, h, subject, "coordinator", "wakes"); strings.Contains(listing.stdout, branch+": ") {
		t.Fatalf("the person's listing still shows the notice after it was acknowledged:\n%s", listing.stdout)
	}
	refusedAs(t, run(t, h, subject, "--json", "coordinator", "acknowledge", named), ipc.CodeInvalidRequest, machine.ExitUsage)
}
