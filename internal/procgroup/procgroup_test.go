package procgroup_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// standInMode makes this test binary a stand-in program when a test runs it,
// so a test has a real program to start without depending on one being
// installed. Its value says what the stand-in does: standInHang waits a
// minute, which a test's deadline ends first, standInPrint writes a line to
// standard output and exits, and anything else exits 0 at once.
const (
	standInMode  = "PROCGROUP_TEST_STAND_IN"
	standInHang  = "hang"
	standInPrint = "print"
)

func TestMain(m *testing.M) {
	switch os.Getenv(standInMode) {
	case "":
		os.Exit(m.Run())
	case standInHang:
		time.Sleep(time.Minute)
		os.Exit(3)
	case standInPrint:
		_, _ = os.Stdout.WriteString("stand-in output\n")
	}
	os.Exit(0)
}

// standIn returns a command that runs this test binary as a program that
// exits 0 at once.
func standIn(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"=1")
	return cmd
}

// Run refuses, and starts nothing for, a command it could not run on its own
// terms; the same command built plainly runs, so the refusals are of the
// shapes named and not of every command.
func TestRunRefusesACommandItCannotRunAsAGroupOfItsOwn(t *testing.T) {
	cases := map[string]struct {
		cmd   func(t *testing.T) *exec.Cmd
		grace time.Duration
	}{
		"built with a context": {cmd: func(t *testing.T) *exec.Cmd {
			c := exec.CommandContext(context.Background(), os.Args[0])
			c.Env = append(os.Environ(), standInMode+"=1")
			return c
		}, grace: time.Second},
		"with process attributes set": {cmd: func(t *testing.T) *exec.Cmd {
			c := standIn(t)
			c.SysProcAttr = &syscall.SysProcAttr{}
			return c
		}, grace: time.Second},
		"with no grace": {cmd: standIn, grace: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := tc.cmd(t)
			err := procgroup.Run(context.Background(), cmd, tc.grace)
			if !errors.Is(err, procgroup.ErrMisconfigured) {
				t.Errorf("Run = %v; want ErrMisconfigured", err)
			}
			if cmd.Process != nil {
				t.Errorf("Run started a command it refused")
			}
		})
	}

	started := standIn(t)
	if err := started.Start(); err != nil {
		t.Fatalf("starting the stand-in: %v", err)
	}
	if err := procgroup.Run(context.Background(), started, time.Second); !errors.Is(err, procgroup.ErrMisconfigured) {
		t.Errorf("Run of a started command = %v; want ErrMisconfigured", err)
	}
	_ = started.Wait()

	if err := procgroup.Run(context.Background(), standIn(t), time.Second); err != nil {
		t.Errorf("Run of a plain command = %v; want it to run", err)
	}
}

// A context that has already ended starts nothing, and Run answers with its
// error, so a call made after its deadline costs no process.
func TestRunStartsNothingUnderAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := standIn(t)
	if err := procgroup.Run(ctx, cmd, time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v; want the context's error", err)
	}
	if cmd.Process != nil {
		t.Errorf("Run started a command under an ended context")
	}
}

// Bound adds a deadline only where the context has none, and never moves one
// the caller chose.
func TestBoundAddsADeadlineOnlyWhereThereIsNone(t *testing.T) {
	bounded, cancel := procgroup.Bound(context.WithoutCancel(context.Background()), time.Minute)
	defer cancel()
	if dl, ok := bounded.Deadline(); !ok || time.Until(dl) > time.Minute {
		t.Errorf("Bound of a context with no deadline gave deadline %v, %v; want one within a minute", dl, ok)
	}

	want := time.Now().Add(time.Hour)
	caller, cancelCaller := context.WithDeadline(context.Background(), want)
	defer cancelCaller()
	kept, cancelKept := procgroup.Bound(caller, time.Second)
	defer cancelKept()
	if dl, _ := kept.Deadline(); !dl.Equal(want) {
		t.Errorf("Bound moved the caller's deadline from %v to %v", want, dl)
	}
}
