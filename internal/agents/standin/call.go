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
	// Prompt is what reached the agent's standard input.
	Prompt string `json:"prompt"`
	// Dir is the working directory the stand-in process resolved. A platform
	// that resolves symbolic links on the way makes this differ textually from
	// the Invocation's Dir, so compare it as a resolved path rather than as
	// the string a caller passed.
	Dir string `json:"dir"`
	// Step is the index of the script step that answered this invocation, or
	// -1 when no step matched and the stand-in exited with ExitUnscripted.
	Step int `json:"step"`
}

// Answered reports whether a step of the script answered this invocation.
func (c Call) Answered() bool { return c.Step >= 0 }

// Session is the session the invocation asked the agent to continue, empty
// when it asked for none. It is the value of --resume, so it is empty for
// every invocation agents.Runner.Run makes, and it is what a test asserts P4
// on from the wire.
func (c Call) Session() string { return c.flag("--resume") }

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

// record writes the call beside the place it claimed, whole and then renamed,
// so nothing partial is ever visible under the name a reader looks for.
func record(dir string, c Call) error {
	encoded, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding call %d: %w", c.Seq, err)
	}
	file, err := os.CreateTemp(dir, "recording-*")
	if err != nil {
		return fmt.Errorf("recording call %d: %w", c.Seq, err)
	}
	if _, err := file.Write(encoded); err != nil {
		// The close is what would report a failure to flush, and this path
		// already has a failure to report; the file it leaves behind is named
		// as a recording in progress and is never read as a call.
		_ = file.Close()
		return fmt.Errorf("recording call %d: %w", c.Seq, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("recording call %d: %w", c.Seq, err)
	}
	if err := os.Rename(file.Name(), placePath(dir, c.Seq, recordSuffix)); err != nil {
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
