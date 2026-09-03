package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// AdmissionHook is the hook that runs before any reference in the gate
	// changes. PRD section 5 puts admission before mutation so that a refusal
	// happens instead of a change rather than after one.
	AdmissionHook = "pre-receive"
	// NotificationHook is the hook that runs once a push has been accepted.
	// It hands off and exits; the background service owns everything
	// long-running.
	NotificationHook = "post-receive"
	// CustomHookSuffix is appended to a hook's name to hold a hook this
	// package did not write. The installed hook runs it after admission with
	// the same standard input and exits with its status.
	CustomHookSuffix = ".local"
)

// hookMarker appears in the first lines of every hook this package writes and
// is how a later initialization tells its own hook from somebody else's. A
// hook carrying it is replaced; a hook without it is preserved.
const hookMarker = "assistant-gate-hook v1"

// chainedVar is set around the call to a preserved hook. A managed hook that
// finds its own name in it is being run as somebody's preserved hook and does
// not chain further, which bounds the chain at one level.
const chainedVar = "ASSISTANT_GATE_CHAINED"

// hookMode is the mode an installed hook is written with. It is executable
// because a hook that is not is a gate whose admission never runs.
// TestInstallationLeavesExactlyTheTwoExecutableHooks checks the bit, and every
// test that pushes checks what the bit is for by pushing.
const hookMode os.FileMode = 0o755

// managed is one hook this package installs and the subcommand it invokes.
// The two are one row rather than two lists, so a hook cannot be added to the
// set without saying what it calls; per PRD principle P14 there is no second
// place to keep that in step.
type managed struct {
	name       string
	subcommand string
}

// managedHooks is what this package installs, in a fixed order so that a
// failure part way through installs a prefix rather than an arbitrary subset.
//
// The subcommands are the interface this package requires of the agent-facing
// command surface, which PRD section 8 calls the machine module. That command
// must accept "gate admit" and "gate notify", each taking --gate with a gate
// identifier and reading git's reference update lines from standard input.
// Admission's exit status decides whether the push is accepted.
var managedHooks = []managed{
	{name: AdmissionHook, subcommand: "admit"},
	{name: NotificationHook, subcommand: "notify"},
}

// hooksDir is the directory inside a bare repository this package installs its
// hooks into. Where a gate's hooks are actually read from is a git
// configuration question this package cannot currently ask, because
// core.hooksPath can point somewhere else; doc.go states that gap.
func hooksDir(repo string) string {
	return filepath.Join(repo, "hooks")
}

// installHooks puts this package's hooks into a gate repository, preserving
// any hook it did not write.
//
// It refuses with ErrCustomHookConflict before writing anything when a foreign
// hook would have to be moved onto an existing file, so a repository is not
// left with one hook installed and the other refused.
func installHooks(repo, id, command string) error {
	dir := hooksDir(repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("gate: creating %s: %w", dir, err)
	}
	for _, hook := range managedHooks {
		if err := checkPreservable(dir, hook.name); err != nil {
			return err
		}
	}
	for _, hook := range managedHooks {
		if err := preserveCustomHook(dir, hook.name); err != nil {
			return err
		}
		script := hookScript(hook, id, command)
		if err := replaceFile(filepath.Join(dir, hook.name), []byte(script), hookMode); err != nil {
			return err
		}
	}
	return nil
}

// checkPreservable reports whether the hook at name can be installed without
// discarding a file somebody else wrote.
func checkPreservable(dir, name string) error {
	foreign, err := isForeignHook(dir, name)
	if err != nil || !foreign {
		return err
	}
	saved := filepath.Join(dir, name+CustomHookSuffix)
	if _, err := os.Lstat(saved); err == nil {
		return fmt.Errorf("%w: %s was not written by this package and %s already exists",
			ErrCustomHookConflict, filepath.Join(dir, name), saved)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("gate: inspecting %s: %w", saved, err)
	}
	return nil
}

// preserveCustomHook moves a hook this package did not write to its .local
// name, keeping its mode, so that the installed hook can run it. A hook this
// package did write, and a name with nothing at it, are both left for the
// caller to overwrite.
func preserveCustomHook(dir, name string) error {
	foreign, err := isForeignHook(dir, name)
	if err != nil || !foreign {
		return err
	}
	from := filepath.Join(dir, name)
	to := filepath.Join(dir, name+CustomHookSuffix)
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("gate: preserving %s as %s: %w", from, to, err)
	}
	return nil
}

// isForeignHook reports whether a hook exists at name and was written by
// somebody other than this package. A file that cannot be read is treated as
// foreign, because a hook whose contents are unknown is exactly the hook that
// must not be silently replaced.
func isForeignHook(dir, name string) (bool, error) {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("gate: inspecting %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		// A symbolic link or anything else that is not a plain file is
		// somebody's arrangement, and renaming it preserves it just as well.
		return true, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return true, nil
	}
	return !strings.Contains(string(content), hookMarker), nil
}

