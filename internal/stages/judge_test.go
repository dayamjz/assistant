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
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// The files the judge-rule tests plant. The test file and the Makefile are
// judge paths under the default list; the source file is not.
const (
	judgedTest     = "total_test.go"
	judgedMakefile = "Makefile"
	plainSource    = "total.go"
)

// nothingTouched is what the scripted fixer says in every judge-rule fix test:
// that it changed nothing. The hold must not depend on that account, so every
// test here runs with a fixer whose reply is false about what was committed.
const nothingTouched = "I left every file as it was; nothing here needed changing"

// newJudgeFixRun builds a fix run whose change already carries a test file and
// a Makefile, so a round can edit what judges it.
func newJudgeFixRun(t *testing.T) *fixRun {
	t.Helper()
	run := newFixRun(t, standin.Text(nothingTouched))
	write(t, run.copy, judgedTest, "package subject\n\nfunc TestTotal(t *testing.T) {}\n")
	write(t, run.copy, judgedMakefile, "test:\n\tgo test ./...\n")
	git(t, run.copy, "add", "-A")
	git(t, run.copy, "commit", "--quiet", "-m", "the change carries its tests")
	run.head = git(t, run.copy, "rev-parse", "HEAD")
	return run
}

// walk drives a pipeline whose fixer is the real one over this run's copy and
// whose stage under test reports one fix-eligible finding and then, once a
// round has run, nothing. Every other stage passes. So the stage's own verdict
// on the execution verifying the round is a pass, and anything holding it
// there came from the round.
//
// The stage's verdict is stated rather than computed, because what is under
// test is the fix path and internal/pipeline's reading of what it raised, not
// a stage body: the round's commit is real, made by vcs through stages.Fix, and
// the paths it touched are read back from git.
func (r *fixRun) walk(t *testing.T, stage pipeline.Stage) graph.Result {
	t.Helper()
	all := pipeline.ConstantStages("nothing to report")
	executions := 0
	verdict := pipeline.Implementation{NewBody: func() pipeline.Body {
		return func(context.Context, pipeline.Input) (pipeline.Output, error) {
			executions++
			if executions == 1 {
				return pipeline.Output{Report: findings.Report{
					Summary:  "the check failed",
					Findings: oneFixFinding(),
				}}, nil
			}
			return pipeline.Output{Report: findings.Report{Summary: "the check passes now"}}, nil
		}
	}}
	switch stage {
	case pipeline.StageReview:
		all.Review = verdict
	case pipeline.StageTest:
		all.Test = verdict
	case pipeline.StageLint:
		all.Lint = verdict
	case pipeline.StageRebase:
		all.Rebase = verdict
	case pipeline.StageCI:
		all.CI = verdict
	default:
		t.Fatalf("the %s stage takes no fix rounds", stage)
	}
	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Fixer:  stages.Fix(r.deps),
		Rounds: config.FixRounds{Rebase: 1, Review: 1, Test: 1, Lint: 1, Checks: 1},
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	state, err := p.NewState(pipeline.Start{
		Repository: r.repositoryID, Run: r.runID, Branch: "topic", Base: "main", Submitted: r.head,
	})
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	result, err := exec.Run(t.Context(), r.runID, state)
	if err != nil {
		t.Fatalf("running the pipeline: %v", err)
	}
	if executions != 2 {
		t.Fatalf("the %s stage ran %d times, want 2: once to report and once to verify the round",
			stage, executions)
	}
	return result
}

