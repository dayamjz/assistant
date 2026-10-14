package cli

import (
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The host adapter maps every answer onto exactly one of the host's two
// statuses: success when the turn may end, the block status when it may not.
// The walk is over internal/supervisor's whole reason set, with and without
// the re-entry signal, and over the uncomputed guard, so a reason added
// there is mapped from the next run.
func TestEveryVerdictMapsToExactlyOneExitCode(t *testing.T) {
	t.Parallel()
	if hostBlockCode == machine.ExitOK {
		t.Fatal("the block status is success, so a blocked turn would end")
	}
	seen := map[machine.Code]bool{}
	for _, reason := range append(supervisor.StopReasons(), "") {
		verdict := supervisor.StopVerdict{Reason: reason}
		for _, reEntered := range []bool{false, true} {
			answer := machine.HostStopOf(verdict, reEntered)
			code := hostExit(answer)
			seen[code] = true
			switch {
			case answer.Allowed && code != machine.ExitOK:
				t.Errorf("%q re-entered=%v is allowed and exits %s", reason, reEntered, code)
			case !answer.Allowed && code != hostBlockCode:
				t.Errorf("%q re-entered=%v is blocked and exits %s, want the host's block status", reason, reEntered, code)
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("the answers mapped onto %d statuses, want the host's two", len(seen))
	}
}
