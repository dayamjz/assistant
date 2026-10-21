package standin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Call is one invocation as the stand-in agent saw it, written by the stand-in
// process itself. It is evidence off the wire rather than the adapter's
// account of what it did, which is what makes it worth asserting against.
//
// It carries no environment. An invocation's environment may hold an agent's
// credentials, and a Call is written to a file; internal/agents keeps them out
// of a Record by having no field for them, and this keeps them out of a Call
// the same way.
type Call struct {
	// Seq is the invocation's arrival order, from one. Invocations that
	// overlap are ordered by when each stand-in process claimed its slot,
	// which is the only order a separate process can observe.
	Seq int `json:"seq"`
	// Args is the command line the adapter built, without the program name. It
	// begins with the argument this package supplies through the factory's own
	// argument list, exactly where a configured entry's own flags would be,
	// and continues with the flags the adapter manages.
	Args []string `json:"args"`
	// Prompt is what reached the agent: standard input for Claude Code and
	// Cursor, the contents of --prompt-file for Grok Build, or the one word
	// after "--" for Codex exec.
	Prompt string `json:"prompt"`
	// Dir is the working directory the stand-in process resolved. A platform
	// that resolves symbolic links on the way makes this differ textually from
	// the Invocation's Dir, so compare it as a resolved path rather than as
	// the string a caller passed.
	Dir string `json:"dir"`
	// Step is the index of the script step that answered this invocation, or
	// -1 when no step matched and the stand-in exited with ExitUnscripted. It
	// is -1 for a worker, which no step answers.
	Step int `json:"step"`
	// Worker reports that this process was started as a worker rather than
	// as a one-shot invocation: the adapter put neither --print nor
	// --prompt-file on its command line, or put --print there with streaming
	// input, which is the whole of how the stand-in tells the two apart.
	Worker bool `json:"worker,omitempty"`
	// Stream reports that the worker read streaming input: --print and
	// --input-format stream-json were on its command line, so each line it
	// read was a JSON message rather than text. It is the process's own
	// account of how it decided, like Worker.
	Stream bool `json:"stream,omitempty"`
	// Lines is every line a worker read on its standard input, in order, the
	// instructions first. A stream-mode worker records the text of each user
	// message it read instead, decoded from the line, and records nothing for
	// a line it passed over, so a test reads what the worker was told rather
	// than how the adapter framed it. A worker rewrites its record as each
	// line arrives, so what a reader sees is complete up to some line and may
	// be behind the worker; Agent.Calls states the terms. It is empty for a
	// one-shot invocation, whose input is Prompt.
	Lines []string `json:"lines,omitempty"`
	// Interactive reports that the worker drew the interactive interface
	// the package documentation models: --ax-screen-reader was among the
	// flags before "--" and --print was not. It is the process's own
	// account of how it decided, like Worker. An interactive worker records
	// in Lines every message it answered, the one after "--" first, as the
	// text it submitted, so a leading space a typist added is part of it.
	Interactive bool `json:"interactive,omitempty"`
	// ConfigDir is the CLAUDE_CONFIG_DIR an interactive worker read its
	// first-run state from, empty when none was set. It is a path and not a
	// credential, which is why this one variable is recorded when the rest
	// of the environment is not.
	ConfigDir string `json:"config_dir,omitempty"`
	// Credentials is the name of each credential variable an interactive
	// worker found set and not empty in its environment, ANTHROPIC_API_KEY
	// before CLAUDE_CODE_OAUTH_TOKEN, so a test reads which credential an
	// adapter handed it. It holds names and never a value, which is why it
	// is recorded when the environment is not.
	Credentials []string `json:"credentials,omitempty"`
	// Queued is every message an interactive worker had submitted to it
	// while it was drawing a turn, in order; each is also in Lines, where it
	// was answered after that turn ended.
	Queued []string `json:"queued,omitempty"`
	// Shell is every input an interactive worker took as a shell command,
	// because it began with "!" typed into an empty input, without the "!".
	// The stand-in records it and runs nothing.
	Shell []string `json:"shell,omitempty"`
	// Slash is every input an interactive worker took as a slash command,
	// because it began with "/" typed into an empty input, with the "/".
	Slash []string `json:"slash,omitempty"`
}

// Answered reports whether a step of the script answered this invocation. It
// is false for a worker, which steps do not answer.
func (c Call) Answered() bool { return c.Step >= 0 }

// Session is the session the invocation asked the agent to continue, empty
// when it asked for none. It is the value of --resume, or for a Codex exec
// command line the thread after its resume subcommand, so it is empty for
// every invocation agents.Runner.Run makes, and it is what a test asserts P4
// on from the wire.
func (c Call) Session() string {
	if codexExecArgs(c.Args) {
		return codexResume(c.Args)
	}
	return c.flag("--resume")
}

// Model is the model the invocation asked for, empty when it named none.
func (c Call) Model() string { return c.flag("--model") }

// flag is the value following name on the command line, empty when the flag is
// absent or is the last word on it.
func (c Call) flag(name string) string {
	for i, a := range c.Args {
		if a == name && i+1 < len(c.Args) {
			return c.Args[i+1]
		}
	}
	return ""
}

