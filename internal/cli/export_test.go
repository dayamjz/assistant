package cli

import (
	"testing"
	"time"
)

// ShortenReceiveWait makes a receive command wait d rather than internal/gate's
// bound for the rest of the test, so a test reaches the refusal past the bound
// without waiting the bound out. It reaches only cli.Run in this process: a
// push's receive command is another process and still waits the real bound.
func ShortenReceiveWait(t *testing.T, d time.Duration) {
	t.Helper()
	was := receiveWait
	receiveWait = d
	t.Cleanup(func() { receiveWait = was })
}
