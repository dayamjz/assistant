package service_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// forkURL is the fork a forked subject's record names, beside consentUpstream.
const forkURL = "https://github.com/someone/assistant.git"

// forkedSubject is a subject whose repository record names consentUpstream as
// its upstream and fork as its fork, and the bare repository the gate
// rewrites the fork to, which holds the default branch as a fork made from
// the upstream would. Both URLs are rewritten in the gate repository to
// repositories on this machine, so every fetch and push a run makes stays
// here.
func forkedSubject(t *testing.T, h *home.Home, fork string) (subject, forkPath string) {
	t.Helper()
	subject = newSubject(t)
	quietMaintenance(t, subject)
	local := git(t, subject, "remote", "get-url", "origin")
	forkPath, err := standin.TempDir("f")
	if err != nil {
		t.Fatalf("making a fork repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(forkPath) })
	git(t, forkPath, "init", "--quiet", "--bare", "-b", "main", ".")
	git(t, subject, "push", "--quiet", forkPath, "main")

	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	service.SeedDatabase(t, h.Database())
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = records.Close() }()
	if _, err := records.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   subject,
		UpstreamURL:   consentUpstream,
		ForkURL:       store.Known(fork),
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	if _, err := gate.Initialize(t.Context(), gate.Spec{
		Home:        h.Root(),
		WorkingPath: subject,
		Command:     testCommand(t),
	}, gate.WithIndex(records)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	identifyGateRepository(t, h, subject)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	git(t, gateRepo, "config", "url."+local+".insteadOf", consentUpstream)
	git(t, gateRepo, "config", "url."+forkPath+".insteadOf", fork)
	return subject, forkPath
}

// cleanReview is a stand-in whose every review is clean.
func cleanReview(t *testing.T) *standin.Agent {
	t.Helper()
	return standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text("resolved the selected findings")},
	}})
}

// startForked starts a run of the subject's branch and returns the refusal
// as it came, for the test whose subject is one.
func startForked(t *testing.T, client *ipc.Client, subject string) (machine.Run, error) {
	t.Helper()
	var run machine.Run
	err := client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working: machine.Working{WorkingPath: subject},
		Intent:  "a change with acceptance criteria stated up front",
	}, &run)
	return run, err
}

// hasBranch reports whether the bare repository at path holds the branch.
func hasBranch(path, branch string) bool {
	return exec.Command("git", "-C", path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// A run of a repository whose record names a fork walks the real rebase,
// review, push and pull request bodies and sends its branch to the fork and
// nowhere else: the fork holds the commit the review approved, and the
// upstream holds no branch of that name. The pull request is proposed to the
// upstream with the fork's owner and the branch as its head, after the code
// host confirmed both repositories, and the run's state carries the fork as
// its own fact. The upstream holding nothing is the control that the push
// went to one place: a run pushing where it always did would leave the
// branch there.
func TestARunWithAForkSendsItsBranchToTheForkAndProposesItToTheUpstream(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject, forkPath := forkedSubject(t, h, forkURL)
	upstream := git(t, subject, "remote", "get-url", "origin")
	provider := forgetest.New(t, forgetest.Script{
		forgetest.OpRepository: {forgetest.Repository("dayamjz/assistant"), forgetest.Repository("someone/assistant")},
		forgetest.OpList: {forgetest.List(), forgetest.List(
			forgetest.ListEntry{Number: 9, HeadOwner: "someone", HeadName: "assistant"})},
		forgetest.OpCreate: {forgetest.Raw("https://github.com/dayamjz/assistant/pull/9\n")},
		forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
			Number: 9, State: "OPEN", Mergeable: "MERGEABLE", Head: "work", Base: "main",
		})},
	})

	withServiceOptions(t, consentOptions(t, h, cleanReview(t), provider, nil), func(running serviceUnderTest) {
		run, err := startForked(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run: %v", err)
		}
		if run.Outcome != machine.OutcomeChecksPassed {
			t.Fatalf("the run ended %s (%s): %s; want checks-passed", run.Outcome, run.Record.Status, run.Reason)
		}
		reviewed := stageView(t, run, pipeline.StageReview).Report.Revision
		if pushed := git(t, forkPath, "rev-parse", "refs/heads/work"); pushed != reviewed {
			t.Fatalf("the fork's branch reads %s, want the commit the review approved, %s", pushed, reviewed)
		}
		if hasBranch(upstream, "work") {
			t.Fatal("the upstream holds the branch, so the run sent it there as well as, or instead of, the fork")
		}

		created := createdFor(t, provider, "someone:work")
		if repo := flagValue(created.Args, "--repo="); repo != "dayamjz/assistant" {
			t.Fatalf("the pull request was proposed to %q, want the upstream", repo)
		}
		var confirmed []string
		for _, c := range provider.Calls() {
			if c.Op == forgetest.OpRepository {
				confirmed = append(confirmed, strings.Join(c.Args, " "))
			}
		}
		if len(confirmed) != 2 || !strings.Contains(confirmed[0], "dayamjz/assistant") || !strings.Contains(confirmed[1], "someone/assistant") {
			t.Fatalf("the pull request was written after confirming %q, want the upstream and then the fork", confirmed)
		}

		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		latest, err := checkpoints.New(records).Latest(t.Context(), run.Record.ID)
		if err != nil {
			t.Fatalf("reading the run's position: %v", err)
		}
		if v, ok := latest.State.Get(string(pipeline.KeyForkRepository)); !ok {
			t.Fatal("the run's state carries no fork")
		} else if fork, _ := v.Text(); fork != "someone/assistant" {
			t.Fatalf("the run's state names the fork %q, want someone/assistant", fork)
		}
	})
}

