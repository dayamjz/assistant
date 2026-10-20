package service_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// faultAfterCall is the service's own invocation recorder with one usage
// reading made to fail after each agent call is recorded, which puts the
// failure at the reading right after the call: for a review that holds,
// the reading before its hold.
type faultAfterCall struct {
	late service.LateRecorder
	svc  atomic.Pointer[service.Service]
}

func (r *faultAfterCall) bind(s *service.Service) {
	r.late.Bind(s)
	r.svc.Store(s)
}

// RecordInvocation records the call and then arms one failed reading.
func (r *faultAfterCall) RecordInvocation(rec agents.Record) {
	r.late.RecordInvocation(rec)
	if s := r.svc.Load(); s != nil {
		s.FailUsageReadsForTest(1)
	}
}

// heldPastBudget arranges the stranding case: an operator's per-run usage
// budget of one review's reported output, a review that spends it and
// reports an ask, and the reading before that review's hold failing once.
// It returns the run's identifier, which the failed start leaves recorded
// and halted at the review's hold with the review kept, and the options a
// later service on the same home opens with.
func heldPastBudget(t *testing.T) (string, string, service.Options, func(serviceUnderTest), int64) {
	t.Helper()
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	cost, _ := standin.DefaultUsage().OutputTokens.Value()
	writeOperatorConfig(t, h.ConfigFile(), "run_usage_budget:\n  output_tokens: "+strconv.FormatInt(cost, 10)+"\n")

	recorder := &faultAfterCall{}
	located := findings.Location{Path: "work.txt", Line: 1}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("one finding", findings.Finding{ID: "scope", Severity: findings.SeverityWarning,
			Action: findings.ActionAsk, Location: located, Description: "a question for the person"}),
	}}}, agents.WithRecorder(recorder))
	opts := options(t, h)
	opts.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})

	var runID string
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		recorder.bind(running.service)
		if _, err := startRunErr(t, running.client, subject); err == nil || !strings.Contains(err.Error(), "could not be read") {
			t.Fatalf("the start = %v, want the failed reading before the review's hold reported", err)
		}
		runs, err := recordsOf(t, h).RunsForRepository(t.Context(), repository.ID)
		if err != nil || len(runs) != 1 {
			t.Fatalf("the repository records %v runs (%v), want the one", runs, err)
		}
		runID = runs[0].ID
	})
	bind := func(running serviceUnderTest) { recorder.bind(running.service) }
	return runID, subject, opts, bind, cost
}

// requireUsagePark holds an answer to the run being terminated on the usage
// budget, with the budget and the spend on the record and in the wire form
// --json prints.
func requireUsagePark(t *testing.T, run machine.Run, cost int64) {
	t.Helper()
	park, ok := run.Record.Park.Get()
	if run.Record.Status != store.RunTerminated || !ok || park.Bound != store.ParkUsageBudget ||
		park.UsageBudget != cost || park.Spent != cost {
		t.Fatalf("the run is recorded %s with park %+v (%v), want terminated on a usage budget park of %d with %d spent",
			run.Record.Status, park, ok, cost, cost)
	}
	if run.Decision != nil || run.Outcome != machine.OutcomeFailed {
		t.Fatalf("the run reports %s holding %+v, want failed and holding nothing", run.Outcome, run.Decision)
	}
	wire, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("encoding the answer: %v", err)
	}
	for _, want := range []string{`"bound":"usage_budget"`, `"usage_budget":` + strconv.FormatInt(cost, 10), `"spent":` + strconv.FormatInt(cost, 10)} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("the structured answer does not carry %s:\n%s", want, wire)
		}
	}
}

// TestAnAnswerToARunPastItsUsageBudgetParksIt is the service half of the
// stranding a failed reading before a hold could cause. The review spends the
// whole budget and reports an ask, the reading before its hold fails once, so
// internal/graph records the run halted at the hold with the review kept, and
// the reads then recover. Answering the hold is refused by the graph with
// ErrUsageSpent, and the service parks the run on its usage budget rather
// than leaving it held behind the refusal: the answer comes back with the
// record terminated on a usage budget park carrying the budget and the spend.
func TestAnAnswerToARunPastItsUsageBudgetParksIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	runID, _, opts, bind, cost := heldPastBudget(t)
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		bind(running)
		var run machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunRespond,
			machine.RespondRequest{Run: runID, Answer: "approved"}, &run); err != nil {
			t.Fatalf("answering the run past its budget: %v", err)
		}
		requireUsagePark(t, run, cost)
	})
}

// TestARestartedServiceParksAHeldRunPastItsUsageBudgetOnAttach is the same
// stranding met by attaching after a restart: the run is reconciled to held
// at the review's hold, and the attach parks it on its usage budget instead
// of reporting it held, with no answer given.
func TestARestartedServiceParksAHeldRunPastItsUsageBudgetOnAttach(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	_, subject, opts, bind, cost := heldPastBudget(t)
	withServiceOptions(t, opts, func(running serviceUnderTest) {
		bind(running)
		requireUsagePark(t, startRun(t, running.client, subject), cost)
	})
}
