package stages_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The two paths the change under review touches in every test here, and the
// path the change does not touch that a finding may want to reach for.
const (
	totalPath  = "internal/total/total.go"
	readmePath = "README.md"
	callerPath = "internal/report/render.go"
)

// The revision every review report has to name is the run's head, which the
// tests read off the repository they built rather than stating, so nothing
// here can agree with the stage about a commit git does not have.

// A reviewer that reads the change and finds nothing wrong. It is the baseline
// every refusal below is measured against: swapping it in has to make the same
// run pass, or the refusal being demonstrated is not the thing that failed it.
func cleanReview(s *subject) findings.Report {
	return findings.Report{
		Summary:  "One pass over the change. Nothing to report.",
		Revision: s.head,
		Read:     findings.Paths{totalPath, readmePath},
		Traced: []findings.Trace{
			{Path: totalPath, Reason: "the intent asked for the sum to be correct"},
			{Path: readmePath, Reason: "the intent asked for the change to be written down"},
		},
	}
}

// The change under review is measured against the run's own commit and the
// run's own list of paths, so a reviewer cannot move either. This drives both
// halves through the real adapter: a report of the commit the run asked about
// is accepted, and a report of some other commit is refused whole and holds
// the run for a person.
//
// The refusal is internal/findings', and what this establishes is that the
// review stage puts the run's commit into the demand at all. A stage that
// filled the demand in from the reviewer's own answer, or left it out, would
// accept both reports here.
func TestAReviewOfSomeOtherCommitIsNotAReviewOfThisChange(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	right := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, s.start())
	if got := right.outcome(); got != pipeline.OutcomePassed {
		t.Fatalf("a review of the run's own commit came to %s, want passed: %s",
			got, right.summary())
	}

	wrong := cleanReview(s)
	wrong.Revision = s.base
	other := run(t, s, script(standin.Report(wrong)), config.Config{}, s.start())
	if got := other.outcome(); got != pipeline.OutcomeHeld {
		t.Fatalf("a review reporting the wrong commit came to %s, want held", got)
	}
	if !hasFinding(other.report(), "review-not-established") {
		t.Fatalf("a review of the wrong commit was not recorded as a review that established "+
			"nothing: %+v", other.report().Findings)
	}
}

// A finding is a claim about code, and the evidence set is what makes the
// claim checkable. A reviewer that reports a fix-eligible finding about a
// caller it never declared reading has it refused; one that declares reading
// that caller keeps it.
//
// Both directions are driven, because only the pair shows the rule discriminating.
// A stage that read its agent's output as an ordinary stage report rather than
// as a review would let the first case through as fix-eligible, and the run
// would go to a fixer on a claim resting on nothing.
func TestAFindingReachingPastWhatTheReviewDeclaredReadingIsRefused(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	claim := findings.Finding{
		ID:          "caller-sums-twice",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: callerPath, Line: 12},
		Description: "The caller adds the same slice a second time now that Total returns it summed.",
	}

	unsupported := cleanReview(s)
	unsupported.Findings = []findings.Finding{claim}
	refused := run(t, s, script(standin.Report(unsupported)), config.Config{}, s.start())
	if got := refused.outcome(); got != pipeline.OutcomePassed {
		t.Fatalf("a fix finding about a path the review never declared reading came to %s, "+
			"want passed: it rests on nothing, so it may not send the run to a fixer", got)
	}

	supported := unsupported
	supported.Read = findings.Paths{totalPath, readmePath, callerPath}
	kept := run(t, s, script(standin.Report(supported)), config.Config{}, s.start())
	if got := kept.outcome(); got != pipeline.OutcomeFixable {
		t.Fatalf("a fix finding about a path the review did declare reading came to %s, want "+
			"fixable; the refusal above therefore shows nothing", got)
	}
}

// P3: a finding whose action the reviewer did not state, or stated in a word
// this system does not recognize, holds the run for a person. It is not a
// finding the review stage may drop, demote, or send to a fixer.
//
// The reviewer here gives its finding a location inside the change and cites
// nothing outside it, so the evidence rule has nothing to say about it and
// what decides is P3 alone. Each case is measured against the same report
// carrying a stated note, which passes, so the hold is the action's doing.
func TestAnUnclassifiedReviewFindingHoldsTheRunForAPerson(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range []struct {
		name   string
		action findings.Action
		want   pipeline.Outcome
	}{
		{"no action at all", "", pipeline.OutcomeHeld},
		{"an action nobody recognizes", "autofix", pipeline.OutcomeHeld},
		{"a stated note", findings.ActionNote, pipeline.OutcomePassed},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSubject(t)
			report := cleanReview(s)
			report.Findings = []findings.Finding{{
				ID:          "sum-loses-the-last-element",
				Severity:    findings.SeverityWarning,
				Action:      c.action,
				Location:    findings.Location{Path: totalPath, Line: 4},
				Description: "The loop stops one element early.",
			}}
			got := run(t, s, script(standin.Report(report)), config.Config{}, s.start())
			if outcome := got.outcome(); outcome != c.want {
				t.Fatalf("a review finding with %s came to %s, want %s", c.name, outcome, c.want)
			}
		})
	}
}

