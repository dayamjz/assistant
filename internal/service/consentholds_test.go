package service_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// mergeableProvider is a scripted code host that opens pull request 9 for the
// branch and reads it open and mergeable, which is what a consenting run that
// walks to checks-passed asks of it.
func mergeableProvider(t *testing.T) *forgetest.Provider {
	t.Helper()
	return forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":9}]`)},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/9\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE", Head: "work", Base: "main",
		})},
	})
}

// serviceLog is what the home's service log holds so far.
func serviceLog(t *testing.T, h *home.Home) string {
	t.Helper()
	text, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	return string(text)
}

// A finding whose reviewer stated no action is P3's ask, and standing consent
// leaves the hold it raises for a person: the run is held at the review with
// no fix round taken, nothing is recorded as the program's answer, and the
// service log names the finding and P3. The control is the same run whose
// reviewer stated "ask" for the same finding, which consent answers with a
// round and carries to checks-passed, so the difference is the stated action
// and nothing else about the run.
func TestStandingConsentLeavesAnAskWhoseActionWasDefaultedForAPerson(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)
	requiresIdentifiedPeer(t)
	located := findings.Location{Path: "work.txt", Line: 1}
	finding := func(action findings.Action) findings.Finding {
		return findings.Finding{ID: "unclear", Severity: findings.SeverityWarning, Action: action,
			Location: located, Description: "a finding about the change"}
	}

	t.Run("defaulted", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := reviewThen(t, finding(findings.ActionUnset))
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(running serviceUnderTest) {
			run, err := startUnderConsent(t, running.client, subject)
			if err != nil {
				t.Fatalf("starting a run under standing consent: %v", err)
			}
			decision := holdingAt(t, run, pipeline.StageReview)
			defaulted := false
			for _, f := range decision.Findings {
				defaulted = defaulted || (f.ID == "unclear" && f.Holds() && f.ActionDefaulted)
			}
			if !defaulted {
				t.Fatalf("the review holds on %+v, want the finding P3 made an ask", decision.Findings)
			}
			if rounds := fixRounds(agent); len(rounds) != 0 {
				t.Fatalf("standing consent sent P3's ask to %d fix round(s)", len(rounds))
			}
			records := openRecords(t, h)
			defer func() { _ = records.Close() }()
			if answers := consentAnswers(t, records, run.Record.ID); len(answers) != 0 {
				t.Fatalf("standing consent recorded an answer to P3's ask: %+v", answers)
			}
			if log := serviceLog(t, h); !strings.Contains(log, "finding unclear") || !strings.Contains(log, "P3") {
				t.Fatalf("the service log does not say why consent left the hold:\n%s", log)
			}
		})
	})

	t.Run("stated", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := reviewThen(t, finding(findings.ActionAsk))
		withServiceOptions(t, consentOptions(t, h, agent, mergeableProvider(t), nil), func(running serviceUnderTest) {
			run, err := startUnderConsent(t, running.client, subject)
			if err != nil {
				t.Fatalf("starting a run under standing consent: %v", err)
			}
			if run.Outcome != machine.OutcomeChecksPassed {
				t.Fatalf("a stated ask under consent ended %s: %s; want checks-passed", run.Outcome, run.Reason)
			}
			if rounds := fixRounds(agent); len(rounds) != 1 {
				t.Fatalf("consent sent a stated ask to %d fix round(s), want one", len(rounds))
			}
		})
	})
}

// A command stage with no command configured holds on an ask no fix round can
// resolve, and standing consent leaves it for a person rather than spending a
// round on it: the run under consent has its review's stated findings
// answered with one round, which shows consent was answering, and then holds
// at the test stage with no further round taken and nothing recorded as the
// program's answer there. The service log says why.
func TestStandingConsentSendsNoRoundToAnAskNoAgentCanFix(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, allowsConsent)
	agent := reviewWithFindings(t)
	o := consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil)
	constant := o.NewStages
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := constant(deps)
		all.Test = stages.All(deps).Test
		return all
	}

	withServiceOptions(t, o, func(running serviceUnderTest) {
		run, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run under standing consent: %v", err)
		}
		decision := holdingAt(t, run, pipeline.StageTest)
		configurationOnly := false
		for _, f := range decision.Findings {
			configurationOnly = configurationOnly || (f.Holds() && f.RemedyOutsideChange)
		}
		if !configurationOnly {
			t.Fatalf("the test stage holds on %+v, want its ask for a command", decision.Findings)
		}
		if rounds := fixRounds(agent); len(rounds) != 1 {
			t.Fatalf("the fixer was invoked %d times, want once, for the review and never for the test stage", len(rounds))
		}
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		answers := consentAnswers(t, records, run.Record.ID)
		if len(answers) != 2 {
			t.Fatalf("standing consent recorded %d answers, want the review's two and none at the test stage: %+v",
				len(answers), answers)
		}
		for _, hold := range answers {
			if strings.Contains(hold.Detail, pipeline.StageTest.String()+" stage") {
				t.Fatalf("standing consent answered the test stage: %+v", hold)
			}
		}
		if log := serviceLog(t, h); !strings.Contains(log, "no fix round can resolve it") {
			t.Fatalf("the service log does not say why consent left the test stage's hold:\n%s", log)
		}
	})
}

