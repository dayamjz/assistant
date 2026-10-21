package vcs

import (
	"context"
	"strconv"
	"strings"
)

// Changes is what a working copy holds that no commit records yet, split the
// way a person acts on it: files git does not track, edits to tracked files
// that are not staged, staged edits that are not committed, and unresolved
// conflicts. One path can set two of these at once: an edit staged and then
// edited again is both Staged and Unstaged. Repository.Changes fills one, and
// Inventory reads its dirty and untracked indicators through the same status
// read and the same parser, so the two never disagree about a tree.
//
// Ignored files are not consulted, which is the residual gap Inventory
// states: a file git is configured to ignore is in none of these.
type Changes struct {
	// Untracked reports a file no commit records and git is not configured
	// to ignore.
	Untracked bool
	// Unstaged reports a tracked file whose working tree content differs
	// from the index: an edit, a deletion or a type change not yet staged.
	Unstaged bool
	// Staged reports an index that differs from HEAD: a path added,
	// modified or deleted in the index and not yet committed.
	Staged bool
	// Unmerged reports a path with an unresolved merge conflict, which has
	// to be resolved and staged before it can be committed.
	Unmerged bool
	// Paths is every path the indicators above were read from, in git's
	// order: each tracked path whose index or working tree differs from
	// HEAD, each unmerged path, and each untracked file. A path git is
	// configured to ignore is not among them, as it is not an indicator.
	Paths []ChangedPath
}

// ChangedPath is one path Changes reports: where it is under the working
// copy, whether git tracks it, and whether the working tree no longer has
// it.
type ChangedPath struct {
	// Path is the path relative to the working copy's root, as git prints
	// it.
	Path string
	// Untracked reports a file no commit records.
	Untracked bool
	// Gone reports a tracked path deleted from the working tree.
	Gone bool
}

// Dirty reports whether a tracked file differs from HEAD in the tree or in
// the index, which is what Inventory.Dirty reports.
func (c Changes) Dirty() bool { return c.Unstaged || c.Staged || c.Unmerged }

// Clean reports that nothing is held that a commit could record.
func (c Changes) Clean() bool { return !c.Dirty() && !c.Untracked }

// Changes reads what this working copy holds that no commit records, as
// Changes describes. It reads: it writes no reference and moves nothing, and
// --no-optional-locks keeps it from taking the index lock another git may
// hold. It refuses with ErrNotARepository on a bare repository, which has no
// tree to read.
func (r *Repository) Changes(ctx context.Context) (Changes, error) {
	if r.kind != KindWorktree {
		return Changes{}, &openError{path: r.path, kind: KindWorktree}
	}
	return r.changes(ctx)
}

// changes is the one status read Changes and Inventory share.
// --no-optional-locks is a git-level option, placed before the subcommand.
func (r *Repository) changes(ctx context.Context) (Changes, error) {
	out, err := r.run(ctx, "status", "--no-optional-locks", "status", "--porcelain=v2", "-z",
		"--untracked-files=all", "--ignored=no", "--no-renames")
	if err != nil {
		return Changes{}, err
	}
	return parseChanges(string(out))
}

// parseChanges reads git status --porcelain=v2 -z output. An ordinary entry
// ("1") and a rename or copy entry ("2") carry the index and working tree
// states as their second field, XY, where a dot means unchanged; an unmerged
// entry ("u") and an untracked one ("?") are indicators of their own. An
// entry of any other shape is refused rather than read as clean.
func parseChanges(out string) (Changes, error) {
	var c Changes
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if entry == "" {
			continue
		}
		switch entry[0] {
		case '1', '2':
			// An entry claiming neither side changed is refused rather
			// than read as clean, so a shape this parser does not know
			// fails toward the caller of a dirty tree, never a clean one.
			if len(entry) < 4 || entry[1] != ' ' || (entry[2] == '.' && entry[3] == '.') {
				return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
			}
			if entry[2] != '.' {
				c.Staged = true
			}
			if entry[3] != '.' {
				c.Unstaged = true
			}
			// The path is the last field, after eight single-space
			// separated fields for an ordinary entry and nine for a rename
			// or copy, and may itself carry spaces.
			fieldsBefore := 8
			if entry[0] == '2' {
				fieldsBefore = 9
			}
			path, ok := fieldAfter(entry, fieldsBefore)
			if !ok {
				return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
			}
			c.Paths = append(c.Paths, ChangedPath{Path: path, Gone: entry[3] == 'D'})
			if entry[0] == '2' {
				// A rename entry carries its original path in the next
				// field. Renames are turned off above, so none is
				// expected, and one that arrives anyway is read correctly
				// rather than as a path.
				i++
			}
		case 'u':
			c.Unmerged = true
			path, ok := fieldAfter(entry, 10)
			if !ok {
				return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
			}
			c.Paths = append(c.Paths, ChangedPath{Path: path})
		case '?':
			c.Untracked = true
			path, ok := fieldAfter(entry, 1)
			if !ok {
				return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
			}
			c.Paths = append(c.Paths, ChangedPath{Path: path, Untracked: true})
		case '!':
			// Ignored entries are not asked for; one that arrives changes
			// nothing, because an ignored file is not an indicator here.
		default:
			return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
		}
	}
	return c, nil
}

// fieldAfter returns what follows the first n single-space separated fields
// of entry, which is a porcelain v2 entry's path, and false when the entry
// has no such field or it is empty.
func fieldAfter(entry string, n int) (string, bool) {
	rest := entry
	for range n {
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return "", false
		}
		rest = rest[i+1:]
	}
	return rest, rest != ""
}

// IndexEntry is one entry of the index as git ls-files -s prints it: the
// mode, the object the entry names, its stage, and its path. An ordinary
// entry is stage 0; an unmerged path has an entry per stage, 1 to 3, and no
// stage 0.
type IndexEntry struct {
	Mode   string
	Object string
	Stage  int
	Path   string
}

// IndexEntries is every entry of the index, every stage included, in git's
// order: the staged content of every tracked path by object identity, and
// of a conflicted path each side git holds. It reads and writes nothing,
// and refuses with ErrNotARepository on a bare repository, which has no
// index of a working copy's.
func (r *Repository) IndexEntries(ctx context.Context) ([]IndexEntry, error) {
	if r.kind != KindWorktree {
		return nil, &openError{path: r.path, kind: KindWorktree}
	}
	out, err := r.run(ctx, "ls-files", "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	var entries []IndexEntry
	for _, line := range strings.Split(string(out), "\x00") {
		if line == "" {
			continue
		}
		// "mode object stage<TAB>path"
		head, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(head)
		if !ok || len(fields) != 3 || path == "" {
			return nil, &outputError{op: "ls-files", detail: "unrecognized entry " + line}
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, &outputError{op: "ls-files", detail: "unrecognized stage in " + line}
		}
		entries = append(entries, IndexEntry{Mode: fields[0], Object: fields[1], Stage: stage, Path: path})
	}
	return entries, nil
}
