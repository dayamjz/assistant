package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The fix path: what a fix round is given, what it does, and why its
// dependencies are a type of their own.
//
// PRD principle P4 makes reviewing and fixing separate roles with separate
// memory, and internal/agents keeps that structural rather than remembered: a
// StageDeps carries an agents.StageAgent, which has no Runner to hand over and
// so cannot open a fixer session. That is the whole reason a stage body cannot
// fix.
//
// A fix body does need one. Putting it on StageDeps would hand every stage
// body a route to a fixer through a field it never reads, and P4 would be gone
// with every type assertion in the repository still green:
// internal/agents/route walks what a caller can reach from exported fields and
// exported method results, transitively, and that is exactly the route it
// exists to catch. So the fix path has dependencies of its own, reachable from
// nothing a stage body holds.

// FixerFor opens the fixer role for one run. internal/runs implements it, and
// what it hands back is that run's durable fixer session.
//
// It is a function rather than the service itself so that this package gets
// the one operation a fix round needs and no route to the rest of a run's
// lifecycle. A fix body cannot pass, fail, or terminate the run it is fixing.
type FixerFor func(ctx context.Context, runID string) (agents.Fixer, error)

// FixDeps is what a fix body reaches the world through. It is the fix path's
// StageDeps, and it is a separate type for the reason this file opens with.
type FixDeps struct {
	// Fixer opens the run's fixer role. It is the only agent route here, and
	// it is a fixer rather than a runner, so nothing on the fix path can
	// review.
	Fixer FixerFor
	// Home is the root the run's isolated copy lives under. A fix body reaches
	// that copy through Copy, on the same terms a stage body does.
	Home *home.Home
	// Config is the run's resolved configuration. The fix path reads one thing
	// from it, CommitFixMessage, which is the subject a fix commit is recorded
	// under.
	Config config.Config
	// Requires is what the run's fixer path needs of the agent adapter, which
	// internal/runs answers with Service.FixerRequires. It is carried here
	// rather than passed beside this struct so that one value is everything
	// the fixer is built from.
	Requires []agents.Capability
	// git are the options every repository opened through Copy is opened with,
	// unexported on the same terms as StageDeps.git.
	git []vcs.Option
	// redact removes credentials from text on its way into anything this path
	// persists or reports, which is the fix summary: it is written into run
	// state and into a commit subject, and an agent that echoed a remote URL
	// with a credential in it would put one in both.
	redact redact.Redactor
}

// NewFixDeps returns the dependencies a fix body is given.
func NewFixDeps(fixer FixerFor, h *home.Home, cfg config.Config, requires []agents.Capability,
	redactor redact.Redactor, git ...vcs.Option) FixDeps {
	return FixDeps{
		Fixer: fixer, Home: h, Config: cfg, Requires: requires, redact: redactor, git: git,
	}
}

// Copy opens the isolated copy this run works in. It is StageDeps.Copy's
// answer for the fix path, and it goes through the same helper so the two
// cannot come to disagree about where a run's copy is.
func (d FixDeps) Copy(ctx context.Context, repositoryID, runID string) (*vcs.Repository, error) {
	return openRunCopy(ctx, d.Home, d.git, repositoryID, runID)
}

// Fix is the fixer this build wires: an agent asked to resolve one stage's
// fix-eligible findings in the run's isolated copy, whose work is committed and
// reported back as a new head.
//
// # What a round does
//
// It reads the findings the stage reported, asks the run's fixer to resolve
// them in the copy, and commits whatever changed as one commit. The commit is
// what makes the round mean anything to the stages after it: every one of them
// reads a commit, and a working copy with uncommitted edits is not one.
//
// pipeline.KeyHead is the write that carries it, and it is the write that makes
// the graph's convergence bound meaningful: a round that changed nothing leaves
// state as it was, and the loop ends rather than asking again forever.
//
// # A round that changes nothing
//
// It is reported, not failed. An agent that could not fix what it was asked
// about is an ordinary outcome of a fix loop, and the summary says so; the
// stage then re-runs, finds what it found before, and the loop converges on
// its own. Failing here would turn a fix nobody could make into a run that
// errored, which reads as a defect in the pipeline rather than as the state of
// the change.
//
// # What it is not asked to do
//
// It is never asked to review. The invocation asks for the agent's own words
// about what it changed, and internal/agents refuses a review shape on a fixer
// in both of its modes. Whether the fix worked is the stage's next execution,
// which is P4's separation doing its job: the role that made the change does
// not get to say whether it is good.
//
// # The residual gaps
//
//   - What the agent actually did is its own account. Nothing here checks that
//     the summary describes the diff, and the commit records the summary as its
//     subject, so a misleading summary becomes a misleading commit message. What
//     is checked is the change itself, by the stage that re-runs.
//   - A round that commits work the agent did not mean to include is possible:
//     the commit takes every change in the copy, including anything a previous
//     failed round left behind. The copy is the run's own and nothing else
//     writes to it, which bounds that to this run's own history.
func Fix(deps FixDeps) pipeline.Fixer {
	return pipeline.Fixer{
		Reads: []pipeline.Key{
			pipeline.KeyRepository,
			pipeline.KeyRun,
			pipeline.KeyBranch,
			pipeline.KeyHead,
			pipeline.KeyIntent,
		},
		Writes:   []pipeline.Key{pipeline.KeyHead},
		Requires: deps.Requires,
		NewBody: func() pipeline.FixBody {
			return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
				return applyFixRound(ctx, deps, in)
			}
		},
	}
}

