// Package knowledge is the one composition of a repository's curated project
// knowledge into what an agent is told: the section a launched worker's
// first turn carries and the section a run's review prompt carries. It is
// pure. It reads no file, runs no git, opens no database and sends nothing;
// internal/service reads the bytes and hands them here, and the two callers
// put the section where their own prompt order says.
//
// # What curated knowledge is
//
// It is one document, Document at a repository's root, that the people who
// own the repository write and merge like any other file. What makes it
// curated is where it is read from and nothing else: internal/service reads
// it only from the repository's default branch at a commit a fresh fetch
// resolved, the copy PRD section 10 calls trusted and P7 reads executable
// configuration from, and never from a pushed commit, a working copy, or an
// agent's output. So the one way to change what a worker or a reviewer is
// told is a change to the default branch; nothing here establishes that the
// branch passed the gate, because a direct push to the default branch is
// still possible, and the record of who changed it and why is the
// repository's own history. Nothing here or anywhere in this build lets an
// agent's assertion become knowledge on its own: there is no store of
// knowledge text, no verb that writes one, and no path from a worker's
// event, report or message into the document.
//
// A task's own session notes are not knowledge and are not composed here.
// They travel as the coordinator's messages, internal/store's message
// record, which is the one channel PRD section 6 gives steering.
//
// # What a composition is
//
// Read takes the commit the bytes were read at and the bytes, and returns a
// Knowledge holding the commit, the SHA-256 digest of the bytes exactly as
// the commit holds them, and the text passed through internal/redact. The
// digest is of the committed bytes rather than of the redacted text, so the
// commit and digest name what the default branch holds there; reproducing
// what a worker was told takes internal/redact as well as git, because the
// section is the redacted text. Absent
// is the composition of a commit whose tree holds no document, which is a
// repository with no curated knowledge: valid, composing nothing, refusing
// nothing.
//
// Section is the text a caller adds. It is empty for an absent document and
// for one holding only white space. Otherwise it says where the text came
// from, the commit and the document's path, and that the text grants
// nothing: it does not widen what the agent may run, change what the program
// checks, or outrank anything else the agent was told. The text is framed
// between two marker lines that name the document's own digest, so a line
// inside the document can pass for the end of it only by naming the digest
// of the document it stands in, and a heading in it reads as part of the
// document rather than as a section the program wrote. That is framing and
// not a boundary: the document is read from the default branch on the terms
// above, and an agent that does not keep to the frame is not stopped by it.
//
// # Refusals
//
// Read refuses a document larger than MaxBytes with ErrTooLarge and one that
// is not UTF-8 with ErrNotText. Neither is truncated or repaired, because a
// worker told part of a document, or a reviewer told text the document does
// not hold, has been told something nobody curated. internal/service turns
// either into a refusal of the launch or the run rather than proceeding
// without the document, and refuses a launch when the composed first turn
// would exceed the terminal launch bound internal/agents owns, with
// ErrLaunchTooLarge on the same terms.
//
// # Residual gaps
//
//   - The worker's copy and a run's copy are checkouts, and each holds its
//     own revision of Document. Nothing here or in internal/agents stops an
//     agent from opening that file itself, so what this package controls is
//     what the program tells an agent, not every file the agent can read.
//   - The section is guidance. Whether an agent follows it is the agent's;
//     no check reads an agent's work against it.
//   - internal/redact recognizes the shapes its own doc lists and no other,
//     so a credential of any other shape in the document reaches the agent.
//   - There is no compaction. A document within MaxBytes is composed whole
//     and one over it is refused, so keeping it within the bound is the
//     curators' job.
package knowledge
