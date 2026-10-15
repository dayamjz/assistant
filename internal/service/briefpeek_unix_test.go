//go:build unix

package service_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// These tests drive a promotion's brief and a peek over the service's real
// socket against the shipped Claude Code adapter's worker, the stand-in in
// stream mode, on steer_unix_test.go's terms: what the program sends is
// framed by the real adapter and read off the wire by the process, and what
// a peek reads is what the process printed.

// streamWorkerOf is steer_unix_test.go's streamWorker for a task of shape,
// with the exchanges given tried before that file's, so a test adds what
// its worker answers or plays back without losing the shell and the
// instructions exchanges every launched worker needs.
func streamWorkerOf(t *testing.T, shape store.TaskShape, branch string, exchanges []standin.Exchange,
	body func(w launched, agent *standin.Agent)) {
	t.Helper()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	quietMaintenance(t, gateRepositoryOf(t, h, subject))
	if err := os.WriteFile(h.ConfigFile(), []byte(steerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	id := recordDispatchedTask(t, h, repository, shape, branch)
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{Exchanges: append(exchanges,
			standin.Exchange{Contains: shellMarker, Run: true},
			standin.Exchange{Contains: "git rev-parse --show-toplevel", Reply: "the instructions were read"},
			standin.Exchange{Contains: "finish", Reply: "worker finished", Ends: true},
		)},
	}, agents.WithTerminationGrace(2*time.Second))
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()})
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, session := launchedSession(t, running.service, id)
		waitCapture(t, provider, session, "the instructions were read")
		body(launched{running: running, home: h, repository: repository, gateRepo: gateRepositoryOf(t, h, subject),
			task: id, group: session.Reference().PID, provider: provider, session: session}, agent)
	})
}

// promoteTo promotes the task onto branch from this process, which is
// outside every worker group, with intent, and fails the test on a refusal.
func promoteTo(t *testing.T, w launched, branch, intent string) machine.TaskPromoted {
	t.Helper()
	var promoted machine.TaskPromoted
	if err := w.running.client.Call(t.Context(), ipc.MethodTaskPromote, machine.TaskPromoteRequest{
		Task: w.task, Repository: w.repository.ID, Branch: branch, Intent: intent,
	}, &promoted); err != nil {
		t.Fatalf("task.promote from outside every worker group: %v", err)
	}
	return promoted
}

// briefHeard is the reply the worker prints when it reads a brief: every
// brief says the task is now a delivery task, and no instructions or
// message here does.
const briefHeard = "heard the new brief"

// PRD section 5's promotion, in the same terminal with the same loaded
// context, and P8 for the brief it sends. Promoting a live investigation
// sends its worker the brief as a message record, through the route
// task.message takes: the answer names the record, the record says written
// and the coordinator sent it, its text is exactly what the instructions
// file was given and carries the branch and the intent, the worker read it
// on exactly one line and answered it, and its acknowledgement, through
// task.event, is held on that record. The person's row says the worker was
// sent what it is to deliver.
func TestAPromotionSendsTheLiveWorkerItsBriefAsAMessage(t *testing.T) {
	principles.Cite(t, principles.P8)
	exchanges := []standin.Exchange{{Contains: "now a delivery task", Reply: briefHeard}}
	streamWorkerOf(t, store.TaskInvestigation, "", exchanges, func(w launched, agent *standin.Agent) {
		intent := "Make the build fast by caching the parser."
		promoted := promoteTo(t, w, "task/briefed", intent)

		brief := promoted.Brief
		if !brief.Delivered() || brief.Unsent != "" || brief.Message == nil {
			t.Fatalf("the promotion answered the brief %+v, want a message written to the live worker", brief)
		}
		stored := messagesOf(t, w)
		if len(stored) != 1 || stored[0].ID != brief.Message.ID || stored[0].Delivery() != store.DeliveryWritten ||
			stored[0].SentBy != store.SentByCoordinator() || stored[0].Attempt != 1 {
			t.Fatalf("the records hold %+v, want the one brief the answer names, written to attempt 1 by the coordinator", stored)
		}
		instructions, err := os.ReadFile(promoted.Instructions)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(instructions), stored[0].Text) ||
			!strings.Contains(stored[0].Text, intent) || !strings.Contains(stored[0].Text, "task/briefed") {
			t.Fatalf("the brief reads %q and the instructions end %q, want one text carrying the branch and the intent",
				stored[0].Text, instructions)
		}
		waitCapture(t, w.provider, w.session, briefHeard)
		if n := linesCarrying(t, agent, intent); n != 1 {
			t.Fatalf("the worker read the intent on %d lines, want exactly one", n)
		}

		if _, code := callAs(t, w, ipc.MethodTaskEvent, machine.TaskEventRequest{
			Kind: store.MessageAcknowledgedEventKind, Detail: strconv.FormatInt(stored[0].ID, 10),
		}); code != "" {
			t.Fatalf("the worker's acknowledgement of the brief was refused with %q", code)
		}
		if acked := messagesOf(t, w)[0]; !acked.Acknowledged() {
			t.Fatalf("the brief's record reads %+v after the acknowledgement, want it acknowledged", acked)
		}
		if machine.TaskPromotedPhrase(promoted) == machine.TaskPromotedPhrase(machine.TaskPromoted{}) {
			t.Fatalf("a brief written reads as one never sent: %q", machine.TaskPromotedPhrase(promoted))
		}
	})
}

