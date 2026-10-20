package service_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// plantTrustedDocument puts a repository configuration document on the
// subject's default branch and pushes it to the upstream, which is where a
// run reads the trusted copy from, then puts the checkout back on the branch
// it was standing on. The trusted copy is the upstream's, so the push is the
// step that makes the document one.
func plantTrustedDocument(t *testing.T, subject, document string) {
	t.Helper()
	standing := git(t, subject, "rev-parse", "--abbrev-ref", "HEAD")
	git(t, subject, "checkout", "--quiet", "main")
	if err := writeFile(filepath.Join(subject, config.RepositoryDocument), document); err != nil {
		t.Fatalf("writing the trusted document: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "configure the repository")
	git(t, subject, "push", "--quiet", "origin", "main")
	git(t, subject, "checkout", "--quiet", standing)
}

// plantPushedDocument puts a repository configuration document on the branch
// the subject stands on, which is the copy a run of that branch reads as the
// pushed layer.
func plantPushedDocument(t *testing.T, subject, document string) {
	t.Helper()
	if err := writeFile(filepath.Join(subject, config.RepositoryDocument), document); err != nil {
		t.Fatalf("writing the pushed document: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "carry a configuration document on the branch")
}

// hermeticCommand is a command a test may let a run execute: it is on every
// platform this suite runs on, reads nothing of the subject, and exits zero.
const hermeticCommand = "go version"

// TestARunResolvesTheTrustedRepositoryConfiguration is PRD section 10's
// repository layer reaching a run: the trusted copy's commands.test is what
// the test stage runs, so a run whose repository configures one no longer
// holds there, and the record's configuration digest is the run's own
// resolution rather than the operator-layer placeholder.
func TestARunResolvesTheTrustedRepositoryConfiguration(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	plantTrustedDocument(t, subject, `{"commands": {"test": "`+hermeticCommand+`"}}`)

	withService(t, h, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		// With commands.test configured by the trusted copy, the test stage
		// runs it and passes, so the first hold is the first stage the
		// configuration still leaves holding: the document stage.
		holdingAt(t, run, pipeline.StageDocument)
		if run.Record.ConfigDigest == "" {
			t.Fatal("the run records no configuration digest")
		}
		// The record carries the resolution's other two facts, and it arrived
		// here over the wire, so the structured surface carries them too. A
		// resolution that dropped nothing is a known empty list, which is a
		// different fact from the unknown a run keeps until it resolves.
		if rejected, known := run.Record.ConfigRejections.Get(); !known || len(rejected) != 0 {
			t.Fatalf("ConfigRejections = %v (known=%v), want a known empty list", rejected, known)
		}
		if name, known := run.Record.ResolvedAgent.Get(); !known || name == "" {
			t.Fatalf("ResolvedAgent = %v, want the agent the run resolved", run.Record.ResolvedAgent)
		}
		if skipped, known := run.Record.ResolvedSkipped.Get(); !known || len(skipped) != 0 {
			t.Fatalf("ResolvedSkipped = %v (known=%v), want a known empty list for a first entry that ran", skipped, known)
		}

		// A second run after the trusted document changes records a different
		// digest, which is what makes the record trace to the documents the
		// run actually resolved rather than to a service-wide placeholder.
		plantTrustedDocument(t, subject, `{"commands": {"test": "`+hermeticCommand+`", "lint": "`+hermeticCommand+`"}}`)
		commitOn(t, subject, "second-change", "second.txt")
		second := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		if second.Record.ConfigDigest == run.Record.ConfigDigest {
			t.Fatalf("two runs over different trusted documents record one digest, %s", run.Record.ConfigDigest)
		}
	})
}

// A run's record names the agent its resolution chose and every entry the
// resolution passed over before it, with the stable reason, and the record
// arrives over the wire, so the structured surface carries it. The operator
// lists an adapter whose executable is missing ahead of the scripted one, so
// the first entry is passed over as one that cannot run here; the error text
// and the entry's arguments are not on the record.
func TestARunRecordsWhatItsAgentResolutionPassedOver(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"agent": ["codex --model gone", "claude"]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := options(t, h)
	runner := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
	}}}).Runner()
	missing := agents.CodexFactory(agents.WithCodexBinary(filepath.Join(t.TempDir(), "no-codex-here")))
	o.Catalog = agents.NewCatalog(missing, fixedFactory{runner: runner}, cursorFactory{runner: runner})
	withServiceOptions(t, o, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		if name, _ := run.Record.ResolvedAgent.Get(); name != runner.Name() {
			t.Fatalf("ResolvedAgent = %v, want %s", run.Record.ResolvedAgent, runner.Name())
		}
		want := []store.SkippedAgent{{Agent: "codex", Reason: agents.SkipCannotRun}}
		if skipped, known := run.Record.ResolvedSkipped.Get(); !known || !slices.Equal(skipped, want) {
			t.Fatalf("ResolvedSkipped = %v (known=%v), want %v", skipped, known, want)
		}
	})
}

