//go:build linux || darwin

package cli_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
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

// pushRecord is everything giving up on a push has learned of it (giveUp):
// every process a reading attributed to the push, every process group one
// of those led when it was read, and every read that failed. One pass of
// giving up keeps one record, and its invariant is that it only grows. A
// reading adds every process it attributed and every group it found one of
// them leading, whether or not another of its reads failed, and nothing any
// reading answers removes anything. Giving up ends what the record holds and
// takes its verdict (verdict) on what the record holds, so a process one
// reading found is ended and checked even when no later reading can find
// it, as none can once its parent has ended and its group was never
// recorded.
//
// A reading (read) lists the process table once and attributes a process
// to the push by what the record holds and where the kernel records the
// process, never by its arguments: the record holds it already, or its
// parent is git push while git push is held (rootHeld) or is a process the
// same reading attributes, or it is in a group the record holds other than
// this process's own. The record starts holding git push's own group, which
// git push leads (inOwnGroup), so what git push started and did not move to
// a group of its own is found there after git push has ended, whatever its
// parent is by then. Every git the push runs is started through
// procgroup.Run in a group it leads, and git runs the hooks in
// receive-pack's, so a process that has lost its parent is still found
// through its group once a reading has recorded that group. git push itself
// is never one of the processes: it is ended through its own exec.Cmd.
//
// What no reading attributes, and so nothing here ends: a process started
// after the last reading that could place it, either in a group or session
// of its own, whose parent had ended by the next reading, or after the
// pass's last reading, which only a process of the push still running then
// can start, and that process fails the verdict unless it exits before its
// presence is read. Giving up makes the first case itself when a recorded
// process starts one in a group of its own after the last reading before
// the end, since the end then ends its parent. Either kind that holds the
// push's output still fails the verdict, as one that keeps the output from
// reaching its end (giveUp), and once the output has reached its end no
// process holds it, since a process holds it only by having it from one
// that did; one that holds no copy of it is not seen at all. A process
// number given to another process while giving up runs is read as that
// other process, and a group number so given is read as the push's, the
// coincidence agents.ForceGroup names.
type pushRecord struct {
	reads table
	// root is git push. rootHeld is whether it has not been waited for,
	// while which a process whose parent is root is taken for its child.
	root     int
	rootHeld bool
	// processes is every process a reading attributed to the push, in the
	// order first found.
	processes []recorded
	// groups is git push's own group and every process group a recorded
	// process led when it was read.
	groups map[int]bool
	// failed is every read that failed, each naming the reading that made
	// it, and the stop of git push when it failed other than by git push
	// having ended.
	failed []error
}

// recorded is one process a reading attributed to a push.
type recorded struct {
	standin.Process
	// placed is the process's parent and group as the reading that first
	// found it read them.
	placed agents.Lineage
	// reached is whether an end (pushRecord.end) has acted on it.
	reached bool
}

// newPushRecord is a record of push for a new pass, read through reads, that
// holds git push's own group and nothing else yet, with git push held until
// the push has been waited for.
func newPushRecord(reads table, push *boundedPush) *pushRecord {
	root := push.cmd.Process.Pid
	r := &pushRecord{reads: reads, root: root, groups: map[int]bool{root: true}}
	select {
	case <-push.done:
	default:
		r.rootHeld = true
	}
	return r
}

// inOwnGroup has cmd, a git push not yet started, start leading a process
// group of its own, which pushRecord starts from. The gate still places such
// a push as a person's: placement walks the push's whole chain of parents,
// and this test binary, on it, is in its terminal's foreground
// (standin.AtTerminal). The gate refuses a push it places as nobody's
// (internal/service's admitPusher), and
// TestAPushReturnsWhileTheRunItStartedIsStillInsideAStage's push, made this
// way, is admitted and starts a run.
func inOwnGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// table is the two reads of the process table a reading makes: the listing,
// as standin.ProcessesMatching makes it, and where a process is, as
// agents.ReadLineage reads it. kernelTable is the real one; a test makes one
// of them fail to show that a reading that failed is not read as one that
// found nothing.
type table struct {
	list    func(match func(args []string) bool) ([]standin.Process, error)
	lineage func(pid int) (agents.Lineage, bool, error)
}

// kernelTable reads the process table the kernel keeps.
var kernelTable = table{list: standin.ProcessesMatching, lineage: agents.ReadLineage}

// read makes one reading of the process table, adds to the record what it
// attributes to the push, and returns what it added. A listing that fails,
// and a listed process whose parent and group cannot be read, are recorded
// as failed reads named by when; whatever else the reading placed is
// attributed and added all the same. A listed process that has exited, or
// whose number names another process, by the time its parent and group are
// read is left out, as one no longer running.
func (r *pushRecord) read(when string) []recorded {
	listed, err := r.reads.list(func([]string) bool { return true })
	if err != nil {
		r.failed = append(r.failed, fmt.Errorf("%s: listing the processes: %w", when, err))
		return nil
	}
	placed := make(map[int]agents.Lineage, len(listed))
	for _, proc := range listed {
		l, found, err := r.reads.lineage(proc.PID)
		switch {
		case err != nil:
			r.failed = append(r.failed, fmt.Errorf("%s: reading where %v is: %w", when, proc, err))
		case found && l.Identity == proc.Identity:
			placed[proc.PID] = l
		}
	}
	ours := syscall.Getpgrp()
	in := map[int]bool{}
	var added []recorded
	for grew := true; grew; {
		grew = false
		for _, proc := range listed {
			l, ok := placed[proc.PID]
			if !ok || in[proc.PID] || proc.PID == r.root {
				continue
			}
			held := r.holds(proc.Identity)
			underPush := (r.rootHeld && l.Parent == r.root) || in[l.Parent]
			inPushGroup := l.Group != ours && r.groups[l.Group]
			if !held && !underPush && !inPushGroup {
				continue
			}
			in[proc.PID], grew = true, true
			if l.Group == proc.PID && l.Group != ours {
				r.groups[l.Group] = true
			}
			if !held {
				r.processes = append(r.processes, recorded{Process: proc, placed: l})
				added = append(added, r.processes[len(r.processes)-1])
			}
		}
	}
	return added
}

// holds reports whether the record holds the process id names.
func (r *pushRecord) holds(id agents.Identity) bool {
	return slices.ContainsFunc(r.processes, func(p recorded) bool { return p.Identity == id })
}

// held is every process the record holds.
func (r *pushRecord) held() []standin.Process {
	out := make([]standin.Process, 0, len(r.processes))
	for _, p := range r.processes {
		out = append(out, p.Process)
	}
	return out
}

