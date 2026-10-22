//go:build linux || darwin

package stages

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// A stage command that runs past its limit and is then abandoned, still not
// exited a grace after its group was sent SIGKILL, is reported as both: it
// ran past its limit, and it was not seen to end, so the result does not
// claim an end the call never observed. A one-nanosecond grace makes the
// abandonment likely rather than certain, so the call repeats until it is
// abandoned and fails loudly if it never is.
func TestATimedOutCommandThatWasAbandonedSaysSo(t *testing.T) {
	t.Parallel()
	const attempts = 200
	for i := 0; i < attempts; i++ {
		result := runCommand(t.Context(), commandSpec{
			command: "exec sleep 10",
			dir:     t.TempDir(),
			record:  io.Discard,
			grace:   time.Nanosecond,
			timeout: 10 * time.Millisecond,
		})
		if result.exited {
			t.Fatalf("a command its limit ended reported status %d as its own", result.code)
		}
		if !errors.Is(result.err, errCommandTimedOut) {
			t.Fatalf("a command past its limit answered %v, want errCommandTimedOut", result.err)
		}
		if errors.Is(result.err, procgroup.ErrAbandoned) {
			if result.code != -1 {
				t.Fatalf("an abandoned command reported status %d, want none (-1)", result.code)
			}
			return
		}
	}
	t.Fatalf("no result carried procgroup.ErrAbandoned in %d attempts: the abandonment is not reported, or the test no longer reaches it", attempts)
}
