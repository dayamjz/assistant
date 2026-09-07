package journey

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/principles"
)

// Reach says how far this harness drives the product for one principle. It is
// the difference between a claim about the binary that ships and a claim about
// a package inside it, and the two are not worth the same.
type Reach string

const (
	// ReachBinary is driven through the assistant binary as a process, against
	// the fixture. It is the only reach that says anything about what ships.
	ReachBinary Reach = "binary"
	// ReachPackage is driven through the package that owns the mechanism, in
	// this process, against the fixture. It is what is left where the binary
	// cannot reach a mechanism at all, which today is every mechanism a stage
	// body would have used, because no stage body exists.
	ReachPackage Reach = "package"
	// ReachNone is not driven here. The reason beside it says why, and it is
	// prose nobody checks; what the row buys is that the gap is enumerable
	// rather than invisible.
	ReachNone Reach = "none"
)

// Established is one row of what this harness establishes about one principle.
type Established struct {
	// Principle is the PRD section 4 principle the row is about.
	Principle principles.Principle
	// Reach is how far the product is driven for it.
	Reach Reach
	// Note says what the journey establishes and what it does not, in the
	// terms a reader deciding whether to trust a green run needs. Nothing
	// checks it against the tests; it is written to be read.
	Note string
}

// Coverage is what this harness says about each of the PRD's principles, one
// row per principle.
//
// It is a declaration, not a measurement, and the distinction is the same one
// internal/principles draws about a citation: a row saying a principle is
// driven through the binary says a test here claims to drive it, never that
// the test is enough. What the table is for is that a principle this harness
// does not reach is a line somebody had to write rather than an absence
// nobody can see.
func Coverage() []Established {
	return []Established{
		{principles.P1, ReachBinary,
			"An ordinary push to origin after the binary has created a gate is driven as a process and " +
				"read back off the remote. What is checked is that origin's own configuration is " +
				"untouched, that the push lands, and that no run exists afterwards."},
		{principles.P2, ReachBinary,
			"One run of the binary walks the nine stages in the specified order; a second skips two of " +
				"them for that run only; and a home whose configuration document asks for a standing " +
				"skip stops the service before it serves. Two narrow facts are what establish the third: " +
				"internal/config's key table admits no key named skip, and a key it does not admit is " +
				"refused where the document is walked, before the service binds. A second key of another " +
				"name is driven beside it and held to the same answer, so the refusal is not read off one " +
				"word. Whether some row that table does carry would apply a standing skip is what a row " +
				"means rather than what it is called, and that table is its one owner; nothing here " +
				"enumerates it. The order being unsayable in any other shape is internal/pipeline's, " +
				"which this does not repeat."},
		{principles.P3, ReachBinary,
			"A run through the binary is walked to its end and every hold it reaches is read: it holds " +
				"once for each of the nine stages, every hold is relayed with the finding that produced " +
				"it, every one of those findings reports itself as holding for a person, and none is one " +
				"a fixer may take. The planted agent output is driven at package reach as well, because " +
				"no stage body launches an agent in this build, so no report an agent wrote reaches a run."},
		{principles.P4, ReachPackage,
			"The binary cannot reach this: no stage body launches an agent, so a run makes no agent " +
				"invocation to assert over. What is driven is the production adapter over the stand-in, " +
				"which is where the type split lives and where an invocation record can be read off the " +
				"wire."},
		{principles.P5, ReachNone,
			"There is no review stage, so no run reviews a change a fix round wrote. " +
				"internal/principles declares this gap and this harness adds nothing to it."},
		{principles.P6, ReachBinary,
			"The service is killed at every stage boundary of a real run and the run is driven to its " +
				"end afterwards, which is P6's own stated verification criterion. The lease anchor and " +
				"the refusal against a remote that advanced out of band are driven at package reach, " +
				"because no stage body pushes."},
		{principles.P7, ReachBinary,
			"The git template that would choose what a gate is born with is refused by the binary, the " +
				"environment channel is shown closed, and a whole run over the branch carrying an agent " +
				"harness installation leaves every tripwire quiet. The trusted configuration document is " +
				"driven at package reach: nothing in this build reads a repository's own configuration " +
				"from anywhere, which internal/service states, so there is no composition to drive."},
		{principles.P8, ReachBinary,
			"A task whose event log ends on an open decision and whose resolved state has moved past it " +
				"is reported by the binary as the resolved state."},
		{principles.P9, ReachNone,
			"Nothing in this repository supervises, so there is no wake to classify. " +
				"internal/principles declares this gap."},
		{principles.P10, ReachNone,
			"There is no coordinator and no watcher here, so no turn ends. internal/principles declares " +
				"this gap."},
		{principles.P11, ReachNone,
			"Nothing here launches a worker into an isolated copy. internal/principles declares this gap."},
		{principles.P12, ReachNone,
			"Nothing here removes an isolated copy. The binary's eject removes a gate, which is a " +
				"different question. internal/principles declares this gap."},
		{principles.P13, ReachNone,
			"Every answer this harness reads back carries run identifiers, stage names and home paths, " +
				"so the principle does not hold on this surface today and a test claiming it here would " +
				"be claiming the opposite of what was observed. internal/principles declares the gap and " +
				"says the same thing."},
		{principles.P14, ReachNone,
			"One owner per fact is a property of the source rather than of a run, so driving a journey " +
				"establishes nothing about it. internal/principles and internal/pipeline's schema test " +
				"claim it."},
	}
}

