//go:build unix

package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
)

// TestAPushArrivingWhileARunsCopyIsBeingCutWaitsForItAndIsReceived is issue
// #144 reproduced and closed, through the product end to end.
//
// A push starts a run, and the service cuts that run's copy as a linked
// worktree of the gate repository. The cut is paused by the real mechanism at
// the point git has written the new worktree's entry and not yet pointed its
// HEAD anywhere: a reference-transaction hook in the gate holds the update
// git makes to that HEAD, which is the step that fills the entry in. Nothing
// here writes the entry by hand, so the state the second push meets is one
// git produced.
//
// A second push then arrives. Before the fix git refused it while checking
// that it was connected, with "bad object worktrees/<id>/HEAD", and this test
// fails at that point with git's own words. With the fix the push's receive
// command waits on the gate's copies lock, which the cut holds, says so on the
// pusher's standard error, and is received once the cut finishes. The test
// lets the cut finish only after it has read that line, so the push it then
// sees succeed is one that waited rather than one that arrived after the cut.
func TestAPushArrivingWhileARunsCopyIsBeingCutWaitsForItAndIsReceived(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	repository := initialize(t, h, subject)
	cut := pauseTheNextCut(t, repository)

	git(t, subject, "branch", "one")
	commitOn(t, subject, "one", "one.txt", "the first change\n")
	git(t, subject, "push", gate.RemoteName, "one")
	select {
	case <-cut.paused:
	case <-time.After(30 * time.Second):
		t.Fatal("the run the first push started never began cutting its copy")
	}

	git(t, subject, "branch", "two")
	head := commitOn(t, subject, "two", "two.txt", "the second change\n")
	push := startPush(t, subject, "two")
	select {
	case <-push.waiting:
	case <-push.done:
		t.Fatalf("the push arriving while a run's copy was being cut ended without waiting for it "+
			"(%v):\n%s", push.err, push.output())
	case <-time.After(30 * time.Second):
		t.Fatalf("the push neither waited nor ended:\n%s", push.output())
	}
	cut.release()
	select {
	case <-push.done:
	case <-time.After(60 * time.Second):
		t.Fatalf("the push did not end once the cut finished:\n%s", push.output())
	}
	if push.err != nil {
		t.Fatalf("the push that waited for the cut was refused (%v):\n%s", push.err, push.output())
	}
	if got := git(t, repository, "rev-parse", "refs/heads/two"); got != head {
		t.Fatalf("the gate holds two at %s after the push, want %s:\n%s", got, head, push.output())
	}
	for _, record := range listRuns(t, h, subject) {
		awaitHold(t, h, subject, record.ID)
	}
}

// TestAReceivePastTheBoundIsRefusedNamingTheCopyAndReceivesNothing is the
// other half of the owner's decision on #144: a push the cut holds past the
// bound is refused, before git starts, in words that name the transient
// state.
//
// It drives the receive command through cli.Run in this process rather than
// through a push, because the bound is internal/gate's thirty seconds and a
// push's receive command is another process, which a test cannot shorten.
// What is lost is git's relay of the standard error, which the test above
// shows reaching a person. The cut is stood in for by holding the copies lock
// the way internal/service holds it while it cuts.
//
// Standard output is checked to be empty, because it is the push protocol and
// git would have advertised its capabilities there the moment it started; the
// positive control receives with the lock free and sees that advertisement, so an empty output is evidence of a refusal before git and
// not of a check that cannot see output.
func TestAReceivePastTheBoundIsRefusedNamingTheCopyAndReceivesNothing(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	id, repository := initializeGate(t, h, subject)
	cli.ShortenReceiveWait(t, 50*time.Millisecond)

	free := receive(t, h, subject, id, repository)
	if free.code != machine.ExitOK || !strings.Contains(free.stdout, "report-status") {
		t.Fatalf("with no copy being cut the receive command exited %s and advertised nothing; the "+
			"check below would see nothing either:\nstdout %q\nstderr %q", free.code, free.stdout, free.stderr)
	}

	lock, err := gate.LockCopies(t.Context(), repository)
	if err != nil {
		t.Fatalf("holding the gate's copies lock as a cut does: %v", err)
	}
	defer func() { _ = lock.Release() }()
	held := receive(t, h, subject, id, repository)
	if held.code != machine.ExitFailure {
		t.Fatalf("a receive held past the bound exited %s, want %s:\n%s", held.code, machine.ExitFailure, held.stderr)
	}
	if held.stdout != "" {
		t.Fatalf("a receive refused past the bound wrote to standard output, which is the push protocol: %q",
			held.stdout)
	}
	for _, want := range []string{machine.PhraseCopyBusy.String(), machine.PhraseCopyStillBusy.String()} {
		if !strings.Contains(held.stderr, want) {
			t.Fatalf("a receive held past the bound did not say %q:\n%s", want, held.stderr)
		}
	}
	if !strings.Contains(machine.PhraseCopyStillBusy.String(), "being set up or cleaned up") {
		t.Fatalf("the refusal %q does not name the state the change waited on", machine.PhraseCopyStillBusy)
	}
	vocabtest.Control(t)
	vocabtest.Assert(t, held.stderr)
}

