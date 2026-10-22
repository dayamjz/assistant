//go:build linux || darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

// A process a test has a shell hold until it is released is
// standin.HeldWait's held process, never a shell loop (#432): held_unix.go
// in internal/agents/standin owns why and what it does not cover. heldShell
// adds the cleanup that ends one a test started, so a failing test ends it
// rather than leaving it to the owner watch.

// heldShell has worker run a shell that writes its own identifier to self
// in dir and then becomes the held process, and returns the identifier and
// identity. Ending it is registered before the shell is asked for, so a
// test that fails at any point after still ends it (endHeld).
func heldShell(t *testing.T, worker agents.Worker, dir string) (int, agents.Identity) {
	t.Helper()
	var held agents.Identity
	t.Cleanup(func() { endHeld(t, dir, held) })
	pid, id := startHeld(t, worker, dir)
	held = id
	return pid, id
}

// startHeld is heldShell without the cleanup, which a test that leaks the
// process on purpose calls.
func startHeld(t *testing.T, worker agents.Worker, dir string) (int, agents.Identity) {
	t.Helper()
	if err := worker.Send(t.Context(), "run: echo $$ > self.tmp && mv self.tmp self; exec "+standin.HeldWait(t, "release")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	pid := atoiFile(t, filepath.Join(dir, "self"))
	id, found, err := agents.Identify(pid)
	if err != nil || !found {
		t.Fatalf("the shell's identity: %+v, %v, %v", id, found, err)
	}
	return pid, id
}

// endHeld kills the process group the held process leads, the shell's own,
// and waits for the process to go, failing t if it does not. With no
// identity recorded it reads the one self in dir names, if any, so a test
// that failed between the shell's start and its identity still ends it.
func endHeld(t *testing.T, dir string, held agents.Identity) {
	t.Helper()
	if held.PID == 0 {
		raw, err := os.ReadFile(filepath.Join(dir, "self"))
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || perr != nil {
			return
		}
		id, found, err := agents.Identify(pid)
		if err != nil || !found {
			return
		}
		held = id
	}
	if presence, err := held.Presence(); err != nil || presence != agents.PresencePresent {
		return
	}
	if group, err := syscall.Getpgid(held.PID); err == nil && group == held.PID {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = syscall.Kill(held.PID, syscall.SIGKILL)
	}
	if !(standin.Process{Identity: held}).GoneWithin(10 * time.Second) {
		t.Errorf("held process %d was still running 10s after its group was killed", held.PID)
	}
}

// releasedHeld is a held process a test's shell runs from a command line
// heldReleased built and that the test's cleanup releases rather than
// kills: the release file it waits on, the record its identity is written
// to, and the identity read from that record (holding).
type releasedHeld struct {
	release, record string
	id              agents.Identity
}

// heldReleased returns the held process for a shell in dir to run, waiting
// on a release file there and recording its identity there first, and
// registers the cleanup that releases it. The cleanup writes the release
// and then waits for the process to exit, so it has read its release
// before anything registered earlier, the removal of dir among them, runs.
// Before #467 the release was written and dir removed straight after, and a
// held process that had not yet read the file found it gone and waited on
// its owner, the whole test binary, past the package's check.
//
// The test calls holding once its shell has been asked to run the command:
// it waits for the record, so the test does not go on, and end, before the
// held process is running. A cleanup that finds no identity read fails t
// unless t has already failed, since without one the release cannot be
// ordered before the removal.
func heldReleased(t *testing.T, dir string) (string, *releasedHeld) {
	t.Helper()
	h := &releasedHeld{release: filepath.Join(dir, "release"), record: filepath.Join(dir, "held")}
	t.Cleanup(func() { h.end(t) })
	return standin.HeldWaitIdentified(t, h.release, h.record), h
}

// holding waits for the held process to record its identity and returns
// it, failing t if no whole record appears.
func (h *releasedHeld) holding(t *testing.T) agents.Identity {
	t.Helper()
	waitFile(t, h.record)
	id, err := standin.ReadHeldIdentity(h.record)
	if err != nil {
		t.Fatalf("the held process's identity: %v", err)
	}
	h.id = id
	return id
}

// end is heldReleased's cleanup: it releases the held process and waits
// for it to exit, killing it and failing t if it has not within
// standin.EndGrace.
func (h *releasedHeld) end(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.release, nil, 0o600); err != nil {
		t.Errorf("releasing the held process: %v", err)
	}
	if h.id.PID == 0 {
		if !t.Failed() {
			t.Errorf("the held process's identity was never read (holding), so its release could not be ordered before the removal of %s", filepath.Dir(h.release))
		}
		return
	}
	p := standin.Process{Identity: h.id}
	if !p.GoneWithin(standin.EndGrace) {
		p.End()
		t.Errorf("held process %d was still running %v after its release", h.id.PID, standin.EndGrace)
	}
}

// Variables the outer tests below start this binary's tests with, to run
// TestHeldShellChild in the mode named and record the held process's
// identifier in the file named.
const (
	heldModeVar   = "ASSISTANT_TEST_HELD_MODE"
	heldRecordVar = "ASSISTANT_TEST_HELD_RECORD"
)

