//go:build darwin || linux

package journey

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
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
// sweep cannot read that program's environment. The helper is not started
// under RunHeld. That wrapper would own the compiler's process group and
// kill it when the helper's alarm exits, so a compiler that leaked would
// still look dead. P15: when the owner ends, the process it started ends
// with it.
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
	if err := procgroup.WriteProgram(filepath.Join(hangDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.timeout=1s", "-test.run=^TestCompile$", "-test.count=1", "-test.v")
	cmd.Dir = root
	cmd.Env = compileHoldEnv(hangDir, pidFile, root)
	out, runErr := cmd.CombinedOutput()
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
// journey.Binary in a test process, GOMAXPROCS=1, with a go on PATH that
// never exits, and the process then ended the way the testing alarm ends
// one: the runtime exits it with status 2 and runs no deferred call, so
// procgroup.Run never handles a cancellation. The build must not stay
// alive, reparented, after the process is gone. P15: when the owner ends,
// the process it started ends with it.
//
// The process is ended with SIGQUIT, which the Go runtime answers the same
// way, and only once the build has written its pid. A real alarm with a
// fixed timeout raced the harness's own setup: under load it fired before
// the build started, and the test then checked nothing.
func TestATimedOutBuildDoesNotOutliveTheHarness(t *testing.T) {
	principles.Cite(t, principles.P15)
	root, err := ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "hang.test")
	if out, err := RunHeld(root, nil, "go", "test", "-c", "-o", helper, "./internal/journey/hangbuild"); err != nil {
		t.Fatalf("compiling the harness: %v\n%s", err, out)
	}
	hangDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := "#!/bin/sh\necho $$ > \"$JOURNEY_HANG_PID\"\nexec /bin/sleep 300\n"
	if err := procgroup.WriteProgram(filepath.Join(hangDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The helper is not started under RunHeld. That wrapper would own the
	// build's process group and kill it when the helper exits, so a build
	// that leaked would still look dead.
	cmd := exec.Command(helper, "-test.timeout=10m", "-test.run=^TestHang$", "-test.count=1", "-test.v")
	cmd.Dir = root
	cmd.Env = hangEnv(hangDir, pidFile)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helperPID := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if pid, err := readPID(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		// The helper is ended inside Binary, after TempDir and before
		// Cleanup. The build directory is in the helper's namespace, and
		// the helper is gone, so this process removes it.
		if err := standin.ReclaimProcess(helperPID); err != nil {
			t.Error(err)
		}
	})
	pid, err := pidOnceStarted(pidFile, exited, time.Minute)
	if err != nil {
		_ = syscall.Kill(helperPID, syscall.SIGKILL)
		<-exited
		t.Fatalf("the hanging build never started: %v\n%s", err, out.String())
	}
	if err := syscall.Kill(helperPID, syscall.SIGQUIT); err != nil {
		t.Fatalf("ending the harness: %v", err)
	}
	runErr := <-exited
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("the harness ended with %v, want the runtime's exit status 2\n%s", runErr, out.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	ppid := parentOf(pid)
	t.Fatalf("build pid %d still alive after the harness exited (parent %d); harness: %v\n%s", pid, ppid, runErr, out.String())
}

// pidOnceStarted waits for the pid the hanging build writes. A harness that
// exits first, or a build that has not started within limit, is an error,
// so a test cannot go on to check a build that never ran.
func pidOnceStarted(path string, exited <-chan error, limit time.Duration) (int, error) {
	deadline := time.Now().Add(limit)
	for {
		if pid, err := readPID(path); err == nil {
			return pid, nil
		}
		select {
		case err := <-exited:
			if err == nil {
				return 0, errors.New("the harness exited first, with status 0")
			}
			return 0, fmt.Errorf("the harness exited first: %w", err)
		default:
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("no pid within %s", limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
