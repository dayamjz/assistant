package agents_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// The conformance suite. Its rule is that what an adapter declares is what it
// is held to, so an adapter that declares nothing is tested as having nothing
// and a declaration is never taken on trust where there is a way to check it.
//
// Each capability gets a probe pair: what must hold of an adapter that
// declared it, and what must hold of one that did not. conform picks between
// them by reading the declaration, which is the whole point - an adapter is
// not asked to say what to test it for.

// probe is one capability's conformance pair. declared is run against an
// adapter that declared the capability and undeclared against one that did
// not; either may be nil where this package has no way to tell, and a nil
// declared probe is a refusal rather than a pass, per the rule conform
// applies.
type probe struct {
	capability agents.Capability
	declared   func(t testing.TB, r agents.Runner)
	undeclared func(t testing.TB, r agents.Runner)
}

var probes = []probe{{
	capability: agents.CapabilityResumableSessions,
	declared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.SessionRunner); !ok {
			t.Errorf("%s declares %s and is no SessionRunner, so nothing can open a session on it",
				r.Name(), agents.CapabilityResumableSessions)
		}
		// The route a caller takes reaches the adapter's own mechanism. What
		// that mechanism then answers is the adapter's business; what is
		// asserted here is that the declaration did not stand in its way.
		if _, err := agents.OpenFixer(t.Context(), r, ""); errors.Is(err, agents.ErrUndeclaredCapability) {
			t.Errorf("%s declares %s and OpenFixer refused it as undeclared: %v",
				r.Name(), agents.CapabilityResumableSessions, err)
		}
	},
	undeclared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		// The type is the load-bearing half: an adapter with no session
		// mechanism has no method a caller could reach one through, so running
		// review and fix in one session is not something it can express.
		if _, ok := r.(agents.SessionRunner); ok {
			t.Errorf("%s has not declared %s and carries the mechanism anyway",
				r.Name(), agents.CapabilityResumableSessions)
		}
		_, err := agents.OpenFixer(t.Context(), r, "")
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s has not declared %s and OpenFixer answered %v, want a *CapabilityError",
				r.Name(), agents.CapabilityResumableSessions, err)
		}
		if refusal.Capability != agents.CapabilityResumableSessions {
			t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityResumableSessions)
		}
		if refusal.Agent != r.Name() {
			t.Errorf("the refusal names agent %q, want %q", refusal.Agent, r.Name())
		}
	},
}, {
	capability: agents.CapabilityWorkerSessions,
	declared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.WorkerRunner); !ok {
			t.Errorf("%s declares %s and is no WorkerRunner, so nothing can start a worker on it",
				r.Name(), agents.CapabilityWorkerSessions)
			return
		}
		// The mechanism, not the type: a worker is started in a directory,
		// is sent text, reads it, and is ended, through the route a caller
		// takes. What the worker says is the script's business and is not
		// asserted here; that it is a live process in a group of its own
		// that stays driveable across what it is sent is the whole of what
		// the declaration claims.
		dir, err := os.MkdirTemp("", "conform-worker-")
		if err != nil {
			t.Fatalf("making a directory for the worker: %v", err)
		}
		defer os.RemoveAll(dir)
		worker, err := agents.OpenWorker(t.Context(), r, agents.WorkerSpec{
			Dir: dir, Instructions: "conformance: a worker is being started",
		})
		if err != nil {
			t.Errorf("%s declares %s and OpenWorker could not start one: %v",
				r.Name(), agents.CapabilityWorkerSessions, err)
			return
		}
		defer worker.Close()
		if worker.Group() <= 0 {
			t.Errorf("%s started a worker in group %d, want a positive identifier", r.Name(), worker.Group())
		}
		// Two sends, each waited on until the worker has written something
		// after it or has ended. Writing into the pipe succeeds whether or
		// not the worker can read what was written, so a send returning nil
		// says nothing on its own; a worker that ends on what it was sent,
		// which is what a worker sent a framing it cannot read does, has
		// at most the first send's wait to hide behind, and nothing it could
		// write after the second, so the second wait ends in Done and this
		// fails. Every worker this probe runs against writes something for
		// every message it reads, an answer or a note on standard error,
		// which is the stand-in's convention and why the probe can wait.
		for _, text := range []string{
			"conformance: a first message after the instructions",
			"conformance: a second message, with \"quotes\" and a\nsecond line",
		} {
			mark := written(worker)
			if err := worker.Send(t.Context(), text); err != nil {
				t.Errorf("%s's worker refused a send while running: %v", r.Name(), err)
				return
			}
			if !reacted(t, worker, mark, written) {
				t.Errorf("%s's worker ended on what it was sent rather than reading it, so it is not driveable: %s",
					r.Name(), worker.Capture().Text)
				return
			}
		}
		worker.Close()
		select {
		case <-worker.Done():
		default:
			t.Errorf("%s's worker is not done after Close returned", r.Name())
		}
	},
	undeclared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.WorkerRunner); ok {
			t.Errorf("%s has not declared %s and carries the mechanism anyway",
				r.Name(), agents.CapabilityWorkerSessions)
		}
		dir, err := os.MkdirTemp("", "conform-worker-")
		if err != nil {
			t.Fatalf("making a directory for the worker: %v", err)
		}
		defer os.RemoveAll(dir)
		_, err = agents.OpenWorker(t.Context(), r, agents.WorkerSpec{Dir: dir, Instructions: "anything"})
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s has not declared %s and OpenWorker answered %v, want a *CapabilityError",
				r.Name(), agents.CapabilityWorkerSessions, err)
		}
		if refusal.Capability != agents.CapabilityWorkerSessions {
			t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityWorkerSessions)
		}
		if refusal.Agent != r.Name() {
			t.Errorf("the refusal names agent %q, want %q", refusal.Agent, r.Name())
		}
	},
}, {
	capability: agents.CapabilityTerminalWorkers,
	declared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.TerminalWorkerRunner); !ok {
			t.Errorf("%s declares %s and is no TerminalWorkerRunner, so nothing can start one on it",
				r.Name(), agents.CapabilityTerminalWorkers)
			return
		}
		// The worker probe's question, asked of a worker hosted in a
		// terminal: it starts, it is typed to twice and reacts each time
		// while still running, and Close ends it. Without tmux it is not
		// asked at all, and standin.Tmux says so rather than this passing.
		term := agents.Terminal{Tmux: standin.Tmux(t), Socket: filepath.Join(standin.SocketDir(t), "c")}
		dir, err := os.MkdirTemp("", "conform-terminal-")
		if err != nil {
			t.Fatalf("making a directory for the worker: %v", err)
		}
		defer os.RemoveAll(dir)
		const instructions = "conformance: a terminal worker is being started"
		// What is typed is echoed onto the screen by the terminal whether or
		// not the worker ever reads it, so a screen that grew says nothing
		// on its own: a worker that has not yet read a line, under load,
		// shows the echo of both sends and nothing of its own. The probe
		// counts only the lines that are no echo of anything it typed, so
		// it waits on the worker's own writing.
		typed := strings.Split(instructions, "\n")
		own := func(w agents.Worker) int64 { return ownLines(w, typed) }
		worker, err := agents.OpenTerminalWorker(t.Context(), r, term, agents.WorkerSpec{
			Dir: dir, Instructions: instructions,
		})
		if err != nil {
			t.Errorf("%s declares %s and OpenTerminalWorker could not start one: %v",
				r.Name(), agents.CapabilityTerminalWorkers, err)
			return
		}
		defer worker.Close()
		if worker.Group() <= 0 {
			t.Errorf("%s started a terminal worker in group %d, want a positive identifier", r.Name(), worker.Group())
		}
		for _, text := range []string{
			"conformance: a first message after the instructions",
			"conformance: a second message, with \"quotes\" and a\nsecond line",
		} {
			typed = append(typed, strings.Split(text, "\n")...)
			mark := own(worker)
			if err := worker.Send(t.Context(), text); err != nil {
				t.Errorf("%s's terminal worker refused a send while running: %v", r.Name(), err)
				return
			}
			if !reacted(t, worker, mark, own) {
				t.Errorf("%s's terminal worker ended on what it was typed rather than reading it: %s",
					r.Name(), worker.Capture().Text)
				return
			}
		}
		worker.Close()
		select {
		case <-worker.Done():
		default:
			t.Errorf("%s's terminal worker is not done after Close returned", r.Name())
		}
	},
	undeclared: func(t testing.TB, r agents.Runner) {
		t.Helper()
		if _, ok := r.(agents.TerminalWorkerRunner); ok {
			t.Errorf("%s has not declared %s and carries the mechanism anyway",
				r.Name(), agents.CapabilityTerminalWorkers)
		}
		_, err := agents.OpenTerminalWorker(t.Context(), r,
			agents.Terminal{Tmux: "/nonexistent/tmux", Socket: "/nonexistent/s"},
			agents.WorkerSpec{Dir: os.TempDir(), Instructions: "anything"})
		var refusal *agents.CapabilityError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s has not declared %s and OpenTerminalWorker answered %v, want a *CapabilityError",
				r.Name(), agents.CapabilityTerminalWorkers, err)
		}
		if refusal.Capability != agents.CapabilityTerminalWorkers || refusal.Agent != r.Name() {
			t.Errorf("the refusal names %q on %q, want %q on %q",
				refusal.Capability, refusal.Agent, agents.CapabilityTerminalWorkers, r.Name())
		}
	},
}}

