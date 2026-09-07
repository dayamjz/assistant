package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 9's outcome table has two members that say a run is not finished
// with, and this exercises the second of them through the surface a person
// actually types at, while a run is genuinely inside a stage body rather than
// with a value a test stated for itself.
//
// The section says a read answers where a run stands without advancing it, and
// that the bare command attaches and reports a run this service is already
// advancing rather than advancing it twice. Both are driven here: the branch's
// status, and the bare command. Each answers executing, neither waits for the
// stage body to finish, and both are a success, because an agent that read a
// healthy run as a failure would stop driving it.
//
// The blocking start that is holding the stage open is checked at the end for
// the other half of the same sentence: a call that did advance the run answers
// at a decision point, not with a run still moving.
func TestReadsOfARunInsideAStageBodyAnswerExecuting(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)

	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	serveHeldAtIntent(t, h, inside, release, &entered)
	// Registered after the service, so it runs before the service is closed.
	// A test that fails while the body is still held would otherwise leave the
	// teardown waiting on a stage nothing is going to release.
	t.Cleanup(let)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	// The start blocks inside the first stage for the whole of every read
	// below, so the two overlap for certain rather than by timing.
	started := startInBackground(t, h, subject, "--json", "--intent", "add a greeting, with the tradeoffs stated")
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the start never reached the stage body")
	}

	// Reporting the branch's status advances nothing, and the run it reports
	// is part way through a stage.
	reported := run(t, h, subject, "--json", "status")
	if reported.code != machine.ExitOK {
		t.Fatalf("status exited %s while a run was advancing:\n%s%s", reported.code, reported.stdout, reported.stderr)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(reported.stdout), &status); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, reported.stdout)
	}
	if status.ActiveRun == nil {
		t.Fatalf("status reports no active run while one is advancing:\n%s", reported.stdout)
	}
	t.Logf("assistant --json status, while the run is inside a stage body:\n%s", reported.stdout)
	assertExecuting(t, *status.ActiveRun, "status")

	// The bare command attaches. It answers rather than waiting for the stage
	// body the start is holding open, and it answers with the run as it stands.
	attached := run(t, h, subject, "--json")
	if attached.code != machine.ExitOK {
		t.Fatalf("attaching exited %s while the run was advancing:\n%s%s", attached.code, attached.stdout, attached.stderr)
	}
	t.Logf("assistant --json, attaching to a run this service is already advancing:\n%s", attached.stdout)
	view := decodeRun(t, attached.stdout)
	assertExecuting(t, view, "attaching")
	if status.ActiveRun.Record.ID != view.Record.ID {
		t.Fatalf("status and attaching answered about different runs: %s and %s",
			status.ActiveRun.Record.ID, view.Record.ID)
	}

	// What a person reads says the run is executing and what to do about it,
	// rather than leaving them to infer either.
	human := run(t, h, subject)
	if human.code != machine.ExitOK {
		t.Fatalf("the human rendering exited %s:\n%s%s", human.code, human.stdout, human.stderr)
	}
	t.Logf("assistant, the same attach rendered for a person:\n%s\nprogress, on standard error:\n%s", human.stdout, human.stderr)
	for _, want := range []string{string(machine.OutcomeExecuting), machine.OutcomeExecuting.NextActionFor(true)} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("the rendering does not say %q:\n%s", want, human.stdout)
		}
	}

	// A rerun has no attach to fall back on, so the branch it would report a
	// moving run for is the branch it refuses.
	again := run(t, h, subject, "--json", "rerun")
	if again.code == machine.ExitOK {
		t.Fatalf("a rerun of a branch with a run in flight exited ok:\n%s", again.stdout)
	}
	if strings.Contains(again.stdout, string(machine.OutcomeExecuting)) {
		t.Fatalf("a rerun answered with a run still executing:\n%s", again.stdout)
	}
	t.Logf("assistant --json rerun, on a branch whose run is in flight:\n%s%s", again.stdout, again.stderr)

	// And the call that did advance the run answers where that run stopped.
	let()
	select {
	case got := <-started:
		if got.code != machine.ExitOK {
			t.Fatalf("the start exited %s:\n%s%s", got.code, got.stdout, got.stderr)
		}
		advanced := decodeRun(t, got.stdout)
		if advanced.Outcome != machine.OutcomeDecision {
			t.Fatalf("a call that advanced the run answered %s, want a decision", advanced.Outcome)
		}
		if advanced.Record.ID != view.Record.ID {
			t.Fatalf("the start and the reads answered about different runs: %s and %s",
				advanced.Record.ID, view.Record.ID)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never returned after the stage body was released")
	}
}