// The scope lens fires on a path the review did not account for, and it fires
// on the run's own list of paths, so a reviewer cannot silence it by staying
// quiet about a file.
//
// The two cases are the discriminator. Tracing both paths leaves no scope
// note; tracing one leaves exactly one, naming the other. A lens fed from the
// reviewer's own account of what it changed would report nothing in both.
func TestTheScopeLensNotesThePathTheReviewDidNotAccountFor(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	accounted := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, s.start())
	if notes := scopeNotes(accounted.report()); len(notes) != 0 {
		t.Fatalf("a review that accounted for every touched path still drew %d scope note(s): %+v",
			len(notes), notes)
	}

	partial := cleanReview(s)
	partial.Traced = []findings.Trace{{Path: totalPath, Reason: "the intent asked for this"}}
	silent := run(t, s, script(standin.Report(partial)), config.Config{}, s.start())
	notes := scopeNotes(silent.report())
	if len(notes) != 1 {
		t.Fatalf("a review that accounted for one of two touched paths drew %d scope note(s), "+
			"want 1: %+v", len(notes), notes)
	}
	if notes[0].Location.Path != readmePath {
		t.Fatalf("the scope note names %q, want the path the review did not account for, %q",
			notes[0].Location.Path, readmePath)
	}
	if got := silent.outcome(); got != pipeline.OutcomePassed {
		t.Fatalf("a scope note brought the stage to %s; the lens is note-only and may not stop "+
			"a run", got)
	}
}

// A scope observation may never be anything but a note, so a review that
// accounted for nothing at all still passes the stage. The lens turns an
// untraced change into something a person reads, never into something that
// blocks them.
//
// It is asked of a review whose own findings are empty, so what the outcome
// reflects is the lens and nothing else.
func TestAChangeAccountedForNowhereStillPassesTheStage(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	silent := cleanReview(s)
	silent.Traced = nil
	got := run(t, s, script(standin.Report(silent)), config.Config{}, s.start())
	notes := scopeNotes(got.report())
	if len(notes) != 2 {
		t.Fatalf("a review that accounted for neither touched path drew %d scope note(s), want 2: %+v",
			len(notes), notes)
	}
	for _, note := range notes {
		if note.Action != findings.ActionNote {
			t.Fatalf("a scope observation carries action %q, and the lens may report nothing but "+
				"a note", note.Action)
		}
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("a change nothing was traced for came to %s, want passed", outcome)
	}
}

// A run with no recorded intent has nothing for the lens to trace to. The lens
// refuses rather than reporting every path as unexplained, and the stage
// reports the refusal rather than skipping it in silence: a lens that did not
// run and a change every path of which was accounted for both produce no
// per-path note, and only this tells them apart.
func TestTheScopeLensSaysSoWhenTheRunCarriesNoIntentToTraceTo(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	start := s.start()
	start.Intent, start.IntentSupplied = "", false
	got := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, start)

	if notes := scopeNotes(got.report()); len(notes) != 0 {
		t.Fatalf("the lens reported %d per-path note(s) for a run with no intent to trace to: %+v",
			len(notes), notes)
	}
	if len(lensNotApplied(got.report())) != 1 {
		t.Fatalf("a run with no intent reports nothing about the scope lens not having run: %+v",
			got.report().Findings)
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("a run with no intent came to %s at the review stage, want passed: the lens "+
			"not applying may not stop a run", outcome)
	}
}

// The note saying the lens did not run is appended to a report the reviewer
// wrote, so it may not carry an identifier the reviewer could have written
// too. An identifier a finding arrives with is never rewritten, and a report
// with two findings sharing one is refused whole, which would fail the run
// over agent output that every other path in this stage holds for a person.
//
// The reviewer here writes the identifier the note used to carry, on the run
// where the note is produced. The control is the same run without that
// finding, which passes, so what this measures is the collision and not the
// intent-less run.
func TestAReviewerCannotFailTheRunByNamingAFindingTheLensAppends(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	start := s.start()
	start.Intent, start.IntentSupplied = "", false

	clean := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, start)
	if outcome := clean.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("the control run came to %s, want passed; the collision below shows nothing", outcome)
	}

	claiming := cleanReview(s)
	claiming.Findings = []findings.Finding{{
		ID:          "scope-lens-not-applied",
		Severity:    findings.SeverityInfo,
		Action:      findings.ActionNote,
		Location:    findings.Location{Path: totalPath, Line: 4},
		Description: "I am naming this finding whatever I like.",
	}}
	got := run(t, s, script(standin.Report(claiming)), config.Config{}, start)

	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("a reviewer that named its own finding after the lens's note brought the run "+
			"to %s, want passed: agent output may not fail a run this stage would otherwise "+
			"hold", outcome)
	}
	if !hasFinding(got.report(), "scope-lens-not-applied") {
		t.Fatalf("the reviewer's own finding lost the identifier it wrote: %+v", got.report().Findings)
	}
	if len(lensNotApplied(got.report())) != 1 {
		t.Fatalf("the lens's note went missing from a report whose reviewer named a finding "+
			"after it: %+v", got.report().Findings)
	}
}

