package agents_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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

// sweepReturnSlack is what a test allows past a run's deadline for the sweep
// to hand its answer back: scheduling, not any wait the run makes, since the
// run bounds all of those by its deadline.
const sweepReturnSlack = 300 * time.Millisecond

// standInLsof writes a program into dir that stands where lsof is: a shell
// running body, in which PIDS is the file every process it starts writes
// its identifier to, the shell first. It returns the program and that file.
func standInLsof(t *testing.T, dir, body string) (program, pids string) {
	t.Helper()
	program, pids = filepath.Join(dir, "lsof"), filepath.Join(dir, "pids")
	script := "#!/bin/sh\necho $$ > " + pids + "\n" + strings.ReplaceAll(body, "PIDS", pids)
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, pids
}

// startedPIDs reads the n identifiers a stand-in lsof wrote, waiting up to
// sweepEnded for all of them to be written.
func startedPIDs(t *testing.T, file string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(sweepEnded)
	for {
		data, _ := os.ReadFile(file)
		if fields := strings.Fields(string(data)); len(fields) == n {
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
			t.Fatalf("the stand-in lsof wrote %q within %s, want %d identifiers", data, sweepEnded, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sweepOverStandIn sweeps an empty copy of a fresh home under a context that
// never ends, as internal/service's sweepFinished gives it, with the
// stand-in body standing where lsof is under deadline. It waits for the n
// identifiers the stand-in writes, fails the test unless the sweep answers
// within deadline and sweepReturnSlack of its start, and fails it unless
// every one of those processes reads gone by identifier soon after. It
// returns what the sweep answered.
func sweepOverStandIn(t *testing.T, body string, n int, deadline time.Duration) (agents.SweepResult, error) {
	t.Helper()
	requiresIdentityRead(t)
	base := sweepBase(t)
	h, _ := sweepHome(t, base, "standin")
	dir := makeTaskCopy(t, h)
	program, pidFile := standInLsof(t, base, body)

	type answer struct {
		r       agents.SweepResult
		err     error
		elapsed time.Duration
	}
	done := make(chan answer, 1)
	go func() {
		started := time.Now()
		r, err := agents.SweepWithLsof(context.WithoutCancel(t.Context()), agents.NewSweeper(h, sweepGrace, nil), dir, program, deadline)
		done <- answer{r, err, time.Since(started)}
	}()
	pids := startedPIDs(t, pidFile, n)
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	var got answer
	select {
	case got = <-done:
	case <-time.After(deadline + sweepEnded):
		t.Fatalf("the sweep was still blocked on the stand-in lsof %s past a deadline of %s", sweepEnded, deadline)
	}
	if got.elapsed > deadline+sweepReturnSlack {
		t.Fatalf("the sweep answered %s, %v after %s, past its deadline of %s and %s of slack", got.r, got.err, got.elapsed, deadline, sweepReturnSlack)
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
	return got.r, got.err
}

// TestASweepWhoseLsofNeverExitsEndsItAtTheDeadlineAndProvesNothing holds
// P15 for the listing's one external program: with a program standing where
// lsof is that never exits and ignores SIGTERM, beside a process it started
// in its group, a sweep under a context that never ends answers within the
// run's deadline with agents.ErrSweepTimedOut and the zero result, which is
// not clear, and neither process is running afterwards, checked by
// identifier. Without the deadline the sweep blocks for as long as the
// stand-in runs.
func TestASweepWhoseLsofNeverExitsEndsItAtTheDeadlineAndProvesNothing(t *testing.T) {
	principles.Cite(t, principles.P15)
	r, err := sweepOverStandIn(t, "trap '' TERM\n/bin/sleep 3600 &\necho $! >> PIDS\nwait\n", 2, 500*time.Millisecond)
	if !errors.Is(err, agents.ErrSweepTimedOut) || r.Clear() || r.String() != (agents.SweepResult{}).String() {
		t.Fatalf("the sweep over a stand-in lsof that never exits answered %s, %v, want the zero result and agents.ErrSweepTimedOut", r, err)
	}
	if errors.Is(err, agents.ErrSweepUnsupported) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the timed out sweep answered %v, which a caller would read as another refusal", err)
	}
}

// TestALsofRunEndsWhatItLeftInItsGroupInsideTheDeadline is review's case
// against the whole run's bound: a program standing where lsof is leaves a
// descendant in its group that ignores SIGTERM and holds none of its output,
// and then exits on its own, once before lsof's share of the deadline is
// spent and once after. Either way the sweep answers within the deadline,
// the descendant is gone by identifier, and the answer is a failure beside
// the zero result: the listing's own refusal of output that does not report
// this process when the program exited in time, and agents.ErrSweepTimedOut
// when it did not. Ending the descendant only by a polite step, or only
// after the deadline's wait, fails the bound.
func TestALsofRunEndsWhatItLeftInItsGroupInsideTheDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	const deadline = 2 * time.Second
	for _, c := range []struct {
		name     string
		exit     string
		timedOut bool
	}{
		{"exits before its share", "1.6", false},
		{"exits after its share", "1.94", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := "(trap '' TERM; exec /bin/sleep 3600) >/dev/null 2>&1 </dev/null &\necho $! >> PIDS\n/bin/sleep " + c.exit + "\nexit 0\n"
			r, err := sweepOverStandIn(t, body, 2, deadline)
			if err == nil || r.Clear() || r.String() != (agents.SweepResult{}).String() || errors.Is(err, agents.ErrSweepTimedOut) != c.timedOut {
				t.Fatalf("the sweep over a stand-in lsof that %s answered %s, %v, want a failure beside the zero result, timed out %v", c.name, r, err, c.timedOut)
			}
		})
	}
}

// TestALsofRunThatExitsAtOnceIsNotWaitedOnUntilTheDeadline holds the wait
// for the leader's exit against the exit that comes before the wait is
// registered: a program standing where lsof is that exits as soon as it
// has written its identifier is answered for well inside the deadline, as
// the listing's refusal of output that does not report this process, and
// never as a timeout.
func TestALsofRunThatExitsAtOnceIsNotWaitedOnUntilTheDeadline(t *testing.T) {
	const deadline = 5 * time.Second
	started := time.Now()
	r, err := sweepOverStandIn(t, "exit 0\n", 1, deadline)
	if took := time.Since(started); took > deadline/2 {
		t.Fatalf("the sweep over a stand-in lsof that exits at once took %s, want well inside its deadline of %s", took, deadline)
	}
	if err == nil || errors.Is(err, agents.ErrSweepTimedOut) || r.Clear() {
		t.Fatalf("the sweep over a stand-in lsof that exits at once answered %s, %v, want the listing's own refusal and the zero result", r, err)
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

// TestALsofRunWhoseOutputIsHeldOpenAnswersAtTheDeadlineOrTheCancellation is
// review's case against the output wait: a program standing where lsof is
// prints a working directory record for this process that the listing
// accepts, starts a descendant in a process group of its own that holds the
// output open for hold, and exits at once. The run never takes that output
// as whole once its deadline has passed or its context has ended: held past
// the deadline it is agents.ErrSweepTimedOut within the deadline, never a
// bare exec.ErrWaitDelay; cancelled while held it is context.Canceled at the
// cancellation, cancelled once the leader has exited and the descendant
// holds the output, never a wait for the deadline, and that holds when the
// descendant lets go of the output after the cancellation too, which is
// never a success. The last row is the positive control: let go at once
// and not cancelled, the same output is read and accepted, so the refusals
// above are not the output's.
func TestALsofRunWhoseOutputIsHeldOpenAnswersAtTheDeadlineOrTheCancellation(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentityRead(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		hold     string
		deadline time.Duration
		cancel   bool
		want     error
		within   time.Duration
	}{
		{"held past the deadline", "3600", 500 * time.Millisecond, false, agents.ErrSweepTimedOut, 500 * time.Millisecond},
		{"held when cancelled", "3600", 5 * time.Second, true, context.Canceled, 0},
		{"let go after the cancellation", "1", 5 * time.Second, true, context.Canceled, 0},
		{"let go at once", "0", 2 * time.Second, false, nil, 2 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := "/usr/bin/perl -e 'setpgrp(0,0); open(my $f, \">>\", $ARGV[0]) or die; print $f \"$$\\n\"; close $f; select(undef, undef, undef, $ARGV[1])' PIDS " + c.hold + " &\n" +
				"printf 'p%s\\nfcwd\\nn%s\\n' " + strconv.Itoa(os.Getpid()) + " '" + cwd + "'\n/bin/sleep 0.05\nexit 0\n"
			program, pidFile := standInLsof(t, t.TempDir(), body)
			ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
			defer cancel()
			// A cancelled row cancels once the leader has exited and the
			// descendant is holding the output, so the cancellation lands in
			// the output wait, and is timed from there.
			started := make(chan time.Time, 1)
			started <- time.Now()
			returned := make(chan struct{})
			if c.cancel {
				<-started
				go func() {
					for {
						select {
						case <-returned:
							return
						case <-time.After(10 * time.Millisecond):
						}
						data, _ := os.ReadFile(pidFile)
						fields := strings.Fields(string(data))
						if len(fields) != 2 {
							continue
						}
						leader, _ := strconv.Atoi(fields[0])
						if _, present, err := agents.Identify(leader); err == nil && !present {
							started <- time.Now()
							cancel()
							return
						}
					}
				}()
			}
			cwds, err := agents.ReadCwdsWithLsof(ctx, program, c.deadline)
			close(returned)
			var took time.Duration
			select {
			case from := <-started:
				took = time.Since(from)
			default:
				t.Fatalf("the read answered %v, %v before the cancellation it was to be timed from", cwds, err)
			}
			for _, pid := range startedPIDs(t, pidFile, 2) {
				if id, present, idErr := agents.Identify(pid); idErr == nil && present {
					t.Cleanup(func() {
						if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
							_ = syscall.Kill(id.PID, syscall.SIGKILL)
						}
					})
				}
			}
			if took > c.within+sweepReturnSlack {
				t.Fatalf("the read answered %v %s after it started or was cancelled, want it within %s and %s of slack", err, took, c.within, sweepReturnSlack)
			}
			if c.want == nil {
				if err != nil || cwds[os.Getpid()] != cwd {
					t.Fatalf("the read of output let go at once answered %v, %v, want this process's working directory %q", cwds, err, cwd)
				}
				return
			}
			if !errors.Is(err, c.want) || cwds != nil || errors.Is(err, exec.ErrWaitDelay) {
				t.Fatalf("the read answered %v, %v, want %v and no working directories", cwds, err, c.want)
			}
		})
	}
}
