package standin

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// rawStreamWorker starts a stream-mode stand-in through the scripted adapter,
// which writes every send as the line it is, so a test can put on the wire
// exactly the line it wants the stream mode to read rather than one the
// Claude Code adapter framed. The instructions are the first such line.
func rawStreamWorker(t *testing.T, script Script, instructions string) (*Agent, agents.Worker) {
	t.Helper()
	agent := NewWorker(t, script)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agents.ScriptedFactory(
		agents.WithBinary(self),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), append(agent.Arguments(), "--print", "--input-format", "stream-json"))
	if err != nil {
		t.Fatalf("building the raw runner: %v", err)
	}
	worker, err := agents.OpenWorker(t.Context(), runner, agents.WorkerSpec{Dir: t.TempDir(), Instructions: instructions})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	t.Cleanup(worker.Close)
	return agent, worker
}

// The stream mode's three rules for a line, each observed through the wire: a
// JSON line that is not a user message is passed over and recorded nowhere, a
// user message whose content is a list of text blocks is read as their text,
// and a line that is not JSON ends the worker.
func TestTheStreamModeReadsLinesTheWayItsDocumentationStates(t *testing.T) {
	t.Parallel()
	script := Script{Worker: &WorkerScript{Exchanges: []Exchange{{Contains: "blocks", Reply: "read the blocks"}}}}
	agent, worker := rawStreamWorker(t, script, `{"type":"assistant","message":{"role":"assistant","content":"not for the worker"}}`)

	if err := worker.Send(t.Context(),
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"text in "},{"type":"text","text":"blocks"}]}}`); err != nil {
		t.Fatalf("sending a user message: %v", err)
	}
	got := waitForOutput(t, worker, `"result":"read the blocks"`)
	if !strings.Contains(got, `passing over a streaming input line of type "assistant"`) {
		t.Errorf("the non-user line drew no note; the output is:\n%s", got)
	}
	call := waitForLines(t, agent, 1)
	if !call.Stream || !slices.Equal(call.Lines, []string{"text in blocks"}) {
		t.Errorf("the worker recorded stream %v and lines %q, want the block text alone", call.Stream, call.Lines)
	}

	if err := worker.Send(t.Context(), "plain text is not a message"); err != nil {
		t.Fatalf("sending a line that is not JSON: %v", err)
	}
	select {
	case <-worker.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the worker is still running after a line that is not JSON")
	}
	if !strings.Contains(worker.Capture().Text, "error parsing streaming input line") {
		t.Errorf("the worker ended without saying why; its output is:\n%s", worker.Capture().Text)
	}
}

// A greeting is a line-mode shape, so a stream-mode worker scripted with one
// refuses rather than printing a line the CLI in that mode was not seen to
// print.
func TestTheStreamModeRefusesAGreeting(t *testing.T) {
	t.Parallel()
	_, worker := rawStreamWorker(t, Script{Worker: &WorkerScript{Greeting: "hello"}},
		`{"type":"user","message":{"role":"user","content":"anything"}}`)
	select {
	case <-worker.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("a stream-mode worker scripted with a greeting is still running")
	}
	if got := worker.Capture().Text; strings.Contains(got, "hello") {
		t.Errorf("the stream-mode worker printed its greeting:\n%s", got)
	}
}

// An exchange playing Recorded lines prints them as they are, in place of
// the turn a stream-mode worker otherwise prints, and the Claude Code
// adapter's capture of them says they are stream-json, so the transcript
// reader reads them as the agent's. The same lines from a line-mode worker
// are marked text, the control that the mark is the adapter's and not the
// bytes'. A script that would play lines beside a Reply or a Run, or a line
// that would print as two, is refused before anything starts.
func TestRecordedLinesArePlayedAsTheyAre(t *testing.T) {
	t.Parallel()
	recorded := []string{
		`{"type":"system","subtype":"thinking_tokens","tokens":4}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"played back"}]}}`,
	}
	script := Script{Worker: &WorkerScript{Exchanges: []Exchange{{Contains: "play", Recorded: recorded}}}}
	stream, err := agents.OpenWorker(t.Context(), New(t, script).Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "play it"})
	if err != nil {
		t.Fatalf("starting the stream worker: %v", err)
	}
	t.Cleanup(stream.Close)
	waitForOutput(t, stream, "played back")
	captured := stream.Capture()
	if captured.Text != strings.Join(recorded, "\n")+"\n" || captured.Format != agents.OutputStreamJSON {
		t.Fatalf("the stream worker printed %q marked %q, want exactly the recorded lines marked stream-json", captured.Text, captured.Format)
	}
	read := agents.ReadTranscript(captured.Text, captured.Format, false)
	if len(read.Events) != 1 || read.Events[0].Text != "played back" || read.Omitted != 1 {
		t.Fatalf("the played lines read as %+v with %d left out, want the text and the counter left out", read.Events, read.Omitted)
	}

	line, err := agents.OpenWorker(t.Context(), NewWorker(t, script).Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "play it"})
	if err != nil {
		t.Fatalf("starting the line worker: %v", err)
	}
	t.Cleanup(line.Close)
	waitForOutput(t, line, "played back")
	if got := line.Capture(); got.Format != agents.OutputText || !strings.Contains(got.Text, recorded[0]) {
		t.Fatalf("the line worker printed %q marked %q, want the recorded lines marked text", got.Text, got.Format)
	}

	for _, bad := range []Exchange{
		{Contains: "x", Reply: "also this", Recorded: recorded},
		{Contains: "x", Run: true, Recorded: recorded},
		{Contains: "x", Recorded: []string{"one\ntwo"}},
	} {
		if err := bad.valid(); err == nil {
			t.Errorf("the exchange %+v was accepted, want it refused", bad)
		}
	}
	if err := (Exchange{Contains: "x", Recorded: recorded}).valid(); err != nil {
		t.Errorf("a well-formed playback was refused: %v", err)
	}
}