// The control: an investigation whose worker the coordinator stopped is
// promoted all the same, and the answer says no running worker was sent the
// brief and why, with no message recorded and no line reaching any worker;
// the brief is in the instructions file, which is where a worker begun on
// the task later reads it. The person's row says the worker was not sent it.
func TestAPromotionWithNoLiveWorkerSaysTheBriefWasNotSent(t *testing.T) {
	exchanges := []standin.Exchange{{Contains: "now a delivery task", Reply: briefHeard}}
	streamWorkerOf(t, store.TaskInvestigation, "", exchanges, func(w launched, agent *standin.Agent) {
		var stopped machine.TaskStopped
		if err := w.running.client.Call(t.Context(), ipc.MethodTaskStop, machine.TaskRequest{Task: w.task}, &stopped); err != nil {
			t.Fatalf("task.stop: %v", err)
		}
		intent := "Make the build fast by caching the parser."
		promoted := promoteTo(t, w, "task/unbriefed", intent)

		if promoted.Record.Shape != store.TaskDelivery {
			t.Fatalf("the promotion of a stopped investigation answered %+v, want a delivery task", promoted.Record)
		}
		if promoted.Brief.Delivered() || promoted.Brief.Message != nil || !strings.Contains(promoted.Brief.Unsent, string(taskstate.Dead)) {
			t.Fatalf("the brief to a stopped worker answered %+v, want none sent, saying the worker read dead", promoted.Brief)
		}
		if got := messagesOf(t, w); len(got) != 0 {
			t.Fatalf("a brief nobody could be sent recorded messages: %+v", got)
		}
		if n := linesCarrying(t, agent, intent); n != 0 {
			t.Fatalf("the intent reached a worker on %d lines, want none", n)
		}
		instructions, err := os.ReadFile(filepath.Clean(promoted.Instructions))
		if err != nil || !strings.Contains(string(instructions), intent) || !strings.Contains(string(instructions), "task/unbriefed") {
			t.Fatalf("the instructions read %q (%v), want the brief a later worker is begun with", instructions, err)
		}
		row := machine.TaskPromotedPhrase(promoted)
		if row != machine.TaskPromotedPhrase(machine.TaskPromoted{}) || !strings.Contains(row.Decision, "coordinator message") {
			t.Fatalf("a brief never sent reads %q, want the row that says to send it", row)
		}
	})
}

