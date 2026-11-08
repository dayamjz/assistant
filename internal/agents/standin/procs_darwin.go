//go:build darwin

package standin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/agents"
	"golang.org/x/sys/unix"
)

// processIDs is every process identifier in the kernel's process table.
func processIDs() ([]int, error) {
	pids, _, err := readTable()
	return pids, err
}

// readTable is every process identifier in the kernel's process table, in
// the order the table names them, and each one's record, all from the one
// kern.proc.all read that names them.
func readTable() ([]int, map[int]tableRecord, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, nil, err
	}
	pids := make([]int, 0, len(procs))
	records := make(map[int]tableRecord, len(procs))
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		pids = append(pids, pid)
		records[pid] = recordOf(&procs[i])
	}
	return pids, records, nil
}

// readRecord is pid's record as a kern.proc.pid read finds it now, an exited
// process nobody has reaped among them; found is false when the table holds
// no entry for pid.
func readRecord(pid int) (tableRecord, bool, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil || len(procs) == 0 {
		return tableRecord{}, false, err
	}
	return recordOf(&procs[0]), true, nil
}

// recordOf is the record a process table entry holds: its identity
// (agents.EntryIdentity), its parent (Eproc.Ppid) and its command name
// (Proc.P_comm, up to its first NUL).
func recordOf(entry *unix.KinfoProc) tableRecord {
	name := entry.Proc.P_comm[:]
	if end := bytes.IndexByte(name, 0); end >= 0 {
		name = name[:end]
	}
	return tableRecord{Identity: agents.EntryIdentity(entry), parent: int(entry.Eproc.Ppid), name: string(name)}
}

// commandLine reads pid's executable path and its arguments, argv[0]
// first, from the kernel's kern.procargs2 record (procArgs). Nothing is
// split on white space, so a path or an argument with a space in it is read
// whole.
func commandLine(pid int) (string, []string, error) {
	executable, args, _, err := procArgs(pid)
	return executable, args, err
}

// startEnvironment reads the environment the kernel holds for pid, one
// NAME=value string per variable, from the same record. That is the
// environment the process was started with, not one it set for itself
// later. Whether a given program's processes carry a variable there is a
// fact about that program, so what relies on it, the owner sweep, is tested
// against a real terminal server rather than against this comment.
func startEnvironment(pid int) ([]string, error) {
	_, _, env, err := procArgs(pid)
	return env, err
}

// procArgs parses pid's kern.procargs2 record: a 32-bit argument count, the
// path the process was executed by and its terminating NUL, padding NULs,
// then that many NUL-terminated arguments, then the NUL-terminated
// environment strings up to the first empty one, after which come the
// strings the kernel adds for the loader, which are not the environment.
func procArgs(pid int) (string, []string, []string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, nil, err
	}
	if len(raw) < 4 {
		return "", nil, nil, fmt.Errorf("process %d: kern.procargs2 is %d bytes", pid, len(raw))
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil, nil, fmt.Errorf("process %d: kern.procargs2 has no terminated executable path", pid)
	}
	executable := string(rest[:end])
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", nil, nil, errors.New("kern.procargs2 ends inside an argument")
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	var env []string
	for {
		end := bytes.IndexByte(rest, 0)
		if end <= 0 {
			break
		}
		env = append(env, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, args, env, nil
}

// foreignProcess reports whether pid runs as another user's: its effective
// user, as the process table holds it (Eproc.Ucred.Uid), is not this
// process's effective user. It is read so that a listing passes over only
// the processes of another user's whose command line it could not read,
// rather than assuming that of every read that failed (processesWhere).
func foreignProcess(pid int) (bool, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return int(k.Eproc.Ucred.Uid) != os.Geteuid(), nil
}

// exitingProcess reports whether pid has begun to exit. This platform's
// process table reads an exited process as a zombie (agents.Identify), and
// a read of a running one that fails keeps a listing unsettled; a process
// still exiting is not told apart here, so it reports false.
func exitingProcess(int) (bool, error) { return false, nil }

// sstop is SSTOP from the platform's sys/proc.h, the process state of one
// a signal has stopped. It is spelled here because x/sys does not export it.
const sstop = 4

// stoppedProcess reports whether pid is stopped by a signal: its state in
// the process table (Proc.P_stat) is sstop. A process that is stopped runs
// none of its code, so it starts no process until it is continued.
func stoppedProcess(pid int) (bool, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return k.Proc.P_stat == sstop, nil
}

// commandNameLimit is MAXCOMLEN from the platform's sys/param.h, the most
// bytes of a command name the process table records. It is spelled here
// because x/sys does not export it.
const commandNameLimit = 16

// recordedName is the command name this platform's process table is
// predicted to record for a process running the file at binary: the base
// name of the file binary resolves to, cut to commandNameLimit bytes, which
// is what the table recorded for a copy of a test binary run by a long name
// and through a symbolic link on the machine this was written on. A listing
// does not take the prediction on its word (candidates). ok is false when
// binary cannot be resolved.
func recordedName(binary string) (name string, ok bool) {
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return "", false
	}
	name = filepath.Base(resolved)
	if len(name) > commandNameLimit {
		name = name[:commandNameLimit]
	}
	return name, true
}
