package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// holdLockVar turns the test binary into a process that holds a home's lock,
// on the pattern internal/home and internal/supervisor/feed use: the process
// reports "held" once it has the lock and lets go when its standard input
// closes, so the cases below that need a live service's lock run against one
// a real second process holds and not a second handle in this one.
const holdLockVar = "CLI_TEST_HOLD_LOCK"

// holdLockMain is what TestMain hands the process to when holdLockVar names
// a home. It returns false when it does not.
func holdLockMain() bool {
	root := os.Getenv(holdLockVar)
	if root == "" {
		return false
	}
	h, err := home.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lock, err := h.Acquire(context.Background(), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
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

// instance is the identity the tests' service goes by, in the shape
// internal/service mints.
const instance = "0123456789abcdef"

// freshness is how the hook judges a beacon under the operator's defaults,
// which is what a home with no configuration file resolves to.
var freshness = supervisor.FreshnessFor(config.DefaultSupervisionBeaconInterval)

// createdHome is a home that exists on disk with its layout made and its
// database created, which is the state a service leaves behind.
func createdHome(t *testing.T) *home.Home {
	t.Helper()
	h := newHome(t)
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	if err := openStore(t, h).Close(); err != nil {
		t.Fatalf("closing the database: %v", err)
	}
	return h
}

// oneTaskInFlight plants one task, which resolves as queued, in the home's
// database: the smallest fleet with work in flight.
func oneTaskInFlight(t *testing.T, h *home.Home) {
	t.Helper()
	records := openStore(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: "abcdefghij23", Shape: store.TaskInvestigation, Project: "one",
		Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the database: %v", err)
	}
}

// writeBeacon writes one beacon under the instance at written, through the
// real writer, so the file is what a service's loop leaves.
func writeBeacon(t *testing.T, h *home.Home, written time.Time) {
	t.Helper()
	w, err := feed.OpenWriter(h.Beacon())
	if err != nil {
		t.Fatalf("opening the beacon: %v", err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.Write(supervisor.Identity{Instance: instance, Epoch: 1}, written); err != nil {
		t.Fatalf("writing the beacon: %v", err)
	}
}

// writeInstance publishes the instance file the way a service does.
func writeInstance(t *testing.T, h *home.Home) {
	t.Helper()
	if err := h.WriteInstance(instance); err != nil {
		t.Fatalf("writing the instance file: %v", err)
	}
}

// hostStop drives "assistant host stop" the way the host does: through
// cli.Run with the test's own streams, under an environment the test
// composes, so the re-entry signal reaches the command the way the host
// supplies it and no subprocess is involved.
func hostStop(t *testing.T, h *home.Home, env map[string]string, args ...string) invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	var out, errs bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:       append([]string{"--home", h.Root(), "host", "stop"}, args...),
		Stdout:     &out,
		Stderr:     &errs,
		Getenv:     func(key string) string { return env[key] },
		WorkingDir: t.TempDir(),
		Executable: executable,
		Version:    "assistant (test)",
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// decodeHostStop reads the document the command wrote under --json.
func decodeHostStop(t *testing.T, got invocation) machine.HostStop {
	t.Helper()
	var answer machine.HostStop
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("decoding the answer: %v\n%s%s", err, got.stdout, got.stderr)
	}
	return answer
}

// P10 through the command the coordinator's host runs at turn end, which is
// the stop guard's push layer; internal/supervisor/feed's test cites the
// same principle for the predicate under it, and what this adds is the hook:
// the verdict reaches the host as an exit status and a message, with no
// service consulted, and the host's re-entry signal is the one input that
// lets a turn end with work under way unwatched.
//
// Section 13's two named cases are here as the host sees them: a stale
// beacon blocks although the process holding the lock is alive, and a fresh
// beacon blocks when the lock is free. The positive control leaves every fact
// intact and the turn may end, which is what shows the blocking cases block
// for the reason named and not for something the setup got wrong. Every
// message the host would relay is held to the vocabulary rule here, where it
// reaches the coordinator.
func TestHostStopBlocksTheTurnUnlessALiveSupervisorHoldsTheHome(t *testing.T) {
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	now := time.Now()

	cases := []struct {
		name    string
		plant   func(t *testing.T, h *home.Home)
		reason  supervisor.StopReason
		allowed bool
	}{
		{"positive control: lock held by a live process, matching fresh beacon, one task in flight",
			func(t *testing.T, h *home.Home) {
				oneTaskInFlight(t, h)
				holdInAnotherProcess(t, h)
				writeInstance(t, h)
				writeBeacon(t, h, now)
			}, supervisor.StopSupervised, true},
		{"the service is dead and its beacon is fresh",
			func(t *testing.T, h *home.Home) {
				oneTaskInFlight(t, h)
				writeInstance(t, h)
				writeBeacon(t, h, now)
			}, supervisor.StopLockFree, false},
		{"the service is alive and its loop is wedged, so the beacon is stale",
			func(t *testing.T, h *home.Home) {
				oneTaskInFlight(t, h)
				holdInAnotherProcess(t, h)
				writeInstance(t, h)
				writeBeacon(t, h, now.Add(-freshness.Window))
			}, supervisor.StopBeaconStale, false},
		{"the service is alive and wrote no beacon",
			func(t *testing.T, h *home.Home) {
				oneTaskInFlight(t, h)
				holdInAnotherProcess(t, h)
				writeInstance(t, h)
			}, supervisor.StopNoBeacon, false},
		{"the database cannot be read",
			func(t *testing.T, h *home.Home) {
				holdInAnotherProcess(t, h)
				writeInstance(t, h)
				writeBeacon(t, h, now)
				if err := os.WriteFile(h.Database(), []byte("not a database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}, supervisor.StopInFlightUnknown, false},
		{"nothing is in flight, with no service at all",
			func(*testing.T, *home.Home) {}, supervisor.StopNothingInFlight, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := createdHome(t)
			c.plant(t, h)
			got := hostStop(t, h, nil, "--json")
			answer := decodeHostStop(t, got)
			if answer.Supervision.Reason != c.reason {
				t.Fatalf("the guard answered %s, want %s:\n%s", answer.Supervision.Reason, c.reason, got.stderr)
			}
			if answer.Allowed != c.allowed || answer.ReEntered {
				t.Fatalf("the answer is %+v, want allowed=%v with no re-entry", answer, c.allowed)
			}
			if c.allowed {
				if got.code != machine.ExitOK || got.stderr != "" {
					t.Fatalf("an allowed turn exited %s with %q on standard error, want success and nothing", got.code, got.stderr)
				}
				return
			}
			// Blocked: the host's convention, a particular non-zero exit
			// and the reason on standard error, in the vocabulary.
			if got.code == machine.ExitOK {
				t.Fatalf("a blocked turn exited %s, want the host's block status", got.code)
			}
			if !strings.HasPrefix(got.stderr, machine.PhraseTurnBlocked) {
				t.Fatalf("standard error does not say the turn cannot end:\n%s", got.stderr)
			}
			if !strings.Contains(got.stderr, answer.Supervision.Warning) || answer.Supervision.Warning == "" {
				t.Fatalf("standard error does not carry the next decision %q:\n%s", answer.Supervision.Warning, got.stderr)
			}
			vocabtest.Assert(t, got.stderr)
			// The same answer without --json: the message is still where the
			// host reads it, and standard output carries no document.
			plain := hostStop(t, h, nil)
			if plain.code != got.code || plain.stderr != got.stderr || plain.stdout != "" {
				t.Fatalf("the plain rendering differs from the structured one: %s %q %q", plain.code, plain.stdout, plain.stderr)
			}
		})
	}
}

// The re-entry signal is the one input that lets a turn end with work under
// way and no live supervisor, and it is an input the host passes: the same
// home answers blocked without it and allowed with it, each invocation
// judged from the environment it was given, so nothing stored carries one
// invocation's answer into the next.
func TestHostStopAllowsAReEnteredTurnAndNeverAnUnsignalledOne(t *testing.T) {
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	h := createdHome(t)
	oneTaskInFlight(t, h)
	writeInstance(t, h)
	writeBeacon(t, h, time.Now())

	blocked := decodeHostStop(t, hostStop(t, h, nil, "--json"))
	if blocked.Allowed || blocked.ReEntered {
		t.Fatalf("with no signal the answer is %+v, want blocked", blocked)
	}

	signalled := map[string]string{cli.HostReentryVar: "1"}
	got := hostStop(t, h, signalled, "--json")
	reEntered := decodeHostStop(t, got)
	if !reEntered.Allowed || !reEntered.ReEntered || got.code != machine.ExitOK {
		t.Fatalf("with the signal the answer is %+v and exited %s, want allowed", reEntered, got.code)
	}
	if reEntered.Supervision.Reason != blocked.Supervision.Reason {
		t.Fatalf("the signal changed the guard's reason from %s to %s", blocked.Supervision.Reason, reEntered.Supervision.Reason)
	}
	if !strings.HasPrefix(got.stderr, machine.PhraseTurnReEntered) {
		t.Fatalf("standard error does not say why the turn may end:\n%s", got.stderr)
	}
	vocabtest.Assert(t, got.stderr)

	// Once: the next invocation without the signal is blocked again, so the
	// signal allowed the invocation it was given to and nothing else.
	if again := decodeHostStop(t, hostStop(t, h, nil, "--json")); again.Allowed {
		t.Fatalf("after a re-entered turn the next unsignalled one is %+v, want blocked", again)
	}
	// An empty value is no signal.
	if empty := decodeHostStop(t, hostStop(t, h, map[string]string{cli.HostReentryVar: ""}, "--json")); empty.Allowed {
		t.Fatalf("an empty variable allowed the turn: %+v", empty)
	}
}

// A guard the command cannot compute blocks rather than judging by a default:
// an operator file that does not parse leaves the reason empty, the detail
// says why, and the host reads a block.
func TestHostStopBlocksWhenTheConfigurationDoesNotResolve(t *testing.T) {
	t.Parallel()
	h := createdHome(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("supervision_beacon_interval: [not a duration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := hostStop(t, h, map[string]string{}, "--json")
	answer := decodeHostStop(t, got)
	if answer.Allowed || got.code == machine.ExitOK {
		t.Fatalf("an unresolved configuration allowed the turn: %+v, exit %s", answer, got.code)
	}
	if answer.Supervision.Reason != "" || answer.Detail == "" {
		t.Fatalf("the answer is %+v, want an uncomputed guard with a detail", answer)
	}
	if !strings.Contains(got.stderr, machine.PhraseUndecided) {
		t.Fatalf("standard error does not say the situation is undecided:\n%s", got.stderr)
	}
}

// TestTheHostVerbServesTheHookAndNothingElse is the boundary around the
// second command outside PRD section 9's table, on the terms
// TestTheGateVerbServesTheHooksAndNothingElse sets for the first: the
// subcommand the host invokes reaches a verb rather than being refused, and
// nothing else under that verb does, so "host" is not a door the
// specification's surface can grow through. The usage refusals are read as
// documents, because the host's block status and the usage status coincide
// and a code alone could not tell the two apart.
func TestTheHostVerbServesTheHookAndNothingElse(t *testing.T) {
	t.Parallel()
	h := createdHome(t)
	subject := t.TempDir()

	got := run(t, h, subject, "--json", "host", "stop")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant host stop over an idle home exited %s, want success:\n%s%s", got.code, got.stdout, got.stderr)
	}
	decodeHostStop(t, got)

	for _, args := range [][]string{
		{"host"},
		{"host", "start"},
		{"host", "stop", "again"},
		// The subcommand comes first after the verb.
		{"host", "--json", "stop", "stop"},
	} {
		got := run(t, h, subject, append([]string{"--json"}, args...)...)
		if got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage", strings.Join(args, " "), got.code)
		}
		var failure machine.Failure
		if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil || failure.Error == "" {
			t.Fatalf("assistant %s did not answer with a usage failure: %v\n%s", strings.Join(args, " "), err, got.stdout)
		}
	}
	// The usage text names the command under its own heading, so a command
	// this binary answers to is one it mentions.
	help := run(t, h, subject, "--help")
	if !strings.Contains(help.stdout, "host stop") || !strings.Contains(help.stdout, cli.HostReentryVar) {
		t.Fatalf("the usage text does not describe the host hook and its signal:\n%s", help.stdout)
	}
}
