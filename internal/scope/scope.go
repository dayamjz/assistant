package scope

import (
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
)

// Change is what the lens looks at: the recorded intent, whether that intent
// was supplied as acceptance criteria, and the paths the change touched.
type Change struct {
	// Intent is what the change set out to do, in words, as the intent stage
	// recorded it. The lens refuses an empty one.
	Intent string
	// Supplied says whether the intent was supplied by a person as the
	// acceptance criteria this change answers to. An intent that was not is a
	// low-confidence hint the change is not held to, whatever it came from.
	// It changes how the lens is framed to the reviewer and how an
	// observation describes itself; it does not change what fires.
	Supplied bool
	// Touched is the repository-relative paths the change touched, as the run
	// knows them and not as the reviewer reports them. That is what makes the
	// lens able to fail: a reviewer cannot drop a path out of the question by
	// not mentioning it. Order is preserved and repeats are collapsed. The
	// lens refuses a set holding no path once trimmed, on the same terms as
	// an empty intent.
	Touched []string
}

// Trace is the reviewer's claim that one changed path follows from the stated
// intent. It is a claim in the reviewer's own words, recorded and attributable
// but not verified here.
type Trace struct {
	// Path is the repository-relative path being accounted for. It is matched
	// against Change.Touched exactly, after trimming surrounding space.
	Path string
	// Reason is the part of the intent the path follows from. A trace with an
	// empty reason accounts for nothing and silences nothing, because a trace
	// is the reason: a bare path is the assertion that a file was changed,
	// which was already known.
	Reason string
}

// Guidance returns the scope section the review stage puts in front of its
// reviewer. It names every touched path, so the reviewer is asked about each
// one rather than about the change in general, and it frames the intent by its
// source. It returns ErrNoIntent when there is no recorded intent to trace to,
// and ErrNoTouched when there is no path to ask about.
//
// The text says what the lens will and will not do with the answer, including
// that scope alone is a note, so a reviewer is not invited to escalate a
// change merely for being unexplained. It also asks for each path back exactly
// as listed, because that is the comparison Observe makes.
func Guidance(c Change) (string, error) {
	intent := strings.TrimSpace(c.Intent)
	if intent == "" {
		return "", ErrNoIntent
	}
	touched := dedupe(c.Touched)
	if len(touched) == 0 {
		return "", ErrNoTouched
	}

	var b strings.Builder
	b.WriteString("Scope: every changed line should trace to the stated intent.\n\n")
	if c.Supplied {
		b.WriteString("The intent below was supplied by a person, so treat it as the " +
			"acceptance criteria this change is answerable to.\n\n")
	} else {
		b.WriteString("The intent below was not supplied as acceptance criteria, so treat it " +
			"as a low-confidence hint the change is not held to: departing from it is " +
			"not by itself a defect.\n\n")
	}
	b.WriteString("Intent:\n")
	b.WriteString(intent)
	b.WriteString("\n\nThe change touches these paths. For each one, say what part of the " +
		"intent it follows from, in your own words, and repeat the path itself " +
		"exactly as it is listed below, character for character: a trace is matched " +
		"to a touched path by exact string equality, so a path you retype, reformat, " +
		"or re-case accounts for nothing and leaves its note standing. A path you " +
		"cannot account for is left untraced rather than explained away.\n")
	for _, path := range touched {
		b.WriteString("  - ")
		b.WriteString(path)
		b.WriteString("\n")
	}
	b.WriteString("\nAn untraced path becomes a note: it informs the person and blocks " +
		"nothing. Unrequested refactoring, drive-by formatting, and speculative " +
		"abstraction belong there. Do not raise scope itself as fix or ask. If a " +
		"change nobody asked for is also wrong, report the wrongness as its own " +
		"finding, on its own merits.\n")
	return b.String(), nil
}

// Observe returns the lens's findings: one note per touched path the traces do
// not account for, in the order Change.Touched lists them. It returns
// ErrNoIntent when there is no recorded intent to trace to, and ErrNoTouched
// when there is no path to account for, so silence here always means a change
// whose paths were asked about and answered.
//
// Every finding it returns carries findings.ActionNote, so none is fix-eligible
// and none holds the run. That is by construction here rather than by a cap
// applied afterwards, which is why no path through this function can produce a
// fix or an ask.
//
// The findings carry no identifier. They are the lens's part of the review
// stage's report, and identifiers are assigned once over the whole set by
// findings.NormalizeFindings, which owns that fact.
func Observe(c Change, traces []Trace) ([]findings.Finding, error) {
	if strings.TrimSpace(c.Intent) == "" {
		return nil, ErrNoIntent
	}
	touched := dedupe(c.Touched)
	if len(touched) == 0 {
		return nil, ErrNoTouched
	}

	traced := make(map[string]struct{}, len(traces))
	for _, t := range traces {
		path := strings.TrimSpace(t.Path)
		if path == "" || strings.TrimSpace(t.Reason) == "" {
			continue
		}
		traced[path] = struct{}{}
	}

	var out []findings.Finding
	for _, path := range touched {
		if _, ok := traced[path]; ok {
			continue
		}
		out = append(out, findings.Finding{
			Severity:    findings.SeverityInfo,
			Action:      findings.ActionNote,
			Location:    findings.Location{Path: path},
			Description: describe(path, c.Supplied),
		})
	}
	return out, nil
}

// describe says what an untraced path means, and says it differently for an
// intent supplied as acceptance criteria than for one that was not. Against
// acceptance criteria the observation is that nobody asked for the change;
// against a hint it is that the hint does not cover it, which is a weaker
// claim and is written as one.
func describe(path string, supplied bool) string {
	if supplied {
		return "The change touches " + path + " and the review traced none of it to the " +
			"supplied intent, so nothing on record says this part was asked for. " +
			"Unrequested refactoring, drive-by formatting, and speculative abstraction " +
			"land here. This is informational: it blocks nothing."
	}
	return "The change touches " + path + " and the review traced none of it to the " +
		"recorded intent, which was not supplied as acceptance criteria. The change is " +
		"not held to it and departing from it is not by itself a defect, so this is a " +
		"weak signal - the hint may simply not cover the work - but the part it does " +
		"not cover is worth seeing. This is informational: it blocks nothing."
}

// dedupe trims each path and returns the non-empty ones in their original
// order with repeats collapsed, so a path listed twice is one question to the
// reviewer and at most one note.
func dedupe(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
