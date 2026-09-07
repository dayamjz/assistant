package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A run whose segment stopped without settling stands where that segment
// reached it with nothing carrying it on, and this holds the surfaces to
// telling that apart from a run that is moving.
//
// PRD section 9's design constraints on the terminal interface require run
// state to be reflected honestly, and say why: working and dead must be
// visually distinct, because a stall that looks alive is worse than an error.
// The record and the checkpoint are the same for both - running, at the
// position the segment reached - so neither can be read for the difference.
// The fact has one owner, which is the slot a run advances in, and P14 is what
// makes asking that owner the fix rather than inferring it from the two
// records that do not hold it.
//
// The two halves are checked against each other. TestReadsOfARunInsideAStageBody
// AnswerExecuting pins what a run genuinely in flight answers, and this pins
// what one nothing is advancing answers; each asserts the other's answer is
// not the one it got, so neither can pass by both cases collapsing onto one
// rendering again.
//
// It ends by attaching, because rendering the stall honestly and leaving the
// run stuck would trade a visible defect for an invisible one. The action the
// answer names is the one driven here, and it carries the run to its first
// decision.
func TestARunNothingIsAdvancingIsNotReportedAsOneInFlight(t *testing.T) {
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	var calls atomic.Int64
	serveIntentFailingOnce(t, h, &calls)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	// The run is put in the state through the path the product takes to it: a
	// stage body that could not run at all, which stops the segment before
	// anything settles the record.
	started := run(t, h, subject, "--json", "--intent", "add a greeting, with the tradeoffs stated")
	if started.code == machine.ExitOK {
		t.Fatalf("the start whose stage body fails exited ok:\n%s", started.stdout)
	}
	if calls.Load() != 1 {
		t.Fatalf("the stage body ran %d times, want the one failing call this test is about", calls.Load())
	}
	t.Logf("assistant --json --intent ..., whose first stage body failed:\n%s", started.stdout)

	// The start has answered, so the segment has returned and given the run's
	// slot back. Nothing is advancing this run, and nothing settled it either.
	reported := run(t, h, subject, "--json", "status")
	if reported.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s%s", reported.code, reported.stdout, reported.stderr)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(reported.stdout), &status); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, reported.stdout)
	}
	if status.ActiveRun == nil {
		t.Fatalf("status reports no active run for a branch whose run is unfinished:\n%s", reported.stdout)
	}
	t.Logf("assistant --json status, on a run nothing is advancing:\n%s", reported.stdout)
	view := *status.ActiveRun
	assertStalled(t, view, "status")

	// What a person reads names the stall rather than leaving them to infer
	// it, and says what carries the run on.
	human := mustOK(t, run(t, h, subject, "status"))
	t.Logf("assistant status, the same run rendered for a person:\n%s", human.stdout)
	if strings.Contains(human.stdout, advancingRendering) {
		t.Fatalf("the rendering says a segment is running on a run nothing is advancing:\n%s", human.stdout)
	}
	for _, want := range []string{stalledRendering, machine.OutcomeExecuting.NextActionFor(false)} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("the rendering does not say %q:\n%s", want, human.stdout)
		}
	}

	// And the action it named moves the run. The stage body fails once, so
	// this is the resume the answer told the reader to make, reaching the
	// first stage this build holds at.
	carried := decodeRun(t, mustOK(t, run(t, h, subject, "--json")).stdout)
	if carried.Record.ID != view.Record.ID {
		t.Fatalf("attaching answered about run %s, want the stalled one %s", carried.Record.ID, view.Record.ID)
	}
	if carried.Outcome != machine.OutcomeDecision {
		t.Fatalf("attaching to the stalled run answered %s, want the decision it was carried on to", carried.Outcome)
	}
	if carried.Steps <= view.Steps {
		t.Fatalf("attaching left the run at %d steps, where it already stood at %d", carried.Steps, view.Steps)
	}
	if calls.Load() != 2 {
		t.Fatalf("the stage body ran %d times, want the failing call and the resume's", calls.Load())
	}
}

