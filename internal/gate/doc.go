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
// command lines, and what it needs of a working copy is declared as an
// interface here rather than as a dependency on a concrete type; internal/vcs
// carries every operation so declared. See git.go, and the core.hooksPath gap
// below for the one operation it needs that cannot be declared there at all.
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
// One hook name is no longer reachable through that channel at all, which
// narrows where the refusal fires by closing the case rather than by catching
// it. Obtaining a gate seals it first, so a repair reaches the git call with
// the admission hook name already occupied by a hook this package wrote. What
// this package can state about that is what it checks afterwards: with a
// template configured that carries a pre-receive, the hook that runs on a push
// to the repaired gate is this package's admission and the template's hook is
// neither installed nor preserved into the chain.
// TestATemplateCannotDisplaceTheAdmissionHookDuringARepair holds that, and it
// proves the closure rather than the refusal.
//
// The refusal covers everything else, so a reader can tell where it still
// fires. Every other hook name arriving during a repair is refused with
// ErrTemplateHooks, which TestAnUnmanagedTemplateHookArrivingDuringARepairIsRefused
// holds at a name this package does not install. A repository that did not
// exist when the operation started is refused for every name, the admission
// hook included, because there was nothing there to seal beforehand; that is
// TestARepositoryBornCarryingHooksIsRefused.
//
// The ordering the narrowing rests on is a property of the call graph rather
// than a caution for a later reader, and every step of it is checkable by
// reading this package. ensureRepository has one caller, the body of an
// operation. That body is reached only through withGate, which acquires the
// gate and only then runs it. acquire is the only thing that produces the
// handle the body needs, and it seals every gate it observed from a deferred
// call, so the seal has completed before it returns. The gate an operation acts
// on is always one acquire observed, because the resolution can only return the
// gate the working copy names or the gate its path hashes to, and both are
// noted before either is resolved.
//
// What is left open is inside this package. The handle is a package-local type,
// so nothing outside can reach ensureRepository without going through the seam,
// while another function added here could construct one by hand and reach it
// without an acquisition. Nothing does. Closing that completely would take
// putting the handle behind a package boundary whose only exported constructor
// is the seam, which is not done here.
//
// The refusal matters more than its own size because of what it composes with.
// A hook this package did not write is preserved rather than discarded, which
// is how an operator's own pre-receive keeps running, and preservation is
// exactly what would promote an injected template hook into the admission
// chain: it would be moved to the .local name and invoked after admission on
// every push. The preservation rule and the template channel are only safe
// together, so the one that can be closed is closed everywhere.
//
// # One seam, and every operation on a gate goes through it
//
// Five times this package covered one path of an operation and not its
// sibling: ownership checked on adoption but not on removal, a hook guarded on
// creation but not on repair, a refusal that said what an action would cost
// when it was the destructive one and said nothing when it was the
// constructive one, a recordless gate marked as inferred when a remote alone
// claimed it but not when a path hash alone did, and the invariant below held
// by initialization and not by removal. Each was written where the problem was
// first noticed rather than around the operation that carries the risk.
//
// It was written down as a rule and then violated twice more, which says the
// rule was not what was missing. What was missing is a mechanism, so there is
// one now. Every operation here obtains its gate from one unexported seam and
// takes that handle rather than a path: no exported entry point takes a gate's
// path, only the home and the working copy the gate is asked about, and the
// seam is what resolves the repository, reads its contents once, asks and
// refuses on the ownership question, and leaves no gate it looked at accepting
// pushes with nothing checking them. An operation added later cannot skip any
// of that, because it cannot obtain a gate without going through it.
// This repository has solved this class twice the same way: internal/agents
// puts P4 in a type split rather than in a rule callers follow, and
// internal/safety makes an anchor's provenance a constructor rather than a
// value somebody remembers to check.
//
// The same holds for what a refusal tells an operator, because a message is a
// guard whose enforcement is the reader. Whatever one side of an operation
// owes them, a caveat, a named action, or a cost, its sibling owes as well.
// Where that has a mechanism it is used: every ErrMalformedRecord goes through
// one constructor that attaches the step that gets a reader out, because two
// producers of it did not attach one while this file promised that all of them
// did. Where it does not yet, it is still only a rule, and the record of this
// package says how far a rule gets.
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
// operation it needs is named in git.go, and nothing here pretends to check in
// the meantime, because a check that cannot fail is worse than an admitted
// gap.
//
// # Identity, moving, and copying
//
// A gate's identifier starts as a stable hash of the working copy's path, as
// PRD section 8's on-disk layout describes, and Identify is the one place that
// computes it. It is a starting point rather than a standing truth: after a
// working copy moves, the gate keeps the identifier it was created with, so
// the run history recorded against it survives the move. What binds a gate to
// a working copy from then on is what the next section describes, and the hash
// only seeds a new gate.
//
// Reattachment and copying are the same question asked twice. When a working
// copy already names a gate under this home, Initialize asks whether any other
// working copy is still bound to it. A working copy that moved leaves nothing
// behind, so nobody is, and the gate is reattached to the new path with its
// identifier and its contents intact. A working copy that was copied leaves the
// original in place and still bound, so somebody is, and the copy gets its own
// gate at its own identifier rather than sharing the original's.
//
// # Every operation on a gate asks whose gate it is, and asks the same way
//
// The question is whether another working copy is still bound to this gate,
// and it is asked once, by the seam, before either operation begins. An
// operation whose gate is held by somebody else is refused with
// ErrGateClaimed, except that an initialization holding a gate through an
// inherited remote falls back to a gate of its own instead, because there is
// one to hand out and taking the other would be the refusal.
//
// A remote and a path hash are not evidence of ownership and are not weighed as
// any. This package writes the remote, and a copy of a gated project inherits
// it byte for byte; the path hash is reproduced by whatever project lands on a
// path next. Both are how a gate is found, and neither says whose it is.
// Weighing something this package produced as though it were a second fact is
// how an adoption launders itself into a deletion.
//
// What the question is answered from is two records, neither believed alone.
// The ownership index in internal/store maps a working path to a gate, so the
// home can enumerate every working copy bound to a gate, which a gate filed
// under a hash of a path can never do from its own directory. The record inside
// the gate names the working copy the gate last belonged to, and it travels
// with the repository, so it still answers when the home's database has been
// lost or replaced. Every working copy either of them names is then checked
// against the present: it counts only if it is still there and its own
// assistant remote still names this gate. Checking rather than believing is
// what keeps two records from becoming two owners of one fact, per PRD
// principle P14; neither decides anything, and what they contribute is the list
// of working copies worth asking about.
//
// So a working copy standing where a moved one used to stand is refused rather
// than handed the moved one's gate, and a copied project directory, whose
// configuration names the original's gate, is neither given it nor allowed to
// delete it. Both hold whether or not the gate still carries its record, which
// is the part that needed the index: with the record gone there used to be no
// ownership evidence left to weigh at all.
//
// A gate carrying no record is repaired rather than abandoned or deleted. It
// still holds everything its runs recorded, and starting over with an empty
// gate at the current path's hash would leave all of that unreachable, because
// nothing here scans the home for a gate nobody names. So an initialization
// adopts it and writes the record back, and a removal refuses it with
// ErrNotAGate and names that initialization, which completes: the refusal is
// reached only after the seam established that nobody else is bound to the
// gate, so the removal after the repair succeeds. There is one such message
// now. There used to be two, because for one of the two askers the
// initialization it would have named led to a second refusal.
//
// # The residual gap is a path that outlives its working copy
//
// A working copy that moves and does not initialize anywhere afterwards leaves
// nothing behind saying where it went. The gate's record still names the path
// it left, the index's row is against that same path, and the working copy
// itself is only reachable from a path nothing here knows. The next project to
// stand on that path hashes to the same identifier and, from everything this
// package can read, is that working copy: it takes the gate, and can then
// delete it.
//
// That is stated rather than guarded because it cannot be told apart from the
// state it has to allow. A working copy that lost its assistant remote, over a
// gate that lost its record, standing at the path the gate is filed under, is
// the same three facts and must be repaired rather than refused. An earlier
// arrangement refused half of this, the half where the gate had lost its
// record, and let the ordinary half through; the half it let through is the
// common one, so what the refusal bought was mostly the appearance of a guard.
//
// One initialization at the new path closes it. That writes a binding the home
// can enumerate, and from then on the project landing on the freed path is
// refused with ErrGateClaimed even if the gate's own record is gone.
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
// what that costs the working copy losing it. ErrMalformedRecord names two
// steps and says which reader each is for, because which one gets anywhere
// depends on who is asking. For the working copy the gate is filed under, and
// for one that moved and would otherwise lose the history in it, the gate is
// reached by the path hash as well as by the remote, so detaching does not help
// and removing the record file is the step that does. For a working copy that
// reaches the gate only through an inherited remote, the path hash names a
// different, empty path, so detaching and initializing gives it a gate of its
// own and takes nothing from anyone. Naming only the first would instruct that
// operator to delete the record of another project's gate, and a refusal that
// instructs damage is worse than one that instructs nothing. Nothing here can
// tell the two askers apart where the refusal is built, which is why it names
// both rather than guessing; every producer goes through one constructor that
// attaches them, so a refusal with no action is not something a new producer
// can write by leaving something out.
//
// # A gate never accepts a push that admission has not seen
//
// This is an invariant, not a description. A gate takes pushes from the moment
// its repository exists, and the admission hook is the only thing that makes a
// push mean anything, so a gate with no admission hook is not an unconfigured
// gate: it accepts everything, runs nothing, and tells nobody. No path here
// may leave one, including a path that is undoing what it just did.
//
// The invariant is held by filling the absence rather than by remembering to,
// and it is held by both operations because both obtain their gate the same
// way. Obtaining one notes every gate the resolution looks at and, whatever it
// is on its way out with, puts a hook that refuses every push into any of them
// that has none. An operation that gets far enough installs the real admission
// hook over that. So a refused operation, of either kind, leaves a gate that
// admits nothing, and what the refusal costs is a closed gate rather than an
// open one.
//
// The seal is discharged before the operation begins rather than on its way
// out, and the ordering is what keeps a failure meaning one thing. A seal that
// fails is then a gate that was never obtained and an operation that has not
// started, so an unrelated gate's unreadable hooks directory cannot turn a
// completed operation into a reported failure, which is what it used to do.
//
// A seal that fails while a refusal is already in flight is joined onto that
// refusal rather than dropped behind it, and one gate's failure does not stop
// the other gates being sealed. Reporting only the refusal would say a gate was
// closed that is open, which is the one failure this section ranks above the
// rest.
//
// Doing it there rather than at each refusal is the other half. The refusals
// are many and the next one added would not have carried it, which is exactly
// the sibling-path shape above. It also means an operation seals a gate it is
// refusing to act on, including one another working copy owns: the only gate it
// changes is one that was already accepting everything with nothing running,
// and leaving that alone out of politeness would be choosing the silent failure
// over the loud one.
//
// One condition is outside what the seam can cover, and ensureRepository owns
// it: a repository that does not exist when the operation starts, between the
// git invocation that creates it and the hooks being installed. That window is
// left only by an I/O failure or the process dying, so it is the one guard here
// that has been reasoned about rather than watched to fail; ensureRepository
// says so where it is written.
//
// It is worth saying why this outranks the loss paths above. Losing history
// announces itself: something that was there is gone, and somebody notices. A
// gate with no admission accepts everything and reports nothing in between, so
// the failure is invisible until the day it matters. Worse, the notification
// hook still runs on such a push, so the run is recorded as one admission saw,
// which is not a missing check but a false record of a check that never
// happened. Given a choice, fail in the direction that shows.
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
// pointing at it, and it gives up the three handles it holds in order of how
// findable the gate stays without each one: the binding in the index, then the
// working copy's remote, then the repository. A reattached gate is filed under
// an identifier its working copy's path no longer hashes to, so its remote is
// the only handle on it, and a removal that gave that up first and then failed
// would leave the repository named by nothing.
//
// One write is common to every refusal and is stated once, in errors.go rather
// than in each of them: obtaining a gate seals any gate the resolution observed
// that has no admission hook. Where a refusal says nothing was created,
// written, or deleted, it means nothing beyond that.
package gate
