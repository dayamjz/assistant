//go:build unix

package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
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

// Who the gate admits a push from, decided from the pushing process's
// parent chain as the kernel reports it and from nothing the push says. The
// pushes here are real git pushes through the hooks internal/gate wrote,
// made from processes shaped the way the chain they are about is shaped: a
// shell in the foreground of a terminal, a script that shell runs with the
// push as a background job, a process whose chain ends at a daemon with no
// terminal on it, and a worker's command the kernel reparented. The worker
// control, a push the worker runs and waits for, is
// TestAWorkersPushIsHeldToItsOwnBranchForwardAndAPersonsIsNot and is run
// again inside the reparented case, so the one difference between the
// admitted push and the refused one there is the chain. Every shape is
// read back off the kernel before the push is released, so a test cannot
// pass on a pusher that was not the shape it claims.

// gitAtTerminal runs git in dir from a shell in the foreground of a
// terminal, the shape a person's shell gives a job, and returns what the
// push printed and whether it failed.
func gitAtTerminal(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	got := runAtTerminal(t, append([]string{"git", "-C", dir}, args...)...)
	out := got.stdout + got.stderr
	if got.status != 0 {
		return out, exitStatus(got.status)
	}
	return out, nil
}

// exitStatus is a non-zero status as an error, for the helpers that return
// what a command came to.
type exitStatus int

func (e exitStatus) Error() string { return "exited with status " + strconv.Itoa(int(e)) }

