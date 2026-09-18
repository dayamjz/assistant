package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestMain lets this binary act as the stand-in agent, which the service a
// test opens resolves instead of whatever agent is installed on the machine
// running the tests, and as the command a gate's hooks invoke.
func TestMain(m *testing.M) {
	standin.Main()
	hookMain()
	os.Exit(m.Run())
}

// hookMain lets this binary act as the command a gate's hooks invoke.
//
// internal/gate writes the absolute path of the binary that initialized the
// gate into its hooks, and in a test that binary is this one. A push therefore
// reaches this process, and this hands the arguments and the streams straight
// to cli.Run, which is the product: cmd/assistant is the process boundary and
// holds no behaviour, so what a push exercises here is the same command
// surface every other test in this file drives, reached the way a push
// reaches it.
//
// It is the arrangement internal/agents/standin already uses for the same
// reason, and it recognizes the invocation the same way: by what the command
// line says. A test binary is never run with "gate" as its first argument by
// the testing package.
func hookMain() {
	if len(os.Args) < 2 || os.Args[1] != "gate" {
		return
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "the gate hook stand-in cannot resolve the working directory:", err)
		os.Exit(1)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "the gate hook stand-in cannot resolve its own path:", err)
		os.Exit(1)
	}
	os.Exit(int(cli.Run(context.Background(), cli.Environment{
		Args:       os.Args[1:],
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
		Getenv:     os.Getenv,
		WorkingDir: workingDir,
		Executable: executable,
		Version:    "assistant (test)",
	})))
}

// platformIdentifiesPeers reports whether internal/ipc has a read for local
// socket peer credentials here, written down on the same terms that package's
// own tests write it down.
func platformIdentifiesPeers() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// requiresIdentifiedPeer skips a test that drives a run through the surface.
func requiresIdentifiedPeer(t *testing.T) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s reports no local socket peer credentials, so every command that drives a run is refused", runtime.GOOS)
	}
}

// invocation is one run of the command surface: what it wrote and what it
// exited with.
type invocation struct {
	code   machine.Code
	stdout string
	stderr string
}

// run drives the whole command surface, which is what the binary does. The
// streams and the working directory are the test's, so what is exercised is
// the product rather than a rearrangement of it.
func run(t *testing.T, h *home.Home, workingDir string, args ...string) invocation {
	t.Helper()
	return runArgs(t, workingDir, append([]string{"--home", h.Root()}, args...)...)
}

// runArgs drives the surface with the command line exactly as given, for a
// test whose subject is where on that line an argument may appear.
func runArgs(t *testing.T, workingDir string, args ...string) invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	return runArgsIn(t.Context(), executable, workingDir, args...)
}

// runArgsIn drives the surface under a context the caller owns, for the tests
// whose subject is a caller that gives up on a call it is blocked in. It takes
// nothing from testing.T, so it is safe to call from a goroutine.
func runArgsIn(ctx context.Context, executable, workingDir string, args ...string) invocation {
	var out, errs bytes.Buffer
	code := cli.Run(ctx, cli.Environment{
		Args:       args,
		Stdout:     &out,
		Stderr:     &errs,
		Getenv:     func(string) string { return "" },
		WorkingDir: workingDir,
		Executable: executable,
		Version:    "assistant (test)",
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// newHome returns a home root short enough to hold a local socket path.
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

// git runs a git command and fails the test when it fails, for building the
// working copy a command surface is exercised in.
//
// What git reads is gitCommand's and not this helper's, so a bounded call and
// an unbounded one cannot differ in it. Failing the test is the whole of what
// this adds.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCommand(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(out)
}

// newSubject returns a working copy with one commit and an origin, which is
// what assistant init reads the upstream and the default branch from. It
// stands on the default branch; a test that starts a run from the checkout
// stands on a change first with standingOnChange, because a run of the
// default branch itself carries nothing and ends at the rebase stage's
// empty-diff short circuit.
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
	// The upstream is a bare repository beside the subject rather than a URL
	// nobody can resolve. A run walks the rebase stage, and that body reads
	// what the upstream holds: against an unreachable one every run these
	// tests drive would be watching a network failure rather than the command
	// surface they are about. It is local so that nothing here reaches the
	// network.
	upstream, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatalf("making an upstream repository: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(upstream) })
	git(t, upstream, "init", "--quiet", "--bare", "-b", "main", ".")
	git(t, dir, "remote", "add", "origin", upstream)
	git(t, dir, "push", "--quiet", "origin", "main")
	// What a clone records about origin's own HEAD, written here because this
	// subject was made in place rather than cloned: it is what assistant init
	// reads the default branch from, and without it init would fall back to
	// whatever branch the checkout happens to stand on.
	git(t, dir, "remote", "set-head", "origin", "main")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}
	return resolved
}

// standingOnChange puts the working copy on a new branch with one commit of
// its own, which is what a run started from the checkout is then of. A run of
// the default branch itself carries no change, and the rebase stage ends such
// a run at its empty-diff short circuit, so a test that means to watch a run
// stop at a hold stands on a change first.
//
// The commit's file is named for the branch, so a test that stands on several
// changes in turn puts a distinct change on each rather than an empty commit
// on every branch after the first.
func standingOnChange(t *testing.T, subject, branch string) {
	t.Helper()
	git(t, subject, "checkout", "--quiet", "-b", branch)
	if err := os.WriteFile(filepath.Join(subject, branch+".txt"), []byte("a change on "+branch+"\n"), 0o600); err != nil {
		t.Fatalf("writing the change under validation: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "the change under validation on "+branch)
}

// serve opens a service on a home with the stages the product wires and serves
// it for the length of the test. The command surface launches one in a process
// of its own; a test opens it here so that the agent it resolves is the
// scripted stand-in rather than whatever is installed.
func serve(t *testing.T, h *home.Home) {
	t.Helper()
	serveStages(t, h, stages.All)
}

// serveStages is serve for a test that varies which stages have bodies, since
// that is what moves where a run first stops and what a body does when it is
// reached.
func serveStages(t *testing.T, h *home.Home, served func(stages.StageDeps) pipeline.Stages) {
	t.Helper()
	serveCatalog(t, h, served, agents.NewCatalog(fixedFactory{runner: reviewingRunner(t)}))
}

// serveUntilStopped is serve for a test whose subject is what happens with the
// service down after it has been up. The returned function stops it, is safe
// to call more than once, and runs at the end of the test whether or not the
// test called it.
func serveUntilStopped(t *testing.T, h *home.Home) func() {
	t.Helper()
	return serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: reviewingRunner(t)}))
}

