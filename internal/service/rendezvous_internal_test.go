package service

import "sync"

// rendezvous holds each of n racers at Arrive until all n have arrived,
// which is how a test holds two dispatchers or relaunchers at the point
// where both have read and neither has written, through the
// betweenAttemptReadAndClaim hook. A racer that returns without arriving,
// because it was refused before that point, opens it for the rest through
// Leave, which every racer calls once its call has returned: the racers
// still at Arrive then go on alone, and the test reads the refusal from the
// racer's error rather than holding the others until the test binary's
// timeout. A sweep that does not clear is one such refusal, since a
// relaunch sweeps its standing copy before it reaches the hook (#381).
// When every racer arrives, Leave changes nothing, so the race the test is
// of is the same race.
type rendezvous struct {
	n       int
	mu      sync.Mutex
	arrived int
	open    chan struct{}
	once    sync.Once
}

// newRendezvous is a rendezvous of n racers.
func newRendezvous(n int) *rendezvous {
	return &rendezvous{n: n, open: make(chan struct{})}
}

// Arrive holds the calling racer until all n have arrived or one has left
// without arriving.
func (r *rendezvous) Arrive() {
	r.mu.Lock()
	r.arrived++
	if r.arrived >= r.n {
		r.release()
	}
	r.mu.Unlock()
	<-r.open
}

// Leave is called by every racer once its call has returned, and opens the
// rendezvous for any racer still held.
func (r *rendezvous) Leave() { r.release() }

func (r *rendezvous) release() { r.once.Do(func() { close(r.open) }) }
