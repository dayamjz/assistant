package standin

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// The wiring check has to fail when it should, or it is worse than no check:
// it would report that a binary answers as the stand-in without having asked
// it anything it could get wrong.
//
// This drives it against a binary that is not the stand-in, which is the case
// it exists for. Sending the probe under a flag the stand-in does not know
// makes this test binary answer the way any binary without the TestMain wiring
// does: Main returns, the test binary reads the flag as its own, and nothing
// answers the handshake.
func TestNewRefusesABinaryThatDoesNotAnswerTheHandshake(t *testing.T) {
	fake := &fatalTB{TB: t}
	run(func() { newAgent(fake, oneStep(Text("unreachable")), "--standin-not-the-handshake=") })

	if !fake.failed {
		t.Fatal("building a stand-in over a binary that answers nothing was allowed")
	}
	if !strings.Contains(fake.message, "standin.Main()") {
		t.Errorf("the refusal was %q, want it to name the TestMain wiring that is missing", fake.message)
	}
}

// The same check passes against this binary, which does answer. Without this
// half the test above would also pass if the check refused everything.
func TestNewAcceptsThisBinary(t *testing.T) {
	fake := &fatalTB{TB: t}
	run(func() { newAgent(fake, oneStep(Text("done")), handshakeFlag) })

	if fake.failed {
		t.Fatalf("building a stand-in over this binary was refused: %s", fake.message)
	}
}

// fatalTB records a fatal failure instead of ending the test with it, so a
// test can assert that something refuses. Everything it does not override is
// the real testing.TB, which is what makes TempDir and Context work.
type fatalTB struct {
	testing.TB
	failed  bool
	message string
}

// Fatalf records the failure and ends the goroutine, which is what the real
// Fatalf does and what the code under test is entitled to expect.
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.failed = true
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// Errorf records the failure without ending the goroutine.
func (f *fatalTB) Errorf(format string, args ...any) {
	f.failed = true
	f.message = fmt.Sprintf(format, args...)
}

// run calls fn on a goroutine of its own and waits for it, so a Fatalf that
// ends its goroutine does not end the test.
func run(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}
