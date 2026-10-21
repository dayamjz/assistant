//go:build unix

package standin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// runScript is a worker that runs whatever follows "run: " on a line.
func runScript() Script {
	return Script{Worker: &WorkerScript{Exchanges: []Exchange{{Contains: "run: ", Run: true}}}}
}

// waitRan polls a worker's output until it carries the Ran of command, in
// either mode: a line-mode worker prints the Ran as its own line, and a
// stream-mode worker carries it as the text of a result line.
func waitRan(t *testing.T, w agents.Worker, command string) Ran {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, line := range strings.Split(w.Capture().Text, "\n") {
			text := line
			var turn struct {
				Type   string `json:"type"`
				Result string `json:"result"`
			}
			if json.Unmarshal([]byte(line), &turn) == nil && turn.Type == "result" {
				text = turn.Result
			}
			var ran Ran
			if json.Unmarshal([]byte(text), &ran) == nil && ran.Command == command {
				return ran
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker never answered %q; its output is:\n%s", command, w.Capture().Text)
		}
		time.Sleep(time.Millisecond)
	}
}

// A Run exchange runs its command as the worker's child in a process group
// the worker does not lead, which is the shape #225 observed of the Claude
// Code CLI's Bash tool, in both worker modes. Every fact asserted is the
// command's own account of itself, written by the shell and read back off
// the worker's output: the shell's own identifier and its parent's, and the
// directory it ran in, beside the group the worker read for it from the
// operating system. The shell leads its group, its parent is the worker, and
// the worker's group is not the shell's, so a check placing the command by
// the worker's group alone would not place it.
func TestARunExchangeRunsItsCommandAsTheWorkersChildInAGroupOfItsOwn(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name  string
		build func(testing.TB, Script, ...agents.ClaudeOption) *Agent
	}{
		{"line mode", NewWorker},
		{"stream mode", New},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			agent := mode.build(t, runScript(), agents.WithTerminationGrace(300*time.Millisecond))
			dir := t.TempDir()
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: dir, Instructions: "work"})
			if err != nil {
				t.Fatalf("starting the worker: %v", err)
			}
			defer worker.Close()

			const command = `echo "$$ $PPID"; pwd -P`
			if err := worker.Send(t.Context(), "run: "+command); err != nil {
				t.Fatalf("Send: %v", err)
			}
			ran := waitRan(t, worker, command)
			lines := strings.Split(strings.TrimSpace(ran.Stdout), "\n")
			if ran.Exit != 0 || len(lines) != 2 {
				t.Fatalf("the command ended %+v, want two lines and status 0", ran)
			}
			ids := strings.Fields(lines[0])
			if len(ids) != 2 {
				t.Fatalf("the shell reported %q, want its identifier and its parent's", lines[0])
			}
			shell, err := strconv.Atoi(ids[0])
			if err != nil {
				t.Fatal(err)
			}
			parent, err := strconv.Atoi(ids[1])
			if err != nil {
				t.Fatal(err)
			}
			if parent != worker.Group() {
				t.Errorf("the shell's parent is %d, want the worker, %d", parent, worker.Group())
			}
			if ran.Group != shell {
				t.Errorf("the shell ran in group %d, want the group it leads, %d", ran.Group, shell)
			}
			if ran.Group == worker.Group() {
				t.Errorf("the shell ran in the worker's own group %d", ran.Group)
			}
			want, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if lines[1] != want {
				t.Errorf("the command ran in %q, want the worker's directory %q", lines[1], want)
			}
		})
	}
}