// A round standing consent asks for is held to the convergence bound, which a
// round a person asks for is exempt from. The reviewer reports the same
// stated ask on every review and the fixer changes nothing, so every round
// leaves the run's state as the round before it left it. Under consent the
// run parks on convergence at the review. The control is the same run with a
// person asking for every round over the machine interface: it never parks,
// and once the rounds are spent the answer asking for another is refused
// with the decision still open.
//
// The number of rounds consent took before the park is pinned rather than
// bounded, because consent.go states it: a stage's re-run report carries the
// hold the previous round left, which the report the first round read did
// not, so the bound fires on the third round and no earlier.
func TestARoundStandingConsentAsksForIsHeldToConvergence(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	located := findings.Location{Path: "work.txt", Line: 1}
	stuck := func(t *testing.T) *standin.Agent {
		return standin.New(t, standin.Script{Steps: []standin.Step{
			{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("one finding",
				findings.Finding{ID: "scope", Severity: findings.SeverityWarning, Action: findings.ActionAsk,
					Location: located, Description: "this widens what the change was asked to do"})},
			{Times: standin.Always, Reply: standin.Text("looked and changed nothing")},
		}})
	}

	t.Run("under consent", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := stuck(t)
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(running serviceUnderTest) {
			run, err := startUnderConsent(t, running.client, subject)
			if err != nil {
				t.Fatalf("starting a run under standing consent: %v", err)
			}
			park, parked := run.Record.Park.Get()
			if !parked || park.Bound != store.ParkConvergence || park.Stage != pipeline.StageReview.String() {
				t.Fatalf("the run ended %s with park %+v (%v): %s; want parked on convergence at the review",
					run.Outcome, park, parked, run.Reason)
			}
			if rounds := fixRounds(agent); len(rounds) != pipeline.RequestedFixRounds {
				t.Fatalf("consent took %d round(s) before the park, want %d", len(rounds), pipeline.RequestedFixRounds)
			}
		})
	})

	t.Run("a person asking", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := stuck(t)
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(running serviceUnderTest) {
			run := startRun(t, running.client, subject)
			for round := range pipeline.RequestedFixRounds {
				holdingAt(t, run, pipeline.StageReview)
				if err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
					Run: run.Record.ID, Answer: string(pipeline.OutcomeFixRequested),
					Fix: &pipeline.FixRequest{Selected: []string{"scope"}},
				}, &run); err != nil {
					t.Fatalf("asking for round %d: %v", round+1, err)
				}
			}
			holdingAt(t, run, pipeline.StageReview)
			if _, parked := run.Record.Park.Get(); parked {
				t.Fatalf("a person's rounds parked the run: %+v", run.Record.Park)
			}
			err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
				Run: run.Record.ID, Answer: string(pipeline.OutcomeFixRequested),
				Fix: &pipeline.FixRequest{Selected: []string{"scope"}},
			}, &run)
			if err == nil {
				t.Fatal("a round past the bound was taken")
			}
			if rounds := fixRounds(agent); len(rounds) != pipeline.RequestedFixRounds {
				t.Fatalf("the person's rounds invoked the fixer %d times, want %d", len(rounds), pipeline.RequestedFixRounds)
			}
		})
	})
}

