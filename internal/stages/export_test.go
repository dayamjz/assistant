package stages

import (
	"context"
	"time"
)

// WithChecksClock returns these dependencies with the clock the checks body
// tells the time and waits by replaced, so a test can drive a wait of days in
// no time. now answers what time it is, and pause is called in place of
// sleeping for d. It exists only in this package's tests: the product waits on
// the wall clock.
func WithChecksClock(d StageDeps, now func() time.Time, pause func(context.Context, time.Duration) error) StageDeps {
	d.clock = checksClock{nowFunc: now, pauseFunc: pause}
	return d
}