// describe writes one line per process in ps: its identifier, its parent
// and group as the reading that found it read them, and its arguments. It
// is read from the kernel rather than from a program, so it says the same
// where no program can be started, as under the per-package leak check's
// sandbox on macOS, which refuses ps.
func describe(w io.Writer, ps []recorded) {
	for _, p := range ps {
		fmt.Fprintf(w, "  process %d (parent %d, group %d): %s\n", p.PID, p.placed.Parent, p.placed.Group, strings.Join(p.Args, " "))
	}
}

// end ends every process the record holds that no end has reached yet, all
// at once, and returns a line for each that it could not answer as ended.
// git push is never one of them: read leaves it out, and giveUp kills it
// through its exec.Cmd.
//
// A process that led its process group when it was read (placed.Group is
// its own identifier) is ended with that group through agents.EndGroup,
// which signals nothing unless the leader's identity still reads as a
// running process, and then sends the group SIGTERM and, once
// standin.EndGrace has passed with a process still in it, SIGKILL. So a
// group whose leader is gone is not signalled, and what is left in it is
// ended one process at a time, as readings attribute it through the group.
// Every other process is ended through standin.Process.End: SIGTERM to that
// process alone and, once standin.EndGrace has passed with it still
// running, SIGKILL, to its group when it leads one by then and to it alone
// otherwise. Its identity is read before each signal, which narrows, and
// does not close, the chance of signalling another process later given the
// same identifier. git push's own group (inOwnGroup) is therefore never
// signalled as a group: its leader, git push, is never a recorded process,
// and no other process leads it, so what is in it is ended one process at a
// time. SIGTERM comes first because the gate's hook script removes the
// temporary file it copies the reference updates into from a trap on
// SIGTERM and on exit, which SIGKILL never runs.
//
// A read the end makes that fails sends nothing. The end decides nothing
// either way: the verdict reads again whether each process still runs.
func (r *pushRecord) end() []string {
	var (
		mu    sync.Mutex
		lines []string
		ended sync.WaitGroup
	)
	for i := range r.processes {
		p := &r.processes[i]
		if p.reached {
			continue
		}
		p.reached = true
		proc, leads := p.Process, p.placed.Group == p.PID
		ended.Go(func() {
			if !leads {
				proc.End()
				return
			}
			end, err := agents.EndGroup(proc.Identity, standin.EndGrace)
			if err == nil && (end == agents.GroupEnded || end == agents.GroupGone) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf("ending %v with its group answered %q (%v)", proc, end, err))
		})
	}
	ended.Wait()
	return lines
}

// verdict is nil only when nothing the record holds failed and every process
// it holds reads now as no longer running (agents.Identity.Presence);
// otherwise it is a *notEnded naming what still runs and everything that
// failed, its own reads included. A failed read is never taken for one that found
// nothing, whichever reading made it: one before the end may have missed a
// process the end would then have ended, and once the end has ended that
// process's parent no later reading can tie it to the push
// (TestAPushGivenUpOnKeepsWhatEachReadingFound).
func (r *pushRecord) verdict() error {
	left := &notEnded{failed: slices.Clone(r.failed)}
	for _, p := range r.processes {
		presence, err := p.Presence()
		switch {
		case err != nil:
			left.failed = append(left.failed, fmt.Errorf("reading whether %v still runs: %w", p.Process, err))
		case presence == agents.PresencePresent:
			left.running = append(left.running, p.Process)
		}
	}
	if len(left.running) == 0 && len(left.failed) == 0 {
		return nil
	}
	return left
}

// notEnded is a verdict that a push is not shown ended: what of it still
// runs, everything that kept giving up from reading the whole of it, and why
// its output did not reach its end. Any one alone is enough.
type notEnded struct {
	running []standin.Process
	failed  []error
	output  error
}

// Error names what still runs, everything that failed, and why the output
// did not reach its end.
func (n *notEnded) Error() string {
	var parts []string
	if len(n.running) > 0 {
		parts = append(parts, fmt.Sprintf("%s %v", stillRunningAfterGivingUp, n.running))
	}
	if len(n.failed) > 0 {
		parts = append(parts, fmt.Sprintf("giving up could not read the whole of the push, so it is not shown ended: %v",
			errors.Join(n.failed...)))
	}
	if n.output != nil {
		parts = append(parts, n.output.Error())
	}
	return strings.Join(parts, "; ")
}

// Unwrap is everything that failed, and why the output did not reach its
// end.
func (n *notEnded) Unwrap() []error {
	if n.output == nil {
		return n.failed
	}
	return append(slices.Clone(n.failed), n.output)
}

// withOutput is verdict, a record's, with output added, why the push's
// output did not reach its end: nil only when both are.
func withOutput(verdict, output error) error {
	if output == nil {
		return verdict
	}
	var left *notEnded
	if !errors.As(verdict, &left) {
		left = &notEnded{}
	}
	left.output = output
	return left
}

// stillRunningAfterGivingUp opens the part of a verdict naming what of a
// push giving up could not end.
const stillRunningAfterGivingUp = "still running after the push was ended:"

// errOutputHeld is the verdict's part saying the push's output never reached
// its end, so a process the push started still holds it.
var errOutputHeld = errors.New("the push's output had not reached its end once git push and everything giving up " +
	"found of the push were ended, so a process the push started still holds it")

// giveUp is one pass of giving up on the push: it ends every process of the
// push it finds, and returns what it found, for a failure, with its verdict
// on whether the push ended, which boundedPush's invariant is held to. It is
// the only way anything of a push is ended, and a push is given up on at
// least once, by its test's cleanup (endWithTest), and again whenever a test
// or within gives up on it first.
//
// While git push still runs, the pass first finds out what the push was
// doing: which of its processes had started, what the kernel said of each,
// and that this binary's were asked for their goroutine stacks. Those stacks
// are why it does not just kill git. Each of the push's processes that is
// this binary is sent SIGQUIT first, the hooks before the receive command,
// so the Go runtime writes every goroutine's stack to its standard error and
// exits. The receive command's standard error is git push's own, so its
// stacks reach the push's output directly, and a receive command listed with
// no stacks there never reached Go code or did not take the signal, which
// tells a stalled launch from a stalled receive. A hook's standard error is
// receive-pack's to relay, so this does not undertake that a hook's stacks
// reach the output; the listing names the hook either way. Then git push is
// stopped, as journey's endTree stops the service before it lists what the
// service started, so the reading that follows and the end find git push as
// it was stopped, still the parent of what it started. A stop that fails
// other than by git push having ended is a failure of the record's, as a
// failed read is. Once git push has been waited for, none of that applies:
// the pass reads what is left in git push's group, and under it.
//
// Everything a pass learns goes into one pushRecord, which starts holding
// git push's own group (newPushRecord), from its readings: when the push
// is given up on, while git push runs, which the signals are chosen from;
// after the signals, or once git push had ended; and after the end has ended
// everything the record holds and git push has been killed and waited for.
// Between the end and that last reading, the push's output is given
// heldOutputGrace to reach its end, which it reaches only once nothing holds
// it. The verdict, gaveUp.notEnded, is the record's (pushRecord.verdict) with
// the output's end (withOutput), taken after the last reading: nil only when
// every read of the pass, and the stop, succeeded, nothing the record holds
// still runs, and the output reached its end. What the last reading found is
// ended once the verdict is taken, so it does not outlive the test, and it
// leaves the verdict as it was. A git push that has ended by itself before it
// could be stopped leaves what it started that left its group to the gap
// pushRecord names.
func (p *boundedPush) giveUp() gaveUp { return p.giveUpReading(kernelTable) }

