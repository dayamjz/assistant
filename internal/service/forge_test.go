package service_test

import (
	"context"
	"errors"
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
	"github.com/dayamjz/assistant/internal/taskstate"
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

// A run whose record names a gitlab.com upstream starts, and carries the
// GitLab specifier, host and all, read out of the record and not out of the
// checkout, whose origin names the github.com repository with the same path
// on purpose: a run that read the checkout, or that dropped the host, would
// carry a specifier the GitHub adapter would open on github.com (P1).
func TestARunWhoseUpstreamIsOnGitLabCarriesTheGitLabSpecifier(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	const (
		recorded = "https://gitlab.com/dayamjz/assistant.git"
		ambient  = "https://github.com/dayamjz/assistant.git"
		want     = "gitlab.com/dayamjz/assistant"
	)
	if seen := observeForgeRepository(t, recorded, ambient); seen != want {
		t.Fatalf("the run acts on %q, want %q", seen, want)
	}
}

// TestARunWhoseUpstreamNamesAnUnsupportedHostIsRefusedAtStart holds issue
// #283's second decision: a network host this build cannot address is refused
// rather than walked to the pull request stage and stalled. The branch may
// already hold a pending row when begin learns the host is unsupported; that
// row must end before the refusal returns, so a later start does not attach to
// a never-executed run as executing.
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

	records := openRecords(t, h)
	defer records.Close()
	all, err := records.RunsForRepository(t.Context(), "subject")
	if err != nil {
		t.Fatalf("listing runs after the refusal: %v", err)
	}
	for _, run := range all {
		switch run.Status {
		case store.RunPending, store.RunRunning, store.RunHeld:
			t.Fatalf("unsupported-host refusal left unfinished run %s in status %s", run.ID, run.Status)
		}
	}
}

// unsupportedHostSubject records the subject's repository with an upstream on
// a host this build does not address, so every run started on it is refused
// by begin's code-host decision after the run is claimed, and queues a
// delivery task behind a blocker on the subject's branch.
func unsupportedHostSubject(t *testing.T) (h *home.Home, subject, blocker, successor string) {
	t.Helper()
	const elsewhere = "https://gitlab.example.invalid/dayamjz/assistant.git"
	h = newHome(t)
	subject = newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	recordRepositoryWithUpstream(t, h, subject, elsewhere)
	git(t, gateRepositoryOf(t, h, subject), "config", "url."+local+".insteadOf", elsewhere)
	blocker, successor = queuedBehindABlocker(t, h, store.Repository{ID: "subject"})
	return h, subject, blocker, successor
}

// A run refused on its code host releases the task queued behind the delivery
// task the run was attributed to, without a restart, on the configuration
// refusal's terms in TestAConfigurationRefusalReleasesTheTaskQueuedBehindIt:
// both refusals end through one helper, and this is the one that used to end
// the record and release nothing. The hook holds the run pending so the test
// sees the successor Queued behind a live run first, which is what makes the
// later Dispatched this refusal's release rather than the one at open.
func TestACodeHostRefusalReleasesTheTaskQueuedBehindIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h, subject, blocker, successor := unsupportedHostSubject(t)

	withService(t, h, func(running serviceUnderTest) {
		reached, proceed := make(chan struct{}), make(chan struct{})
		running.service.SetBeforeRefusalEndForTest(func() {
			close(reached)
			<-proceed
		})
		defer running.service.SetBeforeRefusalEndForTest(nil)
		errCh := make(chan error, 1)
		go func() {
			_, err := startRunErr(t, running.client, subject)
			errCh <- err
		}()
		<-reached
		if record := onlyRun(t, h); record.Status != store.RunPending {
			close(proceed)
			t.Fatalf("before the refusal's ending the run is recorded as %s, want pending", record.Status)
		}
		blockerLive := taskState(t, running.client, blocker)
		successorLive := taskState(t, running.client, successor)
		close(proceed)
		if blockerLive.State != taskstate.Validating || blockerLive.Source != taskstate.SourceRun {
			t.Fatalf("with its run live the blocker resolves %s from %s, want validating from its run", blockerLive.State, blockerLive.Source)
		}
		if successorLive.State != taskstate.Queued {
			t.Fatalf("with the blocker's run live the successor resolves %s, want queued", successorLive.State)
		}
		if err := <-errCh; err == nil || !strings.Contains(err.Error(), service.ErrUnsupportedHost.Error()) {
			t.Fatalf("starting a run on an unsupported host answered %v, want ErrUnsupportedHost", err)
		} else if strings.Contains(err.Error(), "could not be ended") {
			t.Fatalf("a refusal whose run was ended reports it unended: %v", err)
		}
		if record := onlyRun(t, h); record.Status != store.RunTerminated {
			t.Fatalf("the refused run is recorded as %s, want it ended", record.Status)
		}
		if got := taskState(t, running.client, blocker); got.State != taskstate.Failed {
			t.Fatalf("with its run ended the blocker resolves %s, want failed", got.State)
		}
		if got := taskState(t, running.client, successor); got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the blocker cleared the successor resolves %s from %s, want dispatched from its release",
				got.State, got.Source)
		}
	})
}

