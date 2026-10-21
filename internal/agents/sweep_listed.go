//go:build linux || darwin

package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// This file is the part of the listing Linux and macOS share: what is done
// with a process once its working directory has been read or refused, and
// the classification of a refused one. Each platform's listUnder decides
// only how the process table and the working directories are read, so the
// rule a refused process is held to is written once.

// addRead is the listing's step for a process whose working directory was
// read as cwd: its identity is read again, and a process whose identity
// changed since before was read is reported unverified rather than listed
// or skipped, because the working directory read may belong to either. One
// that exited meanwhile is skipped. One whose working directory, resolved
// through symbolic links when it can be, is dir or below it is listed.
func (l *listing) addRead(before Identity, cwd, dir string) {
	after, present, err := Identify(before.PID)
	switch {
	case err != nil || (present && after != before):
		l.unverified = append(l.unverified, before.PID)
		return
	case !present:
		return
	}
	if within(resolvedCwd(cwd), dir) {
		l.under = append(l.under, before)
	}
}

// addRefused is the listing's step for a process whose working directory
// could not be read: it is classified by readRefused and viewer.blocks. One
// that exited during those reads is skipped; one the reads could not decide
// about is counted unreadable and reported unverified; any other is counted
// unreadable and, unless me sets it aside, listed as blocking, through
// blockerOf, which reads its command name only after the decision is made.
func (l *listing) addRefused(before Identity, me viewer) {
	r, present, err := readRefused(before)
	switch {
	case !present && err == nil:
	case err != nil:
		l.unreadable++
		l.unverified = append(l.unverified, before.PID)
	default:
		l.unreadable++
		if me.blocks(r) {
			l.blocking = append(l.blocking, blockerOf(r))
		}
	}
}

// refused is what the listing read about a process whose working directory
// it could not read: the identity it was listed under, the user identifiers
// procUids reported for it, and the answer a zero signal to it got.
type refused struct {
	id     Identity
	uids   []int
	signal error
}

// readRefused reads what viewer.blocks decides from about the process id
// names: its user identifiers through procUids, then a zero signal, which
// delivers nothing, then its identity again. present is false, with no
// error, when the process exited during the reads. User identifiers that
// cannot be read, a signal answered with anything but success, refusal or
// absence, and an identity that changed are errors, which the listing
// reports unverified, so each fails toward not clearing.
func readRefused(id Identity) (refused, bool, error) {
	uids, present, err := procUids(id.PID)
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

// errNameGone and errNameReplaced are commandName's answers when the
// process its identity names is no longer running, or its identifier now
// names a process with another start time. Either is recorded in
// Blocker.NameErr, never returned from a sweep.
var (
	errNameGone     = errors.New("agents: the process exited before its command name was read")
	errNameReplaced = errors.New("agents: the identifier names another process by the time its command name was read")
)

// blockerOf is the Blocker for a refused process viewer.blocks has already
// decided keeps the sweep from clearing: the identity and user identifiers
// that decision read, and the command name commandName reads now, after it.
// The name is read only here, so nothing that decides can see it, and a
// failure to read it is recorded in NameErr rather than failing the sweep.
func blockerOf(r refused) Blocker {
	name, err := commandName(r.id)
	return Blocker{Identity: r.id, UIDs: r.uids, Name: name, NameErr: err}
}

// BlockerFor is the Blocker a sweep records for the process id names when
// that process keeps the sweep from clearing, read the way the sweep reads
// it: readRefused's reads, the user identifiers and then a zero signal and
// the identity again, and then blockerOf's command name. It decides
// nothing: no working directory is read and no carve-out is applied, so it
// does not say whether a sweep would block on the process, only what a
// Blocker for it carries. present is false, with no error, when the
// process has exited; any read readRefused fails on is the error. Its
// consumer is internal/service's test of how a refused sweep is
// attributed, which builds a blocker from it rather than writing one
// without the user identifiers a real one always carries.
func BlockerFor(id Identity) (Blocker, bool, error) {
	r, present, err := readRefused(id)
	if err != nil || !present {
		return Blocker{}, present, err
	}
	return blockerOf(r), true, nil
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

// ancestry walks the parent links from pid upward, through parentPID, and
// returns the identities it verified. A parent is verified only when its
// start time is no later than the start time of the process below it, which
// a parent has to be and a process later given a parent's identifier is
// not. The walk stops at the first entry it cannot read, at an identifier of
// zero, and at the first parent it cannot verify, so every failure only
// shortens the list blocks sets aside, never lengthens it.
func ancestry(pid int) []Identity {
	child, present, err := Identify(pid)
	if err != nil || !present {
		return nil
	}
	var chain []Identity
	for {
		ppid, found, err := parentPID(child.PID)
		if err != nil || !found || ppid <= 0 || slices.ContainsFunc(chain, func(id Identity) bool { return id.PID == ppid }) {
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

// blocks reports whether a process whose working directory could not be
// read keeps a sweep from clearing. Every such process does, with two
// carve-outs and no others, each decided from reads rather than assumed:
//
//   - It is on this process's parent chain, by identity. Each of those
//     started no later than this process, so this service did not start it.
//     This is what lets a service running as the user an init process runs
//     as clear at all where init's working directory is refused to it.
//   - None of the user identifiers procUids reported for it is this
//     process's real or effective one, and the zero signal was refused. The
//     sweep could not end it if it were found, and it shares no user with
//     this service. Every identifier procUids reports can only make this
//     carve-out harder to meet.
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