// Section 13's judge-paths row, first half: a fix round after review whose
// commit changes and deletes judge paths holds the stage with an ask naming
// each path, what happened to it, and the commit - while the scripted fixer
// says it touched nothing. The commit decides, not the fixer's account.
//
// The second half is the control: the same round committing only a file no
// judge pattern matches does not hold, so the first half is not passing on a
// hold something else raised. Both run the real fixer over the scripted
// stand-in, the real commit, and internal/pipeline's real reading of what the
// round raised.
//
// P5 is what this guards from the fix side: a commit the gate wrote after
// review is not trusted to have left the checks alone because it says so, and
// a person sees each check it touched.
func TestAFixRoundAfterReviewThatEditsTheJudgeHoldsWhateverTheFixerSays(t *testing.T) {
	principles.Cite(t, principles.P5)

	for _, stage := range []pipeline.Stage{pipeline.StageTest, pipeline.StageLint, pipeline.StageCI} {
		t.Run(stage.String(), func(t *testing.T) {
			run := newJudgeFixRun(t)
			// The stand-in cannot edit a working copy. What the round's agent
			// would have changed is put there here, and what is under test is
			// what the round does with it.
			write(t, run.copy, judgedTest, "package subject\n\nfunc TestTotal(t *testing.T) { t.Skip() }\n")
			if err := os.Remove(filepath.Join(run.copy, judgedMakefile)); err != nil {
				t.Fatal(err)
			}
			write(t, run.copy, plainSource, "package subject\n\n// fixed\n")

			result := run.walk(t, stage)

			if result.Position != stage.HoldNode() {
				t.Fatalf("a round that edited the judge left the run %s at %q, want it held at %s",
					result.Status, result.Position, stage.HoldNode())
			}
			committed := git(t, run.copy, "rev-parse", "HEAD")
			if committed == run.head {
				t.Fatal("the round committed nothing, so this establishes nothing about a commit")
			}
			report, err := pipeline.StageReport(result.State, stage)
			if err != nil {
				t.Fatalf("reading the %s report: %v", stage, err)
			}
			want := map[string]string{
				judgedTest:     "modified " + judgedTest,
				judgedMakefile: "deleted " + judgedMakefile,
			}
			for _, found := range report.Findings {
				what, judged := want[found.Location.Path]
				if !judged {
					t.Errorf("the %s stage holds over %q, which is no judge path: %+v",
						stage, found.Location.Path, found)
					continue
				}
				if found.Action != findings.ActionAsk {
					t.Errorf("the finding for %s is %s, want ask", found.Location.Path, found.Action)
				}
				for _, part := range []string{what, committed, stage.String()} {
					if !strings.Contains(found.Description, part) {
						t.Errorf("the finding for %s does not say %q: %s",
							found.Location.Path, part, found.Description)
					}
				}
				delete(want, found.Location.Path)
			}
			if len(want) > 0 {
				t.Fatalf("the round's commit touched judge paths the hold does not name: %v", want)
			}
			// The fixer was told, and told something false back. The hold above
			// did not ask it.
			call := run.agent.Call()
			if !strings.Contains(call.Prompt, "not yours to change") ||
				!strings.Contains(call.Prompt, "*_test.go") {
				t.Errorf("the fixer was not told the judge paths are not its to change:\n%s", call.Prompt)
			}
		})
	}

	t.Run("control", func(t *testing.T) {
		run := newJudgeFixRun(t)
		write(t, run.copy, plainSource, "package subject\n\n// fixed\n")
		result := run.walk(t, pipeline.StageTest)
		if result.Status != graph.StatusCompleted {
			t.Fatalf("a round committing no judge path left the run %s at %q, want completed",
				result.Status, result.Position)
		}
		if git(t, run.copy, "rev-parse", "HEAD") == run.head {
			t.Fatal("the round committed nothing, so the control shows nothing about a commit")
		}
	})
}

// The rule is about commits nobody reviews. Review's own fix round is re-read
// by the review stage and rebase's precedes review, so a round of either that
// edits a judge path raises nothing here, and the fixer is not told the judge
// paths are off limits there: a review finding about a test is the reviewer's
// to send to a round.
func TestAFixRoundBeforeTheReviewIsDoneRaisesNoJudgeHold(t *testing.T) {
	for _, stage := range []pipeline.Stage{pipeline.StageReview, pipeline.StageRebase} {
		t.Run(stage.String(), func(t *testing.T) {
			run := newJudgeFixRun(t)
			write(t, run.copy, judgedTest, "package subject\n\nfunc TestTotal(t *testing.T) { t.Skip() }\n")
			result := run.walk(t, stage)
			if result.Status != graph.StatusCompleted {
				t.Fatalf("a %s round editing a judge path left the run %s at %q, want completed",
					stage, result.Status, result.Position)
			}
			if strings.Contains(run.agent.Call().Prompt, "not yours to change") {
				t.Errorf("the %s round's fixer was told the judge paths are off limits", stage)
			}
		})
	}
}

