//go:build linux

package standin

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// statExiting reads the kernel's exiting flag from a /proc/<pid>/stat line
// as the kernel writes it, the line derived from this process's own: this
// process, running, does not read as exiting; the same line with the flag
// set in its flags field, the ninth, does; so does it with a command name
// holding spaces and parentheses of its own, which the fields are read
// after; and a line with no command name, or too few fields after it, is
// an error rather than a guess.
func TestAStatLineIsReadForTheExitingFlag(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	own := strings.TrimSpace(string(raw))
	if exiting, err := statExiting(own); err != nil || exiting {
		t.Fatalf("statExiting of this running process's own line = %v, %v; want false", exiting, err)
	}
	end := strings.LastIndexByte(own, ')')
	fields := strings.Fields(own[end+1:])
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	fields[6] = strconv.FormatUint(flags|pfExiting, 10)
	set := own[:end+1] + " " + strings.Join(fields, " ")
	if exiting, err := statExiting(set); err != nil || !exiting {
		t.Fatalf("statExiting of the line with the exiting flag set = %v, %v; want true", exiting, err)
	}
	open := strings.IndexByte(own, '(')
	named := own[:open] + "(a (strange) name)" + " " + strings.Join(fields, " ")
	if exiting, err := statExiting(named); err != nil || !exiting {
		t.Fatalf("statExiting of the line with a command name holding parentheses = %v, %v; want true", exiting, err)
	}
	for _, bad := range []string{"12 no name", own[:end+1] + " R 1 2"} {
		if _, err := statExiting(bad); err == nil {
			t.Fatalf("statExiting(%q) gave no error", bad)
		}
	}
}
