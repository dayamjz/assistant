//go:build linux

package q1_test

import (
	"os"
	"strconv"
)

// startRecord is what the kernel holds of how pid was started, the record
// ps eww lists from: on Linux its /proc cmdline and environ, the arguments
// and the start environment.
func startRecord(pid int) ([]byte, error) {
	dir := "/proc/" + strconv.Itoa(pid) + "/"
	args, err := os.ReadFile(dir + "cmdline")
	if err != nil {
		return nil, err
	}
	env, err := os.ReadFile(dir + "environ")
	return append(args, env...), err
}