// A refused run whose record cannot then be ended is reported as unended, not
// as a clean refusal. The failure is one the store really produces: before
// the ending, the hook moves the run out of every status Terminate may leave
// (to failed, through the store's own anchored transition), so the move to
// terminated is refused with a *store.RunStatusError. The answer names both
// the refusal and the ending's failure, the row keeps the status the store
// read, and the successor stays Queued, because nothing is released on an
// ending that did not happen.
func TestARefusedRunThatCannotBeEndedIsReportedUnended(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h, subject, _, successor := unsupportedHostSubject(t)

	withService(t, h, func(running serviceUnderTest) {
		moved := make(chan error, 1)
		running.service.SetBeforeRefusalEndForTest(func() {
			ctx := context.Background()
			records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
			if err != nil {
				moved <- err
				return
			}
			defer func() { _ = records.Close() }()
			runs, err := records.RunsForRepository(ctx, "subject")
			if err == nil && len(runs) != 1 {
				err = errors.New("want exactly one run to move")
			}
			if err == nil {
				_, err = records.TransitionRun(ctx, runs[0].ID, []store.RunStatus{store.RunPending}, store.RunFailed)
			}
			moved <- err
		})
		defer running.service.SetBeforeRefusalEndForTest(nil)
		_, err := startRunErr(t, running.client, subject)
		if m := <-moved; m != nil {
			t.Fatalf("moving the run before its ending: %v", m)
		}
		if err == nil {
			t.Fatal("a run on an unsupported host was started rather than refused")
		}
		for _, want := range []string{"could not be ended", service.ErrUnsupportedHost.Error(), string(store.RunFailed)} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the answer for a refused run left unended does not carry %q: %v", want, err)
			}
		}
		if record := onlyRun(t, h); record.Status != store.RunFailed {
			t.Fatalf("the run is recorded as %s, want the status the store read left as it stood", record.Status)
		}
		if got := taskState(t, running.client, successor); got.State != taskstate.Queued {
			t.Fatalf("after an ending that did not happen the successor resolves %s, want queued", got.State)
		}
	})
}

// TestARunWhoseUpstreamIsALocalPathCarriesNoCodeHost is the local half of
// #283: the run starts, carries an empty forge repository, and records
// no_code_host on the run row so listings phrase a completion as pushed.
func TestARunWhoseUpstreamIsALocalPathCarriesNoCodeHost(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)

	subject := newSubject(t)
	local := git(t, subject, "remote", "get-url", "origin")
	seen, record := observeForgeRepositoryAndRun(t, local, local)
	if seen != "" {
		t.Fatalf("a local-upstream run acts on %q, want nothing on the code host", seen)
	}
	if noHost, ok := record.NoCodeHost.Get(); !ok || !noHost {
		t.Fatalf("local-upstream run NoCodeHost = %v, %v; want known true", noHost, ok)
	}
}

// observeForgeRepository starts one run of a subject repository whose record
// names upstream and whose checkout's origin remote names remote, and returns
// the code host repository the run's first stage was handed.
func observeForgeRepository(t *testing.T, upstream, remote string) string {
	t.Helper()
	seen, _ := observeForgeRepositoryAndRun(t, upstream, remote)
	return seen
}

// observeForgeRepositoryAndRun is observeForgeRepository and the run row as
// stored after begin, so a caller can assert durable facts begin wrote.
//
// It reads the forge repository out of a real run: the stage body is placed
// through the service's own stage hook, so what it sees is what the service
// put in the run's state and not what a helper computed alongside it.
//
// A run fetches the recorded upstream now - the trusted configuration copy is
// read at a freshly fetched default branch - and the URLs these tests record
// name hosts nothing here may reach, so the gate repository is given git's
// own url.insteadOf rewrite pointing the recorded URL at the subject's local
// upstream. What the run derives its code host repository from is the
// record's URL as written, which is the fact under test; where the bytes of a
// fetch of it come from is what the rewrite decides, and that keeps this test
// off the network without weakening what it observes.
func observeForgeRepositoryAndRun(t *testing.T, upstream, remote string) (string, store.Run) {
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
	var runID string
	withServiceOptions(t, opts, func(under serviceUnderTest) {
		// Only the first stage is under test here, and every stage after it
		// would reach for the upstream this record names, which is a real code
		// host. Skipping them keeps this test off the network.
		run := startRunSkipping(t, under.client, subject, afterIntent()...)
		runID = run.Record.ID
		select {
		case got, read = <-seen:
		default:
		}
	})
	if !read {
		t.Fatal("the run never reached the stage that reads the repository it acts on")
	}
	records := openRecords(t, h)
	defer records.Close()
	record, err := records.Run(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading the run after start: %v", err)
	}
	return got, record
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