// PackageDir is this package's directory relative to the module root. Citations
// are reported with module-relative paths, so this is what tells a citation
// made here from one made anywhere else.
const PackageDir = "internal/journey"

// CoverageReport describes every way the table and the citations in this
// package disagree, and is empty when they agree.
//
// Both directions are checked and the second is the one that holds the line. A
// row saying a principle is driven here, with no test here claiming it, is a
// table that describes a harness somebody meant to write. A row saying a
// principle is not driven here, with a test here claiming it, is a gap
// declaration that outlived the gap, which is exactly the shape
// internal/principles refuses for the repository as a whole.
//
// Neither direction says a test checks what it claims. Nothing can: a citation
// is a claim a test makes about itself.
func CoverageReport(root string) (string, error) {
	cited, err := citedHere(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	seen := map[principles.Principle]bool{}
	for _, row := range Coverage() {
		if seen[row.Principle] {
			fmt.Fprintf(&b, "%s: the table has more than one row for it, and every fact has one owner.\n", row.Principle)
		}
		seen[row.Principle] = true
		if strings.TrimSpace(row.Note) == "" {
			fmt.Fprintf(&b, "%s: the row says nothing about what is or is not established.\n", row.Principle)
		}
		claims := cited[row.Principle]
		switch {
		case row.Reach == ReachNone && len(claims) > 0:
			fmt.Fprintf(&b, "%s: the table says this harness does not reach it, and %s here cites it. "+
				"Change the row or drop the citation.\n", row.Principle, strings.Join(claims, ", "))
		case row.Reach != ReachNone && len(claims) == 0:
			fmt.Fprintf(&b, "%s: the table says this harness drives it at %s reach, and no test here cites it. "+
				"Add principles.Cite to the test that drives it, or change the row.\n", row.Principle, row.Reach)
		}
	}
	for _, listed := range principles.All() {
		if !seen[listed] {
			fmt.Fprintf(&b, "%s: this package has a constant for it and the table has no row. "+
				"Every principle is either driven here or declared not to be.\n", listed)
		}
	}
	for principle, claims := range cited {
		if !seen[principle] {
			fmt.Fprintf(&b, "%s: cited by %s here and the table has no row for it.\n",
				principle, strings.Join(claims, ", "))
		}
	}
	return b.String(), nil
}

// citedHere is every principle a test in this package claims, by the tests
// that claim it.
func citedHere(root string) (map[principles.Principle][]string, error) {
	all, err := principles.Citations(root)
	if err != nil {
		return nil, err
	}
	here := map[principles.Principle][]string{}
	for _, citation := range all {
		if filepath.ToSlash(filepath.Dir(citation.File)) != PackageDir {
			continue
		}
		name := citation.Test
		if !slices.Contains(here[citation.Principle], name) {
			here[citation.Principle] = append(here[citation.Principle], name)
		}
	}
	for principle := range here {
		slices.Sort(here[principle])
	}
	return here, nil
}