// giveUpReading is giveUp with every reading of the process table made
// through reads.
func (p *boundedPush) giveUpReading(reads table) gaveUp {
	var said strings.Builder
	rec := newPushRecord(reads, p)
	found := false
	reading := func(when, heading string) []recorded {
		before := len(rec.failed)
		added := rec.read(when)
		for _, err := range rec.failed[before:] {
			fmt.Fprintf(&said, "%v\n", err)
		}
		if len(added) > 0 && heading != "" {
			said.WriteString(heading + "\n")
			describe(&said, added)
		}
		found = found || len(added) > 0
		return added
	}
	ending := func() {
		for _, line := range rec.end() {
			fmt.Fprintf(&said, "%s\n", line)
		}
	}

	if rec.rootHeld {
		p.signal(rec, &said, reading("the reading when the push was given up on", ""))
		reading("the reading after the signals", "Found after the signals, and not before them:")
	} else {
		fmt.Fprintf(&said, "git push, process %d, had ended (%v) before the push was given up on.\n", rec.root, p.err)
		reading("the reading once git push had ended", "Found once git push had ended:")
	}
	ending()
	_ = p.cmd.Process.Kill()
	<-p.done
	rec.rootHeld = false
	output := p.outputEnd()
	reading("the reading after the push was ended", "Found after the push was ended:")
	verdict := withOutput(rec.verdict(), output)
	ending()
	if verdict != nil {
		fmt.Fprintf(&said, "%v\n", verdict)
	}
	return gaveUp{said: said.String(), notEnded: verdict, found: found}
}

// signal is the part of giving up made only while git push still runs: from
// first, the first reading's processes, it writes to said what had started,
// sends this binary's processes among them SIGQUIT (giveUp), gives git push
// standin.EndGrace to end, and stops it.
func (p *boundedPush) signal(rec *pushRecord, said *strings.Builder, first []recorded) {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(said, "resolving this binary: %v\n", err)
	}
	var receivers, hooks []standin.Process
	receivePack, others := false, 0
	for _, proc := range first {
		switch {
		case len(proc.Args) > 2 && proc.Args[0] == self && proc.Args[2] == gate.ReceiveSubcommand:
			receivers = append(receivers, proc.Process)
		case len(proc.Args) > 2 && proc.Args[0] == self:
			hooks = append(hooks, proc.Process)
		case slices.Contains(proc.Args, "receive-pack"):
			receivePack = true
		default:
			others++
		}
	}
	fmt.Fprintf(said, "git push was process %d. When it was given up on, the gate's receive command had started: %t; "+
		"git receive-pack had started: %t; gate hooks running: %d; other processes it had started: %d.\n",
		rec.root, len(receivers) > 0, receivePack, len(hooks), others)
	describe(said, first)
	said.WriteString(psLines(append([]int{rec.root}, pids(first)...)))

	quit(said, hooks)
	for _, proc := range hooks {
		proc.GoneWithin(standin.EndGrace)
	}
	quit(said, receivers)
	if len(receivers)+len(hooks) > 0 {
		fmt.Fprintf(said, "Each of this binary's processes above was sent SIGQUIT. A receive command's goroutine "+
			"stacks are in git's output; one listed with no stacks there never reached Go code or did not take "+
			"the signal.\n")
	}
	select {
	case <-p.done:
	case <-time.After(standin.EndGrace):
	}

	switch err := p.cmd.Process.Signal(syscall.SIGSTOP); {
	case errors.Is(err, os.ErrProcessDone):
		rec.rootHeld = false
		fmt.Fprintf(said, "git push had ended by itself once the signals were sent.\n")
	case err != nil:
		rec.failed = append(rec.failed, fmt.Errorf("stopping git push, process %d: %w", rec.root, err))
		fmt.Fprintf(said, "%v\n", rec.failed[len(rec.failed)-1])
	}
}

// outputEnd waits up to heldOutputGrace for the push's output to reach its
// end, and answers nil when it did, errOutputHeld when it had not by then,
// and the failed read when its end was never read.
func (p *boundedPush) outputEnd() error {
	select {
	case <-p.ended:
		if p.readErr != nil {
			return fmt.Errorf("reading the push's output failed, so its end was never read: %w", p.readErr)
		}
		return nil
	case <-time.After(heldOutputGrace):
		return fmt.Errorf("%w (given %s)", errOutputHeld, heldOutputGrace)
	}
}

// quit sends each of ps SIGQUIT once its identity still names the running
// process, and writes to w each one it could not tell was still running,
// since that one's stacks will be missing.
func quit(w io.Writer, ps []standin.Process) {
	for _, proc := range ps {
		presence, err := proc.Presence()
		switch {
		case err != nil:
			fmt.Fprintf(w, "could not tell whether %v still runs, so it was not sent SIGQUIT: %v\n", proc, err)
		case presence == agents.PresencePresent:
			_ = syscall.Kill(proc.PID, syscall.SIGQUIT)
		}
	}
}

func pids(ps []recorded) []int {
	out := make([]int, 0, len(ps))
	for _, proc := range ps {
		out = append(out, proc.PID)
	}
	return out
}