// written is how many bytes a worker has written so far, counting what its
// Capture let go, so it only grows.
func written(w agents.Worker) int64 {
	got := w.Capture()
	return got.Dropped + int64(len(got.Text))
}

// ownLines is how many lines of a terminal worker's screen are not the
// echo of a line in typed, which is everything the probe has typed at it so
// far. The terminal echoes each typed line onto the screen as it arrives,
// before and whether or not the worker reads it, so only a line that is no
// such echo is something the worker wrote. A line the worker writes that
// happens to equal a typed one is not counted either, which can only make
// the probe wait for more, never accept sooner. What this does not cover is
// an echo caught partway, with part of a typed line on the screen: that
// part is no typed line and counts as the worker's. Each send is pasted
// whole in one tmux command, so the probe has not been seen to look
// between the halves of one, but nothing here rules it out.
func ownLines(w agents.Worker, typed []string) int64 {
	var n int64
	for _, line := range strings.Split(strings.TrimRight(w.Capture().Text, "\n"), "\n") {
		if line != "" && !slices.Contains(typed, line) {
			n++
		}
	}
	return n
}

// reacted waits until measure reads the worker past mark while it is still
// running, and reports false once it has ended, whether or not it wrote
// first: a worker that wrote and then ended was not left driveable by what
// it read. measure has to count only what the worker itself wrote, which is
// written for a pipe and ownLines for a terminal. A worker that is still
// exiting when it is looked at reads as having reacted, which is why the
// probe sends twice. The wait is bounded by the test's own context.
func reacted(t testing.TB, w agents.Worker, mark int64, measure func(agents.Worker) int64) bool {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if measure(w) > mark {
			select {
			case <-w.Done():
				// It wrote and ended: what it wrote was its last.
				return false
			default:
				return true
			}
		}
		select {
		case <-w.Done():
			return false
		case <-t.Context().Done():
			t.Fatalf("waiting for a worker to react: %v", t.Context().Err())
			return false
		case <-tick.C:
		}
	}
}

