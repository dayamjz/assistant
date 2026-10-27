//go:build darwin || linux

package journey

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// TestTheBinaryBuildEndsWhenTheTestTimeoutElapses checks the deadline
// binaryBuildContext gives the product-binary build. Run ends the command
// when it handles that cancellation. The testing alarm exiting the process
// first is TestATimedOutBuildDoesNotOutliveTheHarness.
func TestTheBinaryBuildEndsWhenTheTestTimeoutElapses(t *testing.T) {
	if testBinaryTimeout() <= 0 {
		t.Fatal("the test binary's -timeout was read as zero, so the build would be unbounded")
	}
	started := time.Now().Add(-4 * time.Second)
	ctx, cancel := binaryBuildContext(started, 5*time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the build context has no deadline, so a hung go build outlives the test timeout")
	}
	if wait := time.Until(deadline); wait > 2*time.Second {
		t.Fatalf("the build deadline is %s away, want it inside the time the timeout has left", wait)
	}
	cmd := procgroup.Command("/bin/sleep", "30")
	errc := make(chan error, 1)
	go func() { errc <- procgroup.Run(ctx, cmd, time.Second) }()
	select {
	case <-errc:
	case <-time.After(4 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		t.Fatal("the program was still running after the build context's deadline")
	}
	if ctx.Err() == nil {
		t.Fatal("the program returned before the build context ended")
	}
}