// psLines is ps's account of the processes pids name: what it adds to
// describe is each one's state and how long it has run. ps is bounded,
// since a launch stall would hold it too, and where it cannot run, as under
// the per-package leak check's sandbox on macOS, what it could not say is
// said instead.
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
// output and name the wait, and ending the push leaves nothing of it, which
// the verdict is first shown to see while the receive command runs
// (seesRunning).
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
		t.Fatalf("the push's receive command never said it was waiting:\n%s", push.out.String())
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	receiver := seesRunning(t, push, "the receive command", func(p standin.Process) bool {
		return len(p.Args) > 2 && p.Args[0] == self && p.Args[2] == gate.ReceiveSubcommand
	})
	gave := push.giveUp()
	said := gave.said
	out := push.out.String()
	if !(standin.Process{Identity: receiver}).GoneWithin(standin.EndGrace) {
		t.Fatalf("the push was given up on and its receive command, process %d, is still running:\n%s", receiver.PID, said)
	}
	if !strings.Contains(said, "the gate's receive command had started: true") {
		t.Fatalf("giving up did not find the receive command that said it was waiting:\n%s\n%s", said, out)
	}
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "gate.AwaitCopies") {
		t.Fatalf("the receive command's stacks, which name its wait for the copies lock, are not in the push's "+
			"output:\n%s\n%s", said, out)
	}
	if gave.notEnded != nil {
		t.Fatalf("the push was given up on and is not shown ended: %v\n%s", gave.notEnded, said)
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
	t.Cleanup(func() { standin.Process{Identity: id}.End() })
	seesRunning(t, push, "the held hook", isProcess(id))
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) == 0 {
		t.Fatalf("the held hook's copy of the reference updates is not in the push's temporary directory "+
			"(%d entries, %v), so the check that giving up removes it would see nothing", len(entries), err)
	}
	gave := push.giveUp()
	said := gave.said
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
	if gave.notEnded != nil {
		t.Fatalf("the push was given up on and is not shown ended: %v\n%s", gave.notEnded, said)
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
	return gitWrapperAt(t, arg, func(dir string) string {
		return "echo $$ >" + shellWord(filepath.Join(dir, parkedFIFO)) + "\n\t\texec sleep 600"
	})
}

// parkedFIFO and childFIFO are the FIFOs, in the directory gitWrapperAt
// makes, that a parked process and the child the spawner starts write their
// process identifiers to.
const (
	parkedFIFO = "parked"
	childFIFO  = "child"
)

// gitWrapperAt is parkGitAt with what the wrapper does at the invocation
// carrying arg written by park, given the wrapper's directory: shell text
// that ends in an exec, so what parks is the process the receive command
// started, and that reports its identifier on parkedFIFO there.
func gitWrapperAt(t *testing.T, arg string, park func(dir string) string) (string, <-chan agents.Identity) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("finding git: %v", err)
	}
	dir := t.TempDir()
	parked := reportedOn(t, filepath.Join(dir, parkedFIFO))
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n\tif [ \"$arg\" = %s ]; then\n"+
		"\t\t%s\n\tfi\ndone\nunset GIT_EXEC_PATH\nexec %s \"$@\"\n",
		shellWord(arg), park(dir), shellWord(real))
	if err := procgroup.WriteProgram(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the parking git: %v", err)
	}
	return dir, parked
}

// reportedOn makes the FIFO fifo and delivers, on the returned channel, the
// identity of the first process identifier written to it as a line, once it
// names a running process.
func reportedOn(t *testing.T, fifo string) <-chan agents.Identity {
	t.Helper()
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("making the FIFO %s: %v", fifo, err)
	}
	// Opened for reading and writing, so the open does not wait for a
	// writer and closing it ends the read below whether or not one came.
	reader, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("opening the FIFO %s: %v", fifo, err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	reported := make(chan agents.Identity, 1)
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
			reported <- id
		}
	}()
	return reported
}

// spawnerVar, set in the environment of this binary started with no
// argument, makes that process the spawner (spawnerMain). Its value is the
// directory gitWrapperAt made, which holds parkedFIFO and childFIFO.
const spawnerVar = "ASSISTANT_CLI_TEST_SPAWNER"

// spawnerMain makes this process the spawner when it was started as one, and
// returns otherwise. The spawner writes its process identifier to
// parkedFIFO once SIGUSR1 would release it rather than end it, waits for that
// signal, and then runs, through procgroup.Run, which starts what it runs in
// a process group of its own as every program this repository starts is
// started, a shell that writes its process identifier to childFIFO and
// becomes a sleep. So a test decides when a process the push started begins
// in a group of its own under a parent still running, which is between two
// of giving up's readings. Parked in place of a git the receive command
// runs (spawnerAt), it is not one of this binary's processes giving up sends
// SIGQUIT, since it carries no argument. It handles no signal but SIGUSR1,
// and the receive command handles none (of this binary's verbs only a
// foreground service does, stopSignals), so a signal that ends either one
// leaves what it started running.
func spawnerMain() {
	dir := os.Getenv(spawnerVar)
	if dir == "" || len(os.Args) != 1 {
		return
	}
	released := make(chan os.Signal, 1)
	signal.Notify(released, syscall.SIGUSR1)
	if err := os.WriteFile(filepath.Join(dir, parkedFIFO), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "the spawner cannot report its process identifier:", err)
		os.Exit(2)
	}
	<-released
	child := procgroup.Command("/bin/sh", "-c", "echo $$ >"+shellWord(filepath.Join(dir, childFIFO))+"; exec sleep 600")
	if err := procgroup.Run(context.Background(), child, time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "the spawner's child:", err)
	}
	os.Exit(1)
}

// spawnerAt parks the spawner in place of the git the push's processes run
// at the first invocation carrying arg (gitWrapperAt). It returns the
// directory to give git push as GIT_EXEC_PATH, the channel that delivers the
// spawner's identity once it can be released, and release, which releases
// it and returns the identity of the child it then starts, which the test
// ends when it is over whatever giving up did.
func spawnerAt(t *testing.T, arg string) (string, <-chan agents.Identity, func(spawner agents.Identity) agents.Identity) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	dir, spawned := gitWrapperAt(t, arg, func(dir string) string {
		return spawnerVar + "=" + shellWord(dir) + "\n\t\texport " + spawnerVar + "\n\t\texec " + shellWord(self)
	})
	children := reportedOn(t, filepath.Join(dir, childFIFO))
	release := func(spawner agents.Identity) agents.Identity {
		t.Helper()
		if presence, err := spawner.Presence(); err != nil || presence != agents.PresencePresent {
			t.Fatalf("the spawner, process %d, is not running to be released (%v, %v)", spawner.PID, presence, err)
		}
		if err := syscall.Kill(spawner.PID, syscall.SIGUSR1); err != nil {
			t.Fatalf("releasing the spawner, process %d: %v", spawner.PID, err)
		}
		select {
		case child := <-children:
			t.Cleanup(func() { standin.Process{Identity: child}.End() })
			return child
		case <-time.After(30 * time.Second):
			t.Fatalf("the spawner, process %d, was released and its child never reported", spawner.PID)
		}
		return agents.Identity{}
	}
	return dir, spawned, release
}

// startBystander starts a process of this user's that no push started, which
// every reading lists and none attributes to a push, and ends it when the
// test is over.
func startBystander(t *testing.T) agents.Identity {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the bystander: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("identifying the bystander, process %d (found %t): %v", cmd.Process.Pid, found, err)
	}
	return id
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
		t.Fatalf("the receive command's rev-parse never parked:\n%s", push.out.String())
	}
	t.Cleanup(func() { standin.Process{Identity: stalled}.End() })
	seesRunning(t, push, "the parked rev-parse", isProcess(stalled))
	gave := push.giveUp()
	said := gave.said
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
	if gave.notEnded != nil {
		t.Fatalf("the push was given up on and is not shown ended: %v\n%s", gave.notEnded, said)
	}
}