// probeFor returns the pair for a capability, and false when it has none.
func probeFor(capability agents.Capability) (probe, bool) {
	for _, p := range probes {
		if p.capability == capability {
			return p, true
		}
	}
	return probe{}, false
}

// conform holds one adapter to its own declaration, capability by capability.
//
// A capability the adapter declared and this suite cannot probe fails here.
// That is the rule that keeps a declaration from being a comment: a capability
// may be declared only where something checks it, so the day an adapter claims
// instruction suppression, whoever wrote it owes a probe before the claim
// counts for anything.
func conform(t testing.TB, r agents.Runner) {
	t.Helper()
	declared := r.Capabilities()
	for _, capability := range agents.AllCapabilities() {
		p, ok := probeFor(capability)
		if declared.Has(capability) {
			if !ok || p.declared == nil {
				t.Errorf("%s declares %s and nothing here checks it, so the declaration means nothing",
					r.Name(), capability)
				continue
			}
			p.declared(t, r)
			continue
		}
		if ok && p.undeclared != nil {
			p.undeclared(t, r)
		}
	}
}

// recordingTB records a failure instead of ending the test with it, so a test
// can assert that the conformance suite refused something. Everything it does
// not override is the real testing.TB, which is what keeps Context working for
// the probes that need one.
type recordingTB struct {
	testing.TB
	failed  bool
	message string
}

