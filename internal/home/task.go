package home

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// TaskIDLength is the length of every task identifier, in characters. Ids are
// short on purpose: tasks/<id>/copy/<repository files> is the first path under
// a home to meet the Windows path limit, and every character here is one taken
// from the repository's own paths.
const TaskIDLength = 12

// taskIDAlphabet is the closed set of characters a task identifier is drawn
// from: the lower-case RFC 4648 base32 alphabet. It has no separator, no dot,
// no upper case and nothing outside ASCII, so an identifier is one path
// element on every platform the module builds for and names the same file on
// a case-insensitive filesystem as on a case-sensitive one.
const taskIDAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// ErrTaskID reports a task identifier the one rule here refuses. Every refusal
// wraps it and says which part of the rule the identifier failed.
var ErrTaskID = errors.New("home: invalid task identifier")

// reservedNames are the file names Windows reserves on every volume, with or
// without an extension. A task's identifier names a directory, so one of these
// would be a directory that cannot be created there. They are refused on every
// platform rather than only on Windows, because a home is a directory a person
// may copy between machines and an identifier is minted once.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidTaskID is the one rule a task identifier answers to, and the only
// validator of one: a program-generated identifier of exactly TaskIDLength
// characters from the lower-case base32 alphabet. Anything else is refused
// with an error wrapping ErrTaskID, never corrected: a separator, a dot, a
// traversal element, an absolute path, upper case, a space, a Windows
// reserved name, or the wrong length. Sanitizing would make two identifiers
// name one directory, which is the collision this rule exists to make
// impossible.
//
// The rule is deliberately stricter than any filesystem: it refuses a reserved
// name on every platform even though the alphabet and length already make one
// unreachable, so that the refusal does not depend on the length rule staying
// as it is.
func ValidTaskID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrTaskID)
	}
	stem, _, _ := strings.Cut(strings.ToLower(id), ".")
	if reservedNames[stem] {
		return fmt.Errorf("%w: %q is a name Windows reserves", ErrTaskID, id)
	}
	if len(id) != TaskIDLength {
		return fmt.Errorf("%w: %q is %d characters, want %d", ErrTaskID, id, len(id), TaskIDLength)
	}
	for i := 0; i < len(id); i++ {
		if !strings.ContainsRune(taskIDAlphabet, rune(id[i])) {
			return fmt.Errorf("%w: %q has %q at position %d, which is outside the alphabet %q",
				ErrTaskID, id, id[i], i, taskIDAlphabet)
		}
	}
	return nil
}

// NewTaskID mints a task identifier in the one format ValidTaskID accepts, from
// the operating system's random source. It is the format's one owner on the
// generating side, so the package that creates tasks does not carry a second
// copy of the alphabet.
func NewTaskID() (string, error) {
	var raw [TaskIDLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("home: generating a task identifier: %w", err)
	}
	id := make([]byte, TaskIDLength)
	for i, b := range raw {
		id[i] = taskIDAlphabet[int(b)%len(taskIDAlphabet)]
	}
	return string(id), nil
}

// Task is where one task's records live: its instructions and the evidence
// copy of its report. Its events are rows in the store, and nothing under it
// is state: PRD section 8 resolves a task's state on read, so no file here
// holds it.
//
// An identifier ValidTaskID refuses is refused here, so a path under tasks/
// is only ever composed from an identifier that is one path element.
func (h *Home) Task(id string) (string, error) {
	if err := ValidTaskID(id); err != nil {
		return "", err
	}
	return filepath.Join(h.root, "tasks", id), nil
}

// TaskCopy is the task's isolated copy, which PRD section 8 makes a full clone
// of the person's checkout with an assistant remote, under the task's own
// directory. It is refused on the same terms as Task.
func (h *Home) TaskCopy(id string) (string, error) {
	task, err := h.Task(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(task, "copy"), nil
}

// TaskInstructions is the file holding the instructions a task's worker is
// launched with, under the task's own directory. It is refused on the same
// terms as Task. What the file holds is internal/tasks', which writes it at
// intake, and internal/service's, which appends the isolation check at
// dispatch; this package says only where it goes.
func (h *Home) TaskInstructions(id string) (string, error) {
	task, err := h.Task(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(task, "instructions.md"), nil
}
