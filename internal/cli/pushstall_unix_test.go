//go:build linux || darwin

package cli_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// pushTree is what a push has started, as the kernel records where it put
// each process: every process descended from git push, and every process in
// a process group one of those leads. The second half is what reaches a
// process whose parent is gone: git runs everything it starts through
// procgroup.Run in a group the child leads, and git runs the hooks inside
// receive-pack's, so a process that lost its parent to SIGQUIT or SIGKILL is
// still in the group of the process that had it. No argument is matched, so
// a git of any kind is found where a push put it.
type pushTree struct {
	// root is git push.
	root int
	// members is what the reading found, each with the arguments it was
	// read with.
	members []standin.Process
	// groups is every process group a member leads, and every group passed
	// in to the reading.
	groups map[int]bool
	// placed is where the reading found each member: its parent and its
	// group.
	placed map[int]agents.Lineage
}

// describe writes one line per member: its identifier, its parent and group
// as the reading found them, and its arguments. It is read from the kernel
// rather than from a program, so it says the same where no program can be
// started, as under the per-package leak check's sandbox on macOS, which
// refuses ps.
func (tree pushTree) describe(w io.Writer) {
	for _, proc := range tree.members {
		l := tree.placed[proc.PID]
		fmt.Fprintf(w, "  process %d (parent %d, group %d): %s\n", proc.PID, l.Parent, l.Group, strings.Join(proc.Args, " "))
	}
}

// readPushTree reads the process table once and returns the push rooted at
// root as it stands, counting every process in known's groups as the push's
// too, so a reading after a member has ended still finds what that member
// left in its group. The group this process is in is never one of them,
// since git push was started in it. A process that exits while the table is
// read is left out, and one whose identifier was given to another process
// meanwhile is read as that process; a process started in a group of its
// own by a member after the reading, whose parent then ends, is found by no
// later reading, which is the gap giveUp names.
func readPushTree(root int, known map[int]bool) (pushTree, error) {
	tree := pushTree{root: root, groups: maps.Clone(known), placed: map[int]agents.Lineage{}}
	if tree.groups == nil {
		tree.groups = map[int]bool{}
	}
	listed, err := standin.ProcessesMatching(func([]string) bool { return true })
	if err != nil {
		return tree, err
	}
	ours := syscall.Getpgrp()
	lineage := make(map[int]agents.Lineage, len(listed))
	for _, proc := range listed {
		if l, found, err := agents.ReadLineage(proc.PID); err == nil && found && l.Identity == proc.Identity {
			lineage[proc.PID] = l
		}
	}
	in := map[int]bool{}
	for grew := true; grew; {
		grew = false
		for _, proc := range listed {
			l, read := lineage[proc.PID]
			if !read || in[proc.PID] {
				continue
			}
			inGroup := l.Group != ours && tree.groups[l.Group]
			if l.Parent != root && !in[l.Parent] && !inGroup {
				continue
			}
			in[proc.PID], grew = true, true
			if l.Group == proc.PID && l.Group != ours {
				tree.groups[proc.PID] = true
			}
		}
	}
	for _, proc := range listed {
		if in[proc.PID] {
			tree.members = append(tree.members, proc)
			tree.placed[proc.PID] = lineage[proc.PID]
		}
	}
	return tree, nil
}