// The push to a fork is anchored on the fork's branch, observed before the
// work, exactly as a push to the upstream is anchored on the upstream's
// (P6). Between the rebase that observed the fork and the push, the branch is
// created on the fork out of band, the way a second person's push would; the
// real push stage refuses, because what the fork holds now is not what the
// run observed, and holds for a person. Neither the fork's branch nor the
// upstream changed, and nothing was proposed. The control is the test above,
// where the fork was left alone and the same push went through.
func TestAPushToAForkIsAnchoredOnTheForksBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject, forkPath := forkedSubject(t, h, forkURL)
	upstream := git(t, subject, "remote", "get-url", "origin")
	provider := forgetest.New(t, forgetest.Script{})
	moved := pushedOutOfBand(forkPath)

	withServiceOptions(t, consentOptions(t, h, cleanReview(t), provider, &moved), func(running serviceUnderTest) {
		run, err := startForked(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run: %v", err)
		}
		decision := holdingAt(t, run, pipeline.StagePush)
		refused := false
		for _, f := range decision.Findings {
			refused = refused || (f.ID == "push-refused" && f.Holds())
		}
		if !refused {
			t.Fatalf("the push holds on %+v, want its refusal of a fork branch that moved", decision.Findings)
		}
		if pushed := git(t, forkPath, "rev-parse", "refs/heads/work"); pushed != git(t, forkPath, "rev-parse", "refs/heads/main") {
			t.Fatalf("the fork's branch reads %s after the refusal, so the push went ahead", pushed)
		}
		if hasBranch(upstream, "work") {
			t.Fatal("the refused push sent the branch to the upstream instead")
		}
		for _, c := range provider.Calls() {
			if c.Op == forgetest.OpCreate {
				t.Fatalf("a pull request was proposed after the push was refused: %+v", c)
			}
		}
	})
}

// A record naming a fork that is not a repository on the code host is
// refused when the run begins, before anything is sent anywhere, rather than
// pushed to the upstream instead or to the fork with nothing to propose to.
// assistant init does not record such a fork, so the record is written here
// directly, which is the case the begin-time check exists for.
func TestARunWhoseForkDoesNotResolveIsRefusedBeforeItStarts(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject, forkPath := forkedSubject(t, h, "https://example.invalid/someone/assistant.git")
	upstream := git(t, subject, "remote", "get-url", "origin")
	provider := forgetest.New(t, forgetest.Script{})

	withServiceOptions(t, consentOptions(t, h, cleanReview(t), provider, nil), func(running serviceUnderTest) {
		_, err := startForked(t, running.client, subject)
		if err == nil {
			t.Fatal("a run whose fork does not resolve started")
		}
		var wire *ipc.Error
		if !errors.As(err, &wire) || !strings.Contains(wire.Message, "fork") {
			t.Fatalf("the refusal = %v, want one naming the fork", err)
		}
		if hasBranch(forkPath, "work") || hasBranch(upstream, "work") {
			t.Fatal("a refused run sent its branch somewhere")
		}
		if calls := provider.Calls(); len(calls) != 0 {
			t.Fatalf("a refused run reached the code host: %+v", calls)
		}
	})
}
