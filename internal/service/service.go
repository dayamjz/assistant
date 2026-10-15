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
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
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

// ErrRunEnding reports that the run's slot is held by an ending being written
// rather than by a segment. It is separate from ErrRunAdvancing because the
// two describe opposite things about the same slot: nothing is executing under
// an ending's, which is what isAdvancing answers about it, and a caller handed
// the other message would be told a run is moving while it is being ended.
//
// A caller meeting it may ask again once the ending is written, and will then
// be answered from the record rather than from the slot.
var ErrRunEnding = errors.New("service: this run is being ended")

// ErrRunsActive reports a stop or a restart refused because runs are active.
// PRD section 9 makes both refuse by default and requires an explicit force
// rather than a general yes-to-everything flag.
var ErrRunsActive = errors.New("service: runs are active")

// ErrNoRepository reports that the working copy a request named has no
// repository record, which is what a working copy that was never initialized
// looks like.
//
// It names the record rather than the gate, because the two are separate
// things and a gate can be standing when this fires. Saying the gate is
// missing sends a reader to look at something that is there, so the message
// says what is actually absent and which command writes it.
var ErrNoRepository = errors.New("service: this working copy has no repository record; run assistant init")

// DefaultLockWait is how long Open waits for the home's lock before refusing.
// It is not zero so that a service replacing one that is shutting down does
// not have to be retried by hand, and it is short so that a service nobody is
// replacing refuses promptly.
const DefaultLockWait = 3 * time.Second