// seesRunning is the positive control for the check that giving up left
// nothing running: one reading of the push made while it is held, as giving
// up makes them, records the process match picks, and the verdict on that
// record (pushRecord.verdict) names it as still running. A reading that
// could not see the process would pass that check whether or not the process
// outlived the push. It returns the identity of the process match picked.
func seesRunning(t *testing.T, push *boundedPush, what string, match func(standin.Process) bool) agents.Identity {
	t.Helper()
	rec := newPushRecord(kernelTable, push)
	rec.read("the reading of the held push")
	i := slices.IndexFunc(rec.held(), match)
	if len(rec.failed) > 0 || i < 0 {
		t.Fatalf("%s is running and is not among what the push started, %v (%v), so the check that giving up "+
			"ended it would pass without seeing it", what, rec.held(), errors.Join(rec.failed...))
	}
	id := rec.processes[i].Identity
	var left *notEnded
	if verdict := rec.verdict(); !errors.As(verdict, &left) || !slices.ContainsFunc(left.running, isProcess(id)) {
		t.Fatalf("the verdict on what the push started, made while %s, process %d, runs, is %v; want it named",
			what, id.PID, verdict)
	}
	return id
}

// readGone is nil only when id was read as naming no running process, and
// otherwise says why not: it still runs, or whether it does could not be
// read. A check that a process has been ended asks it, so that a failed read
// is never taken for a process found gone.
func readGone(id agents.Identity) error {
	presence, err := id.Presence()
	switch {
	case err != nil:
		return fmt.Errorf("whether process %d still runs could not be read: %w", id.PID, err)
	case presence == agents.PresencePresent:
		return fmt.Errorf("process %d still runs", id.PID)
	}
	return nil
}

// isProcess picks the process id names.
func isProcess(id agents.Identity) func(standin.Process) bool {
	return func(p standin.Process) bool { return p.Identity == id }
}

// A push whose cleanup cannot be read afterwards is not one giving up
// ended. The receive command's rev-parse is parked and kept out of every
// reading, so it outlives the cleanup, and every reading once git push has
// ended fails, as one that cannot settle does. Giving up must then report
// the push as not shown ended, naming the failed reading. Then the verdict
// (pushRecord.verdict) one read at a time, on records that hold the
// survivor's group, as a reading made while the receive command ran would
// have: a failed listing after a reading that found the survivor forgets
// nothing and names both, and a failed read of where a process is, or of
// whether a recorded process still runs, is each the verdict's failure. The
// positive control is a record of one clean reading, whose verdict names the
// survivor while it runs and passes once it is gone: the verdict passes a
// clean record and nothing else.
func TestAPushWhoseCleanupCannotBeReadIsNotReportedEnded(t *testing.T) {
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
	var survivor agents.Identity
	select {
	case survivor = <-parked:
	case <-push.done:
		t.Fatalf("the push ended (%v) without reaching the parked rev-parse:\n%s", push.err, push.out.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("the receive command's rev-parse never parked:\n%s", push.out.String())
	}
	kept := standin.Process{Identity: survivor}
	t.Cleanup(func() { kept.End() })
	unsettled := errors.New("the process table did not settle (injected)")
	hiding := table{lineage: agents.ReadLineage, list: func(match func([]string) bool) ([]standin.Process, error) {
		select {
		case <-push.done:
			return nil, unsettled
		default:
		}
		all, err := standin.ProcessesMatching(match)
		return slices.DeleteFunc(all, func(p standin.Process) bool { return p.Identity == survivor }), err
	}}
	unlisted := table{lineage: agents.ReadLineage,
		list: func(func([]string) bool) ([]standin.Process, error) { return nil, unsettled }}
	unplaced := table{list: standin.ProcessesMatching,
		lineage: func(int) (agents.Lineage, bool, error) { return agents.Lineage{}, false, unsettled }}

	gave := push.giveUpReading(hiding)
	if presence, err := kept.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the hidden rev-parse, process %d, did not outlive the cleanup (%v, %v), so this shows nothing",
			survivor.PID, presence, err)
	}
	if gave.notEnded == nil || !errors.Is(gave.notEnded, unsettled) {
		t.Fatalf("giving up could not read what was left of the push, and process %d still runs, yet its verdict is "+
			"%v:\n%s", survivor.PID, gave.notEnded, gave.said)
	}

	// The survivor leads a group of its own, since the receive command started
	// it through procgroup.Run, so a record holding that group attributes it
	// whatever its parent now is.
	survivorGroup := func(reads table) *pushRecord {
		rec := newPushRecord(reads, push)
		rec.groups[survivor.PID] = true
		return rec
	}
	var left *notEnded
	clean := survivorGroup(kernelTable)
	clean.read("a reading that finds the survivor")
	if verdict := clean.verdict(); len(clean.failed) > 0 || !errors.As(verdict, &left) || len(left.failed) > 0 ||
		!slices.ContainsFunc(left.running, isProcess(survivor)) {
		t.Fatalf("a clean reading made while process %d runs gives the verdict %v; want it named and nothing else",
			survivor.PID, verdict)
	}
	forgetting := survivorGroup(kernelTable)
	forgetting.read("a reading that finds the survivor")
	forgetting.reads = unlisted
	forgetting.read("a later reading whose listing fails")
	if verdict := forgetting.verdict(); !errors.Is(verdict, unsettled) || !errors.As(verdict, &left) ||
		!slices.ContainsFunc(left.running, isProcess(survivor)) {
		t.Fatalf("a failed listing after a reading that found process %d gives the verdict %v; want the failure "+
			"and the process both", survivor.PID, verdict)
	}
	unplacing := survivorGroup(unplaced)
	unplacing.read("a reading that cannot place what it lists")
	if verdict := unplacing.verdict(); !errors.Is(verdict, unsettled) {
		t.Fatalf("a reading that could not place the processes it listed gives the verdict %v; want that failure", verdict)
	}
	// An identity with no start time is one agents.Identity.Presence refuses
	// to read, which is how a presence read fails here without a fake.
	unreadable := newPushRecord(kernelTable, push)
	unreadable.processes = append(unreadable.processes, recorded{Process: standin.Process{Identity: agents.Identity{PID: survivor.PID}}})
	if verdict := unreadable.verdict(); !errors.Is(verdict, agents.ErrIdentityUnreadable) {
		t.Fatalf("a record holding a process whose presence cannot be read gives the verdict %v; want that failure", verdict)
	}
	kept.End()
	if verdict := clean.verdict(); verdict != nil {
		t.Fatalf("the clean record's verdict once process %d was ended is %v; want nothing left", survivor.PID, verdict)
	}
}

