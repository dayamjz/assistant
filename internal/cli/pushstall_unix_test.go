//go:build linux || darwin

package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// ofPush reports whether args are those of a process a push into h's gate
// started: the gate's receive command or a gate hook, which are this binary
// answering a gate verb for h, or the git receive-pack the receive command
// runs in h's gate. A service for h is not one, whatever it runs, and
// neither is anything a run started.
func ofPush(args []string, h *home.Home) bool {
	if !slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, h.Root()) }) {
		return false
	}
	return (len(args) > 2 && args[1] == "gate") || slices.Contains(args, "receive-pack")
}

// giveUp ends a push still running at its bound, and returns what it found
// before it did, for the failure: which of the push's processes had started,
// what the kernel said of each, and that this binary's were asked for their
// goroutine stacks.
//
// Those stacks are why it does not just kill git. Each of the push's
// processes that is this binary is sent SIGQUIT first, the hooks before the
// receive command, so the Go runtime writes every goroutine's stack to its
// standard error and exits. The receive command's standard error is git
// push's own, so its stacks reach the push's output directly, and a receive
// command listed with no stacks there never reached Go code or did not take
// the signal, which tells a stalled launch from a stalled receive. A hook's
// standard error is receive-pack's to relay, so this does not undertake that
// a hook's stacks reach the output; the listing names the hook either way.
//
// Then every process listed, and git push, is ended (endPushProcess), so
// nothing the push started outlives the test that started it. A process that
// is not listed is ended only through the process group of one that is,
// which is how a hook git runs under receive-pack ends. What is still listed
// afterwards is named in the answer.
func (p *boundedPush) giveUp() string {
	h := p.h
	var said strings.Builder
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(&said, "resolving this binary: %v\n", err)
	}
	listed, err := standin.ProcessesMatching(func(args []string) bool { return ofPush(args, h) })
	if err != nil {
		fmt.Fprintf(&said, "listing the push's processes: %v\n", err)
	}
	var receivers, hooks []standin.Process
	receivePack := false
	for _, proc := range listed {
		switch {
		case len(proc.Args) > 2 && proc.Args[0] == self && proc.Args[2] == gate.ReceiveSubcommand:
			receivers = append(receivers, proc)
		case len(proc.Args) > 2 && proc.Args[0] == self:
			hooks = append(hooks, proc)
		case slices.Contains(proc.Args, "receive-pack"):
			receivePack = true
		}
	}
	fmt.Fprintf(&said, "git push was process %d. When it was given up on, the gate's receive command had started: %t; "+
		"git receive-pack had started: %t; gate hooks running: %d.\n", p.cmd.Process.Pid, len(receivers) > 0, receivePack, len(hooks))
	said.WriteString(psLines(append([]int{p.cmd.Process.Pid}, pids(listed)...)))

	quit(hooks)
	for _, proc := range hooks {
		proc.GoneWithin(standin.EndGrace)
	}
	quit(receivers)
	if len(receivers)+len(hooks) > 0 {
		fmt.Fprintf(&said, "Each of this binary's processes above was sent SIGQUIT. A receive command's goroutine "+
			"stacks are in git's output; one listed with no stacks there never reached Go code or did not take "+
			"the signal.\n")
	}
	select {
	case <-p.done:
	case <-time.After(standin.EndGrace):
	}

	var ended sync.WaitGroup
	for _, proc := range listed {
		ended.Go(func() { endPushProcess(proc) })
	}
	ended.Wait()
	_ = p.cmd.Process.Kill()
	<-p.done
	if left, err := standin.ProcessesMatching(func(args []string) bool { return ofPush(args, h) }); err != nil {
		fmt.Fprintf(&said, "listing what is left of the push: %v\n", err)
	} else if len(left) > 0 {
		fmt.Fprintf(&said, "still running after the push was ended: %v\n", left)
	}
	return said.String()
}

