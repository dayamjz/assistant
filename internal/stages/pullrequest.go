package stages

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// titleLimit is how long a derived pull request title may be before it is cut
// short. It bounds a title this stage composed out of intent text, which is
// prose a person wrote for another purpose and is under no length rule of its
// own. A title a person set on an existing pull request is never touched, so
// nothing here shortens anything a person chose.
const titleLimit = 72

// PullRequest is the pull request stage: it creates the pull request for the
// run's branch, or replaces the body of the one that is already there, and
// records which pull request the run is answering for.
//
// # What it composes and what it does not do
//
// internal/forge is the only package that talks to a code host, and
// forge.Submit is PRD section 5's create-or-update rule: one pull request per
// head, an existing one keeping its title, its base, and its draft state
// because those are things a person may have changed on purpose. This stage
// supplies the specification and takes the answer, and neither restates that
// rule nor works around it.
//
// It invokes no git. internal/vcs owns that, so where the branch stands is
// read from the run's state rather than from a checkout, and this stage opens
// none.
//
// It runs no agent, so it parses no agent output and produces no agent
// finding. Everything it reports is its own, and PRD section 5 gives it no
// automatic fix round, which is the stage table's row rather than anything
// here.
//
// # The body is the run, not a summary of it
//
// PRD section 5 asks for a body written for a reviewer who was not present:
// what changed, what was checked, what the risks are, what the pipeline had to
// fix. Those four are this body's four sections, and each is rendered from
// what the run's own stages recorded, through pipeline.ReadStageResult. This
// stage writes no sentence about a stage that it did not read out of that
// stage's report.
//
// A stage that has recorded nothing when the body is written is rendered as
// not having run rather than as having found nothing. This stage's own report
// and outcome are among them, because the stage node writes both after the
// body returns, and so is any stage the run has not reached. Which stages
// those are is read out of the state rather than stated here, so nothing in
// the body depends on where this stage sits in the order.
//
// # The body may not assert what the run did not establish
//
// The pull request is the one thing this run produces that a person outside
// the system reads, so a sentence in it is a claim made on the run's behalf.
// A run can reach this stage having forwarded no commit, because the push
// stage may hold and a person may answer approved, and in that case the branch
// the code host carries is whatever was already there. The body then states
// that rather than naming the commit the run validated as though it were on
// the branch, and the report carries the same fact as a note. It is a note and
// not a hold: skipping the push stage is a person's decision to make, and a
// refusal here would take it away from them.
//
// The base answers the same rule. A pull request a person retargeted keeps
// that base, per forge.Submit, so where the pull request merges to is
// something this run does not know. What the body says is therefore what this
// run validated merging into, which stays true whatever the pull request is
// targeted at.
//
// The residual gap is that the body does not disclose the pull request's own
// base, nor the disagreement when there is one, and a reader outside this
// system would be strictly better served if it did. That is deferred rather
// than overlooked. It would take the existing pull request found before the
// body is rendered, and the body is built as an argument to forge.Submit, so
// having it would reorder this stage around internal/forge's owned
// create-or-update rule while other work is being written against that seam.
// The disagreement does reach the run's record as a note in the meantime.
//
// # What the body does not carry: how many attempts it took
//
// PRD "what passing the gate means" says the pull request records what was
// checked, what was fixed, and how many attempts it took. This body carries
// the first two and no count of the third, and the section that would hold one
// says so rather than reading as complete.
//
// Neither record a count could come from is available to this body. A stage
// body reads state, the per-edge traversal counts are graph.Counters, and no
// internal/pipeline key exposes them. The PRD's round-history record, which it
// says the narrative is generated from, does exist as internal/store's round
// table with store.AppendRound and store.Rounds, but nothing in this build
// appends to it, so it holds no round for any run; and this package does not
// import internal/store, so reading it here would take a state key or a
// dependency that does not exist either.
//
// What the state does hold is what each stage's fixer last wrote, and
// pipeline.StageResult's own documentation says each write replaced the last,
// so nothing here may describe it as the rounds.
//
// # What it refuses
//
// A build with no code host cannot open a pull request, and StageDeps' rule
// for an adapter a body needs is that the body refuses rather than proceeding
// without it, so this fails with an error naming what is missing rather than
// holding for a person. internal/service supplies no provider today, so that
// is the answer every run reaching this stage gets in this build, and
// internal/service's own answer-to-the-end test skips this stage for that one
// run rather than this body softening to let it past.
//
// A refusal from the provider is returned rather than reported, wrapped so
// that the reason the code host did not answer survives. This stage neither
// retries it nor turns it into a finding: a stage that could not reach the
// code host did not run, and saying so is what leaves the run recoverable.
//
// # What it writes, and the one thing it checks about it
//
// pipeline.KeyPullRequest is the run's record of which pull request it is
// answering for, and forge.PullRequest.Number is what every later call
// addresses one by, so the number is what lands there. This stage refuses a
// number that identifies no pull request rather than recording it, because a
// run whose record points at nothing is a run the checks stage cannot address
// and a person cannot follow.
//
// That check is about this stage's own write and not about the provider's
// behaviour. It is what makes recording a number mean the number is usable;
// the GitHub adapter in this module already refuses such an answer, so against
// that adapter this catches nothing, and forge.Provider promises no more than
// that its answer is a pull request.
func PullRequest(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads:  pullRequestReads(),
		Writes: []pipeline.Key{pipeline.KeyPullRequest},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return submitPullRequest(ctx, deps.Forge, in)
			}
		},
	}
}

