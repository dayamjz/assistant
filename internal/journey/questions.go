package journey

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/fixture"
)

// Settlement is this harness's answer to one question internal/fixture
// recorded and declined to answer.
//
// A settlement is a decision, not a report. What it says is what this harness
// does and why, and where the decision only becomes exercisable once something
// that does not exist yet is built, it says what it will be then rather than
// leaving the question open a second time.
type Settlement struct {
	// Question is the identifier internal/fixture recorded the question under.
	Question fixture.ID
	// Decision is what was decided.
	Decision string
	// Exercised says how much of the decision this harness actually drives
	// today, and names what stops it driving the rest. It is empty for a
	// decision that is driven whole.
	Exercised string
	// Raise is a finding this settlement produced against something outside
	// this harness, empty when it produced none. A question that could not be
	// settled cleanly because a contract elsewhere is missing is a finding to
	// raise rather than a decision to invent.
	Raise string
}

// Settlements is this harness's answer to every question internal/fixture
// recorded, one row per question.
//
// The questions were recorded rather than answered because a fixture that
// decides how a harness drives a condition has stopped being a fixture. This
// is the other side of that: the decisions live with the harness that makes
// them, and internal/fixture is not edited to hold them.
func Settlements() []Settlement {
	return []Settlement{
		{
			Question: "question-config-document-name",
			Decision: "This harness never spells the name. It reads Fixture.ConfigPath out of the built " +
				"catalog wherever it needs the path, so the name stays internal/fixture's to state and " +
				"following a package that comes to own it costs nothing here.",
			Exercised: "Nothing in this build reads a repository's own configuration document from " +
				"anywhere, which internal/service states outright, so the name is load-bearing in no " +
				"code path a run takes. What this harness reads it for is driving config.Parse and " +
				"vcs.Repository.FileAt against the planted documents directly.",
			Raise: "PRD section 10 places the document at the repository root and does not name it, and " +
				"no package owns the name. That is a gap in the specification rather than in any package, " +
				"and it stays open.",
		},
		{
			Question: "question-agent-response-delivery",
			Decision: "The stand-in is reached as an executable named claude standing in front of the " +
				"service's PATH, because internal/agents resolves a configured entry's first word off " +
				"PATH and there is no other seam into a separate process. The home's own configuration " +
				"names the entry, and the words after the name are what " +
				"internal/agents/standin's Agent.Arguments reports, which is how a copy of this test " +
				"binary is told which script to answer from.\n\n" +
				"Which response a stage gets is selected by internal/agents/standin's own Match, on the " +
				"prompt and on whether the invocation carried a session, and never by the order the " +
				"steps were written in. A run's invocations are not ordered predictably across a resume, " +
				"and a script that answered by position would hand a resumed run somebody else's answer.\n\n" +
				"The review-path revision is substituted into the recorded bytes before they are written " +
				"into the script, on the same terms as the checks answer: the commit the review stage is " +
				"asked about is the run's, and findings.ParseReviewReport refuses a report naming any " +
				"other commit before a finding is reached.",
			Exercised: "The delivery half is driven: a copy of this binary standing on PATH under the " +
				"agent's name answers a scripted invocation, and a run resolves its agent off that PATH. " +
				"The selection half has nothing to select. No stage body launches an agent in this " +
				"build, so no report an agent wrote reaches a run, and the planted responses are driven " +
				"through internal/findings and the production adapter instead.",
		},
		{
			Question: "question-provider-response-delivery",
			Decision: "The provider command is replaced the same way the agent is: a copy of this test " +
				"binary standing on PATH under the name internal/forge resolves, which prints the " +
				"prepared answer and nothing else. A caller that constructs the adapter itself names the " +
				"file through forge.WithBinary instead, which is the same substitution without the PATH.\n\n" +
				"The head is substituted by this harness before the answer is served, by SubstituteHead, " +
				"which refuses an answer that does not carry the head the build recorded. Serving the " +
				"recorded head unchanged would report a stale check list, which internal/forge " +
				"deliberately tells apart from an empty one, so the substitution silently doing nothing " +
				"is the same failure as not doing it at all and is refused rather than skipped.",
			Exercised: "Driven at package reach, against forge.ChecksReport.Evaluate. No stage body talks " +
				"to a code host in this build, so no run asks the provider anything.",
		},
		{
			Question: "question-deferred-plant-timing",
			Decision: "AdvanceRemoteOutOfBand is called between the observation and the decision: after " +
				"safety.Guard.Observe has taken the anchor and before Guard.Decide is asked about the " +
				"update. That is the constraint the condition states, and it is the only placement that " +
				"exercises anything, because an advance before the observation is a remote the run " +
				"observes ahead.\n\n" +
				"CopyGatedWorkingCopy is called after an initialization through the binary has succeeded " +
				"against the original working copy and before anything is asked of the copy, so the " +
				"copy inherits a remote a real gate wrote.",
			Exercised: "The copy's timing is driven through the binary. The advance's timing is driven at " +
				"package reach around a safety.Guard this harness holds, because no stage body pushes, " +
				"so a run never observes a remote and never submits an update.",
		},
		{
			Question: "question-checks-timeout-observation",
			Decision: "checks_timeout is global-only, so the harness sets it short in the home's own " +
				"configuration document and reads the run's park rather than waiting the timeout out. A " +
				"run that waited parks on the bound; a run that concluded reports a verdict. The two are " +
				"different answers on the same surface, so nothing has to be inferred from elapsed time.",
			Exercised: "Not driven. There is no checks stage, so nothing reads checks_timeout and no run " +
				"can wait on anything. What is driven is the verdict itself, at package reach: an empty " +
				"check list with no no_ci declaration evaluates to VerdictNoChecks, which is not green.",
		},
		{
			Question: "question-project-instructions-suppression",
			Decision: "Both, and the suppressed case is a refusal rather than a run. The shipped Claude " +
				"Code adapter declares no instruction-suppression capability, so a home whose " +
				"configuration sets suppress_project_instructions cannot build a pipeline at all and the " +
				"run refuses before any agent starts. The unsuppressed case is the ordinary run, in " +
				"which the branch's project instructions are prose an agent may read and the condition " +
				"is that nothing in that prose becomes an executed command or a selected agent.\n\n" +
				"internal/fixture therefore does not owe a second trusted document. The suppressed case " +
				"needs no scenario, because no run reaches a scenario under it.",
			Exercised: "The refusal is driven through the binary: it is the answer to starting a run " +
				"under that key. The unsuppressed case is a whole run over the branch, but what it " +
				"establishes is the pushed-configuration rejections rather than that nothing in the " +
				"prose became an executed command: every planted executable is reached only through a " +
				"stage body and this build has none, so the tripwire file is read and logged rather " +
				"than asserted on.",
		},
		{
			Question: "question-trusted-and-pushed-composition",
			Decision: "Nothing in this build composes them, so this harness drives the one call the " +
				"condition names - config.Resolve over the operator's global layer and the pushed layer " +
				"- and reports the other half as a gap rather than as a pass. Inventing a composition " +
				"here would make the harness the owner of a contract no shipped code answers to, and a " +
				"green harness would then be evidence about the harness.",
			Exercised: "The three rejections and the pushed ignore_patterns winning are driven at package " +
				"reach. The trusted values the default branch carries reach no run, because " +
				"internal/service reads the operator's global layer and the schema defaults and nothing " +
				"else.",
			Raise: "PRD section 10 requires configuration that executes code to be read from the default " +
				"branch at a freshly fetched commit, and no package in this build does that or owns " +
				"doing it. internal/service states the gap for itself. It is not a hole in P7 - no " +
				"branch's configuration is read at all, so none can direct what runs - and it is a gap " +
				"against section 10 that stays open.",
		},
		{
			Question: "question-hookspath-redirect-observation",
			Decision: "The configuration file is given to every process this harness runs against that " +
				"scenario, which is the initialization and the push both, and yes: a push is driven " +
				"through the gate under it. Initialization reports success either way, so the redirect " +
				"is invisible until a push is made, and what distinguishes a gate with admission from " +
				"one without is which hook git ran. The planted hooks are tripwires, so the answer is " +
				"read off the tripwire file rather than out of a document.",
			Exercised: "Driven through the binary. Under the ordinary environment the push through the " +
				"gate is declined, which is asserted every time. Under the redirect the harness holds " +
				"the product to a disjunction and does not choose between its halves, because a check " +
				"that asserted the gap persists would fail on the day it is closed: either creating the " +
				"gate is refused, or the push is accepted, and in that second case the redirected " +
				"pre-receive has to be among the tripwires that fired. post-receive is planted too and " +
				"is not asserted, since only pre-receive has to run for git to accept a push. Today it " +
				"is the second half that happens, and it is reported as the gap internal/gate/doc.go " +
				"names rather than as a pass.",
			Raise: "The decline in the unredirected case is not an admission decision. internal/gate " +
				"installs a hook that invokes \"assistant gate admit --gate ID\", which internal/cli's " +
				"verb table does not carry, so the push is declined by the command surface reporting " +
				"incorrect usage. internal/gate/hooks.go states the two subcommands it requires of that " +
				"surface, and internal/cli implements neither, so no push can cross the consent boundary " +
				"P1 draws. That is a finding against PRD section 9's table, which names no such command, " +
				"and it is exactly the case internal/cli/doc.go says to raise rather than quietly add.",
		},
	}
}