// Options is everything the service is built from.
type Options struct {
	// Home is the root this service owns. It is created if it is not there.
	Home *home.Home
	// NewStages builds the nine stage implementations, given the build-scoped
	// dependencies a stage body reaches the world through. A stage
	// internal/stages has no body for is a placeholder that holds, so a build
	// short of all nine serves runs that stop at the first of those.
	//
	// It is a constructor rather than a value for the reason NewFixer is one:
	// a stage body's dependencies include the resolved agent and the run's
	// resolved configuration, and neither is known until a run needs them. It
	// is called once per resolved configuration, so anything varying per run
	// beyond that resolution is a declared state key rather than something
	// captured here.
	//
	// It is required. A service with no stages has no pipeline to run, and
	// defaulting to the nine internal/stages ships would make this package the
	// second place that decides what a run validates.
	NewStages func(stages.StageDeps) pipeline.Stages
	// NewFixer builds the fixer for the pipeline, given the dependencies a fix
	// body reaches the world through. It is a constructor rather than a value
	// for the reason NewStages is one: those dependencies include the resolved
	// agent and what internal/runs asks of it, neither of which is known until
	// this service has resolved one.
	//
	// What it is given is deliberately not a stages.StageDeps. A fix body needs
	// a route to the run's fixer and a stage body must not have one, which is
	// P4 at this seam; stages.FixDeps is that route and nothing a stage body
	// holds can reach it.
	//
	// It is required. internal/pipeline needs a fixer whenever any stage's fix
	// round limit is above zero, and zeroing the configured limits to avoid
	// supplying one would quietly turn every fix-eligible finding into a
	// question for a person.
	NewFixer func(stages.FixDeps) pipeline.Fixer
	// Build identifies the software, which PRD section 8 requires every run
	// to be traceable to. store.CurrentBuild reads it for the common caller.
	Build store.Build
	// Catalog is the agent adapters this build has. Zero means
	// agents.DefaultCatalog, built with the recorder that feeds this
	// service's store the agent invocation history; a supplied catalog
	// already constructed its factories, so it records only what its own
	// factories arranged.
	Catalog *agents.Catalog
	// Ancestry decides whether a caller is contained by an active validation
	// stage. Zero means this service's own registry of the stages it started,
	// which is what StageStarted writes to.
	Ancestry ipc.Ancestry
	// LockWait is how long to wait for the home's lock. Zero means
	// DefaultLockWait, and a negative value asks once and refuses.
	LockWait time.Duration
	// Environment is where the decision-model key, judge.KeyVariable, is
	// looked up, once, when the service opens. Nil means the process
	// environment through os.LookupEnv, which is the operator's: the key is
	// never read from a configuration file, and a key put in the environment
	// after the service opened is not seen until it restarts.
	Environment func(string) (string, bool)
	// Judge is further options for the decision-model client, applied before
	// the recorder this service gives it, so a test can point the client at a
	// stand-in provider. The product passes none.
	Judge []judge.Option
	// Forge is the code host every run of this service reaches: the one the
	// pull request and checks bodies are handed, and the one this service
	// re-reads a run's pull request through before resuming the hold the
	// checks stage raised when checks_timeout elapsed. Zero means GitHub over
	// the gh command line, which the product passes; a test passes a double
	// whose answers and refusals are shapes the GitHub host and adapter
	// produce: Open refuses only an argument it cannot use, and a provider
	// command line that cannot be run surfaces from a read as a *forge.Refusal
	// with forge.ReasonUnavailable, never from Open. It is one host for both
	// readers on purpose, so what a stage read and what the service re-reads
	// come from the same place.
	Forge forge.Host
	// Terminals is what the supervisor loop observes a task's terminal
	// through, and what every task reader this service builds probes
	// through, reconciliation's included. Nil, which is what the product
	// passes, is the service's own session provider, on terminals.go's
	// terms: the provider the launches go through, so a launched task's
	// terminal reads Live through the handle and a dead one reads Dead by
	// its identity. A test passes a double to script what a terminal
	// answers.
	Terminals Terminals
	// WorkerRoute is the command line prefix a launched worker reaches this
	// service through, on route.go's terms: internal/cli passes the absolute
	// path of the program, --home naming this home, and its worker verb.
	// Empty, which a test that opens the service without internal/cli
	// passes, launches every worker with git alone and tells it of no
	// route. A route internal/agents refuses is refused at each launch,
	// which is logged and leaves the task dispatched, on launch.go's terms
	// for a launch that fails.
	WorkerRoute string
	// hooks are the seams this package's tests drive the loop through. They
	// are here rather than on the Service because the loop starts inside
	// Open; supervise.go says what each is, and the product sets none.
	hooks supervisionHooks
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
	// wakes is the coordinator's wake stream, ipc.MethodWakesSubscribe's
	// publisher, which wakewait.go's publishWake is the one producer for.
	// It is a publisher of its own rather than events because events is
	// open to every caller and a wake is the coordinator's.
	wakes *ipc.Publisher

	listener net.Listener
	server   *ipc.Server

	checkpoints *checkpoints.Store
	newStages   func(stages.StageDeps) pipeline.Stages
	newFixer    func(stages.FixDeps) pipeline.Fixer
	// global is the operator's configuration document as a parsed layer, kept
	// because every run's resolution composes it with the repository's two
	// copies; cfg is that layer resolved on its own with the schema defaults,
	// which is what the service-scoped decisions read: the agent this service
	// resolves and the session policy its run service is built with.
	global config.Layer
	cfg    config.Config
	// digest identifies the operator's configuration document. It is the
	// digest a run's record is created with, before the run's own resolution
	// exists; begin replaces it with the run's own, which covers all three
	// documents, so PRD section 8's traceability is about what the run
	// actually resolved.
	digest string
	build  store.Build
	// instance identifies this serving process, so a caller that asked one
	// service to make way for another can tell the successor from the service
	// it replaced. It is minted here and nowhere else.
	instance string
	catalog  *agents.Catalog
	registry *registry
	// reaped is what recovery decided about each run process record at Open,
	// the same accounts the log carries; reap.go owns them. It is written
	// once, before anything is served, and not changed afterwards.
	reaped []reaped
	// judge is the decision-model client every run's review stage is handed.
	// It is built once at Open, from the key in the operator's environment,
	// with the recorder that feeds the store's decision-model record.
	judge *judge.Client
	// forge is the code host, settled at Open from Options.Forge; checkshold.go
	// is the one reader of it outside the stage dependencies.
	forge forge.Host
	// terminals is Options.Terminals, or heldTerminals over this service's
	// own provider when none was passed: the one seam a task's terminal is
	// observed through, which the supervisor loop probes and captures
	// through and every task reader this service builds probes through, so
	// the loop, the task surface and reconciliation cannot disagree about a
	// terminal. Open never leaves it nil; a Service this package's tests
	// build by hand may, and such a service observes no terminal anywhere.
	terminals Terminals
	// queue is internal/tasks' intake and release over this service's store,
	// built at Open from the branch gate and the gate repositories this
	// service knows; tasks.go says which of its entries this service calls.
	queue *tasks.Queue
	// cleaner is internal/tasks' cleanup decision over this service's store,
	// home and code host; cleanup.go says which of its entries this service
	// calls and from where.
	cleaner *tasks.Cleaner
	// workerRoute is Options.WorkerRoute, fixed when the service opens.
	workerRoute string

	// stageLogMu serializes appends to the per-stage logs and the reads of
	// them, so a section stagelog.go writes is never interleaved with another
	// and a read never sees half of one. stagelog.go owns it.
	stageLogMu sync.Mutex

	// mu guards the lazily built driver and topologies, the set of runs being
	// advanced, and the branch gates. It is never held across a run's
	// execution.
	mu    sync.Mutex
	built *driver
	// topologies caches one built pipeline and executor per resolved run
	// configuration, keyed by the resolution's digest. Growth is bounded by
	// the distinct configurations this service's runs resolve, which for one
	// home is the repositories it serves times the revisions of their
	// documents this process has seen.
	topologies map[string]*topology
	advancing  map[string]*slot
	// isolated holds, per task, the proof dispatch.go ended at, or the zero
	// value for a dispatch in flight or one that ended without a proof. It is
	// this service's memory and nothing durable: launch.go takes the proof
	// as the value it re-asserts before it starts a worker, and a restart
	// dispatches the task again. dispatch.go owns it and isolationOf is its
	// one reader. A relaunch takes the slot again for its new attempt,
	// through claimRelaunch, which is the one way a slot is retaken.
	isolated map[string]tasks.Isolation
	// claiming holds the tasks whose slot a dispatch or a relaunch has
	// taken and not yet settled, so a relaunch cannot retake a slot that a
	// claim in flight holds. dispatch.go owns it beside isolated.
	claiming map[string]bool
	// launched holds, per task, the terminal session launch.go started for
	// the latest attempt this service launched: one session per attempt,
	// one dispatch per task per service, and a relaunch replacing the
	// session it superseded, so one entry per task. It is this service's
	// memory, like the slot above: the attempt's record carries the
	// session's reference and nothing durable carries the handle, so a
	// service that restarts holds none of these and relaunches. Close ends
	// every session here, and launch.go owns the map.
	launched map[string]*session.Session
	// wakeBell is rung when the supervisor loop appends a wake, so an armed
	// wakes.wait reads the queue again; wakewait.go owns it and says why it
	// is a hint and never a record.
	wakeBell wakeBell
	// betweenAttemptReadAndClaim runs in a dispatch after the attempts have
	// been read and before the attempt is claimed. It is nil in production
	// and set only by this package's tests, which use it to hold two
	// dispatchers at the point where both have read and neither has written,
	// on the terms internal/tasks' queue offers its own tests the same hook.
	betweenAttemptReadAndClaim func()
	// sweepCopy stands in for the sweep this service asks of a copy: the
	// one relaunch.go and dispatch.go ask of a task's standing copy, and the
	// one runsweep.go asks of a finished run's. It is nil in production,
	// where the sweep is an agents.Sweeper over this service's home, and set
	// only by this package's tests, to an answer the real sweep gives on a
	// platform this test does not run on or to a recorder of what was asked.
	sweepCopy func(ctx context.Context, dir string) (agents.SweepResult, error)
	// givingUp is whether Close has stopped taking background work. It is
	// read and written under mu with the registration itself, which is what
	// orders a continuation started from a request-serving goroutine against
	// the wait below.
	givingUp bool
	// starting holds one gate per branch a start is being decided for, so the
	// check that a branch has no run and the creation of one are a single
	// decision rather than a check a second caller can win the race to.
	starting map[branchKey]*branchGate
	// mergeReadsSettled holds the runs mergeread.go's pass will not read
	// again in this service's life: a pull request read merged at another
	// head, and a run that recorded no observed head. It is memory and
	// nothing durable, guarded by mu; mergeread.go owns it.
	mergeReadsSettled map[string]bool
	// landedTried holds, per landed task, the session reference whose Dead
	// terminal the cleanup decision was last asked under, so the supervisor
	// pass asks once per reference rather than at every interval. It is
	// memory and nothing durable, guarded by mu; landed.go owns it.
	landedTried map[string]string

	revision atomic.Uint64
	// published is the basis of the last task.state this service sent per
	// task, which publish.go owns and the supervisor loop's pass compares
	// against to publish once per change.
	published published
	restart   atomic.Bool
	// stopCtx ends when this service stops. Background work this service
	// started, which has no caller's context to run under, runs under it.
	stopCtx    context.Context
	stopCancel context.CancelFunc
	stopping   chan struct{}
	stopOnce   sync.Once
	closeOnce  sync.Once
	closeErr   error
	work       sync.WaitGroup
	// sup is the supervisor loop's state: its beacon writer, its watchdog's
	// done channel, and what Close reads. It is nil until startSupervision,
	// which Open runs after recovery, and it is deliberately not registered
	// under work, because Close waits for it a bounded grace and for work
	// without bound; supervise.go owns the loop and Close's doc owns the
	// exception.
	sup *supervision
}

