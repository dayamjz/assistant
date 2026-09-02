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
// repository. The redirectingVars list names every variable removed from the
// environment of a child, and each one on it is documented by git. They fall
// into four groups:
//
//   - Which repository git operates on: GIT_DIR, GIT_WORK_TREE,
//     GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_ALTERNATE_OBJECT_DIRECTORIES,
//     GIT_COMMON_DIR, GIT_NAMESPACE, GIT_PREFIX, GIT_CEILING_DIRECTORIES, and
//     GIT_DISCOVERY_ACROSS_FILESYSTEM.
//   - Configuration injected into git: GIT_CONFIG, GIT_CONFIG_PARAMETERS,
//     GIT_CONFIG_COUNT, and the numbered GIT_CONFIG_KEY_n and
//     GIT_CONFIG_VALUE_n that go with the last of those.
//   - What a new repository is built from: GIT_TEMPLATE_DIR, whose hooks git
//     copies into a repository at the moment it creates it.
//   - A program git would run, or where git's own streams go: GIT_EXEC_PATH,
//     GIT_EXTERNAL_DIFF, GIT_EXTERNAL_DIFF_TRUST_EXIT_CODE, GIT_SSH,
//     GIT_SSH_VARIANT, and the Windows-only GIT_REDIRECT_STDIN,
//     GIT_REDIRECT_STDOUT, and GIT_REDIRECT_STDERR. DISPLAY is here too,
//     because git's askpass fallback consults it before deciding whether a
//     graphical helper is worth running.
//
// That list is written by hand, so it covers the variables on it and nothing
// else, and a variable a later git adds is not removed until it is added
// there. The hedge is about the future rather than about anything knowable
// today.
//
// GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM, and GIT_CONFIG_NOSYSTEM are
// deliberately kept. They relocate configuration rather than redirect a
// repository, and a caller that sets them means it. GIT_PAGER and PAGER are
// kept for the same reason and because --no-pager is the single mechanism this
// package uses against a pager. GIT_ALLOW_PROTOCOL and GIT_PROTOCOL_FROM_USER
// are kept because a value inherited from a hardened environment restricts
// which transports git will use, and removing it would loosen that rather than
// tighten anything.
//
// One consequence of keeping GIT_CONFIG_GLOBAL is worth naming: an
// init.templateDir in the operator's own git configuration still decides what
// InitBare copies into a repository it creates. The environment cannot choose
// those hooks; the operator's configuration can.
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
// what ends it. And setting GIT_SSH_COMMAND overrides a core.sshCommand set in
// git configuration, so a caller that relies on core.sshCommand loses it here.
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
