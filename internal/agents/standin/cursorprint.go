package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
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
// invocation the way serve answers the others, with three differences the
// CLI's command line and output make. A command line that does not tell the
// CLI to trust its directory is refused before any step is chosen, on the
// terms refuseUntrusted states. The prompt is the one word after "--",
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
	if !cursorTrusted(args) {
		return refuseUntrusted(control, call)
	}
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

// cursorTrustFlags are the options that let cursor-agent 2026.10.19-6bbf2c4
// run in a directory it was not told to trust: the ones its refusal names,
// --trust, --yolo and -f, and --force, which its help lists as -f's long
// form. --trust and --force were each observed to answer in a fresh
// directory; --yolo and -f are taken from the refusal and the help.
var cursorTrustFlags = []string{"--trust", "--yolo", "-f", "--force"}

// cursorTrusted reports whether a Cursor one-shot command line carries one
// of cursorTrustFlags among the options before its "--". The stand-in keeps
// no record of a directory trusted earlier, so every directory is one the
// CLI was not told to trust; a test's directory is a fresh one, as a run's
// copy is.
func cursorTrusted(args []string) bool {
	options := codexOptions(args)
	return slices.ContainsFunc(cursorTrustFlags, func(flag string) bool {
		return slices.Contains(options, flag)
	})
}

// cursorUntrustedDir stands for the directory in cursorUntrustedText.
const cursorUntrustedDir = "<dir>"

// cursorUntrustedText is what cursor-agent 2026.10.19-6bbf2c4 printed on
// standard error, run as `--mode ask --print --output-format json -- <prompt>`
// in a directory it had not been told to trust, with that directory replaced
// by cursorUntrustedDir. The recording is
// internal/agents/testdata/cursor-print-untrusted-2026.10.19.txt, which
// TestTheModelledTrustRefusalIsTheRecordedOne holds this to.
const cursorUntrustedText = `
⚠ Workspace Trust Required

  Cursor Agent can execute code and access files in this directory.
  Do you trust the contents of this directory?

    <dir>

  To proceed, you can either:
    • Run 'agent' interactively to decide
    • Pass --trust, --yolo, or -f if you trust this directory
`

// cursorUntrusted is the CLI's refusal for this directory.
func cursorUntrusted(dir string) string {
	return strings.Replace(cursorUntrustedText, cursorUntrustedDir, dir, 1)
}

// refuseUntrusted answers a Cursor one-shot that does not tell the CLI to
// trust its directory as the CLI did: nothing on standard output, the
// refusal on standard error, and status 1. It records the call unanswered,
// Step -1, and consumes no step, since the CLI refused before it reached a
// model.
func refuseUntrusted(control string, call Call) int {
	if _, err := claimCall(control, call); err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	if _, err := io.WriteString(os.Stderr, cursorUntrusted(call.Dir)); err != nil {
		return exitNoOutput
	}
	return 1
}
