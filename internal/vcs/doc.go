// Package vcs is the only place in this product that invokes git, apart from
// two packages that state in their own package comments why they may not ask
// it: internal/fixture, because a subject repository built with the code under
// validation could not show that code wrong, and internal/journey, because a
// harness that read a push back through this package would be reporting it
// agreeing with itself. Two named exceptions, and a third is a finding to
// raise rather than a precedent to follow.
//
// PRD section 8 gives this package that ownership, and the point of it is that
// two rules which are easy to forget at a call site are applied here once, on
// every invocation, and cannot be forgotten by a caller that never builds a
// command line.
//
// The surface is operations rather than command strings. A caller asks for a
// diff between two commits, not for an argument vector.
//
// # The two rules applied to every invocation
//
// Repositories are addressed explicitly. A hardened environment sets
// safe.bareRepository=explicit, which forbids git from discovering a bare
// repository from the working directory. Every call this package makes against
// a bare repository passes --git-dir naming it, so those calls work under that
// setting. Calls against a working copy pass -C naming its root.
//
// Explicit addressing is also why the process environment is filtered. A
// process started from a git hook inherits GIT_DIR, GIT_WORK_TREE, and
// GIT_INDEX_FILE pointing at whatever repository invoked the hook, and those
// would silently redirect an operation this package addressed at a different
// repository.
//
// The guarantee the filter buys is stated as a property of this package rather
// than as a description of how git behaves internally, because a reader can
// check the first against this repository and cannot check the second at all.
// An operation performed here takes each of the following from its own
// arguments, from the repository it was pointed at, and from the settings
// envFor writes, and not from what an ancestor process left in the
// environment:
//
//   - Which repository it resolves to.
//   - Where its configuration is read from.
//   - What a repository this package creates is built from.
//   - What it may run, and where it finds it.
//   - Where its own standard streams go.
//
// The variables removed to make that hold are named in redirectingVars and
// redirectingPrefixes in exec.go, grouped by which of the five they serve, so
// a reader can check this paragraph against the list. Those five are also the
// list's scope: a variable outside them, one that only tunes a timeout or a
// transport's TLS settings, is absent because it is out of scope rather than
// because it was missed. The list is written by hand, so a variable a later
// git introduces is not removed until it is added there. StripRedirecting
// applies that one list to an environment a caller is building for a process
// this package does not start, which is how internal/agents keeps a worker's
// own git in the copy the worker runs in; it removes the names on the list and
// proves nothing about the directory.
//
// Three keeps are deliberate, and each is this package's trust policy rather
// than a claim about what git does with them.
//
// GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM, and GIT_CONFIG_NOSYSTEM are kept
// because they are how a caller points git at a configuration file on purpose;
// this package's own tests rely on exactly that to isolate from a developer's
// real git configuration. A configuration file arriving that way is a trusted
// input in the same category as PATH and the git binary this package executes.
// The attributes-location variables GIT_ATTR_SOURCE, GIT_ATTR_GLOBAL, and
// GIT_ATTR_SYSTEM are kept for the same reason; what keeps attributes from
// selecting a program during a comparison is --no-ext-diff and --no-textconv
// on the diff command lines in diff.go and on the name listing
// UncommittedDigest uses. That digest reads a regular file's worktree bytes
// itself and does not ask git for a patch. A path that is not a regular
// file, a symlink, or a directory is not opened. A path classified as a
// regular file is opened without waiting for a writer where the platform
// can ask for that, and a descriptor that is not a regular file is not
// read. On a platform whose open cannot ask not to wait, a path replaced
// by a blocking special file after the stat can still wait. The filter is
// not what enforces either. A
// submodule checkout contributes the commit rev-parse HEAD prints in that
// checkout, so a ref is followed to a commit and the ref storage is that
// invocation's to read.
//
// GIT_PAGER and PAGER are kept because --no-pager on every command line is the
// single mechanism this package uses against a pager, and a second one would
// be two mechanisms answering one question.
//
// What the keeps cost is worth stating rather than implying away. The settings
// a kept configuration file carries are not closed by this filter, and
// init.templateDir is the one with teeth: it decides the hooks InitBare's
// repository is born with, which is what removing GIT_TEMPLATE_DIR closes on
// the direct route. Removing the configuration-location variables too would
// not change that, because a process able to set them is already able to set
// PATH or replace the git binary.
//
// Where an inherited variable bears on what an operation may run, this package
// applies one rule: it removes a variable that can widen what an operation is
// allowed to do, and it honors one that can only narrow it. Widening is an
// ancestor process choosing what runs, which is what PRD principle P7 forbids.
// Narrowing is an ancestor process protecting itself, and taking that away
// would be a loss with nothing bought.
//
// Both sides of that line are named so the rule is checkable against the list.
// On the removed side is GIT_ALLOW_PROTOCOL, and the cost is real: an operator
// who set it to restrict transports loses that restriction here, and an
// explicit option on this package would be the right home for it. There is no
// such option today. On the honored side is GIT_PROTOCOL_FROM_USER, which is
// absent from redirectingVars for that reason rather than by oversight.
//
// Every invocation is non-interactive. There is nobody to answer a prompt
// inside a pipeline, so a prompt is a hang rather than a question. What this
// package guarantees, and a reader can check in envFor:
//
//   - Standard input is os.DevNull, so anything git reads from it sees EOF
//     immediately.
//   - GIT_TERMINAL_PROMPT=0, so git does not read a credential from the
//     terminal.
//   - GIT_ASKPASS and SSH_ASKPASS are set to false, which exits non-zero
//     without writing a line, and SSH_ASKPASS_REQUIRE=never. Setting them is
//     what keeps a graphical askpass helper named in the user's environment or
//     git configuration from being run in their place; git then treats the
//     askpass as having failed and falls back to the terminal, which
//     GIT_TERMINAL_PROMPT=0 refuses. No credential is invented on the way.
//   - GIT_EDITOR and GIT_SEQUENCE_EDITOR are set to false, so an operation
//     that wants an editor gets one that exits non-zero rather than one that
//     waits.
//   - GIT_SSH_COMMAND carries -o BatchMode=yes, so ssh fails rather than
//     asking for a passphrase or a host key confirmation.
//   - --no-pager, so no invocation waits on a pager.
//
// Two residual gaps, stated rather than papered over. A credential helper
// configured in the user's git configuration is still invoked, because that is
// where legitimate credentials come from; if that helper itself blocks on a
// user, the invocation blocks with it, and its deadline, described below, is
// what ends it. And this package chooses the ssh command every invocation
// runs under, which overrides a core.sshCommand set in git configuration, so a
// caller that relies on core.sshCommand loses it here. A caller supplies its
// own with WithSSHCommand, which is the only route: an ssh command left in the
// environment is removed along with the rest of redirectingVars rather than
// adopted.
//
// # Every invocation is bounded and ended with its group
//
// PRD section 8 asks that every invocation of an external program carry a
// deadline and run in a process group the invoker ends when the call returns
// or times out. Every git this package starts goes through internal/procgroup
// for that, ServeReceivePack included. The deadline is the caller's context's,
// or the handle's invocation deadline (WithInvocationDeadline, which
// internal/service sets from the operator's git_deadline, and otherwise
// DefaultInvocationDeadline) where that context carries none, as one derived
// through context.WithoutCancel does not. On linux and darwin git is the
// leader of a group of its own, and the whole group is sent SIGKILL when git
// exits and when the deadline passes, before git is reaped, so a descendant
// still in the group neither outlives the call nor holds its output pipes.
//
// What this does not cover is procgroup's to state, and its package comment
// does: a descendant that left the group is not ended, only stops being
// waited on after killGrace; a descendant meant to outlive the call is ended
// if it stayed in the group; when this process is killed outright the group
// goes on until it exits by itself; and on Windows only git itself is
// killed, and none of its descendants.
//
// # What this package deliberately does not do
//
// It does not implement the data-loss rules. Anchored force updates,
// incorporation checks, and the refuse-when-unverifiable path are policy and
// belong to internal/safety, the module named in PRD section 8. This package
// exposes the mechanism that policy needs - reading a remote ref, resolving a
// commit, comparing two commits, and answering whether a set of commits is
// already present in a tip by content - and stops there. It exposes all of
// it: *Repository satisfies safety.Git in full, and that package's own test
// fails the build if either side drifts.
//
// Incorporated is the content answer, and it is defined by its result and
// not by how it is reached: integrating these commits into this tip changes
// nothing. A conflict, a revision that does not resolve, and a git that
// cannot answer are each ErrInconclusive and never false, because the
// caller is a cleanup proof under PRD principle P12 and a false reading as
// "not landed" would be a refusal while an error reading as false would be
// a proof. Whether the tip is an up-to-date default branch is not a question
// this package answers; a caller fetches the tip, re-reads it with
// RemoteRefs, and passes the identifier it checked.
//
// Inventory is the other read a cleanup decision needs: what a working copy
// holds that could be work, as PRD section 5 defines a task copy's work.
// Its one exclusion, remote-tracking references, is stated on the type with
// its reason. It reports and decides nothing; the comparison against a
// dispatch's start snapshot and against a proof belongs to the caller.
//
// Push is mechanism on the same terms, and it is the one operation here that
// changes a remote. It performs one reference update under a lease and decides
// nothing about whether that update should happen: there is no shape of
// PushSpec that omits the lease, so a caller always carries an expectation
// about what it is overwriting, and where that expectation came from is
// internal/safety's question. Nothing here deletes a remote reference at all.
//
// Local operations write as well. Fetch writes references in the local
// repository, and a refspec beginning with + tells git to update one even when
// that is not a fast-forward; a caller passing such a refspec has chosen that.
// UpdateRef writes one full reference name at a commit already in the
// repository and fetches nothing, and CreateRef does the same only where the
// name does not exist yet. CommitAll and CommitAllWithBody record the working
// copy as a commit and move the branch or detached HEAD it was made on;
// SwitchToNewBranch and
// Detach move HEAD itself, to a new branch or to no branch; and AdvanceBranch
// and MoveBranch move one branch from a commit the caller names to another,
// as a compare-and-swap, with the working tree when it is checked out there.
// Nothing else here moves a branch in a working copy. MergeTreeAt writes the
// objects of the tree it merges and no reference. Whether any of it may
// proceed is the safety module's question, not this one's.
//
// Clone creates a repository, as InitBare does, and refuses a destination
// that already exists; what it promises is the remote set the caller named
// and nothing about the copy's remote-tracking references or branch
// configuration. Observe reads what a directory is as a repository, under
// this package's own environment, and so answers what the copy on disk is
// and never what a process started with some other environment would see
// there; StripRedirecting is the operation on that environment, and
// internal/tasks composes the two into its isolation assertion.
//
// # Errors
//
// A failed invocation returns a *CommandError naming the operation, the
// repository, the exit status, and git's own message. Both the arguments and
// the message pass through a Redactor first, so a URL carrying a password does
// not reach a log. PRD section 8 gives credential removal to a redact module,
// which is internal/redact, and a caller injects it with WithRedactor. A
// caller that injects nothing gets defaultRedactor, which covers exactly one
// shape, the userinfo of a URL with a scheme, and is what keeps this package
// usable on its own rather than the implementation a wired-up caller reaches.
//
// RemoteURL is the one function that returns a credentialed URL to its caller
// unredacted, because recovering that URL is what it is for.
//
// # Requirements
//
// A git binary on PATH, or one named with WithGitBinary. The commands and
// options used here need git 2.36 or newer, and nothing here checks that
// floor: an older git fails at whichever invocation it does not understand,
// as a *CommandError. Incorporated is the one operation that needs more,
// 2.38, and it checks its own floor before running and refuses with
// ErrUnsupportedGit below it, because its answer is one a caller removes a
// copy on and a git that misread the request must not produce one.
package vcs