// A run refused on configuration releases the task queued behind the delivery
// task the run was attributed to on the same branch, without a service
// restart. While the run is still live the successor stays Queued.
func TestAConfigurationRefusalReleasesTheTaskQueuedBehindIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	blocker, successor := queuedBehindABlocker(t, h, repository)
	plantTrustedDocument(t, subject, `{"commands": {"test": "go test ./...",}}`)

	withService(t, h, func(running serviceUnderTest) {
		if got := taskState(t, running.client, successor); got.State != taskstate.Queued {
			t.Fatalf("before any run the successor resolves %s", got.State)
		}
		var arrived sync.WaitGroup
		arrived.Add(2)
		running.service.SetBeforeRefusalEndForTest(func() {
			arrived.Done()
			arrived.Wait()
		})
		defer running.service.SetBeforeRefusalEndForTest(nil)
		events, err := running.client.Subscribe(t.Context(), machine.SubscribeRequest{})
		if err != nil {
			t.Fatalf("subscribing to the service's events: %v", err)
		}
		defer events.Close()
		errCh := make(chan error, 1)
		go func() {
			_, err := startRunErr(t, running.client, subject)
			errCh <- err
		}()
		deadline := time.Now().Add(30 * time.Second)
		var runID string
		for runID == "" {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for the run to be recorded")
			}
			records := openRecords(t, h)
			repositories, err := records.Repositories(t.Context())
			if err != nil {
				_ = records.Close()
				t.Fatalf("listing repositories: %v", err)
			}
			for _, repository := range repositories {
				found, err := records.RunsForRepository(t.Context(), repository.ID)
				if err != nil {
					_ = records.Close()
					t.Fatalf("listing runs: %v", err)
				}
				if len(found) == 1 {
					runID = found[0].ID
				}
			}
			_ = records.Close()
			if runID == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
		awaitRunStatus(t, running.client, events, runID, store.RunPending)
		if got := taskState(t, running.client, blocker); got.State != taskstate.Validating || got.Source != taskstate.SourceRun {
			t.Fatalf("with its run live the blocker resolves %s from %s, want validating from its run", got.State, got.Source)
		}
		if got := taskState(t, running.client, successor); got.State != taskstate.Queued {
			t.Fatalf("with the blocker's run live the successor resolves %s, want queued", got.State)
		}
		arrived.Done()
		if err := <-errCh; err == nil {
			t.Fatal("a run under an unparseable trusted configuration was started rather than refused")
		} else if !strings.Contains(err.Error(), "trusted configuration") {
			t.Fatalf("the refusal does not name the trusted configuration: %v", err)
		}
		record := onlyRun(t, h)
		if record.Status != store.RunTerminated {
			t.Fatalf("the refused run is recorded as %s, want it ended", record.Status)
		}
		awaitRunStatus(t, running.client, events, runID, store.RunTerminated)
		if got := taskState(t, running.client, blocker); got.State != taskstate.Failed {
			t.Fatalf("with its run ended the blocker resolves %s, want failed", got.State)
		}
		got := taskState(t, running.client, successor)
		if got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the blocker cleared the successor resolves %s from %s, want dispatched from its release",
				got.State, got.Source)
		}
	})
}

// TestAnUnreadableTrustedConfigurationStopsTheRunBeforeLaunch is PRD section
// 10's abort: a trusted document that cannot be parsed stops the run before
// any stage body runs, rather than the run guessing at defaults, and the
// refusal reaches the caller naming the trusted copy.
func TestAnUnreadableTrustedConfigurationStopsTheRunBeforeLaunch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	// A trailing comma: valid to a reader, refused by a JSON decoder.
	plantTrustedDocument(t, subject, `{"commands": {"test": "go test ./...",}}`)

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
		events, err := running.client.Subscribe(t.Context(), machine.SubscribeRequest{})
		if err != nil {
			t.Fatalf("subscribing to the service's events: %v", err)
		}
		defer events.Close()
		_, err = startRunErr(t, running.client, subject)
		if err == nil {
			t.Fatal("a run under an unparseable trusted configuration was started rather than refused")
		}
		if !strings.Contains(err.Error(), "trusted configuration") {
			t.Fatalf("the refusal does not name the trusted configuration: %v", err)
		}
		if got := bodies.Load(); got != 0 {
			t.Fatalf("%d stage bodies ran under a trusted configuration nobody could read", got)
		}
		// Ended rather than left pending: a pending run with no checkpoint
		// can never be resumed, and a later start would attach to it.
		record := onlyRun(t, h)
		if record.Status != store.RunTerminated {
			t.Fatalf("the refused run is recorded as %s, want it ended", record.Status)
		}
		awaitRunStatus(t, running.client, events, record.ID, store.RunTerminated)
	})
}

