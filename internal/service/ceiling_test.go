package service_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestARunAfterTheUsageCeilingIsReachedHoldsForAPerson is the usage ceiling
// on the path a run takes: the real executor, the review stage's own body,
// the production adapter over the scripted stand-in, and this service's own
// invocation recorder, so the record the ceiling is weighed against is what
// the adapter read off the wire and the store kept. The operator's ceiling
// on input tokens is the stand-in's reported cost of one review.
//
// The first run is the positive control: its review starts under the
// ceiling, runs, and passes, and the run goes on to hold where a run with no
// ceiling holds, so the ceiling refuses nothing it should not. That review's
// recorded cost reaches the ceiling, and the second run's review is not
// started: the run holds at the review stage for a person, on the ceiling's
// ask, rather than failing or standing stranded, and the stand-in was never
// asked. The hold carries the same total the usage view reports.
func TestARunAfterTheUsageCeilingIsReachedHoldsForAPerson(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	cost, _ := standin.DefaultUsage().InputTokens.Value()
	writeOperatorConfig(t, h.ConfigFile(), "usage_ceiling:\n  window: 24h\n  input_tokens: "+strconv.FormatInt(cost, 10)+"\n")

	recorder := &service.LateRecorder{}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("the change reads cleanly"),
	}}}, agents.WithRecorder(recorder))
	opts := options(t, h)
	opts.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})

	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.Bind(running.service)

		control := startRun(t, running.client, subject)
		if review := stageView(t, control, pipeline.StageReview); review.Outcome != pipeline.OutcomePassed || !review.Ran {
			t.Fatalf("the review under the ceiling came back %s (ran %v), want it run and passed", review.Outcome, review.Ran)
		}
		if control.Decision == nil || control.Decision.Stage == pipeline.StageReview.String() {
			t.Fatalf("the run under the ceiling holds at %+v, want a later stage", control.Decision)
		}
		if got := len(agent.Calls()); got != 1 {
			t.Fatalf("the stand-in was asked %d times, want the one review", got)
		}

		// A second branch, so the second start is a run of its own rather
		// than the first branch's held run answering again.
		commitOn(t, subject, "past-the-ceiling", "past.txt")
		held := startRun(t, running.client, subject)
		decision := holdingAt(t, held, pipeline.StageReview)
		if held.Record.Status != store.RunHeld {
			t.Fatalf("the run past the ceiling is recorded %s, want held", held.Record.Status)
		}
		if got := len(agent.Calls()); got != 1 {
			t.Fatalf("the stand-in was asked %d times, want the ceiling to have refused the second review", got)
		}
		var ask *findings.Finding
		for i := range decision.Findings {
			if decision.Findings[i].ID == machine.UsageCeilingFindingID {
				ask = &decision.Findings[i]
			}
		}
		if ask == nil || !ask.Holds() || !ask.RemedyOutsideChange {
			t.Fatalf("the hold carries %+v, want the ceiling's ask for a person", decision.Findings)
		}

		var usage machine.Usage
		if err := running.client.Call(t.Context(), ipc.MethodUsageRead, machine.UsageRequest{Window: "1d"}, &usage); err != nil {
			t.Fatalf("usage.read: %v", err)
		}
		if usage.Ceiling.Standing != machine.CeilingReached || len(usage.Ceiling.Counts) != 1 {
			t.Fatalf("the usage view weighs the ceiling as %+v, want it reached", usage.Ceiling)
		}
		recorded := usage.Ceiling.Counts[0].Recorded
		if n, ok := recorded.Total.Get(); !ok || n != cost || recorded != usage.Total.InputTokens {
			t.Fatalf("the ceiling weighs %+v and the view totals %+v, want both the one review's %d", recorded, usage.Total.InputTokens, cost)
		}
		want := "input_tokens " + strconv.FormatInt(cost, 10) + " of " + strconv.FormatInt(cost, 10) + " (reached)"
		if !strings.Contains(ask.Description, want) || !strings.Contains(ask.Description, machine.CeilingHoldPhrase(machine.CeilingReached).String()) {
			t.Fatalf("the hold's detail %q does not carry the row and %q", ask.Description, want)
		}
	})
}

// TestATrustedCopyNamingTheUsageCeilingStopsTheRunBeforeLaunch is P7 for the
// ceiling at the read a run actually takes: a repository whose default
// branch carries a document naming a usage ceiling key is refused at the
// trusted copy's read, before any stage body runs, so a repository can no
// more lift the operator's ceiling from its trusted copy than from a pushed
// one, and the refusal names the trusted configuration.
func TestATrustedCopyNamingTheUsageCeilingStopsTheRunBeforeLaunch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	plantTrustedDocument(t, subject, `{"usage_ceiling": {"input_tokens": 1000000000}}`)

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
			t.Fatal("a run whose trusted copy names a usage ceiling was started rather than refused")
		}
		if !strings.Contains(err.Error(), "trusted configuration") || !strings.Contains(err.Error(), "usage_ceiling.input_tokens") {
			t.Fatalf("the refusal does not name the trusted configuration and the key: %v", err)
		}
		if got := bodies.Load(); got != 0 {
			t.Fatalf("%d stage bodies ran under a trusted copy naming a global-only key", got)
		}
	})
}

// writeOperatorConfig writes the operator's own configuration file, which the
// service reads when it opens.
func writeOperatorConfig(t *testing.T, path, document string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("writing the operator's configuration: %v", err)
	}
}
