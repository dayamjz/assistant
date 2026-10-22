package vcs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

// UncommittedDigest identifies the index and the worktree state of paths
// that differ from it, including unresolved conflict stages and untracked
// files.
//
// Equal results mean that state is unchanged. The worktree record of a path
// is its mode and its bytes, so a mode-only change counts. A symlink
// contributes its target text. A directory that holds a submodule checkout
// contributes the commit that checkout has checked out, which is what
// rev-parse HEAD prints there. A symbolic reference is followed to that
// commit, and the reference's own text is not recorded. The invocation
// reads whatever ref storage the checkout uses. A directory that does not
// hold one is recorded as a directory and its mode, not the files inside
// it. A checkout whose HEAD does not resolve fails the digest. Staging a
// path changes the result even when its worktree bytes do not, because the
// index stages are part of it. Ignored files are not part of it, which is
// the same gap Changes leaves. The result is not a git object name.
//
// The name listing passes --no-ext-diff and --no-textconv, and the bytes are
// read from the worktree rather than from a patch. An attributes diff driver
// or textconv does not run, and a large blob is not read through the
// invocation output cap. A listing of paths that itself exceeds that cap
// still fails.
func (r *Repository) UncommittedDigest(ctx context.Context) (string, error) {
	if r.kind != KindWorktree {
		return "", &openError{path: r.path, kind: KindWorktree}
	}
	stage, err := r.run(ctx, "uncommitted-stage", "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	names, err := r.run(ctx, "uncommitted-names",
		"diff-files", "-z", "--name-only", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return "", err
	}
	others, err := r.run(ctx, "uncommitted-untracked", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	if err := digestWrite(sum, stage); err != nil {
		return "", err
	}
	if err := digestWrite(sum, []byte{0}); err != nil {
		return "", err
	}
	if err := hashWorktreeList(ctx, r, sum, r.path, string(names)); err != nil {
		return "", err
	}
	if err := digestWrite(sum, []byte{0}); err != nil {
		return "", err
	}
	if err := hashWorktreeList(ctx, r, sum, r.path, string(others)); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// digestWrite records b and returns the writer's error. A hash writer does
// not fail, and the check is what keeps a later writer from dropping bytes.
func digestWrite(sum io.Writer, b []byte) error {
	_, err := sum.Write(b)
	return err
}

// hashWorktreeList mixes the worktree state of each NUL-separated path into
// sum. A missing path is recorded as absent. A symlink contributes its mode
// and target text. A regular file contributes its mode and bytes. A
// submodule directory contributes its mode and the commit it has checked out.
func hashWorktreeList(ctx context.Context, r *Repository, sum io.Writer, root, listed string) error {
	for path := range strings.SplitSeq(listed, "\x00") {
		if path == "" {
			continue
		}
		full, err := worktreePath(root, path)
		if err != nil {
			return err
		}
		if err := digestWrite(sum, []byte(path)); err != nil {
			return err
		}
		if err := digestWrite(sum, []byte{0}); err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				if err := digestWrite(sum, []byte{0}); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := digestWrite(sum, modeBytes(info)); err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if err := digestWrite(sum, []byte{1}); err != nil {
				return err
			}
			if err := digestWrite(sum, []byte(target)); err != nil {
				return err
			}
		case info.IsDir():
			if err := digestWrite(sum, []byte{2}); err != nil {
				return err
			}
			commit, ok, err := r.submoduleCommit(ctx, full)
			if err != nil {
				return err
			}
			if ok {
				if err := digestWrite(sum, []byte(commit)); err != nil {
					return err
				}
			}
		default:
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			writeErr := digestWrite(sum, []byte{3})
			_, copyErr := io.Copy(sum, f)
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		if err := digestWrite(sum, []byte{0}); err != nil {
			return err
		}
	}
	return nil
}

// modeBytes is the permission bits of info, so a mode-only change is part of
// the digest.
func modeBytes(info os.FileInfo) []byte {
	perm := uint32(info.Mode().Perm())
	return []byte{byte(perm >> 8), byte(perm)}
}

// submoduleCommit is the commit checked out at dir.
//
// ok is false when dir has no .git metadata. When it does, the commit is the
// line rev-parse HEAD prints there, and this package does not read the ref
// files itself. A symbolic chain resolves to the commit it names, and a
// reftable checkout resolves the same way a loose ref does. The absolute -C
// is what makes that invocation the checkout's, after the addressing of the
// superproject. A HEAD that does not resolve, or that prints something other
// than an object name, is an error: the digest then cannot claim the checkout
// was unchanged.
func (r *Repository) submoduleCommit(ctx context.Context, dir string) (commit string, ok bool, err error) {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}
	out, err := r.run(ctx, "submodule-head", "-C", abs, "rev-parse", "--verify", "--end-of-options", "HEAD")
	if err != nil {
		return "", false, err
	}
	id := strings.TrimSpace(string(out))
	if !objectName(id) {
		return "", false, fmt.Errorf("vcs: submodule HEAD in %s resolved to %q", dir, id)
	}
	return id, true, nil
}

// objectName reports whether s is a hex object name, the shape rev-parse
// prints for a commit.
func objectName(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// worktreePath joins a git path onto the working copy and refuses one that
// leaves it.
func worktreePath(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("vcs: uncommitted path %q leaves the working copy", rel)
	}
	return filepath.Join(root, clean), nil
}