// A push given up on keeps what each reading of it found, whatever else that
// reading answered, and is not shown ended when any reading failed. The
// spawner is parked in place of the receive command's rev-parse, and
// released right after the first listing, so the child it then starts in a
// group of its own is born between the first reading and the second, under a
// parent still running at the second. Only the second reading can tie that
// child to the push: once giving up has ended the spawner, the child has
// another parent and a group no reading recorded. A bystander, a process this
// test started that every reading lists and none attributes to the push,
// stands for another listed process whose read can fail. Each case fails one
// read, once:
//
//   - none, the positive control: the second reading finds the child, giving
//     up ends it, and the verdict passes, so a failing verdict below is the
//     injected failure's and a child left running is not this setup's;
//   - the first listing: the second reading still finds everything, and the
//     verdict fails on the failed reading;
//   - where the bystander is, at the second reading (review round 3): that
//     reading also finds the child, and giving up must end it rather than drop
//     what the reading found beside its failure;
//   - the second listing: no reading finds the child, which outlives giving
//     up, so the failed reading is all that keeps the verdict from passing;
//   - the listing after the end (review round 2): the child is ended, and the
//     verdict still fails, since the reading after the end read nothing.
func TestAPushGivenUpOnKeepsWhatEachReadingFound(t *testing.T) {
	t.Parallel()
	injected := errors.New("the reading failed here (injected)")
	for _, c := range []struct {
		name string
		// reading is the reading, counted from one, whose read fails, and
		// zero for none; list fails its listing, and otherwise its read of
		// where the bystander is.
		reading int
		list    bool
		// childLeft is that the child outlives giving up.
		childLeft bool
	}{
		{name: "no reading fails"},
		{name: "the first listing fails", reading: 1, list: true},
		{name: "the second reading cannot place the bystander", reading: 2},
		{name: "the second listing fails", reading: 2, list: true, childLeft: true},
		{name: "the listing after the end fails", reading: 3, list: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHome(t)
			subject := newSubject(t)
			initializeGate(t, h, subject)
			git(t, subject, "checkout", "--quiet", "-b", "work")
			commitOn(t, subject, "work", "work.txt", "a change\n")
			bystander := startBystander(t)

			execPath, spawned, release := spawnerAt(t, "--is-bare-repository")
			cmd := gitCmd(context.Background(), subject, "push", gate.RemoteName, "work")
			cmd.Env = append(cmd.Env, "GIT_EXEC_PATH="+execPath)
			push := startBoundedPush(t, h, "", cmd)
			var spawner agents.Identity
			select {
			case spawner = <-spawned:
			case <-push.done:
				t.Fatalf("the push ended (%v) without reaching the spawner:\n%s", push.err, push.out.String())
			case <-time.After(30 * time.Second):
				t.Fatalf("the receive command's rev-parse never became the spawner:\n%s", push.out.String())
			}

			var child agents.Identity
			readings, failedAt := 0, 0
			reads := table{
				list: func(match func([]string) bool) ([]standin.Process, error) {
					readings++
					listed, err := standin.ProcessesMatching(match)
					if readings == c.reading && c.list {
						listed, err, failedAt = nil, injected, readings
					}
					if readings == 1 {
						child = release(spawner)
					}
					return listed, err
				},
				lineage: func(pid int) (agents.Lineage, bool, error) {
					if readings == c.reading && !c.list && pid == bystander.PID {
						failedAt = readings
						return agents.Lineage{}, false, injected
					}
					return agents.ReadLineage(pid)
				},
			}
			gave := push.giveUpReading(reads)
			if failedAt != c.reading {
				t.Fatalf("the read meant to fail at reading %d failed at %d of %d, so this case shows nothing:\n%s",
					c.reading, failedAt, readings, gave.said)
			}
			presence, err := child.Presence()
			if err != nil {
				t.Fatalf("reading whether the spawner's child, process %d, still runs: %v", child.PID, err)
			}
			switch running := presence == agents.PresencePresent; {
			case running && !c.childLeft:
				t.Fatalf("giving up left the spawner's child, process %d, running:\n%s", child.PID, gave.said)
			case !running && c.childLeft:
				t.Fatalf("no reading could tie the spawner's child, process %d, to the push, yet it is gone, so this "+
					"case does not show what the verdict must do without it:\n%s", child.PID, gave.said)
			}
			// The second reading names the child, under the spawner, exactly when
			// it could place the spawner, which every case but the failed second
			// listing lets it.
			named := fmt.Sprintf("  process %d (parent %d, ", child.PID, spawner.PID)
			if found := strings.Contains(gave.said, "Found after the signals") && strings.Contains(gave.said, named); found == c.childLeft {
				t.Fatalf("whether giving up found the spawner's child, process %d, was %t, and it was left running: %t:\n%s",
					child.PID, found, c.childLeft, gave.said)
			}
			var left *notEnded
			switch {
			case c.reading == 0 && gave.notEnded != nil:
				t.Fatalf("every reading succeeded and the push is not shown ended: %v\n%s", gave.notEnded, gave.said)
			case c.reading != 0 && !errors.Is(gave.notEnded, injected):
				t.Fatalf("reading %d failed, and the verdict is %v rather than that failure:\n%s",
					c.reading, gave.notEnded, gave.said)
			case c.childLeft && errors.As(gave.notEnded, &left) && slices.ContainsFunc(left.running, isProcess(child)):
				t.Fatalf("the verdict names the spawner's child, process %d, so it does not show that the failed "+
					"reading alone fails it: %v", child.PID, gave.notEnded)
			}
		})
	}
}