// assertExecuting holds an answer to what PRD section 9 says an executing run
// is: not finished with, nothing to answer, and never silent about what to do
// next.
func assertExecuting(t *testing.T, view machine.Run, surface string) {
	t.Helper()
	if view.Outcome != machine.OutcomeExecuting {
		t.Fatalf("%s reports %s for a run inside a stage body, want executing", surface, view.Outcome)
	}
	if view.Outcome.Terminal() {
		t.Fatalf("%s reports that a run still inside a stage body has ended", surface)
	}
	if view.Decision != nil {
		t.Fatalf("%s offers a decision to answer on a run that has none: %+v", surface, view.Decision)
	}
	if strings.TrimSpace(view.NextAction()) == "" {
		t.Fatalf("%s says nothing about what to do next about a run that is executing", surface)
	}
	if view.Record.Status != store.RunRunning {
		t.Fatalf("%s reports the record as %s, want running", surface, view.Record.Status)
	}
	// Executing covers a run in flight and a run nothing is carrying on, and
	// they take opposite actions. This is the in-flight half; the other is
	// TestARunNothingIsAdvancingIsNotReportedAsOneInFlight, and each asserts
	// the other's answer is not the one it got, so the two cannot collapse
	// back onto one rendering without failing both.
	if !view.Advancing {
		t.Fatalf("%s reports that nothing is advancing a run that is inside a stage body", surface)
	}
	if view.NextAction() == machine.OutcomeExecuting.NextActionFor(false) {
		t.Fatalf("%s tells a reader to attach a run that is already being advanced: %s", surface, view.NextAction())
	}
	if view.NextAction() != machine.OutcomeExecuting.NextActionFor(true) {
		t.Fatalf("%s says %q about a run in flight, want the action for one being advanced", surface, view.NextAction())
	}
}

// startInBackground drives the surface in a goroutine and hands back what it
// answered, for the one call in this file that is meant to be blocked when the
// reads around it are made. Everything the testing package is not safe to be
// told from another goroutine is settled before that goroutine starts.
func startInBackground(t *testing.T, h *home.Home, workingDir string, args ...string) <-chan invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	line := append([]string{"--home", h.Root()}, args...)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	answered := make(chan invocation, 1)
	go func() {
		var out, errs bytes.Buffer
		code := cli.Run(ctx, cli.Environment{
			Args:       line,
			Stdout:     &out,
			Stderr:     &errs,
			Getenv:     func(string) string { return "" },
			WorkingDir: workingDir,
			Executable: executable,
			Version:    "assistant (test)",
		})
		answered <- invocation{code: code, stdout: out.String(), stderr: errs.String()}
	}()
	return answered
}

// serveHeldAtIntent serves a home whose first stage stays inside its body
// until it is released, which is how a run is put in the one state this file
// is about and held there rather than caught in it.
func serveHeldAtIntent(t *testing.T, h *home.Home, inside chan struct{}, release chan struct{}, entered *sync.Once) {
	t.Helper()
	held := stages.All()
	held.Intent = pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
				entered.Do(func() { close(inside) })
				select {
				case <-release:
				case <-ctx.Done():
					return pipeline.Output{}, ctx.Err()
				}
				return pipeline.Output{Report: findings.Report{
					Summary:  "the stage was held open for the length of the reads",
					Findings: []findings.Finding{{ID: "held", Action: findings.ActionAsk, Description: "a decision"}},
				}}, nil
			}
		},
	}
	serveStages(t, h, held)
}