// driver is what a run needs that depends on an agent being runnable and does
// not vary with a run's configuration: the resolved adapter and the run
// service that owns the record moves and the fixer session.
//
// It is built when a run first needs one rather than when the service opens,
// because PRD section 10 resolves the agent against what is actually runnable
// at run start. A service that opened on a machine with no agent installed
// still serves every read, and starts a run once one is there.
//
// The topology is deliberately not here. A run's configuration is resolved
// per run, from the operator's layer and the repository document's two
// copies, and the pipeline is built from that resolution - its fix round
// limits, its budget, and the dependencies its stage bodies read commands and
// rules from - so it lives in the topology cache instead, keyed by the
// resolution's digest. What stays here is service-scoped on purpose: one
// agent and one run service answer for every run of this home, which is what
// keeps a run's lifecycle moves and its one fixer session independent of a
// configuration that may refuse to resolve.
type driver struct {
	agent agents.Resolution
	runs  *runs.Service
	// sessions is the terminal session provider launch.go starts a task's
	// worker through, and the one heldTerminals probes, captures and attaches
	// through, held as a session.Host because a capture and an attach by
	// reference are a held provider's own reads beside the five
	// session.Provider names; terminals.go's sessionHost decides which
	// provider it is. It is built over the resolved runner before
	// containRunner wraps it, because
	// the wrapper carries no worker arm on purpose - contain.go says why a
	// worker is not registered as a stage - so the one route to a worker is
	// the unwrapped agent, and this is the one place that agent is held
	// unwrapped.
	sessions session.Host
	// workers is the worker group set the provider above produces, held
	// here so a peer can be placed in a task's worker group without
	// asking the provider's concrete type; workers.go reads it. It is nil
	// until the driver is built, and a service with no driver has launched
	// no worker, so nobody is in a worker group before it exists.
	workers *session.Workers
}

