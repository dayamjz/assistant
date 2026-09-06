package runs_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// This is the reason the fixer session lives on the run's record.
//
// internal/graph fingerprints the whole of a run's state at every back edge,
// and a round that leaves it identical is what ends a fix loop. That is one of
// the three bounds, and it is the one that catches a fixer reporting success
// every round without changing anything. A session reference in that state
// would move whenever the agent named a new conversation, so the state would
// never repeat, so the bound could never fire - and every run would still look
// green, which is what makes a bound that cannot fire worse than no bound.
//
// So the loop runs for real, twice, with one difference between the two. Both
// have a stage that never stops reporting the same fix-eligible finding, a
// fixer that changes nothing and says so in the same words every round, a
// round limit and a budget generous enough that neither could be what stops
// the run, and an agent that names a different session every round. The first
// keeps the reference where this package keeps it and converges. The second
// writes it into graph state, which is the design this package exists to
// avoid, and runs until its round limit instead.
//
// The second case is what makes the first one evidence. Without it the
// assertion would hold just as well for a session that never moved.
func TestConvergenceFiresWithADurableSessionAndNotWithOneInGraphState(t *testing.T) {
	const (
		limit  = 6
		budget = 500
	)
	for _, held := range []struct {
		name string
		// writes is what the fix body declares and returns. Empty leaves the
		// reference off the state the graph fingerprints.
		inState bool
		want    graph.Status
		rounds  int
	}{
		{"on the run's record", false, graph.StatusConverged, 2},
		{"in graph state", true, graph.StatusRoundsExhausted, limit},
	} {
		t.Run(held.name, func(t *testing.T) {
			ctx := t.Context()
			const runID = "run-1"

			// One step per possible round, each naming a session of its own,
			// so the reference is guaranteed to move between rounds rather
			// than happening to stay put.
			var steps []standin.Step
			for i := 1; i <= limit+2; i++ {
				steps = append(steps, standin.Step{
					Reply: answering("nothing needed changing", "sess-"+strconv.Itoa(i)),
				})
			}
			ag := standin.New(t, standin.Script{Steps: steps})
			agent := resolution(ag)

			s, _ := openStore(t)
			svc := service(t, s, agent, true)
			run := seedRun(t, s, svc, runID)
			if _, err := svc.Start(ctx, run.ID); err != nil {
				t.Fatalf("Start: %v", err)
			}

			var writes []pipeline.Key
			if held.inState {
				writes = []pipeline.Key{pipeline.KeyHead}
			}
			dir := t.TempDir()
			rounds := 0
			stages := pipeline.ConstantStages("nothing to report")
			stages.Review = pipeline.Constant("one finding", findings.Finding{
				Action:      findings.ActionFix,
				Description: "still wrong",
			})
			p, err := pipeline.New(pipeline.Options{
				Stages: stages,
				Fixer: pipeline.Fixer{
					Requires: svc.FixerRequires(),
					Writes:   writes,
					NewBody: func() pipeline.FixBody {
						return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
							fixer, err := svc.Fixer(ctx, runID)
							if err != nil {
								return pipeline.FixOutput{}, err
							}
							rounds++
							if _, err := fixer.Apply(ctx, agents.Invocation{
								Prompt: "fix " + in.Stage.String(),
								Shape:  agents.ShapeText,
								Dir:    dir,
							}); err != nil {
								return pipeline.FixOutput{}, err
							}
							// The same words every round. A summary naming a
							// round number would defeat convergence on its
							// own, which is internal/pipeline's documented gap
							// and not this one.
							out := pipeline.FixOutput{Summary: "nothing needed changing"}
							if held.inState {
								out.Writes = map[pipeline.Key]graph.Value{
									pipeline.KeyHead: graph.TextValue(fixer.Reference()),
								}
							}
							return out, nil
						}
					},
				},
				Rounds:  config.FixRounds{Review: limit},
				Budget:  budget,
				Adapter: agent.Capabilities,
			})
			if err != nil {
				t.Fatalf("pipeline.New: %v", err)
			}
			executor, err := p.Executor(graph.NewMemoryStore())
			if err != nil {
				t.Fatalf("Executor: %v", err)
			}
			state, err := p.NewState(pipeline.Start{Branch: "topic", Base: "main", Submitted: "aaaa"})
			if err != nil {
				t.Fatalf("NewState: %v", err)
			}

			result, err := executor.Run(ctx, runID, state)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if result.Status != held.want {
				t.Fatalf("the run ended %s (%q), want %s", result.Status, result.Reason, held.want)
			}
			if rounds != held.rounds {
				t.Errorf("the fixer ran %d times, want %d", rounds, held.rounds)
			}
			if result.Steps >= budget {
				t.Errorf("the run spent %d of its %d steps, so the budget could have stopped it",
					result.Steps, budget)
			}

			// The reference did move while the loop ran, which is what makes
			// the difference between the two cases the state it was held in
			// rather than a session that stayed put.
			calls := ag.Calls()
			if len(calls) < 2 {
				t.Fatalf("the agent saw %d invocations, want at least 2", len(calls))
			}
			if got := calls[0].Session(); got != "" {
				t.Errorf("the first round asked to resume %q", got)
			}
			if got := calls[1].Session(); got != "sess-1" {
				t.Errorf("the second round asked to resume %q, want sess-1", got)
			}
			recorded, known := sessionOf(t, s, runID)
			if !known || recorded == "sess-1" {
				t.Errorf("the run records %q (known=%v), want the later session the last round named",
					recorded, known)
			}
		})
	}
}

// What a caller puts on a pipeline's fixer is a runs.Fixer, and a runs.Fixer
// is an agents.Fixer: an entry point that takes an invocation and no purpose.
// Nothing between the pipeline and the agent widens that.
func TestTheRunsFixerIsAnAgentsFixerAndCarriesNoPurpose(t *testing.T) {
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")

	fixer, err := svc.Fixer(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	var asFixer agents.Fixer = fixer
	if _, ok := asFixer.(interface {
		Run(context.Context, agents.Purpose, agents.Invocation) (agents.Result, error)
	}); ok {
		t.Fatal("the run's fixer also carries a purpose-taking entry point")
	}
}
