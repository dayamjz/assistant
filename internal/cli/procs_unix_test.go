//go:build linux || darwin

package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// A service a test starts through the surface is this binary, launched by the
// product in a session of its own so it outlives the command that started
// it. That is right for a person's service and wrong for a test's, and
// nothing in the product can tell the two apart, so this file is what ties a
// test's service to the test: newHome's cleanup ends every process this
// binary runs for the home, watchOwner has such a process end itself once
// the test binary that owns it is gone, and leftAfterTests is the check
// TestMain runs after the package, which fails the package naming every
// process of this binary still running.

// ownerVar names the environment variable carrying the identity of the test
// binary that owns every process this binary runs as, set by ownProcesses in
// TestMain and inherited by whatever the tests start, including a service the
// product launches and the successor a restart launches.
const ownerVar = "CLI_TEST_OWNER"

// ownerPoll is how often an owned process looks for its owner, and
// ownerGrace how long it gives its own graceful exit, on SIGTERM, before it
// kills its process group.
const (
	ownerPoll  = 250 * time.Millisecond
	ownerGrace = 10 * time.Second
)

// endGrace is how long a test's cleanup and the package's check give a
// process of this binary to exit on SIGTERM before they kill it.
const endGrace = 10 * time.Second

// ownProcesses records this process as the owner of every process the tests
// start. It is called in the process that runs the tests, after
// standin.AtTerminal, so the owner is the binary whose exit ends the tests.
func ownProcesses() error {
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		return fmt.Errorf("reading this test binary's own identity (found %v): %w", found, err)
	}
	return os.Setenv(ownerVar, strconv.Itoa(self.PID)+":"+strconv.FormatUint(self.Start, 10))
}

