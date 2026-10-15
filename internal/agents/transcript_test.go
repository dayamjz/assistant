package agents_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// The lines below are the Claude Code CLI 2.1.301's stream-json, recorded
// from a worker started with --print and stream-json on both streams and cut
// down to the fields the reader reads, except the thinking_tokens counter,
// whose fields past its subtype are this test's: #270 quoted the type and
// subtype from a live session, and the recorded run wrote none.
const (
	recordedInit     = `{"type":"system","subtype":"init","cwd":"/copy","session_id":"s","tools":["Bash","Read"]}`
	recordedRate     = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"session_id":"s"}`
	recordedGoal     = `{"type":"active_goal","value":null,"session_id":"s"}`
	recordedCompact  = `{"type":"autocompact_state","value":{"enabled":true},"session_id":"s"}`
	recordedThinking = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"CAQS"}]},"session_id":"s"}`
	recordedText     = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"1001 = 7 × 11 × 13, so it isn't prime. Running the command now."}]},"session_id":"s"}`
	recordedToolUse  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo checked","description":"Print \"checked\""}}]},"session_id":"s"}`
	recordedSummary  = `{"type":"system","subtype":"task_summary","detail":"Reading /etc/hostname","session_id":"s"}`
	recordedResult   = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"checked","is_error":false}]},"session_id":"s"}`
	recordedPost     = `{"type":"system","subtype":"post_turn_summary","status_detail":"replied","session_id":"s"}`
	recordedEnd      = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s"}`
	counter          = `{"type":"system","subtype":"thinking_tokens","tokens":12,"session_id":"s"}`
)

// A Claude Code worker's stream reads as what a person following it sees:
// the agent's text, a tool call named with its command, the tool's answer
// and the turn's end, in order. Every counter, status line and thinking
// block is left out and counted rather than shown. A type, a system subtype
// and a block the reader has no row for are each one marker naming it, and
// a line that is not JSON is shown as it stands, so nothing the reader does
// not know is dropped. The control is the same capture read as text, which
// shows every line, so the stream reading is what left the noise out.
func TestAStreamCaptureReadsAsWhatAPersonSees(t *testing.T) {
	t.Parallel()
	capture := strings.Join([]string{
		recordedGoal, recordedCompact, recordedInit, recordedRate, recordedThinking, counter, counter,
		recordedText, recordedToolUse, recordedSummary, recordedResult, counter, recordedPost, recordedEnd,
		`{"type":"system","subtype":"never_seen"}`,
		`{"type":"brand_new_event","x":1}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"image","source":{}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":[{"type":"text","text":"permission denied"}],"is_error":true}]}}`,
		`{"type":"result","subtype":"error_max_turns","is_error":true}`,
		"Error: the CLI wrote this on standard error",
	}, "\n") + "\n"

	got := agents.ReadTranscript(capture, agents.OutputStreamJSON, false)
	want := []agents.TranscriptEvent{
		{Kind: agents.TranscriptText, Text: "1001 = 7 × 11 × 13, so it isn't prime. Running the command now."},
		{Kind: agents.TranscriptToolCall, Tool: "Bash", Text: "echo checked"},
		{Kind: agents.TranscriptToolResult, Text: "checked"},
		{Kind: agents.TranscriptTurnEnded},
		{Kind: agents.TranscriptUnrecognized, Text: "system/never_seen"},
		{Kind: agents.TranscriptUnrecognized, Text: "brand_new_event"},
		{Kind: agents.TranscriptUnrecognized, Text: "assistant block image"},
		{Kind: agents.TranscriptError, Text: "permission denied"},
		{Kind: agents.TranscriptError, Text: "error_max_turns"},
		{Kind: agents.TranscriptOutput, Text: "Error: the CLI wrote this on standard error"},
	}
	if !slices.Equal(got.Events, want) {
		t.Fatalf("the stream read as\n%+v\nwant\n%+v", got.Events, want)
	}
	// goal, compact, init, rate, thinking, three counters, two summaries.
	if got.Omitted != 10 {
		t.Errorf("the reader left out %d lines and blocks, want 10", got.Omitted)
	}

	asText := agents.ReadTranscript(capture, agents.OutputText, false)
	if len(asText.Events) != strings.Count(capture, "\n") || asText.Omitted != 0 {
		t.Errorf("read as text the capture gave %d events with %d left out, want every one of its %d lines",
			len(asText.Events), asText.Omitted, strings.Count(capture, "\n"))
	}
	unknown := agents.ReadTranscript(counter+"\n", "a-format-nobody-wrote", false)
	if len(unknown.Events) != 1 || unknown.Events[0] != (agents.TranscriptEvent{Kind: agents.TranscriptOutput, Text: counter}) {
		t.Errorf("a format the reader does not know read as %+v, want the line shown as text", unknown.Events)
	}
}

