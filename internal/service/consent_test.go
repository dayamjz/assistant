package service_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
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

// consentUpstream is the code host repository a consenting subject's record
// names, so the pull request stage has one to open a provider on. The gate
// repository rewrites it to the subject's local upstream, so every fetch and
// push the run makes stays on this machine.
const consentUpstream = "https://github.com/dayamjz/assistant.git"

// allowsConsent is the trusted document that allows standing consent.
const allowsConsent = `{"allow_standing_consent": true}`

// consentingSubject is a subject whose repository record names consentUpstream
// and whose default branch carries trusted as its repository document, or no
// document when trusted is empty.
func consentingSubject(t *testing.T, h *home.Home, trusted string) string {
	t.Helper()
	subject := newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	if trusted != "" {
		plantTrustedDocument(t, subject, trusted)
	}
	recordRepositoryWithUpstream(t, h, subject, consentUpstream)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	git(t, gateRepo, "config", "url."+local+".insteadOf", consentUpstream)
	return subject
}

// reviewWithFindings is the stand-in a consenting run is driven against: its
// first review reports one fix finding and one ask finding the reviewer
// stated, both located on the path the change touched so the review's
// binding keeps them; every review after that is clean; and every other
// invocation, which is a fix round, says it resolved what it was given.
func reviewWithFindings(t *testing.T) *standin.Agent {
	t.Helper()
	return reviewThen(t, statedFindings()...)
}

// statedFindings is reviewWithFindings' first review: a fix finding and an
// ask finding, each with the action the reviewer stated.
func statedFindings() []findings.Finding {
	located := findings.Location{Path: "work.txt", Line: 1}
	return []findings.Finding{
		{ID: "rename", Severity: findings.SeverityWarning, Action: findings.ActionFix,
			Location: located, Description: "the name says the opposite of what the file holds"},
		{ID: "scope", Severity: findings.SeverityWarning, Action: findings.ActionAsk,
			Location: located, Description: "this widens what the change was asked to do"},
	}
}

// reviewThen is a stand-in whose first review reports first, every review
// after that is clean, and every other invocation, which is a fix round,
// says it resolved what it was given.
func reviewThen(t *testing.T, first ...findings.Finding) *standin.Agent {
	t.Helper()
	return standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Reply: standin.Reviewed("the first review's findings", first...)},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text("resolved the selected findings")},
	}})
}

// fixRounds is every call the agent took that was not a review, which in
// these runs is every fix round.
func fixRounds(agent *standin.Agent) []standin.Call {
	var rounds []standin.Call
	for _, call := range agent.Calls() {
		if !strings.Contains(call.Prompt, "It must be exactly ") {
			rounds = append(rounds, call)
		}
	}
	return rounds
}

// consentOptions is a service whose run walks the real rebase, review, push
// and pull request bodies, over the stand-in and a scripted code host, with
// every other stage reporting and passing. The review is where the findings
// come from, and rebase records the anchor the push is decided on. A non-nil
// lint replaces the lint stage's body, which is how a test reaches the world
// between the rebase and the push.
func consentOptions(t *testing.T, h *home.Home, agent *standin.Agent, provider *forgetest.Provider, lint *pipeline.Implementation) service.Options {
	t.Helper()
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()})
	o.Forge = provider.Host()
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := pipeline.ConstantStages("nothing to report")
		real := stages.All(deps)
		all.Rebase = real.Rebase
		all.Review = real.Review
		all.Push = real.Push
		all.PR = real.PR
		if lint != nil {
			all.Lint = *lint
		}
		return all
	}
	return o
}

// pushedOutOfBand is a lint body that moves the branch under validation on
// the upstream before the push stage reads it, the way a second person's push
// would: the branch is created there at the default branch's tip. It does it
// once and then reports and passes like any other.
func pushedOutOfBand(upstream string) pipeline.Implementation {
	var once sync.Once
	return pipeline.Implementation{NewBody: func() pipeline.Body {
		return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
			var err error
			once.Do(func() {
				out, runErr := exec.CommandContext(ctx, "git", "-C", upstream, "update-ref",
					"refs/heads/work", "refs/heads/main").CombinedOutput()
				if runErr != nil {
					err = fmt.Errorf("moving the upstream branch out of band: %w: %s", runErr, out)
				}
			})
			if err != nil {
				return pipeline.Output{}, err
			}
			return pipeline.Output{Report: findings.Report{Summary: "nothing to report"}}, nil
		}
	}}
}

