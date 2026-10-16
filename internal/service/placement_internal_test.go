//go:build linux || darwin

package service

import (
	"errors"
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
	"github.com/dayamjz/assistant/internal/session"
)

// These tests read real processes with the real reader. The worker is the
// stand-in in line mode, started through the production adapter as the
// leader of its own process group, and what it starts it starts through a
// Run exchange: a shell in a process group the worker does not lead, which is
// the shape #225 observed of a command the Claude Code CLI's Bash tool runs.
// The set placement asks is one worker, answered by the worker's group, which
// is what session.Workers answers for a worker it holds.

// oneWorker is a membership lookup holding the one worker leading group.
func oneWorker(group int) func(int) (session.Membership, bool) {
	return func(id int) (session.Membership, bool) {
		if id == group {
			return session.Membership{Task: "the-task", Group: group}, true
		}
		return session.Membership{}, false
	}
}

// startedWorker starts a line-mode stand-in worker that runs what follows
// "run: " on a line, and returns it with its working directory.
func startedWorker(t *testing.T) (agents.Worker, string) {
	t.Helper()
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run: ", Run: true}},
	}}, agents.WithTerminationGrace(300*time.Millisecond))
	dir := t.TempDir()
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: dir, Instructions: "work"})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	t.Cleanup(worker.Close)
	return worker, dir
}

// waitFile polls until path holds a line and returns it.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(string(raw), "\n") {
			return strings.TrimSpace(string(raw))
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A process the worker's shell started, in a process group the worker does
// not lead, is placed as the worker's while its chain to the worker stands,
// and the group read alone would not have placed it. Then the shell exits,
// the process is reparented by the kernel, and the same walk places it as
// nobody's without an error: that is the residual gap placement.go names,
// shown in the direction it states rather than assumed. The test process,
// which is the worker's parent and no descendant of it, is placed as
// nobody's throughout, which is the person's control.
func TestPlacementFollowsTheChainAndLosesItWhenTheKernelRewritesIt(t *testing.T) {
	t.Parallel()
	worker, dir := startedWorker(t)
	group := worker.Group()
	member := oneWorker(group)

	// The shell starts a sleep in the background, says which, and waits to
	// be released; the sleep is in the shell's group, which the worker does
	// not lead.
	command := `sleep 60 >/dev/null 2>&1 & echo "$! $$" > child.tmp && mv child.tmp child; ` +
		`while [ ! -e release ]; do sleep 0.02; done`
	if err := worker.Send(t.Context(), "run: "+command); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ids := strings.Fields(waitFile(t, filepath.Join(dir, "child")))
	if len(ids) != 2 {
		t.Fatalf("the shell wrote %q, want the sleep's identifier and its own", ids)
	}
	child, err := strconv.Atoi(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	shell, err := strconv.Atoi(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	own, err := processGroup(child)
	if err != nil {
		t.Fatal(err)
	}
	if own == group {
		t.Fatalf("the sleep is in the worker's own group %d, so this is not the shape the test is about", group)
	}
	if _, ok := member(own); ok {
		t.Fatal("the group read alone places the sleep, so the walk is not what placed it")
	}
	m, ok, err := placeByAncestry(child, readLineage, member)
	if err != nil || !ok || m.Group != group {
		t.Fatalf("the worker's descendant in a group of its own was placed %+v, %v, %v; want the worker's", m, ok, err)
	}
	if _, ok, err := placeByAncestry(os.Getpid(), readLineage, member); err != nil || ok {
		t.Fatalf("the test process, the worker's parent, was placed %v, %v; want nobody's", ok, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		read, err := readLineage(child)
		if err != nil {
			t.Fatalf("the sleep is gone before it was reparented: %v", err)
		}
		if read.Parent != shell {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the sleep's parent is still the shell %d", shell)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok, err := placeByAncestry(child, readLineage, member); err != nil || ok {
		t.Fatalf("the orphaned sleep was placed %v, %v; placement.go says it is placed as nobody's", ok, err)
	}
	select {
	case <-worker.Done():
		t.Fatal("the worker ended, so the orphan was placed as nobody's for want of a worker and not of a chain")
	default:
	}
}

// A parent started after its child is a reused identifier, and the walk
// refuses it rather than reading on. The observation is the test process's
// own, read by the real reader, with one field changed: its parent names a
// process started after it, which is what the reader returns when the
// parent exited and its identifier went to a younger process. That is a
// shape the mechanism produces, and the one the start time exists to catch.
// The control is the same walk over the unchanged reads, which reaches the
// top of the chain and places nobody.
func TestPlacementRefusesAParentYoungerThanItsChild(t *testing.T) {
	t.Parallel()
	self := os.Getpid()
	nobody := oneWorker(-1)
	if _, ok, err := placeByAncestry(self, readLineage, nobody); err != nil || ok {
		t.Fatalf("the unchanged chain from this process was placed %v, %v; want nobody's and no error", ok, err)
	}
	mine, err := readLineage(self)
	if err != nil {
		t.Fatal(err)
	}
	// The younger process is an orphan, so its own chain leads away from
	// this process: a walk that read on through it would reach the top
	// without coming back here, and only the start time can refuse it. A
	// start time in clock ticks can match this process's when both started
	// within one tick, so one is started until it reads younger, which makes
	// the case the one under test rather than the stated gap.
	var younger int
	deadline := time.Now().Add(30 * time.Second)
	for younger == 0 {
		out, err := exec.Command("sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $!").Output()
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		theirs, err := readLineage(pid)
		switch {
		case err != nil:
			t.Fatal(err)
		case theirs.Start > mine.Start:
			younger = pid
		case !time.Now().Before(deadline):
			t.Fatalf("a process started now reads as started at %d, and this one at %d", theirs.Start, mine.Start)
		default:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			time.Sleep(20 * time.Millisecond)
		}
	}
	reused := func(id int) (agents.Lineage, error) {
		p, err := readLineage(id)
		if err == nil && id == self {
			p.Parent = younger
		}
		return p, err
	}
	if _, ok, err := placeByAncestry(self, reused, nobody); !errors.Is(err, errAncestry) || ok {
		t.Fatalf("a chain through a parent younger than its child was placed %v, %v; want errAncestry", ok, err)
	}
}

// A process the walk is led to and cannot read is an error and never
// "nobody's", for the peer and for a parent alike. The identifier is one a
// process held and released, read after that process was reaped.
func TestPlacementRefusesAProcessItCannotRead(t *testing.T) {
	t.Parallel()
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	pid := gone.Process.Pid
	if _, err := readLineage(pid); err == nil {
		t.Skipf("identifier %d was given to another process already", pid)
	}
	if _, ok, err := placeByAncestry(pid, readLineage, oneWorker(-1)); !errors.Is(err, errAncestry) || ok {
		t.Fatalf("a peer that cannot be read was placed %v, %v; want errAncestry", ok, err)
	}
	self := os.Getpid()
	orphaned := func(id int) (agents.Lineage, error) {
		p, err := readLineage(id)
		if err == nil && id == self {
			p.Parent = pid
		}
		return p, err
	}
	if _, ok, err := placeByAncestry(self, orphaned, oneWorker(-1)); !errors.Is(err, errAncestry) || ok {
		t.Fatalf("a parent that cannot be read was placed %v, %v; want errAncestry", ok, err)
	}
}