// String renders the call for a diagnostic: which invocation it was, which
// step answered it, and enough of the prompt to tell it from another.
func (c Call) String() string {
	if c.Worker {
		return fmt.Sprintf("call %d (worker, %d lines read): %s", c.Seq, len(c.Lines), shorten(strings.Join(c.Lines, " / ")))
	}
	step := "unscripted"
	if c.Answered() {
		step = fmt.Sprintf("step %d", c.Step)
	}
	session := c.Session()
	if session == "" {
		session = "none"
	}
	return fmt.Sprintf("call %d (%s, session %s): %s", c.Seq, step, session, shorten(c.Prompt))
}

// shorten renders text for a diagnostic, on one line and bounded.
func shorten(text string) string {
	const limit = 60
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// The stand-in's control directory holds the script the parent wrote, a place
// claimed for every invocation it answered with the record of that invocation
// beside it, and one file per use of a bounded step. Every claim is a file
// created exclusively, which is what orders concurrent invocations and bounds
// a step's uses without a lock.
const (
	scriptName   = "script.json"
	callsName    = "calls"
	usesName     = "uses"
	claimSuffix  = ".claim"
	recordSuffix = ".json"
)

func scriptPath(control string) string { return filepath.Join(control, scriptName) }
func callsPath(control string) string  { return filepath.Join(control, callsName) }
func usesPath(control string) string   { return filepath.Join(control, usesName) }

// claimCall takes the next free place in the calls directory and records c
// there, returning the number it claimed.
//
// The place and the record are two files on purpose. Creating the claim
// exclusively is what stops two stand-in processes taking one number, and the
// record is written whole and then renamed into place, so a reader either sees
// a complete record or sees nothing at all. Writing into the claimed file
// instead would leave a window in which the file exists and is empty, and a
// reader looking during it would have to choose between reporting a record
// that is merely not written yet and skipping one that never will be.
func claimCall(control string, c Call) (int, error) {
	dir := callsPath(control)
	claimed, err := claimedPlaces(dir)
	if err != nil {
		return 0, err
	}
	for seq := claimed + 1; ; seq++ {
		file, err := os.OpenFile(placePath(dir, seq, claimSuffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("claiming call %d: %w", seq, err)
		}
		if err := file.Close(); err != nil {
			return 0, fmt.Errorf("claiming call %d: %w", seq, err)
		}
		c.Seq = seq
		if err := record(dir, c); err != nil {
			return 0, err
		}
		return seq, nil
	}
}

// claimedPlaces is how many places have been claimed already, which is where
// the search for a free one starts. It is a starting point rather than an
// answer: two stand-in processes reading it at once get the same number, and
// the exclusive create is what settles which of them takes it.
func claimedPlaces(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading the calls directory: %w", err)
	}
	claimed := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), claimSuffix) {
			claimed++
		}
	}
	return claimed, nil
}

// record writes the call beside the place it claimed through writeWhole, so
// nothing partial is ever visible under the name a reader looks for, and a
// file a failed write leaves behind ends in writeWhole's ".partial-" suffix
// rather than recordSuffix, so it is never read as a call.
func record(dir string, c Call) error {
	encoded, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding call %d: %w", c.Seq, err)
	}
	if err := writeWhole(placePath(dir, c.Seq, recordSuffix), encoded); err != nil {
		return fmt.Errorf("recording call %d: %w", c.Seq, err)
	}
	return nil
}

// placePath is where one call's claim or record lives. The number is
// zero-padded so the directory sorts into arrival order.
func placePath(dir string, seq int, suffix string) string {
	return filepath.Join(dir, fmt.Sprintf("%06d%s", seq, suffix))
}

// readCalls returns every call the stand-in has recorded, in arrival order.
//
// A record is renamed into place whole, so what is here is complete and one
// that cannot be decoded is corruption rather than a record still being
// written; it is reported rather than skipped. What is not here is an
// invocation whose stand-in has not recorded it yet, which it does before it
// replies, or one ended between claiming its place and recording it. The
// second leaves the place claimed, so every later call keeps the number it
// took, and this reports one call fewer than there were.
func readCalls(control string) ([]Call, error) {
	dir := callsPath(control)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the calls directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), recordSuffix) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	calls := make([]Call, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("reading recorded call %s: %w", name, err)
		}
		var c Call
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("recorded call %s cannot be read: %w", name, err)
		}
		calls = append(calls, c)
	}
	return calls, nil
}

// claimUse takes one of a bounded step's uses, reporting whether it got one.
// Creating the file exclusively is the claim, so a step with two uses answers
// two invocations however many stand-in processes ask for it at once.
func claimUse(control string, step, use int) (bool, error) {
	name := filepath.Join(usesPath(control), fmt.Sprintf("%03d-%03d", step, use))
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case os.IsExist(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("claiming use %d of step %d: %w", use, step, err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("claiming use %d of step %d: %w", use, step, err)
	}
	return true, nil
}