// startUnderConsent starts a run of the subject's branch with standing
// consent asked for.
func startUnderConsent(t *testing.T, client *ipc.Client, subject string) (machine.Run, error) {
	t.Helper()
	var run machine.Run
	err := client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working:         machine.Working{WorkingPath: subject},
		Intent:          "a change with acceptance criteria stated up front",
		StandingConsent: true,
	}, &run)
	return run, err
}

// consentAnswers is every hold row standing consent recorded for a run, read
// at every checkpoint the run's history holds, in the order of the decisions.
func consentAnswers(t *testing.T, records *store.Store, runID string) []store.Hold {
	t.Helper()
	latest, err := checkpoints.New(records).Latest(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading run %s's position: %v", runID, err)
	}
	var out []store.Hold
	for seq := 1; seq <= latest.Seq; seq++ {
		hold, err := records.Hold(t.Context(), service.StandingConsentHoldKey(runID, seq))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatalf("reading the decision at checkpoint %d: %v", seq, err)
		}
		out = append(out, hold)
	}
	return out
}

// A run whose review reports a fix finding and an ask finding, each with the
// action the reviewer stated, reaches checks-passed with nobody answering it,
// because it runs under standing consent: the review's hold is answered with
// one fix round over both, the hold that round leaves so its result is seen
// is accepted, and the run walks on through the real push and pull request
// bodies. Each answer is recorded as the program's under its own member of
// the closed set and never as a person's or a caller's, and the run record,
// the run listing and the pull request body each say the run ran under
// standing consent.
//
// P1 is cited because consent here is given on purpose, per run, by the flag,
// and is not implied by the push or the key alone.
func TestARunUnderStandingConsentReachesChecksPassedUnattended(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, allowsConsent)
	agent := reviewWithFindings(t)
	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":9}]`)},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/9\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE", Head: "work", Base: "main",
		})},
	})

	withServiceOptions(t, consentOptions(t, h, agent, provider, nil), func(running serviceUnderTest) {
		run, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run under standing consent: %v", err)
		}
		if run.Outcome != machine.OutcomeChecksPassed || run.Record.Status != store.RunPassed {
			t.Fatalf("the run ended %s (%s): %s; want checks-passed with nobody answering it",
				run.Outcome, run.Record.Status, run.Reason)
		}
		if !run.Record.StandingConsent.IsKnown() {
			t.Fatal("the run record does not say the run ran under standing consent")
		}
		// The control for issue #283's owner decision: a repository whose
		// upstream is on the code host is not one with no code host, so its
		// run records none, takes the pull request and checks stages rather
		// than passing them over, and opens its pull request.
		if run.Record.NoCodeHost {
			t.Fatal("the record of a run whose upstream is on the code host says it has none")
		}
		for _, stage := range []pipeline.Stage{pipeline.StagePR, pipeline.StageCI} {
			if view := stageView(t, run, stage); !view.Ran || view.SkipReason != "" {
				t.Fatalf("the %s stage ran %v with skip reason %q, want it run", stage, view.Ran, view.SkipReason)
			}
		}
		created := 0
		for _, call := range provider.Calls() {
			if call.Op == forgetest.OpCreate {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("the code host was asked to create %d pull requests, want one", created)
		}
		if review := stageView(t, run, pipeline.StageReview); review.Outcome != pipeline.OutcomeApproved {
			t.Fatalf("the review stage ended %s, want approved after the round", review.Outcome)
		}

		// One fix round, over every finding that was not a note.
		rounds := fixRounds(agent)
		if len(rounds) != 1 {
			t.Fatalf("the fixer was invoked %d times, want once", len(rounds))
		}
		for _, said := range []string{"the name says the opposite", "this widens what the change"} {
			if !strings.Contains(rounds[0].Prompt, said) {
				t.Fatalf("the round was not asked to resolve %q:\n%s", said, rounds[0].Prompt)
			}
		}

		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		answers := consentAnswers(t, records, run.Record.ID)
		if len(answers) != 2 {
			t.Fatalf("standing consent recorded %d answers, want the fix round and the acceptance of its result: %+v",
				len(answers), answers)
		}
		wantResolutions := []string{string(pipeline.OutcomeFixRequested) + ": ", string(pipeline.OutcomeApproved)}
		for i, hold := range answers {
			by, known := hold.ResolvedBy.Get()
			if !known || by != store.ResolvedByStandingConsent() {
				t.Fatalf("answer %d was recorded as resolved by %v, want the program under standing consent", i, hold.ResolvedBy)
			}
			if by == store.ResolvedByPerson() || by == store.ResolvedByMachineInterface() {
				t.Fatalf("answer %d was recorded as %s's", i, by)
			}
			resolution, _ := hold.Resolution.Get()
			if !strings.HasPrefix(resolution, wantResolutions[i]) {
				t.Fatalf("answer %d was recorded as %q, want it to start %q", i, resolution, wantResolutions[i])
			}
			if runID, _ := hold.RunID.Get(); runID != run.Record.ID {
				t.Fatalf("answer %d names run %q", i, runID)
			}
		}

		var listed machine.Runs
		if err := running.client.Call(t.Context(), ipc.MethodRunsList, machine.RunsRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &listed); err != nil {
			t.Fatalf("listing runs: %v", err)
		}
		if len(listed.Runs) != 1 || !listed.Runs[0].StandingConsent.IsKnown() {
			t.Fatalf("the run listing does not say the run ran under standing consent: %+v", listed.Runs)
		}

		// The push was the real stage's: the branch the upstream now holds is
		// the commit the run validated.
		upstream := git(t, subject, "remote", "get-url", "origin")
		if pushed := git(t, upstream, "rev-parse", "refs/heads/work"); pushed != stageView(t, run, pipeline.StageReview).Report.Revision {
			t.Fatalf("the upstream branch reads %s, want the commit the review approved", pushed)
		}

		body := createdFor(t, provider, "work").Stdin
		if !strings.Contains(body, "This run ran under standing consent") {
			t.Fatalf("the pull request body does not say the run ran under standing consent:\n%s", body)
		}
	})
}

