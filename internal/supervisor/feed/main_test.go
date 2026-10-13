package feed_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// holdLockVar turns the test binary into a process that holds a home's lock,
// on internal/home's own pattern: the process reports "held" once it has the
// lock and lets go when its standard input closes, so the P10 cases run
// against a lock a real second process holds and not a second handle in this
// one.
const holdLockVar = "FEED_TEST_HOLD_LOCK"

func TestMain(m *testing.M) {
	if root := os.Getenv(holdLockVar); root != "" {
		os.Exit(holdLock(root))
	}
	os.Exit(m.Run())
}

func holdLock(root string) int {
	h, err := home.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := h.Acquire(context.Background(), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// holdInAnotherProcess starts a second process holding h's lock and returns
// once it reports holding it. The lock is let go when the test ends.
func holdInAnotherProcess(t *testing.T, h *home.Home) {
	t.Helper()
	holder := exec.CommandContext(t.Context(), os.Args[0])
	holder.Env = append(os.Environ(), holdLockVar+"="+h.Root())
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatalf("piping to the holder: %v", err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatalf("piping from the holder: %v", err)
	}
	holder.Stderr = os.Stderr
	if err := holder.Start(); err != nil {
		t.Fatalf("starting the holder: %v", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("the holder reported %q, %v; want held", line, err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = holder.Wait()
	})
}