// A caller that gives up ends the segment it was waiting on, and the run it
// was waiting on is not left standing. The service carries it on itself, which
// is the same thing recovery does for a run a restart interrupted, done at the
// moment the run would otherwise be stranded rather than at the next start.
//
// This is the half of the fix that stops the state existing. The other half is
// TestARunNothingIsAdvancingIsNotReportedAsOneInFlight, which is the state a
// read can still find - a segment in flight, a continuation still running, a
// step that failed - reported for what it is.
//
// Nothing calls in after the caller has gone. The run reaching its decision is
// the service's own doing, and the body's second entry is under a context the
// caller no longer holds, which is what the first assertion below establishes:
// it fires after the caller's start has already returned.
func TestARunWhoseCallerGaveUpIsCarriedOnRatherThanStranded(t *testing.T) {
	principles.Cite(t, principles.P14)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)

	inside := make(chan struct{})
	carried := make(chan struct{})
	var entered, resumed sync.Once
	var calls atomic.Int64
	serveIntentLosingItsCaller(t, h, inside, carried, &entered, &resumed, &calls)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	ctx, giveUp := context.WithCancel(context.Background())
	answered := make(chan invocation, 1)
	startInContext(ctx, t, h, subject, answered, "--json", "--intent", "add a greeting, with the tradeoffs stated")
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the start never reached the stage body")
	}

	giveUp()
	select {
	case got := <-answered:
		if got.code == machine.ExitOK {
			t.Fatalf("the start a caller gave up on exited ok:\n%s", got.stdout)
		}
		t.Logf("the start a caller gave up on: code=%s\n%s%s", got.code, got.stdout, got.stderr)
	case <-time.After(30 * time.Second):
		t.Fatal("the start never returned after its caller gave up")
	}

	// Nothing has asked for this run since. It is entered a second time
	// because the service picked it up.
	select {
	case <-carried:
	case <-time.After(30 * time.Second):
		t.Fatalf("the run was left standing after its caller gave up; the stage body ran %d times", calls.Load())
	}

	// And it goes on to where it was going, which a read finds without asking
	// for it. The read is repeated because the continuation is in flight when
	// the body returns, not because the answer is uncertain.
	view := runUntil(t, h, subject, func(v machine.Run) bool { return v.Outcome == machine.OutcomeDecision })
	if view.Advancing {
		t.Fatal("the run reports a segment still running after it reached its decision")
	}
	if calls.Load() != 2 {
		t.Fatalf("the stage body ran %d times, want the caller's and the continuation's", calls.Load())
	}
}

// runUntil reads the branch's status until the run satisfies want, and fails if
// it does not within a bounded wait.
func runUntil(t *testing.T, h *home.Home, subject string, want func(machine.Run) bool) machine.Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last machine.Run
	for time.Now().Before(deadline) {
		reported := mustOK(t, run(t, h, subject, "--json", "status"))
		var status machine.Status
		if err := json.Unmarshal([]byte(reported.stdout), &status); err != nil {
			t.Fatalf("status does not decode: %v\n%s", err, reported.stdout)
		}
		if status.ActiveRun == nil {
			t.Fatalf("status reports no active run:\n%s", reported.stdout)
		}
		last = *status.ActiveRun
		if want(last) {
			t.Logf("assistant --json status, once the run got where it was going:\n%s", reported.stdout)
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the run never got where this test waited for it; it stands at %s, advancing=%v",
		last.Outcome, last.Advancing)
	return machine.Run{}
}

// The two words the human rendering tells the states apart with. They are
// written here rather than derived from the renderer, so a rendering that
// stopped distinguishing them fails this rather than agreeing with itself.
const (
	advancingRendering = "a segment is running"
	stalledRendering   = "stalled - nothing is advancing this run"
)

// assertStalled holds an answer to what a run nothing is advancing has to say:
// where it stands, that nothing is moving it, and what carries it on.
//
// It asserts the in-flight answer is not the one that arrived, which is what
// keeps it from passing again if the two collapse back onto one rendering.
func assertStalled(t *testing.T, view machine.Run, surface string) {
	t.Helper()
	if view.Record.Status != store.RunRunning {
		t.Fatalf("%s reports the record as %s, want running: the segment stopped without settling it",
			surface, view.Record.Status)
	}
	if view.Progress == nil || *view.Progress != graph.StatusRunning {
		t.Fatalf("%s reports progress %v, want the running checkpoint the segment left", surface, view.Progress)
	}
	if view.Outcome != machine.OutcomeExecuting {
		t.Fatalf("%s reports %s for a run standing at a resumable position, want executing", surface, view.Outcome)
	}
	if view.Advancing {
		t.Fatalf("%s reports that something is advancing a run whose segment has already returned", surface)
	}
	if view.NextAction == machine.OutcomeExecuting.NextActionFor(true) {
		t.Fatalf("%s tells a reader to wait on a run nothing is advancing: %s", surface, view.NextAction)
	}
	if view.NextAction != machine.OutcomeExecuting.NextActionFor(false) {
		t.Fatalf("%s says %q about a stalled run, want the action that carries it on", surface, view.NextAction)
	}
	if view.Decision != nil {
		t.Fatalf("%s offers a decision to answer on a run that has none: %+v", surface, view.Decision)
	}
}

