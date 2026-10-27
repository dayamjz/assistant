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
		lease      = "TestAPushOverARemoteThatMovedUnderTheRunIsRefused"
		ownership  = "TestACopiedProjectDirectoryDoesNotOwnTheGateItInherited"
		checks     = "TestAnEmptyCheckListIsNotAPass"
	)
	// The four hostile-template conditions share one caveat and split into two
	// pairs. No clause anywhere rests on their tripwire file, and none of the
	// four observes a hook at all, so what each establishes is only how the
	// initialization came out.
	const noTemplateHookPath = " Whether a hook the template carries arrived in the gate is not " +
		"established, and neither is whether one ran. No clause looks at the gate's hooks, and the " +
		"tripwire file has no producer here: a template's hooks are receive-side, so only a push to " +
		"the gate could run one, and these four subtests make no push. Each initializes a gate and " +
		"reads how that came out, and none starts a service, so neither a promoted pre-receive nor " +
		"update nor post-update is reached."
	// The two refusals: what is established is that the binary refused, and
	// that the refusal carries the substrings the condition records.
	const templateRefusalDriven = "What is established is the refusal itself, with the substrings the " +
		"condition records." + noTemplateHookPath
	// The two closed channels: neither is a refusal and neither records a
	// substring, so what is established is that the initialization was not
	// refused, which is what shows the channel closed rather than caught.
	const templateChannelClosed = "What is established is that the initialization was not refused, " +
		"which is what shows this channel closed rather than caught. The condition records no " +
		"substring and no sentinel, so there is nothing else here to hold a message to." +
		noTemplateHookPath

	// The document and lint conditions are undriven for one reason, stated
	// once: no run here reaches either stage over the scenario that plants
	// the condition with a command configured. The P7 branch run's trusted
	// document names commands, but the convergence bound parks that run at
	// the test stage first. The walks that reach the push stage configure
	// commands.lint in the home and walk the walkable subject, which plants
	// neither condition, and no run here configures a document command.
	const noConfiguredCommand = "The stage this condition names runs only the command the run's " +
		"resolved configuration names, and no run here reaches this stage over this condition's " +
		"scenario with one: the one run whose trusted document names commands is parked at the " +
		"test stage first, the walks that configure a lint command in the home walk the walkable " +
		"subject, which plants nothing for it to find, and no run configures a document command. " +
		"There is no finding to compare against what was planted. It becomes drivable when a run " +
		"over this condition's scenario reaches the stage carrying a configured command."
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
			"The binary is asked to create a gate under a configuration file naming the hostile " +
				"template. " + templateRefusalDriven},
		{"refusal-template-hooks-on-repair", ReachBinary, gateBirth,
			"The same file over a gate the binary already created. " + templateRefusalDriven},
		{"closed-template-pre-receive-on-repair", ReachBinary, gateBirth,
			"The negative case that gives the two refusals their meaning. " + templateChannelClosed +
				" That the template's pre-receive is neither installed nor preserved at the .local " +
				"name, which this condition also records, is part of what is not established."},
		{"closed-git-template-dir-environment", ReachBinary, gateBirth,
			"The variable is exported into the process that initializes the gate. " +
				templateChannelClosed},
		{"gap-core-hookspath-redirects-the-gate", ReachBinary, gateBirth,
			"Reported as the gap internal/gate names rather than as a pass: a push is driven through the " +
				"gate under the redirect and the tripwire says which hook git ran."},

		{"refusal-hostile-harness-installation", ReachBinary, branch,
			"A whole run over the branch: the run resolves the trusted document, executes its " +
				"commands.test against the branch, and converges at the failing check, and the " +
				"tripwire file is asserted quiet with that execution as the fact that makes the " +
				"absence discriminating. The branch's own \"agent\" is read as a rejection of the " +
				"pushed layer in process - the run's record carries its rejections now, and this " +
				"read predates that - and the suppression refusal is driven in a home of its own. The " +
				"run's fix rounds launch the operator-resolved stand-in, scripted to change " +
				"nothing; the .claude hooks and the branch's agent binary stay unreached because " +
				"the walk skips the review stage and the resolution rejects the branch's agent, " +
				"not because nothing launches at all."},
		{"refusal-pushed-commands-and-agent", ReachBinary, branch,
			"The run above is this condition's tripwire half through the binary: the test stage " +
				"executed the trusted commands.test while the branch's own, a script that would " +
				"have appended to the tripwire file, left it empty. The rejections are read in " +
				"process, over config.Resolve on the operator's layer and the pushed one - a read " +
				"that predates the run's record carrying its own rejections and has not been " +
				"rewired to it; the three keys are dropped and reported and the one the branch " +
				"may set survives."},
		{"refusal-unparseable-trusted-config", ReachBinary, trustedDoc,
			"Both reaches. The document is read off the default branch through internal/vcs and " +
				"parsed in process, which is where the refusal's shape is held to the condition; " +
				"and a run over a clone of the scenario is refused before launching anything, " +
				"naming the trusted configuration, which is PRD section 10's abort through the " +
				"binary."},
		{"refusal-unreadable-trusted-config", ReachPackage, trustedDoc, "As the row above's in-process " +
			"half, for the read rather than the parse; no run is driven over this scenario, and " +
			"internal/service takes the same path for both failures."},

		{"refusal-remote-advanced-out-of-band", ReachBinary, lease,
			"A run through the binary is held between the rebase stage's observation and the push " +
				"stage's decision, at the first hold stages.Holding gives there, and the advance is " +
				"planted then by AdvanceRemoteOutOfBand. The push stage holds on the refusal, which is " +
				"held to the reason the condition records, to every substring of the message it " +
				"records, to naming as discarded exactly the commit the colleague landed, to the " +
				"remedy internal/safety states for would-discard, and to an anchor naming the commit " +
				"the run observed; the remote is read back and still holds the landed commit. The " +
				"remote and the second clone are a copy of the walkable scenario's, made by the same " +
				"git commands the fixture's builder runs, because this scenario's trusted document " +
				"configures commands.test over a branch planted to fail it and a run over it is parked " +
				"at the test stage first. Not established through the binary: the remedy working, " +
				"because no surface here re-runs a run onto its new upstream. " +
				"TestAnUpdateIsAnchoredToWhatTheRunObservedRatherThanToAFreshRead follows it at package " +
				"reach, over internal/safety and this scenario itself."},

		{"refusal-copied-working-copy-removal", ReachPackage, ownership,
			"gate.Remove against the copy, with the binary's own removal driven alongside to establish " +
				"that it removes nothing either."},
		{"adoption-copied-working-copy-initialize", ReachBinary, ownership,
			"The binary initializes through the copy and is checked to have given it a gate of its own."},

		{"refusal-no-registered-checks", ReachPackage, checks,
			"internal/forge over a provider command this harness stands in for, with the run's own head " +
				"substituted into the recorded answer. No run reaches a code host: the pull request " +
				"stage's body asks one and fails because the run's record names no repository there."},

		{"stage-logic-bug", ReachNone, "",
			"Three walks take the review stage against a stand-in scripted to answer a clean review, " +
				"and the scenario they walk plants no bug, so there is no finding to compare against " +
				"what was planted. It becomes drivable when a walk takes the stage over this " +
				"condition's subject with the planted finding scripted - and what it establishes " +
				"then is " +
				"still bounded by README.md's first limit, because the stand-in answers what a test " +
				"scripts rather than reading the planted bug."},
		{"stage-failing-test", ReachBinary, branch,
			"The run over the harness-installation branch is this condition's too: the trusted " +
				"commands.test runs against the branch, fails with the planted assertion in its " +
				"output, the do-nothing fix round changes no state, and the convergence bound " +
				"parks the run rather than walking past the failure."},
		{"stage-stale-documentation", ReachNone, "", noConfiguredCommand},
		{"stage-lint-violation", ReachNone, "", noConfiguredCommand},
		{"stage-no-diff-after-rebase", ReachBinary, "TestARunWhoseRebaseLeavesNothingToDeliverEndsThroughTheBinary",
			"A run of this scenario's branch, over a copy of its remote, really replays the branch's " +
				"commit and drops it: the run passes with every stage after rebase skipped and no agent " +
				"invoked, and the remote's branch does not move. With the branch removed from the copy " +
				"first, the run also writes one nothing-to-deliver record naming the run's commit and " +
				"the default branch's, and changes no task; as planted, with the branch already on the " +
				"remote, it writes none. The walk is a person's run, so what a task does with the " +
				"record is held at package reach by internal/service's tests."},
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
