package standin

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// Agent is one scripted stand-in and the record of what it was asked. It owns
// a control directory the stand-in processes read the script from and write
// their calls into, and it hands out an agents.Runner built by the production
// adapter over that directory.
//
// An Agent may be used from more than one goroutine, and the invocations it
// answers may overlap: a step's uses and a call's arrival order are both
// claimed by creating a file exclusively, so two stand-in processes cannot
// take the same one. What overlapping invocations do lose is which of them
// takes which of two equally matching steps, so a script for concurrent
// invocations should tell them apart with a Match rather than by order.
type Agent struct {
	tb      testing.TB
	control string
	runner  agents.Runner
}

// New writes the script, builds an agents.Runner over the stand-in, and
// verifies that this binary can act as one. Every failure here is fatal to the
// test, because a stand-in that could not be built answers no invocation and
// everything after it would fail for a reason further from the cause.
//
// opts are passed to agents.ClaudeFactory ahead of the one option this package
// sets for itself, so a caller may configure the recorder, the output limit,
// the termination grace, and the base environment, and may not replace the
// binary. That mirrors what the adapter does with a configured entry's own
// flags: what decides how the agent is reached is not up for configuration.
//
// The caller's own options are what a record reaches them through. This
// package installs no agents.Recorder of its own, so a test that wants the
// adapter's account of an invocation, its purpose among it, passes
// agents.WithRecorder and reads that; what this package reports is the
// separate account taken off the wire.
func New(tb testing.TB, script Script, opts ...agents.ClaudeOption) *Agent {
	tb.Helper()
	return newAgent(tb, script, handshakeFlag, opts...)
}

// newAgent is New with the handshake flag as a parameter, so a test can build
// an Agent over a binary that does not answer the handshake and prove the
// check below actually refuses one.
func newAgent(tb testing.TB, script Script, probeFlag string, opts ...agents.ClaudeOption) *Agent {
	tb.Helper()
	self, err := os.Executable()
	if err != nil {
		tb.Fatalf("standin: locating this test binary, which is the stand-in agent: %v", err)
	}
	control := prepare(tb, script)
	verify(tb, self, probeFlag)

	runner, err := agents.ClaudeFactory(slices.Concat(opts, []agents.ClaudeOption{agents.WithBinary(self)})...).
		New(tb.Context(), []string{controlFlag + control})
	if err != nil {
		tb.Fatalf("standin: building a runner over this test binary: %v", err)
	}
	return &Agent{tb: tb, control: control, runner: runner}
}

// prepare creates the control directory and writes the script into it.
func prepare(tb testing.TB, script Script) string {
	tb.Helper()
	control := tb.TempDir()
	for _, dir := range []string{callsPath(control), usesPath(control)} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			tb.Fatalf("standin: preparing the control directory: %v", err)
		}
	}
	encoded, err := json.Marshal(script)
	if err != nil {
		tb.Fatalf("standin: encoding the script: %v", err)
	}
	if err := os.WriteFile(scriptPath(control), encoded, 0o600); err != nil {
		tb.Fatalf("standin: writing the script: %v", err)
	}
	return control
}

// wiring is what the handshake established about this binary, remembered
// across the Agents built in one process. Whether a binary answers as the
// stand-in is a fact about the binary, so asking it once is asking it as often
// as there is an answer to get, and every Agent after the first costs one
// process fewer.
var wiring struct {
	mu       sync.Mutex
	answered map[string]string
}

// verify runs one handshake invocation and refuses anything but the token it
// sent back. The failure it catches is a package that did not call Main from
// its TestMain, which would otherwise surface as agent failures that look like
// the code under test misbehaving.
//
// It asserts on the token rather than on the invocation merely succeeding,
// because a binary that answers something else is not the stand-in and a
// script written for it would be answered by whatever that binary does.
func verify(tb testing.TB, self, probeFlag string) {
	tb.Helper()
	wiring.mu.Lock()
	defer wiring.mu.Unlock()
	reason, asked := wiring.answered[probeFlag]
	if !asked {
		reason = handshakeWith(tb, self, probeFlag)
		if wiring.answered == nil {
			wiring.answered = map[string]string{}
		}
		wiring.answered[probeFlag] = reason
	}
	if reason == "" {
		return
	}
	tb.Fatalf("standin: this test binary does not answer as the stand-in agent, %s.\n"+
		"Add this to the package under test:\n\n"+
		"\tfunc TestMain(m *testing.M) {\n"+
		"\t\tstandin.Main()\n"+
		"\t\tos.Exit(m.Run())\n"+
		"\t}\n", reason)
}

// handshakeWith runs the handshake once and returns why it was not answered,
// empty when it was.
//
// It runs on a runner of its own so that it reaches none of the caller's
// options: it must not be entered in a recorder the caller is asserting over,
// and it must not be answered out of a script. The token is a directory this
// process owns, so an answer that carries it came from a process this call
// started rather than from anything a binary could have printed on its own.
func handshakeWith(tb testing.TB, self, probeFlag string) string {
	tb.Helper()
	token := tb.TempDir()
	probe, err := agents.ClaudeFactory(agents.WithBinary(self)).
		New(tb.Context(), []string{probeFlag + token})
	if err != nil {
		return "building a runner over it failed: " + err.Error()
	}
	result, err := probe.Run(tb.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "standin: answer the handshake",
		Shape:  agents.ShapeText,
		Dir:    token,
	})
	switch {
	case err != nil:
		return "it failed: " + err.Error()
	case result.Text != token:
		return fmt.Sprintf("it answered %q", result.Text)
	default:
		return ""
	}
}

// Runner returns the adapter's Runner over the stand-in. It is the production
// agents.Runner, so a caller reaches a Fixer through agents.OpenFixer, a
// purpose, and every rule internal/agents applies, and nothing this package
// defines sits between the two. Its capability declaration is the adapter's
// own, so a test asking what it supports gets the shipped answer rather than
// one this package chose.
func (a *Agent) Runner() agents.Runner { return a.runner }

// Calls returns what the stand-in processes recorded, in arrival order.
//
// A stand-in records what it was asked before it replies and before it holds,
// so a call is here once its stand-in got that far, which an invocation still
// in flight may already have. What is not here is an invocation whose stand-in
// was ended before it claimed its place, or between claiming and recording,
// and an invocation that never started a process at all: a refused Invocation,
// one whose process failed to start, and one cancelled before the stand-in
// read the script leave nothing here. A test that needs a call to exist waits
// for it rather than assuming the invocation's return put it here.
//
// One of those absences is not free: a stand-in claims a bounded step's use
// before it claims its place here, so an invocation ended between the two has
// spent a use of Step.Times and left nothing here to say it did.
//
// Each record is complete or absent; there is nothing partial to read, and a
// record that cannot be decoded is fatal rather than skipped.
func (a *Agent) Calls() []Call {
	a.tb.Helper()
	calls, err := readCalls(a.control)
	if err != nil {
		a.tb.Fatalf("standin: reading what the stand-in was asked: %v", err)
	}
	return calls
}

// Call returns the one call the stand-in recorded, and is fatal when it
// recorded none or more than one, on the terms Calls states. It is for the
// common test that makes a single invocation and would otherwise index into a
// slice it has not checked the length of.
func (a *Agent) Call() Call {
	a.tb.Helper()
	calls := a.Calls()
	if len(calls) != 1 {
		a.tb.Fatalf("standin: expected the stand-in to be asked once, it was asked %d times: %s",
			len(calls), calls)
	}
	return calls[0]
}
