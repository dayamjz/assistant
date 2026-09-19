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
			Exercised: "Every run reads the document now: internal/service reads both repository " +
				"copies per run, spelling the name from config.RepositoryDocument, and " +
				"internal/fixture's ConfigPath follows that constant, so the name has one owner and " +
				"this harness still reads it out of the built catalog. The harness also drives " +
				"config.Parse and vcs.Repository.FileAt against the planted documents directly.",
			Raise: "PRD section 10 places the document at the repository root and does not name it. " +
				"config.RepositoryDocument owns the name in the mechanism now; what the PRD still " +
				"owes is the sentence naming it.",
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
				"The selection half has nothing to select. No run this harness drives reaches an agent " +
				"- the review body would launch the one these journeys script to answer nothing, so " +
				"every walk skips the stage - so no report an agent wrote reaches a run, and the " +
				"planted responses are driven through internal/findings and the production adapter " +
				"instead.",
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
			Exercised: "Driven at package reach, against forge.ChecksReport.Evaluate. No run asks a " +
				"provider anything in this build: the pull request stage's body would, and it fails " +
				"because the run's record names no repository on the code host.",
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
				"under that key. The unsuppressed case is a whole run over the branch, and it now " +
				"establishes the other half too: the run executes the trusted commands.test against " +
				"the branch, and the tripwire file is asserted quiet with that execution as the " +
				"fact that makes the absence discriminating, so nothing in the installation's prose " +
				"became an executed command or a selected agent.",
		},
		{
			Question: "question-trusted-and-pushed-composition",
			Decision: "internal/service owns it now. Its layers.go reads the trusted copy from the " +
				"default branch at a freshly fetched commit and the pushed copy at the run's " +
				"submitted commit, both out of the gate's bare repository, and config.ResolveRun is " +
				"the composition itself, taking the operator's layer and both copies in one call. " +
				"The fixture's provisional answer - that config.Resolve takes one repository layer, " +
				"so the composition had no owner - described the build before that call existed.",
			Exercised: "A run over the harness-installation branch executes the trusted commands.test " +
				"while the branch's own is rejected, which is the composition observed through the " +
				"binary; the rejection texts are read at package reach, which predates the run's " +
				"record carrying them and has not been rewired to read them off it. A run over a " +
				"trusted document that will not parse is refused before launching anything, through " +
				"the binary as well.",
			Raise: "Raised here once that a run's rejections reached the service log only; answered " +
				"since. The run's record now carries the resolution's rejected keys and the " +
				"resolved agent's name, written when the run begins, and the surfaces that report " +
				"the record relay both: the terminal rendering prints an Agent line and the " +
				"rejection lines wherever a run is shown, and the structured answer carries them " +
				"on the record itself. What the record still understates: a run refused before " +
				"its resolution carries neither fact, and a resume does not rewrite them, so " +
				"they describe the resolution the run began under. This harness still reads the " +
				"rejection texts in process rather than off a run's record.",
		},
		{
			Question: "question-hookspath-redirect-observation",
			Decision: "The configuration file is given to every process this harness runs against that " +
				"scenario, which is the initialization and the push both, and yes: a push is driven " +
				"through the gate under it. Initialization reports success either way, so the redirect " +
				"is invisible until a push is made, and what distinguishes a gate with admission from " +
				"one without is which hook git ran. The planted hooks are tripwires, so the answer is " +
				"read off the tripwire file rather than out of a document.",
			Exercised: "Driven through the binary. Both halves are held to a disjunction rather than to " +
				"the answer this build happens to give, because a check that asserted a gap persists " +
				"would fail on the day it is closed. Under the ordinary environment what is asserted is " +
				"that the push is not accepted with nothing checking it: either it is declined, or it " +
				"is accepted and this home recorded a run from it, read out of the store because the " +
				"surface's answer would need a service this check never starts. Today it is declined, " +
				"and this check never starts the service the gate's admission hook asks, so what " +
				"declined it is not established here: the decline is logged rather than asserted, and " +
				"admission itself is driven by TestAPushToTheGateByNameAuthorizesTheRun, which does " +
				"start one. Under the redirect the harness holds " +
				"the product to a disjunction and does not choose between its halves, because a check " +
				"that asserted the gap persists would fail on the day it is closed: either creating the " +
				"gate is refused, or the push is accepted, and in that second case the redirected " +
				"pre-receive has to be among the tripwires that fired. post-receive is planted too and " +
				"is not asserted, since only pre-receive has to run for git to accept a push. Today it " +
				"is the second half that happens, and it is reported as the gap internal/gate/doc.go " +
				"names rather than as a pass.",
			Raise: "This settlement raised, and internal/cli has since answered, that the gate's " +
				"admission hook invoked subcommands the command surface did not carry, so no push could " +
				"cross the consent boundary P1 draws. That surface now carries them and a push to the " +
				"gate is admitted and starts a run, which " +
				"TestAPushToTheGateByNameAuthorizesTheRun drives. What is left of the finding is the " +
				"specification rather than the code: PRD section 9's table still names no command for " +
				"either subcommand, and internal/gate/hooks.go requires both of the surface, so the two " +
				"documents disagree about what that surface owes.",
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
