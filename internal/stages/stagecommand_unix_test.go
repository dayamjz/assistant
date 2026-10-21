//go:build unix

package stages_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// PRD principle P15 for a check command's deadline: a configured test command
// that never exits is ended at command_timeout together with what it started,
// and the stage holds on a check that answered nothing rather than holding
// the run for ever. Both the interpreter and the child it forked are checked
// by identifier, so a bound that ended only the interpreter fails here.
func TestANeverExitingStageCommandEndsAtItsBoundWithItsGroup(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	dir := t.TempDir()
	shell, child := filepath.Join(dir, "shell"), filepath.Join(dir, "child")
	run := newTestStageRun(t, "echo $$ > "+shell+"; sleep 300 & echo $! > "+child+"; wait")
	cfg := run.deps.Config
	cfg.CommandTimeout = time.Second
	run.deps.Config = cfg
	t.Cleanup(func() { endRecorded(t, shell, child) })

	report := reportWithin(t, run, 60*time.Second)
	var timedOut bool
	for _, f := range report.Findings {
		if f.Action == findings.ActionAsk && strings.Contains(f.Description, "time limit") {
			timedOut = true
		}
	}
	if !report.HasHeld() || !timedOut {
		t.Fatalf("a command ended at its bound produced %+v, which does not hold naming the time limit", report)
	}
	for _, path := range []string{shell, child} {
		assertGone(t, path)
	}
}

// PRD principle P15 for a check command's process group: a configured test
// command that forks a child and exits leaves no child behind once the stage
// returns, and the child no longer holds the output pipe, so the stage
// answers at once with its output whole rather than after commandGrace with
// a note that the record is short.
func TestAForkingStageCommandsChildEndsWithIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	child := filepath.Join(t.TempDir(), "child")
	run := newTestStageRun(t, "sleep 300 & echo $! > "+child)
	t.Cleanup(func() { endRecorded(t, child) })

	started := time.Now()
	report := reportWithin(t, run, 60*time.Second)
	if took := time.Since(started); took >= 5*time.Second {
		t.Errorf("the stage took %s, so the forked child held the output past the command", took)
	}
	if len(report.Findings) != 0 {
		t.Errorf("a command that exited zero reported %+v, and a whole output has nothing to report", report.Findings)
	}
	assertGone(t, child)
}

// With one command slot, two runs' stage commands never overlap: the second
// waits for the first to end. The control gives the same two runs two slots
// and a command that waits until both have started, so it passes only when
// they did run at once; a log that could not show an overlap fails there.
func TestOneCommandSlotRunsTwoRunsCommandsOneAtATime(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		slots int
		body  string
		want  string
	}{
		{"one slot", 1, "sleep 1", "start end start end"},
		{"two slots", 2, "n=0; while [ $(grep -c start LOG) -lt 2 ] && [ $n -lt 200 ]; do sleep 0.05; n=$((n+1)); done",
			"start start end end"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			log := filepath.Join(t.TempDir(), "log")
			command := "echo start >> " + log + "; " + strings.ReplaceAll(c.body, "LOG", log) + "; echo end >> " + log
			slots := stages.NewCommandSlots(c.slots)
			var wg sync.WaitGroup
			for range 2 {
				run := newTestStageRun(t, command)
				run.deps = stages.WithCommandSlots(run.deps, slots)
				wg.Go(func() {
					if _, err := runTestStage(t, run.deps, run.repositoryID, run.runID); err != nil {
						t.Errorf("running the test stage: %v", err)
					}
				})
			}
			wg.Wait()
			b, err := os.ReadFile(log)
			if err != nil {
				t.Fatalf("reading the commands' log: %v", err)
			}
			if got := strings.Join(strings.Fields(string(b)), " "); got != c.want {
				t.Fatalf("the two commands logged %q, want %q", got, c.want)
			}
		})
	}
}

// reportWithin runs the test stage over run and fails the test when it has not
// returned within limit, so a stage held by its command fails rather than
// hanging the package.
func reportWithin(t *testing.T, run *testStageRun, limit time.Duration) findings.Report {
	t.Helper()
	done := make(chan findings.Report, 1)
	failed := make(chan error, 1)
	go func() {
		out, err := runTestStage(t, run.deps, run.repositoryID, run.runID)
		if err != nil {
			failed <- err
			return
		}
		done <- out.Report.Normalize()
	}()
	select {
	case report := <-done:
		return report
	case err := <-failed:
		t.Fatalf("running the test stage: %v", err)
	case <-time.After(limit):
		t.Fatalf("the test stage had not returned after %s", limit)
	}
	return findings.Report{}
}

// assertGone fails the test unless the process whose identifier the command
// wrote to path is gone within a few seconds of the stage returning.
func assertGone(t *testing.T, path string) {
	t.Helper()
	pid := recordedPID(t, path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d (%s) is still there after the stage returned: %v", pid, filepath.Base(path), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// recordedPID reads the process identifier a command wrote to path.
func recordedPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the identifier the command recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("the command recorded %q, which is no process identifier", b)
	}
	return pid
}

// endRecorded ends every process whose identifier a command recorded when
// the test failed, so a failing test leaves nothing running behind it. A
// passing test has shown those processes gone, and signalling an identifier
// after that could reach a process that was given it since.
func endRecorded(t *testing.T, paths ...string) {
	if !t.Failed() {
		return
	}
	for _, path := range paths {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
}
