package agents

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// A worker's output as a person would follow it: the one place this
// repository decodes the stream-json a Claude Code worker writes, so a
// reader of a worker's output asks ReadTranscript rather than parsing the
// lines itself. Reports are internal/findings' to read and never pass
// through here; this reads the conversation around them.
//
// # The format is the adapter's
//
// Which format a Capture is in is decided where the command line that
// chose it is built, and travels on Capture.Format: the Claude Code
// adapter's Start passes --output-format stream-json and so marks its
// captures OutputStreamJSON, and the scripted adapter's line workers and
// every terminal worker mark theirs OutputText. A format this file does not
// know is read as text, so nothing is hidden by a decoder guessing.
//
// # What is shown and what is left out
//
// A stream-json line is decoded by its type, and an assistant or user
// message by the blocks it carries:
//
//   - The agent's text blocks are shown whole, as TranscriptText.
//   - A tool_use block is TranscriptToolCall, naming the tool and the one
//     argument toolSummary picks; a tool_result block is
//     TranscriptToolResult, or TranscriptError when it says it is one.
//   - Text the worker was given, a user message's text, is
//     TranscriptMessage.
//   - A result line is TranscriptTurnEnded, or TranscriptError when it
//     reports an error.
//   - Every line and block noiseTypes names is left out and counted in
//     Transcript.Omitted: the counters and status lines the CLI writes
//     around a turn, and thinking.
//   - Anything else, a type or a block or a system subtype this file has
//     no row for, is TranscriptUnrecognized naming it, one line, never
//     dropped. A line that is not JSON at all is TranscriptOutput as it
//     stands, which is where the CLI's standard error, interleaved on the
//     same capture, lands.
//
// The noise table was taken from what the Claude Code CLI 2.1.301 wrote in
// this mode when it was driven by hand, plus the thinking_tokens counter
// #270 quoted from a live session; a line or subtype a later CLI adds shows
// as an unrecognized marker until it is given a row.
//
// # Residual gaps
//
//   - The decode is of the lines as they are. A line cut at the front of a
//     bounded capture, or one the worker had not finished writing, is
//     TranscriptPartial and carries none of its text.
//   - Nothing here redacts or bounds. A caller relaying any of it removes
//     credentials first and abbreviates after, Abbreviated being the
//     second step, because a cut made before redaction could leave half a
//     credential in a shape the redactor no longer recognizes.

// OutputFormat is what a worker's captured output is written in.
type OutputFormat string

// The formats.
const (
	// OutputText is lines of text as they were written or drawn: the
	// scripted adapter's line workers, and every terminal worker's screen.
	OutputText OutputFormat = "text"
	// OutputStreamJSON is one JSON object per line, which the Claude Code
	// adapter's worker writes under --output-format stream-json.
	OutputStreamJSON OutputFormat = "stream-json"
)

// TranscriptKind is what one transcript event is.
type TranscriptKind string

// The kinds.
const (
	// TranscriptText is text the agent wrote to the person it works for.
	TranscriptText TranscriptKind = "text"
	// TranscriptToolCall is a tool the agent called: Tool names it and Text
	// summarizes its argument.
	TranscriptToolCall TranscriptKind = "tool_call"
	// TranscriptToolResult is what a tool call answered.
	TranscriptToolResult TranscriptKind = "tool_result"
	// TranscriptMessage is text the worker was given.
	TranscriptMessage TranscriptKind = "message"
	// TranscriptTurnEnded is the end of a turn the stream reported as a
	// success; Text is empty.
	TranscriptTurnEnded TranscriptKind = "turn_ended"
	// TranscriptError is an error the stream reported: a failed tool call
	// or a turn that ended in one.
	TranscriptError TranscriptKind = "error"
	// TranscriptOutput is a line of text output: every line of an
	// OutputText capture, and a line of a stream-json capture that is not
	// JSON.
	TranscriptOutput TranscriptKind = "output"
	// TranscriptPartial is a line the capture holds only part of: cut at
	// the front by its bound, or not yet finished.
	TranscriptPartial TranscriptKind = "partial"
	// TranscriptUnrecognized is a stream-json line or block this file has
	// no row for; Text names its type.
	TranscriptUnrecognized TranscriptKind = "unrecognized"
)