// A Run exchange whose command fails answers with the status, so a test
// asserting a refusal reads the command's failure rather than a missing
// answer; and a script a Run exchange cannot answer as written is refused
// when the Agent is built, for each of the two rules.
func TestARunExchangeReportsFailureAndRefusesAScriptItCannotAnswer(t *testing.T) {
	t.Parallel()
	agent := NewWorker(t, runScript(), agents.WithTerminationGrace(300*time.Millisecond))
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work"})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	const command = "echo refused >&2; exit 3"
	if err := worker.Send(t.Context(), "run: "+command); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ran := waitRan(t, worker, command); ran.Exit != 3 || ran.Stderr != "refused\n" {
		t.Fatalf("the failing command answered %+v, want status 3 and its standard error", ran)
	}

	for _, bad := range []Exchange{{Run: true}, {Contains: "run: ", Run: true, Reply: "done"}} {
		fake := &fatalTB{TB: t}
		run(func() { NewWorker(fake, Script{Worker: &WorkerScript{Exchanges: []Exchange{bad}}}) })
		if !fake.failed || !strings.Contains(fake.message, "Run exchange") {
			t.Errorf("a script with the exchange %+v was built, or refused as %q", bad, fake.message)
		}
	}
}

// #267: the one refusal of the Claude Code CLI's Bash check the stand-in
// models. A here-document carrying a report, braces and quotes, is refused
// without running, so a file it would have written is never there; the
// controls are the same here-document with no braces, and a plain command
// carrying braces and quotes, which both run.
func TestARunExchangeRefusesAHereDocumentCarryingJSON(t *testing.T) {
	t.Parallel()
	agent := New(t, runScript(), agents.WithTerminationGrace(300*time.Millisecond))
	dir := t.TempDir()
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: dir, Instructions: "work"})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	defer worker.Close()
	for _, c := range []struct {
		command string
		file    string
		refused bool
	}{
		{"cat > refused.json <<'REPORT'\n{\"summary\": \"x\"}\nREPORT", "refused.json", true},
		{"cat > plain.txt <<'REPORT'\nsummary x\nREPORT", "plain.txt", false},
		{`printf '%s' '{"summary": "x"}' > braces.json`, "braces.json", false},
	} {
		if err := worker.Send(t.Context(), "run: "+c.command); err != nil {
			t.Fatalf("Send: %v", err)
		}
		ran := waitRan(t, worker, c.command)
		_, statErr := os.Stat(filepath.Join(dir, c.file))
		switch {
		case c.refused && (ran.Refused == "" || ran.Exit != -1 || statErr == nil):
			t.Errorf("%q answered %+v and wrote its file (%v), want it refused unrun", c.command, ran, statErr)
		case !c.refused && (ran.Refused != "" || ran.Exit != 0 || statErr != nil):
			t.Errorf("%q answered %+v (%v), want it run", c.command, ran, statErr)
		}
	}
}

// PRD principle P15 for a Run exchange: the command runs in a process group
// the worker ends when the command returns, so a command that starts a
// child in the background and exits leaves nothing running once the worker
// has answered for it, in both worker modes. The child sends its output
// elsewhere, so nothing holds the command's pipes and only ending the group
// ends it; the worker stays open throughout, so its own ending is not what
// ended the child.
func TestARunExchangeEndsWhatItsCommandLeftInItsGroup(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	for _, mode := range []struct {
		name  string
		build func(testing.TB, Script, ...agents.ClaudeOption) *Agent
	}{
		{"line mode", NewWorker},
		{"stream mode", New},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			agent := mode.build(t, runScript(), agents.WithTerminationGrace(300*time.Millisecond))
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "work"})
			if err != nil {
				t.Fatalf("starting the worker: %v", err)
			}
			defer worker.Close()

			const command = "sleep 300 >/dev/null 2>&1 & echo $!"
			if err := worker.Send(t.Context(), "run: "+command); err != nil {
				t.Fatalf("Send: %v", err)
			}
			ran := waitRan(t, worker, command)
			child, err := strconv.Atoi(strings.TrimSpace(ran.Stdout))
			if ran.Exit != 0 || err != nil || child <= 0 {
				t.Fatalf("the command ended %+v, want status 0 and the child's identifier", ran)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				err := syscall.Kill(child, 0)
				if errors.Is(err, syscall.ESRCH) {
					return
				}
				if !time.Now().Before(deadline) {
					_ = syscall.Kill(child, syscall.SIGKILL)
					t.Fatalf("the child %d the command left in the background is still running after the worker answered (%v)", child, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