// A person's push from a shell in the foreground of a terminal is admitted
// and starts the branch's run, and so is a push a script of that shell runs
// as a background job: the job is in a process group of its own that the
// terminal was not handed to, which is read back off the kernel, and its
// parent shell holds the terminal's foreground, so the chain and not the
// pusher's own process is what places it. The gate's references are read
// with git after each push.
func TestAPushFromAShellAtATerminalIsAdmittedThroughTheChain(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	recordRepository(t, h, subject)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	withService(t, h, func(serviceUnderTest) {
		// The subject stands on its work branch, as newSubject leaves it.
		head := commitFile(t, subject, "work.txt", "a change from a shell\n")
		out, err := gitAtTerminal(t, subject, "push", gate.RemoteName, "work")
		if err != nil {
			t.Fatalf("a push from a shell at a terminal was refused:\n%s", out)
		}
		if !strings.Contains(gateRefs(t, gateRepo), "refs/heads/work "+head) {
			t.Fatalf("the admitted push did not move the gate's branch to %s:\n%s", head, gateRefs(t, gateRepo))
		}

		// A script the shell runs, with job control on so that the push it
		// backgrounds is in a group of its own, as an interactive shell's
		// background job is. The job waits to be released, and while it
		// waits its shape is read off the kernel by the real reader: the job
		// in a group the terminal was not handed to, and the shell, blocked
		// in its wait builtin and forking nothing that could take the
		// terminal, holding the foreground. Only then is the push released.
		next := commitFile(t, subject, "work.txt", "a change from a script's background job\n")
		dir := t.TempDir()
		jobPID, shellPID, release := filepath.Join(dir, "job"), filepath.Join(dir, "shell"), filepath.Join(dir, "release")
		pusher := "while [ ! -e " + shellQuote(release) + " ]; do sleep 0.02; done; exec git -C " + shellQuote(subject) +
			" push " + shellQuote(gate.RemoteName) + " work"
		script := "set -m; echo $$ > " + shellQuote(shellPID) + "; /bin/sh -c " + shellQuote(pusher) + " & " +
			"echo $! > " + shellQuote(jobPID) + "; wait $!"
		wait := startAtTerminal(t, "/bin/sh", "-c", script)
		job, shell := readNumber(t, jobPID), readNumber(t, shellPID)
		jobRead, found, err := agents.ReadLineage(job)
		if err != nil || !found {
			t.Fatalf("reading the script's background job %d: %v, %v", job, found, err)
		}
		shellRead, found, err := agents.ReadLineage(shell)
		if err != nil || !found {
			t.Fatalf("reading the script's shell %d: %v, %v", shell, found, err)
		}
		if jobRead.Group == shellRead.Group {
			t.Fatalf("the script's background job runs in the shell's own group %d, so this is not the shape the test is about", shellRead.Group)
		}
		if jobRead.Foreground() || !shellRead.Foreground() || jobRead.TerminalGroup != shellRead.Group {
			t.Fatalf("the job reads %+v and its shell %+v; want the shell holding the terminal's foreground and the job "+
				"outside it, so that the chain and not the job's own process is what places the push", jobRead, shellRead)
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got := wait()
		if got.status != 0 {
			t.Fatalf("the push a script backgrounded was refused:\n%s%s", got.stdout, got.stderr)
		}
		if !strings.Contains(gateRefs(t, gateRepo), "refs/heads/work "+next) {
			t.Fatalf("the admitted push did not move the gate's branch to %s:\n%s", next, gateRefs(t, gateRepo))
		}
	})
}

// A push from a process the service can place as neither a task's worker
// nor a shell at a terminal is refused: the pusher's chain ends at a daemon,
// a process in a session of its own with no controlling terminal whose own
// starter has exited, which is the chain git-receive-pack has under sshd
// and the chain a cron job or any daemon gives a push. The chain is read
// back off the kernel before the push is released, so that it holds no
// terminal and does not reach this test. The refusal carries the typed
// reason, the gate's row, and the chain that was read, and the gate's
// references are as they were (P6): a refused push is sent again, while an
// admitted stranger's push cannot be undone.
func TestAPushWhoseChainEndsAtADaemonIsRefusedAndMovesNothing(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	quietMaintenance(t, subject)
	recordRepository(t, h, subject)
	gateRepo := gateRepositoryOf(t, h, subject)
	quietMaintenance(t, gateRepo)
	withService(t, h, func(serviceUnderTest) {
		// The subject stands on its work branch, as newSubject leaves it.
		head := commitFile(t, subject, "work.txt", "a change from a daemon\n")
		before := gateRefs(t, gateRepo)

		out, status := gitDaemonized(t, subject, "push", gate.RemoteName, "work")
		if status == 0 {
			t.Fatalf("a push whose chain ends at a daemon was admitted:\n%s", out)
		}
		for _, want := range []string{string(ipc.CodeUnplaceable), machine.PhraseSenderUnplaced.Outcome,
			"was read along its parent chain", "process "} {
			if !strings.Contains(out, want) {
				t.Errorf("the refusal does not carry %q:\n%s", want, out)
			}
		}
		if got := gateRefs(t, gateRepo); got != before {
			t.Fatalf("the refused push changed the gate's references:\nbefore: %s\nafter:  %s", before, got)
		}
		if strings.Contains(gateRefs(t, gateRepo), head) {
			t.Fatalf("the refused push landed its commit %s in the gate", head)
		}
	})
}

// A worker's push whose chain the kernel rewrote is refused as nobody's,
// rather than admitted as a person's as it used to be. The control is the
// same worker's push of the same branch from the same copy, run and waited
// for, which is admitted under the worker rule; then the worker backgrounds
// a push and lets its shell exit, the kernel reparents the push's shell away
// from the worker, which is read back off the kernel before the push is
// released, and the same push is refused with the typed reason and moves
// nothing. The commit stands in the worker's copy, so the refusal loses no
// work (P6), and the worker's own push is what gets it in.
func TestAWorkersPushOnAChainTheKernelRewroteIsRefusedNotAdmittedAsAPersons(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	launchedWorker(t, "task/orphaned", func(w launched) {
		copyPath := resolvedCopy(t, w.home, w.task)
		quietMaintenance(t, copyPath)
		if err := os.WriteFile(filepath.Join(copyPath, "work.txt"), []byte("the worker's change\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGitAs(t, w, copyPath, "add", "-A")
		mustGitAs(t, w, copyPath, "commit", "--quiet", "-m", "the worker's change")
		head := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
		if out, err := gitAs(t, w, copyPath, "push", gate.RemoteName, "task/orphaned"); err != nil {
			t.Fatalf("the worker's own push, the control, was refused:\n%s", out)
		}
		if !strings.Contains(gateRefs(t, w.gateRepo), "refs/heads/task/orphaned "+head) {
			t.Fatalf("the control push did not move the gate's branch to %s:\n%s", head, gateRefs(t, w.gateRepo))
		}
		before := gateRefs(t, w.gateRepo)

		mustGitAs(t, w, copyPath, "commit", "--quiet", "--allow-empty", "-m", "the worker's change, pushed from an orphan")
		next := mustGitAs(t, w, copyPath, "rev-parse", "HEAD")
		dir := t.TempDir()
		pid, shell, release := filepath.Join(dir, "pid"), filepath.Join(dir, "shell"), filepath.Join(dir, "release")
		out, errOut, code := filepath.Join(dir, "out"), filepath.Join(dir, "err"), filepath.Join(dir, "code")
		pusher := "while [ ! -e " + shellQuote(release) + " ]; do sleep 0.02; done; " +
			"git -C " + shellQuote(copyPath) + " push " + shellQuote(gate.RemoteName) + " task/orphaned > " +
			shellQuote(out) + " 2> " + shellQuote(errOut) + "; echo $? > " + shellQuote(code+".tmp") +
			" && mv " + shellQuote(code+".tmp") + " " + shellQuote(code)
		// The worker's shell starts the pusher, says which processes they
		// are, and exits, which is what hands the pusher to the kernel.
		ran := asWorker(t, w, "echo $$ > "+shellQuote(shell)+"; nohup /bin/sh -c "+shellQuote(pusher)+
			" >/dev/null 2>&1 & echo $! > "+shellQuote(pid+".tmp")+" && mv "+shellQuote(pid+".tmp")+" "+shellQuote(pid))
		if ran.Exit != 0 {
			t.Fatalf("the worker's shell exited %d: %s%s", ran.Exit, ran.Stdout, ran.Stderr)
		}
		orphan := awaitReparented(t, readNumber(t, pid), readNumber(t, shell))
		if orphan.Foreground() {
			t.Fatalf("the orphaned pusher reads %+v, in the foreground of a terminal, so this is not the shape the test is about", orphan)
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		status := awaitNumber(t, code)
		pushed, _ := os.ReadFile(out)
		refused, _ := os.ReadFile(errOut)
		printed := string(pushed) + string(refused)
		if status == 0 {
			t.Fatalf("the worker's push on a rewritten chain was admitted:\n%s", printed)
		}
		if !strings.Contains(printed, string(ipc.CodeUnplaceable)) {
			t.Fatalf("the push on a rewritten chain was refused for another reason than %s:\n%s", ipc.CodeUnplaceable, printed)
		}
		// The wire renders a refusal's code in parentheses after its message,
		// and the worker rule's refusal names the branch the worker may move.
		if strings.Contains(printed, "("+string(ipc.CodeWorker)+")") || strings.Contains(printed, "may update only refs/heads/") {
			t.Fatalf("the push on a rewritten chain met the worker rule, so the chain was followed to the worker after all:\n%s", printed)
		}
		if got := gateRefs(t, w.gateRepo); got != before {
			t.Fatalf("the refused push changed the gate's references:\nbefore: %s\nafter:  %s", before, got)
		}
		if got := mustGitAs(t, w, copyPath, "rev-parse", "HEAD"); got != next {
			t.Fatalf("the worker's copy stands at %s after the refusal, want its commit %s", got, next)
		}
	})
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
	pusher := "echo $$ > " + shellQuote(pid+".tmp") + " && mv " + shellQuote(pid+".tmp") + " " + shellQuote(pid) + "; " +
		"while [ ! -e " + shellQuote(release) + " ]; do sleep 0.02; done; " +
		"git -C " + shellQuote(dir) + " " + shellQuote(args...) + " > " + shellQuote(out) + " 2> " + shellQuote(errOut) +
		"; echo $? > " + shellQuote(code+".tmp") + " && mv " + shellQuote(code+".tmp") + " " + shellQuote(code)
	// The starter is in a session of its own, so neither it nor what it
	// starts has a controlling terminal; it backgrounds the pusher and
	// exits, which is what makes the pusher a daemon's orphan.
	starter := exec.Command("/bin/sh", "-c", "/bin/sh -c "+shellQuote(pusher)+" >/dev/null 2>&1 &")
	starter.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	starter.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
	)
	if raw, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("starting the daemon: %v: %s", err, raw)
	}
	daemon := awaitReparented(t, readNumber(t, pid), starter.Process.Pid)
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
	status := awaitNumber(t, code)
	pushed, _ := os.ReadFile(out)
	refused, _ := os.ReadFile(errOut)
	return string(pushed) + string(refused), status
}

// awaitReparented waits until the process pid names is no longer the child
// of starter, which is the kernel having rewritten its chain, and returns
// its lineage as read then.
func awaitReparented(t *testing.T, pid, starter int) agents.Lineage {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		lineage, found, err := agents.ReadLineage(pid)
		if err != nil || !found {
			t.Fatalf("process %d is gone before the kernel rewrote its chain: %v, %v", pid, found, err)
		}
		if lineage.Parent != starter {
			return lineage
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process %d is still the child of %d", pid, starter)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitNumber waits for path to hold a number and returns it.
func awaitNumber(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return readNumber(t, path)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readNumber reads the number path holds, waiting for the file to appear.
func readNumber(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
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
		time.Sleep(5 * time.Millisecond)
	}
}

// commitFile writes a file in the working copy and commits it on the branch
// it stands on, returning the commit.
func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "a change to "+name)
	return git(t, dir, "rev-parse", "HEAD")
}