// pullRequestReads is the stage's read declaration: the run's own facts, and
// what every stage of the run left behind.
//
// The per-stage keys come from pipeline.StageResultKeys over pipeline.Order,
// so this names no stage and counts none. A stage added to the pipeline, or a
// key added to what a stage records, reaches this declaration without an edit
// here, which is what keeps the narration from silently missing a stage.
func pullRequestReads() []pipeline.Key {
	reads := []pipeline.Key{
		pipeline.KeyRun,
		pipeline.KeyBranch,
		pipeline.KeyBase,
		pipeline.KeyIntent,
		pipeline.KeyIntentSupplied,
		pipeline.KeySubmitted,
		pipeline.KeyHead,
		pipeline.KeyPushed,
	}
	for _, stage := range pipeline.Order() {
		reads = append(reads, pipeline.StageResultKeys(stage)...)
	}
	return reads
}

// runFacts is what this stage read out of the run's state: the run's own facts
// and what each stage recorded, in the order a run takes them.
type runFacts struct {
	run       string
	branch    string
	base      string
	intent    string
	supplied  bool
	submitted string
	head      string
	pushed    string
	stages    []pipeline.StageResult
}

// submitPullRequest is the stage body.
func submitPullRequest(ctx context.Context, provider forge.Provider, in pipeline.Input) (pipeline.Output, error) {
	if provider == nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: the %s stage has no code host to talk to, so it cannot create or update a pull request",
			in.Stage)
	}
	facts, err := readRunFacts(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	pr, err := forge.Submit(ctx, provider, forge.OpenSpec{
		Head:  facts.branch,
		Base:  facts.base,
		Title: pullRequestTitle(facts),
		Body:  pullRequestBody(facts),
	})
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: the %s stage could not create or update the pull request for branch %q: %w",
			in.Stage, facts.branch, err)
	}
	if pr.Number <= 0 {
		return pipeline.Output{}, fmt.Errorf(
			"stages: the %s stage was given pull request number %d for branch %q, which identifies "+
				"no pull request, so the run has nothing to record and nothing later can address",
			in.Stage, pr.Number, facts.branch)
	}
	return pipeline.Output{
		Report: pullRequestReport(facts, pr),
		Writes: map[pipeline.Key]graph.Value{
			pipeline.KeyPullRequest: graph.TextValue(strconv.Itoa(pr.Number)),
		},
	}, nil
}

// readRunFacts reads everything the stage narrates. A read this stage did not
// declare, and a recorded report that cannot be decoded, are returned as they
// arrive: by the time the body sees either, the step has already failed.
func readRunFacts(state pipeline.Reader) (runFacts, error) {
	text := func(key pipeline.Key) (string, error) {
		value, err := state.Get(key)
		if err != nil {
			return "", err
		}
		s, _ := value.Text()
		return strings.TrimSpace(s), nil
	}
	var facts runFacts
	for _, read := range []struct {
		key  pipeline.Key
		into *string
	}{
		{pipeline.KeyRun, &facts.run},
		{pipeline.KeyBranch, &facts.branch},
		{pipeline.KeyBase, &facts.base},
		{pipeline.KeyIntent, &facts.intent},
		{pipeline.KeySubmitted, &facts.submitted},
		{pipeline.KeyHead, &facts.head},
		{pipeline.KeyPushed, &facts.pushed},
	} {
		value, err := text(read.key)
		if err != nil {
			return runFacts{}, err
		}
		*read.into = value
	}
	suppliedValue, err := state.Get(pipeline.KeyIntentSupplied)
	if err != nil {
		return runFacts{}, err
	}
	facts.supplied, _ = suppliedValue.Bool()
	for _, stage := range pipeline.Order() {
		result, err := pipeline.ReadStageResult(state, stage)
		if err != nil {
			return runFacts{}, err
		}
		facts.stages = append(facts.stages, result)
	}
	return facts, nil
}

