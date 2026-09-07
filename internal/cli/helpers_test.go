package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
// running the tests.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
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
	var out, errs bytes.Buffer
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	code := cli.Run(t.Context(), cli.Environment{
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

// git runs a git command, for building the working copy a command surface is
// exercised in. It shells out on the same terms internal/vcs's and
// internal/gate's own tests do.
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
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	runner := standin.New(t, standin.Script{}).Runner()
	running, err := service.Open(t.Context(), service.Options{
		Home:     h,
		Stages:   stages.All(),
		NewFixer: stages.PendingFixer,
		Build:    build,
		Catalog:  agents.NewCatalog(fixedFactory{runner: runner}),
	})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("closing the service: %v", err)
		}
		if err := <-served; err != nil {
			t.Errorf("serving: %v", err)
		}
	})
}

// fixedFactory hands back a Runner somebody else built, so nothing here can
// produce an adapter the production path could not.
type fixedFactory struct{ runner agents.Runner }

func (f fixedFactory) Name() string { return f.runner.Name() }

func (f fixedFactory) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }

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
