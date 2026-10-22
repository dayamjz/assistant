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
	bad, err := os.MkdirTemp(tempRoot(), tempPrefix(tag)+"unparseable-")
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
	if strings.HasPrefix(tempPrefix(library), tempPrefix("internal_store")) {
		t.Fatalf("prefix %s matches the shorter directory", tempPrefix(library))
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

// The stamped name, with the longest directory that calls TempDir, the
// longest pattern those calls pass, eight base36 digits of process
// identifier, the full width of a start time, and the longest suffix
// os.MkdirTemp appends, still leaves room for a socket under the bound
// terminal.go records as maxSocketPath.
func TestAStampedNameLeavesRoomForASocket(t *testing.T) {
	name := tempPrefix("internal_supervisor_feed") + "zzzzzzzz-" +
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
	path, err := os.MkdirTemp(tempRoot(), tempName(tag, "replaced-", agents.Identity{PID: self.PID, Start: self.Start + 1}))
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