// pullRequestTitle derives the title a pull request is opened with.
//
// It is the first line of the run's intent, and the branch name when the run
// carries no intent, because those are the only two things this stage knows
// that name the change. Nothing here consults the code host: a pull request
// that already exists keeps the title it has, per forge.Submit, so this is
// what a new one is opened with and never what an existing one is renamed to.
//
// The result carries no line break, since the first line is all it takes, and
// it is cut at titleLimit so that intent prose does not become a title nobody
// can read. A run with neither an intent nor a branch yields the empty string,
// which internal/forge refuses before it invokes anything.
func pullRequestTitle(facts runFacts) string {
	first := facts.intent
	if index := strings.IndexAny(first, "\r\n"); index >= 0 {
		first = first[:index]
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return facts.branch
	}
	runes := []rune(first)
	if len(runes) > titleLimit {
		return strings.TrimSpace(string(runes[:titleLimit-3])) + "..."
	}
	return first
}

// pullRequestReport is what the stage reports. Every finding is a note: the
// stage either reached the code host, in which case what it did is
// information, or it did not, in which case it returned an error and nothing
// here was built.
//
// The base is the one thing it compares. A pull request that already existed
// keeps the base a person gave it, and what this run was validated against is
// pipeline.KeyBase, so the two disagreeing means the evidence the run produced
// is about a different merge than the one the pull request describes.
// That is reported rather than held: forge.Submit keeps the existing base
// because a person may have retargeted it deliberately, and holding would stop
// every later run over a decision already made. What the finding has to carry
// is the consequence rather than the mismatch alone, because the mismatch is
// what a reader has to be told the meaning of: the merge that will happen is
// not the merge that was validated, so this run's verdict does not transfer to
// it. The body states which merge the run validated and does not disclose the
// pull request's own base, so this finding is where the disagreement is said.
//
// A run that forwarded no commit is the other fact it reports, on the same
// terms and for the same reason: the pull request then describes a branch this
// run did not update, and a person may have skipped the push stage on purpose,
// so refusing would take a decision that is theirs.
func pullRequestReport(facts runFacts, pr forge.PullRequest) findings.Report {
	found := []findings.Finding{{
		ID:       "pull-request",
		Severity: findings.SeverityInfo,
		Action:   findings.ActionNote,
		Description: fmt.Sprintf(
			"The pull request for branch %s is number %d, and it is %s: %s\n\n"+
				"Its body was written from what this run's stages recorded, and it is replaced "+
				"every time this stage runs.",
			quoteName(facts.branch), pr.Number, pr.State, pr.URL),
	}}
	if pr.Base != facts.base {
		found = append(found, findings.Finding{
			ID:       "pull-request-base",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"This run validated the change against %s, and the pull request merges into %s. "+
					"Nothing this run established is evidence about merging into %s, so this "+
					"run's verdict does not transfer to the merge the pull request describes: "+
					"the merge that will happen is not the merge that was validated, and a "+
					"checks-passed result on this run says nothing about it. This stage does not "+
					"retarget a pull request, because the base may have been changed "+
					"deliberately.",
				quoteName(facts.base), quoteName(pr.Base), quoteName(pr.Base)),
		})
	}
	if facts.pushed == "" {
		found = append(found, findings.Finding{
			ID:       "pull-request-without-a-forwarded-commit",
			Severity: findings.SeverityWarning,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"No stage of this run recorded forwarding a commit to %s, so pull request %d "+
					"describes a branch this run did not update: what the code host carries there "+
					"is whatever was already there. The body says that rather than naming the "+
					"commit this run validated as though it were on the branch. This is reported "+
					"and not refused, because skipping the push stage is a person's decision to "+
					"make.",
				quoteName(facts.branch), pr.Number),
		})
	}
	return findings.Report{
		Summary:  fmt.Sprintf("Pull request %d carries this run's account of the change.", pr.Number),
		Findings: found,
	}
}

// pullRequestBody renders PRD section 5's four questions, in that order, out
// of what the run recorded. Each section is trimmed and the sections are
// joined the same way, so the spacing does not vary with what a section had to
// say.
func pullRequestBody(facts runFacts) string {
	return join(
		bodyPreamble(facts),
		"## What changed\n\n"+whatChanged(facts),
		"## What was checked\n\n"+whatWasChecked(facts),
		"## What the risks are\n\n"+whatTheRisksAre(facts),
		"## What the pipeline had to fix\n\n"+whatWasFixed(facts),
	)
}

