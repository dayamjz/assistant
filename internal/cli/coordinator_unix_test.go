//go:build unix

package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// endWorker is the line the coordinated worker ends on, so a test ends a
// worker through the client's own message rather than through a handle the
// service holds. It is a phrase no instructions or note here carries.
const endWorker = "please end the work now"

// serveCoordinated serves the home with the worker-capable stand-in as the
// resolved agent, on serveWorkers' terms, whose worker answers a steering
// line and ends on endWorker.
func serveCoordinated(t *testing.T, h *home.Home) func() {
	t.Helper()
	agent := standin.NewWorker(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{
			Greeting: "worker ready",
			Exchanges: []standin.Exchange{
				{Contains: endWorker, Reply: "worker finished", Ends: true},
				{Contains: "carry on", Reply: "carrying on"},
			},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	return serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))
}

// coordinate runs the coordinator's client through cli.Run in this process,
// as --json, and decodes its answer into out, failing the test unless it
// exited successfully.
func coordinate(t *testing.T, h *home.Home, dir string, out any, args ...string) {
	t.Helper()
	got := run(t, h, dir, append([]string{"--json", "coordinator"}, args...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("assistant coordinator %s exited %s:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
	}
	if err := json.Unmarshal([]byte(got.stdout), out); err != nil {
		t.Fatalf("decoding assistant coordinator %s: %v\n%s", strings.Join(args, " "), err, got.stdout)
	}
}

// coordinateAs runs the coordinator's client as a process of its own placed
// in a process group, which has to be one a process in this session leads,
// and returns what it wrote and exited with. The process is this test
// binary, which hookMain hands to cli.Run, so what runs is the product; the
// credentials the service reads are that process's, which the kernel
// attributes and nothing here states.
func coordinateAs(t *testing.T, group int, h *home.Home, dir string, args ...string) invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, append([]string{"coordinator"}, append(args, "--json", "--home", h.Root())...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	got := invocation{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
		got.code = machine.ExitOK
	case errors.As(err, &exit):
		got.code = machine.Code(exit.ExitCode())
	default:
		t.Fatalf("running the client in group %d: %v\n%s%s", group, err, got.stdout, got.stderr)
	}
	return got
}

// workerGroup is the process group the worker of the task's latest attempt
// leads, read off the session reference that attempt records: the adapter
// starts a worker as the leader of a group of its own.
func workerGroup(t *testing.T, h *home.Home, id string) int {
	t.Helper()
	attempts := attemptsOf(t, h, id)
	ref, ok := attempts[len(attempts)-1].SessionRef.Get()
	if !ok {
		t.Fatalf("task %s's latest attempt records no session", id)
	}
	parsed, err := session.ParseReference(ref)
	if err != nil {
		t.Fatalf("reading the session reference %q: %v", ref, err)
	}
	return parsed.PID
}

// repositoryOf is the repository record's identifier as assistant --json
// status reports it in the working copy, which is where the coordinator
// skill tells an agent to read it.
func repositoryOf(t *testing.T, h *home.Home, dir string) string {
	t.Helper()
	got := run(t, h, dir, "--json", "status")
	var status machine.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil || status.Repository == nil {
		t.Fatalf("assistant --json status reported no repository (%v):\n%s%s", err, got.stdout, got.stderr)
	}
	return status.Repository.ID
}

// taskCountOf is how many tasks assistant --json tasks lists.
func taskCountOf(t *testing.T, h *home.Home, dir string) int {
	t.Helper()
	got := run(t, h, dir, "--json", "tasks")
	var tasks machine.Tasks
	if err := json.Unmarshal([]byte(got.stdout), &tasks); err != nil {
		t.Fatalf("decoding assistant --json tasks: %v\n%s", err, got.stdout)
	}
	return len(tasks.Tasks)
}

// The coordinator's client drives every coordinator method through cli.Run
// against a live service, and each is refused from inside a worker's
// process group.
//
// A delivery task is created through the client and its worker launched by
// the service. While that worker lives, every subcommand is run by a process
// placed in its group: each exits as an operational failure carrying the
// worker code and the next action naming why, and the create recorded no
// task. The same subcommands from this test's process, in no worker group,
// do what they say, read back from the records rather than taken from the
// answer: a message is recorded and written, and its human rendering is the
// phrase table's; a message the worker ends on ends it, and its task reads
// Unknown from the dead terminal; a listen held before the end is pushed a
// wake about the task, which the push leaves queued; a person's listing
// without --json names it and hands nothing out; a drain hands it out and
// closes nothing, a second drain hands it out again because no stream is
// held, and once the coordinator acknowledges what it was handed, recorded
// as the coordinator's, neither a drain nor a wait lists any of it again,
// while acknowledging any of it again is refused as incorrect usage; a
// relaunch begins attempt 2 and the service launches it;
// a stop ends that attempt's worker and opens the hold naming the task;
// arm verification answers that the loop is live; and an investigation
// created through the client is promoted to a delivery on a branch.
func TestTheCoordinatorClientDrivesEveryMethodAndRefusesAWorker(t *testing.T) {
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	vocabtest.Control(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serveCoordinated(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--shape", "delivery", "--project", "one",
		"--instructions", "Do the work the coordinator asked for.", "--repository", repository, "--branch", "task/one")
	id := created.Record.ID
	if created.Record.Shape != store.TaskDelivery || created.Record.BlockerTask.IsKnown() || created.Cause != "independent" {
		t.Fatalf("create answered %+v, want an independent delivery task", created)
	}
	if raw, err := os.ReadFile(created.Instructions); err != nil || string(raw) != "Do the work the coordinator asked for." {
		t.Fatalf("the instructions file reads %q (%v), want the flag's text", raw, err)
	}
	waitSessions(t, h, id, 1)
	group := workerGroup(t, h, id)

	// From the worker's group, every subcommand is refused, typed.
	before := taskCountOf(t, h, subject)
	for _, args := range [][]string{
		{"create", "--shape", "investigation", "--project", "two", "--instructions", "Look into it.", "--repository", repository},
		{"message", id, "--text", "carry on"},
		{"stop", id},
		{"peek", id},
		{"relaunch", id},
		{"promote", id, "--repository", repository, "--branch", "task/mine", "--intent", "Deliver it."},
		{"wakes"},
		{"await", "--timeout", "1s"},
		{"listen"},
		{"acknowledge", "1"},
		{"supervision"},
	} {
		got := coordinateAs(t, group, h, subject, args...)
		if got.code != machine.ExitFailure {
			t.Fatalf("coordinator %s from the worker's group exited %s, want an operational failure:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
		var failure machine.Failure
		if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
			t.Fatalf("coordinator %s from the worker's group wrote no failure document: %v\n%s", args[0], err, got.stdout)
		}
		if failure.Code != string(ipc.CodeWorker) || !strings.Contains(failure.NextAction, "worker group") {
			t.Fatalf("coordinator %s from the worker's group was refused %+v, want the worker code and why", args[0], failure)
		}
	}
	if after := taskCountOf(t, h, subject); after != before {
		t.Fatalf("a create refused for a worker recorded a task: %d tasks, want %d", after, before)
	}

	// A message, recorded and written, and read out as the phrase table's.
	var messaged machine.TaskMessaged
	coordinate(t, h, subject, &messaged, "message", id, "--text", "carry on with the parser")
	if messaged.Message.TaskID != id || messaged.Message.Text != "carry on with the parser" ||
		messaged.Delivery != store.DeliveryWritten || messaged.SentBy == "" {
		t.Fatalf("message answered %+v, want the record written to task %s", messaged, id)
	}
	plain := run(t, h, subject, "coordinator", "message", id, "--text", "carry on once more")
	if plain.code != machine.ExitOK || !strings.Contains(plain.stdout, machine.MessagePhrase(store.Message{WrittenAt: store.Known(time.Now())}).String()) {
		t.Fatalf("the human message answer exited %s with:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	vocabtest.Assert(t, plain.stdout)

	// A peek reads the worker's recent output, which this pipe-hosted
	// worker has no terminal to attach to; the person's rendering carries
	// none of the output.
	var peeked machine.TaskPeek
	coordinate(t, h, subject, &peeked, "peek", id)
	if peeked.Task != id || peeked.Attempt != 1 || peeked.Liveness != string(taskstate.Live) || len(peeked.Attach) != 0 {
		t.Fatalf("peek answered %+v, want task %s's live first attempt and no terminal to attach to", peeked, id)
	}
	plainPeek := run(t, h, subject, "coordinator", "peek", id)
	if plainPeek.code != machine.ExitOK || !strings.Contains(plainPeek.stdout, machine.PeekPhrase(peeked).String()) {
		t.Fatalf("the human peek answer exited %s with:\n%s%s", plainPeek.code, plainPeek.stdout, plainPeek.stderr)
	}
	if peeked.Output != "" && strings.Contains(plainPeek.stdout, strings.TrimSpace(peeked.Output)) {
		t.Fatalf("the human peek answer relays the worker's output:\n%s", plainPeek.stdout)
	}
	vocabtest.Assert(t, plainPeek.stdout)

	// The coordinator holds the wake stream before the worker ends, so the
	// wake the loop queues when it does is pushed to it. The stream opens
	// with a gap, which is how a listener learns it is attached.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listenCtx, stopListening := context.WithCancel(t.Context())
	defer stopListening()
	lines, listened := listening(listenCtx, executable, subject, h)
	if opened := nextEvent(t, lines, 30*time.Second); opened.Type != ipc.TypeGap {
		t.Fatalf("the wake stream opened with %+v, want the opening gap", opened)
	}

	// The worker ends on a message, and its task reads Unknown from the
	// dead terminal.
	coordinate(t, h, subject, &messaged, "message", id, "--text", endWorker)
	deadline := time.Now().Add(30 * time.Second)
	for {
		state := taskOf(t, h, subject, id).State
		if state.State == taskstate.Unknown && state.Basis.Process == taskstate.Dead {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the ended worker's task never read Unknown from a dead terminal: %+v", state)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The stream pushes a wake about the task, and the push takes nothing:
	// the row it names is still queued and handed out to nobody.
	var pushed store.Wake
	for pushed.ID == 0 {
		event := nextEvent(t, lines, 60*time.Second)
		if event.Type != ipc.TypeWakeQueued {
			continue
		}
		var w store.Wake
		if err := json.Unmarshal(event.Payload, &w); err != nil {
			t.Fatalf("a pushed wake does not read as a wake row: %v\n%s", err, event.Payload)
		}
		if w.TaskID == id && w.Queued() && !w.HandedOut() && event.Revision == uint64(w.ID) {
			pushed = w
		}
	}
	stopListening()
	go func() {
		for range lines {
		}
	}()
	if ended := <-listened; ended.code != machine.ExitOK {
		t.Fatalf("an interrupted listen exited %s:\n%s", ended.code, ended.stderr)
	}

	// A person's listing, without --json, reads the queue in the phrase
	// table's words, naming the pushed wake by its task's project, and
	// hands out nothing: the drain after it is the pushed row's first
	// handout.
	listing := run(t, h, subject, "coordinator", "wakes")
	if listing.code != machine.ExitOK || !strings.Contains(listing.stdout, "about the fleet are waiting") ||
		!strings.Contains(listing.stdout, "\none: ") {
		t.Fatalf("the human listing exited %s with:\n%s%s", listing.code, listing.stdout, listing.stderr)
	}
	vocabtest.Assert(t, listing.stdout)

	// A drain hands out what is queued and closes none of it: every row it
	// lists is still queued, and the pushed one is handed out for the first
	// time. With no stream held the coordinator is not attached, so a
	// second drain hands the same rows out again. Acknowledging them after
	// acting is what closes them, and it is recorded as the coordinator's;
	// after it neither a drain nor a wait lists any of them, and naming one
	// again is refused as incorrect usage.
	var drained machine.Wakes
	coordinate(t, h, subject, &drained, "wakes", "--limit", strconv.Itoa(machine.DefaultWakesLimit))
	if !handedOutAs(drained, pushed.ID, 1) {
		t.Fatalf("the first drain after the listing = %+v, want wake %d handed out for the first time", drained, pushed.ID)
	}
	var taken []int64
	var handed []string
	for _, w := range drained.Wakes {
		if !w.Queued() || !w.HandedOut() {
			t.Fatalf("a drain listed %+v, want every row still queued and handed out", w)
		}
		taken = append(taken, w.ID)
		handed = append(handed, strconv.FormatInt(w.ID, 10))
	}
	var again machine.Wakes
	coordinate(t, h, subject, &again, "wakes", "--limit", strconv.Itoa(machine.DefaultWakesLimit))
	if !handedOutAs(again, pushed.ID, 2) {
		t.Fatalf("a second drain with no stream held = %+v, want wake %d handed out again", again, pushed.ID)
	}
	var acknowledged machine.Acknowledged
	coordinate(t, h, subject, &acknowledged, append([]string{"acknowledge"}, handed...)...)
	if acknowledged.By != store.AcknowledgedByCoordinator().String() || len(acknowledged.Wakes) != len(handed) {
		t.Fatalf("acknowledge answered %+v, want the handed-out rows recorded as the coordinator's", acknowledged)
	}
	var after machine.Wakes
	coordinate(t, h, subject, &after, "wakes")
	var waitedAfter machine.Wakes
	coordinate(t, h, subject, &waitedAfter, "await", "--timeout", "1s")
	for _, listed := range []machine.Wakes{after, waitedAfter} {
		for _, w := range listed.Wakes {
			for _, gone := range taken {
				if w.ID == gone {
					t.Fatalf("wake %d was acknowledged and was listed again: %+v", gone, listed)
				}
			}
		}
	}
	for _, gone := range taken {
		refusedAs(t, run(t, h, subject, "--json", "coordinator", "acknowledge", strconv.FormatInt(gone, 10)), ipc.CodeInvalidRequest, machine.ExitUsage)
	}

	// A relaunch of the gone terminal begins attempt 2, which the service
	// launches.
	var relaunched machine.TaskRelaunched
	coordinate(t, h, subject, &relaunched, "relaunch", id)
	if relaunched.Task != id || relaunched.Attempt != 2 {
		t.Fatalf("relaunch answered %+v, want attempt 2 of task %s", relaunched, id)
	}
	waitSessions(t, h, id, 2)

	// A stop ends that worker and opens the hold naming the task.
	var stopped machine.TaskStopped
	coordinate(t, h, subject, &stopped, "stop", id)
	if stopped.Task != id || stopped.Attempt != 2 || stopped.Hold.Key == "" || stopped.Finished != "" {
		t.Fatalf("stop answered %+v, want attempt 2 ended and a hold registered", stopped)
	}
	if open := openHoldsOf(t, h, id); len(open) != 1 || open[0].Key != stopped.Hold.Key {
		t.Fatalf("after the stop the task's open holds are %+v, want the stop's %s", open, stopped.Hold.Key)
	}

	// Arm verification answers that the service's own loop is live.
	var arm supervisor.Arm
	coordinate(t, h, subject, &arm, "supervision")
	if arm.Outcome != supervisor.ArmStarted && arm.Outcome != supervisor.ArmAttached {
		t.Fatalf("supervision answered %+v, want a live loop", arm)
	}

	// An investigation created through the client is promoted through it.
	var investigation machine.TaskCreated
	coordinate(t, h, subject, &investigation, "create", "--shape", "investigation", "--project", "two",
		"--instructions", "Find out why the build is slow.", "--repository", repository)
	waitSessions(t, h, investigation.Record.ID, 1)
	var promoted machine.TaskPromoted
	coordinate(t, h, subject, &promoted, "promote", investigation.Record.ID,
		"--repository", repository, "--branch", "task/promoted", "--intent", "Make the build fast.")
	if promoted.Record.ID != investigation.Record.ID || promoted.Record.Shape != store.TaskDelivery ||
		promoted.Record.Branch.Or("") != "task/promoted" || promoted.Event.Kind != "promoted" {
		t.Fatalf("promote answered %+v, want task %s a delivery on task/promoted", promoted, investigation.Record.ID)
	}
	if read := taskOf(t, h, subject, investigation.Record.ID); read.Record.Shape != store.TaskDelivery {
		t.Fatalf("after the promotion the task reads %+v, want a delivery", read.Record)
	}
}

// listening runs assistant --json coordinator listen through cli.Run under
// ctx and hands each line it writes to lines as it is written, so a test
// reads the stream while it is held; done receives its exit once ctx ends
// it. It takes nothing from testing.T, so it is safe beside the test's own
// goroutine.
func listening(ctx context.Context, executable, dir string, h *home.Home) (<-chan string, <-chan invocation) {
	reader, writer := io.Pipe()
	lines := make(chan string, 64)
	done := make(chan invocation, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		_, _ = io.Copy(io.Discard, reader)
	}()
	go func() {
		var errs bytes.Buffer
		code := cli.Run(ctx, cli.Environment{
			Args:       []string{"--home", h.Root(), "--json", "coordinator", "listen"},
			Stdout:     writer,
			Stderr:     &errs,
			Getenv:     func(string) string { return "" },
			WorkingDir: dir,
			Executable: executable,
			Version:    "assistant (test)",
		})
		_ = writer.Close()
		done <- invocation{code: code, stderr: errs.String()}
	}()
	return lines, done
}

// nextEvent is the next line listening wrote, read as an event, failing
// the test when none arrives within wait or the line is not one.
func nextEvent(t *testing.T, lines <-chan string, wait time.Duration) ipc.Event {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("the listen ended before the event this test waits for")
		}
		var event ipc.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("listen wrote %q, which is not an event: %v", line, err)
		}
		return event
	case <-time.After(wait):
		t.Fatalf("listen wrote nothing for %s", wait)
	}
	return ipc.Event{}
}

// handedOutAs reports whether an answer carries the wake still queued and
// handed out for the given time.
func handedOutAs(w machine.Wakes, id, handouts int64) bool {
	for _, wake := range w.Wakes {
		if wake.ID == id {
			return wake.HandedOut() && wake.Handouts == store.Known(handouts)
		}
	}
	return false
}
