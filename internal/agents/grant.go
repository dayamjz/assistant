package agents

// Grant is what an agent this package starts is permitted to do in the
// directory it is started in. It is decided by the role the process plays and
// by nothing a caller states, so a stage body cannot ask for more than its
// role carries: a review is never granted an edit, whatever its invocation
// says.
//
// The grant is the least each role needs. A review and every other stage
// invocation reads and reports, so it is read-only. A fix round edits the
// files of the isolated copy it is pointed at and runs nothing, because the
// stage body that asked for it commits what changed and the stages after it
// run the configured commands. A worker edits its task's copy and commits and
// pushes there, so it is granted git as well, and the one command line its
// WorkerSpec names as its route back to the program, so it can say what it
// did without a person answering a prompt. Its grant allows those rather
// than denying everything else, so it does not narrow what the agent's own
// settings already allow, and git runs whatever a repository's hooks and
// configuration name. A subordinate coordinator coordinates and edits
// nothing: it reads, it is denied editing and git, and it is allowed the
// one command line its route names, the coordinator client, so what it does
// to a home through the program is what the service admits that client to.
// Like a review it is denied only what its grant names, so another command
// its agent's own settings allow stays allowed. The Claude Code adapter
// writes it as flags, the Cursor adapter holds every permission request to
// it, and the Codex adapter, which cannot, refuses a subordinate with
// ErrGrantUnenforced. Which of the two
// a started process runs under is the entry point's, OpenWorker or
// OpenSubordinate, and never the spec's: WorkerSpec.Grant reads a field only
// those set.
//
// What a grant buys is the command line an adapter writes for it, and an
// adapter states its translation beside its argument builder. Claude Code,
// Codex, Cursor, and Grok each write one; the openai adapter writes none, so
// what that agent may do is still its own defaults and settings, and a fix
// round there edits only where those allow it. Enforcing that command line is
// the agent's own permission system and not this package's, so a grant is
// only as strong as the agent's reading of the flags it was given.
// config.ReservedAgentFlags refuses an entry naming the flags a grant is
// written with, the permission-mode and tool-list flags, so a configured
// entry cannot contradict them. That is all it refuses: an entry may still
// name another of the CLI's flags that widens what an agent can do under a
// grant, such as one adding MCP servers whose tools no grant's list names or
// one naming a tool that answers the permission prompts a grant relies on
// going unanswered, and the tools the CLI's own settings enable reach an
// agent the same way.
type Grant int

const (
	// GrantRead reads and reports. Its translation denies the agent's own
	// editing tools and its shell; a tool outside what the translation names
	// is not ruled out.
	GrantRead Grant = iota
	// GrantEdit edits files and runs no command.
	GrantEdit
	// GrantWorker edits files and runs git, and commands beginning with
	// WorkerSpec.Route when the spec names one. It adds those to what the
	// agent may run and takes nothing away. Which command the route names
	// is the caller's, and the translation allows a prefix of a command
	// line, so what the route runs with whatever follows the prefix is the
	// routed program's to refuse.
	GrantWorker
	// GrantSubordinate reads and runs commands beginning with
	// WorkerSpec.Route when the spec names one, and is denied editing and
	// git, and, with no route, every command. It is a subordinate
	// coordinator's, which the route reaches the program through and which
	// edits nothing.
	GrantSubordinate
)

// String names the grant for a record or a test's failure message.
func (g Grant) String() string {
	switch g {
	case GrantRead:
		return "read"
	case GrantEdit:
		return "edit"
	case GrantWorker:
		return "worker"
	case GrantSubordinate:
		return "subordinate"
	}
	return "unknown"
}

// GrantFor is the grant an invocation of the given purpose runs under. A fix
// round is the one invocation that edits, whether a Fixer's session carries it
// or Runner.Run does in the session-free mode, so the grant follows the
// purpose rather than the entry point. Every other purpose, an unrecognized
// one included, is GrantRead, so a purpose added later is granted no edit
// until this function says so.
func GrantFor(p Purpose) Grant {
	if p == PurposeFix {
		return GrantEdit
	}
	return GrantRead
}
