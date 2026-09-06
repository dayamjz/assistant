package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// ErrIncomplete reports that the service was asked for without something it
// cannot be built without. There is no defensible default for any of them: a
// service with no home has nowhere to be, and one with no stages has no
// pipeline to run.
var ErrIncomplete = errors.New("service: cannot be built as asked")

// ErrRunAdvancing reports that a run is already being advanced here.
// Advancing one run twice at once would have internal/graph refuse the loser's
// checkpoint after its node body had already run, so the slot is taken before
// a segment starts and this is what a second one meets.
//
// Attaching never reports it: asking where a run stands is answered by saying
// that it is moving, so that path takes the slot, finds it held, and reports
// the run. Where it does reach a caller is responding, in the window between a
// segment settling the record and giving the slot back, which is short and
// real. Retrying is what a caller does about it.
var ErrRunAdvancing = errors.New("service: this run is already advancing")

// ErrRunsActive reports a stop or a restart refused because runs are active.
// PRD section 9 makes both refuse by default and requires an explicit force
// rather than a general yes-to-everything flag.
var ErrRunsActive = errors.New("service: runs are active")

// ErrNoRepository reports that the working copy a request named has no
// repository record, which is what a working copy that was never initialized
// looks like.
var ErrNoRepository = errors.New("service: this working copy has no gate")

// DefaultLockWait is how long Open waits for the home's lock before refusing.
// It is not zero so that a service replacing one that is shutting down does
// not have to be retried by hand, and it is short so that a service nobody is
// replacing refuses promptly.
const DefaultLockWait = 3 * time.Second

// Options is everything the service is built from.
type Options struct {
	// Home is the root this service owns. It is created if it is not there.
	Home *home.Home
	// Stages are the nine stage implementations, which internal/stages
	// supplies. A build with no bodies written supplies nine that hold.
	Stages pipeline.Stages
	// NewFixer builds the fixer for the pipeline, given what the run's fixer
	// path needs of the agent adapter. It is a constructor rather than a value
	// because that requirement is internal/runs' answer and is not known until
	// this service has resolved an agent.
	//
	// It is required. internal/pipeline needs a fixer whenever any stage's fix
	// round limit is above zero, and zeroing the configured limits to avoid
	// supplying one would quietly turn every fix-eligible finding into a
	// question for a person.
	NewFixer func(requires []agents.Capability) pipeline.Fixer
	// Build identifies the software, which PRD section 8 requires every run
	// to be traceable to. store.CurrentBuild reads it for the common caller.
	Build store.Build
	// Catalog is the agent adapters this build has. Zero means
	// agents.DefaultCatalog.
	Catalog *agents.Catalog
	// Ancestry decides whether a caller is contained by an active validation
	// stage. Zero means this service's own registry of the stages it started,
	// which is what StageStarted writes to.
	Ancestry ipc.Ancestry
	// LockWait is how long to wait for the home's lock. Zero means
	// DefaultLockWait, and a negative value asks once and refuses.
	LockWait time.Duration
}

// Service is one home's background service.
//
// It is safe for concurrent use. Its one exclusion is per run: a run advances
// in one place at a time, and everything else is either read-only or
// serialized by the packages it composes.
type Service struct {
	home   *home.Home
	store  *store.Store
	lock   *home.Lock
	log    *home.Log
	events *ipc.Publisher

	listener net.Listener
	server   *ipc.Server

	checkpoints *checkpoints.Store
	stages      pipeline.Stages
	newFixer    func(requires []agents.Capability) pipeline.Fixer
	cfg         config.Config
	// digest identifies the configuration document a run resolved, which PRD
	// section 8 requires on every run so a surprising verdict traces to the
	// settings that reached it as well as to the code.
	digest string
	build  store.Build
	// instance identifies this serving process, so a caller that asked one
	// service to make way for another can tell the successor from the service
	// it replaced. It is minted here and nowhere else.
	instance string
	catalog  *agents.Catalog
	registry *registry

	// mu guards the lazily built driver, the set of runs being advanced, and
	// the branch gates. It is never held across a run's execution.
	mu        sync.Mutex
	built     *driver
	advancing map[string]context.CancelFunc
	// starting holds one gate per branch a start is being decided for, so the
	// check that a branch has no run and the creation of one are a single
	// decision rather than a check a second caller can win the race to.
	starting map[branchKey]*branchGate

	revision atomic.Uint64
	restart  atomic.Bool
	// stopCtx ends when this service stops. Background work this service
	// started, which has no caller's context to run under, runs under it.
	stopCtx    context.Context
	stopCancel context.CancelFunc
	stopping   chan struct{}
	stopOnce   sync.Once
	closeOnce  sync.Once
	closeErr   error
	work       sync.WaitGroup
}