// SettlementReport describes every way the settlements and the questions
// internal/fixture recorded disagree, and is empty when they agree.
//
// Both directions are checked. A question with no settlement is one this
// harness was handed and did not answer, which is the whole failure this
// table exists against. A settlement naming a question the fixture does not
// record is a decision about something that is not being asked, which is how
// an answer outlives the question it was written for.
func SettlementReport() (string, error) {
	subject, err := Subject()
	if err != nil {
		return "", err
	}
	settled := map[fixture.ID]Settlement{}
	var b strings.Builder
	for _, settlement := range Settlements() {
		if _, twice := settled[settlement.Question]; twice {
			fmt.Fprintf(&b, "%s: settled more than once here.\n", settlement.Question)
		}
		if strings.TrimSpace(settlement.Decision) == "" {
			fmt.Fprintf(&b, "%s: the settlement decides nothing.\n", settlement.Question)
		}
		settled[settlement.Question] = settlement
	}
	for _, question := range subject.OpenQuestions {
		if _, ok := settled[question.ID]; !ok {
			fmt.Fprintf(&b, "%s: internal/fixture records it and nothing here settles it. It reads: %s\n",
				question.ID, question.Question)
		}
	}
	recorded := map[fixture.ID]bool{}
	for _, question := range subject.OpenQuestions {
		recorded[question.ID] = true
	}
	for id := range settled {
		if !recorded[id] {
			fmt.Fprintf(&b, "%s: settled here and internal/fixture records no such question.\n", id)
		}
	}
	return b.String(), nil
}