// An empty judge list is a trusted repository turning the rule off, and the
// same round that holds above then does not.
func TestAnEmptyJudgeListTurnsTheFixHoldOff(t *testing.T) {
	run := newJudgeFixRun(t)
	off := config.Defaults()
	off.JudgePaths = nil
	run.deps = stages.NewFixDeps(
		func(ctx context.Context, _ string) (agents.Fixer, error) {
			return agents.OpenFixer(ctx, run.agent.Runner(), "")
		},
		run.home, off, nil, redact.New(),
	)
	write(t, run.copy, judgedTest, "package subject\n\nfunc TestTotal(t *testing.T) { t.Skip() }\n")
	result := run.walk(t, pipeline.StageTest)
	if result.Status != graph.StatusCompleted {
		t.Fatalf("with the rule off, the run is %s at %q, want completed", result.Status, result.Position)
	}
}

// judgeSubject is the review subject with a test file on the target branch, so
// the change can edit, delete, or move it.
const judgeTestPath = "internal/total/total_test.go"

// judgeTestBody is long enough that moving it unchanged is a rename to git
// rather than a deletion and an addition.
const judgeTestBody = `package total

import "testing"

// TestTotal checks every value is summed, including the last one.
func TestTotal(t *testing.T) {
	if got := Total([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Total = %d, want 6", got)
	}
	if got := Total(nil); got != 0 {
		t.Fatalf("Total(nil) = %d, want 0", got)
	}
}
`

// newJudgeSubject builds a review subject whose target branch carries a test
// file, and whose change then does to it what edit says.
func newJudgeSubject(t *testing.T, edit func(dir string)) *subject {
	t.Helper()
	s := newSubject(t)
	git(t, s.dir, "checkout", "--quiet", "main")
	write(t, s.dir, judgeTestPath, judgeTestBody)
	s.base = s.commit(t, "the code had a test")
	git(t, s.dir, "checkout", "--quiet", "topic")
	git(t, s.dir, "rebase", "--quiet", "main")
	edit(s.dir)
	s.head = s.commit(t, "the change does something to the test")
	return s
}

// judgeReview is a clean review of a judge subject: it read everything,
// traced everything to the intent, and found nothing, so whatever the report
// carries about a judge path is the lens's.
func judgeReview(s *subject, read ...string) findings.Report {
	report := cleanReview(s)
	report.Read = append(report.Read, read...)
	for _, path := range read {
		report.Traced = append(report.Traced, findings.Trace{Path: path, Reason: "the intent covers it"})
	}
	return report
}

// pushedJudgeConfig resolves the configuration a run would, from a pushed
// copy that tries to turn the judge rule off and to keep the tests out of
// review. Neither may work: judge_paths is trusted-only, and the lens reads
// the change unfiltered by ignore patterns, which a pushed branch may set.
func pushedJudgeConfig(t *testing.T) config.Config {
	t.Helper()
	pushed, err := config.Parse(config.OriginPushed, []byte(
		`{"judge_paths": [], "ignore_patterns": ["internal/total/*_test.go"]}`))
	if err != nil {
		t.Fatalf("parsing the pushed copy: %v", err)
	}
	res, err := config.ResolveRun(config.Absent(config.OriginGlobal), config.Absent(config.OriginTrusted), pushed)
	if err != nil {
		t.Fatalf("resolving the run's configuration: %v", err)
	}
	return res.Config
}