// An agent that did not come back with a usable review holds the run for a
// person. PRD section 5 makes "the stage could not gather enough evidence" an
// ask finding, and that is what this is: the stage ran, it asked, and it got
// nothing it can report a verdict from.
//
// Each case is a different way the invocation fails, and all of them reach the
// same answer rather than one of them slipping through as a pass.
func TestAnAgentThatDidNotReviewHoldsTheRunRatherThanPassingIt(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		reply func(s *subject) standin.Reply
	}{
		{"the agent reported its own failure", func(*subject) standin.Reply {
			return standin.Failed("I could not read the diff")
		}},
		{"the agent printed no result envelope", func(*subject) standin.Reply {
			return standin.Malformed("I had a look and it seems fine to me")
		}},
		{"the agent answered with prose rather than a report", func(*subject) standin.Reply {
			return standin.Text("Looks good to me!")
		}},
		{"the agent printed more than the adapter will read", func(*subject) standin.Reply {
			return standin.Oversize()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSubject(t)
			got := run(t, s, script(c.reply(s)), config.Config{}, s.start())
			if outcome := got.outcome(); outcome != pipeline.OutcomeHeld {
				t.Fatalf("%s and the stage came to %s, want held: nothing was reviewed, so "+
					"nothing may say this change is good", c.name, outcome)
			}
			if !hasFinding(got.report(), "review-not-established") {
				t.Fatalf("%s was not recorded as a review that established nothing: %+v",
					c.name, got.report().Findings)
			}
		})
	}
}

// An agent whose deadline elapsed did not come back, which is the same fact as
// every case above and reaches the same ask finding. It is asserted apart from
// them because it is the one the run's context can be read as saying something
// about: a guard that returned whenever ctx.Err() was set would take this case
// too, and agents.FailureTimeout is produced under exactly that condition, so
// the ask would be unreachable for it.
//
// The deadline has to elapse while the agent holds rather than while the body
// is still assembling what to ask, so it is derived from a first invocation
// answered at once rather than guessed at: that invocation pays for the same
// four git invocations and the same process start on the same machine under
// the same load. A deadline that elapsed too early reaches a different answer,
// which this fails on rather than passing.
func TestAnAgentWhoseDeadlineElapsedHoldsTheRunRatherThanEndingIt(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	answering := standin.New(t, script(standin.Report(cleanReview(s))))
	measured, measuredIn := reviewBody(t,
		stages.NewStageDeps(agents.NewStageAgent(answering.Runner()), s.home, config.Config{}, nil),
		s.start())
	begun := time.Now()
	if _, err := measured(t.Context(), measuredIn); err != nil {
		t.Fatalf("the invocation measuring what this stage costs ahead of its agent failed, so "+
			"there is nothing here to set a deadline from: %v", err)
	}
	budget := max(6*time.Since(begun), 1500*time.Millisecond)

	agent := standin.New(t, script(standin.Hang()))
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, config.Config{}, nil)
	call, in := reviewBody(t, deps, s.start())

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	out, err := call(ctx, in)

	if err != nil {
		t.Fatalf("an agent that never came back ended the run with %v, want an ask finding: "+
			"the stage ran and asked, and PRD section 5 makes that a person's to resolve", err)
	}
	if !hasFinding(out.Report, "review-not-established") {
		t.Fatalf("an agent that never came back was not recorded as a review that established "+
			"nothing: %+v", out.Report.Findings)
	}
	found := out.Report.Findings[0]
	if found.Action != findings.ActionAsk {
		t.Fatalf("the finding for an agent that never came back carries action %q, want ask",
			found.Action)
	}
	if !strings.Contains(found.Description, string(agents.FailureTimeout)) {
		t.Fatalf("the finding does not say the agent timed out, so the deadline elapsed "+
			"somewhere other than at the agent and this shows nothing: %q", found.Description)
	}
}

