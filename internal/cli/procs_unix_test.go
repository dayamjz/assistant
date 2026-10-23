//go:build linux || darwin

package cli_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// A service a test starts through the surface is this binary, launched by the
// product in a session of its own so it outlives the command that started
// it. That is right for a person's service and wrong for a test's, and
// nothing in the product can tell the two apart, so the test ties it to
// itself: newHome's cleanup ends every process this binary runs for the
// home, standin.WatchOwner has such a process end itself once the test
// binary that owns it is gone, and standin.RunOwned runs the check after the
// package's tests, which fails the package naming every process of this
// binary still running. internal/agents/standin owns the
// watch and the check; this file owns what a home's processes are.

// serves reports whether p was started for h, which is the command line
// every process of this binary for a home has: the binary, --home and the
// home's root.
func serves(p standin.Process, h *home.Home) bool {
	return len(p.Args) > 2 && p.Args[1] == "--home" && p.Args[2] == h.Root()
}

// stayed is standin.Stayed asked of this test binary.
func stayed(match func(standin.Process) bool, grace time.Duration) ([]standin.Process, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return standin.Stayed(self, match, grace)
}

// endServicesOf ends every process this binary runs for h: first through a
// stop asked of the surface, with a context of its own because the test's
// has ended by the time a cleanup runs, and then, for whatever is still
// running, by signal. It is newHome's cleanup, so it runs before the home is
// removed and a service is never left serving a home that no longer exists.
func endServicesOf(t *testing.T, h *home.Home) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Errorf("resolving this binary: %v", err)
		return
	}
	of := func() []standin.Process {
		all, err := standin.ProcessesOf(executable)
		if err != nil {
			t.Errorf("listing what this binary runs for %s: %v", h.Root(), err)
			return nil
		}
		var mine []standin.Process
		for _, p := range all {
			if serves(p, h) {
				mine = append(mine, p)
			}
		}
		return mine
	}
	if len(of()) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = runArgsIn(ctx, executable, "", "--home", h.Root(), "service", "stop", "--force")
	for _, p := range of() {
		if p.End() {
			t.Logf("%s did not exit on SIGTERM within %v and was killed", p, standin.EndGrace)
		}
	}
}

// A service a test leaves up is what the package's check reports, and the
// home's cleanup ends it: started through the surface, it is listed as this
// binary serving the home and still running past a grace, which is the
// check firing on the leak #405 found, and once endServicesOf has run
// nothing of this binary serves the home. The question is asked of this
// home's processes only, so the tests running beside it are not touched. It
// is asked of a home whose root has a space in it too, which a command line
// split on white space would not have matched.
//
// Its subject is the check and the cleanup, not how soon a start's service
// answers, so the start waits for that answer through
// cli.AwaitReadinessUntilTheTestsDeadline: a race-built service on a slow
// runner answered after the start's bound had run out (#543).
func TestAServiceATestLeavesUpIsReportedAndItsHomesCleanupEndsIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	cli.AwaitReadinessUntilTheTestsDeadline(t)
	for _, c := range []struct{ name, pattern string }{
		{"a plain root", "h"},
		{"a root with a space", "h s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHomeNamed(t, c.pattern)
			if got := run(t, h, "", "service", "start"); got.code != machine.ExitOK {
				t.Fatalf("starting the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
			}
			ofHome := func(p standin.Process) bool { return serves(p, h) }
			left, err := stayed(ofHome, time.Second)
			if err != nil {
				t.Fatalf("listing what this binary runs: %v", err)
			}
			if len(left) != 1 {
				t.Fatalf("a service left up for %q is reported as %v, want the one service", h.Root(), left)
			}
			endServicesOf(t, h)
			if left, err := stayed(ofHome, standin.EndGrace); err != nil || len(left) != 0 {
				t.Fatalf("after the home's cleanup %v still serve %q (%v)", left, h.Root(), err)
			}
		})
	}
}

