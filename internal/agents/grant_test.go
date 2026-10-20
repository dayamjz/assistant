package agents_test

import (
	"context"
	"errors"
	"slices"
	"strings"
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
// records the command line it was started with. A worker whose spec names a
// route back to the program is also allowed commands beginning with it, in
// the same list and written as the spec gave it, so the rule the CLI reads
// and the prefix a worker is told to type are the same text; the worker with
// no route is the control, granted git and nothing more.
func TestTheClaudeWorkerRunsUnderTheWorkerGrant(t *testing.T) {
	t.Parallel()
	const route = "/opt/assistant/bin/assistant --home /var/lib/assistant worker"
	for _, c := range []struct {
		name  string
		route string
		want  grantFlags
	}{
		{"no route", "", workerGrant},
		{"a route", route, grantFlags{mode: "acceptEdits", allowed: "Bash(git:*),Bash(" + route + ":*)"}},
	} {
		agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
			agents.WithTerminationGrace(300*time.Millisecond))
		worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
			Dir: t.TempDir(), Instructions: "Work on the task in this copy.", Route: c.route,
		})
		if err != nil {
			t.Fatalf("%s: starting the worker: %v", c.name, err)
		}
		args := workerArgs(t, agent)
		worker.Close()
		if got := grantOf(args); got != c.want {
			t.Errorf("%s: the worker ran under %+v, want %+v (command line %q)", c.name, got, c.want, args)
		}
	}
}

// workerArgs waits for the stand-in's record of the worker it was started
// as, and returns the command line it read.
func workerArgs(t *testing.T, agent *standin.Agent) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, c := range agent.Calls() {
			if c.Worker {
				return c.Args
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no worker call was recorded: %s", agent.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A route that would change the shape of the tool list it is written into
// is refused before anything starts, as an invalid invocation naming the
// field: a comma would end the entry, a parenthesis close it, and a line
// break or surrounding white space is not the prefix a worker types.
func TestAWorkerRouteThatWouldReshapeTheToolListIsRefused(t *testing.T) {
	t.Parallel()
	for _, route := range []string{
		"/bin/assistant worker,Bash", "/bin/assistant worker)", "/bin/(x)/assistant",
		"/bin/assistant\nworker", " /bin/assistant worker", "/bin/assistant worker ",
	} {
		err := agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Work.", Route: route}.Validate()
		if !errors.Is(err, agents.ErrInvalidInvocation) || !strings.Contains(err.Error(), "Route") {
			t.Errorf("route %q = %v, want an invalid invocation naming Route", route, err)
		}
	}
	if err := (agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Work.", Route: "'/a b/assistant' --home /h worker"}).Validate(); err != nil {
		t.Errorf("a quoted route with spaces was refused: %v", err)
	}
}

// A subordinate coordinator runs under the subordinate grant, which only
// the subordinate entry point selects: started through OpenSubordinate with
// a route, the Claude Code adapter writes the default permission mode, the
// editing tools denied and the route alone allowed, so it edits nothing,
// runs no git, and runs the coordinator client without a prompt; with no
// route it is denied the shell too, which is the read grant's list. The
// same spec through OpenWorker runs under the worker grant, which is the
// control and what a caller can select: WorkerSpec.Grant is the worker's
// for every spec a caller builds.
func TestTheClaudeSubordinateRunsUnderTheSubordinateGrant(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)
	const route = "/opt/assistant/bin/assistant --home /var/lib/assistant coordinator"
	if got := (agents.WorkerSpec{Route: route}).Grant(); got != agents.GrantWorker {
		t.Fatalf("a spec a caller builds reads grant %s, want the worker's: the subordinate grant is the entry point's alone", got)
	}
	for _, c := range []struct {
		name  string
		route string
		open  func(context.Context, agents.Runner, agents.WorkerSpec) (agents.Worker, error)
		want  grantFlags
	}{
		{"a subordinate with a route", route, agents.OpenSubordinate,
			grantFlags{mode: "default", denied: "Edit,Write,NotebookEdit,Bash(git:*)", allowed: "Bash(" + route + ":*)"}},
		{"a subordinate with no route", "", agents.OpenSubordinate, readGrant},
		{"a worker with the same route", route, agents.OpenWorker,
			grantFlags{mode: "acceptEdits", allowed: "Bash(git:*),Bash(" + route + ":*)"}},
	} {
		agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
			agents.WithTerminationGrace(300*time.Millisecond))
		worker, err := c.open(t.Context(), agent.Runner(), agents.WorkerSpec{
			Dir: t.TempDir(), Instructions: "Coordinate the domain.", Route: c.route,
		})
		if err != nil {
			t.Fatalf("%s: starting: %v", c.name, err)
		}
		args := workerArgs(t, agent)
		worker.Close()
		if got := grantOf(args); got != c.want {
			t.Errorf("%s ran under %+v, want %+v (command line %q)", c.name, got, c.want, args)
		}
	}
}
