package machine_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
)

// WakeWaitRequest.Bounds is the one rule for which waits are in range: zero
// takes the default bound and limit, each end of the range is accepted,
// and a bound past either end, a count of seconds too large for a duration,
// and a negative limit are refused rather than clamped.
func TestAWaitsBoundsAreItsRangeAndNothingIsClamped(t *testing.T) {
	t.Parallel()
	maximum := int(machine.MaxWakeWaitTimeout / time.Second)
	for _, c := range []struct {
		request machine.WakeWaitRequest
		bound   time.Duration
		limit   int
	}{
		{machine.WakeWaitRequest{}, machine.DefaultWakeWaitTimeout, machine.DefaultWakesLimit},
		{machine.WakeWaitRequest{TimeoutSeconds: 1, Limit: 3}, time.Second, 3},
		{machine.WakeWaitRequest{TimeoutSeconds: maximum}, machine.MaxWakeWaitTimeout, machine.DefaultWakesLimit},
	} {
		bound, limit, err := c.request.Bounds()
		if err != nil || bound != c.bound || limit != c.limit {
			t.Errorf("%+v.Bounds() = %s, %d, %v; want %s, %d", c.request, bound, limit, err, c.bound, c.limit)
		}
	}
	for _, refused := range []machine.WakeWaitRequest{
		{TimeoutSeconds: -1},
		{TimeoutSeconds: maximum + 1},
		{TimeoutSeconds: 1 << 30},
		{TimeoutSeconds: 1, Limit: -1},
	} {
		if bound, limit, err := refused.Bounds(); err == nil {
			t.Errorf("%+v.Bounds() = %s, %d, want a refusal", refused, bound, limit)
		}
	}
}