// applyFixRound is one round: ask the agent, commit what changed, report it.
func applyFixRound(ctx context.Context, deps FixDeps, in pipeline.FixInput) (pipeline.FixOutput, error) {
	facts, err := readFixState(in.State)
	if err != nil {
		return pipeline.FixOutput{}, err
	}
	if len(in.Findings) == 0 {
		// The pipeline routes a round only for a stage that reported
		// fix-eligible findings, so this is unreachable through it. It is
		// answered rather than assumed away because the alternative is an
		// agent asked to fix an empty list, which would be an invocation with
		// nothing to do and an answer nobody could check.
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: the %s stage's fix round was given no findings to resolve", in.Stage)
	}
	if deps.Fixer == nil {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: no fixer is wired in this build, so the %d finding(s) the %s stage reported "+
				"cannot be applied", len(in.Findings), in.Stage)
	}

	copied, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.FixOutput{}, err
	}
	before, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: reading where the copy of run %s stands before its %s fix round: %w",
			facts.run, in.Stage, err)
	}

	fixer, err := deps.Fixer(ctx, facts.run)
	if err != nil {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: opening the fixer of run %s for its %s fix round: %w", facts.run, in.Stage, err)
	}

	prompt := fixPrompt(facts, in)
	if len(prompt) > agents.MaxPromptBytes {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: asking for the %s stage's fixes would take a prompt of %d bytes, and one "+
				"invocation may carry %d", in.Stage, len(prompt), agents.MaxPromptBytes)
	}
	result, err := fixer.Apply(ctx, agents.Invocation{
		Prompt: prompt,
		Shape:  agents.ShapeText,
		Dir:    copied.Path(),
	})
	if err != nil {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: the %s stage's fix round failed: %w", in.Stage, err)
	}

	summary := fixSummary(deps.redact, result.Text, in.Stage)
	subject, err := deps.Config.RenderFixMessage(summary)
	if err != nil {
		// The template and the summary together decide this, and the summary
		// is the agent's text. A round whose subject cannot be rendered still
		// changed the copy, so it is recorded under a subject this package
		// owns rather than dropped.
		subject, err = deps.Config.RenderFixMessage(fmt.Sprintf("resolve %s findings", in.Stage))
		if err != nil {
			return pipeline.FixOutput{}, fmt.Errorf(
				"stages: recording the %s stage's fix round: %w", in.Stage, err)
		}
	}

	committed, err := copied.CommitAll(ctx, subject)
	if errors.Is(err, vcs.ErrNothingToCommit) {
		return pipeline.FixOutput{Summary: nothingChanged(in.Stage, summary)}, nil
	}
	if err != nil {
		return pipeline.FixOutput{}, fmt.Errorf(
			"stages: committing what the %s stage's fix round changed: %w", in.Stage, err)
	}
	if committed == before {
		// Belt and braces: CommitAll reports ErrNothingToCommit for this, and
		// a commit identical to the one before it would otherwise be reported
		// as progress the convergence bound could not see.
		return pipeline.FixOutput{Summary: nothingChanged(in.Stage, summary)}, nil
	}

	return pipeline.FixOutput{
		Summary: summary,
		Writes:  map[pipeline.Key]graph.Value{pipeline.KeyHead: graph.TextValue(committed)},
	}, nil
}

// nothingChanged is the summary for a round that left the copy as it was. It
// carries what the agent said, because that is the account of why nothing
// changed, and says plainly that nothing did.
func nothingChanged(stage pipeline.Stage, summary string) string {
	return fmt.Sprintf("The %s stage's fix round changed no file, so the change stands as it was. "+
		"What the fixer reported: %s", stage, summary)
}

