package standin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		path, err := TempDir(TempTagStandin, "killed-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(path)
		for {
			time.Sleep(time.Hour)
		}
	case "sweep":
		if err := SweepTempDirs(TempTagStandin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
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
	bad, err := os.MkdirTemp(tempRoot(), tempPrefix(TempTagStandin)+"unparseable-")
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

// P15: the tag list has one letter per package. A repeat would make two
// packages one namespace, so a start of either reclaims the other's
// directories.
func TestTempTagsAreOnePerPackage(t *testing.T) {
	principles.Cite(t, principles.P15)
	seen := map[string]struct{}{}
	for _, tag := range TempTags() {
		if err := validTempTag(tag); err != nil {
			t.Fatalf("tag %q: %v", tag, err)
		}
		if _, ok := seen[tag]; ok {
			t.Fatalf("tag %q is used by more than one package", tag)
		}
		seen[tag] = struct{}{}
	}
}

// RegisterTempTag refuses a tag SweepTempDirs would also refuse, and does so
// without replacing a tag already recorded.
func TestRegisterTempTagRefusesATagThatIsNotOneLetter(t *testing.T) {
	before, err := RegisteredTempTag()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"", "ab", "A", "s-"} {
		if err := RegisterTempTag(tag); err == nil {
			t.Fatalf("RegisterTempTag(%q) succeeded", tag)
		}
	}
	after, err := RegisteredTempTag()
	if err != nil || after != before {
		t.Fatalf("registered tag is %q, %v; want it still %q", after, err, before)
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
	pattern := tempPrefix(TempTagStandin) + strconv.FormatInt(int64(self.PID), 36) + "-" +
		strconv.FormatUint(self.Start+1, 36) + "-replaced-"
	path, err := os.MkdirTemp(tempRoot(), pattern)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(path) }()
	if err := SweepTempDirs(TempTagStandin); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("replaced identity left %s: %v", path, err)
	}
}
