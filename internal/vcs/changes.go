package vcs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
			if entry[0] == '2' {
				// A rename entry carries its original path in the next
				// field. Renames are turned off above, so none is
				// expected, and one that arrives anyway is read correctly
				// rather than as a path.
				i++
			}
		case 'u':
			c.Unmerged = true
		case '?':
			c.Untracked = true
		case '!':
			// Ignored entries are not asked for; one that arrives changes
			// nothing, because an ignored file is not an indicator here.
		default:
			return Changes{}, &outputError{op: "status", detail: "unrecognized entry " + entry}
		}
	}
	return c, nil
}

// UncommittedDigest identifies the content a commit of this working copy
// would newly record, including unresolved conflict markers in the worktree.
//
// Equal results mean that content is unchanged. Ignored files are not part of
// it, which is the same gap Changes leaves. The result is not a git object
// name.
func (r *Repository) UncommittedDigest(ctx context.Context) (string, error) {
	if r.kind != KindWorktree {
		return "", &openError{path: r.path, kind: KindWorktree}
	}
	diff, _, err := r.runExpecting(ctx, "uncommitted-diff", []int{1}, "diff", "--binary", "HEAD")
	if err != nil {
		return "", err
	}
	others, err := r.run(ctx, "uncommitted-untracked", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	sum.Write(diff)
	sum.Write([]byte{0})
	for path := range strings.SplitSeq(string(others), "\x00") {
		if path == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(r.path, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		sum.Write([]byte(path))
		sum.Write([]byte{0})
		sum.Write(body)
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
