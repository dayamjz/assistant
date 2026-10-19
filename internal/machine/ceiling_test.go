package machine_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// ceilingUsage is what UsageOf totals for rows over a 24h window measured
// back from usageReadAt, which is the view CeilingOn is given.
func ceilingUsage(t *testing.T, rows ...store.AgentInvocation) machine.Usage {
	t.Helper()
	return machine.UsageOf(window(t, "24h"), usageReadAt, rows)
}

func inputCall(tokens int64) store.AgentInvocation {
	row := call("claude", "", "review", time.Hour)
	row.InputTokens = store.Known[int64](tokens)
	return row
}

func wantCeiling(t *testing.T, err error, total int64, unreported, calls int) {
	t.Helper()
	var got *machine.UsageCeilingError
	if !errors.As(err, &got) {
		t.Fatalf("error %v, want a usage ceiling refusal", err)
	}
	if got.Ceiling != 100 || got.Window != "24h" || got.Unreported != unreported || got.Calls != calls || got.Read != nil {
		t.Fatalf("refusal %+v, want ceiling 100 over 24h, %d unreported of %d calls", got, unreported, calls)
	}
	n, ok := got.Total.Get()
	if !ok || n != total {
		t.Fatalf("total (%d, %v), want %d", n, ok, total)
	}
}

// A total under the ceiling, and a total equal to it, starts. A total over it
// does not. The rows are totalled by UsageOf, and output tokens are not part
// of the comparison: a call that reported a million of those and ten input
// tokens is under a ceiling of 100.
func TestCeilingOnComparesRecordedInputTokens(t *testing.T) {
	under := ceilingUsage(t, inputCall(10))
	if err := machine.CeilingOn(under, 100); err != nil {
		t.Fatalf("10 under 100: %v", err)
	}
	equal := ceilingUsage(t, inputCall(40), inputCall(60))
	if err := machine.CeilingOn(equal, 100); err != nil {
		t.Fatalf("100 equal to 100: %v", err)
	}
	loud := inputCall(10)
	loud.OutputTokens = store.Known[int64](1_000_000)
	loud.CacheReadTokens = store.Known[int64](1_000_000)
	if err := machine.CeilingOn(ceilingUsage(t, loud), 100); err != nil {
		t.Fatalf("output and cache tokens tripped the input ceiling: %v", err)
	}
	over := ceilingUsage(t, inputCall(40), inputCall(61))
	wantCeiling(t, machine.CeilingOn(over, 100), 101, 0, 2)
}

// A window with no calls is under any positive ceiling. A ceiling of zero or
// less is off, including over a total that is unreported or far over.
func TestCeilingOnAllowsAnEmptyWindowAndACeilingThatIsOff(t *testing.T) {
	if err := machine.CeilingOn(ceilingUsage(t), 1); err != nil {
		t.Fatalf("no calls: %v", err)
	}
	huge := ceilingUsage(t, inputCall(1_000_000), call("claude", "", "review", time.Hour))
	for _, ceiling := range []int64{0, -1} {
		if err := machine.CeilingOn(huge, ceiling); err != nil {
			t.Fatalf("ceiling %d: %v", ceiling, err)
		}
	}
}

// Unreported input tokens are not read as zero. A known total under the
// ceiling still refuses when another call in the window did not report, and
// the same when the known total is already over.
func TestCeilingOnRefusesWhenInputTokensAreNotEstablished(t *testing.T) {
	silent := call("claude", "", "review", time.Hour)
	under := ceilingUsage(t, inputCall(10), silent)
	wantCeiling(t, machine.CeilingOn(under, 100), 10, 1, 2)
	over := ceilingUsage(t, inputCall(150), silent)
	wantCeiling(t, machine.CeilingOn(over, 100), 150, 1, 2)
}
