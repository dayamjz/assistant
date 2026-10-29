//go:build unix

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// writeMarker is what a coordinator's message carries ahead of a file the
// stream worker is to write with its file editing tool: a path on the first
// line, relative to the worker's copy, and the content after it.
const writeMarker = "please write: "

// serveRouted serves the home with the shipped Claude Code adapter's worker,
// the stand-in in stream mode, as the resolved agent, and with the route the
// product's service start gives a worker: this test binary, which hookMain
// hands to cli.Run, --home naming the home, and the worker verb. The worker
// runs what a message carries after runMarker, in a process group of its
// own, writes what a message carries after writeMarker the way the Claude
// Code CLI's file editing tool writes a file, and ends on endWorker.
func serveRouted(t *testing.T, h *home.Home) (*standin.Agent, string) {
	t.Helper()
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{
				{Contains: writeMarker, Write: true},
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
		Catalog:     agentCatalog(agent.Runner()),
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
// begins with the route, keyed by the subcommand.
func routeCommands(t *testing.T, instructions, route string) map[string]string {
	t.Helper()
	commands := map[string]string{}
	for _, text := range strings.Split(instructions, "\n") {
		line, ok := strings.CutPrefix(text, "    "+route+" ")
		if !ok {
			continue
		}
		commands[strings.Fields(line)[0]] = route + " " + line
	}
	return commands
}

// reportFile is the file the report command the instructions name reads
// from, the one word after its --file.
func reportFile(t *testing.T, command string) string {
	t.Helper()
	_, named, ok := strings.Cut(command, " --file ")
	if !ok || len(strings.Fields(named)) != 1 || named == "-" {
		t.Fatalf("the report command reads %q, which names no file to file from", command)
	}
	return named
}

// fileReport has the worker of task id file report the way its
// instructions say a worker shaped like the Claude Code CLI can: it writes
// the report with its file editing tool into the file the report command
// names, inside its copy, and then runs that command. It waits until the
// report is stored and the command has removed the file.
func fileReport(t *testing.T, h *home.Home, subject, id, command, report string) {
	t.Helper()
	file := reportFile(t, command)
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(copyPath, file)
	messageWorker(t, h, subject, id, writeMarker+file+"\n"+report)
	waitFor(t, "the report written in the copy", func() bool {
		_, err := os.Stat(written)
		return err == nil
	})
	messageWorker(t, h, subject, id, runMarker+command)
	waitFor(t, "the report", func() bool {
		records := openStore(t, h)
		defer func() { _ = records.Close() }()
		_, err := records.TaskReport(t.Context(), id)
		return err == nil
	})
	waitFor(t, "the report file's removal", func() bool {
		_, err := os.Lstat(written)
		return errors.Is(err, os.ErrNotExist)
	})
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
// carried the command stays unread. The worker files its report the way the
// instructions say, which is the way a worker shaped like the Claude Code
// CLI can (#267): it writes the report with its file editing tool into the
// file the report command names, inside its copy, and runs that command,
// whose here-document predecessor the stand-in refuses as the CLI's Bash
// check did. The report is stored, the command removes the file so it
// cannot stay in the copy, the history records the report once, and
// each of its findings that asks, or names no action, becomes a hold (P3),
// so the task reads Held over the report until a person answers both, and
// then Reported from the stored report while the worker's terminal is still
// live, because PRD section 6 has a stored report outrank the terminal. The
// copy stands while the worker runs and goes on standing through several of
// the service's passes once the worker ends, with no removal recorded,
// because a reported investigation's copy is kept for a promotion (P12); the
// task reads Reported from the report over its dead terminal, and never
// Complete. Its removal on the record of a relay is relay_unix_test.go's.
//
// A person's shell running the same route is refused as no task's worker,
// from the credentials and the parent chain the service reads and not from
// anything the command says, and the history does not grow.
func TestAWorkerReportsBackThroughTheRouteItsGrantAllows(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P12)
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
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
	// note, written into the copy and filed from there.
	report := `{"summary": "Empty input reaches the tokenizer unchecked.", "findings": [` +
		`{"severity": "warning", "action": "ask", "description": "Whether empty input should be an error."}, ` +
		`{"severity": "warning", "description": "Whether the tokenizer should own the check."}, ` +
		`{"severity": "info", "action": "note", "description": "The tests cover no empty input."}]}`
	fileReport(t, h, subject, id, commands["report"], report)
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
	for _, hold := range holds {
		answerNamedTask(t, h, subject, id, hold.Key, "approved")
	}
	// The worker still runs, and the stored report outranks its live
	// terminal.
	if got := taskOf(t, h, subject, id); !reportedFrom(got.State, taskstate.Live) {
		t.Fatalf("with its decisions answered and its worker running the task reads %s from %s (basis %+v), "+
			"want reported from the stored report over a live terminal", got.State.State, got.State.Source, got.State.Basis)
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

	// The copy stands while the worker runs, and after it has ended.
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("the copy of a running worker is gone: %v", err)
	}
	messageWorker(t, h, subject, id, endWorker)
	waitFor(t, "the reported investigation over its dead terminal", func() bool {
		return reportedFrom(taskOf(t, h, subject, id).State, taskstate.Dead)
	})
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("the copy of a reported investigation whose worker ended is gone, so no promotion can relaunch in it: %v", err)
	}
	if got := taskOf(t, h, subject, id); got.State.Basis.CleanupRemoved || !reportedFrom(got.State, taskstate.Dead) {
		t.Fatalf("with its worker ended the task reads %s from %s with %+v, "+
			"want reported from the stored report over a dead terminal with its copy kept", got.State.State, got.State.Source,
			got.State.Basis)
	}
}

// reportedFrom reports whether r is PRD section 6's report tier answering
// over a terminal read as process: Reported from the stored report row,
// which the basis records, and not from the history entry beside it (P8).
func reportedFrom(r taskstate.Resolution, process taskstate.Liveness) bool {
	return r.State == taskstate.Reported && r.Source == taskstate.SourceReport && r.Basis.ReportStored &&
		r.Basis.Process == process && r.Basis.EventKind == store.ReportReadyEventKind
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
