package stages_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestAFixRoundCommitsWhatItChangedAndReportsTheNewHead is the fixer doing its
// job.
//
// The commit is what makes a round mean anything to the stages after it: every
// one of them reads a commit, and a working copy holding uncommitted edits is
// not one. pipeline.KeyHead is how the run learns which commit that is, and a
// round that changed files without writing it would leave every later stage
// validating the commit from before the fix.
func TestAFixRoundCommitsWhatItChangedAndReportsTheNewHead(t *testing.T) {
	run := newFixRun(t, standin.Text("tighten the bound in total.go"))

	// The stand-in prints and exits; it cannot edit a working copy. What an
	// agent would have changed is put in the copy here, and what is under test
	// is what the round does with it.
	write(t, run.copy, "fixed.go", "package subject\n\n// the fix\n")

	out := run.apply(t, pipeline.StageLint, oneFixFinding())

	head, written := out.Writes[pipeline.KeyHead]
	if !written {
		t.Fatalf("the round changed a file and wrote no head, so nothing after it validates the "+
			"fix: %+v", out)
	}
	committed, _ := head.Text()
	if committed == run.head {
		t.Fatalf("the head written is the commit from before the round, %s", committed)
	}
	if got := git(t, run.copy, "rev-parse", "HEAD"); got != committed {
		t.Fatalf("the copy stands at %s and the round reported %s", got, committed)
	}
	if !strings.Contains(git(t, run.copy, "show", "--stat", "--format=", committed), "fixed.go") {
		t.Fatalf("the commit does not carry the file the round changed:\n%s",
			git(t, run.copy, "show", "--stat", "--format=", committed))
	}
	// The subject is the configured template rendered with the agent's own
	// account, so a person reading the history sees what the round claimed.
	if subject := git(t, run.copy, "log", "-1", "--format=%s", committed); !strings.Contains(
		subject, "tighten the bound") {
		t.Fatalf("the commit subject is %q, and the round records what the fixer reported", subject)
	}
	if !strings.Contains(out.Summary, "tighten the bound") {
		t.Fatalf("the summary is %q, and it carries what the fixer reported", out.Summary)
	}
}

