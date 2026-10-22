package standin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// codexExecArgs reports whether args are the Codex adapter's one-shot
// command line: the exec subcommand with --json among the words before the
// "--" that ends its options. Codex has no --print, so without this the
// command line would read as a line-mode worker's.
func codexExecArgs(args []string) bool {
	options := codexOptions(args)
	return slices.Contains(options, "exec") && slices.Contains(options, "--json")
}

// codexOptions is every word before the first "--", which is where the
// Codex adapter puts every flag and subcommand; the prompt follows it.
func codexOptions(args []string) []string {
	if at := slices.Index(args, "--"); at >= 0 {
		return args[:at]
	}
	return args
}

// codexResume is the thread a Codex exec command line resumes: the word
// after the resume subcommand, before the "--" that ends the options.
func codexResume(args []string) string {
	options := codexOptions(args)
	if at := slices.Index(options, "resume"); at >= 0 && at+1 < len(options) {
		return options[at+1]
	}
	return ""
}

// serveCodexExec answers one `codex exec --json` invocation the way serve
// answers the others, with two differences the CLI's command line and
// output make. The prompt is the one word after "--", which is where the
// adapter puts it, since standard input is closed empty. And an envelope is
// written as Codex JSONL events (codexWire), so a
// scripted result reaches the adapter's JSONL reader rather than the
// single-object path it keeps for its own helper. Stdout, Stderr and Exit
// are written as scripted, which is how a test plays back recorded events
// such as testdata/codex-exec-unauthenticated-0.163.0.jsonl.
func serveCodexExec(control string, args []string) int {
	script, err := readScript(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoScript
	}
	at := slices.Index(args, "--")
	if at < 0 || at != len(args)-2 {
		fmt.Fprintln(os.Stderr, "standin: a Codex exec command line carries its prompt as the one word after --")
		return exitNoStdin
	}
	dir, _ := os.Getwd()
	call := Call{Args: args, Prompt: args[at+1], Dir: dir, Step: -1}
	step, err := choose(control, script, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoUse
	}
	call.Step = step
	ignoreTerminate(script, step)
	seq, err := claimCall(control, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	call.Seq = seq
	if !call.Answered() {
		fmt.Fprintf(os.Stderr, "standin: no step of the script answers this invocation: %s\n", call)
		return ExitUnscripted
	}
	return emit(script.Steps[step].Reply, call, Envelope.codexWire)
}

// codexWire encodes the envelope as `codex exec --json` events:
// thread.started carrying the thread and turn.started, then for a failure
// an error event and a turn.failed carrying the message, and for a result
// an item.completed agent_message and a turn.completed with its usage.
// Every half is what Codex CLI 0.163.0 printed: the failing ones are
// recorded in internal/agents/testdata/codex-exec-*-0.163.0.jsonl and the
// result half in codex-exec-0.163.0.jsonl, whose turn.completed reports the
// four token counts, cache creation among them as cache_write_input_tokens.
// NoSession prints no thread.started. What these events do not carry is not
// written: no model and no turn count, so those are what the adapter reports
// as unnamed and unreported.
func (e Envelope) codexWire(c Call) ([]byte, error) {
	thread := ""
	switch {
	case e.Session != nil:
		thread = *e.Session
	case c.Session() != "":
		thread = c.Session()
	default:
		thread = fmt.Sprintf("standin-thread-%d", c.Seq)
	}
	var events []any
	if thread != "" {
		events = append(events, map[string]any{"type": "thread.started", "thread_id": thread})
	}
	events = append(events, map[string]any{"type": "turn.started"})
	if e.IsError {
		events = append(events,
			map[string]any{"type": "error", "message": e.Result},
			map[string]any{"type": "turn.failed", "error": map[string]any{"message": e.Result}})
	} else {
		events = append(events,
			map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_0", "type": "agent_message", "text": e.Result}},
			map[string]any{"type": "turn.completed", "usage": map[string]any{
				"input_tokens":             e.Usage.InputTokens,
				"cached_input_tokens":      e.Usage.CacheReadTokens,
				"cache_write_input_tokens": e.Usage.CacheCreationTokens,
				"output_tokens":            e.Usage.OutputTokens,
			}})
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}