// The package's check reports a live process of a binary whose path has a
// space in it, which is what a test binary built under a GOTMPDIR with one
// is: a copy of this binary placed at such a path, serving a home whose root
// has a space too, is listed with its executable and its arguments whole,
// is reported as still running past a grace, and is gone once ended. A
// listing split on white space would not have seen it, and the check would
// have passed with it running.
//
// The copy is waited on until it answers or exits, bounded by the test's
// own deadline rather than a fixed one: its subject is the check, and a
// race-built service on a slow runner took 12.84s of the 30s this wait once
// had (#543).
func TestTheCheckReportsALiveProcessOfABinaryWhosePathHasASpace(t *testing.T) {
	requiresIdentifiedPeer(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := standin.TempDir("b s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	copied := filepath.Join(dir, "cli test")
	content, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := procgroup.WriteProgram(copied, content, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHomeNamed(t, "h s")

	served, err := standin.StartCopied(func() (*exec.Cmd, error) {
		cmd := exec.Command(copied, "--home", h.Root(), "service", "start", "--foreground")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return cmd, cmd.Start()
	})
	if err != nil {
		t.Fatalf("starting the copy: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = served.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = syscall.Kill(-served.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	})
	deadline, bounded := t.Deadline()
	for instanceOf(t, h) == "" {
		select {
		case <-exited:
			t.Fatalf("the copy exited before it answered; its log is at %s", h.ServiceLog())
		default:
		}
		if bounded && !time.Now().Before(deadline) {
			t.Fatal("the copy never answered within the test's deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}

	left, err := standin.Stayed(copied, func(standin.Process) bool { return true }, time.Second)
	if err != nil {
		t.Fatalf("listing what the copy runs: %v", err)
	}
	if len(left) != 1 || left[0].PID != served.Process.Pid || !serves(left[0], h) {
		t.Fatalf("the copy at %q serving %q is reported as %v, want process %d with its arguments whole",
			copied, h.Root(), left, served.Process.Pid)
	}
	left[0].End()
	select {
	case <-exited:
	case <-time.After(standin.EndGrace + 5*time.Second):
		t.Fatal("the copy was still running after it was ended")
	}
}

// A service whose owning test binary is gone ends itself: a service serving
// with a stand-in owner goes on serving while the owner runs, and exits
// within the poll and the grace once the owner is killed. The owner is a
// process this test starts so it can be killed as a test binary is; the
// service is started the way the product launches one, in a session of its
// own.
//
// The stand-in owner is standin.HeldWait's held process, never released, so
// it ends with this test binary in turn: a test binary killed before its
// cleanup runs still takes the stand-in owner with it, and the service
// with that, where a /bin/sleep owner left both running with parent 1 for
// up to the hour it slept (#440).
func TestAServiceWhoseOwnerIsGoneEndsItself(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	owner := exec.Command("/bin/sh", "-c", "exec "+standin.HeldWait(t, filepath.Join(t.TempDir(), "never")))
	if err := owner.Start(); err != nil {
		t.Fatalf("starting the stand-in owner: %v", err)
	}
	ownerDone := make(chan struct{})
	go func() { _ = owner.Wait(); close(ownerDone) }()
	t.Cleanup(func() { _ = owner.Process.Kill(); <-ownerDone })
	id, found, err := agents.Identify(owner.Process.Pid)
	if err != nil || !found {
		t.Fatalf("reading the stand-in owner's identity (found %v): %v", found, err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	served := exec.Command(executable, "--home", h.Root(), "service", "start", "--foreground")
	env := []string{standin.OwnerVar + "=" + strconv.Itoa(id.PID) + ":" + strconv.FormatUint(id.Start, 10)}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, standin.OwnerVar+"=") {
			env = append(env, kv)
		}
	}
	served.Env = env
	served.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := served.Start(); err != nil {
		t.Fatalf("starting the service: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = served.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = syscall.Kill(-served.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	})

	deadline := time.Now().Add(30 * time.Second)
	for instanceOf(t, h) == "" {
		if !time.Now().Before(deadline) {
			t.Fatal("the service with a running owner never answered")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-exited:
		t.Fatal("the service exited while its owner was running")
	case <-time.After(4 * standin.OwnerPoll):
	}

	_ = owner.Process.Kill()
	<-ownerDone
	bound := standin.OwnerPoll + standin.OwnerGrace + 5*time.Second
	select {
	case <-exited:
	case <-time.After(bound):
		t.Fatalf("the service was still running %v after its owner was killed", bound)
	}
}