// watchOwner has this process end once the test binary named by ownerVar is
// gone, and does nothing when the variable is unset. It is the parent-death
// signal macOS does not have and Linux has only for an immediate parent,
// which a restart's successor does not have: the successor's parent is the
// service it replaced, which exits by design. The process is first sent
// SIGTERM, which the service's own shutdown handles, and then, ownerGrace
// later, SIGKILL, to its whole group when it leads one, which a launched
// service does, since the launch gives it a session of its own.
//
// What it does not cover: an owner read as present that is gone is noticed
// within ownerPoll, so a process outlives its owner by up to that plus
// ownerGrace; a process stopped by a signal runs no watcher; and what this
// process started in a session or group of its own, a terminal server for
// one, is reached by neither signal and is left to the service's own
// shutdown.
func watchOwner() {
	value := os.Getenv(ownerVar)
	if value == "" {
		return
	}
	pid, start, ok := strings.Cut(value, ":")
	owner := agents.Identity{}
	var perr, serr error
	owner.PID, perr = strconv.Atoi(pid)
	owner.Start, serr = strconv.ParseUint(start, 10, 64)
	if !ok || perr != nil || serr != nil {
		fmt.Fprintf(os.Stderr, "the test binary's owner %s=%q does not parse, so nothing would end this process with it\n", ownerVar, value)
		os.Exit(1)
	}
	go func() {
		for {
			time.Sleep(ownerPoll)
			if presence, err := owner.Presence(); err == nil && presence != agents.PresencePresent {
				break
			}
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(ownerGrace)
		if group, err := syscall.Getpgid(0); err == nil && group == os.Getpid() {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}()
}

// binaryProcess is a process running this test binary: its identity and
// its arguments, argv[0] first, as the kernel holds them, one string per
// argument whatever the arguments contain.
type binaryProcess struct {
	agents.Identity
	args []string
}

// String names the process and its command line, for a failure.
func (p binaryProcess) String() string {
	return fmt.Sprintf("process %d (%s)", p.PID, strings.Join(p.args, " "))
}

// serves reports whether p was started for h, which is the command line
// every process of this binary for a home has: the binary, --home and the
// home's root.
func (p binaryProcess) serves(h *home.Home) bool {
	return len(p.args) > 2 && p.args[1] == "--home" && p.args[2] == h.Root()
}

// binaryProcesses is binaryProcessesOf this test binary.
func binaryProcesses() ([]binaryProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving this binary: %w", err)
	}
	return binaryProcessesOf(self)
}

// binaryProcessesOf lists every process whose executable is binary, other
// than this process and its ancestors, which are the test binary run again
// at a terminal by standin.AtTerminal. The executable and the arguments are
// read per process from the kernel (commandLine), never split out of a
// listing, so a path or an argument with a space in it is read whole. A
// list that cannot be read is an error and never an empty list; a single
// process whose command line cannot be read, one that has exited or one of
// another user's, is passed over.
func binaryProcessesOf(binary string) ([]binaryProcess, error) {
	exclude := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !exclude[pid]; {
		exclude[pid] = true
		lineage, found, err := agents.ReadLineage(pid)
		if err != nil || !found {
			break
		}
		pid = lineage.Parent
	}
	pids, err := processIDs()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	var found []binaryProcess
	for _, pid := range pids {
		if exclude[pid] {
			continue
		}
		executable, args, err := commandLine(pid)
		if err != nil || executable != binary {
			continue
		}
		id, present, err := agents.Identify(pid)
		if err != nil || !present {
			continue
		}
		found = append(found, binaryProcess{Identity: id, args: args})
	}
	return found, nil
}

// end sends p SIGTERM and waits up to endGrace for it to go, then sends
// SIGKILL to its group when it leads one and to it otherwise. Before each
// signal the identity is checked to still name the running process, which
// narrows the chance of signalling a later process given the same number to
// the moment between that check and the signal; it does not close it. It
// reports whether p had to be killed.
func (p binaryProcess) end() bool {
	if presence, err := p.Presence(); err != nil || presence != agents.PresencePresent {
		return false
	}
	_ = syscall.Kill(p.PID, syscall.SIGTERM)
	if p.goneWithin(endGrace) {
		return false
	}
	if presence, err := p.Presence(); err != nil || presence != agents.PresencePresent {
		return false
	}
	if group, err := syscall.Getpgid(p.PID); err == nil && group == p.PID {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = syscall.Kill(p.PID, syscall.SIGKILL)
	}
	p.goneWithin(endGrace)
	return true
}

// goneWithin reports whether p is no longer running within wait.
func (p binaryProcess) goneWithin(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if presence, err := p.Presence(); err == nil && presence != agents.PresencePresent {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// endServicesOf ends every process this binary runs for h: first through a
// stop asked of the surface, with a context of its own because the test's
// has ended by the time a cleanup runs, and then, for whatever is still
// running, by signal. It is newHome's cleanup, so it runs before the home is
// removed and a service is never left serving a home that no longer exists.
func endServicesOf(t *testing.T, h *home.Home) {
	t.Helper()
	of := func() []binaryProcess {
		all, err := binaryProcesses()
		if err != nil {
			t.Errorf("listing what this binary runs for %s: %v", h.Root(), err)
			return nil
		}
		var mine []binaryProcess
		for _, p := range all {
			if p.serves(h) {
				mine = append(mine, p)
			}
		}
		return mine
	}
	if len(of()) == 0 {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Errorf("resolving this binary: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = runArgsIn(ctx, executable, "", "--home", h.Root(), "service", "stop", "--force")
	for _, p := range of() {
		if p.end() {
			t.Logf("%s did not exit on SIGTERM within %v and was killed", p, endGrace)
		}
	}
}

// leftAfterTests is the check TestMain runs once every test has returned:
// every process still running this binary is a process the tests started
// and did not end. It gives each endGrace to finish exiting, then names
// every one still running on standard error, ends it, and reports false, so
// the package fails and leaves nothing behind. A process list that cannot be
// read fails the package too, since a check that read nothing would pass.
func leftAfterTests() bool {
	left, err := stayed(func(binaryProcess) bool { return true }, endGrace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: could not check for processes the tests left running: %v\n", err)
		return false
	}
	if len(left) == 0 {
		return true
	}
	for _, p := range left {
		fmt.Fprintf(os.Stderr, "FAIL: %s was started by this package's tests and was still running %v after they finished\n", p, endGrace)
	}
	for _, p := range left {
		p.end()
	}
	return false
}

// stayed is every process of this binary match accepts that is still running
// grace after it was listed, which is leftAfterTests' question asked of a
// subset, so a test can ask it of the processes of one home while other
// tests' processes run.
func stayed(match func(binaryProcess) bool, grace time.Duration) ([]binaryProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving this binary: %w", err)
	}
	return stayedOf(self, match, grace)
}

// stayedOf is stayed asked of the processes of binary, so a test can ask
// the question leftAfterTests asks of a copy of this binary it placed.
func stayedOf(binary string, match func(binaryProcess) bool, grace time.Duration) ([]binaryProcess, error) {
	listed, err := binaryProcessesOf(binary)
	if err != nil {
		return nil, err
	}
	var left []binaryProcess
	for _, p := range listed {
		if match(p) && !p.goneWithin(grace) {
			left = append(left, p)
		}
	}
	return left, nil
}

// A service a test leaves up is what the package's check reports, and the
// home's cleanup ends it: started through the surface, it is listed as this
// binary serving the home and still running past a grace, which is the
// check firing on the leak #405 found, and once endServicesOf has run
// nothing of this binary serves the home. The question is asked of this
// home's processes only, so the tests running beside it are not touched. It
// is asked of a home whose root has a space in it too, which a command line
// split on white space would not have matched.
func TestAServiceATestLeavesUpIsReportedAndItsHomesCleanupEndsIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	for _, c := range []struct{ name, pattern string }{
		{"a plain root", "h"},
		{"a root with a space", "h s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHomeNamed(t, c.pattern)
			if got := run(t, h, "", "service", "start"); got.code != machine.ExitOK {
				t.Fatalf("starting the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
			}
			ofHome := func(p binaryProcess) bool { return p.serves(h) }
			left, err := stayed(ofHome, time.Second)
			if err != nil {
				t.Fatalf("listing what this binary runs: %v", err)
			}
			if len(left) != 1 {
				t.Fatalf("a service left up for %q is reported as %v, want the one service", h.Root(), left)
			}
			endServicesOf(t, h)
			if left, err := stayed(ofHome, endGrace); err != nil || len(left) != 0 {
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
func TestTheCheckReportsALiveProcessOfABinaryWhosePathHasASpace(t *testing.T) {
	requiresIdentifiedPeer(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "b s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	copied := filepath.Join(dir, "cli test")
	content, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, content, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHomeNamed(t, "h s")

	served := exec.Command(copied, "--home", h.Root(), "service", "start", "--foreground")
	served.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := served.Start(); err != nil {
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
	deadline := time.Now().Add(30 * time.Second)
	for instanceOf(t, h) == "" {
		if !time.Now().Before(deadline) {
			t.Fatal("the copy never answered")
		}
		time.Sleep(50 * time.Millisecond)
	}

	left, err := stayedOf(copied, func(binaryProcess) bool { return true }, time.Second)
	if err != nil {
		t.Fatalf("listing what the copy runs: %v", err)
	}
	if len(left) != 1 || left[0].PID != served.Process.Pid || !left[0].serves(h) {
		t.Fatalf("the copy at %q serving %q is reported as %v, want process %d with its arguments whole",
			copied, h.Root(), left, served.Process.Pid)
	}
	left[0].end()
	select {
	case <-exited:
	case <-time.After(endGrace + 5*time.Second):
		t.Fatal("the copy was still running after it was ended")
	}
}

// A service whose owning test binary is gone ends itself: a service serving
// with a stand-in owner goes on serving while the owner runs, and exits
// within the poll and the grace once the owner is killed. The owner is a
// process this test starts so it can be killed as a test binary is; the
// service is started the way the product launches one, in a session of its
// own.
func TestAServiceWhoseOwnerIsGoneEndsItself(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	owner := exec.Command("/bin/sleep", "3600")
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
	env := []string{ownerVar + "=" + strconv.Itoa(id.PID) + ":" + strconv.FormatUint(id.Start, 10)}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, ownerVar+"=") {
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
	case <-time.After(4 * ownerPoll):
	}

	_ = owner.Process.Kill()
	<-ownerDone
	bound := ownerPoll + ownerGrace + 5*time.Second
	select {
	case <-exited:
	case <-time.After(bound):
		t.Fatalf("the service was still running %v after its owner was killed", bound)
	}
}