// A line the capture holds only part of is marked partial rather than shown
// as output or decoded: the first line when the capture's front was cut, and
// the last when it was not finished. A whole line in either place is read
// as itself, which is the control.
func TestALineTheCaptureHoldsPartOfIsMarkedPartial(t *testing.T) {
	t.Parallel()
	front := `":"thinking_tokens","tokens":3}`
	unfinished := `{"type":"assistant","message":{"role":"assis`
	got := agents.ReadTranscript(front+"\n"+recordedText+"\n"+unfinished, agents.OutputStreamJSON, true)
	if len(got.Events) != 3 || got.Events[0].Kind != agents.TranscriptPartial ||
		got.Events[1].Kind != agents.TranscriptText || got.Events[2].Kind != agents.TranscriptPartial {
		t.Fatalf("a cut front and an unfinished end read as %+v, want partial, the text, partial", got.Events)
	}
	whole := agents.ReadTranscript(recordedText+"\n"+recordedEnd+"\n", agents.OutputStreamJSON, false)
	if len(whole.Events) != 2 || whole.Events[0].Kind != agents.TranscriptText || whole.Events[1].Kind != agents.TranscriptTurnEnded {
		t.Fatalf("whole lines read as %+v, want the text and the turn's end", whole.Events)
	}
	text := agents.ReadTranscript("ated line\nsecond\n", agents.OutputText, true)
	if len(text.Events) != 2 || text.Events[0].Kind != agents.TranscriptPartial || text.Events[1].Text != "second" {
		t.Fatalf("a cut text capture read as %+v, want partial, then the second line", text.Events)
	}
}

// Abbreviated bounds each kind and says how much it let go, and Line renders
// each kind on its own terms. A short event is left as it is, the control.
func TestAnEventIsAbbreviatedAndRenderedByItsKind(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	cut := agents.TranscriptEvent{Kind: agents.TranscriptToolResult, Text: "first\n" + long + "\nthird"}.Abbreviated()
	if !strings.HasPrefix(cut.Text, "first (+2 lines)") || len(cut.Text) > 300 {
		t.Errorf("a long tool result abbreviated to %q, want its first line and a count of the rest", cut.Text)
	}
	text := agents.TranscriptEvent{Kind: agents.TranscriptText, Text: long}.Abbreviated()
	if len(text.Text) > 2100 || !strings.HasSuffix(text.Text, "(2952 bytes more)") {
		t.Errorf("long text abbreviated to %d bytes ending %q, want about 2 KiB and what was cut",
			len(text.Text), text.Text[len(text.Text)-20:])
	}
	short := agents.TranscriptEvent{Kind: agents.TranscriptToolCall, Tool: "Read", Text: "/etc/hostname"}
	if short.Abbreviated() != short {
		t.Errorf("a short tool call abbreviated to %+v, want it unchanged", short.Abbreviated())
	}
	for _, c := range []struct {
		event agents.TranscriptEvent
		line  string
	}{
		{short, "- Read(/etc/hostname)"},
		{agents.TranscriptEvent{Kind: agents.TranscriptToolResult, Text: "vm"}, "  = vm"},
		{agents.TranscriptEvent{Kind: agents.TranscriptText, Text: "done"}, "done"},
		{agents.TranscriptEvent{Kind: agents.TranscriptMessage, Text: "carry on"}, "> carry on"},
		{agents.TranscriptEvent{Kind: agents.TranscriptTurnEnded}, "[turn ended]"},
		{agents.TranscriptEvent{Kind: agents.TranscriptError, Text: "denied"}, "[error] denied"},
		{agents.TranscriptEvent{Kind: agents.TranscriptPartial}, "[part of a line, cut]"},
		{agents.TranscriptEvent{Kind: agents.TranscriptUnrecognized, Text: "brand_new_event"}, "[unrecognized brand_new_event]"},
	} {
		if got := c.event.Line(); got != c.line {
			t.Errorf("%+v renders %q, want %q", c.event, got, c.line)
		}
	}
}
