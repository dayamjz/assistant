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
// what assistant init reads the upstream and the default branch from.
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
	git(t, dir, "remote", "add", "origin", "https://example.invalid/o/r.git")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving the subject path: %v", err)
	}
	return resolved
}

// serve opens a service on a home and serves it for the length of the test.
// The command surface launches one in a process of its own; a test opens it
// here so that the agent it resolves is the scripted stand-in rather than
// whatever is installed.
func serve(t *testing.T, h *home.Home) {
	t.Helper()
	serveUntilStopped(t, h)
}

// serveUntilStopped is serve for a test whose subject is what happens with the
// service down after it has been up. The returned function stops it, is safe
// to call more than once, and runs at the end of the test whether or not the
// test called it.
func serveUntilStopped(t *testing.T, h *home.Home) func() {
	t.Helper()
	runner := standin.New(t, standin.Script{}).Runner()
	return serveCatalog(t, h, agents.NewCatalog(fixedFactory{runner: runner}))
}

// serveWithNoRunnableAgent serves a home whose one configured adapter refuses
// to build, which is what agents.Resolve meets on a machine with nothing it
// can run. Every other helper here serves a home that resolves.
func serveWithNoRunnableAgent(t *testing.T, h *home.Home) func() {
	t.Helper()
	return serveCatalog(t, h, agents.NewCatalog(unrunnableFactory{}))
}

// serveCatalog is the one owner of how these tests open and serve a service,
// so the only thing a caller varies is which agents it may resolve.
func serveCatalog(t *testing.T, h *home.Home, catalog *agents.Catalog) func() {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	running, err := service.Open(t.Context(), service.Options{
		Home:     h,
		Stages:   stages.All(),
		NewFixer: stages.PendingFixer,
		Build:    build,
		Catalog:  catalog,
	})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if err := running.Close(); err != nil {
				t.Errorf("closing the service: %v", err)
			}
			if err := <-served; err != nil {
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
