package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// lsofPath is the lsof this package runs. It is named by absolute path, so
// the search path of whoever started this service decides nothing about
// which program reports the working directories a sweep ends processes by.
const lsofPath = "/usr/sbin/lsof"

// errLsofShape is the refusal of lsof output this listing cannot read
// fields out of. It fails the listing, so the sweep makes no answer at all
// rather than one read short.
var errLsofShape = errors.New("agents: lsof field output has an unexpected shape")

// errLsofNoSelf is the refusal of an lsof run whose output does not report
// this process's own working directory, which it can always be asked for.
// Such a run is not taken to have listed anything.
var errLsofNoSelf = errors.New("agents: lsof did not report this process's own working directory")

// listUnder lists the processes whose working directory is dir or below it,
// with the identity of each. dir is a resolved, cleaned path.
//
// The processes come from the kernel's process table (kern.proc.all), read
// once. Each one other than this process and identifier zero is identified
// through Identify; then the working directories are read for all of them
// at once by one run of lsof; then each process is taken on addRead's terms
// when lsof reported a working directory for it and on addRefused's terms
// when it did not. lsof reporting nothing for a process is not read as a
// reason: whether it was refused, could not be read, or exited, the
// process goes through the same classification, and one that exited is
// skipped there. A failure to read the process table, any failure of the
// lsof run readCwds refuses, and ctx ending fail the listing.
//
// Identifier zero is left out because a signal to it would be a signal to
// this process's own group, which the sweep never sends.
//
// dir is refused before anything is read when it holds a byte outside
// printable ASCII, a space or a backslash: lsof's field output may render
// such a byte in a name other than as itself, and a copy whose name could
// not be compared would be a sweep that found nothing under it.
func listUnder(ctx context.Context, dir string) (listing, error) {
	return listUnderReading(ctx, dir, systemLsof.readCwds)
}

// listUnderReading is listUnder with the working directories read by
// readCwds, so a test can leave out a process the real reading reports,
// which is what the real reading does for a process it cannot read, in front
// of every other real read the listing makes.
func listUnderReading(ctx context.Context, dir string, readCwds func(context.Context) (map[int]string, error)) (listing, error) {
	if err := lsofComparable(dir); err != nil {
		return listing{}, err
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return listing{}, fmt.Errorf("agents: reading the process table: %w", err)
	}
	self := os.Getpid()
	me := currentViewer()
	var found listing
	var listed []Identity
	for _, proc := range procs {
		if err := ctx.Err(); err != nil {
			return listing{}, err
		}
		pid := int(proc.Proc.P_pid)
		if pid <= 0 || pid == self {
			continue
		}
		id, present, err := Identify(pid)
		switch {
		case err != nil:
			found.unverified = append(found.unverified, pid)
		case present:
			listed = append(listed, id)
		}
	}
	cwds, err := readCwds(ctx)
	if err != nil {
		return listing{}, err
	}
	for _, before := range listed {
		if err := ctx.Err(); err != nil {
			return listing{}, err
		}
		if cwd, reported := cwds[before.PID]; reported {
			found.addRead(before, cwd, dir)
		} else {
			found.addRefused(before, me)
		}
	}
	return found, nil
}

// lsofComparable refuses a dir whose name lsof's output could carry other
// than byte for byte, on listUnder's terms.
func lsofComparable(dir string) error {
	for i := 0; i < len(dir); i++ {
		if b := dir[i]; b <= ' ' || b >= 0x7f || b == '\\' {
			return fmt.Errorf("agents: %q holds byte %#x, which this listing does not compare against lsof's output", dir, b)
		}
	}
	return nil
}

// lsofRun is the lsof run readCwds makes: the program run and the deadline
// it runs under. systemLsof is the one the listing makes; a test names
// another program through the same run.
type lsofRun struct {
	program  string
	deadline time.Duration
}

// systemLsof is the run listUnder makes.
var systemLsof = lsofRun{program: lsofPath, deadline: lsofDeadline}

// lsofDeadline bounds one whole lsof run, on run's terms: starting it,
// waiting on it, ending its group and collecting its output all fall inside
// it. It is a fixed bound of this package rather than a configuration key,
// because it bounds a reading helper no operator decision turns on, and a
// key would be a row in internal/config's table for nothing to set. Its size
// is the one terminalCommandTimeout gives a tmux client: a run over every
// process of a development machine took under a second when this was
// written, so a run still going at nine is taken as wedged.
const lsofDeadline = terminalCommandTimeout

// lsofCleanupShare is the part of a run's deadline kept back for ending its
// group and collecting its output: lsof itself is given the deadline less
// one lsofCleanupShare-th of it, and is killed when that passes.
const lsofCleanupShare = 10