// Fatalf records the failure and ends the goroutine, which is what the real
// Fatalf does and what the code under test is entitled to expect.
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// Errorf records the failure without ending the goroutine.
func (r *recordingTB) Errorf(format string, args ...any) {
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
}

// onItsOwnGoroutine calls fn on a goroutine of its own and waits for it, so a
// Fatalf that ends its goroutine does not end the test.
func onItsOwnGoroutine(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

// The suite's refusal of a declaration nothing probes is what the rule above
// rests on, and no adapter this build ships makes it fire: the shipped adapter
// declares only resumable sessions, which has a probe, and the sessionless
// stub declares nothing. So the refusal is exercised here against an adapter
// that declares instruction suppression, whose table row carries no probe,
// which is the shape the first adapter to claim suppression would have.
//
// Such an adapter is legal to build, which is why the suite is the only thing
// that catches it: internal/agents skips a row with no probe when it holds an
// adapter to its declaration, so Resolve hands this one back.
func TestTheSuiteRefusesADeclarationNothingProbes(t *testing.T) {
	factory := &stubFactory{
		name:      "claims-suppression",
		available: true,
		declares:  []agents.Capability{agents.CapabilitySuppressProjectInstructions},
	}
	if _, err := agents.Resolve(t.Context(), []string{factory.name}, agents.NewCatalog(factory)); err != nil {
		t.Fatalf("resolving an adapter that declares a capability with no probe: %v", err)
	}

	recorder := &recordingTB{TB: t}
	onItsOwnGoroutine(func() { conform(recorder, factory.runner()) })

	if !recorder.failed {
		t.Fatalf("the suite accepted %s declaring %s, which nothing here checks",
			factory.name, agents.CapabilitySuppressProjectInstructions)
	}
	if !strings.Contains(recorder.message, string(agents.CapabilitySuppressProjectInstructions)) {
		t.Errorf("the suite refused with %q, want it to name %s",
			recorder.message, agents.CapabilitySuppressProjectInstructions)
	}
}

// TestTheShippedAdapterConformsToItsDeclaration runs the suite against the
// production Claude Code adapter, built over the scripted stand-in so what it
// proves is a property of this package rather than of whatever is installed.
// The adapter declares worker sessions, so this is where the worker probe
// runs against the stream-json framing the adapter writes, read by the
// stand-in's stream mode, which ends on a line it cannot read. It declares
// terminal workers too, so the terminal probe runs here against the
// stand-in's interactive mode, which opens on a first-run screen and never
// draws its interface unless the configuration the adapter seeded answers
// every question that screen asks. The base environment carries a key
// because a terminal worker is refused without one, and the key is
// stand-in text, which the stand-in reads only to check its approval.
func TestTheShippedAdapterConformsToItsDeclaration(t *testing.T) {
	conform(t, standin.New(t, standin.Script{},
		agents.WithTerminationGrace(300*time.Millisecond),
		agents.WithBaseEnvironment(withStandinKey(os.Environ())),
	).Runner())
}

// standinKey is the value the stand-in's terminal workers are given as
// their API key. It is no credential, and nothing it is given to sends it
// anywhere.
const standinKey = "standin-placeholder-key-0123456789abcdefghij"

// withStandinKey is env with standinKey as the key a Claude Code terminal
// worker reads.
func withStandinKey(env []string) []string {
	return append(slices.Clone(env), agents.ClaudeKeyVariable+"="+standinKey)
}

// The worker probe has to be able to fail on a worker that cannot read what
// it is sent, or it passes by looking at nothing. This is its positive
// control: the scripted adapter writes plain lines, and here its command line
// carries the flags that put the stand-in in stream mode, so every line it
// writes is one the stream mode refuses by ending. That is the defect a
// Claude Code adapter that framed its sends wrongly would have, and the suite
// must refuse it.
func TestTheWorkerProbeRefusesAWorkerThatEndsOnWhatItIsSent(t *testing.T) {
	agent := standin.NewWorker(t, standin.Script{})
	misframed, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), append(agent.Arguments(), "--print", "--input-format", "stream-json"))
	if err != nil {
		t.Fatalf("building the misframing runner: %v", err)
	}

	recorder := &recordingTB{TB: t}
	onItsOwnGoroutine(func() { conform(recorder, misframed) })

	if !recorder.failed {
		t.Fatal("the suite accepted a worker that ends on every line it is sent")
	}
	if !strings.Contains(recorder.message, misframed.Name()) {
		t.Errorf("the suite refused with %q, want it to name the adapter", recorder.message)
	}
}