// driver is everything a run needs that depends on an agent being runnable:
// the resolved adapter, the run service that owns the fixer session, the
// topology built against that adapter's declaration, and the executor.
//
// It is built when a run first needs one rather than when the service opens,
// because PRD section 10 resolves the agent against what is actually runnable
// at run start. A service that opened on a machine with no agent installed
// still serves every read, and starts a run once one is there.
type driver struct {
	agent    agents.Resolution
	runs     *runs.Service
	pipeline *pipeline.Pipeline
	executor *graph.Executor
}

// Open takes the home's lock, opens its database, binds its socket, and
// recovers the runs it finds. The service owns everything it opened from here
// and gives it all up in Close.
//
// The order is PRD section 8's and is not an implementation detail: the lock
// is taken before recovery and before the socket is bound, so a second service
// cannot be part way through either when it finds out it is not the one.
func Open(ctx context.Context, o Options) (*Service, error) {
	switch {
	case o.Home == nil:
		return nil, fmt.Errorf("%w: no home", ErrIncomplete)
	case o.NewFixer == nil:
		return nil, fmt.Errorf("%w: no fixer constructor", ErrIncomplete)
	case o.Build.Validate() != nil:
		return nil, fmt.Errorf("%w: %w", ErrIncomplete, o.Build.Validate())
	}
	if err := o.Home.Create(); err != nil {
		return nil, err
	}
	wait := o.LockWait
	if wait == 0 {
		wait = DefaultLockWait
	}
	lock, err := o.Home.Acquire(ctx, wait)
	if err != nil {
		return nil, err
	}
	instance, err := newInstanceID()
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	s := &Service{
		home:      o.Home,
		lock:      lock,
		instance:  instance,
		stages:    o.Stages,
		build:     o.Build,
		catalog:   o.Catalog,
		newFixer:  o.NewFixer,
		registry:  newRegistry(),
		advancing: make(map[string]context.CancelFunc),
		starting:  make(map[branchKey]*branchGate),
		stopping:  make(chan struct{}),
	}
	s.stopCtx, s.stopCancel = context.WithCancel(context.WithoutCancel(ctx))
	if s.catalog == nil {
		s.catalog = agents.DefaultCatalog()
	}
	if err := s.openParts(ctx, o); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.recover(ctx)
	return s, nil
}

// openParts opens everything Open owns once the lock is held. It is separate
// so that a failure part way through is cleaned up by Close rather than by an
// unwinding sequence written twice.
func (s *Service) openParts(ctx context.Context, o Options) error {
	cfg, digest, err := resolveConfig(s.home.ConfigFile())
	if err != nil {
		return err
	}
	s.cfg, s.digest = cfg, digest
	records, err := store.Open(ctx, s.home.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		return err
	}
	s.store = records
	s.checkpoints = checkpoints.New(records)

	if s.log, err = s.home.OpenLog(0); err != nil {
		return err
	}
	if s.events, err = ipc.NewPublisher(ipc.PublisherConfig{}); err != nil {
		return err
	}

	// A socket left behind by a service that was killed names nothing, because
	// this process holds the lock that would have stopped a live one from
	// being there. Removing it is safe only in that order.
	if err := os.Remove(s.home.Socket()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service: clearing the stale socket: %w", err)
	}
	if s.listener, err = net.Listen("unix", s.home.Socket()); err != nil {
		return fmt.Errorf("service: binding %s: %w", s.home.Socket(), err)
	}

	ancestry := o.Ancestry
	if ancestry == nil {
		ancestry = s.registry
	}
	s.server, err = ipc.NewServer(ipc.ServerConfig{
		Handler:  ipc.HandlerFunc(s.handle),
		Ancestry: ancestry,
		Events:   s.events,
		ReportPanic: func(value any, stack []byte) {
			s.log.Printf("a handler panicked: %v\n%s", value, stack)
		},
	})
	if err != nil {
		return err
	}
	s.log.Printf("service opened on %s, build %s", s.home.Socket(), s.build)
	return nil
}

