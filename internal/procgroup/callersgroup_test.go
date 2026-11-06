//go:build linux || darwin

package procgroup_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// printGroup is the group stand-in: it prints its own process group.
func printGroup() {
	_, _ = os.Stdout.WriteString(strconv.Itoa(syscall.Getpgrp()) + "\n")
}

// groupOf runs this test binary as a stand-in that prints its own process
// group through run, and returns that group and the stand-in's process.
func groupOf(t *testing.T, run func(context.Context, *exec.Cmd, time.Duration) error) (group, pid int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInGroup)
	var out strings.Builder
	cmd.Stdout = &out
	if err := run(t.Context(), cmd, time.Second); err != nil {
		t.Fatalf("running the stand-in: %v", err)
	}
	group, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("the stand-in printed %q, not its group", out.String())
	}
	return group, cmd.Process.Pid
}

// A program RunInCallersGroup starts is in the caller's own process group,
// where an end of that group reaches it, and one Run starts leads a group of
// its own, as the positive control that the group read is the program's.
func TestRunInCallersGroupJoinsTheCallersGroup(t *testing.T) {
	t.Parallel()
	if group, _ := groupOf(t, procgroup.RunInCallersGroup); group != syscall.Getpgrp() {
		t.Fatalf("the program ran in group %d, not the caller's %d", group, syscall.Getpgrp())
	}
	if group, pid := groupOf(t, procgroup.Run); group != pid {
		t.Fatalf("Run's program ran in group %d, not one it leads as process %d", group, pid)
	}
}

// RunInCallersGroup ends its program when ctx ends, as Run does, though it
// leads no group to signal: a stand-in that would wait a minute is ended,
// and the call answers within its grace of the deadline.
func TestRunInCallersGroupEndsItsProgramWithTheContext(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	err := procgroup.RunInCallersGroup(ctx, cmd, time.Second)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the program ended with %v, want SIGKILL", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("the call answered %s after it began, past its deadline and grace", took)
	}
}
