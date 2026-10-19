package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// streamWait bounds every wait on the service's stream. The loop publishes
// a task's state on the pass after its records moved, at the beacon
// interval these homes configure at its floor of one second.
const streamWait = 30 * time.Second

// watchedHome is a home whose service's supervisor loop passes every second,
// served, with its records open for a test to write tasks through.
func watchedHome(t *testing.T) (*home.Home, *store.Store) {
	t.Helper()
	h := newTestHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	serveForTest(t, h)
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	return h, records
}

// subscribed dials the service and opens its event stream with the given
// client backlog, zero for the default.
func subscribed(t *testing.T, h *home.Home, backlog int) *ipc.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, h.Socket(), ipc.ClientConfig{Backlog: backlog})
	if err != nil {
		t.Fatalf("dialing %s: %v", h.Socket(), err)
	}
	t.Cleanup(func() { _ = client.Close() })
	stream, err := client.Subscribe(ctx, machine.SubscribeRequest{})
	if err != nil {
		t.Fatalf("subscribing: %v", err)
	}
	t.Cleanup(stream.Close)
	return stream
}

// investigation records an investigation task and appends the events given,
// each a kind and its detail.
func investigation(t *testing.T, records *store.Store, id, project string, events ...[2]string) {
	t.Helper()
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: project, Mode: "headless", WorktreePath: "/worktrees/" + id,
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating task %s: %v", id, err)
	}
	for _, e := range events {
		if _, err := records.AppendTaskEvent(t.Context(), id, e[0], e[1]); err != nil {
			t.Fatalf("appending %s to %s: %v", e[0], id, err)
		}
	}
}

// taskStates reads the stream until it has seen a task.state for each of the
// named tasks, returning every event it read in order, the opening marker
// included.
func taskStates(t *testing.T, stream *ipc.Stream, ids ...string) []ipc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), streamWait)
	defer cancel()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var got []ipc.Event
	for len(want) > 0 {
		event, err := stream.Recv(ctx)
		if err != nil {
			t.Fatalf("waiting for the state of %v, having read %d events: %v", want, len(got), err)
		}
		got = append(got, event)
		if event.Type != ipc.TypeTaskState {
			continue
		}
		var task machine.Task
		if err := json.Unmarshal(event.Payload, &task); err != nil {
			t.Fatalf("a task.state payload does not read as a task: %v\n%s", err, event.Payload)
		}
		delete(want, task.Record.ID)
	}
	return got
}

// The fleet view's renderer, over events recorded from the service's own
// publisher through its socket and never written by hand: the stream opens
// with its gap marker, the supervisor loop publishes each task's state once
// its records moved, and every line the renderer makes of them is held to
// the derived forbidden set. A paused worker's declared wait is what its
// line says, and never the word; a task whose newest entry is a relaunch
// note is no word from the worker; and the identifiers and labels the
// payloads carry reach no line.
func TestWatchRendersEventsRecordedFromTheServicesPublisher(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	h, records := watchedHome(t)
	stream := subscribed(t, h, 0)

	const waiting = "an upstream release of the parser"
	investigation(t, records, "parserwaitsa", "the parser", [2]string{"waiting-external", waiting})
	investigation(t, records, "relaunchedbb", "the indexer", [2]string{"relaunched", "attempt 2 in the same copy"})
	investigation(t, records, "untouchedccc", "the docs")

	events := taskStates(t, stream, "parserwaitsa", "relaunchedbb", "untouchedccc")
	if events[0].Type != ipc.TypeGap {
		t.Fatalf("the stream's first delivery is %s, want the opening gap marker", events[0].Type)
	}
	lines := map[string]string{}
	for _, event := range events {
		line := eventLine(event)
		vocabtest.Assert(t, line)
		var out bytes.Buffer
		readOut(&out, event)
		if out.String() != line+"\n" {
			t.Errorf("readOut writes %q for an event eventLine renders %q", out.String(), line)
		}
		if event.Type != ipc.TypeTaskState {
			continue
		}
		var task machine.Task
		if err := json.Unmarshal(event.Payload, &task); err != nil {
			t.Fatal(err)
		}
		lines[task.Record.ID] = line
		if event.Revision == 0 {
			t.Errorf("the state of task %s travelled with no revision", task.Record.ID)
		}
		if len(vocabtest.Violations(t, string(event.Payload))) == 0 {
			t.Errorf("the payload of task %s carries nothing the line is held against:\n%s", task.Record.ID, event.Payload)
		}
	}
	if got := eventLine(events[0]); got != machine.PhraseUpdatesMissed.String() {
		t.Errorf("the opening marker renders %q", got)
	}
	if got := lines["parserwaitsa"]; !strings.Contains(got, "the parser: ") || !strings.Contains(got, waiting) || strings.Contains(got, string(taskstate.Paused)) {
		t.Errorf("the paused task renders %q, want its declared wait and not the word", got)
	}
	if got := lines["relaunchedbb"]; !strings.Contains(got, machine.NoWordYet.Outcome) {
		t.Errorf("the relaunched task renders %q, want no word from the worker yet", got)
	}
	if got := lines["untouchedccc"]; !strings.Contains(got, "the docs: ") || strings.Contains(got, "untouchedccc") {
		t.Errorf("the untouched task renders %q", got)
	}
}

