package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
)

// requireSweepClearsHere skips the test, recording the limit, when this
// host refuses every sweep: the real sweep of an empty task copy of a home
// created for the purpose does not clear, and what keeps it from clearing
// is only processes whose working directory it cannot read and which
// neither of agents.Sweeper's carve-outs sets aside. Nothing was started in
// that copy, so they are processes of this host outside it, and every
// relaunch and every dispatch retry here is refused before its claim while
// they run, which is P6's refusal working. unestablished names what the
// test would have established. A platform with no sweep, and a host whose
// empty copy sweeps clear, run the test; any other answer from the empty
// copy fails it, because it is not the host's standing refusal.
func requireSweepClearsHere(t *testing.T, unestablished string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := agents.NewSweeper(h, 0, nil).Sweep(t.Context(), dir)
	switch {
	case errors.Is(err, agents.ErrSweepUnsupported), err == nil && r.Clear():
		return
	case err != nil:
		t.Fatalf("sweeping an empty copy for the host's baseline: %v", err)
	case len(r.Ended)+len(r.Exited)+len(r.Replaced)+len(r.Survived)+len(r.Unverified) != 0 || len(r.UnreadableBlocking) == 0:
		t.Fatalf("the sweep of an empty copy did %s (%+v), want it clear or blocked only by processes whose working directory it cannot read", r, r)
	}
	t.Skipf("this host has %d process(es) outside any copy under test whose working directory this service cannot read and "+
		"which neither of agents.Sweeper's carve-outs sets aside, so every sweep here refuses to clear, as P6 requires, and "+
		"%s is not established here", len(r.UnreadableBlocking), unestablished)
}

// relaunchedDetail is the detail of the task's newest "relaunched" note, and
// fails the test when there is none.
func relaunchedDetail(t *testing.T, records *store.Store, id string) string {
	t.Helper()
	events, err := records.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == relaunchedKind {
			return events[i].Detail
		}
	}
	t.Fatalf("task %s has no %q note", id, relaunchedKind)
	return ""
}

// sweepUnsupported is the answer agents.Sweeper gives on a platform with no
// listing, wrapped the way Sweep wraps a listing's error, so a test on Linux
// can put the consumer in front of it.
func sweepUnsupported(_ context.Context, dir string) (agents.SweepResult, error) {
	return agents.SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", dir, agents.ErrSweepUnsupported)
}

// A sweep a platform cannot make is not read as one that found nothing, at
// the consumer: with the sweep answering agents.ErrSweepUnsupported the
// relaunch goes ahead as it did before the sweep existed, which is the gap
// PRD section 11's proof leaves on that platform, and the note it records
// says nothing was looked for and nothing is proven, and does not say the
// copy was swept. The control is the same relaunch with the real sweep on a
// platform that lists, whose note says the copy was swept.
func TestAnUnsupportedSweepRelaunchesUnprovenAndSaysSo(t *testing.T) {
	sub := newRelaunchSubject(t)
	sub.restart.sweepCopy = sweepUnsupported
	out, err := sub.restart.relaunchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatalf("relaunching with no sweep on this platform: %v", err)
	}
	detail := relaunchedDetail(t, sub.records, sub.task)
	for _, text := range []string{detail, out.swept, out.launch.note} {
		if !strings.Contains(text, "none is proven absent") || strings.Contains(text, "was swept") ||
			strings.Contains(text, "reclaimed") {
			t.Fatalf("the relaunch with an unsupported sweep says %q, want it to say nothing is proven and nothing was swept", text)
		}
	}

	control := newRelaunchSubject(t)
	if _, err := agents.NewSweeper(control.restart.home, 0, nil).Sweep(t.Context(), control.copy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Logf("%s has no sweep, so the control's swept reading is not established here", runtime.GOOS)
		return
	}
	requireSweepClearsHere(t, "the control, a relaunch whose note says the copy was swept,")
	if _, err := control.restart.relaunchTask(t.Context(), control.task); err != nil {
		t.Fatalf("relaunching the control: %v", err)
	}
	if detail := relaunchedDetail(t, control.records, control.task); !strings.Contains(detail, "was swept") {
		t.Fatalf("the control's relaunch says %q, want it to say the copy was swept", detail)
	}
}

// A sweep that failed refuses the relaunch before the claim: no attempt is
// begun, no note is appended, and the refusal is a *copyUnswept wrapping
// the sweep's error, so the next start asks again rather than relaunching
// into a copy a live process may still own.
func TestAFailedSweepRefusesTheRelaunchBeforeTheClaim(t *testing.T) {
	sub := newRelaunchSubject(t)
	ctx := t.Context()
	listingFailed := errors.New("reading /proc: permission denied")
	sub.restart.sweepCopy = func(_ context.Context, dir string) (agents.SweepResult, error) {
		return agents.SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", dir, listingFailed)
	}
	before := eventKinds(t, sub.records, sub.task)
	_, err := sub.restart.relaunchTask(ctx, sub.task)
	var unswept *copyUnswept
	if !errors.As(err, &unswept) || !errors.Is(err, listingFailed) {
		t.Fatalf("the relaunch answered %v, want *copyUnswept wrapping the listing's failure", err)
	}
	if attempts, err := sub.records.TaskAttempts(ctx, sub.task); err != nil || len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v (%v), want only the first", attempts, err)
	}
	if after := eventKinds(t, sub.records, sub.task); len(after) != len(before) {
		t.Fatalf("the history moved from %v to %v on a refused relaunch", before, after)
	}
}

// PRD section 11's "proves no live process owns it", with the real sweep: a
// process left running in the copy after the worker's terminal is gone, in
// a session of its own so neither the worker's group nor its parent reaches
// it, is ended by the relaunch before the claim, and the relaunched note
// says the copy was reclaimed from whatever was in it. The wait is on the
// process's own exit, not on any record.
func TestARelaunchReclaimsTheCopyFromAProcessLeftInIt(t *testing.T) {
	requireSweepClearsHere(t, "a relaunch reclaiming its copy from a process left in it")
	sub := newRelaunchSubject(t)
	if _, err := agents.NewSweeper(sub.restart.home, 0, nil).Sweep(t.Context(), sub.copy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Skipf("%s has no sweep, so a relaunch reclaiming its copy is not established here: %v", runtime.GOOS, err)
	}
	left := exec.Command("sleep", "60")
	left.Dir = sub.copy
	detach(left)
	if err := left.Start(); err != nil {
		t.Skipf("no sleep to leave running in the copy, so the reclaim is not established here: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = left.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = left.Process.Kill()
		<-exited
	})

	out, err := sub.restart.relaunchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatalf("relaunching over a process left in the copy: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the process left in the copy is still running after the relaunch")
	}
	detail := relaunchedDetail(t, sub.records, sub.task)
	if !strings.Contains(detail, "reclaimed from whatever was running in it: 1 process(es)") ||
		!strings.Contains(out.launch.note, "reclaimed") {
		t.Fatalf("the relaunch says %q and tells the worker %q, want both to say the copy was reclaimed from one process", detail, out.launch.note)
	}
	if _, err := os.Stat(sub.copy); err != nil {
		t.Fatalf("the copy is gone after the relaunch: %v", err)
	}
}
