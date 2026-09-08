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
// The refusal is produced rather than stated, by reaping a real process the
// way the reaper does. Which refusal that is belongs to the platform and is
// not asserted, because it is precisely what Kill may not read: a check that
// pinned it would pin the thing being refused, and could only be written by
// picking the platform it was written on. What discriminates here without it
// is that a Kill returning what the kill reported fails this everywhere,
// whatever the platform reported. A Kill that returned early on some refusals
// and not others is caught only where its refusal is not one it tolerated,
// which is why the control below rests on no refusal at all.
func TestKillIsAnsweredByTheReaperAndNotByWhatTheKillReported(t *testing.T) {
	root := t.TempDir()
	log := harnessLogFor(t, root)
	cmd, refusal := reapedProcess(t)

	reaped := make(chan struct{})
	close(reaped)
	j := &Journey{root: root, service: &serving{cmd: cmd, log: log, done: reaped}}

	if err := j.Kill(); err != nil {
		t.Fatalf("killing a service whose exit this journey's reaper already holds reported %v, over "+
			"a kill this platform refused with %v; nothing is serving, which is the whole of what "+
			"Kill promises about the process", err, refusal)
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
	cmd, refusal := reapedProcess(t)

	// Nothing closes this, which is a reaper that has not accounted for the
	// process. It is the state Kill may not pass over.
	unaccounted := make(chan struct{})
	j := &Journey{root: root, service: &serving{cmd: cmd, log: log, done: unaccounted}}

	started := time.Now()
	err := j.Kill()
	took := time.Since(started)

	if err == nil {
		t.Fatalf("the kill was refused with %v and no reaper holds this process's exit, so nothing "+
			"here establishes that it stopped serving, and Kill reported success anyway", refusal)
	}
	if took < reapGrace {
		t.Fatalf("Kill reported in %s and the grace it gives a reaper is %s, so it reported without "+
			"waiting for one", took, reapGrace)
	}
	requireLogReleased(t, log)
}

// reapedProcess returns a command whose process has ended and been waited on,
// together with what killing it now reports.
//
// The state is the one a serving process reaches rather than one assembled for
// these callers: serving's reaper waits and closes its channel, and nothing in
// this package releases a process handle. This helper used to release one, and
// that is a state no reaper produces - on the platform whose wait already let
// the handle go, releasing it a second time is itself refused, and on the one
// whose wait leaves it held, the release changed which refusal a later kill
// reported. So the condition it built was about os.Process on the machine it
// was written on.
//
// That a kill is refused at all is asserted, since a process this can still
// kill is not the condition either caller is about and both would establish
// nothing over it. Which refusal it is is deliberately not, and is returned
// for the failure messages instead.
//
// It runs this package's own provider shim, which is a copy of this test
// binary that exits at once when no answer is prepared for it. That is the
// portable short-lived process this package already has; a shell command would
// not be one, which is the reason Shims copies a binary rather than writing a
// script.
func reapedProcess(t *testing.T) (*exec.Cmd, error) {
	t.Helper()
	shim, err := ShimPath(ProviderShimName)
	if err != nil {
		t.Fatalf("locating the shim, which is the short-lived process this needs: %v", err)
	}
	cmd := exec.Command(shim)
	// An empty answer is one the shim reports as unprepared and exits on, so
	// what it does is settled here rather than inherited from this process.
	cmd.Env = append(os.Environ(), ProviderAnswerVariable+"=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the shim: %v", err)
	}
	// This is the reaper's own wait, and the callers below stand in for the
	// channel it closes after one.
	_ = cmd.Wait()

	refusal := cmd.Process.Kill()
	if refusal == nil {
		t.Fatal("killing a process that has ended and been waited on was not refused, so the " +
			"condition the callers are about is not present and they would establish nothing")
	}
	return cmd, refusal
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
