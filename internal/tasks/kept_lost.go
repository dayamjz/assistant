package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// lostPaths decides which paths a discard destroys whose content or
// deletion nothing else holds, the list Loss shows (Loss.Files), from what
// the copy holds and never from what git reports, so neither git's ignore
// rules nor its configuration keep a path off it. It is given every entry
// the digest's walk records, and decides each against what holds content
// elsewhere:
//
//   - a working-tree entry against HEAD's tree, whose commit the default
//     branch holds or the list of commits lacking names: a file holding
//     what HEAD's file at its path holds, with git's mode, is not lost,
//     and any other is: its bytes by git's identity of them with no filter
//     applied (vcs.BlobObject), and a regular file whose bytes are not
//     HEAD's blob by the blob git would store for it now, its clean
//     filters and line-ending conversion applied (stored), so a file git
//     checked out converted is HEAD's; the rest are named (LostChanged where HEAD holds a file there, LostNew
//     where it holds nothing, or a directory or a submodule); a directory
//     HEAD's tree does not hold is named once, whole, and nothing under it
//     is named again;
//   - an index entry against HEAD's tree and the working tree at its path,
//     so staged content held by neither is named (LostStaged);
//   - an entry of HEAD's tree against the working tree and the index, so a
//     deletion is named (LostDeleted) whether or not it is staged: the
//     content is HEAD's, which nothing loses, but the deletion is the
//     copy's alone. A directory of HEAD's tree the working tree does not
//     hold is named once, whole, and nothing under it is named again;
//   - an entry of the repository directory against what git writes into a
//     new repository at the same path, read from one made for the purpose
//     (freshRepository), so a sample hook git writes into every repository
//     is not named and a hook or configuration of the copy's own is
//     (LostRepository). The entries git rewrites without changing what
//     they hold (gitDirShownAsObjects) are not walked, so not named: what
//     they hold is shown as the commits and objects lost and the staged
//     paths.
//
// Each path is listed with what it is lost holding, the content the seal
// holds a confirmation to (lostFile): a working-tree or repository entry's
// type, the mode git records (the executable bit, or a link) and its bytes'
// hash or a link's target, a directory lost whole as every entry under it
// the same way with its path; an index entry's mode, object and stage, for
// every lost entry at the path; and a deletion's entry of HEAD's tree, its
// type, mode and object.
//
// Asking git for the stored blob runs the clean filters the copy's
// attributes and configuration name, as git status does when it rehashes
// a file; a filter that fails, and a converted file whose path holds a
// newline, fail the read, which refuses the preview rather than showing
// less.
//
// What it names more than is lost, failing toward showing: a file whose
// content a commit other than HEAD's holds, a mode git is configured to
// ignore (core.fileMode), and a submodule's working tree, whose commits
// are another repository's, are named all the same. A sparse checkout,
// whose paths left out would read as deleted, is refused before anything
// is read (supportedLayout).
type lostPaths struct {
	repo    *vcs.Repository
	format  string
	head    map[string]vcs.TreeEntry
	index   []vcs.IndexEntry
	indexed map[string]bool
	// working is the mode and object of the working-tree entry at each
	// path the index names, which the index entries are decided against.
	working map[string]string
	// seen is every path the working tree's walk visited.
	seen  map[string]bool
	found map[LostKind][]string
	// held is what each path found is lost holding, written as it is found.
	held map[LostPath]*digest
	// converted is every regular working-tree file at a path HEAD's tree
	// or the index holds whose bytes are not the blob there, which is
	// decided once git says what it would store for it (stored).
	converted []convertedFile
}

// convertedFile is a working-tree file the walk read whose bytes differ
// from what HEAD's tree or the index holds at its path, with what the walk
// read of it.
type convertedFile struct {
	rel  string
	info os.FileInfo
	held string
	mode string
}

// lostFile is a path a discard destroys and what it is lost holding, as
// lostPaths decides it: a digest of the content, framed on digest's terms.
type lostFile struct {
	LostPath
	held string
}