// A task's state is published once per resolved change and not once per
// pass: with the loop passing every second and the records still, nothing
// more arrives; with one entry appended, exactly that task's state arrives
// again, with a newer revision.
func TestATasksStateIsPublishedOncePerChangeAndNotPerPass(t *testing.T) {
	t.Parallel()
	h, records := watchedHome(t)
	stream := subscribed(t, h, 0)
	investigation(t, records, "oncepertaska", "the parser")
	first := taskStates(t, stream, "oncepertaska")
	published := func(events []ipc.Event) (revision uint64, n int) {
		for _, e := range events {
			if e.Type == ipc.TypeTaskState {
				n++
				revision = e.Revision
			}
		}
		return revision, n
	}
	before, n := published(first)
	if n != 1 {
		t.Fatalf("the task's state arrived %d times before anything changed, want once", n)
	}

	// Three passes with nothing changed publish nothing.
	quiet, cancel := context.WithTimeout(t.Context(), 3500*time.Millisecond)
	defer cancel()
	if event, err := stream.Recv(quiet); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("with the records still, the stream delivered %s (revision %d), %v; want nothing", event.Type, event.Revision, err)
	}

	// One change publishes once more.
	if _, err := records.AppendTaskEvent(t.Context(), "oncepertaska", "working", "the worker says so"); err != nil {
		t.Fatal(err)
	}
	again := taskStates(t, stream, "oncepertaska")
	after, n := published(again)
	if n != 1 {
		t.Fatalf("after one change the task's state arrived %d times, want once", n)
	}
	if after <= before {
		t.Fatalf("the republished state carries revision %d, not newer than %d", after, before)
	}
	var task machine.Task
	if err := json.Unmarshal(again[len(again)-1].Payload, &task); err != nil {
		t.Fatal(err)
	}
	if task.State.State != taskstate.Running {
		t.Fatalf("the republished state is %s, want the one the appended entry resolves to", task.State.State)
	}
}

// A subscriber that falls behind is gapped rather than stalled, through the
// real bounded stream: with a two-deep queue and several states published
// before it reads, its next delivery is a gap marker that says how many it
// discarded, and the view renders the marker as updates missed. The states
// are retained in the sense that matters, since the gap is what makes a
// consumer read the fleet back in full; this is the overflow case the class
// table promises for state.
func TestAStreamThatFallsBehindRendersTheGapMarker(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	h, records := watchedHome(t)
	behind := subscribed(t, h, 2)
	current := subscribed(t, h, 0)

	// The opening marker, consumed so the queue holds only what follows.
	ctx, cancel := context.WithTimeout(t.Context(), streamWait)
	defer cancel()
	if opening, err := behind.Recv(ctx); err != nil || opening.Type != ipc.TypeGap {
		t.Fatalf("the first delivery is %s, %v; want the opening marker", opening.Type, err)
	}

	ids := []string{"behindaaaaaa", "behindbbbbbb", "behindcccccc", "behindcccccd", "behindcccece", "behindcccfff"}
	for _, id := range ids {
		investigation(t, records, id, "a project")
	}
	taskStates(t, current, ids...)
	// One more change, seen on the stream that keeps up, so the one that
	// fell behind has had every earlier state on its connection for at
	// least a pass before it is read.
	if _, err := records.AppendTaskEvent(t.Context(), ids[0], "working", "the worker says so"); err != nil {
		t.Fatal(err)
	}
	taskStates(t, current, ids[0])

	var marker ipc.Event
	for i := 0; i < 2*len(ids); i++ {
		event, err := behind.Recv(ctx)
		if err != nil {
			t.Fatalf("reading the stream that fell behind: %v", err)
		}
		if event.Type == ipc.TypeGap {
			marker = event
			break
		}
	}
	if marker.Type != ipc.TypeGap {
		t.Fatalf("the stream that fell behind delivered no gap marker in %d events", 2*len(ids))
	}
	gap, err := ipc.GapPayload(marker)
	if err != nil {
		t.Fatal(err)
	}
	if gap.Dropped == 0 {
		t.Fatalf("the marker says nothing was discarded, so no overflow happened: %+v", gap)
	}
	line := eventLine(marker)
	if line != machine.PhraseUpdatesMissed.String() {
		t.Fatalf("the marker renders %q", line)
	}
	vocabtest.Assert(t, line)
}

