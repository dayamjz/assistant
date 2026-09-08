package outcomes

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/principles"
)

// PRDPath is where the PRD lives, relative to the module root, in slash form.
// It is internal/principles' constant rather than a second copy of the path:
// that package is the other document check in this repository and one of them
// owns where the document is.
const PRDPath = principles.PRDPath

// rowName is how a refusal points a reader at the row the set is stated in. It
// names the row and the section by their anchors and not by the section's
// number, because the anchors are what this package checks and the number is
// not. A refusal that fires correctly and then sends its reader to a section
// that has since been renumbered is worse than a terse one, and precision is
// this check's whole claim.
const rowName = `the PRD's outcome row (<tr id="outcome-set"> in <section id="surfaces">)`

// Agreement is the answer to one question: do the PRD's outcome row and
// internal/machine's outcome set say the same thing.
//
// Its scope is the set and what the row says about the set. It says nothing
// about whether the six are the right six, and nothing about whether an
// outcome means on the wire what the row says it means. Both sides naming
// checks-passed in the group that says a run is finished with is all this
// establishes about checks-passed.
type Agreement struct {
	// Listed is what the PRD declares, in its order.
	Listed []Listed
	// Built is what internal/machine declares, in its order.
	Built []Built

	// Unbuilt is declared by the PRD and not by the build.
	Unbuilt []string
	// Unlisted is declared by the build and not by the PRD.
	Unlisted []string
	// Reordered is set when both sides declare the same values in different
	// orders. It is empty when they do not, including when they disagree on
	// membership, because an order is only comparable between two sets that
	// have the same members.
	Reordered []string
	// Regrouped names every value both sides declare and put in different
	// groups, with what each side says.
	Regrouped []string
	// Actionless names every value the build declares and answers with no
	// next action, or with the answer it keeps for an outcome it does not
	// recognize.
	Actionless []string
}

// OK reports whether the two sides declare the same values, in the same order,
// in the same groups, and every one of them carries a next action.
//
// A value that compared nothing is not agreement. An Agreement with an empty
// Listed or an empty Built reports false, which is what the zero value this
// package hands back beside every refusal is, so a caller that dropped that
// error cannot read the result as two sides that agree. Only Compare produces
// a value with both sides filled in.
func (a Agreement) OK() bool {
	return a.compared() && len(a.Unbuilt) == 0 && len(a.Unlisted) == 0 &&
		len(a.Reordered) == 0 && len(a.Regrouped) == 0 && len(a.Actionless) == 0
}

// compared reports whether both sides were read at all.
func (a Agreement) compared() bool {
	return len(a.Listed) > 0 && len(a.Built) > 0
}

// Report describes everything OK is false for and says what closes each. It is
// empty when OK is true.
func (a Agreement) Report() string {
	var b strings.Builder
	if !a.compared() {
		fmt.Fprintf(&b, "the comparison was over %d declared outcomes and %d built ones, so it compared nothing.\n", len(a.Listed), len(a.Built))
		b.WriteString("  This is the value Check returns beside a refusal. Read the error it came with rather than this.\n")
	}
	for _, name := range a.Unbuilt {
		fmt.Fprintf(&b, "%s: %s declares it and internal/machine does not.\n", name, rowName)
		b.WriteString("  Add it to the constant block and the set in internal/machine/outcome.go, or take its marker out of the PRD.\n")
	}
	for _, name := range a.Unlisted {
		fmt.Fprintf(&b, "%s: internal/machine declares it and %s does not.\n", name, rowName)
		b.WriteString("  The PRD owns the set. Add the outcome to its row on a branch of its own, or take it out of the build.\n")
	}
	for _, line := range a.Reordered {
		fmt.Fprintf(&b, "%s\n", line)
		b.WriteString("  Put internal/machine's set in the order the PRD's row declares them.\n")
	}
	for _, line := range a.Regrouped {
		fmt.Fprintf(&b, "%s\n", line)
		b.WriteString("  One of the two changed which group the outcome is in. Reconcile with the PRD rather than around it.\n")
	}
	for _, name := range a.Actionless {
		fmt.Fprintf(&b, "%s: it is declared and carries no next action, which %s requires of every outcome, terminal or not.\n", name, rowName)
		b.WriteString("  Add its row to nextActions in internal/machine/outcome.go.\n")
	}
	return b.String()
}

// Compare reports what the PRD's declarations and the build's do not account
// for between them.
//
// Membership is compared first and order only after it, because two sets with
// different members have no order to disagree about and reporting both would
// bury the difference that matters under one that follows from it.
func Compare(listed []Listed, built []Built) Agreement {
	a := Agreement{Listed: listed, Built: built}

	listedAt := make(map[string]Listed, len(listed))
	var listedNames []string
	for _, l := range listed {
		listedAt[l.Name] = l
		listedNames = append(listedNames, l.Name)
	}
	builtAt := make(map[string]Built, len(built))
	var builtNames []string
	for _, b := range built {
		builtAt[b.Name] = b
		builtNames = append(builtNames, b.Name)
	}

	for _, name := range listedNames {
		if _, ok := builtAt[name]; !ok {
			a.Unbuilt = append(a.Unbuilt, name)
		}
	}
	for _, name := range builtNames {
		if _, ok := listedAt[name]; !ok {
			a.Unlisted = append(a.Unlisted, name)
		}
	}
	if len(a.Unbuilt) == 0 && len(a.Unlisted) == 0 && !slices.Equal(listedNames, builtNames) {
		a.Reordered = append(a.Reordered, fmt.Sprintf(
			"the two declare the same outcomes in different orders: the PRD's row is [%s] and internal/machine's set is [%s]",
			strings.Join(listedNames, " "), strings.Join(builtNames, " ")))
	}
	for _, name := range listedNames {
		b, both := builtAt[name]
		if !both || b.Terminal == listedAt[name].Terminal {
			continue
		}
		a.Regrouped = append(a.Regrouped, fmt.Sprintf(
			"%s: the PRD's row %s and internal/machine reports Terminal %v",
			name, group(listedAt[name].Terminal), b.Terminal))
	}

	unrecognized := unrecognizedAction(built)
	for _, b := range built {
		if strings.TrimSpace(b.Action) == "" || b.Action == unrecognized {
			a.Actionless = append(a.Actionless, b.Name)
		}
	}
	return a
}

// group renders which of the row's two groups a value is in, in the row's own
// words, so a failure reads as the document does.
func group(terminal bool) string {
	if terminal {
		return "puts it in the group that says a run is finished with"
	}
	return "puts it in the group that says a run is not finished with"
}

// Check reads the PRD under root and compares its declarations against the
// build's. Root is the module root: the PRD is read from PRDPath under it.
func Check(root string) (Agreement, error) {
	html, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(PRDPath)))
	if err != nil {
		return Agreement{}, fmt.Errorf("%w: %w", ErrPRD, err)
	}
	listed, err := FromPRD(html)
	if err != nil {
		return Agreement{}, err
	}
	return Compare(listed, FromBuild()), nil
}
