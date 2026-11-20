//go:build darwin

package q1_test

import "golang.org/x/sys/unix"

// startRecord is what the kernel holds of how pid was started, the record
// ps eww lists from: on Darwin its kern.procargs2, the executable, the
// arguments and the start environment. Reading it needs no set-user-ID
// program, which CI's macOS sandbox refuses to start (scripts/leak-check.py).
func startRecord(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) }