// TestTheReceiveRefusalStaysTyped is the typed half of that refusal: the
// rewording the command surface gives a person still matches
// gate.ErrCopyInTransit, through internal/gate's own lock rather than a value
// stated here.
func TestTheReceiveRefusalStaysTyped(t *testing.T) {
	repository := t.TempDir()
	lock, err := gate.LockCopies(t.Context(), repository)
	if err != nil {
		t.Fatalf("holding the copies lock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	_, err = gate.AwaitCopies(t.Context(), repository, time.Millisecond, nil)
	var refused *gate.CopyInTransitError
	if !errors.Is(err, gate.ErrCopyInTransit) || !errors.As(err, &refused) {
		t.Fatalf("a wait past the bound returned %v, want a *gate.CopyInTransitError", err)
	}
}

// TestInitializeWritesTheReceiveCommandOnTheAssistantRemoteOnly is the
// configuration half of the fix and PRD principle P1's half of it: the
// receive command is written on the assistant remote, naming this binary, the
// gate and the home, and origin's configuration is left exactly as it was.
func TestInitializeWritesTheReceiveCommandOnTheAssistantRemoteOnly(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	before := git(t, subject, "config", "--get-regexp", `^remote\.origin\.`)
	id, _ := initializeGate(t, h, subject)
	command := git(t, subject, "config", "remote."+gate.RemoteName+".receivepack")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	for _, want := range []string{filepath.ToSlash(executable), "gate " + gate.ReceiveSubcommand, id, h.Root()} {
		if !strings.Contains(command, want) {
			t.Fatalf("the assistant remote's receive command %q does not carry %q", command, want)
		}
	}
	if after := git(t, subject, "config", "--get-regexp", `^remote\.origin\.`); after != before {
		t.Fatalf("initializing changed origin's configuration:\nbefore %s\nafter  %s", before, after)
	}
}

// initializeGate initializes subject's gate and returns its identifier and
// repository.
func initializeGate(t *testing.T, h *home.Home, subject string) (string, string) {
	t.Helper()
	got := run(t, h, subject, "--json", "init")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var answer machine.Init
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("the init answer does not decode: %v\n%s", err, got.stdout)
	}
	return answer.Gate.ID, answer.Gate.Repository
}

// receive runs the gate's receive command through the command surface, with
// no push on standard input, the way git would run it for the remote.
func receive(t *testing.T, h *home.Home, subject, id, repository string) invocation {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	var out, errs bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:       []string{"gate", gate.ReceiveSubcommand, "--gate", id, "--home", h.Root(), repository},
		Stdout:     &out,
		Stderr:     &errs,
		Stdin:      strings.NewReader("0000"),
		Getenv:     func(string) string { return "" },
		WorkingDir: subject,
		Executable: executable,
		Version:    "assistant (test)",
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// pausedCut is a cut of a run's copy held part way through.
type pausedCut struct {
	paused  chan struct{}
	release func()
}

// pauseTheNextCut installs a reference-transaction hook in the gate
// repository that holds the first update of a HEAD it sees, which is the
// update git makes to fill in a worktree entry it is cutting, until release
// is called. Every later update passes straight through, so only the first
// cut is held.
//
// The hook and the test meet on two named pipes rather than on files polled
// for: the hook's write to the first blocks until the test reads it, which is
// the event that the cut is paused, and its read of the second blocks until
// release writes it.
func pauseTheNextCut(t *testing.T, repository string) pausedCut {
	t.Helper()
	dir := t.TempDir()
	pausedPipe := filepath.Join(dir, "paused")
	releasePipe := filepath.Join(dir, "release")
	for _, pipe := range []string{pausedPipe, releasePipe} {
		if err := syscall.Mkfifo(pipe, 0o600); err != nil {
			t.Fatalf("making %s: %v", pipe, err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
input=$(cat)
[ "$1" = prepared ] || exit 0
case "$input" in *" HEAD") ;; *) exit 0 ;; esac
mkdir %[1]s 2>/dev/null || exit 0
echo paused >%[2]s
read line <%[3]s
exit 0
`, shellWord(filepath.Join(dir, "taken")), shellWord(pausedPipe), shellWord(releasePipe))
	hook := filepath.Join(repository, "hooks", "reference-transaction")
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatalf("installing the pausing hook: %v", err)
	}
	cut := pausedCut{paused: make(chan struct{})}
	go func() {
		// Opening the read end blocks until the hook opens the write end.
		data, err := os.ReadFile(pausedPipe)
		if err == nil && len(data) > 0 {
			close(cut.paused)
		}
	}()
	released := false
	cut.release = func() {
		if released {
			return
		}
		released = true
		// Non-blocking, so a release with no hook waiting fails at once
		// rather than hanging the teardown.
		f, err := os.OpenFile(releasePipe, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString("go\n")
		_ = f.Close()
	}
	t.Cleanup(cut.release)
	return cut
}

// shellWord quotes s for the hook's shell.
func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runningPush is a git push the test watches line by line.
type runningPush struct {
	waiting chan struct{}
	done    chan struct{}
	err     error
	buf     *lockedBuffer
}

func (p *runningPush) output() string { return p.buf.String() }

// startPush starts pushing branch to the gate and reports, as events, the
// receive command saying it is waiting and the push ending.
func startPush(t *testing.T, subject, branch string) *runningPush {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "git", "push", gate.RemoteName, branch)
	cmd.Dir = subject
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(subject, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(subject, ".gitconfig-absent"),
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("piping the push's standard error: %v", err)
	}
	push := &runningPush{waiting: make(chan struct{}), done: make(chan struct{}), buf: &lockedBuffer{}}
	cmd.Stdout = push.buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the push: %v", err)
	}
	go func() {
		announced := false
		lines := bufio.NewReader(stderr)
		for {
			line, err := lines.ReadString('\n')
			_, _ = push.buf.Write([]byte(line))
			if !announced && strings.Contains(line, machine.PhraseCopyBusy.Outcome) {
				announced = true
				close(push.waiting)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					_, _ = fmt.Fprintf(push.buf, "[reading the push's standard error: %v]\n", err)
				}
				break
			}
		}
		push.err = cmd.Wait()
		close(push.done)
	}()
	return push
}

// lockedBuffer is a buffer two goroutines may write and a third read.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