// TranscriptEvent is one thing a person following the worker would see.
type TranscriptEvent struct {
	// Kind is what it is.
	Kind TranscriptKind `json:"kind"`
	// Tool names the tool, for a tool call.
	Tool string `json:"tool,omitempty"`
	// Text is the event's text, whole as ReadTranscript reads it and cut
	// to a bound by Abbreviated.
	Text string `json:"text,omitempty"`
}

// Transcript is a capture read as events.
type Transcript struct {
	// Events are the events, in the order the capture holds them.
	Events []TranscriptEvent
	// Omitted counts the lines and blocks left out by noiseTypes' rows.
	Omitted int
}

// noiseTypes are the stream-json lines and blocks left out of a transcript
// and counted: a top-level type, "system/<subtype>" for a system line, and
// "block/<type>" for a content block. It is the one list of them.
var noiseTypes = map[string]bool{
	"active_goal":              true,
	"autocompact_state":        true,
	"rate_limit_event":         true,
	"system/init":              true,
	"system/thinking_tokens":   true,
	"system/task_summary":      true,
	"system/post_turn_summary": true,
	"block/thinking":           true,
	"block/redacted_thinking":  true,
}

// ReadTranscript reads text, a capture in format, as events, on the terms
// this file's opening comment states. cut says the capture's front was let
// go by its bound, so its first line is partial.
func ReadTranscript(text string, format OutputFormat, cut bool) Transcript {
	var out Transcript
	if text == "" {
		return out
	}
	finished := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		first, last := i == 0, i == len(lines)-1
		if format != OutputStreamJSON {
			if first && cut {
				out.Events = append(out.Events, TranscriptEvent{Kind: TranscriptPartial})
				continue
			}
			out.Events = append(out.Events, TranscriptEvent{Kind: TranscriptOutput, Text: line})
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			if (first && cut) || (last && !finished) {
				out.Events = append(out.Events, TranscriptEvent{Kind: TranscriptPartial})
			} else {
				out.Events = append(out.Events, TranscriptEvent{Kind: TranscriptOutput, Text: line})
			}
			continue
		}
		out.streamLine(object)
	}
	return out
}

// streamLine adds what one decoded stream-json line shows.
func (t *Transcript) streamLine(object map[string]json.RawMessage) {
	kind := stringField(object, "type")
	name := kind
	if kind == "system" {
		name = "system/" + stringField(object, "subtype")
	}
	if noiseTypes[name] {
		t.Omitted++
		return
	}
	switch kind {
	case "assistant", "user":
		t.message(kind, object["message"])
	case "result":
		if boolField(object, "is_error") || stringField(object, "subtype") != "success" {
			reason := stringField(object, "result")
			if reason == "" {
				reason = stringField(object, "subtype")
			}
			t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptError, Text: reason})
			return
		}
		t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptTurnEnded})
	default:
		if name == "" {
			name = "a line with no type"
		}
		t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptUnrecognized, Text: name})
	}
}

// message adds what an assistant or user message's content shows. A
// content that is a string is one text block.
func (t *Transcript) message(role string, raw json.RawMessage) {
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &message); err != nil || len(message.Content) == 0 {
		t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptUnrecognized, Text: role + " with no content"})
		return
	}
	var plain string
	if err := json.Unmarshal(message.Content, &plain); err == nil {
		t.text(role, plain)
		return
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptUnrecognized, Text: role + " content"})
		return
	}
	for _, block := range blocks {
		kind := stringField(block, "type")
		if noiseTypes["block/"+kind] {
			t.Omitted++
			continue
		}
		switch kind {
		case "text":
			t.text(role, stringField(block, "text"))
		case "tool_use":
			name := stringField(block, "name")
			t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptToolCall, Tool: name, Text: toolSummary(block["input"])})
		case "tool_result":
			result := TranscriptEvent{Kind: TranscriptToolResult, Text: resultText(block["content"])}
			if boolField(block, "is_error") {
				result.Kind = TranscriptError
			}
			t.Events = append(t.Events, result)
		default:
			t.Events = append(t.Events, TranscriptEvent{Kind: TranscriptUnrecognized, Text: role + " block " + kind})
		}
	}
}

