package vcs

import (
	"context"
	"slices"
	"strings"
)

// Reads of where git keeps a working copy's repository files and of the
// configuration git reads for it, each as git itself answers: what a
// caller deciding how a copy is laid out asks.

// GitPath is the absolute path git uses for name in this working copy's
// repository directory, git rev-parse --git-path, which for a linked
// worktree is its own directory for what it keeps of its own (HEAD, the
// index, logs/HEAD) and the common directory for the rest.
func (r *Repository) GitPath(ctx context.Context, name string) (string, error) {
	if err := checkArg("path", name); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "git-path", "rev-parse", "--path-format=absolute", "--git-path", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ConfigValue is the value git reads for the configuration key, the last
// one set when several are, and whether it is set at all, as git config
// -z --get puts it on the wire: the value ended by a NUL, or exit status 1
// for a key not set. An explicitly empty value, and a key given with no
// value, are set and empty.
func (r *Repository) ConfigValue(ctx context.Context, key string) (string, bool, error) {
	if err := checkArg("key", key); err != nil {
		return "", false, err
	}
	out, code, err := r.runExpecting(ctx, "config-get", []int{1}, "config", "-z", "--get", key)
	if err != nil || code == 1 {
		return "", false, err
	}
	value, ok := strings.CutSuffix(string(out), "\x00")
	if !ok {
		return "", false, &outputError{op: "config-get", detail: "the value of " + key + " is not ended by a NUL"}
	}
	return value, true, nil
}

// ConfigBool is the boolean git reads for the configuration key, by git's
// own reading of a boolean (git config --type=bool), and whether it is set
// at all; a value git does not read as a boolean is a *CommandError.
func (r *Repository) ConfigBool(ctx context.Context, key string) (value, set bool, err error) {
	if err := checkArg("key", key); err != nil {
		return false, false, err
	}
	out, code, err := r.runExpecting(ctx, "config-get-bool", []int{1}, "config", "--type=bool", "--get", key)
	if err != nil || code == 1 {
		return false, false, err
	}
	return strings.TrimSpace(string(out)) == "true", true, nil
}

// ConfigNames is every configuration key git reads for this repository
// whose name matches pattern, an extended regular expression git matches
// against each key as git config --get-regexp does (its section and name
// lowercase, a subsection as written), each once and in git's order, as
// git config -z --name-only --get-regexp puts them on the wire: each name
// ended by a NUL, or exit status 1 when none matches.
func (r *Repository) ConfigNames(ctx context.Context, pattern string) ([]string, error) {
	if err := checkArg("pattern", pattern); err != nil {
		return nil, err
	}
	out, code, err := r.runExpecting(ctx, "config-get-regexp", []int{1}, "config", "-z", "--name-only", "--get-regexp", pattern)
	if err != nil || code == 1 {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}
