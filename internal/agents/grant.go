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
// pushes there, so it is granted git as well, and no other command.
//
// What a grant buys is the command line an adapter writes for it, and an
// adapter states its translation beside its argument builder. Only the Claude
// Code adapter has one: the cursor, grok and openai adapters write no grant,
// so what their agents may do is still their own defaults and settings, and
// a fix round there edits only where those allow it. Enforcing that
// command line is the agent's own permission system and not this package's,
// so a grant is only as strong as the agent's reading of the flags it was
// given. config.ReservedAgentFlags refuses an entry naming the flags a grant
// is written with, so a configured entry cannot widen one.
type Grant int

const (
	// GrantRead reads and reports, and edits nothing.
	GrantRead Grant = iota
	// GrantEdit edits files and runs no command.
	GrantEdit
	// GrantWorker edits files and runs git, and no other command.
	GrantWorker
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
	}
	return "unknown"
}

// GrantFor is the grant an invocation of the given purpose runs under. A fix
// round is the one invocation that edits, whether a Fixer's session carries it
// or Runner.Run does in the session-free mode, so the grant follows the
// purpose rather than the entry point. Every other purpose, an unrecognized
// one included, reads only, so a purpose added later edits nothing until this
// function says so.
func GrantFor(p Purpose) Grant {
	if p == PurposeFix {
		return GrantEdit
	}
	return GrantRead
}