// stillRunning is every process of the push that is still running: what a
// fresh reading finds from root in groups, and every one of recorded whose
// identity still names a running process.
func stillRunning(root int, groups map[int]bool, recorded []standin.Process) ([]standin.Process, error) {
	now, err := readPushTree(root, groups)
	left := now.members
	for _, proc := range recorded {
		if slices.ContainsFunc(left, func(l standin.Process) bool { return l.Identity == proc.Identity }) {
			continue
		}
		if presence, perr := proc.Presence(); perr == nil && presence == agents.PresencePresent {
			left = append(left, proc)
		}
	}
	return left, err
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
// The push's processes are its pushTree, read before the signals and again
// after them, so a process that lost its parent to one is still found
// through its group. Every one of them, and git push, is then ended
// (endPushProcess), and the push is read once more: whatever is still
// running is named in the answer, so a test that fails on it fails naming
// the process. What this does not reach is a process a member started in a
// group of its own after the last reading and before that member ended,
// which no reading can place under git push once its parent is gone.
func (p *boundedPush) giveUp() string {
	var said strings.Builder
	root := p.cmd.Process.Pid
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(&said, "resolving this binary: %v\n", err)
	}
	tree, err := readPushTree(root, nil)
	if err != nil {
		fmt.Fprintf(&said, "reading what the push started: %v\n", err)
	}
	var receivers, hooks []standin.Process
	receivePack, others := false, 0
	for _, proc := range tree.members {
		switch {
		case len(proc.Args) > 2 && proc.Args[0] == self && proc.Args[2] == gate.ReceiveSubcommand:
			receivers = append(receivers, proc)
		case len(proc.Args) > 2 && proc.Args[0] == self:
			hooks = append(hooks, proc)
		case slices.Contains(proc.Args, "receive-pack"):
			receivePack = true
		default:
			others++
		}
	}
	fmt.Fprintf(&said, "git push was process %d. When it was given up on, the gate's receive command had started: %t; "+
		"git receive-pack had started: %t; gate hooks running: %d; other processes it had started: %d.\n",
		root, len(receivers) > 0, receivePack, len(hooks), others)
	tree.describe(&said)
	said.WriteString(psLines(append([]int{root}, pids(tree.members)...)))

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

	members := tree.members
	if again, err := readPushTree(root, tree.groups); err != nil {
		fmt.Fprintf(&said, "reading what the push started, after the signals: %v\n", err)
	} else {
		for _, proc := range again.members {
			if !slices.ContainsFunc(members, func(m standin.Process) bool { return m.Identity == proc.Identity }) {
				members = append(members, proc)
			}
		}
		tree.groups = again.groups
	}
	var ended sync.WaitGroup
	for _, proc := range members {
		ended.Go(func() { endPushProcess(proc) })
	}
	ended.Wait()
	_ = p.cmd.Process.Kill()
	<-p.done
	if left, err := stillRunning(root, tree.groups, members); err != nil {
		fmt.Fprintf(&said, "reading what is left of the push: %v\n", err)
	} else if len(left) > 0 {
		fmt.Fprintf(&said, "%s %v\n", stillRunningAfterGivingUp, left)
	}
	return said.String()
}

// stillRunningAfterGivingUp opens the line giveUp writes naming what of a
// push it could not end.
const stillRunningAfterGivingUp = "still running after the push was ended:"

// endPushProcess ends proc and, when it leads a process group of its own,
// everything in that group: every git the push runs leads one, since its
// parent runs it through procgroup.Run, and git runs the hooks in
// receive-pack's. What giveUp read of the push is ended process by process
// whatever this does; the group is what still reaches a member of it that
// started after the last reading, which no test here reaches. A process
// that leads none is ended as standin.Process.End ends one, so the group git
// push shares with this test is never signalled.
//
// The group is sent SIGTERM first and SIGKILL only once standin.EndGrace has
// passed with a process still in it, because the gate's hook script removes
// the temporary file it copies the reference updates into from a trap on
// SIGTERM and on exit, which SIGKILL never runs.
func endPushProcess(proc standin.Process) {
	group, err := syscall.Getpgid(proc.PID)
	if err != nil || group != proc.PID {
		proc.End()
		return
	}
	if presence, err := proc.Presence(); err != nil || presence != agents.PresencePresent {
		return
	}
	_ = syscall.Kill(-group, syscall.SIGTERM)
	if !groupGoneWithin(group, standin.EndGrace) {
		_ = syscall.Kill(-group, syscall.SIGKILL)
		groupGoneWithin(group, standin.EndGrace)
	}
}

// groupGoneWithin reports whether no process is left in the process group
// group within wait.
func groupGoneWithin(group int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// psLines is ps's account of the processes pids name: what it adds to
// pushTree.describe is each one's state and how long it has run. ps is
// bounded, since a launch stall would hold it too, and where it cannot run,
// as under the per-package leak check's sandbox on macOS, what it could not
// say is said instead.
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

	push := startBoundedPush(t, h, machine.PhraseCopyBusy.Outcome,
		gitCmd(context.Background(), subject, "push", gate.RemoteName, "work"))
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
	if strings.Contains(said, stillRunningAfterGivingUp) {
		t.Fatalf("the push was given up on and left some of it running:\n%s", said)
	}
}

// The same, past admission: the push is held in a hook of the gate's own
// repository that never ends, a custom admission hook (gate.CustomHookSuffix)
// that says it has started and then sleeps. That is the shape #634 reported,
// git receive-pack running under the receive command when the push was
// given up on, with a hook below it that is no process of this binary's. So
// this checks that receive-pack is found, that the receive command's stacks
// name its wait on receive-pack, that the hook's own process is among what
// the push started while it runs, and that it is gone, with the temporary
// file the hook script made, once the push was given up on.
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

	// The push gets a temporary directory of its own, which the hook script's
	// copy of the reference updates is made in, so what giving up leaves
	// there can be read.
	scratch := t.TempDir()
	cmd := gitCmd(context.Background(), subject, "push", gate.RemoteName, "work")
	cmd.Env = append(cmd.Env, "TMPDIR="+scratch)
	push := startBoundedPush(t, h, started, cmd)
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
	seesRunning(t, push, id, "the held hook")
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) == 0 {
		t.Fatalf("the held hook's copy of the reference updates is not in the push's temporary directory "+
			"(%d entries, %v), so the check that giving up removes it would see nothing", len(entries), err)
	}
	said := push.giveUp()
	out := push.out.String()
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("giving up on the push left %v in its temporary directory (%v):\n%s", entries, err, said)
	}
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
	if strings.Contains(said, stillRunningAfterGivingUp) {
		t.Fatalf("the push was given up on and left some of it running:\n%s", said)
	}
}

