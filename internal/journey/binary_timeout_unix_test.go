//go:build darwin || linux

package journey

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// TestTheBuildWrapperRunsTheCommandWhileThePipeIsOpen is the control for
// the watcher's descriptor. A background command whose standard input was
// left alone would read /dev/null, see end of file, and kill the group
// before the command ran.
func TestTheBuildWrapperRunsTheCommandWhileThePipeIsOpen(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	cmd := procgroup.Command("/bin/sh", "-c", buildScript, "_", "/bin/echo", "ok")
	cmd.Stdin = r
	var out bytes.Buffer
	cmd.Stdout = &out
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := procgroup.Run(ctx, cmd, time.Second); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ok\n" {
		t.Fatalf("command output %q", out.String())
	}
}

// TestTheBuildGroupEndsWhenThePipeCloses is the mechanism the testing alarm
// relies on: the parent does not have to run a cancellation. Closing the
// write end is what this process exiting does, and the group ends with it.
func TestTheBuildGroupEndsWhenThePipeCloses(t *testing.T) {
	principles.Cite(t, principles.P15)
	pidFile := filepath.Join(t.TempDir(), "pid")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	cmd := procgroup.Command("/bin/sh", "-c", buildScript, "_",
		"/bin/sh", "-c", `echo $$ > "$1"; exec /bin/sleep 300`, "_", pidFile)
	cmd.Stdin = r
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- procgroup.Run(ctx, cmd, time.Second) }()
	t.Cleanup(func() {
		cancel()
		if pid, err := readPID(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	pid, err := waitForPID(pidFile, 2*time.Second)
	if err != nil {
		t.Fatalf("the build never started: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still alive after the pipe closed", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the group leader exited without an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the build's Run did not return after the pipe closed")
	}
}

// TestTheHarnessCompileDoesNotOutliveTheProcess is the compile the alarm
// harness starts. An unbounded exec.Command of go survives the testing
// alarm: with the package's owner watch running, a hanging go that becomes
// /bin/sleep was still alive under PID 1 twelve seconds later, because the
// sweep cannot read that program's environment. RunHeld is what the harness
// uses, and this process exiting closes its pipe. P15: when the owner ends,
// the process it started ends with it.
func TestTheHarnessCompileDoesNotOutliveTheProcess(t *testing.T) {
	principles.Cite(t, principles.P15)
	root, err := ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "compilehold.test")
	if out, err := RunHeld(root, nil, "go", "test", "-c", "-o", bin, "./internal/journey/compilehold"); err != nil {
		t.Fatalf("compiling the owner-watch harness: %v\n%s", err, out)
	}
	hangDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := "#!/bin/sh\necho $$ > \"$HANG_PID\"\nexec /bin/sleep 300\n"
	if err := os.WriteFile(filepath.Join(hangDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out, runErr := RunHeld(root, compileHoldEnv(hangDir, pidFile, root), bin, "-test.timeout=1s", "-test.run=^TestCompile$", "-test.count=1", "-test.v")
	t.Cleanup(func() {
		if pid, err := readPID(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	pid, err := readPID(pidFile)
	if err != nil {
		t.Fatalf("the hanging compile never started (harness: %v)\n%s", runErr, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("compile pid %d still alive after the harness exited (parent %d); harness: %v\n%s", pid, parentOf(pid), runErr, out)
}

func compileHoldEnv(hangDir, pidFile, root string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "GOMAXPROCS=") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"GOMAXPROCS=1",
		"PATH="+hangDir,
		"HANG_PID="+pidFile,
		"JOURNEY_COMPILE_HOLD=1",
		"ROOT="+root,
		"OUT="+filepath.Join(hangDir, "unused.test"),
	)
}

// TestATimedOutBuildDoesNotOutliveTheHarness is the review's reproduction:
// journey.Binary under a real testing alarm, GOMAXPROCS=1, a one-second
// timeout, and a go on PATH that never exits. The alarm can exit that
// process before procgroup.Run handles cancellation. The build must not
// stay alive, reparented, after the process is gone. P15: when the owner
// ends, the process it started ends with it.
func TestATimedOutBuildDoesNotOutliveTheHarness(t *testing.T) {
	principles.Cite(t, principles.P15)
	root, err := ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "hang.test")
	if out, err := RunHeld(root, nil, "go", "test", "-c", "-o", helper, "./internal/journey/hangbuild"); err != nil {
		t.Fatalf("compiling the alarm harness: %v\n%s", err, out)
	}
	hangDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := "#!/bin/sh\necho $$ > \"$JOURNEY_HANG_PID\"\nexec /bin/sleep 300\n"
	if err := os.WriteFile(filepath.Join(hangDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out, runErr := RunHeld(root, hangEnv(hangDir, pidFile), helper, "-test.timeout=1s", "-test.run=^TestHang$", "-test.count=1", "-test.v")
	t.Cleanup(func() {
		if pid, err := readPID(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	pid, err := readPID(pidFile)
	if err != nil {
		t.Fatalf("the hanging build never started (harness: %v)\n%s", runErr, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	ppid := parentOf(pid)
	t.Fatalf("build pid %d still alive after the harness exited (parent %d); harness: %v\n%s", pid, ppid, runErr, out)
}

func hangEnv(hangDir, pidFile string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "ASSISTANT_BINARY=") || strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "GOMAXPROCS=") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"GOMAXPROCS=1",
		"PATH="+hangDir,
		"JOURNEY_HANG_BUILD=1",
		"JOURNEY_HANG_PATH="+hangDir,
		"JOURNEY_HANG_PID="+pidFile,
	)
}

func waitForPID(path string, d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for {
		pid, err := readPID(path)
		if err == nil {
			return pid, nil
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readPID(path string) (int, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(body)))
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func parentOf(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return n
}