// A push the run cannot verify still holds under standing consent. Between
// the rebase that observed the branch's target and the push, the target is
// moved on the upstream out of band, so the real push stage refuses on P6's
// terms - the target no longer stands where the run observed it, and pushing
// would lose what is there now - and holds. Consent answers the review's
// decisions before it and answers nothing at the push. Status reports the
// run held at that decision and says it runs under standing consent, and no
// answer is recorded for the push.
func TestAPushRefusalStillHoldsUnderStandingConsent(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, allowsConsent)
	upstream := git(t, subject, "remote", "get-url", "origin")
	agent := reviewWithFindings(t)
	provider := forgetest.New(t, forgetest.Script{})
	moved := pushedOutOfBand(upstream)

	withServiceOptions(t, consentOptions(t, h, agent, provider, &moved), func(running serviceUnderTest) {
		run, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run under standing consent: %v", err)
		}
		decision := holdingAt(t, run, pipeline.StagePush)
		refused := false
		for _, f := range decision.Findings {
			refused = refused || (f.ID == "push-refused" && f.Holds())
		}
		if !refused {
			t.Fatalf("the push holds on %+v, want its refusal of a target that moved", decision.Findings)
		}
		if pushed := git(t, upstream, "rev-parse", "refs/heads/work"); pushed != git(t, upstream, "rev-parse", "refs/heads/main") {
			t.Fatalf("the upstream branch reads %s after the refusal, so the push went ahead", pushed)
		}
		// Asking again with the flag attaches, and consent still does not
		// answer the push.
		again, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("attaching under standing consent: %v", err)
		}
		holdingAt(t, again, pipeline.StagePush)

		var status machine.Status
		if err := running.client.Call(t.Context(), ipc.MethodStatus, machine.StatusRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &status); err != nil {
			t.Fatalf("reading status: %v", err)
		}
		if status.ActiveRun == nil || !status.ActiveRun.Record.StandingConsent.IsKnown() {
			t.Fatalf("status does not say the active run runs under standing consent: %+v", status.ActiveRun)
		}
		if status.ActiveRun.Decision == nil || status.ActiveRun.Decision.Stage != pipeline.StagePush.String() {
			t.Fatalf("status reports the run at %+v, want held at the push", status.ActiveRun.Decision)
		}

		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		answers := consentAnswers(t, records, run.Record.ID)
		if len(answers) != 2 {
			t.Fatalf("standing consent recorded %d answers, want the review's two and none at the push: %+v", len(answers), answers)
		}
		for _, hold := range answers {
			if detail := hold.Detail; strings.Contains(detail, pipeline.StagePush.String()+" stage") {
				t.Fatalf("standing consent answered the push: %+v", hold)
			}
		}
	})
}