// parkGitAt makes the git a push's processes run park at the first
// invocation carrying arg: a wrapper that writes its process identifier to a
// FIFO and then becomes a sleep in place, and runs the real git, without the
// exec path, for every other invocation. It returns the directory to give
// git push as GIT_EXEC_PATH, since git puts its exec path first on the PATH
// of everything it runs, which is how the wrapper rather than the real git
// is what the receive command and the hooks under git push find. The parked
// process is then where the real git would have been: started by its parent
// through procgroup.Run, leading a process group of its own. The returned
// channel delivers its identity once it is parked.
func parkGitAt(t *testing.T, arg string) (string, <-chan agents.Identity) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("finding git: %v", err)
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "parked")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("making the FIFO the parked git reports on: %v", err)
	}
	// Opened for reading and writing, so the open does not wait for the
	// wrapper and closing it ends the read below whether or not one came.
	reader, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening the FIFO the parked git reports on: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n\tif [ \"$arg\" = %s ]; then\n"+
		"\t\techo $$ >%s\n\t\texec sleep 600\n\tfi\ndone\nunset GIT_EXEC_PATH\nexec %s \"$@\"\n",
		shellWord(arg), shellWord(fifo), shellWord(real))
	if err := procgroup.WriteProgram(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the parking git: %v", err)
	}
	parked := make(chan agents.Identity, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return
		}
		if id, found, err := agents.Identify(pid); err == nil && found {
			parked <- id
		}
	}()
	return dir, parked
}

// A push given up on while the receive command waits on a git that is not
// receive-pack, here vcs.OpenBare's rev-parse, ends that git too. It is a
// process the push started, in a process group of its own, which nothing
// names by its arguments, so this is what giving up has to find by where
// the push put it: under git push, or in a group something under it leads.
func TestAPushGivenUpOnInAGitOtherThanReceivePackEndsThatGit(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	initializeGate(t, h, subject)
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")

	execPath, parked := parkGitAt(t, "--is-bare-repository")
	cmd := gitCmd(context.Background(), subject, "push", gate.RemoteName, "work")
	cmd.Env = append(cmd.Env, "GIT_EXEC_PATH="+execPath)
	push := startBoundedPush(t, h, "", cmd)
	var stalled agents.Identity
	select {
	case stalled = <-parked:
	case <-push.done:
		t.Fatalf("the push ended (%v) without reaching the parked rev-parse:\n%s", push.err, push.out.String())
	case <-time.After(30 * time.Second):
		_ = push.giveUp()
		t.Fatalf("the receive command's rev-parse never parked:\n%s", push.out.String())
	}
	seesRunning(t, push, stalled, "the parked rev-parse")
	said := push.giveUp()
	// How many other processes that is depends on the platform: git push
	// runs the receive command through a shell that stays on Linux and not on
	// macOS. The parked git is named by its identifier in the listing either
	// way.
	if !strings.Contains(said, fmt.Sprintf("  process %d (parent ", stalled.PID)) {
		t.Fatalf("giving up did not list the parked rev-parse, process %d, among what the push had started:\n%s",
			stalled.PID, said)
	}
	if !(standin.Process{Identity: stalled}).GoneWithin(standin.EndGrace) {
		t.Fatalf("the push was given up on and the rev-parse it was waiting on, process %d, is still running:\n%s",
			stalled.PID, said)
	}
	if strings.Contains(said, stillRunningAfterGivingUp) {
		t.Fatalf("the push was given up on and left some of it running:\n%s", said)
	}
}

// seesRunning is the positive control for the check that giving up left
// nothing running: the reading that check makes, stillRunning, made of the
// push while it is held, names the process id, what names it. A reading
// that could not see the process would pass that check whether or not the
// process outlived the push.
func seesRunning(t *testing.T, push *boundedPush, id agents.Identity, what string) {
	t.Helper()
	running, err := stillRunning(push.cmd.Process.Pid, nil, nil)
	if err != nil {
		t.Fatalf("reading what the push started: %v", err)
	}
	if !slices.ContainsFunc(running, func(p standin.Process) bool { return p.Identity == id }) {
		t.Fatalf("%s, process %d, is running and is not among what the push started, %v, so the check that "+
			"giving up ended it would pass without seeing it", what, id.PID, running)
	}
}
