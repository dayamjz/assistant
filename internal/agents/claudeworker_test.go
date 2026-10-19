package agents_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
)

// The Claude Code adapter's worker is told exactly what it was sent. The
// stand-in records the text of each user message it decoded off the wire, so
// a framing that lost, added or altered anything - a newline appended to the
// content, a quote or a non-ASCII letter mangled, a second line split into a
// second message - shows here as a difference in what the worker read.
//
// The command line is asserted too, because it is what selects the stand-in's
// stream mode and the CLI's streaming input alike, and because it carries no
// --resume: a worker opens a conversation and continues none.
func TestTheClaudeWorkerIsToldExactlyWhatItIsSent(t *testing.T) {
	t.Parallel()
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "second message", Reply: "the second message was read"}},
	}}, agents.WithTerminationGrace(300*time.Millisecond))

	instructions := "Work on the task in this copy."
	sent := "the second message\nspans two lines, with \"quotes\", a tab\t, <angles> & é"
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: instructions,
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	if err := worker.Send(t.Context(), sent); err != nil {
		t.Fatalf("sending: %v", err)
	}
	result := waitForResult(t, worker)
	if result != "the second message was read" {
		t.Errorf("the worker's result line carries %q, want the scripted reply", result)
	}
	worker.Close()

	var call standin.Call
	for _, c := range agent.Calls() {
		if c.Worker {
			call = c
		}
	}
	if !call.Worker || !call.Stream {
		t.Fatalf("no stream-mode worker was recorded: %s", agent.Calls())
	}
	if want := []string{instructions, sent}; !slices.Equal(call.Lines, want) {
		t.Errorf("the worker read %q, want exactly %q", call.Lines, want)
	}
	managed := []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if len(call.Args) < len(managed) || !slices.Equal(call.Args[len(call.Args)-len(managed):], managed) {
		t.Errorf("the worker was started with %q, want it to end with %q", call.Args, managed)
	}
	if call.Session() != "" {
		t.Errorf("the worker was asked to resume %q, and a worker continues no conversation", call.Session())
	}
}

// A worker role on Claude Fable 5.1 reaches the Claude Code CLI as
// --model claude-fable-5-1. The role is the operator's YAML document read by
// the real parser and resolver, the identifier is Translate's, which is the
// call internal/service's launch makes, and the command line is what the
// stand-in recorded off the process the adapter started, so no step states the
// identifier the next one reads.
func TestAFableWorkerRoleReachesTheClaudeCommandLine(t *testing.T) {
	t.Parallel()
	doc := "roles:\n  worker-hard:\n    agent: claude\n    provider: anthropic\n    type: fable\n    version: \"5.1\"\n    effort: high\n"
	layer, err := config.ParseYAML(config.OriginGlobal, []byte(doc))
	if err != nil {
		t.Fatalf("parsing the roles: %v", err)
	}
	res, err := config.Resolve(layer, config.Absent(config.OriginTrusted))
	if err != nil {
		t.Fatalf("resolving the roles: %v", err)
	}
	role, err := res.Config.Roles.ForTask("worker-hard")
	if err != nil {
		t.Fatalf("resolving the task's role: %v", err)
	}
	model, err := agents.Translate(agents.ClaudeName, role.Provider, role.Type, role.Version)
	if err != nil {
		t.Fatalf("translating %s %s %s: %v", role.Provider, role.Type, role.Version, err)
	}
	if err := agents.AcceptsEffort(agents.ClaudeName, role.Effort); err != nil {
		t.Fatalf("the Claude adapter refused effort %q: %v", role.Effort, err)
	}

	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "Work on the task", Reply: "started"}},
	}}, agents.WithTerminationGrace(300*time.Millisecond))
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "Work on the task in this copy.", Model: model, Effort: role.Effort,
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	if got := waitForResult(t, worker); got != "started" {
		t.Fatalf("the worker answered %q, want the scripted reply", got)
	}
	worker.Close()

	var call standin.Call
	for _, c := range agent.Calls() {
		if c.Worker {
			call = c
		}
	}
	if !call.Worker {
		t.Fatalf("no worker was recorded: %s", agent.Calls())
	}
	if call.Model() != "claude-fable-5-1" {
		t.Errorf("the worker was started with --model %q, want claude-fable-5-1: %q", call.Model(), call.Args)
	}
	if i := slices.Index(call.Args, "--effort"); i < 0 || i+1 >= len(call.Args) || call.Args[i+1] != "high" {
		t.Errorf("the worker was started without --effort high: %q", call.Args)
	}
}

// waitForResult polls the worker's Capture until a stream-json result line is
// in it and returns that line's result. Capture is the adapter's account of
// the output, handed over as bytes, so this decodes it the way a reader of
// the CLI's stream-json output would and not through anything the adapter
// parsed.
func waitForResult(t *testing.T, worker agents.Worker) string {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		for line := range strings.Lines(worker.Capture().Text) {
			var decoded struct {
				Type   string `json:"type"`
				Result string `json:"result"`
			}
			if json.Unmarshal([]byte(line), &decoded) == nil && decoded.Type == "result" {
				return decoded.Result
			}
		}
		select {
		case <-worker.Done():
			t.Fatalf("the worker ended before it printed a result: %s", worker.Capture().Text)
		case <-t.Context().Done():
			t.Fatalf("waiting for a result: %v", t.Context().Err())
		case <-tick.C:
		}
	}
}

// A stream-json message cannot carry bytes that are not UTF-8 unchanged, so
// instructions holding them are refused before a process starts rather than
// delivered altered, and a send holding them is refused before anything is
// written, leaving the worker running and its input in order.
func TestTheClaudeWorkerRefusesTextAMessageCannotCarry(t *testing.T) {
	t.Parallel()
	agent := standin.New(t, standin.Script{}, agents.WithTerminationGrace(300*time.Millisecond))
	invalid := "not utf-8: \xff\xfe"

	_, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: invalid,
	})
	if !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("starting a worker over invalid UTF-8 answered %v, want ErrInvalidInvocation", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Fatalf("a refused worker started a process: %s", calls)
	}

	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "valid instructions",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	if err := worker.Send(t.Context(), invalid); !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Errorf("sending invalid UTF-8 answered %v, want ErrInvalidInvocation", err)
	}
	if err := worker.Send(t.Context(), "valid afterwards"); err != nil {
		t.Errorf("a send after a refused one answered %v, want it delivered", err)
	}
}
