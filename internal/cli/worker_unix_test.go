//go:build unix

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// runMarker is what a coordinator's message carries ahead of a command the
// stream worker is to run; the worker runs the rest of the message.
const runMarker = "please run: "

// serveRouted serves the home with the shipped Claude Code adapter's worker,
// the stand-in in stream mode, as the resolved agent, and with the route the
// product's service start gives a worker: this test binary, which hookMain
// hands to cli.Run, --home naming the home, and the worker verb. The worker
// runs what a message carries after runMarker, in a process group of its
// own, and ends on endWorker.
func serveRouted(t *testing.T, h *home.Home) (*standin.Agent, string) {
	t.Helper()
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{
				{Contains: runMarker, Run: true},
				{Contains: endWorker, Reply: "worker finished", Ends: true},
			},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	route := cli.WorkerRoute(executable, h.Root())
	serveOptions(t, service.Options{
		Home: h, NewStages: stages.All, NewFixer: stages.Fix,
		Catalog:     agents.NewCatalog(fixedFactory{runner: agent.Runner()}),
		WorkerRoute: route,
	})
	return agent, route
}

// workerCall is the stand-in's record of the one worker it was started as,
// waiting until it has read at least n lines.
func workerCall(t *testing.T, agent *standin.Agent, n int) standin.Call {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		for _, c := range agent.Calls() {
			if c.Worker && len(c.Lines) >= n {
				if !c.Stream {
					t.Fatalf("the worker read its input in line mode, want the Claude Code adapter's stream mode")
				}
				return c
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no worker has read %d lines: %s", n, agent.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// routeCommands is the commands the worker's instructions tell it to type,
// read off the instructions it was started with: each indented line that
// begins with the route, and for a here-document the lines through its
// terminator, keyed by the subcommand.
func routeCommands(t *testing.T, instructions, route string) map[string]string {
	t.Helper()
	commands := map[string]string{}
	lines := strings.Split(instructions, "\n")
	for i := 0; i < len(lines); i++ {
		line, ok := strings.CutPrefix(lines[i], "    "+route+" ")
		if !ok {
			continue
		}
		command := route + " " + line
		if _, doc, here := strings.Cut(line, "<<'"); here {
			terminator := strings.TrimSuffix(doc, "'")
			for i++; i < len(lines); i++ {
				body := strings.TrimPrefix(lines[i], "    ")
				command += "\n" + body
				if body == terminator {
					break
				}
			}
		}
		commands[strings.Fields(line)[0]] = command
	}
	return commands
}

// messageWorker sends the task's worker a message through the
// coordinator's client and returns its record's identifier.
func messageWorker(t *testing.T, h *home.Home, dir, id, text string) int64 {
	t.Helper()
	var sent machine.TaskMessaged
	coordinate(t, h, dir, &sent, "message", id, "--text", text)
	if sent.Delivery != store.DeliveryWritten {
		t.Fatalf("the message to task %s was %s, want written", id, sent.Delivery)
	}
	return sent.Message.ID
}

// waitFor polls cond until it holds, failing the test with what after a
// minute.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if !time.Now().Before(deadline) {
			t.Fatalf("%s never happened", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// historyKinds is how many entries of each kind the task's history holds.
func historyKinds(t *testing.T, h *home.Home, id string) map[string]int {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	history, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range history {
		kinds[e.Kind]++
	}
	return kinds
}

// #226: a worker reports back through the route its grant allows, and only
// a worker can.
//
// An investigation created through the coordinator's client is launched on
// the shipped Claude Code adapter's worker, read off the wire by the
// stand-in in stream mode. Its command line allows exactly the route the
// service was given, in the same tool list as git, and its instructions name
// the commands to type after it. Every command the worker runs below is one
// of those, read back out of the instructions it was started with, run by
// the worker in a process group of its own as the Claude Code CLI's Bash
// tool runs one.
//
// A message reaches the worker under its number, and the worker's
// acknowledge command records it read on the message record, in the same
// write as the history entry the record names (P8); the message that
// carried the command stays unread. The worker's report command files a
// report on standard input: it is stored, the history records it once, and
// each of its findings that asks, or names no action, becomes a hold (P3),
// so the task reads Held over the report until a person answers both, and
// then Running from the worker's live terminal over the report's claim of
// Reported, which the resolution reports as the claim it overrode. The copy
// stands while the worker runs, and once the worker ends the service's pass
// removes it on the scratch proof, recorded on the task (P12), never
// because the report was filed. PRD section 6's precedence reads a dead
// terminal with no run as Unknown rather than the history, so the report's
// claim is what the resolution still carries then; the pull request that
// added this test raises that as a finding.
//
// A person's shell running the same route is refused as no task's worker,
// from the credentials and the parent chain the service reads and not from
// anything the command says, and the history does not grow.
func TestAWorkerReportsBackThroughTheRouteItsGrantAllows(t *testing.T) {
	principles.Cite(t, principles.P3)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P12)
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, route := serveRouted(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--shape", "investigation", "--project", "look",
		"--instructions", "Look into how the parser handles empty input.", "--repository", repository)
	id := created.Record.ID
	waitSessions(t, h, id, 1)

	// The grant and the instructions carry the same route.
	started := workerCall(t, agent, 1)
	if got := allowedTools(started.Args); got != "Bash(git:*),Bash("+route+":*)" {
		t.Fatalf("the worker was allowed %q, want git and the route %q (command line %q)", got, route, started.Args)
	}
	commands := routeCommands(t, started.Lines[0], route)
	for _, sub := range []string{"acknowledge", "report", "event"} {
		if commands[sub] == "" {
			t.Fatalf("the instructions name no %s command after the route; they read:\n%s", sub, started.Lines[0])
		}
	}

	// A message, then its acknowledgement through the route.
	first := messageWorker(t, h, subject, id, "Keep to the parser.")
	if lines := workerCall(t, agent, 2).Lines; !strings.Contains(lines[1], "Message "+strconv.FormatInt(first, 10)) {
		t.Fatalf("the message reached the worker as %q, with no number to acknowledge it by", lines[1])
	}
	ack := strings.Replace(commands["acknowledge"], "NUMBER", strconv.FormatInt(first, 10), 1)
	carrier := messageWorker(t, h, subject, id, runMarker+ack)
	waitFor(t, "the acknowledgement", func() bool { return messageOf(t, h, id, first).Acknowledged() })
	if got := messageOf(t, h, id, carrier); got.Acknowledged() {
		t.Fatalf("message %d, which only carried the command, reads acknowledged", carrier)
	}
	records := openStore(t, h)
	acked, err := records.TaskMessage(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	history, err := records.TaskEvents(t.Context(), id, acked.AcknowledgedEvent.Or(0)-1, 1)
	_ = records.Close()
	if err != nil || len(history) != 1 || history[0].Kind != store.MessageAcknowledgedEventKind ||
		history[0].Detail != strconv.FormatInt(first, 10) {
		t.Fatalf("the record names event %d and the history holds %+v there (%v), want the acknowledgement",
			acked.AcknowledgedEvent.Or(0), history, err)
	}

	// A report through the route: an ask, a finding naming no action, and a
	// note, on standard input.
	report := `{"summary": "Empty input reaches the tokenizer unchecked.", "findings": [` +
		`{"severity": "warning", "action": "ask", "description": "Whether empty input should be an error."}, ` +
		`{"severity": "warning", "description": "Whether the tokenizer should own the check."}, ` +
		`{"severity": "info", "action": "note", "description": "The tests cover no empty input."}]}`
	example := strings.Split(commands["report"], "\n")
	if len(example) != 3 {
		t.Fatalf("the report command reads %q, want the command, the example and the terminator", commands["report"])
	}
	filing := example[0] + "\n" + report + "\n" + example[2]
	messageWorker(t, h, subject, id, runMarker+filing)
	waitFor(t, "the report", func() bool {
		records := openStore(t, h)
		defer func() { _ = records.Close() }()
		_, err := records.TaskReport(t.Context(), id)
		return err == nil
	})
	if n := historyKinds(t, h, id)[store.ReportReadyEventKind]; n != 1 {
		t.Fatalf("the history records the report %d times, want once", n)
	}
	holds := openHoldsOf(t, h, id)
	if len(holds) != 2 {
		t.Fatalf("the report registered %d open holds, want two: the ask and the finding with no action", len(holds))
	}
	for _, hold := range holds {
		if !strings.HasPrefix(hold.Key, tasks.HoldKey(id, "")) {
			t.Fatalf("hold %s is not one a report registers", hold.Key)
		}
	}
	held := taskOf(t, h, subject, id)
	if held.State.State != taskstate.Held || held.State.Source != taskstate.SourceHold {
		t.Fatalf("with the report's decisions open the task reads %s from %s, want held", held.State.State, held.State.Source)
	}
	for range holds {
		answerTask(t, h, subject, id, "approved")
	}
	// The worker still runs, and a live terminal outranks the history, which
	// the resolution reports as the claim it overrode.
	if got := taskOf(t, h, subject, id); got.State.State != taskstate.Running || got.State.Source != taskstate.SourceProcess ||
		!claimsReported(got.State) {
		t.Fatalf("with its decisions answered and its worker running the task reads %s from %s overriding %+v, "+
			"want running from the process over the report's claim", got.State.State, got.State.Source, got.State.Superseded)
	}

	// A person's shell running the same route is refused.
	before := historyKinds(t, h, id)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	shell := exec.CommandContext(ctx, "/bin/sh", "-c", route+" event working --detail mine --json")
	shell.Dir = subject
	out, err := shell.Output()
	var exit *exec.ExitError
	var failure machine.Failure
	if !errors.As(err, &exit) || machine.Code(exit.ExitCode()) != machine.ExitFailure ||
		json.Unmarshal(out, &failure) != nil || failure.Code != string(ipc.CodeNotWorker) {
		t.Fatalf("a person's shell running the route = %v:\n%s", err, out)
	}
	if after := historyKinds(t, h, id); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused call grew the history from %v to %v", before, after)
	}

	// The copy stands while the worker runs, and goes once it has ended.
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("the copy of a running worker is gone: %v", err)
	}
	messageWorker(t, h, subject, id, endWorker)
	t.Cleanup(func() {
		if t.Failed() {
			text, _ := os.ReadFile(h.ServiceLog())
			t.Logf("the service log:\n%s", text)
		}
	})
	waitFor(t, "the copy's recorded removal", func() bool { return taskOf(t, h, subject, id).State.Basis.CleanupRemoved })
	if _, err := os.Lstat(copyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the task records its copy removed and %s still stands: %v", copyPath, err)
	}
	got := taskOf(t, h, subject, id)
	if !got.State.Basis.CleanupRemoved || got.State.Basis.Process != taskstate.Dead || !claimsReported(got.State) {
		t.Fatalf("with its worker ended and its copy gone the task reads %s from %s with %+v overriding %+v, "+
			"want the removal recorded over a dead terminal and the report's claim", got.State.State, got.State.Source,
			got.State.Basis, got.State.Superseded)
	}
}

// claimsReported reports whether the history's newest entry, as the
// resolver read it, is the report and claims Reported: the answer when
// nothing above the history decided, and otherwise the claim the answer
// overrode.
func claimsReported(r taskstate.Resolution) bool {
	if r.Basis.EventKind != store.ReportReadyEventKind {
		return false
	}
	if r.State == taskstate.Reported && r.Source == taskstate.SourceEvent {
		return true
	}
	for _, overridden := range r.Superseded {
		if overridden.Sequence == r.Basis.EventSequence && overridden.Claimed == taskstate.Reported {
			return true
		}
	}
	return false
}

// allowedTools is the value a worker's command line gave --allowedTools,
// read the way the CLI reads a flag and its value.
func allowedTools(args []string) string {
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// messageOf is one of the task's message records.
func messageOf(t *testing.T, h *home.Home, id string, message int64) store.Message {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	got, err := records.TaskMessage(t.Context(), message)
	if err != nil || got.TaskID != id {
		t.Fatalf("reading message %d of task %s: %+v, %v", message, id, got, err)
	}
	return got
}