// TestAFixCommitSubjectEndsOnAWordAndTheBodyKeepsTheAccount is the commit a
// fix round writes onto the branch, read back from git.
//
// A subject is the fixer's first line, and three things have gone wrong with
// it on real runs: it was cut mid-word, it nested the agent's own
// conventional-commit prefix inside the configured one, and nothing kept what
// the cut dropped. Each case below is a reply the scripted stand-in prints, put
// through the real adapter and the real fix body, and the subject and body are
// what git recorded. The first case is the positive control: a short plain
// line is the subject as it stands and adds no body, so a body in every other
// case is the account being kept rather than one written regardless.
func TestAFixCommitSubjectEndsOnAWordAndTheBodyKeepsTheAccount(t *testing.T) {
	long := "tighten the bound in total.go so a loop halts at the limit, instead of running past it"
	unbroken := "retry-the-fetch-against-https://example.invalid/a/very/long/path/that/never/ends x"
	for _, c := range []struct {
		name    string
		reply   string
		subject string
		body    string
		// absent, when set, is text the commit may not carry anywhere.
		absent string
	}{{
		name:    "a short plain line stands as it is",
		reply:   "tighten the bound in total.go",
		subject: "fix: tighten the bound in total.go",
		body:    "",
	}, {
		name:    "a line longer than the bound ends on a word",
		reply:   long,
		subject: "fix: tighten the bound in total.go so a loop halts at the limit...",
		body:    long,
	}, {
		name:    "the agent's own prefix is not nested in the configured one",
		reply:   "docs(agents): narrow the grant comments to the tools",
		subject: "fix: narrow the grant comments to the tools",
		body:    "docs(agents): narrow the grant comments to the tools",
	}, {
		name:    "a prefix without a scope is removed the same way",
		reply:   "Fix: guard the empty list in total.go",
		subject: "fix: guard the empty list in total.go",
		body:    "Fix: guard the empty list in total.go",
	}, {
		// A reply of no bytes at all never reaches the fix body: the adapter
		// refuses an envelope with no result as a failed invocation. What
		// reaches it empty is a reply of whitespace, or of a prefix alone.
		name:    "an empty reply is said to be one",
		reply:   " \n\t\n",
		subject: "fix: the lint stage's fixer reported nothing about what it did",
		body:    "",
	}, {
		name:    "a reply that is only a prefix is empty past it",
		reply:   "chore:",
		subject: "fix: the lint stage's fixer reported nothing about what it did",
		body:    "chore:",
	}, {
		name:    "a line with no word to end on names the stage instead",
		reply:   unbroken,
		subject: "fix: resolve lint findings",
		body:    unbroken,
	}, {
		name:    "everything after the first line is the body",
		reply:   "refactor: guard the empty list\n\nThe loop in total.go read past the end.\n# kept, not a comment\n",
		subject: "fix: guard the empty list",
		body:    "refactor: guard the empty list\n\nThe loop in total.go read past the end.\n# kept, not a comment",
	}, {
		name:    "the body is redacted as the subject is",
		reply:   "guard the empty list\n\nfetched https://someone:hunter2@example.invalid/subject.git first",
		subject: "fix: guard the empty list",
		body:    "guard the empty list\n\nfetched " + redact.New().Redact("https://someone:hunter2@example.invalid/subject.git") + " first",
		absent:  "hunter2",
	}, {
		name:    "a character that could hide text in the body is shown instead",
		reply:   "guard the empty list\n\nsee \u202etxt.go\x07 for why",
		subject: "fix: guard the empty list",
		body:    "guard the empty list\n\nsee \ufffdtxt.go\ufffd for why",
	}, {
		name:    "a body past its bound is cut after a whole line and says so",
		reply:   "guard the empty list\n" + strings.Repeat("a line of the account\n", 1000),
		subject: "fix: guard the empty list",
		body: "guard the empty list\n" + strings.TrimSuffix(strings.Repeat("a line of the account\n", 371), "\n") +
			"\n\n[cut: the fixer's reply was 22020 bytes, and a fix commit's body records at most 8192]",
	}} {
		t.Run(c.name, func(t *testing.T) {
			run := newFixRun(t, standin.Text(c.reply))
			write(t, run.copy, "fixed.go", "package subject\n\n// the fix\n")

			out := run.apply(t, pipeline.StageLint, oneFixFinding())

			head, written := out.Writes[pipeline.KeyHead]
			if !written {
				t.Fatalf("the round changed a file and wrote no head: %+v", out)
			}
			committed, _ := head.Text()
			if got := git(t, run.copy, "log", "-1", "--format=%s", committed); got != c.subject {
				t.Errorf("the subject is %q, want %q", got, c.subject)
			}
			if got := strings.TrimSpace(git(t, run.copy, "log", "-1", "--format=%b", committed)); got != c.body {
				t.Errorf("the body is %q, want %q", got, c.body)
			}
			if c.absent != "" {
				if message := git(t, run.copy, "log", "-1", "--format=%B", committed); strings.Contains(message, c.absent) {
					t.Errorf("the commit carries %q, which redaction removes:\n%s", c.absent, message)
				}
			}
			if want := strings.TrimPrefix(c.subject, "fix: "); out.Summary != want {
				t.Errorf("the summary run state records is %q, and the subject was rendered from %q",
					out.Summary, want)
			}
		})
	}
}

// TestAFixCommitSubjectFollowsTheConfiguredTemplate is commit.fix_message
// respected: the template decides the subject's form, the agent's own prefix
// is still not nested inside it, and a subject the template cannot carry falls
// back to one this package owns without losing the agent's account, which the
// body keeps.
func TestAFixCommitSubjectFollowsTheConfiguredTemplate(t *testing.T) {
	for _, c := range []struct {
		name     string
		template string
		reply    string
		subject  string
		body     string
	}{{
		name:     "a template of the operator's own takes the summary",
		template: "chore(fixer): {summary}",
		reply:    "docs(agents): narrow the grant comments",
		subject:  "chore(fixer): narrow the grant comments",
		body:     "docs(agents): narrow the grant comments",
	}, {
		name:     "a summary the template has no room for falls back and keeps the account",
		template: "fix: {summary} [by the run's fixer, not reviewed yet]",
		reply:    "tighten the bound in total.go so a loop halts",
		subject:  "fix: resolve lint findings [by the run's fixer, not reviewed yet]",
		body:     "tighten the bound in total.go so a loop halts",
	}} {
		t.Run(c.name, func(t *testing.T) {
			run := newFixRun(t, standin.Text(c.reply))
			run.deps.Config.CommitFixMessage = c.template
			write(t, run.copy, "fixed.go", "package subject\n\n// the fix\n")

			out := run.apply(t, pipeline.StageLint, oneFixFinding())

			head, written := out.Writes[pipeline.KeyHead]
			if !written {
				t.Fatalf("the round changed a file and wrote no head: %+v", out)
			}
			committed, _ := head.Text()
			if got := git(t, run.copy, "log", "-1", "--format=%s", committed); got != c.subject {
				t.Errorf("the subject is %q, want %q", got, c.subject)
			}
			if got := strings.TrimSpace(git(t, run.copy, "log", "-1", "--format=%b", committed)); got != c.body {
				t.Errorf("the body is %q, want %q", got, c.body)
			}
		})
	}
}