// The terminal probe's positive control, on the worker probe's terms: the
// stand-in's command line here puts it in stream mode, which ends on the
// first plain line typed at it, so a terminal worker that cannot read what
// it is typed is what the suite must refuse.
func TestTheTerminalProbeRefusesAWorkerThatEndsOnWhatItIsTyped(t *testing.T) {
	standin.Tmux(t)
	agent := standin.NewWorker(t, standin.Script{})
	misframed, err := agents.ScriptedFactory(
		agents.WithBinary(helperBinary(t)),
		agents.WithTerminationGrace(300*time.Millisecond),
	).New(t.Context(), append(agent.Arguments(), "--print", "--input-format", "stream-json"))
	if err != nil {
		t.Fatalf("building the misframing runner: %v", err)
	}
	p, _ := probeFor(agents.CapabilityTerminalWorkers)
	recorder := &recordingTB{TB: t}
	onItsOwnGoroutine(func() { p.declared(recorder, misframed) })
	if !recorder.failed {
		t.Fatal("the terminal probe accepted a worker that ends on every line typed at it")
	}
}

// TestAnAdapterWithoutSessionsConformsToItsDeclaration runs the same suite
// against a second adapter that declares nothing, which is the case the
// declaration exists for and the one this build has no real example of.
func TestAnAdapterWithoutSessionsConformsToItsDeclaration(t *testing.T) {
	conform(t, (&stubFactory{name: "sessionless", available: true}).runner())
}

// TestTheScriptedAdapterConformsToItsDeclaration runs the suite against the
// scripted adapter, the second declarer of worker sessions, built over the
// stand-in's line mode, so the worker probe is exercised against both
// framings this package writes.
func TestTheScriptedAdapterConformsToItsDeclaration(t *testing.T) {
	conform(t, newWorkerRunner(t))
}

// What the scripted adapter declares is a fact about it too: the three
// mechanisms it carries, and not suppression. The second half holds for the reason the shipped
// adapter's does.
func TestWhatTheScriptedAdapterDeclares(t *testing.T) {
	declared := newWorkerRunner(t).Capabilities()
	want := []agents.Capability{agents.CapabilityResumableSessions, agents.CapabilityWorkerSessions,
		agents.CapabilityTerminalWorkers}
	if got := declared.List(); !slices.Equal(got, want) {
		t.Errorf("the scripted adapter declares %v, want exactly %v", got, want)
	}
	if slices.Contains(agents.DefaultCatalog().Names(), agents.ScriptedName) {
		t.Errorf("the scripted adapter is in the default catalog, and no real agent is known to answer its plain-line worker path")
	}
	if agents.ScriptedFactory().AutoEligible() {
		t.Error("the scripted adapter is auto-eligible, so \"auto\" could resolve a worker nothing real answers")
	}
}

