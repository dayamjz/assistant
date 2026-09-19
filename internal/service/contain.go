package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
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

// advancingKey carries which run a segment advances, written by advance and
// read by the containment wrapper below. It is a key of this package's own,
// set by this service for itself within one process, so nothing here rests on
// what a caller says: a peer's containment is still decided from kernel
// credentials and this registry alone.
type advancingKey struct{}

// withAdvancing marks ctx as advancing one run.
func withAdvancing(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, advancingKey{}, runID)
}

// advancingRun is the run the segment on ctx advances, empty where the
// invocation runs outside one. Empty does not weaken containment - the group
// is registered either way - it only leaves the refusal without a run to name.
func advancingRun(ctx context.Context) string {
	runID, _ := ctx.Value(advancingKey{}).(string)
	return runID
}

// containRunner wraps the resolved agent so that every invocation it runs is
// registered with this service's registry for as long as its process group
// exists. It is what makes StageStarted called rather than callable: the
// wrapper stands where every invocation of a run passes - the stage bodies
// reach the runner through agents.StageAgent and the fix rounds through the
// fixer the wrapped runner opens - so no stage body has to remember to
// register anything on the resolved agent's path. A body that resolved an
// adapter of its own would stand outside it, which is the same gap
// stages.StageDeps already names for P4, not a new one this wrapper opens.
//
// The stage recorded is the invocation's purpose, because that is what this
// seam can say truthfully: the pipeline stage that asked is not in an
// invocation, and the purpose names the same work in the same words for every
// stage that launches an agent. The run is read off the segment context
// advance marked.
//
// The wrapper preserves the shape agents.Resolve vouched for: a runner with
// resumable sessions stays an agents.SessionRunner and one without stays a
// plain agents.Runner, so agents.OpenFixer reads the same declaration and
// finds the same mechanism either way.
func (s *Service) containRunner(r agents.Runner) agents.Runner {
	wrapped := containedRunner{inner: r, registry: s.registry}
	if sessions, ok := r.(agents.SessionRunner); ok {
		return containedSessionRunner{containedRunner: wrapped, sessions: sessions}
	}
	return wrapped
}

// containedRunner is containRunner's plain half.
type containedRunner struct {
	inner    agents.Runner
	registry *registry
}

func (c containedRunner) Name() string                      { return c.inner.Name() }
func (c containedRunner) Capabilities() agents.Capabilities { return c.inner.Capabilities() }

func (c containedRunner) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (
	agents.Result, error) {
	inv.Started = c.registry.starter(advancingRun(ctx), string(purpose))
	return c.inner.Run(ctx, purpose, inv)
}

// containedSessionRunner is containRunner's session-carrying half, and the
// fixer it opens registers its rounds the same way.
type containedSessionRunner struct {
	containedRunner
	sessions agents.SessionRunner
}

func (c containedSessionRunner) Fixer(ctx context.Context, resume string) (agents.Fixer, error) {
	fixer, err := c.sessions.Fixer(ctx, resume)
	if err != nil {
		return nil, err
	}
	return containedFixer{inner: fixer, registry: c.registry}, nil
}

// containedFixer registers each fix round's invocation on the same terms as
// containedRunner registers a stage's.
type containedFixer struct {
	inner    agents.Fixer
	registry *registry
}

func (c containedFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	inv.Started = c.registry.starter(advancingRun(ctx), string(agents.PurposeFix))
	return c.inner.Apply(ctx, inv)
}

func (c containedFixer) Reference() string { return c.inner.Reference() }

// starter is the agents.Invocation.Started value the wrappers set: it
// registers the invocation's process group under the run and purpose it was
// launched for, and hands back the forget.
func (r *registry) starter(run, purpose string) func(pgid int) func() {
	return func(pgid int) func() { return r.add(run, purpose, pgid) }
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
