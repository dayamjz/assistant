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
// side, which PRD principle P7 forbids.
//
// A repository this package creates must be born with no hooks. Removing
// GIT_TEMPLATE_DIR from the environment, which internal/vcs does on every
// invocation, closes the direct route by which an ancestor process chooses
// what a created repository contains, but internal/vcs deliberately keeps
// GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM, and init.templateDir in a
// configuration file those point at chooses a template just as effectively.
// So Initialize inspects the hooks directory of a repository it has just
// created and refuses with ErrTemplateHooks when it holds any active hook,
// naming init.templateDir. That refusal exists because the alternative is
// worse than losing the template: a hook this package did not write would
// otherwise be preserved as somebody's custom hook and chained into the
// admission path, which is an ancestor process choosing code that runs inside
// the gate.
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
