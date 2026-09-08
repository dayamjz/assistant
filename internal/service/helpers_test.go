package service_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestMain lets this binary act as the stand-in agent. The service resolves an
// agent before it can start a run, and a test that reached the operator's real
// agent would be running whatever happens to be installed.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
}

// platformIdentifiesPeers reports whether internal/ipc has a read for local
// socket peer credentials here. It is written down rather than derived,
// exactly as that package's own tests write it down, so a skip cannot hide a
// regression on a platform that does identify peers.
func platformIdentifiesPeers() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// requiresLocalSocket skips a test whose service could not bind its socket on
// a platform that has no local socket transport to serve this protocol over.
// It is the same answer internal/ipc's own tests give, and it is bounded the
// same way: a platform that does identify peers is one where a failure to
// bind is a failure.
func requiresLocalSocket(t *testing.T, cause error) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, cause)
	}
}

// requiresIdentifiedPeer skips a test that drives a run. Starting, answering,
// cancelling and stopping are restricted methods, and internal/ipc refuses
// every one of them when the kernel cannot say who is calling.
func requiresIdentifiedPeer(t *testing.T) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s reports no local socket peer credentials, so every method that drives a run is refused", runtime.GOOS)
	}
}

// newHome returns a home root short enough to hold a local socket path. The
// operating system bounds that path well below what a temporary directory
// named after a test would produce, so this does not use t.TempDir.
func newHome(t *testing.T) *home.Home {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	return h
}

// git runs a git command in a directory, for building the subject repository a
// run validates. It shells out on the same terms internal/vcs's and
// internal/gate's own tests do: a working copy assembled with the code under
// test could not show that code wrong.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newSubject returns a working copy with one commit on its default branch.
func newSubject(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatalf("making a subject repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	git(t, dir, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "first")
	// The path a repository record is filed under is the resolved one, which
	// is what the service compares against.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}
	return resolved
}

// writeFile writes a file in the subject repository.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// recordRepository writes the repository record a run needs, which assistant
// init writes in the product.
func recordRepository(t *testing.T, h *home.Home, workingPath string) store.Repository {
	t.Helper()
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = records.Close() }()
	repository, err := records.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   workingPath,
		UpstreamURL:   "https://example.invalid/o/r.git",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return repository
}

// scriptedAgent is a catalog holding the scripted stand-in under the name the
// production adapter carries, so a service resolving "auto" reaches it.
func scriptedAgent(t *testing.T) *agents.Catalog {
	t.Helper()
	runner := standin.New(t, standin.Script{}).Runner()
	return agents.NewCatalog(fixedFactory{runner: runner})
}

// fixedFactory hands back a Runner somebody else built. It builds no Runner of
// its own, so nothing here can produce an adapter the production path could
// not.
type fixedFactory struct{ runner agents.Runner }

func (f fixedFactory) Name() string { return f.runner.Name() }

func (f fixedFactory) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }

// options is what a test opens a service with: the pending stages the product
// wires, the scripted agent, and this build's identity.
func options(t *testing.T, h *home.Home) service.Options {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	return service.Options{
		Home:      h,
		NewStages: stages.All,
		NewFixer:  stages.PendingFixer,
		Build:     build,
		Catalog:   scriptedAgent(t),
		// Ask for the home's lock once and refuse rather than waiting. A test
		// that opens a second service while the first is still holding the
		// home is a test whose restart did not happen, and a bounded wait
		// would let it pass a little later instead of failing.
		LockWait: -1,
	}
}

// dial connects to a service's socket.
func dial(t *testing.T, running *service.Service) *ipc.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, running.Socket(), ipc.ClientConfig{})
	if err != nil {
		t.Fatalf("dialing %s: %v", running.Socket(), err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// startRun starts or attaches to the run for a working copy and returns where
// it stopped.
//
// It is startRunSkipping with nothing skipped, so the request a run is started
// with is stated once. Skip is omitempty, so a run started here and a run
// started there differ in the skip list and in nothing else.
func startRun(t *testing.T, client *ipc.Client, workingPath string) machine.Run {
	t.Helper()
	return startRunSkipping(t, client, workingPath)
}

// startRunSkipping starts a run that does not take the named stages.
//
// It exists for the one thing this build cannot walk through: a stage body
// that needs a dependency this service does not construct. Nothing here builds
// a code host, so the pull request stage fails rather than holding, and a test
// that wants to reach the end of a run has to go around it. The skip is a run
// input, which is a person's per-run choice, so this drives the surface a
// person would rather than weakening what the stage does.
//
// It names the stage rather than deriving it, and that name goes away when
// this service constructs a provider.
func startRunSkipping(t *testing.T, client *ipc.Client, workingPath string, skip ...pipeline.Stage) machine.Run {
	t.Helper()
	names := make([]string, len(skip))
	for i, stage := range skip {
		names[i] = stage.String()
	}
	var run machine.Run
	err := client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working: machine.Working{WorkingPath: workingPath},
		Intent:  "a change with acceptance criteria stated up front",
		Skip:    names,
	}, &run)
	if err != nil {
		t.Fatalf("starting a run with skip %v: %v", names, err)
	}
	return run
}

// answer answers the decision a run is holding on.
func answer(t *testing.T, client *ipc.Client, runID, with string) machine.Run {
	t.Helper()
	var run machine.Run
	err := client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{Run: runID, Answer: with}, &run)
	if err != nil {
		t.Fatalf("answering run %s with %q: %v", runID, with, err)
	}
	return run
}