// A cancelled run is the other side of that line. Nobody is waiting on the
// answer, so there is no person to hold the run for, and the stage returns the
// error rather than reporting a finding.
//
// The cancellation happens once the reviewer has been asked, which the
// stand-in records before it holds, so what this measures is the body's answer
// to a cancelled agent invocation rather than to a context that was already
// dead when the stage started.
func TestACancelledRunEndsTheStageRatherThanHoldingItForAPerson(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	agent := standin.New(t, script(standin.Hang()))
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, config.Config{}, nil)
	call, in := reviewBody(t, deps, s.start())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type answer struct {
		out pipeline.Output
		err error
	}
	done := make(chan answer, 1)
	go func() {
		out, err := call(ctx, in)
		done <- answer{out, err}
	}()
	for len(agent.Calls()) == 0 {
		select {
		case got := <-done:
			t.Fatalf("the stage answered before the reviewer was ever asked, so cancelling now "+
				"would measure nothing: %+v, %v", got.out.Report, got.err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	got := <-done

	if got.err == nil {
		t.Fatalf("a cancelled run came back with a report rather than the error it is: %+v",
			got.out.Report)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("a cancelled run ended with %v, want an error carrying context.Canceled", got.err)
	}
	if len(got.out.Report.Findings) != 0 {
		t.Fatalf("a cancelled run reported %d finding(s) as well as failing: %+v",
			len(got.out.Report.Findings), got.out.Report.Findings)
	}
}

// A change whose diff git produced more of than this build reads in one piece
// is the same fact as a change too large to put in one prompt, one threshold
// earlier, and it reaches the same ask rather than ending the run. Which cap a
// change crossed is the person's to read; whether they are asked is not.
//
// The bound is internal/vcs's own and is reached by lowering it rather than by
// building a change large enough to cross the shipped one. The control is the
// same change under the shipped bound, which passes, so what this measures is
// the size of the diff and not the change.
func TestAChangeWhoseDiffIsTooLargeToReadHoldsForAPersonRatherThanFailing(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	within := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, s.start())
	if outcome := within.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("the control run came to %s, want passed; the refusal below shows nothing", outcome)
	}

	got := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, s.start(),
		withGit(vcs.WithMaxOutput(256)))
	if calls := got.agent.Calls(); len(calls) != 0 {
		t.Fatalf("the review stage asked an agent %d time(s) about a change it could not read "+
			"the diff of", len(calls))
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomeHeld {
		t.Fatalf("a change whose diff could not be read in one piece came to %s, want held: "+
			"nothing was reviewed, and this build's own limit is not the person's problem to "+
			"be failed over", outcome)
	}
	if !hasFinding(got.report(), "review-change-too-large") {
		t.Fatalf("a change whose diff could not be read was not recorded as too large to "+
			"review: %+v", got.report().Findings)
	}
}

// A body wired with no agent is a defect in this build and not a review that
// came back empty, so it fails the stage rather than putting a question in
// front of a person that they cannot answer.
//
// This is the other side of the line the test above draws. A stage that
// answered every failure with an ask finding would report this one too, and a
// person would be asked to approve past a review that could never have run.
func TestAStageWiredWithNoAgentFailsRatherThanAskingAPerson(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	deps := stages.NewStageDeps(agents.StageAgent{}, s.home, config.Config{}, nil)
	_, err := body(t, deps, s.start())
	if err == nil {
		t.Fatal("a review stage with no agent produced a report rather than failing, so a " +
			"person would be asked about a review that could not have run")
	}
	if !errors.Is(err, agents.ErrNoStageAgent) {
		t.Fatalf("a review stage with no agent failed with %v, want ErrNoStageAgent", err)
	}
}

// A change whose every path the repository excludes from review is not sent to
// an agent at all. The stage reports that it read nothing and says which
// patterns took the change away, rather than reporting a pass it established
// by looking.
//
// That the agent was not invoked is asserted off the wire rather than inferred
// from the report, because a stage that asked and then discarded the answer
// would produce the same report.
func TestAChangeExcludedFromReviewIsNotSentToAnAgent(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	cfg := config.Config{IgnorePatterns: patterns(t, "internal/**", "*.md")}
	got := run(t, s, script(standin.Report(cleanReview(s))), cfg, s.start())

	if calls := got.agent.Calls(); len(calls) != 0 {
		t.Fatalf("the review stage invoked an agent %d time(s) for a change with no reviewable "+
			"path", len(calls))
	}
	if !hasFinding(got.report(), "review-everything-excluded") {
		t.Fatalf("a wholly excluded change was not recorded as excluded: %+v", got.report().Findings)
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("a wholly excluded change came to %s, want passed: there was nothing to "+
			"review, which is not the same as something being wrong", outcome)
	}
}

