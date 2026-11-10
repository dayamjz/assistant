package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Start launches Claude Code as a long-lived worker in spec.Dir and delivers
// spec.Instructions as its first message. Having this method is what makes the
// Claude Code adapter a WorkerRunner, and Capabilities declares the same fact.
//
// The command line is the configured entry's own flags, then the spec's
// grant's flags (claudeGrantFlags over WorkerSpec.Grant: GrantWorker, or
// GrantSubordinate for a spec OpenSubordinate started), then --model and
// --effort when the spec names
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
// Interrupt ends a running turn over control_request subtype interrupt once
// system/init has been seen (CLI #98713), and the turn's end is a result line.
//
// What this buys is a worker the caller drives and ends on the terms Worker
// states, over a wire this package frames rather than one it leaves to a
// terminal. What it does not decide is anything the CLI does with a turn
// beyond the grant: a worker runs under GrantWorker and a subordinate
// coordinator under GrantSubordinate, and nothing here answers a permission
// request, so a tool outside that grant is refused by the CLI rather than
// granted by anything here.
func (r *claudeRunner) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	w := &claudePipeWorker{}
	worker, err := startWorker(ctx, ClaudeName, procSpec{
		bin: r.resolved, args: r.workerArguments(spec), dir: spec.Dir,
		env: WorkerEnvironment(r.settings.base, spec.Env), grace: r.settings.grace, maxOut: r.settings.maxOut,
	}, spec.Instructions, claudeUserMessage, OutputStreamJSON, w.observe)
	if err != nil {
		return nil, err
	}
	w.procWorker = worker
	// Wait for the instructions turn without arming settle channels.
	// waitCapture returns on a result, or after 5s with no system/init
	// (a silent worker). That soft return is a residual gap: a late
	// instructions result after the first Send arms can still markIdle
	// that arm, because observe has no generation token.
	w.waitCapture(ctx)
	return w, nil
}

// workerArguments builds a worker's command line: the configured entry's own
// flags, then the spec's grant with its route, then --model and --effort
// when the spec names them, then the four Start states.
func (r *claudeRunner) workerArguments(spec WorkerSpec) []string {
	args := append(slices.Clone(r.extra), claudeGrantFlags(spec.Grant(), spec.Route)...)
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

// claudePipeWorker is the Claude Code stream-json Worker with pipe interrupt.
type claudePipeWorker struct {
	*procWorker
	mu             sync.Mutex
	settled, begun chan struct{}
	next           uint64
}

// Send delivers one stream-json user message, waiting for the prior result
// and this turn's system/init so the next Send cannot race a late init.
func (w *claudePipeWorker) Send(ctx context.Context, text string) error {
	w.mu.Lock()
	settled := w.settled
	w.mu.Unlock()
	if err := w.await(ctx, settled); err != nil {
		return err
	}
	w.armTurn()
	mark := len(w.Capture().Text)
	if err := w.procWorker.Send(ctx, text); err != nil {
		w.markIdle()
		return err
	}
	return w.waitTurnProgress(ctx, mark)
}

// Interrupt ends the turn over control_request subtype interrupt.
func (w *claudePipeWorker) Interrupt(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	w.mu.Lock()
	settled, begun := w.settled, w.begun
	w.mu.Unlock()
	if settled == nil {
		return false, nil
	}
	select {
	case <-begun:
	case <-settled:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-w.done:
		return false, ErrWorkerEnded
	}
	select {
	case <-settled:
		return false, nil
	default:
	}
	w.mu.Lock()
	w.next++
	id := strconv.FormatUint(w.next, 10)
	w.mu.Unlock()
	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{
		"type": "control_request", "request_id": id,
		"request": map[string]string{"subtype": "interrupt"},
	}); err != nil {
		return false, err
	}
	if err := w.writeFramed(ctx, line.Bytes(), "", false); err != nil {
		return false, err
	}
	if err := waitAfterClaudeInterruptCancel(ctx, settled, w.done); err != nil {
		return false, err
	}
	return true, nil
}

