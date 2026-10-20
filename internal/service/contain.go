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
	// leader is the group leader's identity read when the group was
	// registered, and the zero Identity when it could not be read, which
	// forceGroups then passes over rather than signals.
	leader agents.Identity
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
	// durable is the run process record containRunner's registrations are
	// written to beside their entry here and cleared from with it, so a
	// service that dies with them leaves the next one a record to reap by;
	// reap.go owns it. Nil records nothing, which is a registry built
	// without a store.
	durable *processJournal
	// hurry is closed by hurryTerminations and rides every invocation the
	// wrappers start, through agents.WithHurry, so a forced stop cuts each
	// invocation's own termination short. hurried closes it once.
	hurry   chan struct{}
	hurried sync.Once
}

// newRegistry returns an empty registry, which is a service that has started
// no validation stage and therefore contains nobody.
func newRegistry() *registry {
	return &registry{groups: make(map[int]stage), hurry: make(chan struct{})}
}

// add records that a process group is running a stage, and returns the
// function that forgets it again.
func (r *registry) add(run, name string, pgid int) func() {
	entry := stage{run: run, stage: name}
	if leader, found, err := agents.Identify(pgid); err == nil && found {
		entry.leader = leader
	}
	r.mu.Lock()
	r.groups[pgid] = entry
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.groups, pgid)
		r.mu.Unlock()
	}
}

// hurryTerminations cuts short the termination of every invocation the
// wrappers started, on agents.WithHurry's terms.
func (r *registry) hurryTerminations() { r.hurried.Do(func() { close(r.hurry) }) }

// hurrying is ctx carrying this registry's hurry channel.
func (r *registry) hurrying(ctx context.Context) context.Context {
	return agents.WithHurry(ctx, r.hurry)
}

// held reports how many groups are registered.
func (r *registry) held() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.groups)
}

// leaders returns the identity of every registered group whose leader could
// be read when it was registered.
func (r *registry) leaders() []agents.Identity {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agents.Identity
	for _, entry := range r.groups {
		if entry.leader.PID > 0 {
			out = append(out, entry.leader)
		}
	}
	return out
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

// servingKey carries which stage a body or a fix round on ctx is serving,
// written by the observe wrappers around every stage body and the fix body
// and read by the containment wrapper below, on advancingKey's terms: a key
// of this package's own, set by this service for itself.
type servingKey struct{}

// withServing marks ctx as serving one stage.
func withServing(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, servingKey{}, stage)
}