// topology is one resolved configuration's pipeline and executor. Every run
// whose resolution digests the same shares one, and a run resumed under a
// changed resolution is handed a different one, which is what lets
// internal/graph's identity check refuse a resume whose bounds are no longer
// the ones the run's counters were indexed against.
type topology struct {
	pipeline *pipeline.Pipeline
	executor *graph.Executor
	// allowsConsent is whether this resolution's trusted copy allows standing
	// consent, config.KeyAllowStandingConsent. It is the resolution's and so
	// the topology's: the digest covers the documents it was read from, and
	// consent.go reads it per segment rather than once per run.
	allowsConsent bool
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
	case o.NewStages == nil:
		return nil, fmt.Errorf("%w: no stages constructor", ErrIncomplete)
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
	// The lifecycle log is opened as soon as the lock is held, so a start
	// that fails after it opened writes why to the log an operator is
	// pointed at. A failure to open it, and any failure before the lock is
	// held, is not written there: the log then cannot be written or belongs
	// to whichever service holds the home, and that failure is this
	// process's own to return.
	log, err := o.Home.OpenLog(0)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	unstarted := func(err error) error {
		log.Printf("the service could not start: %v", err)
		_ = log.Close()
		_ = lock.Release()
		return err
	}
	instance, err := newInstanceID()
	if err != nil {
		return nil, unstarted(err)
	}
	// PRD section 8 has the service write its instance identity once at
	// start, after it holds the lock, so that a process outside this one can
	// read which service last took the home. Nothing in this build reads it
	// back; the file says who wrote it and proves nothing about readiness.
	// A write that fails, or that is still refused at the end of its own
	// retry bound of about two seconds, still refuses the start: the file is
	// not state, but a home whose instance file names an earlier process
	// beside a lock this one holds is a reading a liveness check must never
	// be given, so the strict stance is to not serve rather than to serve
	// under another process's name.
	if err := o.Home.WriteInstance(instance); err != nil {
		return nil, unstarted(err)
	}
	s := &Service{
		home:       o.Home,
		lock:       lock,
		log:        log,
		instance:   instance,
		newStages:  o.NewStages,
		build:      o.Build,
		catalog:    o.Catalog,
		newFixer:   o.NewFixer,
		registry:   newRegistry(),
		topologies: make(map[string]*topology),
		advancing:  make(map[string]*slot),
		isolated:   make(map[string]tasks.Isolation),
		claiming:   make(map[string]bool),
		launched:   make(map[string]*session.Session),
		starting:   make(map[branchKey]*branchGate),
		stopping:   make(chan struct{}),

		workerRoute: o.WorkerRoute,
	}
	s.stopCtx, s.stopCancel = context.WithCancel(context.WithoutCancel(ctx))
	// The terminal seam is the service's own provider unless a test passed
	// a double; terminals.go owns what the product's answers through.
	s.terminals = o.Terminals
	if s.terminals == nil {
		s.terminals = heldTerminals{s}
	}
	s.forge = o.Forge
	if s.forge == nil {
		// Built with its own redactor because the redactor is otherwise
		// chosen per topology; both are redact.New, which internal/redact
		// makes the one owner of what is removed.
		s.forge = forge.NewGitHubHost(redact.New())
	}
	if s.catalog == nil {
		// The shipped adapters are built with the recorder that feeds the
		// store's agent invocation history, so a run's cost is recorded
		// without any caller arranging it. The recorder reads the store at
		// call time because the store opens below and adapters only invoke
		// once a run does, well after Open; a supplied catalog built its
		// factories already, so there this history holds only what that
		// caller's own factories arranged, which invocation.go states.
		s.catalog = agents.DefaultCatalog(
			agents.WithCatalogRecorder(invocationRecorder{s}))
	}
	lookup := o.Environment
	if lookup == nil {
		lookup = os.LookupEnv
	}
	s.judge = judge.FromEnvironment(lookup,
		append(append([]judge.Option(nil), o.Judge...), judge.WithRecorder(decisionRecorder{s}))...)
	if err := s.openParts(ctx, o); err != nil {
		s.log.Printf("the service could not start: %v", err)
		_ = s.Close()
		return nil, err
	}
	s.recover(ctx)
	// The supervisor loop starts after recovery and before the caller
	// serves, so a run recovery continued is under supervision before the
	// first request arrives and the beacon is being written before anything
	// reads it.
	if err := s.startSupervision(o.hooks, s.terminals); err != nil {
		s.log.Printf("the service could not start: %v", err)
		_ = s.Close()
		return nil, err
	}
	// A person's merge of a run that ended checks-passed is read for once
	// recovery is done and then on mergeread.go's interval, on a goroutine
	// of its own, so serving waits on no code host for it.
	s.startMergeReads()
	return s, nil
}

