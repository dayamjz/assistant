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
	"time"

	"golang.org/x/sys/unix"

	"github.com/dayamjz/assistant/internal/procgroup"
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
// printable ASCII or a backslash: lsof's field output was observed to print
// a tab or a newline in a name as a backslash escape, and may print a byte
// above ASCII as one, so a name holding such a byte or a backslash could not
// be compared byte for byte, and a copy whose name could not be compared
// would be a sweep that found nothing under it. A space is printable ASCII
// and is printed as itself, which a test sweeping a copy with one in its
// path holds against the real lsof run, so it is compared like any other
// byte: parseLsof takes a name as the whole of its line after the n (#415).
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
		if b := dir[i]; b < ' ' || b >= 0x7f || b == '\\' {
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
// sweepFinished runs under context.WithoutCancel. It runs through
// procgroup.Run, the one owner of a short-lived program's group, whose
// package comment states the mechanism and its gaps: lsof leads a process
// group of its own, the group is ended once lsof exits and, at once, when
// lsof's share of the deadline passes or ctx ends, so a leader that ignores
// SIGTERM and a descendant that stayed in its group are both ended. The
// share is l.deadline less one lsofCleanupShare-th of it, and the grace
// procgroup.Run is given is a third of that last part, because Run answers
// within three graces of its context ending, so the whole run returns
// within l.deadline.
//
// Output a descendant that left the group holds open is waited on for one
// grace once lsof has exited, and then the run answers without it. A
// deadline that passed, or output that was not complete within its grace, is
// ErrSweepTimedOut, and ctx having ended is ctx's error, each in place of
// whatever the run printed or exited with: output is never taken as whole
// past any of them. A ctx that ends while that output is waited on is
// answered when the grace ends rather than at once, since procgroup.Run
// does not read its context once lsof is reaped; that is within l.deadline
// all the same.
//
// Residual gaps are procgroup.Run's: a descendant that left the group is
// not ended, only stops being waited on, and a leader that SIGKILL does not
// end within the grace is left to a goroutine that reaps it once it exits,
// with the run answering ErrSweepTimedOut without it.
func (l lsofRun) run(ctx context.Context) ([]byte, error) {
	share := l.deadline - l.deadline/lsofCleanupShare
	grace := lsofGrace(l.deadline)
	bounded, cancel := context.WithTimeout(ctx, share)
	defer cancel()
	cmd := exec.Command(l.program, "-n", "-P", "-w", "-d", "cwd", "-F", "pfn")
	cmd.Env = []string{}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := procgroup.Run(bounded, cmd, grace)
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("agents: running %s: %w", l.program, ctx.Err())
	case bounded.Err() != nil:
		return nil, fmt.Errorf("%w: %s ran past %s and its process group was ended", ErrSweepTimedOut, l.program, share)
	case errors.Is(err, procgroup.ErrAbandoned), errors.Is(err, exec.ErrWaitDelay):
		return nil, fmt.Errorf("%w: the output of %s was not complete within %s and its process group was ended", ErrSweepTimedOut, l.program, l.deadline)
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

// lsofGrace is the grace a run under deadline gives procgroup.Run: a third
// of the part of the deadline kept back from lsof, since Run answers within
// three graces of its context ending. It is never less than a millisecond,
// because Run refuses a grace that bounds nothing.
func lsofGrace(deadline time.Duration) time.Duration {
	return max(deadline/lsofCleanupShare/3, time.Millisecond)
}

// parseLsof reads lsof's field output for working directory descriptors: a
// p line opening each process, an f line naming each descriptor, and an n
// line carrying its name. A field is one line, told apart by its first
// byte, and its value is the rest of the line, never a part of it split off
// at a space, so a name with a space in it is read whole. A working
// directory is recorded only from an n line that follows an f line reading
// cwd within a p line's process, and only when it is an absolute path; a
// name that is not one is left out, which the listing then reads as a
// working directory it could not read.
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