// readCwds runs lsof once over every process, selecting only working
// directory descriptors and asking for the process, descriptor and name
// fields, and returns the working directory it reported for each process.
//
// lsof runs with an empty environment, so nothing of the operator's reaches
// it. Its exit status is accepted as zero or one and nothing else, because a
// status of one is also how it ends a run in which some process could not be
// read, and what decides about each process is whether a working directory
// was reported for it, not the status. A run that ended any other way, whose
// output parseLsof refuses, or whose output does not report this process's
// own working directory is a failed listing.
func (l lsofRun) readCwds(ctx context.Context) (map[int]string, error) {
	out, err := l.run(ctx)
	if err != nil {
		return nil, err
	}
	cwds, err := parseLsof(out)
	if err != nil {
		return nil, err
	}
	if _, ok := cwds[os.Getpid()]; !ok {
		return nil, errLsofNoSelf
	}
	return cwds, nil
}

// run is readCwds' run of lsof, and returns its standard output when it
// exited with a status readCwds accepts.
//
// The run carries l.deadline whatever ctx carries, because the sweep's
// callers may hand it a context that never ends: internal/service's
// sweepFinished runs under context.WithoutCancel. lsof is started as the
// leader of a process group of its own, and the group is signalled only
// while that leader is unreaped, so its identifier cannot have been given
// to another process and still names this group. That is mechanism rather
// than timing: nothing reaps the leader until cmd.Wait, which is called
// only after the last signal, and the wait for its exit is a kqueue's
// NOTE_EXIT, which leaves it unreaped. When lsof has not exited by its share
// of the deadline, or ctx ends first, the group is sent SIGKILL, so a
// leader that ignores SIGTERM and a descendant that stayed in its group are
// both ended. Once the leader has exited, by either path, the group is sent
// SIGKILL again, which ends a descendant a leader that exited on its own
// left in it. Then the leader is reaped, with WaitDelay set to what is left
// of the deadline for output a descendant that left the group holds open,
// so the whole run returns within l.deadline. A deadline that passed is
// ErrSweepTimedOut and ctx ending is ctx's error, each in place of whatever
// the killed run printed or exited with.
//
// Residual gaps: a descendant that left the group is not ended, only stops
// being waited on; a leader that SIGKILL does not end before the deadline
// is left to a goroutine that reaps it once it exits, and the run still
// answers at the deadline; and anything else in this process that reaped
// every child, a wait on any process, would undo the unreaped guarantee,
// which nothing in this module does.
func (l lsofRun) run(ctx context.Context) ([]byte, error) {
	end := time.Now().Add(l.deadline)
	cut := time.NewTimer(l.deadline - l.deadline/lsofCleanupShare)
	defer cut.Stop()
	cmd := exec.Command(l.program, "-n", "-P", "-w", "-d", "cwd", "-F", "pfn")
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agents: running %s: %w", l.program, err)
	}
	pgid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- awaitExitUnreaped(pgid) }()
	var stopped error
	select {
	case err := <-exited:
		if err != nil {
			// Its exit cannot be observed, but the leader is still
			// unreaped, since only cmd.Wait reaps it, so the group is
			// still this run's to end before that reap.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			reapWithin(cmd, end)
			return nil, fmt.Errorf("agents: waiting on %s: %w", l.program, err)
		}
	case <-cut.C:
		stopped = fmt.Errorf("%w: %s ran past %s and its process group was ended", ErrSweepTimedOut, l.program, l.deadline-l.deadline/lsofCleanupShare)
	case <-ctx.Done():
		stopped = fmt.Errorf("agents: running %s: %w", l.program, ctx.Err())
	}
	if stopped != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(remaining(end)):
			go func() {
				<-exited
				_ = cmd.Wait()
			}()
			return nil, stopped
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	cmd.WaitDelay = remaining(end)
	err := cmd.Wait()
	if stopped != nil {
		return nil, stopped
	}
	out := stdout.Bytes()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("agents: running %s: %w (%s)", l.program, err, firstLine(stderr.String()))
		}
	}
	return out, nil
}

// awaitExitUnreaped blocks until the child pid has exited and returns
// without reaping it, so its identifier stays its own until cmd.Wait reaps
// it. It waits on a kqueue for the process's NOTE_EXIT, which reports an
// exit and reaps nothing, and then reads the process table, so an exit
// before the registration is not waited on for ever: a child that is a
// zombie, or that the registration could not find, has exited, since
// nothing but cmd.Wait reaps it.
func awaitExitUnreaped(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(kq) }()
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	// The registration is made without blocking, so the process table can
	// be read between it and the wait.
	changes, timeout := []unix.Kevent_t{change}, &unix.Timespec{}
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, changes, events, timeout)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESRCH):
			return nil
		case err != nil:
			return err
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0 && events[0].Data == int64(unix.ESRCH):
			return nil
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0:
			return unix.Errno(events[0].Data)
		case n == 1 && events[0].Fflags&unix.NOTE_EXIT != 0:
			return nil
		}
		if changes != nil {
			changes, timeout = nil, nil
			if _, running, err := kinfo(pid); err != nil {
				return err
			} else if !running {
				return nil
			}
		}
	}
}

