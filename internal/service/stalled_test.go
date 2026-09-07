package service_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A segment that stopped without settling leaves the run's record saying
// running against a checkpoint saying running, which is what a run being
// walked this instant also looks like. Whether anything is advancing it is a
// third fact, and neither record holds it: it lives for the length of one
// segment in the slot a run advances in, and P14 is why this is asked of that
// owner rather than inferred from the two records that do not have it.
//
// Reading a run by identifier is the machine surface a driving agent uses to
// ask where a run stands, so this holds that read to the same answer the
// branch's status gives. A surface that answered differently would be a second
// owner of the fact.
//
// It also holds the answer a call that advanced the run gives. That call is
// still holding the run's slot when it reports, and it is answering for a
// segment that has already stopped, so reading the slot without allowing for
// that would have every blocking call claim its own finished segment is still
// running.
func TestAReadSaysWhetherAnythingIsAdvancingTheRun(t *testing.T) {
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	var calls atomic.Int64

	withServiceOptions(t, optionsWithIntentFailingOnce(t, h, &calls), func(running serviceUnderTest) {
		// The first start fails inside the stage body, which is the path that
		// leaves a run standing with nothing advancing it.
		_, err := startRunErr(t, running.client, subject)
		if err == nil {
			t.Fatal("the start whose stage body fails answered without an error")
		}
		if calls.Load() != 1 {
			t.Fatalf("the stage body ran %d times, want the one failing call", calls.Load())
		}

		record := onlyRun(t, h)
		if record.Status != store.RunRunning {
			t.Fatalf("the run's record is %s, want running: the segment stopped without settling it", record.Status)
		}

		stalled := runByID(t, running.client, record.ID)
		if stalled.Outcome != machine.OutcomeExecuting {
			t.Fatalf("reading the stalled run answers %s, want executing", stalled.Outcome)
		}
		if stalled.Progress == nil || *stalled.Progress != graph.StatusRunning {
			t.Fatalf("reading the stalled run reports progress %v, want running", stalled.Progress)
		}
		if stalled.Advancing {
			t.Fatal("reading the run says something is advancing it, after the segment returned")
		}
		if stalled.NextAction != machine.OutcomeExecuting.NextActionFor(false) {
			t.Fatalf("reading the stalled run says %q, want the action that carries it on", stalled.NextAction)
		}

		// Attaching carries it on, and the call that did the advancing answers
		// for a segment that has stopped rather than one still moving.
		carried := startRun(t, running.client, subject)
		if carried.Record.ID != record.ID {
			t.Fatalf("attaching answered about run %s, want the stalled one %s", carried.Record.ID, record.ID)
		}
		if carried.Outcome != machine.OutcomeDecision {
			t.Fatalf("attaching to the stalled run answered %s, want the decision it reached", carried.Outcome)
		}
		if carried.Advancing {
			t.Fatal("a call that advanced the run reports that a segment is still running")
		}
		if calls.Load() != 2 {
			t.Fatalf("the stage body ran %d times, want the failing call and the resume's", calls.Load())
		}
	})
}

// runByID reads one run over the machine surface that reports where a run
// stands without advancing it.
func runByID(t *testing.T, client *ipc.Client, runID string) machine.Run {
	t.Helper()
	var run machine.Run
	if err := client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run); err != nil {
		t.Fatalf("reading run %s: %v", runID, err)
	}
	return run
}

// onlyRun returns the home's single run record, and fails if there is not
// exactly one.
func onlyRun(t *testing.T, h *home.Home) store.Run {
	t.Helper()
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	repositories, err := records.Repositories(t.Context())
	if err != nil {
		t.Fatalf("listing repositories: %v", err)
	}
	var all []store.Run
	for _, repository := range repositories {
		found, err := records.RunsForRepository(t.Context(), repository.ID)
		if err != nil {
			t.Fatalf("listing the runs of %s: %v", repository.ID, err)
		}
		all = append(all, found...)
	}
	if len(all) != 1 {
		t.Fatalf("this home holds %d runs, want the one this test started: %s", len(all), branchesOf(all))
	}
	return all[0]
}

// optionsWithIntentFailingOnce is options with a first stage body that fails
// the first time it runs and reports a decision after that, so one run enters
// the state this file is about and can then be carried out of it.
//
// calls counts every entry into the body, including the resume's.
func optionsWithIntentFailingOnce(t *testing.T, h *home.Home, calls *atomic.Int64) service.Options {
	t.Helper()
	o := options(t, h)
	served := stages.All()
	served.Intent = pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				if calls.Add(1) == 1 {
					return pipeline.Output{}, errBodyCouldNotRun
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "the stage stood in for the one this test is not about",
					Findings: []findings.Finding{
						{ID: "stand-in", Action: findings.ActionAsk, Description: "a decision"},
					},
				}}, nil
			}
		},
	}
	o.Stages = served
	return o
}

// errBodyCouldNotRun is a stage body reporting that it could not run at all,
// which internal/pipeline distinguishes from a stage that ran and found
// something.
var errBodyCouldNotRun = bodyFailure{}

type bodyFailure struct{}

func (bodyFailure) Error() string { return "the intent stage body could not run at all" }