// assistant watch, without --json, through the surface against the real
// service: it prints the catch-up read and then one line per event,
// every line through the phrase table, and ends cleanly when interrupted.
// With --json the same view writes documents that keep the identifiers.
func TestWatchSpeaksThePhraseTableAndEndsWhenInterrupted(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	h, records := watchedHome(t)
	const waiting = "a sign-off from the security team"
	investigation(t, records, "watchedtaska", "the parser", [2]string{"waiting-external", waiting})

	lines, code := watchFor(t, h, func(seen <-chan string) {
		waitLine(t, seen, waiting)
		// A change, so the view has an event to render as well as the
		// listing it opened with.
		if _, err := records.AppendTaskEvent(t.Context(), "watchedtaska", "working", "the worker says so"); err != nil {
			t.Fatal(err)
		}
		waitLine(t, seen, "A worker is on this work.")
	}, "watch")
	if code != machine.ExitOK {
		t.Fatalf("an interrupted watch exited %s, want ok", code)
	}
	if len(lines) < 2 {
		t.Fatalf("the view wrote %d lines, want the listing and an event", len(lines))
	}
	for _, line := range lines {
		vocabtest.Assert(t, line)
		for _, forbidden := range []string{"watchedtaska", string(taskstate.Paused), "task.state", "revision"} {
			if strings.Contains(line, forbidden) {
				t.Errorf("the line %q carries %q", line, forbidden)
			}
		}
	}

	documents, _ := watchFor(t, h, func(seen <-chan string) {
		waitLine(t, seen, "watchedtaska")
	}, "--json", "watch")
	var fleet machine.CatchUp
	if err := json.Unmarshal([]byte(documents[0]), &fleet); err != nil {
		t.Fatalf("the first document is not the catch-up read: %v\n%s", err, documents[0])
	}
	// The worker moved on above, so the structured view carries the
	// identifier and the state label the human one keeps out.
	under := fleet.UnderWay.Tasks
	if len(under) != 1 || under[0].Record.ID != "watchedtaska" || under[0].State.State != taskstate.Running {
		t.Fatalf("the structured view lost the identifier or the label: %+v", under)
	}
}

// watchFor drives assistant watch on a home with the arguments given, hands
// each line it writes to the caller's script as it is written, and ends the
// command when the script returns. It returns every line and the exit code.
func watchFor(t *testing.T, h *home.Home, script func(<-chan string), args ...string) ([]string, machine.Code) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	seen := make(chan string, 1024)
	var lines []string
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			seen <- scanner.Text()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), streamWait)
	defer cancel()
	var errs bytes.Buffer
	exited := make(chan machine.Code, 1)
	go func() {
		exited <- Run(ctx, Environment{
			Args:       append([]string{"--home", h.Root()}, args...),
			Stdout:     writer,
			Stderr:     &errs,
			Getenv:     func(string) string { return "" },
			WorkingDir: t.TempDir(),
			Executable: executable,
			Version:    "assistant (test)",
		})
		_ = writer.Close()
	}()
	script(seen)
	cancel()
	code := <-exited
	<-scanned
	t.Logf("assistant %s:\n%s\nstandard error:\n%s", strings.Join(args, " "), strings.Join(lines, "\n"), errs.String())
	return lines, code
}

// waitLine reads lines until one carries want.
func waitLine(t *testing.T, seen <-chan string, want string) {
	t.Helper()
	deadline := time.After(streamWait)
	for {
		select {
		case line := <-seen:
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no line carrying %q arrived within %s", want, streamWait)
		}
	}
}
