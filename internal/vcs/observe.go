package vcs

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// Observation is what a directory is, as a repository: where its working copy
// and its repository directories are, which remotes it carries, and what its
// HEAD names. Observe fills one and nothing else does.
//
// It is taken under this package's own environment, the one every invocation
// here runs under, so it answers what the copy on disk is and never what a
// process started with some other environment would see there. A GIT_DIR a
// worker would inherit is a property of that worker's environment, and
// StripRedirecting is the operation on that; the two questions have two
// owners on purpose, because an observation that tried to answer both would
// answer neither.
//
// Paths are as git reported them, made absolute, and no symbolic link in them
// is resolved. A caller comparing two of them for identity resolves both
// itself; this package does not decide what two paths naming one directory
// should count as.
type Observation struct {
	// Dir is the directory the observation was taken from, absolute. It may
	// be a subdirectory of the working copy, in which case Toplevel is not
	// Dir.
	Dir string
	// Toplevel is the root of the working copy Dir is in.
	Toplevel string
	// GitDir is the repository directory for this working copy. For a main
	// working copy it is the .git directory under Toplevel and equals
	// CommonDir; for a linked worktree it is a directory of that worktree's
	// own and differs from CommonDir, which is the difference a caller uses
	// to tell the two apart.
	GitDir string
	// CommonDir is the repository directory this working copy shares with
	// every other worktree of the same repository.
	CommonDir string
	// Remotes is every remote configured in the working copy, by name, in
	// name order. It is empty when there is none.
	Remotes []Remote
	// Branch is the short name of the branch HEAD is on, and empty when HEAD
	// is detached.
	Branch string
	// Detached reports that HEAD is not on a branch, and is the only case in
	// which Branch is empty.
	Detached bool
}

// Remote is one configured remote and every URL it carries.
type Remote struct {
	// Name is the remote's name.
	Name string
	// URLs is every fetch URL configured for the remote, in configuration
	// order. A remote has at least one; a remote may carry several.
	URLs []string
	// PushURLs is every push URL configured for the remote, in configuration
	// order, and is empty when pushes go to URLs. It is reported because a
	// push URL is a route to a repository that URLs does not name.
	PushURLs []string
}

// Observe reports what the working copy at dir is. It refuses with
// ErrNotARepository when dir is not inside a working copy, on the terms
// OpenWorktree states, and it reads: nothing in the working copy changes.
//
// What it reports about the repository's other worktrees is nothing. A linked
// worktree added to the repository later is a different directory with its
// own Toplevel and GitDir, so an observation of it is a different
// observation, and the one of dir does not change; a caller asking whether a
// directory is the copy it expects asks about that directory. The remotes
// are read in full, every URL of every remote, because each is a route to
// some repository and a caller deciding what the copy may reach needs all of
// them.
func Observe(ctx context.Context, dir string, opts ...Option) (Observation, error) {
	r, err := OpenWorktree(ctx, dir, opts...)
	if err != nil {
		return Observation{}, err
	}
	obs := Observation{Dir: r.path}
	out, err := r.run(ctx, "observe", "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return Observation{}, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		return Observation{}, &outputError{op: "observe", detail: "expected three paths, got " + string(out)}
	}
	for i, line := range lines {
		path := strings.TrimSpace(line)
		if path == "" {
			return Observation{}, &outputError{op: "observe", detail: "expected three paths, got " + string(out)}
		}
		// A path reported relative is taken against the directory the
		// invocation ran in, which is Dir.
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.path, path)
		}
		abs, err := absolutePath(path)
		if err != nil {
			return Observation{}, err
		}
		switch i {
		case 0:
			obs.Toplevel = abs
		case 1:
			obs.GitDir = abs
		case 2:
			obs.CommonDir = abs
		}
	}
	obs.Remotes, err = r.remotes(ctx)
	if err != nil {
		return Observation{}, err
	}
	branch, err := r.HeadBranch(ctx)
	switch {
	case err == nil:
		obs.Branch = branch
	case errors.Is(err, ErrDetachedHead):
		obs.Detached = true
	default:
		return Observation{}, err
	}
	return obs, nil
}

// remotes reads every configured remote with all of its URLs.
func (r *Repository) remotes(ctx context.Context) ([]Remote, error) {
	out, err := r.run(ctx, "remote-list", "remote")
	if err != nil {
		return nil, err
	}
	var remotes []Remote
	for _, name := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		urls, err := r.configValues(ctx, "remote."+name+".url")
		if err != nil {
			return nil, err
		}
		if len(urls) == 0 {
			return nil, &outputError{op: "remote-list", detail: "remote " + name + " is listed and has no URL"}
		}
		push, err := r.configValues(ctx, "remote."+name+".pushurl")
		if err != nil {
			return nil, err
		}
		remotes = append(remotes, Remote{Name: name, URLs: urls, PushURLs: push})
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	return remotes, nil
}

// configValues reads every value of one configuration key in this
// repository, and none when the key is unset.
func (r *Repository) configValues(ctx context.Context, key string) ([]string, error) {
	// Exit status 1 is what git config reports for a key that is not set.
	out, code, err := r.runExpecting(ctx, "config-get-all", []int{1}, "config", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, nil
	}
	var values []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			values = append(values, line)
		}
	}
	return values, nil
}
