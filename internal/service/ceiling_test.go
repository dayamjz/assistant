package service_test

import (
	"context"
	"errors"
	"os"
	"slices"
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

// TestACeilingIsWeighedAgainstWhatCursorReports is #510 on the path a run
// takes: the production Cursor adapter as the reviewer, over the stand-in's
// model of what cursor-agent prints (camelCase usage keys, recorded in
// internal/agents/testdata), and an operator ceiling on input tokens well
// above one review's reported cost. The first review runs under an empty
// window. Its counts reach the record, so the second run's review starts and
// passes too, and the usage view totals both reviews' input tokens with none
// unreported. Before the adapter read the keys the CLI writes, every Cursor
// call was recorded with no count at all, so the second run held at review
// on the ceiling's ask while the record held two hundred tokens against a
// ceiling of ten thousand.
func TestACeilingIsWeighedAgainstWhatCursorReports(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	writeOperatorConfig(t, h.ConfigFile(), "usage_ceiling:\n  window: 24h\n  input_tokens: 10000\n")

	recorder := &service.LateRecorder{}
	agent := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("the change reads cleanly"),
	}}}, agents.WithCursorRecorder(recorder))
	opts := options(t, h)
	opts.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()})

	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.Bind(running.service)

		first := startRun(t, running.client, subject)
		if review := stageView(t, first, pipeline.StageReview); review.Outcome != pipeline.OutcomePassed || !review.Ran {
			t.Fatalf("the first review came back %s (ran %v), want it run and passed", review.Outcome, review.Ran)
		}

		commitOn(t, subject, "after-the-first-review", "after.txt")
		second := startRun(t, running.client, subject)
		if second.Decision != nil && second.Decision.Stage == pipeline.StageReview.String() {
			t.Fatalf("the second run holds at review on %+v, want its review run under the ceiling", second.Decision.Findings)
		}
		if review := stageView(t, second, pipeline.StageReview); review.Outcome != pipeline.OutcomePassed || !review.Ran {
			t.Fatalf("the second review came back %s (ran %v), want it run and passed", review.Outcome, review.Ran)
		}
		if got := len(agent.Calls()); got != 2 {
			t.Fatalf("the stand-in was asked %d times, want both reviews", got)
		}

		var usage machine.Usage
		if err := running.client.Call(t.Context(), ipc.MethodUsageRead, machine.UsageRequest{Window: "1d"}, &usage); err != nil {
			t.Fatalf("usage.read: %v", err)
		}
		cost, _ := standin.DefaultUsage().InputTokens.Value()
		input := usage.Total.InputTokens
		if n, ok := input.Total.Get(); !ok || n != 2*cost || input.Unreported != 0 {
			t.Fatalf("the usage view totals input tokens as %+v, want both reviews' %d with none unreported", input, 2*cost)
		}
		if usage.Ceiling.Standing != machine.CeilingUnder {
			t.Fatalf("the usage view weighs the ceiling as %s (%s), want under", usage.Ceiling.Standing, usage.Ceiling.Figures())
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
		// Ended rather than left pending, on the terms of the other
		// configuration refusals: a pending run with no checkpoint can never
		// be resumed, and a later start would attach to it.
		refused := onlyRun(t, h)
		if refused.Status != store.RunTerminated {
			t.Fatalf("the refused run is recorded as %s, want it ended", refused.Status)
		}

		plantTrustedDocument(t, subject, `{"ignore_patterns": ["vendor/**"]}`)
		fresh := startRun(t, running.client, subject)
		if fresh.Record.ID == refused.ID {
			t.Fatalf("the start after the trusted copy was fixed attached to the refused run %s", refused.ID)
		}
		if fresh.Record.Status == store.RunPending || fresh.Record.Status == store.RunTerminated {
			t.Fatalf("the fresh run is recorded as %s, want it walked", fresh.Record.Status)
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

// TestARunHeldOnTheUsageCeilingIsRetriedOnceThereIsRoom is PRD section 5's
// Retry at the hold the usage ceiling raised, on the path a person takes:
// the real executor, the review stage's own body, the production adapter
// over the scripted stand-in, and this service's own invocation recorder.
//
// The first run's review reaches the operator's ceiling, so the second run
// holds at review on the ceiling's ask. A retry while there is still no
// room re-runs the stage, whose call the ceiling refuses again, and the run
// holds on the same question with the stand-in still unasked: it does not
// loop. The operator then raises the ceiling and restarts the service, and
// a retry runs the review in the same run, which passes, so the run goes
// on past review instead of being cancelled and started again.
//
// The first run's later hold is the control: it is not over a refused call,
// so it does not list the retry, and a retry there is refused as the
// caller's error with the decision still open.
func TestARunHeldOnTheUsageCeilingIsRetriedOnceThereIsRoom(t *testing.T) {
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

	var heldID string
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.Bind(running.service)
		retry := string(pipeline.OutcomeRetry)
		control := startRun(t, running.client, subject)
		if control.Decision == nil || control.Decision.Stage == pipeline.StageReview.String() {
			t.Fatalf("the run under the ceiling holds at %+v, want a later stage", control.Decision)
		}
		if slices.Contains(control.Decision.Options, retry) {
			t.Fatalf("the %s hold, over no refused call, lists %v", control.Decision.Stage, control.Decision.Options)
		}
		var refused machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{Run: control.Record.ID, Answer: retry}, &refused)
		if !errors.Is(err, ipc.ErrInvalidRequest) || !strings.Contains(err.Error(), "usage ceiling") {
			t.Fatalf("a retry at the %s hold returned %v, want it refused as the caller's error", control.Decision.Stage, err)
		}
		var still machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: control.Record.ID}, &still); err != nil {
			t.Fatalf("reading the control run: %v", err)
		}
		if still.Decision == nil || still.Decision.Stage != control.Decision.Stage {
			t.Fatalf("after the refused retry the run reports %+v, want its decision still open", still.Decision)
		}

		commitOn(t, subject, "past-the-ceiling", "past.txt")
		held := startRun(t, running.client, subject)
		if decision := holdingAt(t, held, pipeline.StageReview); !slices.Contains(decision.Options, retry) {
			t.Fatalf("the ceiling's hold lists %v, want the retry", decision.Options)
		}
		heldID = held.Record.ID

		again := answer(t, running.client, heldID, string(pipeline.OutcomeRetry))
		decision := holdingAt(t, again, pipeline.StageReview)
		if !carriesCeilingAsk(decision) {
			t.Fatalf("the retry with no room holds on %+v, want the ceiling's ask again", decision.Findings)
		}
		if got := len(agent.Calls()); got != 1 {
			t.Fatalf("the stand-in was asked %d times, want the retry with no room refused before it", got)
		}
	})

	writeOperatorConfig(t, h.ConfigFile(), "usage_ceiling:\n  window: 24h\n  input_tokens: "+strconv.FormatInt(10*cost, 10)+"\n")
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.Bind(running.service)
		retried := answer(t, running.client, heldID, string(pipeline.OutcomeRetry))
		if retried.Record.ID != heldID {
			t.Fatalf("the retry answered run %s, want the held run %s", retried.Record.ID, heldID)
		}
		if review := stageView(t, retried, pipeline.StageReview); review.Outcome != pipeline.OutcomePassed || !review.Ran {
			t.Fatalf("the retried review came back %s (ran %v), want it run and passed", review.Outcome, review.Ran)
		}
		if retried.Decision != nil && retried.Decision.Stage == pipeline.StageReview.String() {
			t.Fatalf("the retried run still holds at review on %+v", retried.Decision.Findings)
		}
		if got := len(agent.Calls()); got != 2 {
			t.Fatalf("the stand-in was asked %d times, want the retried review to have run", got)
		}
	})
}

// carriesCeilingAsk reports whether a decision holds on the ceiling's ask.
func carriesCeilingAsk(d machine.Decision) bool {
	for _, f := range d.Findings {
		if f.ID == machine.UsageCeilingFindingID && f.Holds() && f.RemedyOutsideChange {
			return true
		}
	}
	return false
}