// reapWithin reaps cmd's leader, waiting until end at most, and leaves the
// reap to a goroutine of its own when end passes first.
func reapWithin(cmd *exec.Cmd, end time.Time) {
	cmd.WaitDelay = remaining(end)
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	select {
	case <-reaped:
	case <-time.After(remaining(end)):
	}
}

// remaining is how long is left until end, never less than a millisecond,
// because a WaitDelay of zero is os/exec's way of asking for no bound.
func remaining(end time.Time) time.Duration {
	return max(time.Until(end), time.Millisecond)
}

// parseLsof reads lsof's field output for working directory descriptors: a
// p line opening each process, an f line naming each descriptor, and an n
// line carrying its name. A working directory is recorded only from an n
// line that follows an f line reading cwd within a p line's process, and
// only when it is an absolute path; a name that is not one is left out,
// which the listing then reads as a working directory it could not read.
//
// Anything else is refused with errLsofShape rather than skipped: a line of
// another field, an empty line, a process identifier that is not a positive
// number, a descriptor or a name before any process, a name before any
// descriptor, a descriptor other than cwd, and a second working directory
// for one process.
func parseLsof(out []byte) (map[int]string, error) {
	cwds := map[int]string{}
	pid, fd := 0, ""
	text := strings.TrimSuffix(string(out), "\n")
	if text == "" {
		return cwds, nil
	}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			return nil, fmt.Errorf("%w: an empty line", errLsofShape)
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("%w: process field %q", errLsofShape, value)
			}
			pid, fd = n, ""
		case 'f':
			if pid == 0 {
				return nil, fmt.Errorf("%w: descriptor %q before any process", errLsofShape, value)
			}
			if value != "cwd" {
				return nil, fmt.Errorf("%w: descriptor %q of process %d, only cwd was asked for", errLsofShape, value, pid)
			}
			fd = value
		case 'n':
			if pid == 0 || fd == "" {
				return nil, fmt.Errorf("%w: name %q before its process and descriptor", errLsofShape, value)
			}
			if _, twice := cwds[pid]; twice {
				return nil, fmt.Errorf("%w: a second working directory for process %d", errLsofShape, pid)
			}
			if strings.HasPrefix(value, "/") {
				cwds[pid] = value
			}
			fd = ""
		default:
			return nil, fmt.Errorf("%w: field %q", errLsofShape, line)
		}
	}
	return cwds, nil
}

// firstLine is the first line of s, for an error message.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// kinfo reads pid's entry in the kernel's process table. found is false when
// there is no entry, or when the entry is a process that has exited and not
// been reaped, which Identify also counts as not running.
func kinfo(pid int) (unix.KinfoProc, bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return unix.KinfoProc{}, false, err
	}
	if len(procs) == 0 || procs[0].Proc.P_stat == szomb {
		return unix.KinfoProc{}, false, nil
	}
	if got := int(procs[0].Proc.P_pid); got != pid {
		return unix.KinfoProc{}, false, fmt.Errorf("agents: the process table entry asked for %d names %d", pid, got)
	}
	return procs[0], true, nil
}

// procUids reads the user identifiers the process table entry carries for
// pid, as x/sys/unix lays it out: Eproc.Pcred.P_ruid, Eproc.Ucred.Uid and
// Eproc.Pcred.P_svuid. present is false when the entry is gone.
func procUids(pid int) ([]int, bool, error) {
	k, found, err := kinfo(pid)
	if err != nil || !found {
		return nil, found, err
	}
	return []int{int(k.Eproc.Pcred.P_ruid), int(k.Eproc.Ucred.Uid), int(k.Eproc.Pcred.P_svuid)}, true, nil
}

// parentPID reads the parent identifier the process table entry carries for
// pid, Eproc.Ppid. found is false when the entry is gone.
func parentPID(pid int) (int, bool, error) {
	k, found, err := kinfo(pid)
	if err != nil || !found {
		return 0, found, err
	}
	return int(k.Eproc.Ppid), true, nil
}

// commandName reads the command name the process table entry carries for
// the process id names, Proc.P_comm, out of the same entry as the start
// time it is checked against, so a name returned without an error came from
// an entry whose start time is id's. P_comm is a 17-byte array in
// x/sys/unix's Proc, so a name longer than its terminated capacity comes
// back cut short. It is for diagnosis only: nothing decides from it.
func commandName(id Identity) (string, error) {
	k, found, err := kinfo(id.PID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errNameGone
	}
	if startOf(k.Proc) != id.Start {
		return "", errNameReplaced
	}
	return unix.ByteSliceToString(k.Proc.P_comm[:]), nil
}