// activeHooks lists the hooks present in a repository's hooks directory,
// sorted. Names ending in .sample are skipped: those are the disabled copies a
// git template ships, and this package treats a sample as documentation rather
// than as a hook somebody installed.
func activeHooks(repo string) ([]string, error) {
	entries, err := os.ReadDir(hooksDir(repo))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gate: reading %s: %w", hooksDir(repo), err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// namesAdded is the names present in after that were not present in before.
// It is how this package tells a hook that arrived during an operation from
// one that was already there, without asking git what it did.
func namesAdded(before, after []string) []string {
	had := make(map[string]struct{}, len(before))
	for _, name := range before {
		had[name] = struct{}{}
	}
	var added []string
	for _, name := range after {
		if _, ok := had[name]; !ok {
			added = append(added, name)
		}
	}
	return added
}

// hookScript renders the hook installed at name.
//
// Three things about the script are load-bearing rather than stylistic. The
// command is an absolute path written into the file, so the environment a push
// happens in cannot choose what admission runs. Standard input is captured to
// a file and given in full to both the command and any preserved hook. And
// the preserved hook's exit status becomes the script's, so a custom
// pre-receive can still reject a push after admission has accepted it.
//
// The command is written with forward slashes, whatever the host's own
// separator is. A shell searches PATH for a command word that contains no
// slash, so on a host whose separator is a backslash the native spelling of an
// absolute path reaches the hook shell as a bare word, and the pushed-from
// PATH would choose what admission runs after all, which is exactly what PRD
// principle P7 takes away from it. Every shell these hooks run under,
// including the one Git for Windows bundles, resolves the forward-slash
// spelling as a path. On a host whose separator is already a forward slash
// this changes nothing.
//
// Capturing the input is what lets both of them have it: standard input can be
// consumed once, and a hook that handed it to the command would have nothing
// left to give the preserved hook.
//
// No arguments are forwarded to a preserved hook, because neither of the two
// hooks installed here is invoked with any; what they carry arrives on
// standard input instead, and that is what is forwarded.
//
// Chaining stops after one level, and that bound is not decoration. A file at
// the .local name is normally somebody else's hook, and installation checks
// that before putting one there. Nothing stops a person from putting a copy of
// a managed hook there by hand afterwards, and a managed hook chains to
// .local, so without a bound that arrangement is a push that never returns.
// The environment variable set around the chained call is what a second
// managed hook sees and declines to chain on.
func hookScript(hook managed, id, command string) string {
	name := hook.name
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\n")
	fmt.Fprintf(&b, "# %s %s\n", hookMarker, name)
	fmt.Fprintf(&b, "#\n")
	fmt.Fprintf(&b, "# Written by the assistant gate and replaced whenever the gate is initialized\n")
	fmt.Fprintf(&b, "# or repaired, so an edit here does not survive. A hook of your own belongs at\n")
	fmt.Fprintf(&b, "# %s%s beside this file: it runs after admission, reads the same\n", name, CustomHookSuffix)
	fmt.Fprintf(&b, "# standard input, and its exit status becomes this hook's.\n")
	fmt.Fprintf(&b, "set -u\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "hook_dir=${0%%/*}\n")
	fmt.Fprintf(&b, "if [ \"$hook_dir\" = \"$0\" ]; then hook_dir=.; fi\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "refs=$(mktemp \"${TMPDIR:-/tmp}/assistant-gate.XXXXXX\") || exit 1\n")
	fmt.Fprintf(&b, "trap 'rm -f \"$refs\"' EXIT HUP INT TERM\n")
	fmt.Fprintf(&b, "cat >\"$refs\" || exit 1\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "%s gate %s --gate %s <\"$refs\" || exit $?\n",
		shellQuote(filepath.ToSlash(command)), hook.subcommand, shellQuote(id))
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "custom=$hook_dir/%s%s\n", name, CustomHookSuffix)
	fmt.Fprintf(&b, "if [ -x \"$custom\" ] && [ \"${%s:-}\" != %s ]; then\n", chainedVar, shellQuote(name))
	fmt.Fprintf(&b, "\t%s=%s \"$custom\" <\"$refs\" || exit $?\n", chainedVar, shellQuote(name))
	fmt.Fprintf(&b, "fi\n")
	fmt.Fprintf(&b, "exit 0\n")
	return b.String()
}

// shellQuote renders s as a single shell word that expands to exactly s. It
// wraps s in single quotes, inside which every byte but a single quote is
// literal, and closes and reopens the quoting around each single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