// Section 13's judge-paths row, second half: a change that deletes a judge
// file, or moves one to a path no judge pattern matches, holds at review with
// an ask naming it, even when the reviewer found nothing. The configuration is
// resolved from a pushed copy that sets judge_paths to nothing and excludes
// the test from review, and it has no effect on either: the trusted copy, here
// absent, governs with the default list.
func TestAChangeRemovingAJudgeFileHoldsAtReview(t *testing.T) {
	principles.Cite(t, principles.P7)
	cfg := pushedJudgeConfig(t)

	for _, c := range []struct {
		name  string
		edit  func(dir string)
		where string
		what  string
	}{
		{"deleted", func(dir string) {
			if err := os.Remove(filepath.Join(dir, judgeTestPath)); err != nil {
				t.Fatal(err)
			}
		}, judgeTestPath, "deleted " + judgeTestPath},
		{"moved out of the judge paths", func(dir string) {
			git(t, dir, "mv", judgeTestPath, "internal/total/cases.go")
		}, judgeTestPath, "renamed " + judgeTestPath + " to internal/total/cases.go"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newJudgeSubject(t, c.edit)
			got := run(t, s, script(standin.Report(judgeReview(s, "internal/total/cases.go"))), cfg, s.start())
			if outcome := got.outcome(); outcome != pipeline.OutcomeHeld {
				t.Fatalf("a change that %s a judge file came to %s at review, want held: %+v",
					c.name, outcome, got.report().Findings)
			}
			asks := 0
			for _, found := range got.report().Findings {
				if found.Action != findings.ActionAsk {
					continue
				}
				asks++
				if found.Location.Path != c.where || !strings.Contains(found.Description, c.what) {
					t.Errorf("the review holds over %+v, want an ask at %s saying %q", found, c.where, c.what)
				}
			}
			if asks != 1 {
				t.Fatalf("the review carries %d asks, want the one for the judge file: %+v",
					asks, got.report().Findings)
			}
		})
	}
}

// An edit to a judge file is reported as a note and holds nothing, and the
// reviewer is asked whether it makes a check weaker. The prompt is read off
// the wire, and the control is the clean review of a change touching no judge
// path, whose prompt carries no such question.
func TestAChangeEditingAJudgeFileIsANoteTheReviewerIsAskedAbout(t *testing.T) {
	cfg := config.Defaults()
	s := newJudgeSubject(t, func(dir string) {
		write(t, dir, judgeTestPath, strings.Replace(judgeTestBody, "got != 6", "got < 0", 1))
	})
	got := run(t, s, script(standin.Report(judgeReview(s, judgeTestPath))), cfg, s.start())
	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("a change editing a judge file came to %s at review, want passed: %+v",
			outcome, got.report().Findings)
	}
	notes := 0
	for _, found := range got.report().Findings {
		if found.Location.Path != judgeTestPath {
			continue
		}
		notes++
		if found.Action != findings.ActionNote || !strings.Contains(found.Description, "modified "+judgeTestPath) {
			t.Errorf("the judge finding is %+v, want a note saying the file was modified", found)
		}
	}
	if notes != 1 {
		t.Fatalf("the review carries %d findings at %s, want the one judge note: %+v",
			notes, judgeTestPath, got.report().Findings)
	}
	prompt := got.agent.Call().Prompt
	if !strings.Contains(prompt, "makes a check weaker") || !strings.Contains(prompt, "modified "+judgeTestPath) {
		t.Fatalf("the reviewer was not asked about the judge edit:\n%s", prompt)
	}

	plain := newSubject(t)
	control := run(t, plain, script(standin.Report(cleanReview(plain))), cfg, plain.start())
	if strings.Contains(control.agent.Call().Prompt, "makes a check weaker") {
		t.Fatal("a change touching no judge path was asked about judge paths, so the question " +
			"above says nothing about the edit")
	}
	if control.outcome() != pipeline.OutcomePassed {
		t.Fatalf("the control came to %s, want passed", control.outcome())
	}
}
