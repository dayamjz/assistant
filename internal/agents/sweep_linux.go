package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
// this process is refused permission to read is counted unreadable. Any
// other failure to read an entry is unverified; a failure to read /proc
// itself, or ctx ending, fails the listing.
//
// The link's target is resolved through symbolic links when it can be, and
// compared as read when it cannot, which is a working directory that no
// longer exists.
func listUnder(ctx context.Context, dir string) (listing, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return listing{}, err
	}
	self := os.Getpid()
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
		cwd, err := os.Readlink("/proc/" + entry.Name() + "/cwd")
		if err != nil {
			switch {
			case gone(err):
			case errors.Is(err, os.ErrPermission):
				found.unreadable++
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
