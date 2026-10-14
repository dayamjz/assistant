package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// answerAbout appends one decision-model answer for run about path, the way
// the review stage's recorder would, and returns its sequence.
func answerAbout(t *testing.T, s *Service, run, path string) int64 {
	t.Helper()
	a, err := s.store.AppendDecisionAnswer(t.Context(), store.DecisionAnswer{
		Run: run, Stage: "review", Check: "reviewer-directed-text", Path: path,
		Model: "jev-1.13.0", Probability: store.Known(0.9), Cutoff: 0.3, Finding: "note",
	})
	if err != nil {
		t.Fatalf("appending an answer: %v", err)
	}
	return a.Seq
}

// followed returns what followed each of run's answers, by answer sequence.
func followed(t *testing.T, s *Service, run string) map[int64][]store.DecisionFollowUp {
	t.Helper()
	rows, err := s.store.DecisionFollowUps(t.Context(), run)
	if err != nil {
		t.Fatalf("reading follow-ups: %v", err)
	}
	out := map[int64][]store.DecisionFollowUp{}
	for _, r := range rows {
		out[r.Answer] = append(out[r.Answer], r)
	}
	return out
}

// A run executed through the service's wrapped stages records every stage
// execution's start and end, records a later stage's finding at a path as
// what followed the answer about it, and does not record a finding as
// following an answer the same execution produced. The hold it stops at is
// read back from the run's real checkpoint, and a person's answer there is
// recorded as what followed the answer about the held path.
//
// The answers about b.go and t.go are the controls: nothing touched b.go, and
// t.go's answer was recorded by the execution that reported the finding.
func TestWhatARunDidNextIsRecordedBesideItsExecution(t *testing.T) {
	s := openDecisionService(t, nil, judgetest.New(t, judgetest.Always(judgetest.Answer(0.1))))
	ctx := t.Context()
	const run = "run-1"
	aSeq := answerAbout(t, s, run, "a.go")
	bSeq := answerAbout(t, s, run, "b.go")
	zSeq := answerAbout(t, s, run, "z.go")
	var tSeq int64

	at := func(path string, action findings.Action) findings.Finding {
		return findings.Finding{Severity: findings.SeverityInfo, Action: action,
			Location: findings.Location{Path: path}, Description: "about " + path}
	}
	st := pipeline.ConstantStages("nothing to report")
	st.Lint = pipeline.Constant("lint", at("a.go", findings.ActionNote))
	st.Test = pipeline.Implementation{NewBody: func() pipeline.Body {
		return func(context.Context, pipeline.Input) (pipeline.Output, error) {
			tSeq = answerAbout(t, s, run, "t.go")
			return pipeline.Output{Report: findings.Report{Summary: "test",
				Findings: []findings.Finding{at("t.go", findings.ActionNote)}}}, nil
		}
	}}
	st.Push = pipeline.Constant("push", at("z.go", findings.ActionAsk))
	p, err := pipeline.New(pipeline.Options{Stages: s.observeStages(st), Budget: 100})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	exec, err := p.Executor(s.checkpoints)
	if err != nil {
		t.Fatalf("building the executor: %v", err)
	}
	state, err := p.NewState(pipeline.Start{Repository: "repo-1", Run: run, Branch: "topic", Base: "main",
		Submitted: strings.Repeat("a", 40)})
	if err != nil {
		t.Fatalf("building the state: %v", err)
	}
	result, err := exec.Run(ctx, run, state)
	if err != nil || result.Status != graph.StatusHalted {
		t.Fatalf("the run came to %s (%v), want a hold", result.Status, err)
	}

	executions, err := s.store.StageExecutions(ctx, run)
	if err != nil {
		t.Fatalf("reading executions: %v", err)
	}
	var ran []string
	for _, e := range executions {
		ran = append(ran, e.Stage)
		if e.Ended.Before(e.Started) {
			t.Fatalf("an execution ended before it started: %+v", e)
		}
	}
	if got := strings.Join(ran, ","); got != "intent,rebase,review,test,document,lint,push" {
		t.Fatalf("the executions recorded are %s", got)
	}

	after := followed(t, s, run)
	if f := after[aSeq]; len(f) != 1 || f[0].Kind != store.FollowUpFinding || f[0].Stage != "lint" || f[0].Detail != "note" {
		t.Fatalf("the lint finding at a.go was recorded as %+v", f)
	}
	if len(after[bSeq]) != 0 || len(after[tSeq]) != 0 || tSeq == 0 {
		t.Fatalf("an answer nothing later followed was recorded as followed: %+v", after)
	}
	if f := after[zSeq]; len(f) != 1 || f[0].Kind != store.FollowUpFinding || f[0].Detail != "ask" {
		t.Fatalf("the held finding at z.go was recorded as %+v", f)
	}

	stage, paths, mark := s.heldPaths(ctx, run)
	if stage != "push" || len(paths) != 1 || paths[0] != "z.go" || mark < tSeq {
		t.Fatalf("the hold reads back as stage %q at %v with mark %d", stage, paths, mark)
	}
	s.recordAnswered(run, stage, "approved", paths, mark)
	answered := followed(t, s, run)[zSeq]
	if len(answered) != 2 || answered[1].Kind != store.FollowUpAnswer || answered[1].Detail != "approved" {
		t.Fatalf("the person's answer at z.go was recorded as %+v", answered)
	}
}