// TestAPushedConfigurationDocumentThatDoesNotParseIsRefused is section 10's
// parse-time rule on the untrusted copy: a broken document surfaces before it
// merges, even though its keys would mostly have been dropped. The refused run
// is ended rather than left pending, so once the branch carries a document
// that parses, the next start on the branch is a fresh run rather than an
// attach to one nothing can resume.
func TestAPushedConfigurationDocumentThatDoesNotParseIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	plantPushedDocument(t, subject, `{"ignore_patterns": ["vendor/**",]}`)

	withService(t, h, func(running serviceUnderTest) {
		_, err := startRunErr(t, running.client, subject)
		if err == nil {
			t.Fatal("a run carrying an unparseable pushed document was started rather than refused")
		}
		if !strings.Contains(err.Error(), "pushed configuration") {
			t.Fatalf("the refusal does not name the pushed copy: %v", err)
		}
		refused := onlyRun(t, h)
		if refused.Status != store.RunTerminated {
			t.Fatalf("the refused run is recorded as %s, want it ended", refused.Status)
		}

		plantPushedDocument(t, subject, `{"ignore_patterns": ["vendor/**"]}`)
		fresh := startRun(t, running.client, subject)
		if fresh.Record.ID == refused.ID {
			t.Fatalf("the start after the document was fixed attached to the refused run %s", refused.ID)
		}
		if fresh.Record.Status == store.RunPending || fresh.Record.Status == store.RunTerminated {
			t.Fatalf("the fresh run is recorded as %s, want it walked", fresh.Record.Status)
		}
	})
}

// TestAPushedCommandDoesNotDisplaceTheTrustedOne is the trust boundary on the
// wire a run actually takes: the branch under validation carries a document
// naming a command that would fail loudly, the trusted copy names one that
// passes, and the run's test stage runs the trusted one. A run that read the
// pushed copy as trusted would fail its test stage on a command that is not
// there; instead the run passes that stage and holds at the next.
func TestAPushedCommandDoesNotDisplaceTheTrustedOne(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	plantTrustedDocument(t, subject, `{"commands": {"test": "`+hermeticCommand+`"}}`)
	// The branch builds on the configured default branch, so the run's rebase
	// replays its edit of the document cleanly rather than meeting a conflict
	// this test is not about.
	git(t, subject, "rebase", "--quiet", "main")
	plantPushedDocument(t, subject, `{"commands": {"test": "no-such-command-anywhere --fail"}}`)

	withService(t, h, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		holdingAt(t, run, pipeline.StageDocument)
		view := stageView(t, run, pipeline.StageTest)
		if view.Outcome != pipeline.OutcomePassed {
			t.Fatalf("the test stage came back %s; the trusted command passes, so a run that read "+
				"the pushed copy as trusted is the likely cause", view.Outcome)
		}
		// The displaced key is on the run's record, so the author who set it
		// learns it had no effect from the run rather than from the service
		// log; the record arrived here over the wire, so the structured
		// surface carries the same lines. The rejection text is
		// config.Rejection's rendering, held here to the two facts an author
		// acts on: which key, and that the pushed layer is what lost it.
		rejected, known := run.Record.ConfigRejections.Get()
		if !known || len(rejected) == 0 {
			t.Fatalf("ConfigRejections = %v (known=%v), want the dropped pushed key reported", rejected, known)
		}
		var namesTheKey bool
		for _, line := range rejected {
			if strings.Contains(line, "commands.test") && strings.Contains(line, "pushed") {
				namesTheKey = true
			}
		}
		if !namesTheKey {
			t.Fatalf("no rejection names the pushed commands.test: %v", rejected)
		}
		if name, _ := run.Record.ResolvedAgent.Get(); name != "claude" {
			t.Fatalf("ResolvedAgent = %v, want the resolved stand-in's name", run.Record.ResolvedAgent)
		}
	})
}

// TestARepositoryAgentThisBuildCannotHonourIsRefused pins the stated
// limitation as a refusal rather than a silent drop: this build resolves one
// agent per service, from the operator's layer, so a trusted document asking
// for a different agent list stops the run before launch instead of the run
// quietly using an agent its repository did not ask for.
func TestARepositoryAgentThisBuildCannotHonourIsRefused(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	plantTrustedDocument(t, subject, `{"agent": "some-other-agent"}`)

	withService(t, h, func(running serviceUnderTest) {
		_, err := startRunErr(t, running.client, subject)
		if err == nil {
			t.Fatal("a run whose repository pins a different agent was started rather than refused")
		}
		if !strings.Contains(err.Error(), "one agent per service") {
			t.Fatalf("the refusal does not state the limitation: %v", err)
		}
	})
}