// Socket is the endpoint this service is serving on.
func (s *Service) Socket() string { return s.home.Socket() }

// Serve accepts connections until ctx ends, until a caller stops the service
// through the protocol, or until the listener fails. It returns nil for the
// first two.
func (s *Service) Serve(ctx context.Context) error {
	accepted := make(chan error, 1)
	go func() { accepted <- s.server.Serve(s.listener) }()
	select {
	case err := <-accepted:
		return err
	case <-ctx.Done():
		return nil
	case <-s.stopping:
		return nil
	}
}

// Restarting reports whether the service was stopped in order to be replaced.
// A caller that started this service reads it after Serve returns and starts a
// successor.
func (s *Service) Restarting() bool { return s.restart.Load() }

// Stop ends serving. It is what the protocol's stop and restart reach, and it
// returns immediately: Serve returns, and Close is the caller's.
func (s *Service) Stop(restarting bool) {
	s.stopOnce.Do(func() {
		s.restart.Store(restarting)
		close(s.stopping)
		s.stopCancel()
	})
}

// Close gives up everything the service opened, in the reverse of the order it
// took them, and waits for the work it started.
//
// The lock is released last, so nothing this service still holds outlives the
// point at which another service may take the home.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.Stop(false)
		var errs []error
		if s.server != nil {
			errs = append(errs, s.server.Close())
		}
		if s.listener != nil {
			errs = append(errs, s.listener.Close())
		}
		s.cancelAdvancing()
		s.work.Wait()
		if s.events != nil {
			errs = append(errs, s.events.Close())
		}
		if s.store != nil {
			errs = append(errs, s.store.Close())
		}
		if s.log != nil {
			s.log.Printf("service closed")
			errs = append(errs, s.log.Close())
		}
		if s.lock != nil {
			errs = append(errs, s.lock.Release())
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// cancelAdvancing ends every run this service is executing. A run's agent
// processes are scoped to the context its round runs on, so cancelling is what
// ends them, per PRD section 8's process lifetime rule.
func (s *Service) cancelAdvancing() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.advancing))
	for _, cancel := range s.advancing {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// StageStarted records that a validation stage's agent is running in a process
// group, and returns the function that forgets it when the stage ends.
//
// It is the producer of the containment relation PRD section 9 rests on: a
// caller whose process group is one of these is inside a validation stage and
// is refused every restricted method. internal/agents starts an agent as the
// leader of a new process group, which is the identifier to pass here.
//
// Nothing in this build calls it, because no stage launches an agent yet. That
// is stated in doc.go rather than hidden: until a stage launcher calls this,
// the registry is empty and containment refuses nobody.
func (s *Service) StageStarted(run, stage string, pgid int) func() {
	return s.registry.add(run, stage, pgid)
}

// driverFor returns the driver, building it the first time a run needs one.
//
// A failure is not remembered. Resolving an agent asks what is runnable on
// this machine now, so a service that opened before an agent was installed
// answers differently once it is, and remembering the first refusal would make
// that need a restart.
func (s *Service) driverFor(ctx context.Context) (*driver, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.built != nil {
		return s.built, nil
	}
	resolution, err := agents.Resolve(ctx, s.cfg.Agent, s.catalog)
	if err != nil {
		return nil, err
	}
	runService, err := runs.New(runs.Options{
		Store:        s.store,
		Agent:        resolution,
		SessionReuse: s.cfg.SessionReuse,
	})
	if err != nil {
		return nil, err
	}
	built, err := pipeline.New(pipeline.Options{
		Stages:                      s.stages,
		Fixer:                       s.newFixer(runService.FixerRequires()),
		Rounds:                      s.cfg.FixRounds,
		Budget:                      s.cfg.RunBudget,
		Adapter:                     resolution.Capabilities,
		SuppressProjectInstructions: s.cfg.SuppressProjectInstructions,
	})
	if err != nil {
		return nil, err
	}
	executor, err := built.Executor(s.checkpoints)
	if err != nil {
		return nil, err
	}
	s.built = &driver{agent: resolution, runs: runService, pipeline: built, executor: executor}
	s.log.Printf("resolved agent %s for runs of this service", resolution.Name)
	return s.built, nil
}

// newInstanceID mints the identifier one serving process is known by. It is
// random rather than the operating system's process identifier, which is
// reused, and it carries no meaning: what it is for is telling two services of
// one home apart.
func newInstanceID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("service: generating a service identifier: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