// What the shipped adapter declares is a fact about it, so it is asserted
// rather than derived. Claude Code has resumable sessions, worker sessions
// and terminal workers, and it has no instruction-suppression mechanism, so a run
// configured to suppress a repository's own instructions is refused against
// it.
//
// The second half is the one worth spelling out. It is not an accident of the
// list: internal/agents implements no suppression mechanism at all, so a
// declaration of it here would be a claim with nothing behind it, and this is
// what fails if one is ever added without the mechanism.
func TestWhatTheShippedAdapterDeclares(t *testing.T) {
	claude := newRunner(t)
	claudeDeclared := claude.Capabilities()
	wantClaude := []agents.Capability{agents.CapabilityResumableSessions, agents.CapabilityWorkerSessions,
		agents.CapabilityTerminalWorkers}
	if got := claudeDeclared.List(); !slices.Equal(got, wantClaude) {
		t.Errorf("the claude adapter declares %v, want exactly %v", got, wantClaude)
	}
	if claudeDeclared.Has(agents.CapabilitySuppressProjectInstructions) {
		t.Errorf("the claude adapter declares %s, and nothing in this repository implements it",
			agents.CapabilitySuppressProjectInstructions)
	}

	// Test that all adapters are in the default catalog
	names := agents.DefaultCatalog().Names()
	wantNames := []string{agents.ClaudeName, agents.CursorName, agents.CodexName, agents.OpenAIName, agents.GrokName}
	if !slices.Equal(names, wantNames) {
		t.Errorf("this build ships adapters %v, want %v", names, wantNames)
	}

	// Verify Cursor also declares resumable sessions
	cursorFactory := agents.CursorFactory(agents.WithCursorBinary(helperBinary(t)))
	cursor, err := cursorFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("cursor factory failed: %v", err)
	}
	cursorDeclared := cursor.Capabilities()
	wantCursor := []agents.Capability{agents.CapabilityResumableSessions}
	if got := cursorDeclared.List(); !slices.Equal(got, wantCursor) {
		t.Errorf("the cursor adapter declares %v, want exactly %v", got, wantCursor)
	}

	// Verify Codex declares resumable sessions
	codexFactory := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t)))
	codex, err := codexFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("codex factory failed: %v", err)
	}
	codexDeclared := codex.Capabilities()
	wantCodex := []agents.Capability{agents.CapabilityResumableSessions}
	if got := codexDeclared.List(); !slices.Equal(got, wantCodex) {
		t.Errorf("the codex adapter declares %v, want exactly %v", got, wantCodex)
	}

	// Verify OpenAI declares no capabilities
	openaiFactory := agents.OpenAIFactory(agents.WithOpenAIBinary(helperBinary(t)))
	openai, err := openaiFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("openai factory failed: %v", err)
	}
	openaiDeclared := openai.Capabilities()
	if got := openaiDeclared.List(); len(got) != 0 {
		t.Errorf("the openai adapter declares %v, want no capabilities", got)
	}

	// Verify Grok declares no capabilities
	grokFactory := agents.GrokFactory(agents.WithGrokBinary(helperBinary(t)))
	grok, err := grokFactory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("grok factory failed: %v", err)
	}
	grokDeclared := grok.Capabilities()
	wantGrok := []agents.Capability{agents.CapabilityResumableSessions}
	if got := grokDeclared.List(); !slices.Equal(got, wantGrok) {
		t.Errorf("the grok adapter declares %v, want exactly %v", got, wantGrok)
	}
}