// openParts opens everything Open owns once the lock is held. It is separate
// so that a failure part way through is cleaned up by Close rather than by an
// unwinding sequence written twice.
func (s *Service) openParts(ctx context.Context, o Options) error {
	global, cfg, digest, err := resolveConfig(s.home.ConfigFile())
	if err != nil {
		return err
	}
	s.global, s.cfg, s.digest = global, cfg, digest
	records, err := store.Open(ctx, s.home.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		return err
	}
	s.store = records
	s.checkpoints = checkpoints.New(records)
	if s.queue, err = s.newQueue(); err != nil {
		return err
	}
	if s.cleaner, err = tasks.NewCleaner(tasks.CleanupDeps{Home: s.home, Store: s.store, Forge: s.forge}); err != nil {
		return err
	}

	// The containment registry's entries are recorded durably from here,
	// so what this service starts and does not live to end is something the
	// next service of this home can reap; reap.go owns the record.
	s.registry.durable = &processJournal{store: s.store, log: s.log}
	if s.events, err = ipc.NewPublisher(ipc.PublisherConfig{}); err != nil {
		return err
	}
	if s.wakes, err = ipc.NewPublisher(ipc.PublisherConfig{}); err != nil {
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
		Streams:  map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: s.wakes},
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
//
// Background work is refused before it is waited for, and the two are that way
// round on purpose. A continuation is started from whichever goroutine a
// segment ended on, so nothing orders its decision against this; what is
// ordered is its registration, which stopWork closes off under the mutex
// startWork registers under. Waiting first and refusing after would leave the
// interleaving where work is registered against a database this then closes.
//
// The supervisor loop is the one piece of work Close does not wait for
// without bound. A loop wedged inside a provider call that ignores its
// context cannot be ended, and a Close that waited for it would hold the
// store open and the lock taken for as long as the wedge lasts, which turns
// a wedged loop into an unstoppable service holding the home. So Close
// waits CloseGrace for the loop and its watchdog, and then abandons them
// with ErrSupervisionAbandoned in its result and goes on to close the store
// and release the lock. What makes that safe is the incarnation fence
// supervise.go states: the abandoned goroutine is refused at every write it
// could still make. The residual gap is the goroutine itself, which runs on
// inside this process until the wedged call returns or the process ends.
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
		s.stopWork()
		s.work.Wait()
		errs = append(errs, s.stopSupervision())
		// The workers this service launched are ended here, after the work
		// that could record against them and before the store they were
		// recorded in closes. What that ends is exactly the sessions this
		// service's provider created and still holds, on the terms
		// session.Provider.Close states; a worker an earlier service of this
		// home launched is no session of this one and goes on, which
		// launch.go states.
		errs = append(errs, s.closeSessions()...)
		if s.events != nil {
			errs = append(errs, s.events.Close())
		}
		if s.wakes != nil {
			errs = append(errs, s.wakes.Close())
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
//
// A slot standing for an ending rather than for a segment holds no segment to
// end, so it is passed over: nothing is executing under it.
func (s *Service) cancelAdvancing() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.advancing))
	for _, held := range s.advancing {
		if held.cancel != nil {
			cancels = append(cancels, held.cancel)
		}
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
// On a run's path nothing needs to call it, because registration is the
// wrapper's: driverFor wraps the resolved runner in containRunner, which sets
// agents.Invocation.Started on every invocation, and the adapter then reports
// the group when the process starts and its end when the tree has been
// terminated. This entry point remains for a launcher outside that path, and
// it registers in memory only: it writes no run process record, because the
// group is whatever its caller states, and a record recovery would act on
// has to name a group this service started, which only the wrapper knows.
// The residual gaps are the relation's own: a descendant that moves itself to
// a new process group is no longer in the registered one, which
// internal/agents' documentation names, and a peer whose group cannot be read
// while a stage runs has its request refused rather than passed, which
// contain.go states.
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
	// The session provider is built over the agent as resolved, before the
	// wrapper below takes its worker arm away; driver.sessions says why.
	sessions := s.sessionHost(resolution.Runner)
	// Every invocation of a run passes through the resolved runner or the
	// fixer it opens, so wrapping here is what registers each one's process
	// group with the containment registry for as long as it runs; contain.go
	// owns the wrapper and StageStarted's doc owns what registration buys.
	resolution.Runner = s.containRunner(resolution.Runner)
	runService, err := runs.New(runs.Options{
		Store:        s.store,
		Agent:        resolution,
		SessionReuse: s.cfg.SessionReuse,
		// Every ending of a run is a move of internal/runs, so the sweep of
		// its copy is decided there from the row the ending wrote rather than
		// at each site that ends one; runsweep.go owns what it does.
		OnFinish: func(ctx context.Context, run store.Run) { s.sweepFinished(ctx, run, store.SweptAtFinish) },
	})
	if err != nil {
		return nil, err
	}
	s.built = &driver{agent: resolution, runs: runService, sessions: sessions, workers: sessions.Workers()}
	s.log.Printf("resolved agent %s for runs of this service", resolution.Name)
	return s.built, nil
}