// reviewingRunner is the stand-in these tests serve as the resolved agent. It
// is scripted to answer every review invocation with a clean review of
// whatever change the invocation carries, because the review stage launches
// one for any run that reaches it and a run these tests drive should stop at
// the stages that hold, not at a reviewer that never answered. Everything
// else stays unscripted, so a run that reaches an agent a test did not mean
// it to reach fails loudly.
func reviewingRunner(t *testing.T) agents.Runner {
	t.Helper()
	return standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("the change reads cleanly"),
	}}}).Runner()
}

// serveWithNoRunnableAgent serves a home whose one configured adapter refuses
// to build, which is what agents.Resolve meets on a machine with nothing it
// can run. Every other helper here serves a home that resolves.
func serveWithNoRunnableAgent(t *testing.T, h *home.Home) func() {
	t.Helper()
	return serveCatalog(t, h, stages.All, agents.NewCatalog(unrunnableFactory{}))
}

// serveCatalog is the one owner of how these tests open and serve a service,
// so the only things a caller varies are the stages it serves and which agents
// it may resolve.
func serveCatalog(t *testing.T, h *home.Home, served func(stages.StageDeps) pipeline.Stages, catalog *agents.Catalog) func() {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	running, err := service.Open(t.Context(), service.Options{
		Home:      h,
		NewStages: served,
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   catalog,
	})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	serving := make(chan error, 1)
	go func() { serving <- running.Serve(context.Background()) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if err := running.Close(); err != nil {
				t.Errorf("closing the service: %v", err)
			}
			if err := <-serving; err != nil {
				t.Errorf("serving: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// fixedFactory hands back a Runner somebody else built, so nothing here can
// produce an adapter the production path could not.
type fixedFactory struct{ runner agents.Runner }

func (f fixedFactory) Name() string { return f.runner.Name() }

func (f fixedFactory) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }

// unrunnableFactory is an adapter this build has and this machine cannot run,
// which is the shape agents.Resolve reports as no configured agent being
// runnable. It refuses at New rather than being absent from the catalog, so
// the refusal carries a reason an operator can act on.
type unrunnableFactory struct{}

func (unrunnableFactory) Name() string { return "unrunnable" }

func (unrunnableFactory) New(context.Context, []string) (agents.Runner, error) {
	return nil, errors.New("this adapter is not installed on this machine")
}

// openStore opens a home's database directly, for a test that has to put a
// record where only the service would otherwise write one.
func openStore(t *testing.T, h *home.Home) *store.Store {
	t.Helper()
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	return records
}

// newSubjectWithoutOrigin returns a working copy with no origin, which is the
// state an init cannot complete from unless the caller names the upstream.
func newSubjectWithoutOrigin(t *testing.T) string {
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
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}
	return resolved
}
