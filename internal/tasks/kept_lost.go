package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/vcs"
)

// lostPaths decides which paths a discard destroys whose content nothing
// else holds, the list Loss shows (Loss.Files), from what the copy holds
// and never from what git reports, so neither git's ignore rules nor its
// configuration keep a path off it. It is given every entry the digest's
// walk records, and decides each against what holds content elsewhere:
//
//   - a working-tree entry against HEAD's tree, whose commit the default
//     branch holds or the list of commits lacking names: a file holding
//     what HEAD's file at its path holds, by git's identity of its content
//     with no filter applied (vcs.BlobObject) and git's mode, is not lost,
//     and any other is (LostChanged where HEAD holds a file there, LostNew
//     where it holds nothing, or a directory or a submodule); a directory
//     HEAD's tree does not hold is named once, whole, and nothing under it
//     is named again;
//   - an index entry against HEAD's tree and the working tree at its path,
//     so staged content held by neither is named (LostStaged);
//   - an entry of the repository directory against what git writes into a
//     new repository at the same path, read from one made for the purpose
//     (freshRepository), so a sample hook git writes into every repository
//     is not named and a hook or configuration of the copy's own is
//     (LostRepository). The entries sealed by what they hold
//     (gitDirSealedByContent) are not walked, so not named: what they hold
//     is shown as the commits lacking and the stash indicator.
//
// What it names more than is lost, failing toward showing: a file whose
// content a commit other than HEAD's holds, one a filter or line-ending
// conversion would store as HEAD's content, and a submodule's working tree,
// whose commits are another repository's, are named all the same.
type lostPaths struct {
	format  string
	head    map[string]vcs.TreeEntry
	index   []vcs.IndexEntry
	indexed map[string]bool
	// working is the mode and object of the working-tree entry at each
	// path the index names, which the index entries are decided against.
	working map[string]string
	found   map[LostKind][]string
}

// newLostPaths reads HEAD's tree and the repository's object format, which
// the working tree is decided against, with the index entries copyState
// read.
func newLostPaths(ctx context.Context, copied *vcs.Repository, head string, index []vcs.IndexEntry) (*lostPaths, error) {
	format, err := copied.ObjectFormat(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := copied.AllTreeEntries(ctx, head)
	if err != nil {
		return nil, err
	}
	l := &lostPaths{format: format, head: tree, index: index, indexed: map[string]bool{}, working: map[string]string{},
		found: map[LostKind][]string{}}
	for _, e := range index {
		l.indexed[e.Path] = true
	}
	return l, nil
}

// worktree is the visit that decides the working tree's entries under
// root.
func (l *lostPaths) worktree(root string) treeVisit {
	whole := ""
	return func(rel string, info os.FileInfo, _ string) error {
		// The copy's .git that is not walked is its repository directory,
		// decided on its own terms; one that is a file is a linked
		// worktree's pointer to that directory.
		if rel == ".git" && !info.IsDir() {
			return nil
		}
		mode := gitMode(info)
		e, inHead := l.head[rel]
		object := ""
		if mode != "" && (l.indexed[rel] || (inHead && e.Type == "blob")) {
			var err error
			if object, err = vcs.BlobObject(l.format, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				return err
			}
			l.working[rel] = mode + " " + object
		}
		if whole != "" && strings.HasPrefix(rel, whole) {
			return nil
		}
		whole = ""
		switch {
		case info.IsDir():
			if inHead && e.Type == "tree" {
				return nil
			}
			whole = rel + "/"
			l.add(LostNew, whole)
		case inHead && e.Type == "blob":
			if mode == "" || mode != e.Mode || object != e.Object {
				l.add(LostChanged, rel)
			}
		default:
			l.add(LostNew, rel)
		}
		return nil
	}
}

// repository is the visit that decides the repository directory's entries
// against fresh, what git writes into a new repository.
func (l *lostPaths) repository(fresh map[string]freshEntry) treeVisit {
	whole := ""
	return func(rel string, info os.FileInfo, held string) error {
		if whole != "" && strings.HasPrefix(rel, whole) {
			return nil
		}
		whole = ""
		kind := entryKind(info)
		f, ok := fresh[rel]
		switch {
		case ok && f.kind == kind && (kind == "dir" || (kind != "other" && f.held == held)):
		case kind == "dir":
			whole = rel + "/"
			l.add(LostRepository, whole)
		default:
			l.add(LostRepository, rel)
		}
		return nil
	}
}

// list is every path found, the index decided last against what the walk
// read of the working tree, in lostKinds' order and by path within each.
func (l *lostPaths) list() []LostPath {
	for _, e := range l.index {
		if h, ok := l.head[e.Path]; ok && h.Mode == e.Mode && h.Object == e.Object {
			continue
		}
		if l.working[e.Path] == e.Mode+" "+e.Object || slices.Contains(l.found[LostStaged], e.Path) {
			continue
		}
		l.add(LostStaged, e.Path)
	}
	out := []LostPath{}
	for _, kind := range lostKinds {
		paths := l.found[kind]
		slices.Sort(paths)
		for _, p := range paths {
			out = append(out, LostPath{Kind: kind, Path: p})
		}
	}
	return out
}

func (l *lostPaths) add(kind LostKind, path string) { l.found[kind] = append(l.found[kind], path) }

// gitMode is the mode git records a working-tree entry under, or "" for
// one git records no blob for: a link, an executable file (the owner's
// execute bit) and any other regular file.
func gitMode(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "120000"
	case info.Mode().IsRegular() && info.Mode()&0o100 != 0:
		return "100755"
	case info.Mode().IsRegular():
		return "100644"
	}
	return ""
}

// freshEntry is one entry of a new repository's directory: its type and
// what recordTree read it to hold.
type freshEntry struct{ kind, held string }

// entryKind is an entry's type as recordTree records it.
func entryKind(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "link"
	case info.IsDir():
		return "dir"
	case info.Mode().IsRegular():
		return "file"
	}
	return "other"
}

// freshRepository is what git writes into a new repository's directory,
// read from one it initializes in a temporary directory this removes
// afterwards, under the configuration the program's git runs under, which
// chose the copy's templates when the copy was made; skip is the same
// skip the copy's repository directory is walked with.
func freshRepository(ctx context.Context, skip func(rel string, info os.FileInfo) bool) (map[string]freshEntry, error) {
	dir, err := os.MkdirTemp("", "assistant-fresh-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "repository")
	if _, err := vcs.InitBare(ctx, path, vcs.WithRedactor(redact.New())); err != nil {
		return nil, fmt.Errorf("initializing one: %w", err)
	}
	fresh := map[string]freshEntry{}
	err = recordTree(newDigest(), "fresh", path, skip, func(rel string, info os.FileInfo, held string) error {
		fresh[rel] = freshEntry{kind: entryKind(info), held: held}
		return nil
	})
	return fresh, err
}