// A change too large to put in front of a reviewer in one piece is not sent,
// and it holds for a person rather than failing the run: nothing was reviewed,
// splitting the change is a decision, and agents.MaxPromptBytes is the bound
// that decides.
//
// The invocation being refused at the agent instead would fail the stage with
// a message about a prompt, so this checks both that nothing was sent and that
// the person is the one asked.
func TestAChangeTooLargeToReviewHoldsForAPersonRatherThanFailing(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	write(t, s.dir, "internal/total/huge.txt", strings.Repeat("a line of a very large generated file\n", 40000))
	s.head = s.commit(t, "add something too large to review")
	start := s.start()

	got := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, start)
	if calls := got.agent.Calls(); len(calls) != 0 {
		t.Fatalf("the review stage sent a prompt over the limit to an agent %d time(s)", len(calls))
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomeHeld {
		t.Fatalf("a change too large to review came to %s, want held", outcome)
	}
	if !hasFinding(got.report(), "review-change-too-large") {
		t.Fatalf("a change too large to review was not recorded as too large: %+v",
			got.report().Findings)
	}
}

// A rename changed both of its paths, so the review is asked about both. Only
// the new one is in the diff's later commit, so a set built from that alone
// would leave the reviewer unable to support a finding about where the file
// used to be, and unable to be asked whether moving it was in scope.
//
// The prompt is read off the wire, which is where the question actually
// reaches the reviewer, and what is read is the list of paths the evidence
// demand states rather than the prompt as a whole. The whole prompt ends with
// the unified diff, and git names both sides of a rename in it whether or not
// the touched set does, so a search over the prompt text would find the old
// path with the stage doing nothing to put it there.
func TestARenameIsAskedAboutAtBothOfItsPaths(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	// The prose goes back to what the target branch has before it moves, so
	// what git reports is a rename. A move that also rewrites a one-line file
	// is below git's similarity threshold and comes back as a deletion and an
	// addition, which reaches the old path through vcs.FileChange.Path and so
	// would leave the branch under test here unexercised.
	write(t, s.dir, readmePath, baseReadme)
	git(t, s.dir, "mv", readmePath, "GUIDE.md")
	s.head = s.commit(t, "put the guide back as it was and move it")
	if status := git(t, s.dir, "diff", "--name-status", "--find-renames", s.base, s.head); !strings.Contains(status, "R100") {
		t.Fatalf("git does not report this change as a rename, so nothing here asks about "+
			"one:\n%s", status)
	}

	report := cleanReview(s)
	report.Revision = s.head
	got := run(t, s, script(standin.Report(report)), config.Config{}, s.start())
	prompt := got.agent.Call().Prompt

	asked := askedAbout(t, prompt)
	want := map[string]bool{totalPath: true, readmePath: true, "GUIDE.md": true}
	for path := range want {
		if !asked[path] {
			t.Fatalf("the reviewer was not asked about %q, and a rename changed both of its "+
				"paths: it was asked about %v", path, asked)
		}
	}
	if len(asked) != len(want) {
		t.Fatalf("the reviewer was asked about %v, want exactly %d path(s): the change's own "+
			"two plus the path the rename moved away from", asked, len(want))
	}
}

// P5: the re-review of a fix round's work reads that work as author code, and
// it is given the previous findings and the fix summary as claims to check
// rather than as evidence that anything was resolved.
//
// This drives the real loop rather than describing it: the first review
// reports a fix-eligible finding, internal/pipeline routes it to a fixer, the
// fixer writes a summary, and the stage runs again. What the second reviewer
// was asked is read off the wire.
//
// The first prompt is the control. It carries neither the summary nor the
// earlier finding, because there was no round before it, so an assertion that
// finds them in the second prompt is finding something the stage put there for
// this execution and not boilerplate every prompt carries.
func TestAReReviewIsGivenTheRoundsWorkAsClaimsAndNotAsEvidence(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P5)

	const (
		claimed  = "I rewrote the loop so it visits the last element."
		reported = "The loop stops one element early, so the last value is never added."
	)
	s := newSubject(t)

	first := cleanReview(s)
	first.Findings = []findings.Finding{{
		ID:          "sum-loses-the-last-element",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Location:    findings.Location{Path: totalPath, Line: 4},
		Description: reported,
	}}
	got := run(t, s, standin.Script{Steps: []standin.Step{
		{Match: standin.Match{PromptContains: claimed}, Times: standin.Always,
			Reply: standin.Report(cleanReview(s))},
		{Times: standin.Always, Reply: standin.Report(first)},
	}}, config.Config{}, s.start(), withRounds(config.FixRounds{Review: 1}, summarizingFixer(claimed)))

	calls := got.agent.Calls()
	if len(calls) != 2 {
		t.Fatalf("the review stage ran %d time(s), want 2: one review, one re-review after the "+
			"fix round", len(calls))
	}
	if strings.Contains(calls[0].Prompt, claimed) || strings.Contains(calls[0].Prompt, reported) {
		t.Fatal("the first review was already told about a fix round, so finding that in the " +
			"second proves nothing")
	}
	rereview := calls[1].Prompt
	for _, want := range []string{claimed, reported} {
		if !strings.Contains(rereview, want) {
			t.Fatalf("the re-review was not given %q, so it cannot check the claim", want)
		}
	}
	for _, want := range []string{"author code", "not evidence", "claims"} {
		if !strings.Contains(rereview, want) {
			t.Fatalf("the re-review is not told to read the round's work as %q", want)
		}
	}
	if got.outcome() != pipeline.OutcomePassed {
		t.Fatalf("the run came to %s after its fix round, want passed", got.outcome())
	}
}