// join trims each block and separates them by one blank line, dropping the
// ones that came out empty.
func join(blocks ...string) string {
	kept := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if trimmed := strings.TrimSpace(block); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "\n\n") + "\n"
}

// bodyPreamble says where the body came from, so a reviewer knows it is a
// record rather than a description written after the fact.
func bodyPreamble(facts runFacts) string {
	if facts.run == "" {
		return "This body was written by the assistant delivery gate from what the run's own stages recorded."
	}
	return "This body was written by the assistant delivery gate from what run " + facts.run +
		"'s own stages recorded."
}

// whatChanged renders the change the run validated: where it goes, which
// commits it moved between, and what it was measured against.
//
// What it attests to is conditional on what the run forwarded. A run that
// forwarded a commit is attested as validated at the commit it validated,
// which is the commit the branch then carries. A run that forwarded none says
// so instead and the attestation is not rendered at all: the commit was still
// validated and that is still stated, but a line reading as though it were on
// the branch would be this artifact claiming what the run did not establish.
//
// The opening line answers the same rule for the base. It says what this run
// validated merging into, which is pipeline.KeyBase and is true whatever the
// pull request is targeted at, rather than what the pull request merges into,
// which this run does not know and may be wrong about.
func whatChanged(facts runFacts) string {
	var where strings.Builder
	fmt.Fprintf(&where, "This run validated merging %s into %s.\n\n",
		quoteName(facts.branch), quoteName(facts.base))
	fmt.Fprintf(&where, "- Submitted to the gate: %s\n", quoteCommit(facts.submitted))
	if facts.pushed == "" {
		fmt.Fprintf(&where, "- Forwarded to the branch target: nothing was forwarded. This run "+
			"validated the change at %s, no stage of it recorded forwarding a commit, and the "+
			"branch on the code host is whatever was already there.\n", quoteCommit(facts.head))
	} else {
		fmt.Fprintf(&where, "- Validated at: %s\n", quoteCommit(facts.head))
		fmt.Fprintf(&where, "- Forwarded to the branch target: %s\n", quoteCommit(facts.pushed))
	}
	switch {
	case facts.intent != "" && facts.supplied:
		return join(where.String(),
			"The intent below was supplied, so it is the acceptance criteria this change was held to.",
			blockquote(facts.intent))
	case facts.intent != "":
		return join(where.String(),
			"The intent below was offered as a hint about the change rather than as acceptance "+
				"criteria, so the change was not held to it.",
			blockquote(facts.intent))
	default:
		return join(where.String(),
			"No intent was recorded for this run, so every stage judged the change on what it "+
				"does rather than against stated criteria.")
	}
}

// whatWasChecked renders every stage of the run: what became of it, what it
// said, what it checked, and what it found.
func whatWasChecked(facts runFacts) string {
	sections := make([]string, 0, len(facts.stages))
	for _, result := range facts.stages {
		sections = append(sections, "### "+result.Stage.String(), stageSection(result))
	}
	return join(sections...)
}

// stageSection renders one stage's account of itself.
func stageSection(result pipeline.StageResult) string {
	if !result.Ran {
		return didNotRun(result)
	}
	blocks := []string{ranAs(result), result.Report.Summary}
	if len(result.Report.Tested) > 0 {
		blocks = append(blocks, "Checked:\n\n"+bullets(result.Report.Tested))
	}
	if len(result.Report.Evidence) > 0 {
		evidence := make([]string, len(result.Report.Evidence))
		for i, artifact := range result.Report.Evidence {
			evidence[i] = fmt.Sprintf("%s (`%s`)", artifact.Label, artifact.Path)
		}
		blocks = append(blocks, "Evidence:\n\n"+bullets(evidence))
	}
	if len(result.Report.Findings) == 0 {
		return join(append(blocks, "It reported no findings.")...)
	}
	found := make([]string, len(result.Report.Findings))
	for i, finding := range result.Report.Findings {
		found[i] = renderFinding(finding)
	}
	return join(append(blocks, "Found:\n\n"+strings.Join(found, ""))...)
}

// bullets renders lines as a markdown list.
func bullets(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(line))
	}
	return b.String()
}

