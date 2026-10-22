package standin

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// cursorPrintArgs reports whether args are the Cursor adapter's one-shot
// command line: --print, and one of the two grant flags only that adapter
// writes, --mode or --force, among the words before the "--" that ends its
// options. The Claude Code adapter writes --print too and neither grant flag,
// so without this a Cursor one-shot would be answered as a Claude Code one.
func cursorPrintArgs(args []string) bool {
	options := codexOptions(args)
	return slices.Contains(options, printFlag) &&
		(slices.Contains(options, "--mode") || slices.Contains(options, "--force"))
}

// serveCursorPrint answers one `cursor-agent --print --output-format json`
// invocation the way serve answers the others, with two differences the
// CLI's command line and output make. The prompt is the one word after "--",
// which is where the adapter puts it. And an envelope is written in the
// shape cursorWire states rather than the Claude Code one. Stdout, Stderr and
// Exit are written as scripted, which is how a test plays back a recording
// such as testdata/cursor-print-2026.10.19.json.
func serveCursorPrint(control string, args []string) int {
	script, err := readScript(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoScript
	}
	at := slices.Index(args, "--")
	if at < 0 || at != len(args)-2 {
		fmt.Fprintln(os.Stderr, "standin: a Cursor one-shot command line carries its prompt as the one word after --")
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
	return emit(script.Steps[step].Reply, call, Envelope.cursorWire)
}

// cursorWire encodes the envelope as the Cursor CLI's one result object.
// The success half is what cursor-agent 2026.10.19-6bbf2c4 printed, recorded
// in internal/agents/testdata/cursor-print-2026.10.19.json: no model and no
// turn count, and the four token counts under camelCase keys, cache creation
// among them as cacheWriteTokens. The failure half was not recorded; it is
// the success shape with is_error set and the Claude Code subtype for an
// error, which is what the adapter's reader decodes. NoSession reports an
// empty session_id.
func (e Envelope) cursorWire(c Call) ([]byte, error) {
	subtype := e.Subtype
	if subtype == "" {
		subtype = "success"
		if e.IsError {
			subtype = "error_during_execution"
		}
	}
	return json.Marshal(map[string]any{
		"type":       "result",
		"subtype":    subtype,
		"is_error":   e.IsError,
		"result":     e.Result,
		"session_id": e.session(c),
		"usage": map[string]any{
			"inputTokens":      e.Usage.InputTokens,
			"outputTokens":     e.Usage.OutputTokens,
			"cacheReadTokens":  e.Usage.CacheReadTokens,
			"cacheWriteTokens": e.Usage.CacheCreationTokens,
		},
	})
}