// The reviewer is told the rule its answer is checked against, in the words of
// the packages that do the checking. A reviewer held to a rule nobody stated
// is a trap rather than a check, and the two rules that refuse a review's
// findings are internal/findings' evidence demand and internal/scope's lens.
//
// The assertion is that the stage's prompt carries those texts, which is what
// makes them the same words on both sides: neither is restated here or in the
// stage.
func TestTheReviewerIsToldTheRulesItsAnswerIsCheckedAgainst(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	start := s.start()
	got := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, start)
	prompt := got.agent.Call().Prompt

	touched := strings.Split(git(t, s.dir, "diff", "--name-only", s.base, s.head), "\n")
	evidence, err := (findings.Demand{Revision: s.head, Touched: touched}).Guidance()
	if err != nil {
		t.Fatalf("building the evidence guidance: %v", err)
	}
	if !strings.Contains(prompt, evidence) {
		t.Fatal("the reviewer was not told the evidence rule its findings are bound to")
	}
	if !strings.Contains(prompt, start.Intent) {
		t.Fatal("the reviewer was not told what the change set out to do")
	}
	if !strings.Contains(prompt, "Scope: every changed line should trace to the stated intent") {
		t.Fatal("the reviewer was not asked the scope lens's question, which its answer is measured against")
	}
}

// The reviewer reads the run's own isolated copy and not whatever tree the
// service happens to be standing in. The working directory is where an agent
// looks when it opens a file, so a body that ran the agent somewhere else
// would review some other code and report about this one.
//
// The path is compared resolved, because a platform that resolves a temporary
// directory through a symbolic link gives the process a path spelled
// differently from the one the invocation asked for.
func TestTheReviewerRunsInTheRunsOwnIsolatedCopy(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	got := run(t, s, script(standin.Report(cleanReview(s))), config.Config{}, s.start())
	want, err := filepath.EvalSymlinks(s.home.Worktree(s.repository, s.run))
	if err != nil {
		t.Fatalf("resolving the isolated copy: %v", err)
	}
	ran, err := filepath.EvalSymlinks(got.agent.Call().Dir)
	if err != nil {
		t.Fatalf("resolving where the reviewer ran: %v", err)
	}
	if ran != want {
		t.Fatalf("the reviewer ran in %s, want the run's own isolated copy at %s", ran, want)
	}
}

// A path-scoped review rule reaches the reviewer with its scope, and a rule
// scoped to nothing this change touches does not reach it at all. A reviewer
// told a rule without its scope applies it to files nobody scoped it to.
func TestAPathScopedReviewRuleReachesTheReviewerWithItsScope(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	cfg := config.Config{ReviewPathRules: []config.PathRule{
		{Paths: patterns(t, "internal/**"), Guidance: "State the contract, not the name."},
		{Paths: patterns(t, "docs/**"), Guidance: "This rule is scoped to nothing here."},
	}}
	got := run(t, s, script(standin.Report(cleanReview(s))), cfg, s.start())
	prompt := got.agent.Call().Prompt

	if !strings.Contains(prompt, "State the contract, not the name.") {
		t.Fatal("a review rule scoped to a path this change touches did not reach the reviewer")
	}
	if !strings.Contains(prompt, "internal/**") {
		t.Fatal("a review rule reached the reviewer without the scope it applies to")
	}
	if strings.Contains(prompt, "This rule is scoped to nothing here.") {
		t.Fatal("a review rule scoped to nothing this change touches reached the reviewer anyway")
	}
}

