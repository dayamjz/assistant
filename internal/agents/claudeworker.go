package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"
)

// Start launches Claude Code as a long-lived worker in spec.Dir and delivers
// spec.Instructions as its first message. Having this method is what makes the
// Claude Code adapter a WorkerRunner, and Capabilities declares the same fact.
//
// The command line is the configured entry's own flags, then GrantWorker's
// flags (claudeGrantFlags), then --model and --effort when the spec names
// them, then the four this mode manages: --print, --input-format stream-json, --output-format
// stream-json, and --verbose. Those four are the flags the CLI's own --help
// documents for realtime streaming input, which it offers only with --print;
// --verbose is there because the CLI answers stream-json output under --print
// without it with an error and no output. No --resume is passed, because a
// worker opens a conversation rather than continuing one. config.ReservedAgentFlags refuses
// an entry naming --input-format, --output-format or --resume, so an entry
// cannot contradict the three that decide how the worker's input and output
// are read and which conversation it is in.
//
// Every send, the instructions first, is one line on standard input: a JSON
// object of type "user" whose message carries the text, framed by
// claudeUserMessage. That is the shape Anthropic's Agent SDK writes to the
// same CLI for a string prompt. What comes back is the CLI's stream-json
// output, one JSON object per line: a worker produces no Result, and Capture
// hands the lines over as the bytes they are, marked OutputStreamJSON, which
// ReadTranscript in transcript.go is the one reader of.
//
// What this buys is a worker the caller drives and ends on the terms Worker
// states, over a wire this package frames rather than one it leaves to a
// terminal. What it does not decide is anything the CLI does with a turn
// beyond the grant: a worker runs under GrantWorker, and nothing here answers
// a permission request, so a tool outside that grant is refused by the CLI
// rather than granted by anything here.
func (r *claudeRunner) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	worker, err := startWorker(ctx, ClaudeName, procSpec{
		bin:    r.resolved,
		args:   r.workerArguments(spec),
		dir:    spec.Dir,
		env:    WorkerEnvironment(r.settings.base, spec.Env),
		grace:  r.settings.grace,
		maxOut: r.settings.maxOut,
	}, spec.Instructions, claudeUserMessage, OutputStreamJSON)
	if err != nil {
		return nil, err
	}
	return worker, nil
}

// workerArguments builds a worker's command line: the configured entry's own
// flags, then the worker's grant with its route, then --model and --effort
// when the spec names them, then the four Start states.
func (r *claudeRunner) workerArguments(spec WorkerSpec) []string {
	args := append(slices.Clone(r.extra), claudeGrantFlags(GrantWorker, spec.Route)...)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return append(args,
		"--print",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose")
}

// claudeStreamMessage is one user message on a worker's standard input. Its
// fields are the four the Agent SDK writes for a string prompt, and the
// session field is left empty because the conversation is the process's own:
// nothing here names one.
type claudeStreamMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
}

// claudeUserMessage frames one send as a stream-json user message, one line
// ending in a newline, whose content is text exactly. A newline inside text is
// escaped by the encoding, so one send stays one line however many lines it
// carries, and no newline is added to the content.
//
// Text that is not valid UTF-8 is refused with an error wrapping
// ErrInvalidInvocation, before anything is written, because the encoding would
// replace the bytes it cannot carry and the worker would be told something
// other than what was sent.
func claudeUserMessage(text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, &invocationFieldError{
			field:  "text",
			reason: "is not valid UTF-8, so a stream-json message cannot carry it unchanged",
		}
	}
	var message claudeStreamMessage
	message.Type = "user"
	message.Message.Role = "user"
	message.Message.Content = text
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		return nil, err
	}
	return line.Bytes(), nil
}