// topologyFor returns the pipeline and executor for one resolved run
// configuration, building them the first time that resolution is seen.
//
// The stage seam is built here rather than at Open for two reasons that used
// to be one. A stage body's dependencies include the resolved agent, which is
// not known until a run needs one; and they include the run's resolved
// configuration - the commands a check runs, the rules a review is held to -
// which is not known until the run's repository copies have been read. The
// agent is wrapped as an agents.StageAgent, which is what keeps P4 structural
// at this seam: a body handed the Runner itself could open a fixer session on
// it through agents.OpenFixer, and a StageAgent has no Runner to hand over.
//
// The code host is a forge.Host rather than a forge.Provider for the lifetime
// reason internal/stages' deps.go gives: a Provider addresses one repository
// and a topology serves every run that resolves this configuration, so the
// repository arrives per run instead, as pipeline.KeyForgeRepository. What is
// settled here is the rest of the adapter, including the redactor, which is
// the same one every repository this service opens is opened with and the one
// the bodies that persist or report a command's text apply at those
// boundaries.
//
// The checks body is handed this service's store as the record its wait on a
// pull request's checks is measured from, because checks_timeout has to
// survive the segment that began waiting: a resume after a restart or a
// walked-away caller re-enters the same wait rather than beginning another.
// It is handed the service's one code host too, so the pull request it reads
// while it waits and the one this service re-reads at the hold that wait
// leaves are read through the same adapter.
func (s *Service) topologyFor(built *driver, resolved runResolution) (*topology, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if topo, ok := s.topologies[resolved.digest]; ok {
		return topo, nil
	}
	redactor := redact.New()
	deps := stages.NewStageDeps(
		agents.NewStageAgent(built.agent.Runner),
		s.home,
		resolved.cfg,
		s.forge,
		redactor,
		vcs.WithRedactor(redactor),
	).WithJudge(s.judge).WithChecksWaits(s.store).WithRunRecords(s.store)
	// The fix seam is built here, beside the stage seam and deliberately apart
	// from it. It carries the one thing a stage body may not have: a route to
	// the run's fixer role, which internal/runs hands out per run and which
	// keeps the durable session those rounds share. P4 is what makes the two
	// separate values rather than one - a stage body that could reach a fixer
	// is a stage body that can fix what it is about to report on.
	fixDeps := stages.NewFixDeps(
		func(ctx context.Context, runID string) (agents.Fixer, error) {
			return built.runs.Fixer(ctx, runID)
		},
		s.home,
		resolved.cfg,
		built.runs.FixerRequires(),
		redactor,
		vcs.WithRedactor(redactor),
	)
	pipe, err := pipeline.New(pipeline.Options{
		Stages:                      s.observeStages(s.newStages(deps)),
		Fixer:                       s.observeFixer(s.newFixer(fixDeps), fixDeps.Copy),
		Rounds:                      resolved.cfg.FixRounds,
		Budget:                      resolved.cfg.RunBudget,
		Adapter:                     built.agent.Capabilities,
		SuppressProjectInstructions: resolved.cfg.SuppressProjectInstructions,
	})
	if err != nil {
		return nil, err
	}
	executor, err := pipe.Executor(s.checkpoints)
	if err != nil {
		return nil, err
	}
	topo := &topology{pipeline: pipe, executor: executor, allowsConsent: resolved.cfg.AllowStandingConsent}
	s.topologies[resolved.digest] = topo
	return topo, nil
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
