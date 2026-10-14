package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// Every agent call a run makes lands in the invocation history naming the run
// and the stage it served, and a call made outside both names neither. The
// calls go the way a run's do: the production adapter over the scripted
// stand-in, with this service's recorder, wrapped by containRunner, reached
// from a stage body through agents.StageAgent and from a fix round through
// the fixer the wrapped runner opens, each inside the observe wrapper that
// marks its stage. Nothing here states a record: the rows are what the
// adapter reported and the store kept.
//
// The fix round is the case the stage column exists for: its purpose is fix
// and the stage it served is the one whose findings it was fixing. The call
// outside any run is the control that the names come from the context the
// service marked and not from somewhere every call would find them.
func TestAnAgentCallIsRecordedWithTheRunAndStageItServed(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Service{store: st, stopCtx: ctx, registry: newRegistry()}

	agent := standin.New(t, standin.Script{Steps: []standin.Step{{Times: -1, Reply: standin.Text("done")}}},
		agents.WithRecorder(invocationRecorder{s: s}))
	wrapped := s.containRunner(agent.Runner())
	stageAgent := agents.NewStageAgent(wrapped)
	dir := t.TempDir()
	const run = "run-1"
	values := mapReader{pipeline.KeyRun: graph.TextValue(run)}

	review := s.observeStage(pipeline.Implementation{NewBody: func() pipeline.Body {
		return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
			if _, err := stageAgent.Run(ctx, agents.PurposeReview, agents.Invocation{Prompt: "review", Dir: dir}); err != nil {
				return pipeline.Output{}, err
			}
			return pipeline.Output{Report: findings.Report{Summary: "reviewed"}}, nil
		}
	}})
	segment := withAdvancing(ctx, run)
	if _, err := review.NewBody()(segment, pipeline.Input{Stage: pipeline.StageReview, State: values}); err != nil {
		t.Fatalf("the review body failed: %v", err)
	}

	fixer, err := agents.OpenFixer(ctx, wrapped, "")
	if err != nil {
		t.Fatalf("opening the fixer: %v", err)
	}
	fix := s.observeFixer(pipeline.Fixer{NewBody: func() pipeline.FixBody {
		return func(ctx context.Context, _ pipeline.FixInput) (pipeline.FixOutput, error) {
			_, err := fixer.Apply(ctx, agents.Invocation{Prompt: "fix", Dir: dir})
			return pipeline.FixOutput{}, err
		}
	}}, nil)
	if _, err := fix.NewBody()(segment, pipeline.FixInput{Stage: pipeline.StageTest, State: values}); err != nil {
		t.Fatalf("the fix round failed: %v", err)
	}

	if _, err := stageAgent.Run(ctx, agents.PurposeIntent, agents.Invocation{Prompt: "outside", Dir: dir}); err != nil {
		t.Fatalf("the call outside a run failed: %v", err)
	}

	rows, err := st.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("the history holds %d rows, want one per call", len(rows))
	}
	for i, want := range []struct {
		purpose    string
		run, stage string
		attributed bool
	}{
		{string(agents.PurposeReview), run, "review", true},
		{string(agents.PurposeFix), run, "test", true},
		{string(agents.PurposeIntent), "", "", false},
	} {
		got := rows[i]
		if got.Purpose != want.purpose {
			t.Errorf("row %d is purpose %q, want %q", i, got.Purpose, want.purpose)
		}
		if !want.attributed {
			if got.Run.IsKnown() || got.Stage.IsKnown() {
				t.Errorf("the call outside a run recorded run %v stage %v, want unknown for both", got.Run, got.Stage)
			}
			continue
		}
		if r, ok := got.Run.Get(); !ok || r != want.run {
			t.Errorf("row %d names run (%q, %v), want %q", i, r, ok, want.run)
		}
		if stage, ok := got.Stage.Get(); !ok || stage != want.stage {
			t.Errorf("row %d names stage (%q, %v), want %q", i, stage, ok, want.stage)
		}
	}
}