// servedOn is the run and stage an invocation launched on ctx serves, as
// agents.Invocation.Served carries them into the invocation record: the run
// the segment advances and the stage whose body or fix round is executing.
// Either is empty where ctx was never marked with it, which the record keeps
// as unknown rather than a guess.
func servedOn(ctx context.Context) agents.Served {
	stage, _ := ctx.Value(servingKey{}).(string)
	return agents.Served{Run: advancingRun(ctx), Stage: stage}
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
// The stage the containment registry records is the invocation's purpose,
// which names the same work in the same words for every stage that launches
// an agent. The run is read off the segment context advance marked.
//
// The same seam stamps agents.Invocation.Served, which is what the agent
// invocation record names: the run the segment advances, and the pipeline
// stage the observe wrappers marked on ctx around the body or fix round that
// made the call. A fix round serves the stage whose findings it fixes, so
// there its purpose and its stage differ. What it does not reach is the same
// as for registration: a body that resolved an adapter of its own stands
// outside this wrapper, and its calls are recorded by whatever recorder that
// adapter was built with, if any.
//
// It is also where every such invocation is held to the operator's usage
// ceiling, before anything else: admitInvocation in ceiling.go is asked
// before the process is registered or launched, and a refusal is returned to
// the caller as the typed *machine.UsageCeilingError, written to the stage
// log like any error an invocation ends with, and never a row of the
// invocation record, because no call ran. A call already handed to the inner
// runner is not cancelled by a later refusal; ceiling.go owns what that
// leaves open.
//
// It is also where every such invocation reaches the run's stage log: once
// it returns, its account and its full reply, or the error it ended with, are
// written to the log of the stage agents.Served names, which stagelog.go
// owns and redacts. The invocation record keeps no output by design; the log
// is where the output is kept, as evidence.
//
// The wrapper preserves the shape agents.Resolve vouched for: a runner with
// resumable sessions stays an agents.SessionRunner and one without stays a
// plain agents.Runner, so agents.OpenFixer reads the same declaration and
// finds the same mechanism either way.
//
// It carries no worker arm on purpose, and that is the one place the shape is
// not preserved: an agents.WorkerRunner wrapped here is no WorkerRunner
// afterwards, and the wrapper's declaration says so, dropping
// agents.CapabilityWorkerSessions from what the agent declared so that the
// pair agents.Resolve holds every adapter to still agrees. agents.OpenWorker
// on the wrapped agent is therefore refused as undeclared, by name. A worker
// is not a validation stage. It pushes to the gate the way a person does, and
// a worker registered here would have every push refused ErrContained before
// admission, so a worker is obtained from the agent this wraps and registered
// in a set of whoever owns the task, never in this registry.
func (s *Service) containRunner(r agents.Runner) agents.Runner {
	wrapped := containedRunner{inner: r, registry: s.registry, log: s.logInvocation, admit: s.admitInvocation}
	if sessions, ok := r.(agents.SessionRunner); ok {
		return containedSessionRunner{containedRunner: wrapped, sessions: sessions}
	}
	return wrapped
}

// containedRunner is containRunner's plain half.
type containedRunner struct {
	inner    agents.Runner
	registry *registry
	// log writes what an invocation came to into the log of the stage it
	// served, which stagelog.go owns; it is the same seam's third use beside
	// registration and the stamp, for the same reason: every invocation of a
	// run passes here.
	log invocationLog
	// admit is the usage ceiling, asked before every call. containRunner
	// always sets it to the service's admitInvocation.
	admit func(context.Context) error
}

// invocationLog is how the wrappers hand an invocation's account and reply
// to the stage log: the run and stage it served, its purpose, and what it
// returned.
type invocationLog func(agents.Served, agents.Purpose, agents.Result, error)

func (c containedRunner) Name() string { return c.inner.Name() }

// Capabilities is the wrapped agent's declaration less worker sessions, which
// this wrapper does not carry; containRunner says why.
func (c containedRunner) Capabilities() agents.Capabilities {
	var kept []agents.Capability
	for _, capability := range c.inner.Capabilities().List() {
		if capability != agents.CapabilityWorkerSessions {
			kept = append(kept, capability)
		}
	}
	return agents.Declare(kept...)
}

func (c containedRunner) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (
	agents.Result, error) {
	if err := c.admit(ctx); err != nil {
		c.log.write(servedOn(ctx), purpose, agents.Result{}, err)
		return agents.Result{}, err
	}
	inv.Started = c.registry.starter(advancingRun(ctx), string(purpose))
	inv.Served = servedOn(ctx)
	result, err := c.inner.Run(c.registry.hurrying(ctx), purpose, inv)
	c.log.write(inv.Served, purpose, result, err)
	return result, err
}

// write hands one invocation to the stage log, and does nothing for a
// wrapper built without one.
func (l invocationLog) write(served agents.Served, purpose agents.Purpose, result agents.Result, err error) {
	if l != nil {
		l(served, purpose, result, err)
	}
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
	return containedFixer{inner: fixer, registry: c.registry, log: c.log, admit: c.admit}, nil
}

// containedFixer registers each fix round's invocation on the same terms as
// containedRunner registers a stage's, and writes it to the stage log on the
// same terms too, which is where a fixer's full reply is kept.
type containedFixer struct {
	inner    agents.Fixer
	registry *registry
	log      invocationLog
	admit    func(context.Context) error
}

func (c containedFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	if err := c.admit(ctx); err != nil {
		c.log.write(servedOn(ctx), agents.PurposeFix, agents.Result{}, err)
		return agents.Result{}, err
	}
	inv.Started = c.registry.starter(advancingRun(ctx), string(agents.PurposeFix))
	inv.Served = servedOn(ctx)
	result, err := c.inner.Apply(c.registry.hurrying(ctx), inv)
	c.log.write(inv.Served, agents.PurposeFix, result, err)
	return result, err
}

func (c containedFixer) Reference() string { return c.inner.Reference() }

// starter is the agents.Invocation.Started value the wrappers set: it
// registers the invocation's process group under the run and purpose it was
// launched for, writes the run process record reap.go keeps, and hands back
// the forget, which clears the record only when this registration wrote it.
// The adapter calls the forget after its termination of the invocation's
// tree has finished, so the record stands from just after the process
// exists until then; reap.go states what that span leaves out.
func (r *registry) starter(run, purpose string) func(pgid int) func() {
	return func(pgid int) func() {
		forget := r.add(run, purpose, pgid)
		recorded := r.durable.record(run, purpose, pgid)
		return func() {
			forget()
			if recorded {
				r.durable.clear(run, pgid)
			}
		}
	}
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

// SessionAgent forwards only the primitive owner of a stored fixer reference.
func (c containedRunner) SessionAgent(reference string) string {
	return agents.SessionAgent(c.inner, reference)
}
