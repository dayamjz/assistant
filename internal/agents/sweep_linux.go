package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// listUnder lists, from /proc, the processes whose working directory is dir
// or below it, with the identity of each. dir is a resolved, cleaned path.
//
// For each numbered entry other than this process, the identity is read,
// then the cwd link, then the identity again, on addRead's terms. An entry
// that vanished at any read is a process that exited, and is skipped. A
// link this process is refused permission to read is counted unreadable,
// and is then classified on addRefused's terms. Any other failure to read
// an entry is unverified; a failure to read /proc itself, or ctx ending,
// fails the listing.
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
		switch {
		case err == nil:
			found.addRead(before, cwd, dir)
		case gone(err):
		case errors.Is(err, os.ErrPermission):
			found.addRefused(before, me)
		default:
			found.unverified = append(found.unverified, pid)
		}
	}
	return found, nil
}

// procUids reads the Uid line of /proc/<pid>/status: the real, effective,
// saved and filesystem user identifiers, in that order. present is false
// when the entry is gone. A line that is missing or does not carry exactly
// four numbers is refused with errStatShape rather than read short.
func procUids(pid int) ([]int, bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		if gone(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return parseStatusUids(pid, string(raw))
}

// parseStatusUids is procUids' parse of a status entry's text.
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
				return nil, false, fmt.Errorf("%w: /proc/%d/status Uid field %q: %w", errStatShape, pid, f, err)
			}
			uids = append(uids, uid)
		}
		return uids, true, nil
	}
	return nil, false, fmt.Errorf("%w: /proc/%d/status has no Uid line", errStatShape, pid)
}

// parentPID reads the parent field of /proc/<pid>/stat. found is false when
// the entry is gone.
func parentPID(pid int) (int, bool, error) {
	fields, found, err := statFields(pid)
	if err != nil || !found {
		return 0, found, err
	}
	// fields[1] is field 4, the parent's identifier.
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false, fmt.Errorf("%w: /proc/%d/stat parent %q: %w", errStatShape, pid, fields[1], err)
	}
	return ppid, true, nil
}

// gone reports whether a read of a /proc entry failed because the process is
// no longer there.
func gone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}
