package session

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE, the exit code GetExitCodeProcess reports for
// a process that has not exited.
const stillActive = 259

// startTime opens the process for limited querying and reads its creation
// time, in 100-nanosecond intervals since the Windows epoch. An identifier
// no process has cannot be opened and proves absence; one that opens but
// whose exit code is no longer STILL_ACTIVE has exited and is counted as not
// running, since an exited process stays openable while a handle to it is
// held somewhere.
func startTime(pid int) (uint64, bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return 0, false, nil
		}
		return 0, false, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return 0, false, err
	}
	if code != stillActive {
		return 0, false, nil
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), true, nil
}
