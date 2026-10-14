package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// listUnder lists, from /proc, the processes whose working directory is dir
// or below it, with the identity of each. dir is a resolved, cleaned path.
//
// For each numbered entry other than this process, the identity is read,
// then the cwd link, then the identity again, and a process whose identity
// changed between the two reads is reported unverified rather than listed
// or skipped, because the link read may belong to either. An entry that
// vanished at any read is a process that exited, and is skipped. A link
// this process is refused permission to read is counted unreadable, and is
// then classified by readRefused and viewer.blocks: one that is not set
// aside there is listed as blocking. Any other failure to read an entry is
// unverified; a failure to read /proc itself, or ctx ending, fails the
// listing.
//
// The link's target is resolved through symbolic links when it can be, and
// compared as read when it cannot, which is a working directory that no
// longer exists.
func listUnder(ctx context.Context, dir string) (listing, error) {
	return listUnderReading(ctx, dir, readCwdLink)
}

// readCwdLink reads the cwd link of pid as /proc reports it.
func readCwdLink(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
}

// listUnderReading is listUnder with the cwd link read by readCwd, so a test
// can stand a refusal the kernel will not produce on demand in front of
// every other real read the listing makes.
func listUnderReading(ctx context.Context, dir string, readCwd func(pid int) (string, error)) (listing, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return listing{}, err
	}
	self := os.Getpid()
	me := currentViewer()
	var found listing
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return listing{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		before, present, err := Identify(pid)
		switch {
		case err != nil:
			found.unverified = append(found.unverified, pid)
			continue
		case !present:
			continue
		}
		cwd, err := readCwd(pid)
		if err != nil {
			switch {
			case gone(err):
			case errors.Is(err, os.ErrPermission):
				r, present, err := readRefused(before)
				switch {
				case !present && err == nil:
				case err != nil:
					found.unreadable++
					found.unverified = append(found.unverified, pid)
				default:
					found.unreadable++
					if me.blocks(r) {
						found.blocking = append(found.blocking, before)
					}
				}
			default:
				found.unverified = append(found.unverified, pid)
			}
			continue
		}
		after, present, err := Identify(pid)
		switch {
		case err != nil || (present && after != before):
			found.unverified = append(found.unverified, pid)
			continue
		case !present:
			continue
		}
		if within(resolvedCwd(cwd), dir) {
			found.under = append(found.under, before)
		}
	}
	return found, nil
}

// refused is what the listing read about a process whose working directory
// it was refused: the identity it was listed under, the user identifiers on
// the Uid line of its status entry, and the answer a zero signal to it got.
type refused struct {
	id     Identity
	uids   []int
	signal error
}

// readRefused reads what viewer.blocks decides from about the process id
// names: the Uid line of its status entry, then a zero signal, which
// delivers nothing, then its identity again. present is false, with no
// error, when the process exited during the reads. A status entry that
// cannot be read or parsed, a signal answered with anything but success,
// refusal or absence, and an identity that changed are errors, which the
// listing reports unverified, so each fails toward not clearing.
func readRefused(id Identity) (refused, bool, error) {
	uids, present, err := statusUids(id.PID)
	if err != nil || !present {
		return refused{}, present, err
	}
	signal := syscall.Kill(id.PID, 0)
	switch {
	case errors.Is(signal, syscall.ESRCH):
		return refused{}, false, nil
	case signal != nil && !errors.Is(signal, syscall.EPERM):
		return refused{}, false, fmt.Errorf("agents: probing process %d with a zero signal: %w", id.PID, signal)
	}
	after, present, err := Identify(id.PID)
	switch {
	case err != nil:
		return refused{}, false, err
	case !present:
		return refused{}, false, nil
	case after != id:
		return refused{}, false, fmt.Errorf("agents: process %d changed identity while it was being read", id.PID)
	}
	return refused{id: id, uids: uids, signal: signal}, true, nil
}

