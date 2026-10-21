package agents_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
)

// realLsof is the real output of the lsof run the listing makes, with this
// process's record found in it: the line opening it and the name line
// carrying its working directory.
func realLsof(t *testing.T) (out []byte, self, name string) {
	t.Helper()
	out, err := agents.LsofOutput(t.Context())
	if err != nil {
		t.Fatalf("running lsof as the listing does: %v", err)
	}
	self = "p" + strconv.Itoa(os.Getpid())
	lines := bytes.Split(out, []byte("\n"))
	for i, line := range lines {
		if string(line) == self && i+2 < len(lines) && string(lines[i+1]) == "fcwd" {
			return out, self, string(lines[i+2])
		}
	}
	t.Fatalf("lsof's output reports no working directory record for this process (%s): %q", self, out)
	return nil, "", ""
}

// TestTheListingReadsLsofsRealOutputAndRefusesWhatItCannotRead holds the
// parse of the working directories against what lsof really prints, rather
// than against output written here: the real output parses, and the
// working directory it reports for this process resolves to the one this
// process has. Every refusal is then a mutation of that real output, so
// each shape is one lsof's output is a step away from: an empty line, a
// descriptor that was not asked for, a second working directory for one
// process, and a field the run did not ask for each fail the parse; a name
// that is not an absolute path is left out, which the listing reads as a
// working directory it could not read rather than as one outside the copy.
func TestTheListingReadsLsofsRealOutputAndRefusesWhatItCannotRead(t *testing.T) {
	out, self, name := realLsof(t)
	cwds, err := agents.ParseLsof(out)
	if err != nil {
		t.Fatalf("parsing lsof's real output: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(cwds[os.Getpid()])
	if err != nil || got != want {
		t.Fatalf("lsof reports this process's working directory as %q (resolving to %q, %v), want %q", cwds[os.Getpid()], got, err, want)
	}

	record := self + "\nfcwd\n" + name + "\n"
	for _, c := range []struct {
		what   string
		mutate func(string) string
	}{
		{"an empty line", func(s string) string { return "\n" + s }},
		{"a descriptor other than cwd", func(s string) string { return replaceOnce(s, record, self+"\nftxt\n"+name+"\n") }},
		{"a second working directory for one process", func(s string) string { return s + self + "\nfcwd\n" + name + "\n" }},
		{"a field not asked for", func(s string) string { return replaceOnce(s, record, self+"\nccommand\nfcwd\n"+name+"\n") }},
		{"a name before any process", func(s string) string { return name + "\n" + s }},
	} {
		mutated := c.mutate(string(out))
		if mutated == string(out) {
			t.Fatalf("the mutation %q did not change the real output", c.what)
		}
		if _, err := agents.ParseLsof([]byte(mutated)); err == nil {
			t.Fatalf("lsof output with %s parsed", c.what)
		}
	}

	relative := replaceOnce(string(out), record, self+"\nfcwd\nnrelative/name\n")
	if relative == string(out) {
		t.Fatal("the relative-name mutation did not change the real output")
	}
	cwds, err = agents.ParseLsof([]byte(relative))
	if err != nil {
		t.Fatalf("parsing lsof output whose name for this process is not absolute: %v", err)
	}
	if got, reported := cwds[os.Getpid()]; reported {
		t.Fatalf("a name that is not an absolute path was reported as the working directory %q", got)
	}
}

// replaceOnce replaces the first old in s with replacement.
func replaceOnce(s, old, replacement string) string {
	return strings.Replace(s, old, replacement, 1)
}

// TestASweepOfACopyWhoseNameLsofCouldRenderDifferentlyFails holds the one
// refusal the listing makes before it reads anything: a copy whose path
// holds a space is one whose name lsof's output might not carry byte for
// byte, so the sweep fails rather than comparing, and its zero result is
// not clear.
func TestASweepOfACopyWhoseNameLsofCouldRenderDifferentlyFails(t *testing.T) {
	h, _ := sweepHome(t, sweepBase(t), "with space")
	dir := makeTaskCopy(t, h)
	r, err := agents.NewSweeper(h, sweepGrace, nil).Sweep(t.Context(), dir)
	if err == nil || errors.Is(err, agents.ErrSweepUnsupported) || errors.Is(err, home.ErrNotACopy) || r.Clear() {
		t.Fatalf("sweeping %s answered %s, %v, want a failed listing and the zero result", dir, r, err)
	}
}

// hangingLsof writes a program into dir that stands where lsof is and never
// exits: a shell that ignores SIGTERM and waits on a sleep it started in its
// own process group, each writing its identifier to the returned file
// first. It is the run a wedged lsof makes, with a descendant beside it.
func hangingLsof(t *testing.T, dir string) (program, pids string) {
	t.Helper()
	program, pids = filepath.Join(dir, "lsof"), filepath.Join(dir, "pids")
	script := "#!/bin/sh\ntrap '' TERM\necho $$ > " + pids + "\n/bin/sleep 3600 &\necho $! >> " + pids + "\nwait\n"
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, pids
}

// startedPIDs reads the two identifiers hangingLsof's program wrote, waiting
// up to sweepEnded for both to be written.
func startedPIDs(t *testing.T, file string) []int {
	t.Helper()
	deadline := time.Now().Add(sweepEnded)
	for {
		data, _ := os.ReadFile(file)
		if fields := strings.Fields(string(data)); len(fields) == 2 {
			var pids []int
			for _, f := range fields {
				pid, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("the stand-in lsof wrote %q, want identifiers", data)
				}
				pids = append(pids, pid)
			}
			return pids
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the stand-in lsof wrote %q within %s, want two identifiers", data, sweepEnded)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestASweepWhoseLsofNeverExitsEndsItAtTheDeadlineAndProvesNothing holds
// P15 for the listing's one external program: with a program standing where
// lsof is that never exits and ignores SIGTERM, a sweep under a context that
// never ends, as internal/service's sweepFinished gives it, returns within
// the run's deadline and a bounded slack, with agents.ErrSweepTimedOut and
// the zero result, which is not clear; and neither the stand-in nor the
// process it started in its group is running afterwards, checked by
// identifier. Without the deadline the sweep blocks for as long as the
// stand-in runs.
func TestASweepWhoseLsofNeverExitsEndsItAtTheDeadlineAndProvesNothing(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	base := sweepBase(t)
	h, _ := sweepHome(t, base, "hanging")
	dir := makeTaskCopy(t, h)
	program, pidFile := hangingLsof(t, base)
	const deadline = 500 * time.Millisecond

	type answer struct {
		r   agents.SweepResult
		err error
	}
	done := make(chan answer, 1)
	started := time.Now()
	go func() {
		r, err := agents.SweepWithLsof(context.WithoutCancel(t.Context()), agents.NewSweeper(h, sweepGrace, nil), dir, program, deadline)
		done <- answer{r, err}
	}()
	pids := startedPIDs(t, pidFile)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	var got answer
	select {
	case got = <-done:
	case <-time.After(deadline + sweepEnded):
		t.Fatalf("the sweep was still blocked on a stand-in lsof that never exits %s after it started, against a deadline of %s", time.Since(started).Round(time.Millisecond), deadline)
	}
	if !errors.Is(got.err, agents.ErrSweepTimedOut) || got.r.Clear() || got.r.String() != (agents.SweepResult{}).String() {
		t.Fatalf("the sweep over a stand-in lsof that never exits answered %s, %v, want the zero result and agents.ErrSweepTimedOut", got.r, got.err)
	}
	if errors.Is(got.err, agents.ErrSweepUnsupported) || errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("the timed out sweep answered %v, which a caller would read as another refusal", got.err)
	}
	for _, pid := range pids {
		gone := time.Now().Add(sweepEnded)
		for {
			_, present, err := agents.Identify(pid)
			if err == nil && !present {
				break
			}
			if !time.Now().Before(gone) {
				t.Fatalf("process %d, which the stand-in lsof's run started, reads present (%v) %s after the sweep returned", pid, err, sweepEnded)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestASweepWhoseLsofExitsInTimeStillClears is the positive control for the
// test above: the real /usr/sbin/lsof, run through the same seam under the
// production deadline over an empty copy, gives a sweep that clears, or one
// blocked only by the host's standing blockers, which hostLimit records.
func TestASweepWhoseLsofExitsInTimeStillClears(t *testing.T) {
	requiresIdentityRead(t)
	blockers := hostBlockers(t)
	h, _ := sweepHome(t, sweepBase(t), "timely")
	dir := makeTaskCopy(t, h)
	r, err := agents.SweepWithLsof(t.Context(), agents.NewSweeper(h, sweepGrace, nil), dir, "/usr/sbin/lsof", agents.LsofDeadline)
	if err != nil {
		t.Fatalf("sweeping an empty copy with the real lsof under the production deadline: %v", err)
	}
	requireClearUnlessHostBlocks(t, r, blockers, "the sweep of an empty copy")
	hostLimit(t, blockers, "that a timely lsof run clears the sweep")
}
