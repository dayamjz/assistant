package home

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNotACopy reports a directory CopyAt refuses: one that is not exactly
// where this home's layout puts a task's copy or a run's copy, or that is
// reached through a symbolic link, or that cannot be resolved at all. Every
// refusal wraps it and says which.
var ErrNotACopy = errors.New("home: not an isolated copy this home's layout places")

// CopyAt answers whether dir is one of this home's isolated copies, and
// returns it cleaned when it is. A copy is a task's, at TaskCopy of an
// identifier ValidTaskID accepts, or a run's, at Worktree of a repository and
// a run, which is two non-empty path elements under worktrees.
//
// The answer is about the layout and the filesystem together, and neither is
// read loosely. dir must name such a place as written, under this home's
// root as Open resolved it, and resolving every symbolic link in dir must
// give back dir unchanged. So a copy path that is itself a link, or that
// passes through one, is refused even when the link lands in another copy
// of this home: the path names one copy and the directory it reaches would
// be another. A dir that does not exist cannot be resolved and is refused
// too.
//
// It says nothing about whether a run or a task still owns the copy, or
// whether the directory holds a repository; those are the store's and
// internal/tasks' questions. Its one consumer is internal/agents' sweep,
// which ends what runs in a directory and so must not be pointed anywhere
// else.
func (h *Home) CopyAt(dir string) (string, error) {
	cleaned, err := h.copyPlace(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("%w: %s cannot be resolved: %w", ErrNotACopy, cleaned, err)
	}
	if resolved != cleaned {
		return "", fmt.Errorf("%w: %s resolves through a symbolic link to %s", ErrNotACopy, cleaned, resolved)
	}
	return cleaned, nil
}

// copyPlace is CopyAt's half about the layout alone: dir, cleaned, when it
// is written as exactly where this home places a task's copy or a run's
// copy, refused with ErrNotACopy otherwise. It reads nothing on disk.
func (h *Home) copyPlace(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: %q is not absolute", ErrNotACopy, dir)
	}
	cleaned := filepath.Clean(dir)
	rel, err := filepath.Rel(h.root, cleaned)
	if err != nil {
		return "", fmt.Errorf("%w: %s is not under %s: %w", ErrNotACopy, cleaned, h.root, err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	switch {
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "copy":
		if err := ValidTaskID(parts[1]); err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrNotACopy, cleaned, err)
		}
	case len(parts) == 3 && parts[0] == "worktrees" && element(parts[1]) && element(parts[2]):
	default:
		return "", fmt.Errorf("%w: %s is not a task's copy or a run's copy under %s", ErrNotACopy, cleaned, h.root)
	}
	return cleaned, nil
}

// Discarding is the directory a confirmed discard of the copy at copyPath
// sets what it removes aside into and keeps its journal in, PRD section 8's
// on-disk layout: beside the copy, so on its file system, under a name no
// copy takes. That is tasks/<id>/.copy.discarding for a task's copy and
// worktrees/<repo>/.<run>.discarding for a run's. copyPath is held to the
// layout as CopyAt holds it, and refused with ErrNotACopy otherwise, but
// nothing is read on disk: a discard past its point of no return has moved
// the copy away, and its directory is still named from the copy's path.
func (h *Home) Discarding(copyPath string) (string, error) {
	cleaned, err := h.copyPlace(copyPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cleaned), "."+filepath.Base(cleaned)+".discarding"), nil
}

// DiscardingStage is where a discard writes its first journal before its
// directory (Discarding) exists: a directory os.MkdirTemp makes in parent
// from pattern, beside the copy, which the discard then renames to
// Discarding whole. So Discarding never stands without a journal. copyPath
// is held to the layout as Discarding holds it.
func (h *Home) DiscardingStage(copyPath string) (parent, pattern string, err error) {
	dir, err := h.Discarding(copyPath)
	if err != nil {
		return "", "", err
	}
	return filepath.Dir(dir), filepath.Base(dir) + ".begin*", nil
}

// element reports whether s is one path element that names a child, rather
// than nothing, the directory itself or its parent.
func element(s string) bool {
	return s != "" && s != "." && s != ".."
}
