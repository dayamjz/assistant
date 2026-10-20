package service_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// runUnderUsageBudget starts one run on a fresh home whose operator set the
// per-run usage budget to budget output tokens, with the review stage's own
// body over the production adapter and the scripted stand-in, and this
// service's own invocation recorder, so what the budget is weighed against is
// what the adapter read off the wire and the store kept. It returns the run's
// answer and how many times the stand-in was asked.
func runUnderUsageBudget(t *testing.T, budget int64) (machine.Run, int) {
	t.Helper()
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	writeOperatorConfig(t, h.ConfigFile(), "run_usage_budget:\n  output_tokens: "+strconv.FormatInt(budget, 10)+"\n")

	recorder := &service.LateRecorder{}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("the change reads cleanly"),
	}}}, agents.WithRecorder(recorder))
	opts := options(t, h)
	opts.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})

	var run machine.Run
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.Bind(running.service)
		run = startRun(t, running.client, subject)
	})
	return run, len(agent.Calls())
}

// TestARunPastItsUsageBudgetParksAndSaysWhatItSpent is the per-run usage
// budget on the path a run takes, through the real executor. The operator's
// budget is the stand-in's reported output for one review, so the review
// runs under it, its recorded cost reaches it, and the run parks in front of
// the next node rather than holding for a person: its record is terminated
// with a usage budget park carrying the budget and what the run's calls
// reported, which travels on the structured answer, the outcome is failed,
// the next action names the operator's key, and the phrase is the usage
// budget's row. The control is the same run under a budget one token larger,
// which goes on to hold where a run with no budget holds.
func TestARunPastItsUsageBudgetParksAndSaysWhatItSpent(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	cost, _ := standin.DefaultUsage().OutputTokens.Value()

	control, asked := runUnderUsageBudget(t, cost+1)
	if control.Record.Park.IsKnown() || control.Decision == nil || control.Decision.Stage == pipeline.StageReview.String() {
		t.Fatalf("the run with room under its budget stands at %+v with park %v, want it held past the review",
			control.Decision, control.Record.Park)
	}
	if asked != 1 {
		t.Fatalf("the stand-in was asked %d times under the roomy budget, want the one review", asked)
	}

	parked, asked := runUnderUsageBudget(t, cost)
	if asked != 1 {
		t.Fatalf("the stand-in was asked %d times, want the one review the budget let start", asked)
	}
	if review := stageView(t, parked, pipeline.StageReview); review.Outcome != pipeline.OutcomePassed || !review.Ran {
		t.Fatalf("the review came back %s (ran %v), want it run and passed under the budget", review.Outcome, review.Ran)
	}
	if parked.Record.Status != store.RunTerminated || parked.Decision != nil {
		t.Fatalf("the run is recorded %s holding %+v, want it terminated by a park and holding nothing",
			parked.Record.Status, parked.Decision)
	}
	park, ok := parked.Record.Park.Get()
	if !ok || park.Bound != store.ParkUsageBudget || park.UsageBudget != cost || park.Spent != cost || park.Unreported != 0 {
		t.Fatalf("the record's park is %+v (%v), want a usage budget park of %d with %d spent", park, ok, cost, cost)
	}
	if parked.Outcome != machine.OutcomeFailed {
		t.Fatalf("the parked run's outcome is %s, want failed", parked.Outcome)
	}
	if action := parked.NextAction(); !strings.Contains(action, string(config.KeyRunUsageBudgetOutputTokens)) ||
		!strings.Contains(action, "usage budget of "+strconv.FormatInt(cost, 10)) {
		t.Fatalf("the next action does not name the budget and its key:\n%s", action)
	}
	if phrase := parked.Phrase(); !strings.Contains(phrase.Outcome, "usage budget") {
		t.Fatalf("the parked run reads %q, want the usage budget's row", phrase.Outcome)
	}

	// The structured answer is the wire form, so the figures are what --json
	// prints rather than fields this test read off a Go value.
	wire, err := json.Marshal(parked)
	if err != nil {
		t.Fatalf("encoding the answer: %v", err)
	}
	figures := `"bound":"usage_budget"`
	for _, want := range []string{figures, `"usage_budget":` + strconv.FormatInt(cost, 10), `"spent":` + strconv.FormatInt(cost, 10)} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("the structured answer does not carry %s:\n%s", want, wire)
		}
	}
}

// TestARepositoryCopyNamingTheRunUsageBudgetStopsTheRunBeforeLaunch is P7 for
// the per-run usage budget at the read a run actually takes: a pushed copy,
// and a trusted copy, naming the key is refused before any stage body runs,
// so a repository can neither raise nor lift the operator's budget, and the
// refusal names the copy and the key.
func TestARepositoryCopyNamingTheRunUsageBudgetStopsTheRunBeforeLaunch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	const doc = `{"run_usage_budget": {"output_tokens": 1000000000}}`
	for name, c := range map[string]struct {
		plant func(t *testing.T, subject, document string)
		copy  string
	}{
		"pushed":  {plantPushedDocument, "pushed configuration"},
		"trusted": {plantTrustedDocument, "trusted configuration"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			recordRepository(t, h, subject)
			c.plant(t, subject, doc)

			var bodies atomic.Int64
			opts := options(t, h)
			opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
				nine := stages.All(deps)
				nine.Intent = pipeline.Implementation{NewBody: func() pipeline.Body {
					return func(context.Context, pipeline.Input) (pipeline.Output, error) {
						bodies.Add(1)
						return pipeline.Output{Report: findings.Report{Summary: "counted"}}, nil
					}
				}}
				return nine
			}
			withServiceOptions(t, opts, func(running serviceUnderTest) {
				_, err := startRunErr(t, running.client, subject)
				if err == nil {
					t.Fatalf("a run whose %s copy names the usage budget was started rather than refused", name)
				}
				if !strings.Contains(err.Error(), c.copy) || !strings.Contains(err.Error(), string(config.KeyRunUsageBudgetOutputTokens)) {
					t.Fatalf("the refusal does not name the %s and the key: %v", c.copy, err)
				}
				if got := bodies.Load(); got != 0 {
					t.Fatalf("%d stage bodies ran under a %s copy naming a global-only key", got, name)
				}
			})
		})
	}
}