// newLostPaths reads HEAD's tree and the repository's object format, which
// the working tree is decided against, with the index entries copyFiles
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
	l := &lostPaths{repo: copied, format: format, head: tree, index: index, indexed: map[string]bool{}, working: map[string]string{},
		seen: map[string]bool{}, found: map[LostKind][]string{}, held: map[LostPath]*digest{}}
	for _, e := range index {
		l.indexed[e.Path] = true
	}
	return l, nil
}

// worktree is the visit that decides the working tree's entries under
// root.
func (l *lostPaths) worktree(root string) treeVisit {
	whole := ""
	var under *digest
	return func(rel string, info os.FileInfo, held string) error {
		// The copy's .git that is not walked is its repository directory,
		// decided on its own terms; one that is a file is a linked
		// worktree's pointer to that directory.
		if rel == ".git" && !info.IsDir() {
			return nil
		}
		l.seen[rel] = true
		mode := gitMode(info)
		e, inHead := l.head[rel]
		object := ""
		if mode != "" && (l.indexed[rel] || (inHead && e.Type == "blob")) {
			var err error
			if object, err = vcs.BlobObject(l.format, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				return err
			}
			l.working[rel] = mode + " " + object
			// A file HEAD's tree holds is set aside below; one only the
			// index holds is, when its bytes are not the index's blob.
			inHeadBlob := inHead && e.Type == "blob"
			if !inHeadBlob && info.Mode().IsRegular() && !l.indexHolds(rel, mode+" "+object) {
				l.converted = append(l.converted, convertedFile{rel: rel, info: info, held: held, mode: mode})
			}
		}
		if whole != "" && strings.HasPrefix(rel, whole) {
			holding(under, rel, info, held)
			return nil
		}
		whole = ""
		switch {
		case info.IsDir():
			if inHead && e.Type == "tree" {
				return nil
			}
			whole = rel + "/"
			under = l.add(LostNew, whole)
			holding(under, rel, info, held)
		case inHead && e.Type == "blob":
			switch {
			case object == e.Object && mode == e.Mode:
			case info.Mode().IsRegular() && object != e.Object:
				// Decided once git says what it would store (stored).
				l.converted = append(l.converted, convertedFile{rel: rel, info: info, held: held, mode: mode})
			default:
				holding(l.add(LostChanged, rel), rel, info, held)
			}
		default:
			holding(l.add(LostNew, rel), rel, info, held)
		}
		return nil
	}
}

// holding writes into d what the entry at rel holds, on lostPaths' terms.
func holding(d *digest, rel string, info os.FileInfo, held string) {
	d.record(rel, entryKind(info), gitMode(info), held)
}

// repository is the visit that decides the repository directory's entries
// against fresh, what git writes into a new repository: an entry is the
// template's, and not lost, when its type matches and, for anything but a
// directory, its bytes and its mode do too; every other is LostRepository.
func (l *lostPaths) repository(fresh map[string]freshEntry) treeVisit {
	whole := ""
	var under *digest
	return func(rel string, info os.FileInfo, held string) error {
		if whole != "" && strings.HasPrefix(rel, whole) {
			holding(under, rel, info, held)
			return nil
		}
		whole = ""
		kind := entryKind(info)
		f, ok := fresh[rel]
		switch {
		case ok && f.kind == kind && (kind == "dir" || (kind != "other" && f.held == held && f.mode == gitMode(info))):
		case kind == "dir":
			whole = rel + "/"
			under = l.add(LostRepository, whole)
			holding(under, rel, info, held)
		default:
			holding(l.add(LostRepository, rel), rel, info, held)
		}
		return nil
	}
}

// indexHolds reports whether an index entry at rel holds the mode and
// object given as "mode object".
func (l *lostPaths) indexHolds(rel, working string) bool {
	return slices.ContainsFunc(l.index, func(e vcs.IndexEntry) bool { return e.Path == rel && e.Mode+" "+e.Object == working })
}

