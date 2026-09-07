package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/ipc"
)

// stage names one validation stage that is running, so a refusal can say what
// contains the caller rather than only that something does.
type stage struct {
	run   string
	stage string
}

// registry is what this service knows about the validation stages it started:
// one entry per process group, added when a stage's agent is launched and
// removed when it ends.
//
// It is the whole of what containment rests on. Nothing a caller sends reaches
// it, and nothing outside this service writes to it, so the question "is this
// peer inside a validation stage" is answered from what this process did
// rather than from what the peer says about itself.
type registry struct {
	mu     sync.Mutex
	groups map[int]stage
}

// newRegistry returns an empty registry, which is a service that has started
// no validation stage and therefore contains nobody.
func newRegistry() *registry {
	return &registry{groups: make(map[int]stage)}
}

// add records that a process group is running a stage, and returns the
// function that forgets it again.
func (r *registry) add(run, name string, pgid int) func() {
	r.mu.Lock()
	r.groups[pgid] = stage{run: run, stage: name}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.groups, pgid)
		r.mu.Unlock()
	}
}

// empty reports whether this service is running no validation stage at all.
func (r *registry) empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.groups) == 0
}

// lookup returns the stage a process group is running, if any.
func (r *registry) lookup(pgid int) (stage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.groups[pgid]
	return s, ok
}

// Contained reports whether the process on the other end of a connection is
// running inside a validation stage this service started.
//
// It takes the credentials the kernel attributed to the connection, never a
// marker or an identifier a caller wrote, which is what internal/ipc's
// Ancestry interface exists to require.
//
// With no validation stage running there is nothing for a caller to be inside,
// so the answer is no without asking the operating system anything. That is
// not a check skipped: it is a question with no content, because containment
// is a relation to a running stage and there is none. Where there is one, the
// peer's process group is read, and a read that fails refuses the request
// rather than letting it through, because a fact that could not be established
// has not said no.
func (r *registry) Contained(_ context.Context, c ipc.Credentials) (ipc.Containment, error) {
	if r.empty() {
		return ipc.Containment{}, nil
	}
	pgid, err := processGroup(c.PID)
	if err != nil {
		return ipc.Containment{}, fmt.Errorf("reading the process group of %s: %w", c, err)
	}
	found, ok := r.lookup(pgid)
	if !ok {
		return ipc.Containment{}, nil
	}
	return ipc.Containment{Contained: true, Run: found.run, Stage: found.stage}, nil
}