// holdingAt asserts that a run is waiting for a decision at one stage's hold,
// and returns the decision.
func holdingAt(t *testing.T, run machine.Run, stage pipeline.Stage) machine.Decision {
	t.Helper()
	if run.Outcome != machine.OutcomeDecision {
		t.Fatalf("the run reports %s, want a decision", run.Outcome)
	}
	if run.Decision == nil {
		t.Fatal("the run reports a decision and carries none")
	}
	if run.Decision.Stage != stage.String() {
		t.Fatalf("the run is holding at %s, want %s", run.Decision.Stage, stage)
	}
	return *run.Decision
}

// pendingStage returns the nth stage this build has no body for, counting from
// zero in the order a run takes them. Those are the stages a run holds at, so
// a test that walks a run from one hold to the next names them this way rather
// than by stage: a run advances through every stage that has a body, so naming
// one would make these tests fail the day a body lands for a reason unrelated
// to what they check.
func pendingStage(t *testing.T, n int) pipeline.Stage {
	t.Helper()
	implemented := make(map[pipeline.Stage]bool)
	for _, stage := range stages.Implemented() {
		implemented[stage] = true
	}
	var pending []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if !implemented[stage] {
			pending = append(pending, stage)
		}
	}
	if n >= len(pending) {
		t.Fatalf("this build has %d stages without a body and this test walks a run to hold %d; "+
			"it needs rewriting against whatever now holds a run", len(pending), n)
	}
	return pending[n]
}

// stageView returns the run's view of one named stage. A test asks by stage
// rather than by position because where a stage sits in the answer moves as
// bodies land, and a fixed index quietly starts reading a different stage
// instead of failing.
func stageView(t *testing.T, run machine.Run, stage pipeline.Stage) machine.Stage {
	t.Helper()
	for _, view := range run.Stages {
		if view.Stage == stage.String() {
			return view
		}
	}
	t.Fatalf("the run reports no %s stage: %+v", stage, run.Stages)
	return machine.Stage{}
}

// serviceUnderTest is a running service and a connection to it.
type serviceUnderTest struct {
	service *service.Service
	client  *ipc.Client
}

// withService opens a service, hands it to the body with a connection, and
// closes it before returning.
//
// Closing rather than leaving it to the test's cleanup is the point: a test
// that calls this twice has the first service gone before the second opens, so
// the second reads the run's position out of the database rather than out of
// anything the first left in memory.
//
// Two things make that a mechanism rather than a hope. Service.Close releases
// the home's lock last, after the database is closed, and the options here ask
// for that lock once and refuse, so a second service opened while the first
// still held the home fails to open at all rather than waiting for it. And
// homeIsFree asserts the same fact directly between the phases of the restart
// test, so the claim is checked where it is being relied on.
func withService(t *testing.T, h *home.Home, body func(serviceUnderTest)) {
	t.Helper()
	withServiceOptions(t, options(t, h), body)
}

// withServiceOptions is withService for a test that needs a service built
// differently. The options are the caller's, so what withService's comment
// says about the home's lock holds where those options keep it: build them
// from options rather than from scratch.
func withServiceOptions(t *testing.T, o service.Options, body func(serviceUnderTest)) {
	t.Helper()
	running, err := service.Open(t.Context(), o)
	if err != nil {
		requiresLocalSocket(t, err)
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()

	client, dialErr := ipc.Dial(t.Context(), running.Socket(), ipc.ClientConfig{})
	if dialErr != nil {
		_ = running.Close()
		<-served
		t.Fatalf("dialing %s: %v", running.Socket(), dialErr)
	}
	func() {
		defer func() {
			_ = client.Close()
			if err := running.Close(); err != nil {
				t.Errorf("closing the service: %v", err)
			}
			if err := <-served; err != nil {
				t.Errorf("serving: %v", err)
			}
		}()
		body(serviceUnderTest{service: running, client: client})
	}()
}

// serviceWithClient opens a service the test closes itself, for a test whose
// subject is the stopping.
func serviceWithClient(t *testing.T, h *home.Home) (serviceUnderTest, error) {
	t.Helper()
	running, err := service.Open(t.Context(), options(t, h))
	if err != nil {
		return serviceUnderTest{}, err
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		_ = running.Close()
		if err := <-served; err != nil {
			t.Errorf("serving: %v", err)
		}
	})
	client, err := ipc.Dial(t.Context(), running.Socket(), ipc.ClientConfig{})
	if err != nil {
		return serviceUnderTest{}, err
	}
	t.Cleanup(func() { _ = client.Close() })
	return serviceUnderTest{service: running, client: client}, nil
}

// openRecords opens a home's database directly, for a test that has to put a
// record where a service that died would have left it.
func openRecords(t *testing.T, h *home.Home) *store.Store {
	t.Helper()
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return records
}

// startRunErr starts a run and returns whatever happened, for a test whose
// subject is whether it was refused.
func startRunErr(t *testing.T, client *ipc.Client, workingPath string) (machine.Run, error) {
	t.Helper()
	var run machine.Run
	err := client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working: machine.Working{WorkingPath: workingPath},
	}, &run)
	return run, err
}

// homeIsFree asserts that no service holds this home, which is what makes a
// restart a restart.
//
// It takes the lock and gives it straight back. Service.Close releases that
// lock last, after it has closed the database, so a home this can lock is a
// home whose previous service ran Close to the end: the lock being free is
// evidence about the database as well as about the lock.
func homeIsFree(t *testing.T, h *home.Home) {
	t.Helper()
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("the home is still held, so the previous service did not close: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the home lock: %v", err)
	}
}