// TestAFixRoundThatChangedNothingSaysSoAndWritesNoHead is what the graph's
// convergence bound rests on.
//
// A round whose agent could not fix anything is an ordinary outcome, not a
// failure. It has to leave state as it was, or the loop cannot tell a round
// that made progress from one that did not and runs to its round limit every
// time.
func TestAFixRoundThatChangedNothingSaysSoAndWritesNoHead(t *testing.T) {
	run := newFixRun(t, standin.Text("could not work out what to change"))

	out := run.apply(t, pipeline.StageLint, oneFixFinding())

	if _, written := out.Writes[pipeline.KeyHead]; written {
		t.Fatalf("a round that changed no file wrote a head, so the convergence bound cannot see "+
			"that nothing happened: %+v", out.Writes)
	}
	if !strings.Contains(out.Summary, "changed no file") {
		t.Fatalf("the summary is %q, and a round that changed nothing has to say so", out.Summary)
	}
	if got := git(t, run.copy, "rev-parse", "HEAD"); got != run.head {
		t.Fatalf("the copy moved from %s to %s without anything being committed", run.head, got)
	}
}

// TestAFixRoundIsAskedForTheFindingsTheStageReported keeps the fixer working
// from the stage's own words.
//
// A fixer told this package's paraphrase of a finding would be fixing the
// paraphrase. What the prompt carries is asserted against what the stage
// reported, through the prompt the real adapter put on the agent's standard
// input.
func TestAFixRoundIsAskedForTheFindingsTheStageReported(t *testing.T) {
	run := newFixRun(t, standin.Text("done"))
	reported := oneFixFinding()

	run.apply(t, pipeline.StageLint, reported)

	call := run.agent.Call()
	if !strings.Contains(call.Prompt, reported[0].Description) {
		t.Fatalf("the fixer was not given what the stage reported.\nprompt:\n%s", call.Prompt)
	}
	if !strings.Contains(call.Prompt, "lint") {
		t.Fatalf("the fixer was not told which stage reported it.\nprompt:\n%s", call.Prompt)
	}
	// A fixer that was also asked to judge its own work would be the role
	// split PRD principle P4 exists to prevent, collapsed into one prompt.
	if strings.Contains(strings.ToLower(call.Prompt), "review the change") {
		t.Fatalf("the fixer was asked to review, and reviewing is the other role.\nprompt:\n%s",
			call.Prompt)
	}
}

// TestTheFixPathCannotReachARunnerFromWhatAStageBodyHolds is P4 at this seam.
//
// A stage body gets a StageDeps and a fix body gets a FixDeps, and the two are
// separate types for one reason: a body that could reach a fixer could fix
// what it is about to report on. This asserts the separation the way
// internal/agents/route asserts P4 - by what a caller can reach - rather than
// by one assertion about one field.
func TestTheFixPathCannotReachARunnerFromWhatAStageBodyHolds(t *testing.T) {
	principles.Cite(t, principles.P4)
	var deps stages.StageDeps
	if _, reachable := any(deps).(interface {
		Fixer(context.Context, string) (agents.Fixer, error)
	}); reachable {
		t.Fatal("a stage body's dependencies expose a fixer, so a stage can fix what it reports on")
	}
	// The positive control: FixDeps really does carry one, so a walk that
	// stopped looking at anything fails here rather than reporting a
	// separation it no longer checks.
	if (stages.FixDeps{}).Fixer != nil {
		t.Fatal("the zero FixDeps carries a fixer, so this control establishes nothing")
	}
	wired := stages.NewFixDeps(
		func(context.Context, string) (agents.Fixer, error) { return nil, nil },
		nil, config.Defaults(), nil, redact.New(),
	)
	if wired.Fixer == nil {
		t.Fatal("NewFixDeps did not carry the fixer it was given, so the fix path has no agent " +
			"route and this test would pass against a build where nothing can fix")
	}
}