// A reading of a push counts as the push's every process its record already
// holds, and so every process under one, however the kernel places that
// process now: what one reading attributed, a later one never sets aside. The
// process held here is a shell this test started, with a sleep it started in
// turn, so nothing but the record ties either to a push. The positive
// control is a record that does not hold the shell, whose reading attributes
// neither.
func TestAReadingOfAPushCountsEveryProcessItsRecordHolds(t *testing.T) {
	t.Parallel()
	shell := exec.Command("/bin/sh", "-c", "sleep 600 >/dev/null & echo $!; wait")
	stdout, err := shell.StdoutPipe()
	if err != nil {
		t.Fatalf("piping the shell's output: %v", err)
	}
	if err := shell.Start(); err != nil {
		t.Fatalf("starting the shell: %v", err)
	}
	var child agents.Identity
	t.Cleanup(func() {
		if child.PID != 0 {
			standin.Process{Identity: child}.End()
		}
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the shell's child's process identifier: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("the shell wrote %q as its child's process identifier: %v", line, err)
	}
	identify := func(pid int, what string) agents.Identity {
		id, found, err := agents.Identify(pid)
		if err != nil || !found {
			t.Fatalf("identifying %s, process %d (found %t): %v", what, pid, found, err)
		}
		return id
	}
	held := identify(shell.Process.Pid, "the shell")
	child = identify(pid, "the shell's child")

	without := &pushRecord{reads: kernelTable, groups: map[int]bool{}}
	without.read("a reading with nothing recorded")
	if len(without.failed) > 0 || without.holds(held) || without.holds(child) {
		t.Fatalf("a record holding nothing attributed %v (%v), so the shell is tied to a push by something other "+
			"than the record", without.held(), errors.Join(without.failed...))
	}
	with := &pushRecord{reads: kernelTable, groups: map[int]bool{},
		processes: []recorded{{Process: standin.Process{Identity: held}}}}
	with.read("a reading with the shell recorded")
	if len(with.failed) > 0 || !with.holds(child) {
		t.Fatalf("a record holding the shell, process %d, did not count its child, process %d, as the push's: "+
			"%v (%v)", held.PID, child.PID, with.held(), errors.Join(with.failed...))
	}
}

// A push that ends while a process it started runs on has that process ended
// with the test that pushed (review round 4): waiting for git push is no
// reason to skip giving up. The subject's pre-push hook starts a sleep in
// the background and refuses the push, so git push ends with the sleep still
// running, in git push's group, holding git's output, the case the review
// reproduced, or with its output sent elsewhere. The push runs in a subtest,
// so its cleanup has run when the subtest returns; the positive control is
// that the sleep is running once git push has ended, inside it.
func TestAPushThatEndsLeavingAProcessRunningHasItEndedWithItsTest(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, redirect string }{
		{name: "holding git's output"},
		{name: "with its output elsewhere", redirect: " >/dev/null 2>&1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var left agents.Identity
			t.Cleanup(func() {
				if left.PID != 0 {
					standin.Process{Identity: left}.End()
				}
			})
			passed := t.Run("push", func(t *testing.T) {
				h := newHome(t)
				subject := newSubject(t)
				initializeGate(t, h, subject)
				git(t, subject, "checkout", "--quiet", "-b", "work")
				commitOn(t, subject, "work", "work.txt", "a change\n")
				fifo := filepath.Join(t.TempDir(), "left")
				reported := reportedOn(t, fifo)
				prePushLeaving(t, subject, "sleep 600"+c.redirect+" &\necho $! >"+shellWord(fifo))
				push := startBoundedPush(t, h, "", gitCmd(context.Background(), subject, "push", gate.RemoteName, "work"))
				select {
				case left = <-reported:
				case <-time.After(30 * time.Second):
					t.Fatalf("the pre-push hook never reported the process it left:\n%s", push.out.String())
				}
				select {
				case <-push.done:
				case <-time.After(30 * time.Second):
					t.Fatalf("git push did not end once its pre-push hook refused the push:\n%s", push.out.String())
				}
				if presence, err := left.Presence(); err != nil || presence != agents.PresencePresent {
					t.Fatalf("the pre-push hook's sleep, process %d, is not running once git push ended (%v, %v), so "+
						"this shows nothing", left.PID, presence, err)
				}
				t.Logf("git push answered %v", push.err)
			})
			if !passed {
				t.Fatalf("the push's own test failed")
			}
			if err := readGone(left); err != nil {
				t.Fatalf("the pre-push hook's sleep, after the test that pushed ended: %v", err)
			}
		})
	}
}

// within, given up on at its bound, returns the push's output as it stands
// once giving up is over (review round 4): the receive command's stacks,
// which SIGQUIT has it write while giving up runs, are in it.
func TestAPushStillRunningAtItsBoundReturnsItsOutputAfterGivingUp(t *testing.T) {
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
		t.Fatalf("the push's receive command never said it was waiting:\n%s", push.out.String())
	}
	out, err := push.within(time.Millisecond)
	if err == nil {
		t.Fatalf("a push held on the copies lock ended within its bound:\n%s", out)
	}
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "gate.AwaitCopies") {
		t.Fatalf("the output within returned lacks the receive command's stacks, which name its wait for the "+
			"copies lock:\n%s\nthe push's output now:\n%s", out, push.out.String())
	}
}

// leaverVar, set in the environment of this binary started with no argument,
// makes that process the leaver (leaverMain), with the FIFO its child
// reports on as its value.
const leaverVar = "ASSISTANT_CLI_TEST_LEAVER"

// leaverMain makes this process the leaver when it was started as one, and
// returns otherwise. The leaver starts a shell in a process group of its own,
// with the leaver's standard output and standard error, that writes its
// process identifier to the FIFO leaverVar names and becomes a sleep, and
// exits without waiting for it. Run from a push's hook, that child is a
// process the push started that left git push's group and whose parent is
// gone before any reading, so no reading finds it (pushRecord's gap), and
// that holds the push's output, which is how giving up still sees it.
func leaverMain() {
	fifo := os.Getenv(leaverVar)
	if fifo == "" || len(os.Args) != 1 {
		return
	}
	child := exec.Command("/bin/sh", "-c", "echo $$ >"+shellWord(fifo)+"; exec sleep 600")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "the leaver's child:", err)
		os.Exit(2)
	}
	os.Exit(0)
}

// prePushLeaving makes subject's pre-push hook run body and then refuse the
// push, so git push ends having started whatever body leaves running. body
// is shell text, and reports the identifier of what it leaves on fifo.
func prePushLeaving(t *testing.T, subject, body string) {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\nexit 1\n"
	if err := procgroup.WriteProgram(filepath.Join(subject, ".git", "hooks", "pre-push"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the pre-push hook: %v", err)
	}
}

// A push whose output a process holds that no reading finds is not shown
// ended (review round 4). The pre-push hook runs the leaver, whose child
// left git push's group and lost its parent before any reading, so giving
// up, once git push has ended, finds none of it; the child holds git's
// output, so the output never reaches its end, and the verdict says so
// rather than passing. The positive control is the same check once the child
// is ended: the output then reaches its end and the verdict passes.
func TestAPushWhoseOutputAProcessNoReadingFindsHoldsIsNotShownEnded(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	initializeGate(t, h, subject)
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "left")
	reported := reportedOn(t, fifo)
	prePushLeaving(t, subject, leaverVar+"="+shellWord(fifo)+"\nexport "+leaverVar+"\n"+shellWord(self))

	push := startBoundedPush(t, h, "", gitCmd(context.Background(), subject, "push", gate.RemoteName, "work"))
	var left agents.Identity
	select {
	case left = <-reported:
	case <-time.After(30 * time.Second):
		t.Fatalf("the leaver's child never reported:\n%s", push.out.String())
	}
	kept := standin.Process{Identity: left}
	t.Cleanup(func() { kept.End() })
	select {
	case <-push.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("git push did not end once its pre-push hook refused the push:\n%s", push.out.String())
	}

	gave := push.giveUp()
	if presence, err := left.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the leaver's child, process %d, is not running after giving up (%v, %v), so this shows nothing:\n%s",
			left.PID, presence, err, gave.said)
	}
	var verdict *notEnded
	if !errors.As(gave.notEnded, &verdict) || !errors.Is(gave.notEnded, errOutputHeld) {
		t.Fatalf("a process holding the push's output still runs, and the verdict is %v:\n%s", gave.notEnded, gave.said)
	}
	if slices.ContainsFunc(verdict.running, isProcess(left)) {
		t.Fatalf("giving up found the leaver's child, process %d, so this does not show the output's end is what "+
			"fails the verdict:\n%s", left.PID, gave.said)
	}
	kept.End()
	if gave := push.giveUp(); gave.notEnded != nil {
		t.Fatalf("once nothing held the push's output, giving up still answered %v:\n%s", gave.notEnded, gave.said)
	}
}

