package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// One run advances in one place at a time, and a second caller asking about a
// run that is moving is told where it stands rather than made to wait or given
// a second walk of the same graph.
//
// The first stage is held inside its body for the whole of the second call, so
// the two overlap for certain rather than by timing.
func TestARunAdvancesInOnePlaceAtATime(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }

	held := func(deps stages.StageDeps) pipeline.Stages {
		nine := stages.All(deps)
		nine.Intent = pipeline.Implementation{
			NewBody: func() pipeline.Body {
				return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
					entered.Do(func() { close(inside) })
					select {
					case <-release:
					case <-ctx.Done():
						return pipeline.Output{}, ctx.Err()
					}
					return pipeline.Output{Report: findings.Report{
						Summary:  "the stage was held open for the length of another call",
						Findings: []findings.Finding{{ID: "held", Action: findings.ActionAsk, Description: "a decision"}},
					}}, nil
				}
			},
		}
		return nine
	}
	o := options(t, h)
	o.NewStages = held

	running, err := service.Open(t.Context(), o)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		let()
		_ = running.Close()
		if err := <-served; err != nil {
			t.Errorf("serving: %v", err)
		}
	})

	first := dial(t, running)
	second := dial(t, running)

	started := make(chan machine.Run, 1)
	go func() {
		var run machine.Run
		if err := first.Call(context.Background(), ipc.MethodRunStart, machine.StartRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &run); err != nil {
			t.Errorf("the first start: %v", err)
		}
		started <- run
	}()

	select {
	case <-inside:
	case <-time.After(10 * time.Second):
		t.Fatal("the first call never reached the stage body")
	}

	// The second call arrives while the first is inside the stage. It reports
	// the run rather than starting another or walking the same graph again.
	var attached machine.Run
	if err := second.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working: machine.Working{WorkingPath: subject},
	}, &attached); err != nil {
		t.Fatalf("attaching while the run was advancing: %v", err)
	}
	if attached.Record.Status != store.RunRunning {
		t.Fatalf("the attached run is recorded as %s, want running", attached.Record.Status)
	}
	// And it is reported as what it is. A run advancing normally is not a run
	// that ended without a verdict, and telling the caller it failed would be
	// the surface asserting the opposite of the truth about a healthy run.
	if attached.Outcome != machine.OutcomeExecuting {
		t.Fatalf("a run inside a stage body reports %s, want executing", attached.Outcome)
	}
	if attached.Outcome.Terminal() {
		t.Fatalf("%s reports that a run still inside a stage body has ended", attached.Outcome)
	}
	if attached.Decision != nil {
		t.Fatalf("a run with no decision open offers one to answer: %+v", attached.Decision)
	}

	let()
	select {
	case run := <-started:
		if run.Record.ID != attached.Record.ID {
			t.Fatalf("the two calls answered about different runs: %s and %s", run.Record.ID, attached.Record.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first call never returned")
	}

	var runs machine.Runs
	if err := second.Call(t.Context(), ipc.MethodRunsList, machine.RunsRequest{
		Working: machine.Working{WorkingPath: subject},
	}, &runs); err != nil {
		t.Fatalf("listing runs: %v", err)
	}
	if len(runs.Runs) != 1 {
		t.Fatalf("two overlapping calls left %d runs, want 1", len(runs.Runs))
	}
}