// text adds a text block: the agent's when role is assistant, and text the
// worker was given otherwise.
func (t *Transcript) text(role, text string) {
	kind := TranscriptMessage
	if role == "assistant" {
		kind = TranscriptText
	}
	t.Events = append(t.Events, TranscriptEvent{Kind: kind, Text: text})
}

// toolArguments are the arguments a tool call is summarized by, in the
// order they are looked for: the first one the input carries as a
// non-empty string is the summary.
var toolArguments = []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt", "skill"}

// toolSummary is a tool call's input as one argument: the first of
// toolArguments it carries, and otherwise its keys, sorted, so an input
// with none of them still says what it was given.
func toolSummary(raw json.RawMessage) string {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}
	for _, key := range toolArguments {
		if value := stringField(input, key); value != "" {
			return value
		}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// resultText is a tool result's content as text: the string itself, or the
// text blocks of a content list joined by newlines, with any other block
// named in brackets.
func resultText(raw json.RawMessage) string {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if kind := stringField(block, "type"); kind != "text" {
			parts = append(parts, "["+kind+"]")
			continue
		}
		parts = append(parts, stringField(block, "text"))
	}
	return strings.Join(parts, "\n")
}

// stringField is object's key as a string, or empty when it is absent or
// not one.
func stringField(object map[string]json.RawMessage, key string) string {
	var s string
	if err := json.Unmarshal(object[key], &s); err != nil {
		return ""
	}
	return s
}

// boolField is object's key as a boolean, false when absent or not one.
func boolField(object map[string]json.RawMessage, key string) bool {
	var b bool
	_ = json.Unmarshal(object[key], &b)
	return b
}

// The bounds Abbreviated cuts each kind's text to, in bytes.
const (
	abbreviateText    = 2048
	abbreviateResult  = 240
	abbreviateSummary = 160
	abbreviateLine    = 480
)

// Abbreviated is e with its text cut to its kind's bound: the agent's text
// and an error to a few lines' worth, a tool call's summary and a tool
// result to their first line, and everything else to one long line. A cut
// says how much it let go. It is for after redaction, never before, for
// the reason this file's residual gaps give.
func (e TranscriptEvent) Abbreviated() TranscriptEvent {
	switch e.Kind {
	case TranscriptText, TranscriptError:
		e.Text = cutText(e.Text, abbreviateText)
	case TranscriptToolCall:
		e.Tool = cutText(headLine(e.Tool), abbreviateSummary)
		e.Text = cutText(headLine(e.Text), abbreviateSummary)
	case TranscriptToolResult:
		e.Text = cutText(headLine(e.Text), abbreviateResult)
	default:
		e.Tool = cutText(e.Tool, abbreviateSummary)
		e.Text = cutText(e.Text, abbreviateLine)
	}
	return e
}

// Line is e as a person reads it in a transcript: the agent's text as it
// stands, a tool call as the tool and its argument, a tool result indented
// under it, and every other kind marked in brackets.
func (e TranscriptEvent) Line() string {
	switch e.Kind {
	case TranscriptText, TranscriptOutput:
		return e.Text
	case TranscriptToolCall:
		return fmt.Sprintf("- %s(%s)", e.Tool, e.Text)
	case TranscriptToolResult:
		return "  = " + e.Text
	case TranscriptMessage:
		return "> " + e.Text
	case TranscriptTurnEnded:
		return "[turn ended]"
	case TranscriptError:
		return "[error] " + e.Text
	case TranscriptPartial:
		return "[part of a line, cut]"
	default:
		return fmt.Sprintf("[unrecognized %s]", e.Text)
	}
}

// headLine is text up to its first newline, followed by how many lines
// more there were when there were any.
func headLine(text string) string {
	first, rest, more := strings.Cut(strings.TrimSpace(text), "\n")
	if !more {
		return first
	}
	return fmt.Sprintf("%s (+%d lines)", first, strings.Count(rest, "\n")+1)
}

// cutText keeps at most limit bytes of text, ending at a character
// boundary, and says how many bytes it let go.
func cutText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return fmt.Sprintf("%s... (%d bytes more)", text[:end], len(text)-end)
}