// The flag is refused, as an invalid request and with nothing recorded, where
// the repository's trusted copy does not allow standing consent, and that
// includes a branch whose own pushed copy sets the key: the key is
// trusted-only, so a pushed branch cannot allow consent for itself. The
// control is the same branch started without the flag, which runs.
func TestStandingConsentIsRefusedWithoutTheTrustedKeyAndAPushedCopyCannotGrantIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, "")
	plantPushedDocument(t, subject, allowsConsent)
	agent := reviewWithFindings(t)
	provider := forgetest.New(t, forgetest.Script{})

	withServiceOptions(t, consentOptions(t, h, agent, provider, nil), func(running serviceUnderTest) {
		_, err := startUnderConsent(t, running.client, subject)
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Fatalf("standing consent on a branch whose own copy sets the key: %v, want an invalid request", err)
		}
		if !strings.Contains(err.Error(), "allow_standing_consent") || !strings.Contains(err.Error(), "main") {
			t.Fatalf("the refusal does not name the key and the branch that would allow it: %v", err)
		}
		var listed machine.Runs
		if err := running.client.Call(t.Context(), ipc.MethodRunsList, machine.RunsRequest{
			Working: machine.Working{WorkingPath: subject},
		}, &listed); err != nil {
			t.Fatalf("listing runs: %v", err)
		}
		if len(listed.Runs) != 0 {
			t.Fatalf("the refusal left %d runs recorded", len(listed.Runs))
		}

		// The control: without the flag the same branch starts, and the run
		// it starts is not under consent, so it stops at the review's
		// decision for a person.
		run := startRunSkipping(t, running.client, subject)
		if run.Record.StandingConsent.IsKnown() {
			t.Fatal("a run started without the flag is under standing consent")
		}
		holdingAt(t, run, pipeline.StageReview)
	})
}

// Consent given to a run already holding reaches that run: the run is started
// without the flag and stops at the review's decision for a person, and asking
// again with the flag attaches, puts that run under standing consent, and
// carries it to checks-passed with the same answers recorded as the
// program's. The flag on an attach is the per-run flag too, so the run it
// names is the one consent is given to, and no second run is started.
func TestStandingConsentGivenToAHeldRunAnswersItsDecision(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, allowsConsent)
	agent := reviewWithFindings(t)
	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant")},
		forgetest.OpList:       {forgetest.Raw("[]"), forgetest.Raw(`[{"number":9}]`)},
		forgetest.OpCreate:     {forgetest.Raw("https://github.com/dayamjz/assistant/pull/9\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE", Head: "work", Base: "main",
		})},
	})

	withServiceOptions(t, consentOptions(t, h, agent, provider, nil), func(running serviceUnderTest) {
		held := startRunSkipping(t, running.client, subject)
		holdingAt(t, held, pipeline.StageReview)
		if held.Record.StandingConsent.IsKnown() {
			t.Fatal("a run started without the flag is under standing consent")
		}

		run, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("attaching under standing consent: %v", err)
		}
		if run.Record.ID != held.Record.ID {
			t.Fatalf("the flag started run %s rather than attaching to %s", run.Record.ID, held.Record.ID)
		}
		if run.Outcome != machine.OutcomeChecksPassed || !run.Record.StandingConsent.IsKnown() {
			t.Fatalf("the attached run ended %s under consent %v: %s", run.Outcome, run.Record.StandingConsent, run.Reason)
		}
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		answers := consentAnswers(t, records, run.Record.ID)
		if len(answers) != 2 {
			t.Fatalf("standing consent recorded %d answers, want two: %+v", len(answers), answers)
		}
		for _, hold := range answers {
			if by, _ := hold.ResolvedBy.Get(); by != store.ResolvedByStandingConsent() {
				t.Fatalf("an answer was recorded as %s's", by)
			}
		}
	})
}