// stored decides every file the walk set aside as converted, by the blob
// git would store for it now (vcs.StoredObjects: its clean filters and
// line-ending conversion applied), never by what git reports changed, so
// neither an index flag nor a stale stat keeps a change off the list: one
// whose stored blob and mode are HEAD's at its path is not lost, and any
// other at a path HEAD's tree holds is LostChanged, with what its bytes
// hold. Every one is taken as the working tree's content at its path for
// the index entries list decides.
func (l *lostPaths) stored(ctx context.Context) error {
	paths := make([]string, len(l.converted))
	for i, f := range l.converted {
		paths[i] = f.rel
	}
	objects, err := l.repo.StoredObjects(ctx, paths)
	if err != nil {
		return err
	}
	for i, f := range l.converted {
		l.working[f.rel] = f.mode + " " + objects[i]
		if e, ok := l.head[f.rel]; ok && e.Type == "blob" && (f.mode != e.Mode || objects[i] != e.Object) {
			holding(l.add(LostChanged, f.rel), f.rel, f.info, f.held)
		}
	}
	l.converted = nil
	return nil
}

// list is every path found with what it is lost holding, the index and
// HEAD's tree decided last against what the walk read of the working tree,
// in lostKinds' order and by path within each.
func (l *lostPaths) list() []lostFile {
	l.deletions()
	for _, e := range l.index {
		if h, ok := l.head[e.Path]; ok && h.Mode == e.Mode && h.Object == e.Object {
			continue
		}
		if l.working[e.Path] == e.Mode+" "+e.Object {
			continue
		}
		d, ok := l.held[LostPath{Kind: LostStaged, Path: e.Path}]
		if !ok {
			d = l.add(LostStaged, e.Path)
		}
		d.record(e.Mode, e.Object, strconv.Itoa(e.Stage))
	}
	out := []lostFile{}
	for _, kind := range lostKinds {
		paths := l.found[kind]
		slices.Sort(paths)
		for _, p := range paths {
			at := LostPath{Kind: kind, Path: p}
			out = append(out, lostFile{LostPath: at, held: l.held[at].sum()})
		}
	}
	return out
}

// deletions names every entry of HEAD's tree the working tree or the index
// no longer holds, on lostPaths' terms.
func (l *lostPaths) deletions() {
	paths := make([]string, 0, len(l.head))
	for path := range l.head {
		paths = append(paths, path)
	}
	// Sorted, a directory comes before every path under it.
	slices.Sort(paths)
	whole := ""
	for _, path := range paths {
		if whole != "" && strings.HasPrefix(path, whole) {
			continue
		}
		whole = ""
		switch e := l.head[path]; {
		case e.Type == "tree" && !l.seen[path]:
			whole = path + "/"
			l.add(LostDeleted, whole).record(e.Type, e.Mode, e.Object)
		case e.Type != "tree" && (!l.seen[path] || !l.indexed[path]):
			l.add(LostDeleted, path).record(e.Type, e.Mode, e.Object)
		}
	}
}

// add finds path lost for kind and returns the digest what it holds is
// written into.
func (l *lostPaths) add(kind LostKind, path string) *digest {
	l.found[kind] = append(l.found[kind], path)
	d := newDigest()
	l.held[LostPath{Kind: kind, Path: path}] = d
	return d
}

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

// freshEntry is one entry of a new repository's directory: its type, what
// walkTree read it to hold, and its mode as gitMode reads it, the mode the
// digest seals, so a file of the copy's repository directory with the
// template's bytes and another mode, a hook made executable among them, is
// not taken for the template's.
type freshEntry struct{ kind, held, mode string }

// entryKind is an entry's type as walkTree reads it.
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
// skip the copy's repository directory is walked with, and git the options
// the copy was opened with, its deadline among them.
func freshRepository(ctx context.Context, skip func(rel string, info os.FileInfo) bool, git ...vcs.Option) (map[string]freshEntry, error) {
	dir, err := os.MkdirTemp("", "assistant-fresh-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "repository")
	if _, err := vcs.InitBare(ctx, path, git...); err != nil {
		return nil, fmt.Errorf("initializing one: %w", err)
	}
	fresh := map[string]freshEntry{}
	err = walkTree(path, skip, func(rel string, info os.FileInfo, held string) error {
		fresh[rel] = freshEntry{kind: entryKind(info), held: held, mode: gitMode(info)}
		return nil
	})
	return fresh, err
}
