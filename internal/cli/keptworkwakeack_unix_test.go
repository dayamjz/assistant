//go:build unix

package cli_test

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The queue's third shape through the command line, over a kept-work wake
// already in the record when the service opens beside a task's own wake
// about work kept in its copy, the two numbered 1 in their own sequences:
// the person's listing, assistant coordinator wakes, names the kept-work
// wake by its entry's project and the branch it was kept on and phrases it, where it
// otherwise printed the count and nothing else; the coordinator's --json
// drain carries it under kept_work_wakes; and the acknowledge command a
// drain's plain answer prints, run as printed, closes exactly the two rows
// it handed out, the task's as 1 and the kept-work wake as k1, and leaves
// nothing queued. A drain that described the shapes in words instead left a
// person to tell a task's notice about a kept copy from the third shape's,
// and naming the task's k1 closed the wrong row or refused the batch.
// Naming the kept-work wake again is refused as an invalid request.
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
	// A task's own notice that its copy was kept, which reads as work left
	// in a local copy too and is a task wake, acknowledged by its number
	// alone. The task is complete, its work merged and its copy removed, so
	// the loop raises nothing more about it and the queue holds the two rows
	// this test put there.
	landTask(t, records, "subject", "keptcopytask", "the indexer", true)
	tasked, err := records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{TaskID: "keptcopytask",
		Kind: string(supervisor.KindKeptWork), Marker: "kept-work:on-a-task"}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	if tasked.Wake.ID != appended.Wake.ID {
		t.Fatalf("the task wake is %d and the kept-work wake %d; the test needs the two numbered alike", tasked.Wake.ID, appended.Wake.ID)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	serve(t, h)

	line := "subject " + branch + ": " + machine.QueuedKeptWorkWakePhrase(appended.Wake).String()
	if listing := run(t, h, subject, "coordinator", "wakes"); !strings.Contains(listing.stdout, line) {
		t.Fatalf("the person's listing does not read %q:\n%s%s", line, listing.stdout, listing.stderr)
	}
	var drained machine.Wakes
	coordinate(t, h, subject, &drained, "wakes")
	if len(drained.KeptWorkWakes) != 1 || drained.KeptWorkWakes[0].ID != appended.Wake.ID || len(drained.Wakes) != 1 {
		t.Fatalf("the coordinator's drain carries task wakes %+v and kept-work wakes %+v, want one of each", drained.Wakes, drained.KeptWorkWakes)
	}
	// A drain read by a person prints the acknowledge command for exactly
	// the rows it handed out, so a person runs it as printed.
	awaited := run(t, h, subject, "coordinator", "await", "--timeout", "5s")
	printed := regexp.MustCompile(`assistant coordinator acknowledge((?: \w+)+)`).FindStringSubmatch(awaited.stdout)
	if printed == nil {
		t.Fatalf("a drain's plain answer prints no acknowledge command to run:\n%s%s", awaited.stdout, awaited.stderr)
	}
	names := strings.Fields(printed[1])
	named := "k" + strconv.FormatInt(appended.Wake.ID, 10)
	if want := []string{strconv.FormatInt(tasked.Wake.ID, 10), named}; !slices.Equal(names, want) {
		t.Fatalf("a drain's plain answer prints acknowledge %v, want %v:\n%s", names, want, awaited.stdout)
	}
	var acknowledged machine.Acknowledged
	coordinate(t, h, subject, &acknowledged, append([]string{"acknowledge"}, names...)...)
	if !slices.Equal(acknowledged.KeptWorkWakes, []int64{appended.Wake.ID}) || !slices.Equal(acknowledged.Wakes, []int64{tasked.Wake.ID}) ||
		len(acknowledged.DomainWakes) != 0 {
		t.Fatalf("acknowledge %v answered %+v, want the task wake and the kept-work wake closed", names, acknowledged)
	}
	// A drain offers every queued row again to a coordinator not still
	// attached, so an empty one is an empty queue.
	var again machine.Wakes
	coordinate(t, h, subject, &again, "wakes")
	if n := len(again.Wakes) + len(again.DomainWakes) + len(again.KeptWorkWakes); n != 0 {
		t.Fatalf("%d row(s) are still queued after the printed command ran: %+v", n, again)
	}
	if listing := run(t, h, subject, "coordinator", "wakes"); strings.Contains(listing.stdout, branch+": ") {
		t.Fatalf("the person's listing still shows the notice after it was acknowledged:\n%s", listing.stdout)
	}
	refusedAs(t, run(t, h, subject, "--json", "coordinator", "acknowledge", named), ipc.CodeInvalidRequest, machine.ExitUsage)
}

// PRD section 8 lists a kept-work wake under its entry's project and branch.
// Two repositories each keep work from a person's run on a branch of the
// same name, and each entry is raised as a kept-work wake: the person's
// listing, assistant coordinator wakes, names each notice by its project and
// its branch, so the two lines differ and each says which project it is
// about. A listing by branch alone printed the same line twice.
func TestTheListingNamesAKeptWorkWakeByItsProjectAndBranch(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	const branch = "a-persons-branch"
	records := openStore(t, h)
	ctx := t.Context()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	inc, err := records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projects := []string{"first-project", "second-project"}
	wakes := map[string]store.KeptWorkWake{}
	for _, project := range projects {
		if _, err := records.UpsertRepository(ctx, store.Repository{ID: project, WorkingPath: t.TempDir(),
			UpstreamURL: "/nowhere/upstream.git", DefaultBranch: "main"}); err != nil {
			t.Fatal(err)
		}
		runID := "run-of-" + project
		if _, err := records.CreateRun(ctx, store.Run{ID: runID, RepositoryID: project, Branch: branch, SubmittedHead: "aaaa",
			Base: "bbbb", Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
			t.Fatal(err)
		}
		if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
			t.Fatal(err)
		}
		entry, _, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: runID, CopyPath: "/nowhere/worktrees/" + runID,
			Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		appended, err := records.AppendKeptWorkWake(ctx, inc, store.KeptWorkWakeRequest{KeptWork: entry.ID,
			Kind: string(supervisor.KindKeptWork), Marker: "kept-work:" + strconv.FormatInt(entry.ID, 10)})
		if err != nil {
			t.Fatal(err)
		}
		wakes[project] = appended.Wake
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	serve(t, h)

	listing := run(t, h, subject, "coordinator", "wakes")
	var lines []string
	for _, project := range projects {
		line := project + " " + branch + ": " + machine.QueuedKeptWorkWakePhrase(wakes[project]).String()
		if !strings.Contains(listing.stdout, line) {
			t.Fatalf("the person's listing does not read %q:\n%s%s", line, listing.stdout, listing.stderr)
		}
		lines = append(lines, line)
	}
	if lines[0] == lines[1] {
		t.Fatalf("the two projects' notices read alike: %q", lines[0])
	}
}
