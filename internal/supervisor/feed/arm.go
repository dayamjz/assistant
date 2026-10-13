package feed

import (
	"context"
	"errors"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// armLooks is how many times VerifyArm looks within the first-beacon wait.
// The pause between looks is the wait divided by this, so a loop that comes
// up late in the wait is still seen before it ends.
const armLooks = 20

// VerifyArm is PRD section 5's arm verification over a home: it reads the
// beacon once to learn what was there before it asked, then looks at the
// instance file, the lock and the beacon until supervisor.ArmOf settles or
// supervisor.FirstBeaconWait at interval has elapsed, judging each look at
// clock's time under supervisor.FreshnessFor the same interval. The answer
// is the closed set supervisor.Arm carries, and the sequence on it is the
// beacon's, so a caller that remembers the sequence it was answered with can
// tell a later attached from the same beacon still standing.
//
// A context that ends before the verdict settles is answered as
// ArmNoBeacon with the context's error, because the verification did not
// see a beacon and a caller holding a verdict and an error knows the wait
// was cut short. Nothing here starts a service, writes a beacon or holds the
// lock: it reads, and the service that holds the lock is whatever process
// does.
func VerifyArm(ctx context.Context, h *home.Home, interval time.Duration, clock func() time.Time) (supervisor.Arm, error) {
	fresh := supervisor.FreshnessFor(interval)
	wait := supervisor.FirstBeaconWait(interval)
	pause := wait / armLooks
	asked, _ := ReadBeacon(h.Beacon())
	deadline := clock().Add(wait)
	for {
		obs, _ := ObserveFiles(ctx, h)
		now := clock()
		waitOver := !now.Before(deadline)
		if arm, settled := supervisor.ArmOf(asked, obs, fresh, now, waitOver); settled {
			return arm, nil
		}
		select {
		case <-ctx.Done():
			return supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmNoBeacon}, ctx.Err()
		case <-time.After(pause):
		}
	}
}

// join is errors.Join without the nil entries a caller collected, so a list
// of successes is nil rather than an empty joined error.
func join(errs []error) error {
	var kept []error
	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}
	return errors.Join(kept...)
}