// A fix round's commit is recorded as what followed the answers about the
// paths it touched, read from the commit itself rather than from what the
// fixer said; an answer about a path the commit did not touch is the control.
func TestAFixRoundsCommitIsRecordedAsWhatFollowedItsPaths(t *testing.T) {
	s := openDecisionService(t, nil, judgetest.New(t, judgetest.Always(judgetest.Answer(0.1))))
	ctx := t.Context()
	dir := t.TempDir()
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
			"-c", "commit.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn("init", "-q")
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn("add", "-A")
	gitIn("commit", "-q", "-m", "one")
	before := gitIn("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n\nvar y = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn("commit", "-q", "-am", "two")
	after := gitIn("rev-parse", "HEAD")

	const run = "run-1"
	aSeq := answerAbout(t, s, run, "a.go")
	bSeq := answerAbout(t, s, run, "b.go")
	fixer := s.observeFixer(pipeline.Fixer{
		Writes: []pipeline.Key{pipeline.KeyHead},
		NewBody: func() pipeline.FixBody {
			return func(context.Context, pipeline.FixInput) (pipeline.FixOutput, error) {
				return pipeline.FixOutput{Summary: "changed a.go",
					Writes: map[pipeline.Key]graph.Value{pipeline.KeyHead: graph.TextValue(after)}}, nil
			}
		},
	}, func(ctx context.Context, _, _ string) (*vcs.Repository, error) { return vcs.OpenWorktree(ctx, dir) })
	values := map[pipeline.Key]graph.Value{
		pipeline.KeyRun: graph.TextValue(run), pipeline.KeyRepository: graph.TextValue("repo-1"),
		pipeline.KeyHead: graph.TextValue(before),
	}
	if _, err := fixer.NewBody()(ctx, pipeline.FixInput{Stage: pipeline.StageTest, State: mapReader(values)}); err != nil {
		t.Fatalf("the fix round failed: %v", err)
	}
	got := followed(t, s, run)
	if f := got[bSeq]; len(f) != 1 || f[0].Kind != store.FollowUpFix || f[0].Detail != after || f[0].Stage != "test" {
		t.Fatalf("the commit touching b.go was recorded as %+v", f)
	}
	if len(got[aSeq]) != 0 {
		t.Fatalf("a.go, which the commit did not touch, was recorded as followed: %+v", got[aSeq])
	}
}

// mapReader is a pipeline.Reader over fixed values.
type mapReader map[pipeline.Key]graph.Value

func (m mapReader) Get(key pipeline.Key) (graph.Value, error) { return m[key], nil }
