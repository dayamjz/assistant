// Package gate owns the local bare repository that sits between a working copy
// and its real remote. PRD section 5's setup subsection is the contract: a
// gate is created, repaired, reattached, and removed here, and nothing else in
// this product decides where a gate lives or what it is born with.
//
// A gate is not an abstraction over git. It is a bare repository an ordinary
// git push reaches by name, plus the two hooks that make a push mean
// something: an admission check that runs before any reference in it changes,
// and a notification that runs after.
//
// This package composes internal/vcs rather than invoking git. It builds no
// command lines, and the one operation it needs that internal/vcs does not
// carry yet is declared as an interface here rather than added there; see
// git.go.
//
// # What this package does and does not do
//
// PRD section 5 lists five things initialization does. Three of them are here:
// the bare repository, the two hooks, and the assistant remote. Installing the
// agent-facing instructions and making sure the background service is running
// are the other two, and both belong to modules PRD section 8 names elsewhere,
// so a caller composing a full initialization performs them around this one.
//
// PRD section 8 gives this module three operations, initialize, refresh, and
// remove, and this package exposes two. Refresh is what Initialize does when
// the gate is already there, and per PRD principle P14 that is one behavior
// with one owner rather than two entry points that have to keep agreeing about
// what a correct gate looks like.
//
// Nothing here decides what admission means. The hooks invoke a command whose
// path a caller supplies, and hooks.go states the two subcommands and the one
// option that command has to accept. That contract is this package's
// requirement of the agent-facing command surface, not an implementation of
// it.
//
// # Your origin is never touched
//
// PRD principle P1 makes pushing to a named remote the consent boundary, so an
// ordinary git push has to behave exactly as it did before a gate existed.
// What this package does to keep that true is narrow: it sets one remote,
// named by RemoteName, and reads one remote of the same name. Every write it
// makes to a working copy goes through WorkingCopy.SetRemote, which internal/vcs
// documents as touching only the remote it names. No configuration outside
// that remote is written, and no reference in the working copy is moved.
//
// The claim is proved rather than asserted: a test pushes to origin after a
// gate is initialized and checks both that origin received the same refs it
// would have and that the gate's admission command was never invoked. That
// test is paired with one that pushes to the gate and sees the command run,
// because a negative check whose recorder never fires under any push would
// pass while proving nothing.
//
// # The hooks are the trust anchor
//
// Everything the gate enforces runs from the hooks it installs, so what those
// hooks contain, and whether they are the ones that run, is the whole of this
// package's security story. Three parts of it are worth stating exactly,
// including the part that is still open.
//
// The command the hooks invoke is an absolute path recorded at initialization
// time, never a name resolved through PATH. A push happens in whatever
// environment the person pushing has, and letting that environment choose
// which program admission runs would hand the decision to the pushed-from
// side, which PRD principle P7 forbids. It is written into the hook with
// forward slashes for the same reason: a shell searches PATH for a command
// word holding no slash, so on a host whose separator is a backslash the
// native spelling would arrive as a bare word and be looked up after all.
//
// No hook may enter a gate except from this package. Removing GIT_TEMPLATE_DIR
// from the environment, which internal/vcs does on every invocation, closes
// the direct route by which an ancestor process chooses what a repository is
// created with, but internal/vcs deliberately keeps GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM, and init.templateDir in a configuration file those point
// at chooses a template just as effectively. So Initialize lists the gate's
// hooks directory on both sides of the git call it makes and refuses with
// ErrTemplateHooks, naming init.templateDir, when a hook is there afterwards
// that was not there before.
//
// That refusal is written against the operation and not against the case that
// motivated it, and the difference is the whole of its value. Stated as "a
// repository this package creates must be born with no hooks" it held on
// creation and was silent on repair, while the channel it closes is open on
// every initialization: an existing gate is reinitialized too, and a template
// reaches it then just as well. Stated as "no hook may appear across this
// operation" it covers both, and covers whatever calls the operation next.
//
// The refusal matters more than its own size because of what it composes with.
// A hook this package did not write is preserved rather than discarded, which
// is how an operator's own pre-receive keeps running, and preservation is
// exactly what would promote an injected template hook into the admission
// chain: it would be moved to the .local name and invoked after admission on
// every push. The preservation rule and the template channel are only safe
// together, so the one that can be closed is closed everywhere.
//
// # What one side of an operation owes an operator, its sibling owes too
//
// Four times now something in this package has covered one path and not its
// sibling: ownership checked on adoption but not on removal, a hook guarded on
// creation but not on repair, a refusal that said what an action would cost
// when it was the destructive one and said nothing when it was the
// constructive one, and a recordless gate marked as inferred when a remote
// alone claimed it but not when a path hash alone did. Each was written where
// the problem was first noticed rather than around the operation, or the
// question, that carries the risk, and each left the sibling reachable and
// unaddressed.
//
// So when a rule about a gate is added here, ask what operation it constrains
// and put it there, not at the call that prompted it. The two questions that
// find this are: which other caller performs the same act, and what does this
// check do when the state it inspects was already there when the operation
// started. A guard scoped to a code path is a guard the next caller of that
// operation reopens without noticing.
//
// The same holds for what a refusal tells an operator, because a message is a
// guard whose enforcement is the reader. Whatever one side of an operation
// owes them, a caveat, a named action, or a cost, its sibling owes as well.
// The side that is easy to forget is usually the one where being wrong cannot
// be undone, which is why the forgetting is worth a rule rather than a fix
// each time.
//
// The near relative of that mistake is a signal that answers two conditions
// with one value. A gate whose record file is gone and a gate that is not
// there at all both read as "no record", and they call for opposite things:
// the first still holds every reference its runs produced and is repaired in
// place, the second holds nothing and must not be reported as a gate carried
// across a move. So what a path holds is a three-valued answer here rather
// than a boolean, and it is computed once. When a check reads state, ask what
// its "absent" answer covers, because absent and empty are rarely the same
// thing and the code that reads them cannot tell them apart afterwards.
//
// The part that is open is core.hooksPath. A configuration file reachable
// through the same two kept variables can point git at a hooks directory
// somewhere else entirely, and every hook this package installs is then inert
// while initialization reports success. This package cannot currently see
// that: reading a git configuration value is a git invocation, internal/vcs is
// the only package that makes those, and it exposes no such operation. The
// operation it needs is named in git.go along with the other one, and nothing
// here pretends to check in the meantime, because a check that cannot fail is
// worse than an admitted gap.
//
// # Identity, moving, and copying
//
// A gate's identifier starts as a stable hash of the working copy's path, as
// PRD section 8's on-disk layout describes, and Identify is the one place that
// computes it. It is a starting point rather than a standing truth: after a
// working copy moves, the gate keeps the identifier it was created with, so
// the run history recorded against it survives the move. What binds a gate to
// a working copy from then on is the record file inside the gate, and the
// remote in the working copy that points at it. Per PRD principle P14 that
// record is the owner of the binding, and the hash only seeds a new one.
//
// Reattachment and copying are the same question asked twice. When a working
// copy already names a gate under this home, Initialize reads that gate's
// record and asks whether the working copy the record names is still bound to
// it. A working copy that moved leaves nothing behind, so the claim is stale
// and the gate is reattached to the new path with its identifier and its
// contents intact. A working copy that was copied leaves the original in place
// and still bound, so the claim holds, and the copy gets its own gate at its
// own identifier rather than sharing the original's.
//
// # Every operation on a gate asks whose gate it is
//
// A remote and a path hash are both evidence a gate belongs to a working copy,
// and both are evidence a copy of that working copy inherits or reproduces
// exactly. The record inside the gate is the owner of the binding, so every
// operation here reads it and refuses with ErrGateClaimed when it names a
// different working copy that is still pointing at the gate. There is one
// implementation of that question and both operations call it.
//
// The destructive operation is the one that has to ask. An adoption that goes
// to the wrong working copy is loud and reversible: the gate's references stay
// where they are, and the working copy that lost the binding meets
// ErrGateClaimed the next time it initializes. A deletion is neither. This
// package shipped the check on adoption first and left deletion deciding on an
// inherited remote alone, which is the wrong way round, and the shape to watch
// for anywhere else it appears.
//
// So a working copy standing where a moved one used to stand hashes to the
// moved one's identifier and is refused rather than handed its gate, and a
// copied project directory, whose configuration names the original's gate, is
// refused rather than allowed to delete it.
//
// A gate carrying no record at all is adopted rather than refused, and that is
// a deliberate trade rather than an exception. A gate that lost its record and
// is reached only through a remote would otherwise be abandoned for a new
// empty one at the current path's hash, with everything recorded against the
// old identifier unreachable, because nothing here scans the home for a gate
// nobody names. What it costs is that with no record there is no ownership
// evidence left to weigh, so a copy holding the inherited remote can take a
// recordless gate over, and no reading of the two gates afterwards can tell
// that apart from a working copy that moved and lost the same file.
//
// So the adoption is recorded as one. The record this package then writes says
// the binding rested on one piece of evidence rather than two, that field is
// carried into every record an initialization reached the same way writes
// afterwards, and Remove refuses on it with ErrGateBindingInferred. Deleting is
// the one act here that cannot be undone, and an inferred binding does not
// justify it. Without that the adoption would manufacture the very evidence a
// later removal reads, and the eject that followed would delete somebody
// else's history with nothing having refused anything.
//
// One piece of evidence is what a remote alone is, because a copy inherits it,
// and it is equally what a path hash alone is, because a path outlives the
// working copy that stood on it and the next project to land there hashes the
// same. Those are siblings and they are marked alike: a recordless gate is
// adopted with an evidenced binding only where the remote and the path hash
// agree, and with an inferred one where only one of them speaks.
//
// What is left is loud in both directions. The copy is told it may not delete
// the gate, and is told the detachment that does succeed from where it stands.
// The original is refused with ErrGateClaimed the next time it initializes,
// because the copy is by then a working copy that still points at the gate.
// Neither loses a reference.
//
// The mark is not permanent, and it should not be. An initialization that has
// two pieces of evidence writes an evidenced binding over the inferred one: a
// record naming this working copy over a gate its path hashes to, or a
// recordless gate its remote and its path hash both point at. A mark that
// outlived the ambiguity would leave a gate whose owner is standing exactly
// where it is named for permanently unremovable.
//
// # A refusal has to name an action that succeeds
//
// Every refusal here is a dead end for somebody unless it says what to do, and
// the actions available differ by the state the operator is in. Removing a
// gate can itself be refused, so a refusal must not send an operator to a
// removal that will refuse them again: a chain of refusals is what makes
// somebody delete a directory by hand, and losing that directory is the loss
// every guard here exists to prevent.
//
// So the action each message names is the one that always succeeds from where
// the reader is: detaching, by dropping the assistant remote, which no guard
// refuses because it takes nothing away, followed by an initialization that
// gives that working copy a gate of its own. ErrNotAGate names an
// initialization where one would rebuild what is missing and a detachment
// where it would not. ErrGateClaimed names the detachment of the working copy
// that holds the gate, after which the gate is handed over, and says first
// what that costs the working copy losing it. ErrGateBindingInferred names the
// detachment of the asker, and leaves the deleting of the gate to the operator
// once they are satisfied whose history is in it. ErrMalformedRecord names the
// record file, because both operations refuse on it and detaching does not
// help, so removing that file is the only step that gets anywhere.
//
// # A gate never accepts a push that admission has not seen
//
// This is an invariant, not a description. A gate takes pushes from the moment
// its repository exists, and the admission hook is the only thing that makes a
// push mean anything, so a gate with no admission hook is not an unconfigured
// gate: it accepts everything, runs nothing, and tells nobody. No path here
// may leave one, including a path that is undoing what it just did.
//
// The invariant is held by filling the absence rather than by remembering to.
// Every initialization, before it can refuse for any reason, puts a hook that
// refuses every push into a gate that has none, and installs the real
// admission hook over it when it gets that far. So a refused initialization
// leaves a gate that admits nothing, and what the refusal cost is a closed
// gate rather than an open one.
//
// It is worth saying why this outranks the loss paths above. Losing history
// announces itself: something that was there is gone, and somebody notices. A
// gate with no admission behaves perfectly, accepts everything, and reports
// nothing in between, so the failure is invisible until the day it matters.
// Given a choice, fail in the direction that shows.
//
// The residual gap in identity is the path itself. The identifier is computed
// from the cleaned, symlink-resolved absolute path, so two spellings that
// differ only by a symlink agree. Two that differ only by letter case do not,
// so on a case-insensitive filesystem one working copy reached by two
// spellings can be given two gates. Resolving that needs a filesystem
// identity, not a path, and this package does not have one.
//
// # A hook somebody else wrote keeps running
//
// A gate whose repository already carries a pre-receive or post-receive hook
// this package did not write does not lose it. The hook is moved aside to the
// same name with CustomHookSuffix appended, and the installed hook runs it
// after admission with the same standard input and exits with its status. A
// hook already recognizable as this package's own is replaced instead, which
// is what makes repeated initialization repair rather than pile up.
//
// One shape is refused rather than resolved: a foreign hook present alongside
// an existing file at the .local name. Preserving both would mean choosing
// which one to discard, and this package does not discard a file somebody
// wrote. It returns ErrCustomHookConflict and leaves the repository alone.
//
// # Errors
//
// Every refusal here is a typed error a caller matches with errors.Is, and
// each one is returned before the mutation it refuses rather than after.
// Removal in particular checks that it can complete before it deletes
// anything, so a gate is never half-removed with the working copy still
// pointing at it.
package gate
