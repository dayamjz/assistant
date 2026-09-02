// Package vcs is the only place in this product that invokes git. PRD section
// 8 gives it that ownership, and the point of the ownership is that two rules
// which are easy to forget at a call site are applied here once, on every
// invocation, and cannot be forgotten by a caller that never builds a command
// line.
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
// git introduces is not removed until it is added there.
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
// on the diff command lines in diff.go, not this filter.
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
// user, the invocation blocks with it, and the caller's context deadline is
// what ends it. And this package chooses the ssh command every invocation
// runs under, which overrides a core.sshCommand set in git configuration, so a
// caller that relies on core.sshCommand loses it here. A caller supplies its
// own with WithSSHCommand, which is the only route: an ssh command left in the
// environment is removed along with the rest of redirectingVars rather than
// adopted.
//
// # What this package deliberately does not do
//
// It does not implement the data-loss rules. Anchored force updates,
// incorporation checks, and the refuse-when-unverifiable path are policy and
// belong to the safety module named in PRD section 8. This package exposes the
// mechanism that policy needs, which is reading a remote ref, resolving a
// commit, and comparing two commits, and stops there.
//
// Nothing here pushes, and no operation moves a branch in a working copy.
// Fetch is the exception worth naming: it writes references in the local
// repository, and a refspec beginning with + tells git to update one even when
// that is not a fast-forward. A caller passing such a refspec has chosen that,
// and this package does not second-guess it. Whether an update may proceed is
// the safety module's question, not this one's.
//
// # Errors
//
// A failed invocation returns a *CommandError naming the operation, the
// repository, the exit status, and git's own message. Both the arguments and
// the message pass through a Redactor first, so a URL carrying a password does
// not reach a log. PRD section 8 gives credential removal to a redact module,
// which does not exist yet; until it does, defaultRedactor is the
// implementation, and it covers exactly one shape: the userinfo of a
// URL with a scheme. A caller with a better redactor injects it with
// WithRedactor.
//
// RemoteURL is the one function that returns a credentialed URL to its caller
// unredacted, because recovering that URL is what it is for.
//
// # Requirements
//
// A git binary on PATH, or one named with WithGitBinary. The commands and
// options used here need git 2.36 or newer.
package vcs
