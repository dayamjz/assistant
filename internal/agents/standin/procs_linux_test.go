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

// statStopped reads the state from a /proc/<pid>/stat line as the kernel
// writes it, the line derived from this process's own: this process,
// running, does not read as stopped; the same line with its state T, or t,
// does, so does it with a command name holding spaces and parentheses of
// its own, which the state is read after; and a line with no command name,
// or nothing after it, is an error rather than a guess.
func TestAStatLineIsReadForTheStoppedState(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	own := strings.TrimSpace(string(raw))
	if stopped, err := statStopped(own); err != nil || stopped {
		t.Fatalf("statStopped of this running process's own line = %v, %v; want false", stopped, err)
	}
	end := strings.LastIndexByte(own, ')')
	fields := strings.Fields(own[end+1:])
	for _, state := range []string{"T", "t"} {
		fields[0] = state
		if stopped, err := statStopped(own[:end+1] + " " + strings.Join(fields, " ")); err != nil || !stopped {
			t.Fatalf("statStopped of the line with state %s = %v, %v; want true", state, stopped, err)
		}
	}
	open := strings.IndexByte(own, '(')
	if stopped, err := statStopped(own[:open] + "(a (T) name)" + " " + strings.Join(fields, " ")); err != nil || !stopped {
		t.Fatalf("statStopped of the line with a command name holding parentheses = %v, %v; want true", stopped, err)
	}
	fields[0] = "S"
	if stopped, err := statStopped(own[:open] + "(a (T) name)" + " " + strings.Join(fields, " ")); err != nil || stopped {
		t.Fatalf("statStopped of a sleeping line whose command name holds T = %v, %v; want false", stopped, err)
	}
	for _, bad := range []string{"12 no name", own[:end+1]} {
		if _, err := statStopped(bad); err == nil {
			t.Fatalf("statStopped(%q) gave no error", bad)
		}
	}
}