// didNotRun says why a stage recorded nothing, which is two different facts. A
// stage the run passed over was decided against and says so; a stage with no
// outcome at all had not been reached when this body was written, and saying
// anything about what it found would be inventing it.
func didNotRun(result pipeline.StageResult) string {
	if result.Outcome == pipeline.OutcomePending {
		return "This stage had not run when this body was written, so nothing here is a claim about it."
	}
	return "This stage did not run: " + string(result.Outcome) + "."
}

// ranAs says what became of a stage that ran. An outcome a person answered is
// stated as such, because "a person let this through" and "nothing was found"
// are different things for a reviewer to be told.
func ranAs(result pipeline.StageResult) string {
	switch result.Outcome {
	case pipeline.OutcomeApproved:
		return "This stage ran, found something a person had to decide, and a person let the run continue."
	case pipeline.OutcomeSkipped:
		return "This stage ran, found something a person had to decide, and a person skipped it."
	case pipeline.OutcomePassed:
		return "This stage ran and nothing it found needed anything done about it."
	case pipeline.OutcomePending:
		return "This stage ran and what became of it was not recorded."
	default:
		return "This stage ran and its outcome was recorded as " + string(result.Outcome) + "."
	}
}

// whatTheRisksAre renders the risk each stage judged, and one line per finding
// that was not a note, which is every finding a person answered or a fixer did
// not clear. The findings themselves are in the section above, so these are
// pointers into it rather than a second copy.
func whatTheRisksAre(facts runFacts) string {
	var b strings.Builder
	var stated, standing int
	for _, result := range facts.stages {
		if !result.Ran {
			continue
		}
		if result.Report.Risk != findings.RiskUnstated {
			stated++
			fmt.Fprintf(&b, "- The %s stage judged the risk %s", result.Stage, result.Report.Risk)
			if rationale := strings.TrimSpace(result.Report.RiskRationale); rationale != "" {
				fmt.Fprintf(&b, ": %s", rationale)
			}
			b.WriteString("\n")
		}
		for _, finding := range result.Report.Findings {
			if finding.Action == findings.ActionNote {
				continue
			}
			standing++
			fmt.Fprintf(&b, "- The %s stage's finding %s is still standing, and its action is %s.\n",
				result.Stage, finding.ID, finding.Action)
		}
	}
	if stated == 0 && standing == 0 {
		b.WriteString("No stage of this run stated a risk level, and every finding any stage " +
			"reported was informational.\n")
	}
	return b.String()
}

// whatWasFixed renders what each stage's fixer changed, which is the summary
// the last fix round of that stage wrote.
//
// It closes by saying what it does not carry. PRD "what passing the gate
// means" asks for how many attempts it took, this build has no count to render,
// and a section that stopped at the summaries would read to a person as the
// whole account of the fixing. Where the count would have to come from is in
// PullRequest's own documentation; what belongs here is that a reader is told
// this is the last summary per stage and not the tally.
func whatWasFixed(facts runFacts) string {
	var b strings.Builder
	var rounds int
	for _, result := range facts.stages {
		summary := strings.TrimSpace(result.Fix)
		if summary == "" {
			continue
		}
		rounds++
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", result.Stage, summary)
	}
	if rounds == 0 {
		b.WriteString("No stage of this run recorded a fix round, so nothing in this change was " +
			"written by the gate.\n\n")
	}
	b.WriteString("This section is the last fix summary each stage recorded, and it does not say " +
		"how many attempts anything took: this build of the gate records no history of the rounds " +
		"a stage went through, so it has no count to state here.\n")
	return b.String()
}

// renderFinding renders one finding as a bullet: what decides who resolves it,
// how bad the stage thought it was, where it is, and what it says.
func renderFinding(finding findings.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s** (%s, %s)", finding.ID, finding.Action, finding.Severity)
	if location := finding.Location.String(); location != "" {
		fmt.Fprintf(&b, " at `%s`", location)
	}
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimSpace(finding.Description), "\n") {
		fmt.Fprintf(&b, "  %s\n", strings.TrimSpace(line))
	}
	return b.String()
}

// blockquote renders text as a markdown block quote, so intent a person wrote
// is set apart from what this stage wrote around it.
func blockquote(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		b.WriteString("> " + strings.TrimSpace(line) + "\n")
	}
	return b.String()
}

// quoteName renders a branch name, and renders an empty one as words rather
// than as empty quotes.
func quoteName(name string) string {
	if name == "" {
		return "an unrecorded branch"
	}
	return "`" + name + "`"
}

// quoteCommit renders a commit, and renders an unrecorded one as words.
func quoteCommit(commit string) string {
	if commit == "" {
		return "not recorded"
	}
	return "`" + commit + "`"
}