// A service that stopped between writing standing consent's hold row and
// applying the answer leaves the row open, and the next service finishes it
// from the run's history before serving, without giving the answer: P6's
// refuse-and-report, applied to the record. Three histories are put where
// such a service would have left them, each by driving the run with the real
// executor and then writing the open row by hand, which is the one thing the
// stopped service did and did not finish:
//
//   - no answer after the decision: the row is finished as not applied, the
//     run stays held for a person, and consent asked again does not take the
//     decision a second time;
//   - the answer the row was written for: the row is finished with it, which
//     is the positive control that the check reads the history rather than
//     always refusing;
//   - another answer: the row is finished as not applied, naming it.
func TestRecoveryFinishesAStandingConsentRowFromTheRunsHistory(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)

	// held starts a run without consent and stops it at the review's
	// decision, then answers it with answer when that is not empty, over
	// provider, and returns the run and the checkpoint the decision stood
	// at, with the open row written for that checkpoint.
	held := func(t *testing.T, h *home.Home, subject string, agent *standin.Agent, provider *forgetest.Provider,
		fix *pipeline.FixRequest, answer string) (string, int) {
		t.Helper()
		var runID string
		var seq int
		withServiceOptions(t, consentOptions(t, h, agent, provider, nil), func(running serviceUnderTest) {
			run := startRun(t, running.client, subject)
			holdingAt(t, run, pipeline.StageReview)
			runID = run.Record.ID
			records := openRecords(t, h)
			defer func() { _ = records.Close() }()
			latest, err := checkpoints.New(records).Latest(t.Context(), runID)
			if err != nil {
				t.Fatalf("reading the decision's checkpoint: %v", err)
			}
			seq = latest.Seq
			if answer == "" {
				return
			}
			if err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
				Run: runID, Answer: answer, Fix: fix,
			}, &run); err != nil {
				t.Fatalf("answering the decision with %s: %v", answer, err)
			}
		})
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		if _, err := records.RegisterHold(t.Context(), store.Hold{
			Key:     service.StandingConsentHoldKey(runID, seq),
			RunID:   store.Known(runID),
			Subject: "what to do about the findings this change stopped on before it was sent upstream",
		}); err != nil {
			t.Fatalf("writing the open row: %v", err)
		}
		return runID, seq
	}
	finished := func(t *testing.T, h *home.Home, runID string, seq int) string {
		t.Helper()
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		hold, err := records.Hold(t.Context(), service.StandingConsentHoldKey(runID, seq))
		if err != nil {
			t.Fatalf("reading the row: %v", err)
		}
		resolution, done := hold.Resolution.Get()
		if !done {
			t.Fatal("recovery left the row open")
		}
		if by, _ := hold.ResolvedBy.Get(); by != store.ResolvedByStandingConsent() {
			t.Fatalf("the row was finished as %s's", by)
		}
		return resolution
	}
	consents := []string{"rename", "scope"}

	t.Run("no answer was applied", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := reviewWithFindings(t)
		runID, seq := held(t, h, subject, agent, forgetest.New(t, forgetest.Script{}), nil, "")
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(running serviceUnderTest) {
			if got := finished(t, h, runID, seq); !strings.HasPrefix(got, "not applied: ") {
				t.Fatalf("the row was finished with %q, want it to say the answer was not applied", got)
			}
			run, err := startUnderConsent(t, running.client, subject)
			if err != nil {
				t.Fatalf("asking again under standing consent: %v", err)
			}
			holdingAt(t, run, pipeline.StageReview)
			if rounds := fixRounds(agent); len(rounds) != 0 {
				t.Fatalf("consent took the decision a second time: %d round(s)", len(rounds))
			}
		})
	})

	t.Run("the row's answer was applied", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := reviewWithFindings(t)
		runID, seq := held(t, h, subject, agent, forgetest.New(t, forgetest.Script{}),
			&pipeline.FixRequest{Selected: consents}, string(pipeline.OutcomeFixRequested))
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(serviceUnderTest) {
			want := string(pipeline.OutcomeFixRequested) + ": " + strings.Join(consents, ", ")
			if got := finished(t, h, runID, seq); got != want {
				t.Fatalf("the row was finished with %q, want %q", got, want)
			}
		})
	})

	t.Run("another answer was applied", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := consentingSubject(t, h, allowsConsent)
		agent := reviewWithFindings(t)
		runID, seq := held(t, h, subject, agent, mergeableProvider(t), nil, string(pipeline.OutcomeApproved))
		withServiceOptions(t, consentOptions(t, h, agent, forgetest.New(t, forgetest.Script{}), nil), func(serviceUnderTest) {
			got := finished(t, h, runID, seq)
			if !strings.HasPrefix(got, "not applied: ") || !strings.Contains(got, string(pipeline.OutcomeApproved)) {
				t.Fatalf("the row was finished with %q, want it to say another answer, approved, was taken", got)
			}
		})
	})
}

// run.start given a task is for standing consent alone and selects its run
// one way: a request that also names a working copy, one that asks for no
// consent, and one carrying an input that starts a run are each refused as
// invalid before any task is looked up, so the refusal holds even for a task
// nobody recorded. The control is the same request with only the task and
// consent, which reaches the lookup and is refused there instead, as a task
// the home has no record of.
func TestAStartGivenATaskIsForStandingConsentAlone(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	withService(t, h, func(running serviceUnderTest) {
		start := func(r machine.StartRequest) error {
			var run machine.Run
			return running.client.Call(t.Context(), ipc.MethodRunStart, r, &run)
		}
		for name, r := range map[string]machine.StartRequest{
			"a working copy too": {Task: "nobody", StandingConsent: true, Working: machine.Working{WorkingPath: t.TempDir()}},
			"no consent":         {Task: "nobody"},
			"an intent":          {Task: "nobody", StandingConsent: true, Intent: "something"},
			"a skip":             {Task: "nobody", StandingConsent: true, Skip: []string{"lint"}},
			"a reduced path":     {Task: "nobody", StandingConsent: true, ReducedPath: true},
		} {
			if err := start(r); !errors.Is(err, ipc.ErrInvalidRequest) {
				t.Errorf("a start given a task with %s came to %v, want an invalid request", name, err)
			}
		}
		if err := start(machine.StartRequest{Task: "nobody", StandingConsent: true}); !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("consent given a task nobody recorded came to %v, want the lookup's refusal", err)
		}
	})
}
