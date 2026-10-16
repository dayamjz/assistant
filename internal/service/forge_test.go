package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestTheRepositoryAPullRequestWouldLandInComesFromTheRunsOwnRecord is what
// stands between a run and a pull request opened somewhere nobody asked for.
//
// PRD principle P1 makes the push to the gate the consent boundary for the
// pull request that run opens, and nothing else implies that consent. Opening
// a pull request is outward-facing and not undoable, so where it lands may not
// come from a caller's request, from configuration, or from whichever remote a
// working copy happens to carry. It is derived from the upstream URL
// on this run's repository row, which is the record PRD section 8 makes
// authoritative for it, and the derivation is in the one place that has the
// record and cannot be skipped by a call site.
//
// The subject working copy is given a git remote naming a different repository
// on purpose. If anything downstream resolved a repository from the checkout
// rather than from the record, the run would carry that one, and the pull
// request stage would open against it. That is the failure this catches, and
// it is why the two are set to disagree rather than to match.
func TestTheRepositoryAPullRequestWouldLandInComesFromTheRunsOwnRecord(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	const (
		recorded = "https://github.com/dayamjz/assistant.git"
		ambient  = "https://github.com/someone-else/not-this-one.git"
		want     = "dayamjz/assistant"
	)
	seen := observeForgeRepository(t, recorded, ambient)

	if seen != want {
		t.Fatalf("the run acts on %q, want %q: a pull request would be opened against a repository "+
			"the run's own record does not name", seen, want)
	}
}

// TestARunWhoseUpstreamNamesAnUnsupportedHostIsRefusedAtStart holds issue
// #283's second decision: a network host this build cannot address is refused
// before anything is recorded as running, rather than walked to the pull
// request stage and stalled.
func TestARunWhoseUpstreamNamesAnUnsupportedHostIsRefusedAtStart(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)

	const elsewhere = "https://gitlab.example.invalid/dayamjz/assistant.git"
	h := newHome(t)
	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, elsewhere)
	git(t, gateRepositoryOf(t, h, subject), "config", "url."+local+".insteadOf", elsewhere)

	withService(t, h, func(under serviceUnderTest) {
		err := under.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
			Working: machine.Working{WorkingPath: subject},
			Intent:  "a change that should not start on an unsupported host",
		}, &machine.Run{})
		if err == nil || !strings.Contains(err.Error(), service.ErrUnsupportedHost.Error()) {
			t.Fatalf("starting a run against %q: %v, want an error naming ErrUnsupportedHost", elsewhere, err)
		}
	})
}

// TestARunWhoseUpstreamIsALocalPathCarriesNoCodeHost is the local half of
// #283: the run starts, carries an empty forge repository, and marks
// no_code_host so the pull request and checks stages skip.
func TestARunWhoseUpstreamIsALocalPathCarriesNoCodeHost(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)

	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	seen := observeForgeRepository(t, local, local)
	if seen != "" {
		t.Fatalf("a local-upstream run acts on %q, want nothing on the code host", seen)
	}
}

// observeForgeRepository starts one run of a subject repository whose record
// names upstream and whose checkout's origin remote names remote, and returns
// the code host repository the run's first stage was handed.
//
// It reads the value out of a real run: the stage body is placed through the
// service's own stage hook, so what it sees is what the service put in the
// run's state and not what a helper computed alongside it.
//
// A run fetches the recorded upstream now - the trusted configuration copy is
// read at a freshly fetched default branch - and the URLs these tests record
// name hosts nothing here may reach, so the gate repository is given git's
// own url.insteadOf rewrite pointing the recorded URL at the subject's local
// upstream. What the run derives its code host repository from is the
// record's URL as written, which is the fact under test; where the bytes of a
// fetch of it come from is what the rewrite decides, and that keeps this test
// off the network without weakening what it observes.
func observeForgeRepository(t *testing.T, upstream, remote string) string {
	t.Helper()

	h := newHome(t)
	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	// newSubject gives the checkout an origin of its own, and this test is
	// about the run ignoring it, so the ambient remote replaces that one rather
	// than being added beside it.
	git(t, subject, "remote", "set-url", "origin", remote)
	recordRepositoryWithUpstream(t, h, subject, upstream)
	git(t, gateRepositoryOf(t, h, subject), "config", "url."+local+".insteadOf", upstream)

	seen := make(chan string, 1)
	opts := options(t, h)
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Intent = observingStage(seen)
		return all
	}

	var got string
	var read bool
	withServiceOptions(t, opts, func(under serviceUnderTest) {
		// Only the first stage is under test here, and every stage after it
		// would reach for the upstream this record names, which is a real code
		// host. Skipping them keeps this test off the network.
		startRunSkipping(t, under.client, subject, afterIntent()...)
		select {
		case got, read = <-seen:
		default:
		}
	})
	if !read {
		t.Fatal("the run never reached the stage that reads the repository it acts on")
	}
	return got
}

// observingStage is a stage body that reports the code host repository the run
// carries and reports nothing else, so a run walks straight past it.
func observingStage(seen chan<- string) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyForgeRepository},
		NewBody: func() pipeline.Body {
			return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
				value, err := in.State.Get(pipeline.KeyForgeRepository)
				if err != nil {
					return pipeline.Output{}, err
				}
				repository, _ := value.Text()
				select {
				case seen <- repository:
				default:
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "read the repository this run acts on",
				}}, nil
			}
		},
	}
}

// recordRepositoryWithUpstream writes the repository record a run needs,
// naming the upstream the code host repository is derived from.
func recordRepositoryWithUpstream(t *testing.T, h *home.Home, workingPath, upstream string) {
	t.Helper()
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
		WorkingPath:   workingPath,
		UpstreamURL:   upstream,
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	// The gate is the other half of what assistant init writes, and a run
	// cannot start without one: its isolated copy is cut from the gate's
	// repository. recordRepository says the same thing at more length.
	if _, err := gate.Initialize(t.Context(), gate.Spec{
		Home:        h.Root(),
		WorkingPath: workingPath,
		Command:     testCommand(t),
	}, gate.WithIndex(records)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	identifyGateRepository(t, h, workingPath)
}
