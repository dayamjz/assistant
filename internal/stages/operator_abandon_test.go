//go:build linux || darwin

package stages_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/stages"
)

// A command procgroup.Run abandons, one still not exited a grace after its
// group was sent SIGKILL, is reported as abandoned and with no exit status,
// and the call reads nothing of the process state the abandoned reaper
// writes once the command does exit. That read raced the reaper's cmd.Wait,
// which only the race detector sees, so this test is the scenario the race
// was found with and is meant to run under -race, as make test runs it.
//
// A one-nanosecond grace makes the abandonment the likely outcome rather
// than a certain one, since the kernel may end the command first, so the
// scenario repeats until it has been abandoned several times and fails
// loudly if it never is. Each repetition also gives the reapers of the
// earlier ones time to run inside the test, where the detector watches
// their writes.
func TestAnAbandonedOperatorCommandReportsNoStatusAndReadsNoneOfTheReapers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const want, attempts = 5, 200
	abandoned := 0
	for i := 0; i < attempts && abandoned < want; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		got := stages.RunOperatorCommand(ctx, stages.OperatorCommand{
			Line: "exec sleep 10", Dir: dir, Grace: time.Nanosecond,
		})
		cancel()
		if got.Exited {
			t.Fatalf("a command the context ended reported status %d as its own", got.Code)
		}
		if !errors.Is(got.Err, procgroup.ErrAbandoned) {
			continue
		}
		abandoned++
		if got.Code != -1 {
			t.Fatalf("an abandoned command reported status %d, want none (-1)", got.Code)
		}
	}
	if abandoned < want {
		t.Fatalf("the command was abandoned %d times in %d attempts, want %d: the scenario no longer reaches the abandoned path", abandoned, attempts, want)
	}
}
