package pipeline_test

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Example builds a pipeline from nine stages that report nothing and runs it.
// The nine implementations are the only thing a caller supplies: the order,
// the halt points, the fix loop, and its bounds are the pipeline's.
func Example() {
	p, err := pipeline.New(pipeline.Options{
		Stages: pipeline.ConstantStages("nothing to report"),
		Rounds: config.Defaults().FixRounds,
		Fixer: pipeline.Fixer{
			NewBody: func() pipeline.FixBody {
				return func(context.Context, pipeline.FixInput) (pipeline.FixOutput, error) {
					return pipeline.FixOutput{Summary: "nothing needed changing"}, nil
				}
			},
		},
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		fmt.Println(err)
		return
	}
	state, err := p.NewState(pipeline.Start{
		Branch:    "topic",
		Base:      "main",
		Submitted: "9f2c1ab",
		Skip:      []pipeline.Stage{pipeline.StageCI},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	result, err := exec.Run(context.Background(), "example", state)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Status)
	for _, stage := range pipeline.Order() {
		fmt.Printf("%-8s %s\n", stage, pipeline.StageOutcome(result.State, stage))
	}
	// Output:
	// completed
	// intent   passed
	// rebase   passed
	// review   passed
	// test     passed
	// document passed
	// lint     passed
	// push     passed
	// pr       passed
	// ci       skipped
}
