package guard

import "fmt"

// Exception is one function outside internal/procgroup that may name a
// starter, and why it cannot go through procgroup.
type Exception struct {
	// File is the path from the module root, with forward slashes.
	File string
	// Function is the declaration, on Use.Function's terms.
	Function string
	Reason   Reason
	// Why is what in this function makes Reason true of it, and what does
	// end what it starts. Where the function's own doc comment owns those
	// terms, Why points there rather than restating them.
	Why string
}

// Reason is why a program is started outside procgroup. Each value is one
// kind of start procgroup's terms do not fit, as its constant's comment
// says; the zero value is no reason and is refused by Wellformed.
type Reason int

const (
	// LongLivedAgent is an agent process: a worker, a Codex or Cursor
	// worker, or a fixer or review call. The owner's decision of 2026-10-21
	// on #430 makes it a declared exception to the invocation deadline: it
	// runs in a process group of its own with a recorded owner that ends
	// it, and the exception names the bound that does.
	LongLivedAgent Reason = iota + 1
	// ExternalCLIModel is internal/agents/standin's model of an external
	// command line's behaviour, which procgroup's end-at-return would
	// change rather than model. The owner's decision of 2026-10-21 on #455
	// names the stand-in's Run exchange as one, bounded by HangFor.
	ExternalCLIModel
	// OutlivesItsStarter is a process started to go on after the call that
	// started it returns, which procgroup, ending the group when the call
	// returns, would end.
	OutlivesItsStarter
	// OwnTerminal is a process that needs a session and a controlling
	// terminal of its own. procgroup sets the process attributes itself to
	// make a group and refuses a command whose attributes are already set.
	OwnTerminal
	// DocumentedGit is one of AGENTS.md's two documented exceptions to
	// "only internal/vcs invokes git": internal/fixture and internal/journey
	// read a subject with git independently of the code under validation.
	DocumentedGit
	// ProductUnderTest is internal/journey running the assistant binary
	// the way a person's shell does. A shell does not end a verb's process
	// group when the verb returns, so ending it here would hide from the
	// journey's process checks what the product left running.
	ProductUnderTest
)

// String names the reason the way a failure reads it.
func (r Reason) String() string {
	switch r {
	case LongLivedAgent:
		return "long-lived agent process (#430)"
	case ExternalCLIModel:
		return "model of an external command line (#455)"
	case OutlivesItsStarter:
		return "outlives the call that starts it"
	case OwnTerminal:
		return "needs a terminal of its own"
	case DocumentedGit:
		return "AGENTS.md's documented git exception"
	case ProductUnderTest:
		return "the product binary under test"
	}
	return fmt.Sprintf("Reason(%d)", int(r))
}

// known reports whether r is one of the constants above.
func (r Reason) known() bool { return r >= LongLivedAgent && r <= ProductUnderTest }

// Wellformed reports the first row of exceptions that names no file or
// function, carries a reason that is not one of the constants, or gives no
// Why, and a row that repeats another's file and function, which would
// leave one of the two unable to go stale.
func Wellformed(exceptions []Exception) error {
	seen := map[[2]string]bool{}
	for i, e := range exceptions {
		switch {
		case e.File == "" || e.Function == "":
			return fmt.Errorf("guard: exception %d names no file or no function", i)
		case !e.Reason.known():
			return fmt.Errorf("guard: exception %d (%s in %s) has %s, which is no reason", i, e.Function, e.File, e.Reason)
		case e.Why == "":
			return fmt.Errorf("guard: exception %d (%s in %s) says nothing in Why", i, e.Function, e.File)
		case seen[[2]string{e.File, e.Function}]:
			return fmt.Errorf("guard: exception %d repeats %s in %s", i, e.Function, e.File)
		}
		seen[[2]string{e.File, e.Function}] = true
	}
	return nil
}

// Exceptions is the closed list: every function outside internal/procgroup
// whose non-test code may name a starter. A row that covers no use fails the
// build as stale, so the list says what the module does and nothing more.
var Exceptions = []Exception{
	{
		File: "internal/agents/exec.go", Function: "runProcess", Reason: LongLivedAgent,
		Why: "A review or fix call. runProcess ends its group on completion, failure and cancellation, " +
			"politely and then by force (its doc comment and the package doc's \"An invocation owns its " +
			"process tree\" own the terms); its context is the caller's, and internal/service registers the " +
			"group through Invocation.Started and, past stop_grace, hurries its termination and ends it by force.",
	},
	{
		File: "internal/agents/worker.go", Function: "startWorker", Reason: LongLivedAgent,
		Why: "A worker or subordinate coordinator session, in a group of its own. Its owner ends it with " +
			"Worker.Close, its hurry ends it at once, and the group is ended once its leader exits; " +
			"internal/service ends the sessions it launched when its stop runs past stop_grace.",
	},
	{
		File: "internal/agents/cursorworker.go", Function: "(*cursorRunner).Start", Reason: LongLivedAgent,
		Why: "A Cursor ACP worker, on startWorker's terms: Close, its hurry, and the end of its group once its leader exits.",
	},
	{
		File: "internal/agents/codexworker.go", Function: "(*codexRunner).Start", Reason: LongLivedAgent,
		Why: "A Codex app-server worker, on startWorker's terms: Close, its hurry, and the end of its group once its leader exits.",
	},
	{
		File: "internal/agents/standin/run.go", Function: "runCommand", Reason: ExternalCLIModel,
		Why: "A Run exchange's command, in a group of its own and bounded by HangFor. The stand-in models the " +
			"Claude Code CLI's Bash tool as keeping a backgrounded child alive after the command returns, and " +
			"two internal/service placement tests rely on that child; ending the group at return would change " +
			"the model, which the owner declined on #455.",
	},
	{
		File: "internal/agents/standin/owner_unix.go", Function: "startSweep", Reason: OutlivesItsStarter,
		Why: "The owner sweep, which runs on after the test binary that starts it has exited, in a session of " +
			"its own, to end what that binary left; its doc comment owns the terms.",
	},
	{
		File: "internal/agents/standin/terminal_unix.go", Function: "AtTerminal", Reason: OwnTerminal,
		Why: "Runs the test binary again as the leader of a session whose controlling terminal is a " +
			"pseudo-terminal, so its tests push from a terminal's foreground; the child watches this binary " +
			"as its owner and ends with it (watchOuter).",
	},
	{
		File: "internal/cli/service.go", Function: "(*invocation).launch", Reason: OutlivesItsStarter,
		Why: "The service itself, detached from the command that launches it; launch's doc comment owns what " +
			"ends it, under the owner's decision on #430.",
	},
	{
		File: "internal/fixture/git.go", Function: "(*gitRunner).run", Reason: DocumentedGit,
		Why: "The fixture's own git, for plants and reads independent of internal/vcs.",
	},
	{
		File: "internal/journey/subject.go", Function: "GitWith", Reason: DocumentedGit,
		Why: "The journey's read of a subject with git, through fixture.GitInvocation and not internal/vcs.",
	},
	{
		File: "internal/journey/journey.go", Function: "(*Journey).exec", Reason: ProductUnderTest,
		Why: "One verb of the product binary, whose leader this harness kills only when a caller's within runs out.",
	},
	{
		File: "internal/journey/journey.go", Function: "(*Journey).Serve", Reason: ProductUnderTest,
		Why: "The service in the foreground of a child this harness owns, ended by Kill; Serve's doc comment owns the terms.",
	},
}
