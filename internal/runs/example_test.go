package runs_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// A run is recorded, begins, waits on a person, runs again, and ends. Each of
// those is one call, and each is anchored to the status the caller expected to
// find, so a run that has moved since is reported rather than written over.
func Example() {
	ctx := context.Background()

	var db *store.Store         // from store.Open.
	var agent agents.Resolution // from agents.Resolve.
	if db == nil || agent.Runner == nil {
		return
	}

	service, err := runs.New(runs.Options{Store: db, Agent: agent, SessionReuse: true})
	if err != nil {
		// An adapter that cannot keep a session is refused here, before a run
		// exists, rather than on the first fix round.
		fmt.Println("this gate cannot run:", err)
		return
	}

	run, err := service.Create(ctx, store.Run{
		ID: "run-1", RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa1111", Base: "bbbb2222",
		Intent: "validate the pushed branch", IntentSource: "push",
		Build:        store.Build{Version: "v0.1.0", Revision: "cccc3333", Go: "go1.25.0"},
		ConfigDigest: "sha256:dddd4444",
	})
	if err != nil {
		fmt.Println("not recorded:", err)
		return
	}
	if _, err := service.Start(ctx, run.ID); err != nil {
		fmt.Println("not started:", err)
		return
	}

	// A stage found something a person must decide. Registering the decision
	// itself is store.RegisterHold's; this records only where the run stands.
	if _, err := service.Hold(ctx, run.ID); err != nil {
		fmt.Println("not held:", err)
		return
	}
	if _, err := service.Release(ctx, run.ID); err != nil {
		// The run moved under this caller, which is a state to report and not
		// a write to force through.
		var refusal *store.RunStatusError
		if errors.As(err, &refusal) {
			fmt.Println("the run is", refusal.Actual)
		}
		return
	}
	if _, err := service.Pass(ctx, run.ID); err != nil {
		fmt.Println("no verdict recorded:", err)
	}
}

// A fix round reaches the agent through the run's fixer role, and the pipeline
// is told what that role needs of the adapter so a path it cannot serve is
// refused before the topology is built.
func ExampleService_Fixer() {
	var service *runs.Service // from runs.New.
	var stages pipeline.Stages
	var adapter agents.Capabilities // agents.Resolution.Capabilities.
	if service == nil {
		return
	}
	runID := "run-1"

	fixer := pipeline.Fixer{
		Requires: service.FixerRequires(),
		NewBody: func() pipeline.FixBody {
			return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
				// One object per run, so a new advance segment continues the
				// conversation the last one opened rather than starting a
				// second one about the same change.
				role, err := service.Fixer(ctx, runID)
				if err != nil {
					return pipeline.FixOutput{}, err
				}
				result, err := role.Apply(ctx, agents.Invocation{
					Prompt: "Apply these findings.", // The stage writes the prompt.
					Shape:  agents.ShapeText,
					Dir:    "/absolute/path/to/the/isolated/copy",
				})
				if err != nil {
					return pipeline.FixOutput{}, err
				}
				// The same words for the same outcome. A summary naming the
				// round number would cost this loop its convergence bound.
				_ = result
				return pipeline.FixOutput{Summary: "applied the findings"}, nil
			}
		},
	}

	if _, err := pipeline.New(pipeline.Options{
		Stages: stages, Fixer: fixer, Budget: 40, Adapter: adapter,
	}); err != nil {
		fmt.Println("this gate cannot be built:", err)
	}
}