// recordedStream is the Claude Code CLI's stream-json in testdata, recorded
// from two runs of the CLI 2.1.301 in this mode with the session, the
// working directory, the tool list and a few process metrics scrubbed or cut
// down, and nothing else changed.
func recordedStream(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude-worker-stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// thinkingCounter is the counter #270 found filling a peek, by its type and
// subtype, the two fields it quoted; the rest of its fields are this test's.
func thinkingCounter(n int) string {
	return fmt.Sprintf(`{"type":"system","subtype":"thinking_tokens","tokens":%d,"session_id":"recorded-session"}`, n)
}

// peekOf reads the task's worker through task.peek from this process.
func peekOf(t *testing.T, w launched) machine.TaskPeek {
	t.Helper()
	var peeked machine.TaskPeek
	if err := w.running.client.Call(t.Context(), ipc.MethodTaskPeek, machine.TaskRequest{Task: w.task}, &peeked); err != nil {
		t.Fatalf("task.peek: %v", err)
	}
	return peeked
}

// eventsOfKind is the events of kind a peek shows.
func eventsOfKind(p machine.TaskPeek, kind agents.TranscriptKind) []agents.TranscriptEvent {
	var out []agents.TranscriptEvent
	for _, e := range p.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// #270's peek. A worker that played back the recorded CLI stream peeks as
// what a person following it sees: its text, its tool calls named with
// their arguments, their results and the turns' ends, with none of the
// counters, status lines or thinking the stream carries and those counted,
// a type the reader has no row for shown as one marker, and a credential
// in a tool's argument redacted. A tail full of thinking counters with one
// assistant message in it peeks as that message, which the raw tail it
// replaced never carried: the counters alone are many times the bound. And
// a worker that wrote more than the bound shows its most recent events,
// within it, with the rest counted. The control for the noise rule is the
// raw capture, which does carry every counter the peek left out.
func TestAPeekShowsTheWorkersConversationAndNotItsCounters(t *testing.T) {
	recorded := append(recordedStream(t),
		`{"type":"brand_new_event","detail":"a type no row names"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9","name":"Bash","input":{"command":"git clone https://ci:hunter2secret@example.com/r.git"}}]},"session_id":"recorded-session"}`)
	counters := make([]string, 0, 801)
	for i := range 400 {
		counters = append(counters, thinkingCounter(i))
	}
	counters = append(counters, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the one thing it said among the counters"}]},"session_id":"recorded-session"}`)
	for i := range 400 {
		counters = append(counters, thinkingCounter(400+i))
	}
	var chatter []string
	for i := range 300 {
		chatter = append(chatter, fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"line %03d %s"}]}}`,
			i, strings.Repeat("words ", 30)))
	}
	exchanges := []standin.Exchange{
		{Contains: "play the recording", Recorded: recorded},
		{Contains: "count your tokens", Recorded: counters},
		{Contains: "say a great deal", Recorded: chatter},
	}
	streamWorkerOf(t, store.TaskDelivery, "task/peek", exchanges, func(w launched, _ *standin.Agent) {
		send := func(text, until string) {
			var sent machine.TaskMessaged
			if err := w.running.client.Call(t.Context(), ipc.MethodTaskMessage, machine.TaskMessageRequest{Task: w.task, Text: text}, &sent); err != nil {
				t.Fatalf("task.message %q: %v", text, err)
			}
			waitCapture(t, w.provider, w.session, until)
		}

		send("play the recording", "hunter2secret")
		peeked := peekOf(t, w)
		texts := eventsOfKind(peeked, agents.TranscriptText)
		calls := eventsOfKind(peeked, agents.TranscriptToolCall)
		results := eventsOfKind(peeked, agents.TranscriptToolResult)
		joined := fmt.Sprintf("%+v", peeked.Events)
		for _, want := range []string{"1001 = 7 × 11 × 13, so it isn't prime. Running the command now.", "done"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the peek's events carry no %q:\n%s", want, peeked.Output)
			}
		}
		if len(texts) < 3 || len(calls) != 4 || len(results) != 3 || len(eventsOfKind(peeked, agents.TranscriptTurnEnded)) < 2 {
			t.Errorf("the recording peeked as %d texts, %d tool calls, %d results and the turns' ends:\n%s",
				len(texts), len(calls), len(results), peeked.Output)
		}
		for _, want := range []string{"- Bash(echo checked)", "- Read(/etc/hostname)", "  = hello-peek", "[unrecognized brand_new_event]",
			"- Bash(git clone https://redacted@example.com/r.git)"} {
			if !strings.Contains(peeked.Output, want) {
				t.Errorf("the peek's output carries no %q:\n%s", want, peeked.Output)
			}
		}
		for _, noise := range []string{"hunter2secret", "rate_limit", "thinking", "task_summary", "post_turn_summary", "autocompact", `"type"`} {
			if strings.Contains(peeked.Output, noise) {
				t.Errorf("the peek's output carries %q:\n%s", noise, peeked.Output)
			}
		}
		if peeked.Omitted == 0 {
			t.Errorf("the peek counted nothing left out of a stream full of status lines")
		}

		send("count your tokens", thinkingCounter(799))
		raw, err := w.provider.Capture(w.session)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(raw.Text, `"subtype":"thinking_tokens"`) < 800 {
			t.Fatalf("the raw capture carries %d counters, want the 800 played back", strings.Count(raw.Text, `"subtype":"thinking_tokens"`))
		}
		if tail := raw.Text[len(raw.Text)-machine.MaxPeekBytes:]; strings.Contains(tail, "the one thing it said") {
			t.Fatalf("the raw tail of %d bytes already carries the message, so this test shows nothing about the counters", machine.MaxPeekBytes)
		}
		peeked = peekOf(t, w)
		if !strings.Contains(peeked.Output, "the one thing it said among the counters") || strings.Contains(peeked.Output, "thinking_tokens") {
			t.Fatalf("a tail of counters around one message peeked as:\n%s", peeked.Output)
		}
		if peeked.Omitted < 800 {
			t.Errorf("the peek counted %d left out, want at least the 800 counters", peeked.Omitted)
		}

		send("say a great deal", "line 299")
		peeked = peekOf(t, w)
		if len(peeked.Output) > machine.MaxPeekBytes || peeked.Earlier == 0 {
			t.Fatalf("a worker that said more than the bound peeked as %d bytes with %d earlier events, want at most %d and some counted",
				len(peeked.Output), peeked.Earlier, machine.MaxPeekBytes)
		}
		last := peeked.Events[len(peeked.Events)-1]
		if !strings.HasPrefix(last.Text, "line 299 ") || strings.Contains(peeked.Output, "1001 = 7") {
			t.Fatalf("the bounded peek ends with %+v, want the most recent line and none of the earliest", last)
		}
		var rendered strings.Builder
		for _, e := range peeked.Events {
			rendered.WriteString(e.Line() + "\n")
		}
		if rendered.String() != peeked.Output {
			t.Fatalf("the output is not the events it names, rendered:\n%s\nagainst\n%s", peeked.Output, rendered.String())
		}
	})
}