// askedAbout returns the set of paths the prompt asked the reviewer about.
//
// It reads them out of the prompt, which is the generated interface the agent
// is handed and the one place the touched set becomes visible to it:
// findings.Demand.Guidance closes its section with the touched paths as a
// bullet list, and this reads that list back. Reading the prompt rather than
// searching it is what keeps the assertion from being answered by the unified
// diff further down, which names paths the touched set never admitted.
func askedAbout(t *testing.T, prompt string) map[string]bool {
	t.Helper()
	const anchor = "The change touches these paths."
	at := strings.Index(prompt, anchor)
	if at < 0 {
		t.Fatal("the prompt carries no list of the paths the change touches, so there is " +
			"nothing here to read the reviewer's question out of")
	}
	out := make(map[string]bool)
	for _, line := range strings.Split(prompt[at:], "\n")[1:] {
		if !strings.HasPrefix(line, "  - ") {
			break
		}
		out[strings.TrimPrefix(line, "  - ")] = true
	}
	if len(out) == 0 {
		t.Fatal("the prompt's list of the paths the change touches is empty")
	}
	return out
}

// scopeNotes returns the report's scope observations: the notes the lens
// produced, told from the reviewer's own findings by the description
// internal/scope writes.
func scopeNotes(report findings.Report) []findings.Finding {
	var out []findings.Finding
	for _, f := range report.Findings {
		if strings.Contains(f.Description, "the review traced none of it to the") {
			out = append(out, f)
		}
	}
	return out
}

// lensNotApplied returns the report's notes saying the scope lens did not run
// over this change.
//
// They are told from the reviewer's own findings by the sentence the stage
// writes rather than by an identifier, because the note carries none: one
// appended to an agent's report would collide with a reviewer that wrote the
// same string, and a report with a duplicate identifier is refused whole.
func lensNotApplied(report findings.Report) []findings.Finding {
	var out []findings.Finding
	for _, f := range report.Findings {
		if strings.Contains(f.Description, "The scope lens did not run over this change") {
			out = append(out, f)
		}
	}
	return out
}

// hasFinding reports whether the report carries a finding with this identifier.
func hasFinding(report findings.Report, id string) bool {
	for _, f := range report.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}

// patterns builds a config.PatternSet from the patterns a repository would
// have written, through the package that owns what a pattern matches.
func patterns(t *testing.T, list ...string) config.PatternSet {
	t.Helper()
	set := make(config.PatternSet, 0, len(list))
	for _, p := range list {
		pattern, err := config.ParsePattern(p)
		if err != nil {
			t.Fatalf("parsing the pattern %q: %v", p, err)
		}
		set = append(set, pattern)
	}
	return set
}

// summarizingFixer is a fixer that changes nothing and reports that it did
// something. It stands in for the fix round in the re-review test, where what
// matters is what the round leaves behind for the next reviewer rather than
// what it edited: the summary crosses to the next round through
// internal/pipeline, which is the path under test.
func summarizingFixer(summary string) pipeline.Fixer {
	return pipeline.Fixer{
		NewBody: func() pipeline.FixBody {
			return func(context.Context, pipeline.FixInput) (pipeline.FixOutput, error) {
				return pipeline.FixOutput{Summary: summary}, nil
			}
		},
	}
}

// baseReadme is the prose the change under review rewrites, as the target
// branch has it.
const baseReadme = "Total adds up numbers.\n"

// baseTotal is the file the change under review edits.
const baseTotal = `package total

// Total sums the values.
func Total(values []int) int {
	sum := 0
	for _, v := range values {
		sum += v
	}
	return sum
}
`

// subject is the run a review test drives: a home, the isolated copy at the
// path this run's own keys derive, and the commits the change spans.
//
// The copy is built with raw git for the reason helpers_test.go's git states,
// and it is placed where internal/home says this run's copy lives rather than
// anywhere convenient, so what the stage opens is what the product would open.
type subject struct {
	home       *home.Home
	repository string
	run        string
	dir        string
	base       string
	head       string
}

// newSubject builds the change under review: one commit on the target branch,
// then one commit on the branch under validation touching two paths.
func newSubject(t *testing.T) *subject {
	t.Helper()
	h := newHome(t)
	s := &subject{home: h, repository: "repo-1", run: "run-1"}
	s.dir = h.Worktree(s.repository, s.run)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatalf("making the isolated copy: %v", err)
	}
	git(t, s.dir, "init", "--quiet", "-b", "main", ".")
	write(t, s.dir, totalPath, baseTotal)
	write(t, s.dir, readmePath, baseReadme)
	s.base = s.commit(t, "the code as it stood")

	git(t, s.dir, "checkout", "--quiet", "-b", "topic")
	write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // start empty", 1))
	write(t, s.dir, readmePath, "Total sums every value it is given.\n")
	s.head = s.commit(t, "sum the values and say so")
	return s
}

// commit records everything in the working copy and returns the commit.
func (s *subject) commit(t *testing.T, message string) string {
	t.Helper()
	git(t, s.dir, "add", "-A")
	git(t, s.dir, "commit", "--quiet", "-m", message)
	return git(t, s.dir, "rev-parse", "HEAD")
}