// fixSummary is the agent's account of the round, redacted and bounded.
//
// It is redacted because it is persisted twice over - into run state and into
// a commit subject - and an agent that echoed a remote URL carrying a
// credential would put one in both. It is bounded because it becomes a commit
// subject, and config.RenderFixMessage refuses one that is too long; cutting it
// here means the round reports something rather than failing over the length of
// an agent's prose.
//
// An agent that said nothing gets a sentence saying so rather than an empty
// summary, because the summary is what the next round and the re-review read as
// the account of this one.
func fixSummary(redactor redact.Redactor, text string, stage pipeline.Stage) string {
	summary := strings.TrimSpace(redactor.Redact(text))
	if line, _, found := strings.Cut(summary, "\n"); found {
		summary = strings.TrimSpace(line)
	}
	if summary == "" {
		return fmt.Sprintf("the %s stage's fixer reported nothing about what it did", stage)
	}
	if limit := maxFixSummaryRunes; len([]rune(summary)) > limit {
		summary = strings.TrimSpace(string([]rune(summary)[:limit])) + "..."
	}
	return summary
}

// maxFixSummaryRunes bounds the summary so that the rendered commit subject
// stays inside config.MaxCommitSubjectRunes with room for the template around
// it. config owns the real limit and refuses what exceeds it; this is what
// keeps an ordinary round from reaching that refusal.
const maxFixSummaryRunes = 60

// fixFacts is what the run's state says about the change a round is fixing.
type fixFacts struct {
	repository string
	run        string
	branch     string
	head       string
	intent     string
}

// readFixState reads what a fix round needs from the run.
func readFixState(state pipeline.Reader) (fixFacts, error) {
	var f fixFacts
	for _, field := range []struct {
		key    pipeline.Key
		target *string
	}{
		{pipeline.KeyRepository, &f.repository},
		{pipeline.KeyRun, &f.run},
		{pipeline.KeyBranch, &f.branch},
		{pipeline.KeyHead, &f.head},
		{pipeline.KeyIntent, &f.intent},
	} {
		value, err := state.Get(field.key)
		if err != nil {
			return fixFacts{}, err
		}
		*field.target, _ = value.Text()
	}
	return f, nil
}

// fixPrompt assembles what the fixer is asked.
//
// It is one string rather than a template, for the reason reviewPrompt is: what
// reaches the agent is what a reader of this function sees. The order is what
// the fixer is, what it is working on, what the change was for, what the
// previous round claimed, what it must resolve, and what it must not do.
//
// Every finding is rendered with what the stage said about it and nothing
// added. A fixer told more than the stage reported would be fixing this
// package's paraphrase of a finding rather than the finding.
func fixPrompt(f fixFacts, in pipeline.FixInput) string {
	var b strings.Builder
	section := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimRight(text, "\n"))
	}

	section("You are fixing a change in this repository. You are not reviewing it: another " +
		"reviewer, with no memory of this conversation, checks your work afterwards.")
	section(fmt.Sprintf("The change is on branch %s, at commit %s. The %s stage reported the "+
		"findings below against it.", f.branch, f.head, in.Stage))
	if strings.TrimSpace(f.intent) != "" {
		section("What the change set out to do:\n" + f.intent)
	}
	if strings.TrimSpace(in.Previous) != "" {
		section("A previous fix round of this stage reported:\n" + in.Previous +
			"\n\nThe findings below are what the stage reported after that round, so treat that " +
			"account as a claim rather than as something already done.")
	}
	section("Resolve these findings:\n" + renderFindings(in.Findings))
	section("Edit the files in this working copy and nothing else. Do not commit: what you " +
		"change is committed for you. Do not push, and do not change git configuration or " +
		"history.\n\nWhen you are done, reply with one short line saying what you changed. " +
		"That line becomes the commit subject, so write it as one: say what changed, not that " +
		"you were asked to change it. If you could not fix something, say which and why, and " +
		"leave those files alone rather than guessing.")
	return b.String()
}

// renderFindings lists the findings a round must resolve, each as the stage
// reported it.
func renderFindings(list []findings.Finding) string {
	var b strings.Builder
	for i, finding := range list {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, finding.Severity, finding.Description)
		if where := finding.Location.String(); where != "" {
			fmt.Fprintf(&b, "\n   where: %s", where)
		}
		if len(finding.Cites) > 0 {
			fmt.Fprintf(&b, "\n   cites: %s", strings.Join(finding.Cites, ", "))
		}
	}
	return b.String()
}