// TestHeldShellChild is the test the outer tests run as a test binary of
// its own, and is skipped otherwise. It starts a worker whose shell holds a
// process as TestASurvivorIsPlacedAlongTheChainUntilItIsForgotten's does,
// records its identity, and then, by mode: blocks until it is killed
// ("block"), fails ("fail"), or returns having started it without the
// cleanup that ends it ("leak").
func TestHeldShellChild(t *testing.T) {
	mode, record := os.Getenv(heldModeVar), os.Getenv(heldRecordVar)
	if mode == "" {
		t.Skip("run by the held-process tests as a test binary of its own")
	}
	worker, dir := startedWorker(t)
	var id agents.Identity
	if mode == "leak" {
		_, id = startHeld(t, worker, dir)
	} else {
		_, id = heldShell(t, worker, dir)
	}
	line := strconv.Itoa(id.PID) + " " + strconv.FormatUint(id.Start, 10) + "\n"
	if err := os.WriteFile(record+".tmp", []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(record+".tmp", record); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "block":
		<-make(chan struct{})
	case "fail":
		t.Fatal("failing on purpose with the held process running")
	}
}

// lockedOutput is a child's output, written by the goroutines exec copies
// it with and read by the test while the child runs.
type lockedOutput struct {
	mu sync.Mutex
	b  strings.Builder
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

// heldChildRun is a child test binary heldChild started: the command, closed
// exited once it has been waited for, with its error then in err, its
// output, and the identity of the held process it recorded.
type heldChildRun struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
	out    *lockedOutput
	held   agents.Identity
}

// heldChild runs TestHeldShellChild in mode as a test binary of its own,
// with extra arguments, and returns once the child has recorded its held
// process. The child is a copy of this binary, so the check its RunOwned
// runs lists the processes of the copy alone, never those of this run's
// other tests, which it would otherwise report and end. It shares this
// process's group, the foreground of the terminal AtTerminal gave it, so
// it runs its tests itself rather than running itself again, and it is its
// own owner (RunOwned). The child and the held process are killed in
// cleanup if they are still running.
//
// The child's TMPDIR is a directory inside this test's own, so this test's
// removal of it takes whatever temporary entries the child made. A child
// that is killed or times out runs none of its cleanup, and before #454 its
// TestMain's gitconfig directory and its test's TempDir outlived the whole
// run. The removal is registered before the cleanup that kills and waits for
// the child, so it runs after the child has exited.
func heldChild(t *testing.T, mode string, extra ...string) *heldChildRun {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	childTemp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(childTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "child.test")
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "held")
	r := &heldChildRun{exited: make(chan struct{}), out: &lockedOutput{}}
	r.cmd, err = standin.StartCopied(func() *exec.Cmd {
		cmd := exec.Command(binary, append([]string{"-test.run=^TestHeldShellChild$", "-test.count=1"}, extra...)...)
		cmd.Env = append(os.Environ(), heldModeVar+"="+mode, heldRecordVar+"="+record, "TMPDIR="+childTemp)
		cmd.Stdout, cmd.Stderr = r.out, r.out
		return cmd
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { r.err = r.cmd.Wait(); close(r.exited) }()
	t.Cleanup(func() {
		_ = r.cmd.Process.Kill()
		<-r.exited
		if r.held.PID != 0 && !(standin.Process{Identity: r.held}).GoneWithin(0) {
			_ = syscall.Kill(r.held.PID, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		// The child reads the identity while the process runs, since a
		// failing child may have ended it before this read could.
		if raw, err := os.ReadFile(record); err == nil {
			if _, err := fmt.Sscanf(string(raw), "%d %d", &r.held.PID, &r.held.Start); err != nil || r.held.Start == 0 {
				t.Fatalf("the child recorded %q: %v", raw, err)
			}
			return r
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the child never recorded its held process: %s", r.out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wait waits up to within for the child to exit, failing t if it does not.
func (r *heldChildRun) wait(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case <-r.exited:
		return r.err
	case <-time.After(within):
		t.Fatalf("the child test binary never exited: %s", r.out)
		return nil
	}
}

// A held process ends with its test on every path a test can end by
// (P15): the test binary killed outright, the test binary timing out, which
// runs no cleanup, and the test failing, whose cleanup must have ended it
// before the binary exits. Each is checked by the held process's identity.
// Before #432 the process was a shell loop waiting on a release file, which
// survived the first two with parent 1; it survived a failure too when the
// failure came before its release was registered, which heldShell rules
// out by registering the end before the shell is started.
func TestAHeldProcessEndsWithItsTestOnEveryPath(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	gone := standin.OwnerPoll + standin.OwnerGrace + 10*time.Second
	for _, c := range []struct {
		name, mode string
		extra      []string
		kill       bool
		within     time.Duration
	}{
		{name: "killed", mode: "block", kill: true, within: gone},
		{name: "timed out", mode: "block", extra: []string{"-test.timeout=20s"}, within: gone},
		{name: "failed", mode: "fail", within: 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := heldChild(t, c.mode, c.extra...)
			if c.kill {
				_ = r.cmd.Process.Kill()
			}
			_ = r.wait(t, 60*time.Second)
			if !(standin.Process{Identity: r.held}).GoneWithin(c.within) {
				t.Fatalf("held process %d was still running %v after its test binary exited: %s", r.held.PID, c.within, r.out)
			}
		})
	}
}

// The check RunOwned runs once the tests return reports a held process a
// test leaked, names it, ends it, and fails the package (P15). Before #432
// the process was a shell loop, which the check could not see on macOS,
// and the package passed with it running.
func TestTheOwnerCheckReportsAHeldProcessATestLeaked(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	r := heldChild(t, "leak")
	id, out := r.held, r.out
	if err := r.wait(t, standin.EndGrace+60*time.Second); err == nil {
		t.Fatalf("the child passed with its held process %d leaked: %s", id.PID, out)
	}
	if want := "process " + strconv.Itoa(id.PID) + " ("; !strings.Contains(out.String(), want) {
		t.Fatalf("the child's check did not name held process %d: %s", id.PID, out)
	}
	if !(standin.Process{Identity: id}).GoneWithin(5 * time.Second) {
		t.Fatalf("held process %d was still running after the check that named it: %s", id.PID, out)
	}
}