// mustOK fails the test unless an invocation succeeded, and returns it.
func mustOK(t *testing.T, got invocation) invocation {
	t.Helper()
	if got.code != machine.ExitOK {
		t.Fatalf("the command exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	return got
}

// startInContext drives the surface under a context the test can cancel, which
// is how a caller gives up on a call it is blocked in. Everything the testing
// package is not safe to be told from another goroutine is settled before that
// goroutine starts.
func startInContext(ctx context.Context, t *testing.T, h *home.Home, workingDir string, answered chan<- invocation, args ...string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	line := append([]string{"--home", h.Root()}, args...)
	go func() {
		answered <- runArgsIn(ctx, executable, workingDir, line...)
	}()
}

// serveIntentFailingOnce serves a home whose first stage body fails the first
// time it runs and reports a decision after that, so one run enters the stalled
// state and can then be carried out of it.
//
// calls counts every entry into the body, including the resume's, so the test
// can say which of them it is talking about.
func serveIntentFailingOnce(t *testing.T, h *home.Home, calls *atomic.Int64) {
	t.Helper()
	stagesToServe := stages.All()
	stagesToServe.Intent = pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				if calls.Add(1) == 1 {
					return pipeline.Output{}, errBodyCouldNotRun
				}
				return heldReport(), nil
			}
		},
	}
	serveStages(t, h, stagesToServe)
}

// serveIntentLosingItsCaller serves a home whose first stage body waits for its
// caller to give up and fails with that, and reports a decision the next time
// it is entered. The second entry is what the service's own continuation
// reaches, and it says so on carried.
func serveIntentLosingItsCaller(t *testing.T, h *home.Home, inside, carried chan struct{}, entered, resumed *sync.Once, calls *atomic.Int64) {
	t.Helper()
	stagesToServe := stages.All()
	stagesToServe.Intent = pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
				if calls.Add(1) > 1 {
					resumed.Do(func() { close(carried) })
					return heldReport(), nil
				}
				entered.Do(func() { close(inside) })
				<-ctx.Done()
				return pipeline.Output{}, ctx.Err()
			}
		},
	}
	serveStages(t, h, stagesToServe)
}

// heldReport is what the stand-in intent bodies above report when they do run:
// one ask finding, so the run stops at a decision the way a stage with no body
// does.
func heldReport() pipeline.Output {
	return pipeline.Output{Report: findings.Report{
		Summary: "the stage stood in for the one this test is not about",
		Findings: []findings.Finding{
			{ID: "stand-in", Action: findings.ActionAsk, Description: "a decision"},
		},
	}}
}

// serveStages opens a service on a home with the given stages and serves it
// for the length of the test.
func serveStages(t *testing.T, h *home.Home, served pipeline.Stages) {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	runner := standin.New(t, standin.Script{}).Runner()
	running, err := service.Open(t.Context(), service.Options{
		Home:     h,
		Stages:   served,
		NewFixer: stages.PendingFixer,
		Build:    build,
		Catalog:  agents.NewCatalog(fixedFactory{runner: runner}),
	})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	serving := make(chan error, 1)
	go func() { serving <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("closing the service: %v", err)
		}
		if err := <-serving; err != nil {
			t.Errorf("serving: %v", err)
		}
	})
}

// errBodyCouldNotRun is a stage body reporting that it could not run at all,
// which is the error internal/pipeline distinguishes from a finding.
var errBodyCouldNotRun = bodyFailure{}

type bodyFailure struct{}

func (bodyFailure) Error() string { return "the intent stage body could not run at all" }