// within, once git push has ended with something it started still holding
// its output, gives the push up rather than answering as if it had ended
// (review round 4): the pre-push hook's sleep holds git's output, within
// names it in its error, and it is gone once within has answered.
func TestAPushWhoseOutputOutlivesGitIsGivenUpOnByWithin(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	initializeGate(t, h, subject)
	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "a change\n")
	fifo := filepath.Join(t.TempDir(), "left")
	reported := reportedOn(t, fifo)
	prePushLeaving(t, subject, "sleep 600 &\necho $! >"+shellWord(fifo))

	push := startBoundedPush(t, h, "", gitCmd(context.Background(), subject, "push", gate.RemoteName, "work"))
	var left agents.Identity
	select {
	case left = <-reported:
	case <-time.After(30 * time.Second):
		t.Fatalf("the pre-push hook never reported the process it left:\n%s", push.out.String())
	}
	t.Cleanup(func() { standin.Process{Identity: left}.End() })
	out, err := push.within(30 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "still held its output") {
		t.Fatalf("git push ended with its pre-push hook's sleep holding its output, and within answered %v:\n%s", err, out)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("  process %d (parent ", left.PID)) {
		t.Fatalf("within's answer does not name the sleep, process %d, that held the output:\n%v", left.PID, err)
	}
	if err := readGone(left); err != nil {
		t.Fatalf("the sleep, once within has given the push up: %v", err)
	}
}

// A push still running when its test ends is given up on then, and what it
// started ends with the test (review round 4). The push runs in a subtest
// that returns while the receive command's rev-parse is parked; the positive
// control is that the verdict sees the parked rev-parse running inside the
// subtest (seesRunning).
func TestAPushStillRunningWhenItsTestEndsIsEndedWithIt(t *testing.T) {
	t.Parallel()
	var held agents.Identity
	t.Cleanup(func() {
		if held.PID != 0 {
			standin.Process{Identity: held}.End()
		}
	})
	if !t.Run("push", func(t *testing.T) {
		h := newHome(t)
		subject := newSubject(t)
		initializeGate(t, h, subject)
		git(t, subject, "checkout", "--quiet", "-b", "work")
		commitOn(t, subject, "work", "work.txt", "a change\n")
		execPath, parked := parkGitAt(t, "--is-bare-repository")
		cmd := gitCmd(context.Background(), subject, "push", gate.RemoteName, "work")
		cmd.Env = append(cmd.Env, "GIT_EXEC_PATH="+execPath)
		push := startBoundedPush(t, h, "", cmd)
		select {
		case held = <-parked:
		case <-push.done:
			t.Fatalf("the push ended (%v) without reaching the parked rev-parse:\n%s", push.err, push.out.String())
		case <-time.After(30 * time.Second):
			t.Fatalf("the receive command's rev-parse never parked:\n%s", push.out.String())
		}
		seesRunning(t, push, "the parked rev-parse", isProcess(held))
	}) {
		t.Fatalf("the push's own test failed")
	}
	if err := readGone(held); err != nil {
		t.Fatalf("the parked rev-parse, after the test that pushed ended: %v", err)
	}
}

// errorsOf is a testing.TB that records the errors a test would fail with,
// and passes everything else to the test it wraps.
type errorsOf struct {
	testing.TB
	errors []string
}

func (e *errorsOf) Errorf(format string, args ...any) {
	e.errors = append(e.errors, fmt.Sprintf(format, args...))
}

func (e *errorsOf) Helper() {}

// endWithTest fails the test whose push a pass of giving up did not show
// ended, and leaves alone one whose push it did (review round 4). The
// receive command's rev-parse is parked, and a pass that can list nothing
// gives the verdict that fails the test. The positive control is the pass
// after it, which reads: it finds the receive command in git push's group and
// the rev-parse under it, ends both, and fails nothing.
func TestTheEndOfAPushsTestFailsItOnlyWhenThePushIsNotShownEnded(t *testing.T) {
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
		t.Fatalf("the receive command's rev-parse never parked:\n%s", push.out.String())
	}
	t.Cleanup(func() { standin.Process{Identity: stalled}.End() })
	unlisted := table{lineage: agents.ReadLineage, list: func(func([]string) bool) ([]standin.Process, error) {
		return nil, errors.New("the process table did not settle (injected)")
	}}

	failing := &errorsOf{TB: t}
	push.endWithTest(failing, push.giveUpReading(unlisted))
	if len(failing.errors) != 1 {
		t.Fatalf("a pass that could list nothing failed the test %d times, want once: %q", len(failing.errors), failing.errors)
	}
	passing := &errorsOf{TB: t}
	push.endWithTest(passing, push.giveUp())
	if len(passing.errors) != 0 {
		t.Fatalf("a pass that read the push and ended it failed the test: %q", passing.errors)
	}
	if err := readGone(stalled); err != nil {
		t.Fatalf("the parked rev-parse, after the second pass: %v", err)
	}
}

// readGone, which the checks here that a process was ended ask, is nil only
// for a process read as gone (review round 5): a running process and one
// whose presence cannot be read each fail it. The positive control is the
// same process once it has been ended.
func TestReadGoneIsNilOnlyForAProcessReadAsGone(t *testing.T) {
	t.Parallel()
	running := startBystander(t)
	if err := readGone(running); err == nil {
		t.Fatalf("readGone passed process %d while it runs", running.PID)
	}
	// An identity with no start time is one agents.Identity.Presence refuses
	// to read, which is how a presence read fails here without a fake.
	if err := readGone(agents.Identity{PID: running.PID}); !errors.Is(err, agents.ErrIdentityUnreadable) {
		t.Fatalf("readGone answered %v for a process whose presence cannot be read; want that failure", err)
	}
	standin.Process{Identity: running}.End()
	if err := readGone(running); err != nil {
		t.Fatalf("readGone failed process %d once it was ended: %v", running.PID, err)
	}
}
