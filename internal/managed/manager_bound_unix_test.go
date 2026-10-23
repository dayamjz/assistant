//go:build unix

package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// TestAManagerCommandThatNeverAnswersIsEndedWithItsGroupAtItsBound holds
// P15 for the service manager's command line: with a launchctl or a
// systemctl standing in that answers the install and then, asked to start,
// ignores SIGTERM, starts a descendant in its group that ignores it too, and
// never exits, a Start under a context that never ends refuses with
// ReasonManagerRefused at the manager's bound, saying it ran past it, and
// neither process is running afterwards. Without the bound the Start blocks
// for as long as the command runs, and bounded by a context alone it blocks
// on the output the descendant still holds.
func TestAManagerCommandThatNeverAnswersIsEndedWithItsGroupAtItsBound(t *testing.T) {
	principles.Cite(t, principles.P15)
	const bound = 500 * time.Millisecond
	for _, c := range []struct {
		kind    Kind
		command string
		start   string
	}{
		{KindSystemd, "systemctl", "start"},
		{KindLaunchd, "launchctl", "kickstart"},
	} {
		t.Run(string(c.kind), func(t *testing.T) {
			t.Parallel()
			userHome := t.TempDir()
			dir := t.TempDir()
			pids := filepath.Join(dir, "pids")
			script := "#!/bin/sh\n" +
				"for a in \"$@\"; do\n" +
				"  if [ \"$a\" = " + c.start + " ]; then\n" +
				"    trap '' TERM\n" +
				"    /bin/sleep 3600 &\n" +
				"    echo $$ $! >> '" + pids + "'\n" +
				"    while :; do wait; done\n" +
				"  fi\n" +
				"done\n"
			if err := os.WriteFile(filepath.Join(dir, c.command), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			m, err := forKind(c.kind, env(userHome, dir), filepath.Join(userHome, ".assistant"), os.Getuid())
			if err != nil {
				t.Fatalf("forKind: %v", err)
			}
			// The install runs the stand-in under the production bound, which
			// also pays for a first run of a newly written program before
			// the bound under test starts.
			if err := m.Install(context.Background(), spec(filepath.Join(userHome, ".assistant"))); err != nil {
				t.Fatalf("Install: %v", err)
			}
			m.bound = bound

			// A passing run has already seen both gone, so only a failing one
			// has anything here to end.
			t.Cleanup(func() {
				if !t.Failed() {
					return
				}
				data, _ := os.ReadFile(pids)
				for _, f := range strings.Fields(string(data)) {
					if pid, err := strconv.Atoi(f); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			type answer struct {
				err     error
				elapsed time.Duration
			}
			done := make(chan answer, 1)
			go func() {
				started := time.Now()
				err := m.Start(context.WithoutCancel(t.Context()))
				done <- answer{err, time.Since(started)}
			}()
			within := bound + 3*managerGrace + 2*time.Second
			var got answer
			select {
			case got = <-done:
			case <-time.After(within):
				t.Fatalf("Start was still blocked on a %s that never answers %s after it began, past a bound of %s", c.command, within, bound)
			}
			data, _ := os.ReadFile(pids)
			fields := strings.Fields(string(data))
			if len(fields) != 2 {
				t.Fatalf("the stand-in %s wrote %q, want its identifier and its descendant's", c.command, data)
			}
			var refusal *Refusal
			if !errors.As(got.err, &refusal) || refusal.Reason != ReasonManagerRefused || !strings.Contains(refusal.Detail, "still running after "+bound.String()) {
				t.Fatalf("Start over a %s that never answers = %v, want manager-refused saying it ran past %s", c.command, got.err, bound)
			}
			if got.elapsed < bound {
				t.Fatalf("Start answered after %s, before its bound of %s", got.elapsed, bound)
			}
			for _, f := range fields {
				pid, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("the stand-in %s wrote %q, want identifiers", c.command, data)
				}
				gone := time.Now().Add(5 * time.Second)
				for {
					err := syscall.Kill(pid, 0)
					if errors.Is(err, syscall.ESRCH) {
						break
					}
					if !time.Now().Before(gone) {
						_ = syscall.Kill(pid, syscall.SIGKILL)
						t.Fatalf("process %d of the %s command's group is still running (%v) after Start returned: its group was not ended", pid, c.command, err)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
}
