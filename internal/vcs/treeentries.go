package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// TreeEntry is one path's entry in a tree: its mode, its object type and
// its object identifier, as ls-tree reports them.
type TreeEntry struct {
	Mode   string
	Type   string
	Object string
}

// TreePathsDiffer lists every path whose entry differs between two trees,
// content or mode, recursing into directories and with no rename detection,
// so a path moved is named at both ends and a path added or deleted is named
// once. a and b are anything that names a tree, a commit included.
func (r *Repository) TreePathsDiffer(ctx context.Context, a, b string) ([]string, error) {
	out, err := r.run(ctx, "tree-diff", "diff-tree", "-r", "--name-only", "-z", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// TreeEntries returns rev's entry for each of paths that rev's tree holds,
// and no entry for a path it does not. A path that is a directory in rev is
// returned as that directory's tree entry, so it compares unequal to a file
// at the same path. Paths are matched literally, never as patterns, and only
// an entry at exactly the path given is returned.
func (r *Repository) TreeEntries(ctx context.Context, rev string, paths []string) (map[string]TreeEntry, error) {
	id, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(paths))
	for _, path := range paths {
		want[path] = true
	}
	entries := make(map[string]TreeEntry, len(paths))
	const chunk = 256
	for start := 0; start < len(paths); start += chunk {
		end := min(start+chunk, len(paths))
		args := append([]string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", id, "--"}, paths[start:end]...)
		out, err := r.run(ctx, "tree-entries", args...)
		if err != nil {
			return nil, err
		}
		for _, record := range strings.Split(string(out), "\x00") {
			if record == "" {
				continue
			}
			// <mode> SP <type> SP <object> TAB <path>
			meta, path, ok := strings.Cut(record, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 {
				return nil, &outputError{op: "tree-entries", detail: "unexpected ls-tree record " + record}
			}
			if want[path] {
				entries[path] = TreeEntry{Mode: fields[0], Type: fields[1], Object: fields[2]}
			}
		}
	}
	return entries, nil
}

// TreeWithEntries returns the tree that onto's tree becomes when each path in
// set takes the entry given, and each path in remove is taken out. It applies
// no patch and runs no merge: it reads onto's tree into a scratch index, in a
// temporary directory this removes afterwards, sets those entries there with
// update-index, and records the result with write-tree, so the tree is fixed
// by the entries alone. Neither this repository's index nor its working tree
// is read or written.
//
// built is false when the scratch index refuses an entry, which is a path
// that is a directory in onto where an entry wants a file, or the reverse.
func (r *Repository) TreeWithEntries(ctx context.Context, onto string, set map[string]TreeEntry, remove []string) (
	tree string, built bool, err error) {
	id, err := r.ResolveCommit(ctx, onto)
	if err != nil {
		return "", false, err
	}
	dir, err := os.MkdirTemp("", "assistant-tree-")
	if err != nil {
		return "", false, fmt.Errorf("vcs: making a scratch directory for a tree: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	scratch := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	if _, err := r.runWith(ctx, "tree-index", scratch, "read-tree", id); err != nil {
		return "", false, err
	}
	refused := func(err error) bool {
		var ce *CommandError
		return errors.As(err, &ce) && ce.Err == nil && ce.ExitCode > 0
	}
	var batches [][]string
	const chunk = 256
	for start := 0; start < len(remove); start += chunk {
		end := min(start+chunk, len(remove))
		batches = append(batches, append([]string{"update-index", "--force-remove", "--"}, remove[start:end]...))
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for start := 0; start < len(paths); start += chunk {
		args := []string{"update-index", "--add"}
		for _, path := range paths[start:min(start+chunk, len(paths))] {
			entry := set[path]
			args = append(args, "--cacheinfo", entry.Mode+","+entry.Object+","+path)
		}
		batches = append(batches, args)
	}
	for _, args := range batches {
		if _, err := r.runWith(ctx, "tree-entries-set", scratch, args...); err != nil {
			if refused(err) {
				return "", false, nil
			}
			return "", false, err
		}
	}
	out, err := r.runWith(ctx, "tree-write", scratch, "write-tree")
	if err != nil {
		return "", false, err
	}
	tree = strings.TrimSpace(string(out))
	if tree == "" {
		return "", false, &outputError{op: "tree-write", detail: "write-tree named no tree"}
	}
	return tree, true, nil
}
