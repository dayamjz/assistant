package standin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

const tempDirChildVar = "STANDIN_TEST_TEMP_DIR_CHILD"

func tempDirChildMain() {
	switch os.Getenv(tempDirChildVar) {
	case "create":
		path, err := TempDir("killed-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(path)
		for {
			time.Sleep(time.Hour)
		}
	case "sweep":
		if err := SweepTempDirs(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	case "unregistered":
		registeredTempTag = ""
		_, err := TempDir("ipc")
		if err == nil {
			fmt.Fprintln(os.Stderr, "TempDir stamped a directory with no namespace")
			os.Exit(2)
		}
		fmt.Println(err)
		os.Exit(0)
	case "again":
		before := registeredTempTag
		if err := RegisterTempTag(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if registeredTempTag != before {
			fmt.Fprintf(os.Stderr, "registering again recorded %s, was %s\n", registeredTempTag, before)
			os.Exit(2)
		}
		fmt.Println("same")
		os.Exit(0)
	case "foreign":
		registeredTempTag = "internal_supervisor_feed"
		if err := RegisterTempTag(); err == nil {
			fmt.Fprintln(os.Stderr, "RegisterTempTag replaced the recorded namespace")
			os.Exit(2)
		}
		if registeredTempTag != "internal_supervisor_feed" {
			fmt.Fprintf(os.Stderr, "recorded namespace changed to %s\n", registeredTempTag)
			os.Exit(2)
		}
		fmt.Println("kept")
		os.Exit(0)
	}
}

// P15: a killed test binary cannot run its cleanup. The next package start
// reclaims its directory, while a concurrently running owner's directory and
// an unrecognized name remain. The child reaches the helper through TestMain.
func TestAKilledTestBinarysTempDirIsReclaimedAtTheNextStart(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self)
	child.Env = append(os.Environ(), tempDirChildVar+"=create")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	path, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the child's directory: %v", err)
	}
	path = strings.TrimSpace(path)
	defer func() { _ = os.RemoveAll(path) }()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("child directory before its owner is killed: %v", err)
	}
	tag, err := RegisteredTempTag()
	if err != nil {
		t.Fatal(err)
	}
	bad, err := unparseableTempDir(tag)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(bad) }()
	sweep := func() {
		t.Helper()
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(), tempDirChildVar+"=sweep")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("next package start: %v\n%s", err, out)
		}
	}
	sweep()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a live owner's directory was removed: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the killed binary did not leave its directory: %v", err)
	}
	sweep()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the next package start left %s: %v", filepath.Base(path), err)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("the unrecognized name was removed: %v", err)
	}
}

// P15: TempDir has no namespace argument. With nothing registered it returns
// an error and makes no directory, so a caller cannot substitute another
// package's namespace the way a fallback letter used to.
func TestTempDirRefusesWhenNothingIsRegistered(t *testing.T) {
	principles.Cite(t, principles.P15)
	out := runTempDirChild(t, "unregistered")
	if !strings.Contains(out, "no test temp namespace") {
		t.Fatalf("TempDir with nothing registered: %s", out)
	}
}

// P15: a helper that is handed another package's letter cannot pass it to
// TempDir. The directory is this process's namespace.
func TestALetterArgumentCannotSelectTheNamespace(t *testing.T) {
	principles.Cite(t, principles.P15)
	dir, err := tempDirTakingALetter("p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	tag, err := RegisteredTempTag()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, tempPrefix(tag)) {
		t.Fatalf("directory %s is not in namespace %s", base, tag)
	}
	if strings.HasPrefix(base, "atp-") {
		t.Fatalf("directory %s used the letter handed to the helper", base)
	}
}

// tempDirTakingALetter is a helper whose parameter used to be the namespace.
// TempDir takes only the pattern, so the parameter cannot select one.
func tempDirTakingALetter(tag string) (string, error) {
	if tag == "" {
		return "", fmt.Errorf("standin: empty letter")
	}
	return TempDir("h")
}

