package cli

import (
	"io"
	"strings"

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
	return "File (" + kind + "):"
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
// escaped. The digest's line is always present, so a loss naming nothing
// is the digest's line alone.
func keptLossLines(l machine.KeptWorkLoss) []string {
	lines := make([]string, 0, len(l.Lacks)+len(l.Objects)+len(l.Files)+1)
	for _, commit := range l.Lacks {
		lines = append(lines, keptLostCommitLabel+" "+printable(commit))
	}
	for _, o := range l.Objects {
		line := keptLostObjectLabel + " " + printable(o.Object) + " (" + printable(o.Type) + ")"
		if len(o.Names) > 0 {
			names := make([]string, len(o.Names))
			for i, n := range o.Names {
				names[i] = printable(n)
			}
			line += " " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	for _, f := range l.Files {
		lines = append(lines, keptLostFileLabel(f.Kind)+" "+printable(f.Path))
	}
	return append(lines, keptLostDigestLabel+" "+printable(l.Digest))
}