// endPushProcess ends proc and, when it leads a process group of its own,
// everything in that group: receive-pack leads one, since the receive command
// runs it through procgroup.Run, and git runs the hooks in it, so ending it
// alone would leave a hook running once the receive command that would have
// ended the group is gone. A process that leads none is ended as
// standin.Process.End ends one, so the group git push shares with this test
// is never signalled.
func endPushProcess(proc standin.Process) {
	group, err := syscall.Getpgid(proc.PID)
	if err != nil || group != proc.PID {
		proc.End()
		return
	}
	if presence, err := proc.Presence(); err == nil && presence == agents.PresencePresent {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	proc.GoneWithin(standin.EndGrace)
}

// quit sends each of ps SIGQUIT once its identity still names the running
// process.
func quit(ps []standin.Process) {
	for _, proc := range ps {
		if presence, err := proc.Presence(); err == nil && presence == agents.PresencePresent {
			_ = syscall.Kill(proc.PID, syscall.SIGQUIT)
		}
	}
}

func pids(ps []standin.Process) []int {
	out := make([]int, 0, len(ps))
	for _, proc := range ps {
		out = append(out, proc.PID)
	}
	return out
}

// psLines is ps's account of the processes pids name: parent, group,
// state, and how long each has run, beside its command line. ps is bounded,
// since a launch stall would hold it too, and what it could not say is said
// instead.
func psLines(pids []int) string {
	ids := make([]string, 0, len(pids))
	for _, pid := range pids {
		ids = append(ids, strconv.Itoa(pid))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "pid,ppid,pgid,stat,etime,command", "-p", strings.Join(ids, ",")).CombinedOutput()
	if err != nil && len(out) == 0 {
		return fmt.Sprintf("ps -p %s: %v\n", strings.Join(ids, ","), err)
	}
	return string(out)
}

// A push given up on says which of its processes had started and what they
// were doing, and leaves none of them running. The push is held where it is
// known to be: the gate's copies lock is held as a cut holds it, and the
// push is given up on once its receive command has said it is waiting for
// it, so the receive command has started and is inside Go code waiting. That
// makes this the positive control for each part of the failure the push
// tests write: the listing finds the receive command, its stacks reach the
// output and name the wait, and ending the push leaves nothing of it.
func TestAPushGivenUpOnSaysWhatItWasDoingAndLeavesNothingRunning(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	_, repository := initializeGate(t, h, subject)
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")
	lock, err := gate.LockCopies(t.Context(), repository)
	if err != nil {
		t.Fatalf("holding the gate's copies lock as a cut does: %v", err)
	}
	defer func() { _ = lock.Release() }()

	push := startBoundedPush(t, h, machine.PhraseCopyBusy.Outcome, subject, "push", gate.RemoteName, "work")
	select {
	case <-push.out.cued:
	case <-push.done:
		t.Fatalf("the push ended (%v) without waiting for the held copies lock:\n%s", push.err, push.out.String())
	case <-time.After(30 * time.Second):
		_ = push.giveUp()
		t.Fatalf("the push's receive command never said it was waiting:\n%s", push.out.String())
	}
	said := push.giveUp()
	out := push.out.String()
	if !strings.Contains(said, "the gate's receive command had started: true") {
		t.Fatalf("giving up did not find the receive command that said it was waiting:\n%s\n%s", said, out)
	}
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "gate.AwaitCopies") {
		t.Fatalf("the receive command's stacks, which name its wait for the copies lock, are not in the push's "+
			"output:\n%s\n%s", said, out)
	}
	left, err := standin.ProcessesMatching(func(args []string) bool { return ofPush(args, h) })
	if err != nil {
		t.Fatalf("listing what is left of the push: %v", err)
	}
	if len(left) > 0 {
		t.Fatalf("the push was given up on and left %v running", left)
	}
}

// The same, past admission: the push is held in a hook of the gate's own
// repository that never ends, a custom admission hook (gate.CustomHookSuffix)
// that says it has started and then sleeps. That is the shape #634 reported,
// git receive-pack running under the receive command when the push was
// given up on, and it is the shape where ending the listed processes alone
// is not enough: the sleeping hook is in receive-pack's process group and
// is listed by nothing. So this checks that receive-pack is found, that the
// receive command's stacks name its wait on receive-pack, and that the
// hook's own process is gone once the push was given up on.
func TestAPushGivenUpOnInsideAHookLeavesNothingOfItRunning(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	repository := initialize(t, h, subject)
	marker := filepath.Join(t.TempDir(), "hook-pid")
	const started = "the held hook has started"
	hook := filepath.Join(repository, "hooks", gate.AdmissionHook+gate.CustomHookSuffix)
	script := fmt.Sprintf("#!/bin/sh\necho $$ >%s\necho %s >&2\nexec sleep 600\n", shellWord(marker), shellWord(started))
	if err := procgroup.WriteProgram(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the held hook: %v", err)
	}
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")

	push := startBoundedPush(t, h, started, subject, "push", gate.RemoteName, "work")
	select {
	case <-push.out.cued:
	case <-push.done:
		t.Fatalf("the push ended (%v) without reaching the held hook:\n%s", push.err, push.out.String())
	case <-time.After(30 * time.Second):
		_ = push.giveUp()
		t.Fatalf("the held hook never said it had started:\n%s", push.out.String())
	}
	written, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading the held hook's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(written)))
	if err != nil {
		t.Fatalf("the held hook wrote %q as its pid: %v", written, err)
	}
	id, found, err := agents.Identify(pid)
	if err != nil || !found {
		t.Fatalf("the held hook, process %d, is not running before the push is given up on (found %t): %v", pid, found, err)
	}
	said := push.giveUp()
	out := push.out.String()
	if !strings.Contains(said, "the gate's receive command had started: true; git receive-pack had started: true") {
		t.Fatalf("giving up did not find the receive command and receive-pack:\n%s\n%s", said, out)
	}
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "ServeReceivePack") {
		t.Fatalf("the receive command's stacks, which name its wait on receive-pack, are not in the push's "+
			"output:\n%s\n%s", said, out)
	}
	if !(standin.Process{Identity: id}).GoneWithin(standin.EndGrace) {
		t.Fatalf("the push was given up on and its hook, process %d, is still running:\n%s", pid, said)
	}
	left, err := standin.ProcessesMatching(func(args []string) bool { return ofPush(args, h) })
	if err != nil {
		t.Fatalf("listing what is left of the push: %v", err)
	}
	if len(left) > 0 {
		t.Fatalf("the push was given up on and left %v running", left)
	}
}
