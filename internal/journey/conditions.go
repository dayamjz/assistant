package journey

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/fixture"
)

// Drove is one row of what this harness does about one planted condition.
type Drove struct {
	// Condition is the identifier internal/fixture plants it under.
	Condition fixture.ID
	// Reach is how far the product is driven to meet it, and is ReachNone for
	// a condition nothing here drives.
	Reach Reach
	// By names the test that drives it, and is empty exactly when Reach is
	// ReachNone. It is a claim on the same terms as a principle citation: it
	// says a test says it drives this, never that the test is enough.
	By string
	// Note says what is driven, or why nothing is. It is prose nobody checks.
	Note string
}

// Drives is what this harness does about every condition internal/fixture
// plants, one row per condition.
//
// The fixture plants twenty-odd conditions and most of them are refusals,
// which is where most of this product's value is. A harness that drove the
// ones it found easy and said nothing about the rest would report green over
// refusals nobody had triggered, which is indistinguishable from refusals that
// do not work. So every condition is either driven by a test named here or
// declared undriven with a reason, and DrivesReport fails on either half being
// out of step with the catalog.
func Drives() []Drove {
	const (
		findings   = "TestAFindingThatIsNotClassifiedStopsForAPerson"
		gateBirth  = "TestNothingOutsideTheGateChoosesWhatRunsOnAPushToIt"
		branch     = "TestTheBranchUnderValidationChoosesNothingThatRuns"
		trustedDoc = "TestATrustedConfigurationThatCannotBeReadIsNotFallenBackFrom"
		anchor     = "TestAnUpdateIsAnchoredToWhatTheRunObservedRatherThanToAFreshRead"
		ownership  = "TestACopiedProjectDirectoryDoesNotOwnTheGateItInherited"
		checks     = "TestAnEmptyCheckListIsNotAPass"
	)
	// Every stage condition is undriven for one reason, stated once. A stage
	// with no body reports one ask finding and holds, so there is nothing for
	// review, test, document, or lint to have found and nothing for the rebase
	// to have emptied.
	const noStageBody = "internal/stages holds no body for the stage this condition names, so the stage " +
		"reads nothing and reports one ask finding. There is no finding to compare against what was " +
		"planted, and a harness that reported this condition as met would be reporting the placeholder. " +
		"It becomes drivable with the stage body."
	return []Drove{
		{"refusal-finding-action-missing", ReachPackage, findings,
			"The planted bytes go through the production adapter and internal/findings, on the entry " +
				"point a stage that is not a review reports through."},
		{"refusal-finding-action-empty", ReachPackage, findings, "As above."},
		{"refusal-finding-action-unrecognized", ReachPackage, findings, "As above."},
		{"refusal-finding-action-missing-review-path", ReachPackage, findings,
			"As above, on the entry point that binds evidence, with the run's own revision substituted " +
				"into the bytes and the unsubstituted bytes driven alongside to show the substitution " +
				"was needed."},
		{"refusal-finding-action-empty-review-path", ReachPackage, findings, "As above."},
		{"refusal-finding-action-unrecognized-review-path", ReachPackage, findings, "As above."},

		{"refusal-template-hooks-at-birth", ReachBinary, gateBirth,
			"The binary is asked to create a gate under a configuration file naming the hostile template."},
		{"refusal-template-hooks-on-repair", ReachBinary, gateBirth,
			"The same file over a gate the binary already created."},
		{"closed-template-pre-receive-on-repair", ReachBinary, gateBirth,
			"The negative case that gives the two refusals their meaning."},
		{"closed-git-template-dir-environment", ReachBinary, gateBirth,
			"The variable is exported into the process that initializes the gate, and the channel is " +
				"closed before git sees it."},
		{"gap-core-hookspath-redirects-the-gate", ReachBinary, gateBirth,
			"Reported as the gap internal/gate names rather than as a pass: a push is driven through the " +
				"gate under the redirect and the tripwire says which hook git ran."},

		{"refusal-hostile-harness-installation", ReachBinary, branch,
			"A whole run over the branch carrying the installation, with the tripwire file read at the end."},
		{"refusal-pushed-commands-and-agent", ReachBinary, branch,
			"The one call the condition names is driven at package reach; the run alongside it is what " +
				"establishes that nothing the branch named executed."},
		{"refusal-unparseable-trusted-config", ReachPackage, trustedDoc,
			"The document is read off the default branch through internal/vcs and parsed. No run reads a " +
				"repository's own document in this build, which the same test observes on a run."},
		{"refusal-unreadable-trusted-config", ReachPackage, trustedDoc, "As above, for the read rather " +
			"than the parse."},

		{"refusal-remote-advanced-out-of-band", ReachPackage, anchor,
			"internal/safety over internal/vcs's own reads, with the advance planted between the " +
				"observation and the decision. No stage body pushes, so no run proposes an update."},

		{"refusal-copied-working-copy-removal", ReachPackage, ownership,
			"gate.Remove against the copy, with the binary's own removal driven alongside to establish " +
				"that it removes nothing either."},
		{"adoption-copied-working-copy-initialize", ReachBinary, ownership,
			"The binary initializes through the copy and is checked to have given it a gate of its own."},

		{"refusal-no-registered-checks", ReachPackage, checks,
			"internal/forge over a provider command this harness stands in for, with the run's own head " +
				"substituted into the recorded answer. No stage body talks to a code host."},

		{"stage-logic-bug", ReachNone, "", noStageBody},
		{"stage-failing-test", ReachNone, "", noStageBody},
		{"stage-stale-documentation", ReachNone, "", noStageBody},
		{"stage-lint-violation", ReachNone, "", noStageBody},
		{"stage-no-diff-after-rebase", ReachNone, "", noStageBody +
			" This one needs the rebase stage in particular, because the branch's change is only " +
			"invisible once a real rebase has dropped it."},
	}
}