// statusUids reads the Uid line of /proc/<pid>/status: the real, effective,
// saved and filesystem user identifiers, in that order. present is false
// when the entry is gone. A line that is missing or does not carry exactly
// four numbers is refused with errStatShape rather than read short.
func statusUids(pid int) ([]int, bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		if gone(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return parseStatusUids(pid, string(raw))
}

// parseStatusUids is statusUids' parse of a status entry's text.
func parseStatusUids(pid int, text string) ([]int, bool, error) {
	for line := range strings.Lines(text) {
		rest, ok := strings.CutPrefix(line, "Uid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 4 {
			return nil, false, fmt.Errorf("%w: /proc/%d/status Uid line has %d fields, want 4", errStatShape, pid, len(fields))
		}
		uids := make([]int, 0, 4)
		for _, f := range fields {
			uid, err := strconv.Atoi(f)
			if err != nil {
				return nil, false, fmt.Errorf("%w: /proc/%d/status Uid field %q: %v", errStatShape, pid, f, err)
			}
			uids = append(uids, uid)
		}
		return uids, true, nil
	}
	return nil, false, fmt.Errorf("%w: /proc/%d/status has no Uid line", errStatShape, pid)
}

// viewer is this process as the classification of a refused process sees
// it: its real and effective user identifiers, and the identities on its
// parent chain.
type viewer struct {
	uid, euid int
	ancestors []Identity
}

// currentViewer reads this process's viewer once per listing.
func currentViewer() viewer {
	return viewer{uid: os.Getuid(), euid: os.Geteuid(), ancestors: ancestry(os.Getpid())}
}

// ancestry walks the parent links from pid upward, through the parent field
// of each stat entry, and returns the identities it verified. A parent is
// verified only when its start time is no later than the start time of the
// process below it, which a parent has to be and a process later given a
// parent's identifier is not. The walk stops at the first entry it cannot
// read, at an identifier of zero, and at the first parent it cannot verify,
// so every failure only shortens the list blocks sets aside, never lengthens
// it.
func ancestry(pid int) []Identity {
	child, present, err := Identify(pid)
	if err != nil || !present {
		return nil
	}
	var chain []Identity
	for {
		fields, found, err := statFields(child.PID)
		if err != nil || !found {
			return chain
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid <= 0 || slices.ContainsFunc(chain, func(id Identity) bool { return id.PID == ppid }) {
			return chain
		}
		parent, present, err := Identify(ppid)
		if err != nil || !present || parent.Start > child.Start {
			return chain
		}
		chain = append(chain, parent)
		child = parent
	}
}

// blocks reports whether a process whose working directory was refused
// keeps a sweep from clearing. Every such process does, with two carve-outs
// and no others, each decided from reads rather than assumed:
//
//   - It is on this process's parent chain, by identity. Each of those
//     started no later than this process, so this service did not start it.
//     This is what lets a service running as the user an init process runs
//     as clear at all where init's working directory is refused to it.
//   - None of its four user identifiers is this process's real or effective
//     one, and the zero signal was refused. The sweep could not end it if it
//     were found, and it shares no user with this service.
//
// A process with a user in common with this service blocks whatever the
// signal answered, and one that answered the signal blocks whatever its
// users are.
func (v viewer) blocks(r refused) bool {
	if slices.Contains(v.ancestors, r.id) {
		return false
	}
	for _, uid := range r.uids {
		if uid == v.uid || uid == v.euid {
			return true
		}
	}
	return !errors.Is(r.signal, syscall.EPERM)
}

// gone reports whether a read of a /proc entry failed because the process is
// no longer there.
func gone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// resolvedCwd is the working directory resolved through symbolic links, or
// as read when it cannot be resolved.
func resolvedCwd(cwd string) string {
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
}

// within reports whether path is dir or below it, by path element.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// signalProcess sends the polite signal, or the forceful one, to the one
// process pid, not to its group.
func signalProcess(pid int, forceful bool) error {
	sig := syscall.SIGTERM
	if forceful {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(pid, sig)
}