// oneFixFinding is a stage's fix-eligible finding, in the shape a stage
// reports one.
func oneFixFinding() []findings.Finding {
	return []findings.Finding{{
		ID:          "lint-failed",
		Severity:    findings.SeverityError,
		Action:      findings.ActionFix,
		Description: "the configured lint command exited 1 and named total.go",
		Location:    findings.Location{Path: "total.go", Line: 12},
	}}
}

// fixRun is one fix round against a real isolated copy and a real agent
// adapter over the scripted stand-in.
type fixRun struct {
	home         *home.Home
	agent        *standin.Agent
	deps         stages.FixDeps
	repositoryID string
	runID        string
	copy         string
	head         string
}

// newFixRun builds the copy a round works in and the dependencies a fix body
// is given.
//
// The fixer is the adapter's own, opened over the scripted stand-in through
// agents.OpenFixer. Nothing here implements agents.Fixer itself: a double
// stating typed values directly is how this repository once shipped a guard
// that could never fire, and the fix path's whole job is what the real adapter
// does with what an agent printed.
func newFixRun(t *testing.T, reply standin.Reply) *fixRun {
	t.Helper()
	run := &fixRun{
		home:         newHome(t),
		repositoryID: "repository-1",
		runID:        "run-1",
	}
	run.agent = standin.New(t, standin.Script{
		Steps: []standin.Step{{Times: standin.Always, Reply: reply}},
	})

	source := t.TempDir()
	git(t, source, "init", "--quiet", "-b", "main")
	// The round's commit is made by the product, under whatever identity the
	// environment provides - vcs.CommitAll's contract - so the subject carries
	// one in its own configuration the way a person's clone does. The test
	// helper's environment only covers the commands the test itself runs, and
	// a machine with no global identity (CI among them) fails the product's
	// commit without this.
	git(t, source, "config", "user.name", "test")
	git(t, source, "config", "user.email", "test@example.invalid")
	write(t, source, "total.go", "package subject\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "--quiet", "-m", "the change under validation")

	run.copy = run.home.Worktree(run.repositoryID, run.runID)
	if err := os.MkdirAll(filepath.Dir(run.copy), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(run.copy), err)
	}
	git(t, source, "worktree", "add", "--quiet", "--detach", run.copy)
	run.head = git(t, run.copy, "rev-parse", "HEAD")

	run.deps = stages.NewFixDeps(
		func(ctx context.Context, _ string) (agents.Fixer, error) {
			return agents.OpenFixer(ctx, run.agent.Runner(), "")
		},
		run.home,
		config.Defaults(),
		nil,
		redact.New(),
	)
	return run
}

// apply runs one fix round through the same read restriction the fix node
// applies, so a key the fixer did not declare is refused here as it would be
// there.
func (r *fixRun) apply(t *testing.T, stage pipeline.Stage, list []findings.Finding) pipeline.FixOutput {
	t.Helper()
	fixer := stages.Fix(r.deps)
	allowed := make(map[pipeline.Key]bool, len(fixer.Reads))
	for _, key := range fixer.Reads {
		allowed[key] = true
	}
	out, err := fixer.NewBody()(t.Context(), pipeline.FixInput{
		Stage:    stage,
		Findings: list,
		State: declaredReader{allowed: allowed, state: map[pipeline.Key]graph.Value{
			pipeline.KeyRepository: graph.TextValue(r.repositoryID),
			pipeline.KeyRun:        graph.TextValue(r.runID),
			pipeline.KeyBranch:     graph.TextValue("topic"),
			pipeline.KeyHead:       graph.TextValue(r.head),
			pipeline.KeyIntent:     graph.TextValue("tighten the bound"),
		}},
	})
	if err != nil {
		t.Fatalf("running a fix round: %v", err)
	}
	return out
}