// Resolve holds an adapter to its declaration in both directions, and refuses
// the whole resolution rather than passing over the entry.
func TestResolveRefusesAnAdapterWhoseDeclarationDoesNotMatchWhatItIs(t *testing.T) {
	cases := []struct {
		name    string
		factory *stubFactory
		want    string
	}{{
		name: "declares sessions it cannot open",
		factory: &stubFactory{
			name:      "boaster",
			available: true,
			declares:  []agents.Capability{agents.CapabilityResumableSessions},
			sessions:  false,
		},
		want: "carries no mechanism",
	}, {
		name: "opens sessions it did not declare",
		factory: &stubFactory{
			name:      "quiet",
			available: true,
			sessions:  true,
		},
		want: "does not declare it",
	}, {
		name: "declares workers it cannot start",
		factory: &stubFactory{
			name:      "worker-boaster",
			available: true,
			declares:  []agents.Capability{agents.CapabilityWorkerSessions},
		},
		want: "carries no mechanism",
	}, {
		name: "starts workers it did not declare",
		factory: &stubFactory{
			name:      "quiet-worker",
			available: true,
			workers:   true,
		},
		want: "does not declare it",
	}, {
		name: "declares terminal workers it cannot start",
		factory: &stubFactory{
			name:      "terminal-boaster",
			available: true,
			declares:  []agents.Capability{agents.CapabilityTerminalWorkers},
		},
		want: "carries no mechanism",
	}, {
		name: "starts terminal workers it did not declare",
		factory: &stubFactory{
			name:      "quiet-terminal",
			available: true,
			terminals: true,
		},
		want: "does not declare it",
	}, {
		name: "declares a capability this build does not define",
		factory: &stubFactory{
			name:      "inventive",
			available: true,
			declares:  []agents.Capability{"time_travel"},
		},
		want: "not a capability this build defines",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A working adapter sits behind the defective one, so a resolution
			// that fell through to it would look like a success. That is the
			// outcome this refusal exists to prevent.
			fallback := &stubFactory{name: "fallback", available: true}
			catalog := agents.NewCatalog(c.factory, fallback)

			got, err := agents.Resolve(t.Context(), []string{c.factory.name, "fallback"}, catalog)
			if !errors.Is(err, agents.ErrAdapterDeclaration) {
				t.Fatalf("resolution answered %v with runner %v, want ErrAdapterDeclaration",
					err, got.Runner)
			}
			var defect *agents.AdapterError
			if !errors.As(err, &defect) {
				t.Fatalf("the refusal is not an *AdapterError: %v", err)
			}
			if defect.Agent != c.factory.name {
				t.Errorf("the refusal names %q, want %q", defect.Agent, c.factory.name)
			}
			if !strings.Contains(defect.Error(), c.want) {
				t.Errorf("the refusal reads %q, want it to say %q", defect.Error(), c.want)
			}
		})
	}
}

// What Resolve reports is the resolved adapter's own declaration, so a caller
// that refuses a path before building it reads the same answer the adapter
// would give.
func TestResolveReportsTheAdapterDeclaration(t *testing.T) {
	factory := &stubFactory{
		name:      "sessions",
		available: true,
		declares:  []agents.Capability{agents.CapabilityResumableSessions},
		sessions:  true,
	}
	got, err := agents.Resolve(t.Context(), []string{"sessions"}, agents.NewCatalog(factory))
	if err != nil {
		t.Fatalf("resolving an adapter that agrees with itself: %v", err)
	}
	if !got.Capabilities.Has(agents.CapabilityResumableSessions) {
		t.Errorf("the resolution declares %v, want resumable sessions", got.Capabilities)
	}
	if !reflect.DeepEqual(got.Capabilities.List(), got.Runner.Capabilities().List()) {
		t.Errorf("the resolution declares %v and its runner declares %v",
			got.Capabilities, got.Runner.Capabilities())
	}
}

// OpenFixer reaches the adapter's mechanism once the declaration allows it,
// and refuses before reaching it when the declaration does not. The pair is
// what makes the declaration decide rather than describe.
func TestOpenFixerConsultsTheDeclarationBeforeTheMechanism(t *testing.T) {
	declared := (&stubFactory{
		name:      "declared",
		available: true,
		declares:  []agents.Capability{agents.CapabilityResumableSessions},
		sessions:  true,
	}).runner()
	if _, err := agents.OpenFixer(t.Context(), declared, ""); !errors.Is(err, errStubSessionReached) {
		t.Errorf("OpenFixer on a declared adapter answered %v, want the adapter's own mechanism", err)
	}

	// The same mechanism, undeclared. Reaching it would be the substitution
	// PRD section 8 refuses, so the refusal must come from the declaration and
	// not from the type.
	undeclared := &sessionStubRunner{stubRunner: &stubRunner{name: "undeclared"}}
	_, err := agents.OpenFixer(t.Context(), undeclared, "")
	if errors.Is(err, errStubSessionReached) {
		t.Fatal("OpenFixer reached the session mechanism of an adapter that declared nothing")
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("OpenFixer answered %v, want ErrUndeclaredCapability", err)
	}
}

