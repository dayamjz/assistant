//go:build unix

package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestAPushFromAProcessTheGateCannotPlaceIsRefusedInThePersonsWords is the
// gate's refusal of a push whose sender the service can place as neither a
// task's worker nor a shell at a terminal, read the way the person sending
// it reads it: through a real git push, from a process whose chain ends at
// a daemon with no terminal on it, which is the chain git-receive-pack has
// under sshd. What git relays from the hook is internal/machine's row for
// the refusal, then the service's own sentence naming the processes it
// read, and the wire code; the gate's references are read with git before
// and after and compared whole, because a refusal happens instead of a
// change (P6). The control is the same push from this test's own process,
// whose chain reaches a terminal's foreground, admitted on the terms every
// push always met.
func TestAPushFromAProcessTheGateCannotPlaceIsRefusedInThePersonsWords(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serveUntilStopped(t, h)
	repository := initialize(t, h, subject)

	git(t, subject, "checkout", "--quiet", "-b", "work")
	commitOn(t, subject, "work", "work.txt", "the change a daemon sends\n")
	before := gateRefs(t, repository)

	out, status := gitDaemonized(t, subject, "push", gate.RemoteName, "work")
	if status == 0 {
		t.Fatalf("a push from a process the gate cannot place was admitted:\n%s", out)
	}
	t.Logf("git push %s work from a daemon's chain:\n%s", gate.RemoteName, out)
	for _, want := range []string{
		machine.PhraseSenderUnplaced.Outcome, machine.PhraseSenderUnplaced.Consequence, machine.PhraseSenderUnplaced.Decision,
		"was read along its parent chain", "(" + string(ipc.CodeUnplaceable) + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("what the person reads does not carry %q", want)
		}
	}
	if after := gateRefs(t, repository); !slices.Equal(before, after) {
		t.Fatalf("the refused push changed the gate's references.\nbefore: %v\nafter:  %v", before, after)
	}

	// The control: the same change from this process, whose chain reaches
	// a terminal's foreground, is let in.
	pushed := git(t, subject, "push", gate.RemoteName, "work")
	if !strings.Contains(pushed, machine.PhraseAdmitted.Outcome) {
		t.Fatalf("the control push from this process was not admitted:\n%s", pushed)
	}
}

// gitDaemonized runs git in dir from a process whose chain ends at a daemon:
// a shell in a session of its own with no controlling terminal, whose
// starter exited before the push was released, so the chain from the push
// reaches neither a terminal nor this test. The shape is read back off the
// kernel before the push is released. It returns what the push printed and
// its exit status.
func gitDaemonized(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	files := t.TempDir()
	pid, release := filepath.Join(files, "pid"), filepath.Join(files, "release")
	out, errOut, code := filepath.Join(files, "out"), filepath.Join(files, "err"), filepath.Join(files, "code")
	pusher := "echo $$ > " + quoted(pid+".tmp") + " && mv " + quoted(pid+".tmp") + " " + quoted(pid) + "; " +
		"while [ ! -e " + quoted(release) + " ]; do sleep 0.02; done; " +
		"git -C " + quoted(dir) + " " + quotedAll(args) + " > " + quoted(out) + " 2> " + quoted(errOut) +
		"; echo $? > " + quoted(code+".tmp") + " && mv " + quoted(code+".tmp") + " " + quoted(code)
	// The starter is in a session of its own, so neither it nor what it
	// starts has a controlling terminal; it backgrounds the pusher and
	// exits, which is what makes the pusher a daemon's orphan.
	starter := exec.Command("/bin/sh", "-c", "/bin/sh -c "+quoted(pusher)+" >/dev/null 2>&1 &")
	starter.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	starter.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
	)
	if raw, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("starting the daemon: %v: %s", err, raw)
	}
	daemonPID := awaitNumberIn(t, pid)
	var daemon agents.Lineage
	for deadline := time.Now().Add(30 * time.Second); ; {
		lineage, found, err := agents.ReadLineage(daemonPID)
		if err != nil || !found {
			t.Fatalf("the daemon %d is gone before the kernel rewrote its chain: %v, %v", daemonPID, found, err)
		}
		if lineage.Parent != starter.Process.Pid {
			daemon = lineage
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the daemon %d is still the child of its starter %d", daemonPID, starter.Process.Pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if daemon.TerminalGroup != 0 {
		t.Fatalf("the daemon reads %+v, with a controlling terminal, so this is not the shape the test is about", daemon)
	}
	for current := daemon; ; {
		if current.PID == os.Getpid() {
			t.Fatal("the daemon's chain reaches this test, so it is not a daemon's chain")
		}
		if current.Parent <= 1 || current.Parent == current.PID {
			break
		}
		parent, found, err := agents.ReadLineage(current.Parent)
		if err != nil || !found {
			t.Fatalf("reading process %d above the daemon: %v, %v", current.Parent, found, err)
		}
		current = parent
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	status := awaitNumberIn(t, code)
	pushed, _ := os.ReadFile(out)
	refused, _ := os.ReadFile(errOut)
	return string(pushed) + string(refused), status
}

// awaitNumberIn waits for path to hold a number and returns it.
func awaitNumberIn(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatalf("%s holds %q, not a number", path, raw)
			}
			return n
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// quoted renders s as one POSIX shell word.
func quoted(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// quotedAll renders words as POSIX shell words separated by spaces.
func quotedAll(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = quoted(w)
	}
	return strings.Join(out, " ")
}