// waitAfterClaudeInterruptCancel waits for settled after control_request was
// written. Tests may replace it to observe that Interrupt reached this wait
// rather than returning on the cancel alone.
var waitAfterClaudeInterruptCancel = func(ctx context.Context, settled <-chan struct{}, ended <-chan struct{}) error {
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-ended:
		return ErrWorkerEnded
	}
}

func (w *claudePipeWorker) armTurn() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settled, w.begun = make(chan struct{}), make(chan struct{})
}

func (w *claudePipeWorker) waitCapture(ctx context.Context) {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	// Once system/init is seen, wait for a protocol result (or done /
	// hurry / ctx) with no time soft-return. Before init, a 5s bound
	// covers a worker that prints nothing; Capture quiet without init
	// is not a settle, because stderr shares Capture with stdout.
	emptyBound := time.NewTimer(5 * time.Second)
	defer emptyBound.Stop()
	for {
		text := w.Capture().Text
		if claudeCaptureHasResult(text) {
			return
		}
		sawInit := strings.Contains(text, `"subtype":"init"`)
		var boundC <-chan time.Time
		if !sawInit {
			boundC = emptyBound.C
		}
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-w.hurry:
			return
		case <-boundC:
			return
		case <-tick.C:
		}
	}
}

func claudeCaptureHasResult(text string) bool {
	for line := range strings.Lines(text) {
		var msg struct{ Type string }
		if json.Unmarshal([]byte(line), &msg) == nil && msg.Type == "result" {
			return true
		}
	}
	return false
}

// claudeRecordedSettleFrame is a stdout frame a Recorded fixture may emit
// without system/init, and that the checked-in live recording never places
// before init (active_goal and autocompact_state come first there).
func claudeRecordedSettleFrame(msg struct{ Type, Subtype string }) bool {
	switch msg.Type {
	case "assistant", "user", "brand_new_event":
		return true
	case "system":
		return msg.Subtype == "thinking_tokens"
	default:
		return false
	}
}

func (w *claudePipeWorker) await(ctx context.Context, settled chan struct{}) error {
	if settled == nil {
		return nil
	}
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
		return ErrWorkerEnded
	}
}

// waitTurnProgress returns once system/init has been seen, the turn has
// settled, or the worker has ended. Capture growth alone never marks idle:
// stderr shares Capture with stdout and a partial line before init would
// otherwise drop a live turn.
func (w *claudePipeWorker) waitTurnProgress(ctx context.Context, _ int) error {
	for {
		w.mu.Lock()
		settled, begun := w.settled, w.begun
		w.mu.Unlock()
		if settled == nil {
			return nil
		}
		select {
		case <-begun:
			return nil
		case <-settled:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			w.markIdle()
			return nil
		}
	}
}

func (w *claudePipeWorker) markIdle() {
	w.mu.Lock()
	settled, begun := w.settled, w.begun
	w.settled, w.begun = nil, nil
	w.mu.Unlock()
	for _, ch := range []chan struct{}{begun, settled} {
		if ch == nil {
			continue
		}
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (w *claudePipeWorker) observe(line []byte) {
	var msg struct{ Type, Subtype string }
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	switch {
	case msg.Type == "system" && msg.Subtype == "init":
		w.mu.Lock()
		begun := w.begun
		w.mu.Unlock()
		if begun == nil {
			return
		}
		select {
		case <-begun:
		default:
			close(begun)
		}
	case msg.Type == "result":
		w.mu.Lock()
		settled := w.settled
		w.mu.Unlock()
		if settled == nil {
			return
		}
		// A result ends the turn even when system/init was never seen
		// (a Recorded reply). Interrupt still waits on begun first.
		w.markIdle()
	default:
		// Recorded fixtures may omit init; settle only on frame types the
		// live recording never emits before system/init, so active_goal
		// and autocompact_state cannot drop a live turn.
		if !claudeRecordedSettleFrame(msg) {
			return
		}
		w.mu.Lock()
		begun, settled := w.begun, w.settled
		w.mu.Unlock()
		if settled == nil || begun == nil {
			return
		}
		select {
		case <-begun:
		default:
			w.markIdle()
		}
	}
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
