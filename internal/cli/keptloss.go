package cli

import (
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dayamjz/assistant/internal/machine"
)

// The labels a discard shown prints what would be lost after, named so a
// test can tell the labelled lines from the rows. A file's label says why
// it is lost, one per kind internal/tasks' Loss reports.
const (
	keptLostCommitLabel = "Commit:"
	keptLostObjectLabel = "Other object:"
	keptLostDigestLabel = "Digest:"
)

// keptLostFileLabels is the label for each kind of lost path, the closed
// set machine.KeptWorkLostPath.Kind takes.
var keptLostFileLabels = map[string]string{
	"changed":    "Changed file:",
	"new":        "New file:",
	"staged":     "Staged file:",
	"deleted":    "Deleted file:",
	"repository": "Repository file:",
}

// keptLostFileLabel is a lost path's label, and for a kind outside that set
// a label naming the kind as it came, so a kind added without a label is
// still printed rather than dropped.
func keptLostFileLabel(kind string) string {
	if label, ok := keptLostFileLabels[kind]; ok {
		return label
	}
	return "File (" + exactText(kind) + "):"
}

// readOutKeptWorkAnswer is a person's reading of an answer to kept work:
// the answer's row, and for a discard shown, exactly what would be lost
// after it, PRD section 9's "shows exactly what would be lost, with a short
// digest of it". Each line keptLossLines composes is machine text a person
// copies exactly, the commits, objects and files of the loss and its
// digest, after a label; when the loss lists nothing,
// machine.PhraseKeptWorkLosesNothing stands before the digest instead.
func readOutKeptWorkAnswer(w io.Writer, a machine.KeptWorkAnswered) {
	writeln(w, machine.KeptWorkAnsweredPhrase(a))
	if a.Effect != machine.KeptWorkDiscardShown || a.Loss == nil {
		return
	}
	lines := keptLossLines(*a.Loss)
	if len(lines) == 1 {
		writeln(w, machine.PhraseKeptWorkLosesNothing)
	}
	for _, line := range lines {
		writeln(w, line)
	}
}

// keptLossLines is what a discard shown prints of its loss, in order: one
// line per lost commit, then per other object with the names the removal
// destroys of it, then per lost file, and the digest last, each value
// written by exactText, so two distinct paths or names never print the
// same. The digest's line is always present, so a loss naming nothing is
// the digest's line alone.
func keptLossLines(l machine.KeptWorkLoss) []string {
	lines := make([]string, 0, len(l.Lacks)+len(l.Objects)+len(l.Files)+1)
	for _, commit := range l.Lacks {
		lines = append(lines, keptLostCommitLabel+" "+exactText(commit))
	}
	for _, o := range l.Objects {
		line := keptLostObjectLabel + " " + exactText(o.Object) + " (" + exactText(o.Type) + ")"
		if len(o.Names) > 0 {
			names := make([]string, len(o.Names))
			for i, n := range o.Names {
				names[i] = exactText(n)
			}
			line += " " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	for _, f := range l.Files {
		lines = append(lines, keptLostFileLabel(f.Kind)+" "+exactText(f.Path))
	}
	return append(lines, keptLostDigestLabel+" "+exactText(l.Digest))
}

// exactText writes a value a person must tell apart from every other, a
// path, a reference's name, a commit or the digest, so that two distinct
// values never print the same and none acts on the terminal. A value that
// is valid UTF-8 of printable runes (strconv.IsPrint: letters, marks,
// numbers, punctuation, symbols and the ASCII space), holds no backslash
// or double quote, and neither starts nor ends with a space, prints as it
// is. Any other is written quoted, as strconv.Quote writes a Go string
// literal: a backslash, a quote, a control or format character, a space
// other than the ASCII one and a byte that is not UTF-8 are each escaped,
// and strconv.Unquote reads the value back exactly. A plain value never
// starts with a quote and a quoted one always does, so the two forms
// cannot meet.
//
// What it does not cover: two distinct values that a terminal draws alike
// though each is printable, letters of two scripts that look the same or a
// precomposed letter beside the same letter with a combining mark, print
// as their runes and look alike.
func exactText(value string) string {
	if plainText(value) {
		return value
	}
	return strconv.Quote(value)
}

// plainText is whether exactText prints value as it is.
func plainText(value string) bool {
	if value == "" || !utf8.ValidString(value) || value[0] == ' ' || value[len(value)-1] == ' ' {
		return false
	}
	for _, r := range value {
		if r == '\\' || r == '"' || !strconv.IsPrint(r) {
			return false
		}
	}
	return true
}
