package standin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ignoresHangup reports whether the kernel records pid as ignoring the
// hangup signal, read from the SigIgn mask in its /proc status, a
// hexadecimal mask in which signal n is bit n-1.
func ignoresHangup(pid int) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil {
				return false, fmt.Errorf("SigIgn %q: %w", value, err)
			}
			return mask&(1<<(unix.SIGHUP-1)) != 0, nil
		}
	}
	return false, fmt.Errorf("process %d: /proc status has no SigIgn line", pid)
}
