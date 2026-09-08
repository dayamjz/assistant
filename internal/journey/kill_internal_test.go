package journey

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestKillIsAnsweredByTheReaperAndNotByWhatTheKillReported drives Kill against
// a service whose process this journey's own reaper has already accounted for
// and whose kill is therefore refused.
//
// It cites no principle. What it is about is this harness: every check that
// kills a service passes through here, and so does the cleanup of every
// journey that ever served, so a Kill that reports a failure for a service
// that is already gone fails checks for the harness rather than the product.
// It did, on the one platform where the refusal was not the one Kill
// recognized, and the failure it caused was not even its own: the report came
// back as a home that could not be removed, because the same path that
// returned early kept the log open.
//
// The refusal is produced rather than stated. A process a reaper has waited on
// is refused a kill, and how it is refused depends on what the wait left the
// handle as, which is not the same everywhere. Releasing the handle reaches
// one of those states on any platform, and the check below that the refusal is
// not the one the old code recognized is what makes this the condition rather
// than a picture of it: were the release to stop producing that state, this
// test would say so instead of quietly establishing nothing.
func TestKillIsAnsweredByTheReaperAndNotByWhatTheKillReported(t *testing.T) {
	root := t.TempDir()
	log := harnessLogFor(t, root)
	cmd := refusedKill(t)

	reaped := make(chan struct{})
	close(reaped)
	j := &Journey{root: root, service: &serving{cmd: cmd, log: log, done: reaped}}

	if err := j.Kill(); err != nil {
		t.Fatalf("killing a service whose exit this journey's reaper already holds reported %v; "+
			"nothing is serving, which is the whole of what Kill promises about the process", err)
	}
	if j.service != nil {
		t.Fatal("Kill returned and this journey is still recorded as serving, so a later Serve is " +
			"refused as a double serve and a later Close kills a process that is gone")
	}
	requireLogReleased(t, log)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("removing the home after Kill: %v", err)
	}
}

// TestKillReportsARefusedKillWhenNothingAccountedForTheProcess is the control
// on the one above.
//
// Answering from the reaper is worth having only if a kill that failed over a
// process nothing has accounted for is still reported. Without this, end
// returning nil unconditionally passes every other check here, and a service
// that went on serving would be reported as killed.
//
// It also holds the release of the log to the harder half: the log is let go
// on the path that reports a failure too, which is the path the early return
// used to take.
func TestKillReportsARefusedKillWhenNothingAccountedForTheProcess(t *testing.T) {
	root := t.TempDir()
	log := harnessLogFor(t, root)
	cmd := refusedKill(t)

	// Nothing closes this, which is a reaper that has not accounted for the
	// process. It is the state Kill may not pass over.
	unaccounted := make(chan struct{})
	j := &Journey{root: root, service: &serving{cmd: cmd, log: log, done: unaccounted}}

	started := time.Now()
	err := j.Kill()
	took := time.Since(started)

	if err == nil {
		t.Fatal("the kill was refused and no reaper holds this process's exit, so nothing here " +
			"establishes that it stopped serving, and Kill reported success anyway")
	}
	if took < reapGrace {
		t.Fatalf("Kill reported in %s and the grace it gives a reaper is %s, so it reported without "+
			"waiting for one", took, reapGrace)
	}
	requireLogReleased(t, log)
}

// refusedKill returns a command whose process has ended, been waited on, and
// had its handle released, so that asking to kill it is refused.
//
// It runs this package's own provider shim, which is a copy of this test
// binary that exits at once when no answer is prepared for it. That is the
// portable short-lived process this package already has; a shell command would
// not be one, which is the reason Shims copies a binary rather than writing a
// script.
func refusedKill(t *testing.T) *exec.Cmd {
	t.Helper()
	dir, err := Shims()
	if err != nil {
		t.Fatalf("installing the shims, one of which is the short-lived process this needs: %v", err)
	}
	cmd := exec.Command(filepath.Join(dir, ProviderShimName+exeSuffix()))
	// An empty answer is one the shim reports as unprepared and exits on, so
	// what it does is settled here rather than inherited from this process.
	cmd.Env = append(os.Environ(), ProviderAnswerVariable+"=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the shim: %v", err)
	}
	_ = cmd.Wait()
	if err := cmd.Process.Release(); err != nil {
		t.Fatalf("releasing the handle of a process that has already been waited on: %v", err)
	}

	refusal := cmd.Process.Kill()
	if refusal == nil {
		t.Fatal("killing a process that has been waited on and released was not refused, so the " +
			"condition the caller is about is not present and it would establish nothing")
	}
	if errors.Is(refusal, os.ErrProcessDone) {
		t.Fatalf("killing a process that has been waited on and released was refused with %v, which "+
			"is the one refusal Kill used to recognize, so the caller would pass against the code "+
			"that failed", refusal)
	}
	return cmd
}

// harnessLogFor opens the file Kill is responsible for letting go of, at the
// path this journey would have opened it at.
func harnessLogFor(t *testing.T, root string) *os.File {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(root, "journey-service.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("opening the harness log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// requireLogReleased fails the test unless Kill closed the log, which it
// establishes by closing it again and reading the refusal.
//
// The consequence being guarded against is a home that cannot be removed,
// which only appears where an open file cannot be unlinked. Asserting the
// removal instead would therefore be an assertion that holds vacuously
// wherever this repository's own tests mostly run, so what is asserted is the
// handle itself.
func requireLogReleased(t *testing.T, log *os.File) {
	t.Helper()
	err := log.Close()
	if err == nil {
		t.Fatal("Kill returned with the harness log still open; the home is this process's to hold " +
			"open or not, and on a platform where an open file cannot be unlinked one it holds is a " +
			"home Close cannot remove")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closing the harness log a second time reported %v rather than that it was already "+
			"closed, so whether Kill let go of it is unestablished", err)
	}
}