// P15: the namespace is the caller's directory. Two directories differ, two
// files in one directory do not, and a library file's directory is not the
// directory of a test that reaches it through another package.
func TestEachDirectoryHasItsOwnNamespace(t *testing.T) {
	principles.Cite(t, principles.P15)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ipc, err := namespaceFromFile(filepath.Join(root, "internal", "ipc", "main_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := namespaceFromFile(filepath.Join(root, "internal", "ipc", "peer_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := namespaceFromFile(filepath.Join(root, "internal", "supervisor", "feed", "helpers_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	library, err := namespaceFromFile(filepath.Join(root, "internal", "store", "storetest", "storetest.go"))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := namespaceFromFile(filepath.Join(root, "internal", "cli", "helpers_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if ipc != peer {
		t.Fatalf("two files in ipc are %s and %s", ipc, peer)
	}
	if ipc == feed {
		t.Fatalf("ipc and feed share namespace %s", ipc)
	}
	if library == caller {
		t.Fatalf("storetest and cli share namespace %s", library)
	}
	parent, err := namespaceFromFile(filepath.Join(root, "internal", "store", "helpers_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(tempPrefix(library), tempPrefix(parent)) {
		t.Fatalf("prefix %s matches the shorter directory %s", tempPrefix(library), parent)
	}
	slash, err := namespaceFromFile(filepath.Join(root, "review488probe", "a", "b", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	under, err := namespaceFromFile(filepath.Join(root, "review488probe", "a_b", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if slash == under {
		t.Fatalf("a/b and a_b share namespace %s", slash)
	}
	withUnder, err := namespaceFromFile(filepath.Join(root, "review488probe", "a", "_b", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	underSlash, err := namespaceFromFile(filepath.Join(root, "review488probe", "a_", "b", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if withUnder == underSlash {
		t.Fatalf("a/_b and a_/b share namespace %s", withUnder)
	}
}

// P15: RegisterTempTag recorded this directory, and calling it again from
// here does not change that. A different directory cannot replace it.
func TestTheRegisteredNamespaceIsThisDirectory(t *testing.T) {
	principles.Cite(t, principles.P15)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	want, err := namespaceFromFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RegisteredTempTag()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("registered namespace is %s, this directory is %s", got, want)
	}
	out := runTempDirChild(t, "again")
	if !strings.Contains(out, "same") {
		t.Fatalf("registering again from this directory: %s", out)
	}
	out = runTempDirChild(t, "foreign")
	if !strings.Contains(out, "kept") {
		t.Fatalf("a different directory replaced the namespace: %s", out)
	}
}

// The stamped name, for internal/supervisor/feed as namespaceFromFile encodes
// it, the longest pattern those calls pass, eight base36 digits of process
// identifier, the full width of a start time, and the longest suffix
// os.MkdirTemp appends, still leaves room for a socket under the bound
// terminal.go records as maxSocketPath.
func TestAStampedNameLeavesRoomForASocket(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := namespaceFromFile(filepath.Join(root, "internal", "supervisor", "feed", "main_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	name := tempPrefix(tag) + "zzzzzzzz-" +
		strconv.FormatUint(^uint64(0), 36) + "-storetemplate4294967295"
	path := filepath.Join(tempRoot(), name, "socket")
	const socketPathLimit = 103
	if len(path) > socketPathLimit {
		t.Fatalf("socket path is %d bytes, bound is %d: %s", len(path), socketPathLimit, path)
	}
}

func runTempDirChild(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), tempDirChildVar+"="+mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child %s: %v\n%s", mode, err, out)
	}
	return string(out)
}

// P15: a/b and a_b are different directories. A sweep of one must not reclaim
// a directory the other made, once that owner is gone, and must not reclaim
// it while the owner is still running.
func TestADirectoryDoesNotSweepItsSlashUnderscoreTwin(t *testing.T) {
	principles.Cite(t, principles.P15)
	mod := namespaceProbeModule(t)
	bins := t.TempDir()
	under := buildNamespaceProbe(t, mod, "a_b", filepath.Join(bins, "under.bin"), false)
	slash := buildNamespaceProbe(t, mod, "a/b", filepath.Join(bins, "slash.bin"), false)

	cmd := exec.Command(under, "create", "stay")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	path, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading a_b's directory: %v", err)
	}
	path = strings.TrimSpace(path)
	t.Cleanup(func() { _ = os.RemoveAll(path) })

	runNamespaceProbe(t, slash, "sweep")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a live owner's directory was removed: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	runNamespaceProbe(t, slash, "sweep")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a/b reclaimed a_b's directory %s: %v", filepath.Base(path), err)
	}
	runNamespaceProbe(t, under, "sweep")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a_b left its own directory %s: %v", filepath.Base(path), err)
	}
}

// P15: a binary built with -trimpath still registers, including when it is
// started from its package directory, and its directory is the same namespace
// a build without -trimpath sweeps.
func TestTrimpathRegistersWithoutTheSourceTree(t *testing.T) {
	principles.Cite(t, principles.P15)
	mod := namespaceProbeModule(t)
	bins := t.TempDir()
	plain := buildNamespaceProbe(t, mod, "a_b", filepath.Join(bins, "plain.bin"), false)
	trim := buildNamespaceProbe(t, mod, "a_b", filepath.Join(bins, "trim.bin"), true)

	cmd := exec.Command(trim, "create")
	cmd.Dir = filepath.Join(mod, "a_b")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trimpath from the package directory: %v\n%s", err, out)
	}
	path := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	runNamespaceProbe(t, plain, "sweep")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the untrimmed build left the trimpath directory %s: %v", filepath.Base(path), err)
	}
}

// namespaceProbeModule writes the probe, a module of its own named
// namespaceProbePath with the packages a_b and a/b, into a temporary
// directory, and returns that directory. It is never written into this
// checkout: another package loading ./... while these tests ran read it
// there and failed when it was removed under the load (#584), and a test
// does not write into the tree it tests. The probe imports this package
// through a Go workspace, go.work beside its go.mod, that also uses the
// module containing this test, which go test makes the working directory's.
func namespaceProbeModule(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := moduleRootFrom(wd)
	if err != nil {
		t.Fatal(err)
	}
	version, err := goDirectives(filepath.Join(checkout, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	// Resolved, because go compares the directory it is run in, with every
	// link followed, against the workspace's own.
	mod, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":  "module " + namespaceProbePath + "\n\n" + version,
		"go.work": version + "\nuse (\n\t.\n\t" + strconv.Quote(checkout) + "\n)\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeNamespaceProbe(t, mod)
	return mod
}

// namespaceProbePath is the probe module's path, which a -trimpath build
// records in place of the directory its sources are in. It is named under
// this repository's module path, though it is a module of its own outside
// the checkout, because Go lets only an import path under that one import
// this package, which is internal.
const namespaceProbePath = "github.com/dayamjz/assistant/review488probe"

// goDirectives is the go line of the go.mod at path, and its toolchain line
// when it has one, each ending in a newline, so the probe builds with the
// toolchain this module's own build selects.
func goDirectives(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var goLine, toolchain string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "go "):
			goLine = line + "\n"
		case strings.HasPrefix(line, "toolchain "):
			toolchain = line + "\n"
		}
	}
	if goLine == "" {
		return "", fmt.Errorf("%s has no go line", path)
	}
	return goLine + toolchain, nil
}

// writeNamespaceProbe writes the probe's two packages into the module at mod.
func writeNamespaceProbe(t *testing.T, mod string) {
	t.Helper()
	src := []byte(`package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

func main() {
	if err := standin.RegisterTempTag(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "create":
		dir, err := standin.TempDir("p")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(dir)
		if len(os.Args) == 3 && os.Args[2] == "stay" {
			time.Sleep(time.Hour)
		}
	case "sweep":
		if err := standin.SweepTempDirs(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
`)
	for _, rel := range []string{"a_b/main.go", "a/b/main.go"} {
		path := filepath.Join(mod, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// buildNamespaceProbe builds the probe's package pkg from the module at root
// into out, through that module's workspace. It passes -mod=readonly, the
// workspace default, because workspace mode refuses -mod=mod, and a flag on
// the command line overrides what GOFLAGS says, from the environment or the
// go env file, so the build does not depend on the caller's setting (#593).
func buildNamespaceProbe(t *testing.T, root, pkg, out string, trimpath bool) string {
	t.Helper()
	args := []string{"build", "-mod=readonly", "-o", out}
	if trimpath {
		args = append(args, "-trimpath")
	}
	args = append(args, "./"+pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOMAXPROCS=4", "GOWORK="+filepath.Join(root, "go.work"))
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, outb)
	}
	return out
}

func runNamespaceProbe(t *testing.T, bin, arg string) {
	t.Helper()
	cmd := exec.Command(bin, arg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(bin), arg, err, out)
	}
}

// P15: a reused PID with a different start time cannot keep the previous
// owner's directory alive.
func TestAReplacedOwnerIdentityIsReclaimed(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		t.Fatalf("identifying this process: %+v, %v, %v", self, found, err)
	}
	tag, err := RegisteredTempTag()
	if err != nil {
		t.Fatal(err)
	}
	path, err := replacedTempDir(tag, agents.Identity{PID: self.PID, Start: self.Start + 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(path) }()
	if err := SweepTempDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("replaced identity left %s: %v", path, err)
	}
}