// DrivesReport describes every way the rows above and the catalog
// internal/fixture built disagree, and is empty when they agree.
//
// Three things are checked and the last is the one that keeps the rest honest.
// A condition the catalog plants and no row mentions is one this harness was
// handed and said nothing about. A row naming a condition the catalog does not
// plant is an answer that outlived its question. And a row naming a test this
// package does not have is a claim nobody could have made, which is the shape
// a row acquires when a test is renamed or deleted.
//
// What none of it says is that a named test drives what its row claims. A row
// is a claim on the same terms internal/principles states for a citation, and
// nothing here can check it.
func DrivesReport(root string) (string, error) {
	subject, err := Subject()
	if err != nil {
		return "", err
	}
	tests, err := TestNames(filepath.Join(root, filepath.FromSlash(PackageDir)))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	rows := map[fixture.ID]Drove{}
	for _, row := range Drives() {
		if _, twice := rows[row.Condition]; twice {
			fmt.Fprintf(&b, "%s: more than one row here, and every fact has one owner.\n", row.Condition)
		}
		rows[row.Condition] = row
		if strings.TrimSpace(row.Note) == "" {
			fmt.Fprintf(&b, "%s: the row says nothing about what is driven or why nothing is.\n", row.Condition)
		}
		switch {
		case row.Reach == ReachNone && row.By != "":
			fmt.Fprintf(&b, "%s: the row says nothing here drives it and names %s.\n", row.Condition, row.By)
		case row.Reach != ReachNone && row.By == "":
			fmt.Fprintf(&b, "%s: the row says this harness drives it at %s reach and names no test.\n",
				row.Condition, row.Reach)
		case row.By != "" && !slices.Contains(tests, row.By):
			fmt.Fprintf(&b, "%s: the row names %s, and this package has no test by that name.\n",
				row.Condition, row.By)
		}
	}
	for _, condition := range subject.Conditions {
		if _, ok := rows[condition.ID]; !ok {
			fmt.Fprintf(&b, "%s: internal/fixture plants it in the %s scenario and nothing here says what "+
				"this harness does about it. Drive it, or add a row saying why it is not driven.\n",
				condition.ID, condition.Scenario)
		}
	}
	planted := map[fixture.ID]bool{}
	for _, condition := range subject.Conditions {
		planted[condition.ID] = true
	}
	for id := range rows {
		if !planted[id] {
			fmt.Fprintf(&b, "%s: a row here names it and internal/fixture plants no such condition.\n", id)
		}
	}
	return b.String(), nil
}

// TestNames is every test function declared in the Go files of one directory.
//
// It reads source, which this repository rejects as evidence about behaviour
// and accepts here for the reason internal/principles gives for its own scan:
// what is being measured is a claim the test suite makes, and a claim is a
// thing written in the test suite. Nothing it reports says a test checks
// anything.
func TestNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("journey: reading %s: %w", dir, err)
	}
	set := token.NewFileSet()
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(set, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("journey: parsing %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			names = append(names, fn.Name.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("journey: %s declares no test, so nothing could name one", dir)
	}
	slices.Sort(names)
	return names, nil
}
