package agents_test

import (
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

// grantFlags is what a command line said about the grant it ran under: the
// permission mode and both tool lists, read off the arguments the way the
// CLI reads a flag and its value.
type grantFlags struct {
	mode, allowed, denied string
}

func grantOf(args []string) grantFlags {
	value := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	return grantFlags{
		mode:    value("--permission-mode"),
		allowed: value("--allowedTools"),
		denied:  value("--disallowedTools"),
	}
}

var (
	readGrant   = grantFlags{mode: "default", denied: "Edit,Write,NotebookEdit,Bash"}
	editGrant   = grantFlags{mode: "acceptEdits", denied: "Bash"}
	workerGrant = grantFlags{mode: "acceptEdits", allowed: "Bash(git:*)"}
)

// Only a fix round edits. Every other purpose a stage can run, and a purpose
// nobody has defined, reads only, so a purpose added later edits nothing
// until GrantFor says so.
func TestOnlyAFixRoundIsGrantedEdits(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)
	readOnly := []agents.Purpose{
		agents.PurposeIntent, agents.PurposeRebase, agents.PurposeReview, agents.PurposeTest,
		agents.PurposeDocument, agents.PurposeLint, agents.PurposeChecks, agents.Purpose("later"),
	}
	for _, p := range readOnly {
		if got := agents.GrantFor(p); got != agents.GrantRead {
			t.Errorf("GrantFor(%q) = %s, want read", p, got)
		}
	}
	if got := agents.GrantFor(agents.PurposeFix); got != agents.GrantEdit {
		t.Errorf("GrantFor(fix) = %s, want edit", got)
	}
}

// The grant reaches the agent on the command line the real adapter wrote, as
// the agent read it. A review runs read-only, and a fix round is granted
// edits whether a Fixer's session carries it or Runner.Run does in the
// session-free mode, because the grant follows the purpose rather than the
// entry point. The review and the fix rounds disagree on every field, so each
// half is the other's control: a reading that found no flags at all would
// fail both.
func TestTheClaudeAdapterWritesEachRolesGrant(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)
	runner := newRunner(t)
	call := map[string]string{helperModeVar: "call"}

	review, err := runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, call))
	if err != nil {
		t.Fatalf("review invocation: %v", err)
	}
	sessionFree, err := runner.Run(t.Context(), agents.PurposeFix, invocation(t, agents.ShapeText, call))
	if err != nil {
		t.Fatalf("session-free fix invocation: %v", err)
	}
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening a fixer: %v", err)
	}
	round, err := fixer.Apply(t.Context(), invocation(t, agents.ShapeText, call))
	if err != nil {
		t.Fatalf("fix round: %v", err)
	}

	for _, c := range []struct {
		name string
		text string
		want grantFlags
	}{
		{"review", review.Text, readGrant},
		{"session-free fix", sessionFree.Text, editGrant},
		{"fixer session", round.Text, editGrant},
	} {
		args := decodeCall(t, c.text).Args
		if got := grantOf(args); got != c.want {
			t.Errorf("%s ran under %+v, want %+v (command line %q)", c.name, got, c.want, args)
		}
		if i := slices.Index(args, "--print"); i < 0 || i < slices.Index(args, "--permission-mode") {
			t.Errorf("%s: the grant is not ahead of the managed flags: %q", c.name, args)
		}
	}
}

// A worker runs under the worker grant: edits and git, and the stand-in
// records the command line it was started with.
func TestTheClaudeWorkerRunsUnderTheWorkerGrant(t *testing.T) {
	t.Parallel()
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond))
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "Work on the task in this copy.",
	})
	if err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, c := range agent.Calls() {
			if c.Worker {
				worker.Close()
				if got := grantOf(c.Args); got != workerGrant {
					t.Errorf("the worker ran under %+v, want %+v (command line %q)", got, workerGrant, c.Args)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			worker.Close()
			t.Fatalf("no worker call was recorded: %s", agent.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
