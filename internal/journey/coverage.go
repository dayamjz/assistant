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
	// body would have used past the run's own state: the intent body reads
	// the supplied intent and launches nothing, and the review body opens the
	// run's isolated copy - which nothing in this build creates - before it
	// launches anything, so a run that takes it fails there and every walk
	// here skips it instead.
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
			"P1 has two halves and both are driven, so a skip takes down one of them rather than the " +
				"principle. The half that says what a push to origin must not become: an ordinary push " +
				"to origin after the binary has created a gate is driven as a process and read back off " +
				"the remote, and what is checked is that origin's own configuration is untouched, that " +
				"the push lands, and that no run exists afterwards. It reaches no method internal/ipc " +
				"restricts, so no platform refuses it for want of peer credentials, but it does need a " +
				"service, so on a platform with no local socket transport to serve this protocol over it " +
				"is skipped rather than passing. The half that says what a push to the gate by name does: " +
				"such a push is driven and the run it authorized is held to the branch and the commit " +
				"that were pushed, rather than to some run having started. That push crosses the gate's " +
				"admission hook, which asks methods internal/ipc restricts, so on a platform where that " +
				"package reads no local socket peer credentials it is skipped rather than passing. Where " +
				"either skip is taken nothing about that half is established there, and where both are, " +
				"nothing about P1 is."},
		{principles.P2, ReachBinary,
			"One run of the binary reaches every stage it does not skip; a second skips two of " +
				"them for that run only; and a home whose configuration document asks for a standing " +
				"skip stops the service before it serves. Both runs skip review, because its body " +
				"fails on the isolated copy nothing in this build creates, so no run here both takes " +
				"that stage and reaches the end of the gate. Two narrow facts are what establish the third: " +
				"internal/config's key table admits no key named skip, and a key it does not admit is " +
				"refused where the document is walked, before the service binds. A second key of another " +
				"name is driven beside it and held to the same answer, so the refusal is not read off one " +
				"word. Whether some row that table does carry would apply a standing skip is what a row " +
				"means rather than what it is called, and that table is its one owner; nothing here " +
				"enumerates it. The order the stages come back in is not established here and no clause " +
				"claims it: internal/pipeline's nine named fields make another order unsayable, and " +
				"internal/service renders a run's answer by iterating that order, so a clause over the " +
				"order could not fail from what a run reports. What the run half establishes instead is " +
				"that a report came back for every stage, that each the run did not skip ran, and that " +
				"each carried the outcome its hold was given. All three parts are one test, and it starts a run through a " +
				"method internal/ipc restricts, so on a platform where that package reads no local socket " +
				"peer credentials the test is skipped rather than passing and nothing about P2 is " +
				"established there."},
		{principles.P3, ReachBinary,
			"A run through the binary is walked to its end and every hold it reaches is read: it holds " +
				"once for each stage this build has no body for, which is read off internal/stages rather " +
				"than counted, every hold is relayed with the finding that produced " +
				"it, every one of those findings reports itself as holding for a person, and none is one " +
				"a fixer may take. The planted agent output is driven at package reach as well, because " +
				"no run reaches an agent in this build - the review body would launch one, and it fails " +
				"on the isolated copy nothing creates before launching, so the walk skips it - and no " +
				"report an agent wrote therefore reaches a run. " +
				"The run half starts a run through a method internal/ipc restricts, so on a platform where " +
				"that package reads no local socket peer credentials it is skipped rather than passing, and " +
				"the package-reach half over the planted agent output is the whole of what is established " +
				"there."},
		{principles.P4, ReachPackage,
			"The binary cannot reach this: no run launches an agent, because the one body that would - " +
				"review - fails on the isolated copy nothing in this build creates before it launches, " +
				"and every walk here skips it, so a run makes no agent invocation to assert over. What " +
				"is driven is the production adapter over the stand-in, which is where the type split " +
				"lives and where an invocation record can be read off the wire."},
		{principles.P5, ReachNone,
			"The review stage has a body and no run this harness drives can take it: it fails on the " +
				"isolated copy nothing in this build creates, so every walk skips it, no run reviews " +
				"anything, and no fix round is taken for one to re-review. internal/stages' own tests " +
				"claim P5 against the body directly; this harness adds nothing to that."},
		{principles.P6, ReachBinary,
			"The service is killed at every boundary a real run reaches and the run is driven to its " +
				"end afterwards, which is P6's own stated verification criterion; the criterion is every " +
				"boundary rather than a count of them. A boundary is a hold, so the boundaries are the " +
				"stages this build has no body for. The intent stage is not among them and no kill is " +
				"manufactured for it: it has a body, PRD section 5 has it never block a run, and a stage " +
				"that never holds offers nothing to kill at. Review is not among them either: the run " +
				"skips it, because its body fails on the isolated copy nothing creates rather than " +
				"holding, and a skipped stage offers no hold. What the recovered decision is held to is " +
				"that it stands at the same stage and still offers what it offered, which a checkpoint " +
				"round trip can lose; that it offers something nobody was offered is not claimed, " +
				"because internal/pipeline gives every hold the same fixed rendering and internal/graph " +
				"copies it through, so no run can report one. The lease anchor and " +
				"the refusal against a remote that advanced out of band are driven at package reach, " +
				"because no stage body pushes. The killed run, and the contention check beside it, are " +
				"driven through methods internal/ipc restricts, so on a platform where that package reads " +
				"no local socket peer credentials both are skipped rather than passing, and the two " +
				"package-reach observations are the whole of what is established there."},
		{principles.P7, ReachBinary,
			"The git template that would choose what a gate is born with is refused by the binary, and " +
				"the environment channel is shown closed. How the initialization came out is the whole " +
				"of what those four subtests establish: the refusals refuse with the substrings their " +
				"conditions record, and the closed channels are not refused. Whether a template hook " +
				"arrived in the gate is not among it, because no clause there looks at a hook. A whole " +
				"run is driven over a branch carrying an agent harness installation, and " +
				"what that run establishes is that a suppression the adapter does not implement is " +
				"refused before anything launches. The keys the branch may not set are dropped and " +
				"reported while the one it may set survives, but that is config.Resolve driven in " +
				"process beside the run rather than anything the run does. Two things are not " +
				"established at any reach and no clause claims them: that nothing the branch or the " +
				"template installed arrived or executed, because the branch's planted executables are " +
				"reached only through a stage body that launches something and no run reaches one - " +
				"the intent body reads the supplied intent, and the review body fails on the isolated " +
				"copy nothing creates before it launches, so the run skips it - while the template's " +
				"are receive-side hooks reached " +
				"only by a push to the gate and the four subtests that plant one make no push; " +
				"and which agent a run resolved, because no shipped surface reports it. The trusted configuration " +
				"document is driven at package reach: nothing in this build reads a repository's own " +
				"configuration from anywhere, which internal/service states, so there is no composition " +
				"to drive. The branch run, and the one subtest that starts a run over the unparseable " +
				"trusted document, go through methods internal/ipc restricts, so on a platform where that " +
				"package reads no local socket peer credentials both are skipped rather than passing; what " +
				"is established there is the template initialization, which needs no service, and the " +
				"package-reach reads of the planted documents."},
		{principles.P8, ReachBinary,
			"A task whose event log ends on an open decision and whose resolved state has moved past it " +
				"is reported by the binary as the resolved state. Reading it reaches no method " +
				"internal/ipc restricts, so no platform refuses it for want of peer credentials, but it " +
				"is asked of a service, so on a platform with no local socket transport to serve this " +
				"protocol over the test is skipped rather than passing and nothing about P8 is " +
				"established there."},
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