// An adapter that declares sessions and carries no mechanism is a defect
// Resolve refuses, and OpenFixer is reached by a caller that built a Runner
// without going through Resolve, which every test in this repository and
// internal/agents/standin do. It refuses there too rather than returning a nil
// Fixer with a nil error, which is what a caller would otherwise dereference.
func TestOpenFixerRefusesAnAdapterThatDeclaresSessionsItCannotOpen(t *testing.T) {
	boaster := &stubRunner{
		name:     "boaster",
		declared: agents.Declare(agents.CapabilityResumableSessions),
	}
	fixer, err := agents.OpenFixer(t.Context(), boaster, "")
	if fixer != nil {
		t.Errorf("OpenFixer returned %v for an adapter with no mechanism, want nothing", fixer)
	}
	var defect *agents.AdapterError
	if !errors.As(err, &defect) {
		t.Fatalf("OpenFixer answered %v, want an *AdapterError", err)
	}
	if defect.Agent != "boaster" {
		t.Errorf("the refusal names %q, want %q", defect.Agent, "boaster")
	}
}

// The Runner interface is where the sessionless adapter's inability lives, so
// it is asserted the way session_test.go asserts the rest of P4's arrangement:
// on the type, where adding a method would break it.
func TestRunnerCarriesNoRouteToASession(t *testing.T) {
	runner := reflect.TypeFor[agents.Runner]()
	for i := range runner.NumMethod() {
		method := runner.Method(i)
		if method.Type.NumOut() > 0 && method.Type.Out(0) == reflect.TypeFor[agents.Fixer]() {
			t.Errorf("Runner.%s returns a Fixer, so an adapter with no sessions could still be asked for one",
				method.Name)
		}
	}
	if _, ok := reflect.TypeFor[agents.SessionRunner]().MethodByName("Fixer"); !ok {
		t.Error("SessionRunner has no Fixer method, so nothing distinguishes an adapter that has sessions")
	}
}

// An unrecognized capability satisfies nothing, whatever a declaration says
// about it, so a typo can only refuse a path and never open one.
func TestAnUnrecognizedCapabilityIsNeverHad(t *testing.T) {
	declared := agents.Declare("resumable_session", agents.CapabilityResumableSessions)
	if declared.Has("resumable_session") {
		t.Error("a capability this build does not define was had")
	}
	if !declared.Has(agents.CapabilityResumableSessions) {
		t.Error("an unrecognized capability alongside a recognized one lost the recognized one")
	}
	if agents.Capability("resumable_session").Recognized() {
		t.Error("a capability this build does not define reported itself recognized")
	}
}

// The zero declaration is the one an adapter that says nothing has, and it has
// nothing.
func TestTheZeroDeclarationHasNothing(t *testing.T) {
	var none agents.Capabilities
	for _, capability := range agents.AllCapabilities() {
		if none.Has(capability) {
			t.Errorf("the zero Capabilities has %s", capability)
		}
	}
	if got := none.List(); len(got) != 0 {
		t.Errorf("the zero Capabilities lists %v, want nothing declared", got)
	}
	if none.String() != "none" {
		t.Errorf("the zero Capabilities renders as %q, want %q", none.String(), "none")
	}
}

// List renders a declaration in table order whatever order it was declared in,
// so one declaration reads the same on every run.
func TestListIsStableWhateverOrderWasDeclared(t *testing.T) {
	table := agents.AllCapabilities()
	reversed := slices.Clone(table)
	slices.Reverse(reversed)

	forwards, backwards := agents.Declare(table...), agents.Declare(reversed...)
	if !slices.Equal(forwards.List(), backwards.List()) {
		t.Errorf("declaring in two orders lists as %v and %v", forwards.List(), backwards.List())
	}
	if !slices.Equal(forwards.List(), table) {
		t.Errorf("List returned %v, want the table order %v", forwards.List(), table)
	}
}