// start is the run this subject is validated by.
func (s *subject) start() pipeline.Start {
	return pipeline.Start{
		Repository:     s.repository,
		Run:            s.run,
		Branch:         "topic",
		Base:           "main",
		Submitted:      s.head,
		Intent:         "make Total sum every value it is given, and say so in the README",
		IntentSupplied: true,
	}
}

// script is the one-step script for a review that is answered the same way
// however it is asked.
func script(reply standin.Reply) standin.Script {
	return standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: reply}}}
}

// setup is what a review test varies about the run it drives: the pipeline it
// is executed by, and the options every repository the stage opens is opened
// with.
type setup struct {
	options pipeline.Options
	git     []vcs.Option
}

// option configures the run a review test drives.
type option func(*setup)

// withRounds gives the review stage automatic fix rounds and the fixer that
// serves them.
func withRounds(rounds config.FixRounds, fixer pipeline.Fixer) option {
	return func(s *setup) { s.options.Rounds, s.options.Fixer = rounds, fixer }
}

// withGit opens the run's isolated copy with these options, which is how a
// test reaches internal/vcs's own bounds without building a change large
// enough to cross the shipped ones.
func withGit(opts ...vcs.Option) option {
	return func(s *setup) { s.git = append(s.git, opts...) }
}

// reviewed is what a driven run came to: the executed result, and the stand-in
// the review stage reached.
type reviewed struct {
	t      *testing.T
	result graph.Result
	agent  *standin.Agent
}

// outcome is what became of the review stage.
func (r reviewed) outcome() pipeline.Outcome {
	return pipeline.StageOutcome(r.result.State, pipeline.StageReview)
}

// report is what the review stage recorded, read back the way anything else
// reads it.
func (r reviewed) report() findings.Report {
	r.t.Helper()
	report, err := pipeline.StageReport(r.result.State, pipeline.StageReview)
	if err != nil {
		r.t.Fatalf("reading the review stage's report: %v", err)
	}
	return report
}

// summary is the recorded report's summary, for a failure message that says
// what the stage thought it was doing.
func (r reviewed) summary() string { return r.report().Summary }

// run drives a whole pipeline whose review stage is the real one, over the
// subject's own isolated copy and against a scripted agent.
//
// It runs the pipeline rather than the body alone, because the stage's answer
// is only half of what it establishes: whether a review holds a run, sends it
// to a fixer, or lets it past is internal/pipeline's reading of the report,
// and a body-level assertion would be this test's reading instead.
func run(t *testing.T, s *subject, sc standin.Script, cfg config.Config, start pipeline.Start, opts ...option) reviewed {
	t.Helper()
	agent := standin.New(t, sc)

	cfgured := setup{options: pipeline.Options{Rounds: config.FixRounds{}, Budget: config.DefaultRunBudget}}
	for _, opt := range opts {
		opt(&cfgured)
	}
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, nil, cfgured.git...)

	all := pipeline.ConstantStages("nothing to report")
	all.Review = stages.Review(deps)
	options := cfgured.options
	options.Stages = all
	p, err := pipeline.New(options)
	if err != nil {
		t.Fatalf("building a pipeline around the review stage: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	state, err := p.NewState(start)
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	result, err := exec.Run(t.Context(), s.run, state)
	if err != nil {
		t.Fatalf("running the pipeline: %v", err)
	}
	return reviewed{t: t, result: result, agent: agent}
}

// body runs the review stage's body alone, for the failures that are the
// stage refusing to run rather than a run coming to an outcome.
func body(t *testing.T, deps stages.StageDeps, start pipeline.Start) (pipeline.Output, error) {
	t.Helper()
	run, in := reviewBody(t, deps, start)
	return run(t.Context(), in)
}

// reviewBody prepares the review stage's body and the input a stage node would
// hand it, without running either. The two answers that are a function of what
// became of the run's context need a context of the caller's own, and one of
// them needs the call made from a goroutine the test is not standing in, so
// everything that can fail the test is done here first.
func reviewBody(t *testing.T, deps stages.StageDeps, start pipeline.Start) (pipeline.Body, pipeline.Input) {
	t.Helper()
	impl := stages.Review(deps)
	all := pipeline.ConstantStages("nothing to report")
	all.Review = impl
	p, err := pipeline.New(pipeline.Options{Stages: all, Budget: config.DefaultRunBudget})
	if err != nil {
		t.Fatalf("building a pipeline around the review stage: %v", err)
	}
	state, err := p.NewState(start)
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody(), pipeline.Input{
		Stage: pipeline.StageReview,
		State: stateReader{allowed: allowed, state: state},
	}
}
