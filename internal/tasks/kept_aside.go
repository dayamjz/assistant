package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A discard deletes what the person was shown and confirmed and nothing
// else (the owner's decision of 2026-10-24, narrowing the discard). It
// never removes the copy's directory by force. It touches only what the
// removal needs gone and the confirmed loss names, each item by itself,
// and first sets it aside, recoverably, into a directory of its own beside
// the copy (asideDir), with a journal of what it does written before it
// does it:
//
//   - every path the loss shows of the working tree, moved there whole,
//     each held to the state read before the loss was (moveAsRead), so a
//     change made since refuses;
//   - when a task's clone's HEAD stands on a lost commit, every file of
//     HEAD's tree that the tree of the commit the default branch stood on
//     when the loss was read (the target) does not hold as it is, moved
//     there the same way: the content of a commit the person confirmed as
//     lost;
//   - every directory those moves leave empty, removed;
//   - the index entries of every path whose entries are not the target's
//     tree's (HEAD's commit's when HEAD does not move), each path's
//     entries recorded and replaced by the target's (vcs.ReplaceIndexEntries),
//     after the whole index is held to what was read before the loss was,
//     so content staged since refuses; then every file of the target's
//     tree the working tree no longer holds as it is written from it
//     without force (vcs.CheckoutMissing), so a file written there
//     meanwhile refuses;
//   - for a task's clone only, which P12's proof reads by its HEAD and its
//     branches, tags and stash: HEAD, when its commit is lost, detached
//     onto the target anchored to the commit it was read at, touching no
//     file (vcs.MoveHEAD), and every branch, tag and the stash pointing at
//     an object the loss shows as lost, deleted anchored to that object
//     (vcs.DeleteRefAt), each first with its own reflog moved there and
//     held to what it was before the loss was read, so an entry written
//     since refuses.
//
// No other reflog, reference or index entry is touched. A submodule
// HEAD's tree and the target's tree hold differently is not set aside: the
// discard refuses.
//
// Then the copy is read again (the same raw-graph loss read) and must lose
// nothing the loss confirmed did not name: every commit and object lost is
// one shown, which the reflogs left in place are read for, no path of the
// working tree or the index is lost, and every entry of the repository
// directory but HEAD is as the loss read it (beyondShown). Only then is
// the directory removed, by a removal that refuses when anything is left:
// a run's linked worktree by git's own worktree removal without force
// (vcs.RemoveWorktree), a task's clone on P12's proof (removeOnProof). A
// refusal anywhere puts everything back as the journal says
// (restoreAside), each item only from where the discard put it and each
// struck from the journal once it is back, so a restore that stopped
// takes up where it stopped. Nothing is deleted but what was confirmed,
// and that only once the copy is gone. A stop part way leaves the journal,
// and the next discard of the copy puts everything back before it reads
// anything; until then Loss refuses with ErrAsideStands.
//
// What this does not cover:
//
//   - Between the read once everything is set aside and the removal, a
//     file written into a run's worktree record in the gate goes with it,
//     since git removes that directory whole, and so does a file git
//     ignores written into the worktree, which git's removal without
//     force does not refuse; for a task's clone, a file git does not see
//     written between removeOnProof's last inventory and the removal goes
//     with the clone, P12 cleanup's own window.
//   - An index entry is replaced without an anchor of git's: content
//     staged at the same path between the read that holds the index to
//     what it was and the replacement is replaced unread.
//   - A reflog moved aside is held to its content before the loss was
//     read, so an entry written and taken out again in between, leaving
//     the same bytes, is not seen.
//   - The flags git keeps beside an index entry (skip-worktree,
//     assume-unchanged, intent-to-add) are not put back with it.
//   - What a process writes through a descriptor it opened in the copy
//     before the discard moved that file aside is lost with it.
//   - A clone removal that fails part way is not put back: what was set
//     aside was confirmed and goes, and what was removed of the rest,
//     which the read found to lose nothing unconfirmed, stays removed.
//   - P12 cleanup does not look for what a stopped discard set aside: one
//     that removes the copy on a proof before the next answer leaves it,
//     and the next confirmed discard finds the copy gone and removes it,
//     all of it confirmed by that discard.
//   - A file written exactly where something set aside goes back keeps
//     both, and the copy is not read again until a person moves one of
//     them (ErrAsideStands names which).

// ErrAsideStands is returned by Loss and by Discard for a copy beside which
// a discard that did not finish left what it set aside, and by Discard
// when that could not be put back, naming where it is: nothing in it is
// lost, and the copy is read only once it is back.
var ErrAsideStands = errors.New("tasks: a discard that did not finish set part of the copy aside, so it is not read until that is put back")

// asideDir is where a discard of the copy at copyPath sets what it confirms
// aside, beside the copy and so on its file system, under a name no copy
// takes.
func asideDir(copyPath string) string {
	return filepath.Join(filepath.Dir(copyPath), "."+filepath.Base(copyPath)+".discarding")
}

// asideJournal is what a discard set aside and how to put it back, written
// before each step and read by restoreAside, which strikes each item from
// it once the item is back.
type asideJournal struct {
	// HeadCommit and HeadBranch are HEAD as it stood: its commit and, when
	// it was on a branch, that branch in full.
	HeadCommit string `json:"head_commit"`
	HeadBranch string `json:"head_branch,omitempty"`
	// Detached is whether the discard moves HEAD onto Against, the target,
	// and HeadMoved whether it did.
	Detached  bool   `json:"detached"`
	Against   string `json:"against,omitempty"`
	HeadMoved bool   `json:"head_moved,omitempty"`
	// Moved is every working-tree path set aside under tree/, relative to
	// the copy; Emptied is every directory the moves left empty and the
	// discard removed, deepest first, with its mode; Created is every
	// directory writing the target's tree made, shallowest first; Written
	// is every path written from the target's tree, with the state it was
	// written in.
	Moved   []string              `json:"moved,omitempty"`
	Emptied []asideDirEntry       `json:"emptied,omitempty"`
	Created []string              `json:"created,omitempty"`
	Written map[string]stateEntry `json:"-"`
	Wrote   []writtenPath         `json:"written,omitempty"`
	// Index is every path whose index entries the discard replaced, with
	// the entries it held and those written.
	Index []asideIndexPath `json:"index,omitempty"`
	// Logs is every reflog moved aside, HEAD's and each deleted
	// reference's, and Refs every reference deleted.
	Logs []asideLog `json:"logs,omitempty"`
	Refs []asideRef `json:"refs,omitempty"`
}

// asideIndexPath is a path whose index entries a discard replaced: the
// entries it held (Held) and those the discard wrote (Wrote), either
// possibly none.
type asideIndexPath struct {
	Path  string           `json:"path"`
	Held  []vcs.IndexEntry `json:"held,omitempty"`
	Wrote []vcs.IndexEntry `json:"wrote,omitempty"`
}

// asideLog is a reflog a discard moved aside: where git keeps it and its
// name under the aside directory.
type asideLog struct {
	Path  string `json:"path"`
	Aside string `json:"aside"`
}

// writtenPath is one entry of asideJournal.Written as the journal holds it.
type writtenPath struct {
	Path string      `json:"path"`
	Mode fs.FileMode `json:"mode"`
	Held string      `json:"held"`
}

// asideDirEntry is a directory a discard removed once it was empty, by its
// path relative to the copy, and its mode.
type asideDirEntry struct {
	Path string      `json:"path"`
	Mode fs.FileMode `json:"mode"`
}

// asideRef is a reference a discard deleted and what it pointed at.
type asideRef struct {
	Name   string `json:"name"`
	Object string `json:"object"`
}

const journalName = "journal.json"

func writeJournal(dir string, j asideJournal) error {
	j.Wrote = j.Wrote[:0]
	for p, s := range j.Written {
		j.Wrote = append(j.Wrote, writtenPath{Path: p, Mode: s.mode, Held: s.held})
	}
	slices.SortFunc(j.Wrote, func(a, b writtenPath) int { return strings.Compare(a.Path, b.Path) })
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, journalName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, journalName))
}

func readJournal(dir string) (asideJournal, error) {
	raw, err := os.ReadFile(filepath.Join(dir, journalName))
	if err != nil {
		return asideJournal{}, err
	}
	var j asideJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return asideJournal{}, err
	}
	j.Written = map[string]stateEntry{}
	for _, w := range j.Wrote {
		j.Written[w.Path] = stateEntry{mode: w.Mode, held: w.Held}
	}
	return j, nil
}

// asideBefore is what a discard reads of the copy before it reads the loss,
// which what it sets aside is held to: the working tree's state
// (workingState), the index's entries and, for a task's clone, the state
// of its reflogs, by absolute path.
type asideBefore struct {
	tree  copyState
	index []vcs.IndexEntry
	logs  copyState
}

// readAsideBefore reads asideBefore of the entry's copy.
func readAsideBefore(ctx context.Context, entry store.KeptWork, copied *vcs.Repository) (asideBefore, error) {
	var b asideBefore
	var err error
	if b.tree, err = workingState(entry.CopyPath); err != nil {
		return asideBefore{}, err
	}
	if b.index, err = copied.IndexEntries(ctx); err != nil {
		return asideBefore{}, err
	}
	b.logs = copyState{}
	if entry.TaskID.IsKnown() {
		logs, err := copied.GitPath(ctx, "logs")
		if err != nil {
			return asideBefore{}, err
		}
		if b.logs, err = readCopyState(logs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return asideBefore{}, err
		} else if err != nil {
			b.logs = copyState{}
		}
	}
	return b, nil
}

// workingState is the state of every entry of the copy's working tree,
// its repository directory or file aside, by absolute path.
func workingState(copyPath string) (copyState, error) {
	state := copyState{}
	entries, err := os.ReadDir(copyPath)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		sub, err := readCopyState(filepath.Join(copyPath, e.Name()))
		if err != nil {
			return nil, err
		}
		for p, s := range sub {
			state[p] = s
		}
	}
	return state, nil
}

// asidePaths is what the discard moves aside and what it then writes from
// the target's tree (target, HEAD's commit when it is not lost). Moved is
// every path of the loss the working tree holds (a path lost whole once, as
// the directory) and, when target is not HEAD's commit, every file of
// HEAD's tree the target's does not hold as it is that the working tree
// holds (before) under no path already moved. Written is every file of the
// target's tree that HEAD's does not hold as it is, or that the loss shows
// changed or deleted, a directory deleted whole by the files under it. A
// submodule the two trees hold differently refuses with ErrLossChanged.
func asidePaths(ctx context.Context, copied *vcs.Repository, copyPath string, loss Loss, target string, before copyState) (moved, written []string, targetTree map[string]vcs.TreeEntry, err error) {
	head, err := copied.AllTreeEntries(ctx, loss.Head)
	if err != nil {
		return nil, nil, nil, err
	}
	targetTree = head
	if target != loss.Head {
		if targetTree, err = copied.AllTreeEntries(ctx, target); err != nil {
			return nil, nil, nil, err
		}
	}
	changed := map[string]bool{}
	var deleted []string
	for _, f := range loss.Files {
		path := strings.TrimSuffix(f.Path, "/")
		switch f.Kind {
		case LostNew, LostChanged:
			moved = append(moved, path)
			if f.Kind == LostChanged {
				changed[path] = true
			}
		case LostDeleted:
			deleted = append(deleted, f.Path)
		}
	}
	under := func(p string, paths []string) bool {
		for _, q := range paths {
			if p == strings.TrimSuffix(q, "/") || strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/") {
				return true
			}
		}
		return false
	}
	for _, p := range sortedPaths(head) {
		e := head[p]
		if e.Type == "tree" || targetTree[p] == e {
			continue
		}
		if e.Type == "commit" {
			return nil, nil, nil, fmt.Errorf("%w: the submodule at %s is not set aside, and the default branch's tree does not hold it as HEAD's does", ErrLossChanged, p)
		}
		if _, stands := before[filepath.Join(copyPath, p)]; stands && !under(p, moved) {
			moved = append(moved, p)
		}
	}
	for _, p := range sortedPaths(targetTree) {
		e := targetTree[p]
		if e.Type == "tree" {
			continue
		}
		if head[p] == e && !changed[p] && !under(p, deleted) {
			continue
		}
		if e.Type == "commit" {
			return nil, nil, nil, fmt.Errorf("%w: the submodule at %s is not set aside, and the default branch's tree does not hold it as HEAD's does", ErrLossChanged, p)
		}
		written = append(written, p)
	}
	return moved, written, targetTree, nil
}

// sortedPaths is a tree's paths in order, so what a discard does follows
// the same order every time.
func sortedPaths(tree map[string]vcs.TreeEntry) []string {
	paths := make([]string, 0, len(tree))
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}

// setAside sets the loss's confirmed items aside on this file's terms,
// journalled in dir before each is touched. before is what was read of the
// copy before the loss was. A refusal puts back what was set aside and
// returns ErrLossChanged; one that could not be put back returns
// ErrAsideStands as well.
func (c *Cleaner) setAside(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, loss Loss, before asideBefore, dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("%w at %s: %w", ErrAsideStands, dir, err)
	}
	refuse := func(why error) error {
		if restoreErr := c.restoreAside(ctx, entry, copied, dir); restoreErr != nil {
			return fmt.Errorf("%w; %w", why, restoreErr)
		}
		return why
	}
	head, err := copied.HEADTarget(ctx)
	if err != nil {
		return refuse(err)
	}
	lost := map[string]bool{}
	for _, c := range loss.Lacks {
		lost[c] = true
	}
	for _, o := range loss.Objects {
		lost[o.Object] = true
	}
	clone := entry.TaskID.IsKnown()
	j := asideJournal{HeadCommit: loss.Head, HeadBranch: head, Detached: clone && lost[loss.Head], Against: loss.against,
		Written: map[string]stateEntry{}}
	target := loss.Head
	if j.Detached {
		target = j.Against
	}
	moved, written, targetTree, err := asidePaths(ctx, copied, entry.CopyPath, loss, target, before.tree)
	if err != nil {
		return refuse(err)
	}
	j.Moved = moved
	if clone {
		inv, err := copied.Inventory(ctx)
		if err != nil {
			return refuse(err)
		}
		for _, r := range inv.Refs {
			if lost[r.Object] {
				j.Refs = append(j.Refs, asideRef{Name: r.Name, Object: r.Object})
			}
		}
	}
	if err := writeJournal(dir, j); err != nil {
		return refuse(err)
	}

	// The working tree's paths, each held to the state read before the
	// loss was, then the directories that leaves empty.
	for _, rel := range moved {
		if _, err := moveAsRead(filepath.Join(entry.CopyPath, rel), filepath.Join(dir, "tree", rel), before.tree, rel); err != nil {
			return refuse(err)
		}
	}
	for _, rel := range moved {
		for d := filepath.Dir(rel); d != "."; d = filepath.Dir(d) {
			path := filepath.Join(entry.CopyPath, d)
			children, err := os.ReadDir(path)
			if err != nil || len(children) > 0 {
				break
			}
			info, err := os.Lstat(path)
			if err != nil {
				return refuse(err)
			}
			j.Emptied = append(j.Emptied, asideDirEntry{Path: d, Mode: info.Mode()})
			if err := writeJournal(dir, j); err != nil {
				return refuse(err)
			}
			if err := os.Remove(path); err != nil {
				// Something was written into it meanwhile: it stays, and
				// the read once everything is set aside refuses it.
				break
			}
		}
	}
	// The index entries that are not the target's tree's, the index held
	// to what was read before the loss was, then the target's tree written
	// where the working tree no longer holds it, never over a file that
	// stands.
	now, err := copied.IndexEntries(ctx)
	if err != nil {
		return refuse(err)
	}
	if !slices.Equal(now, before.index) {
		return refuse(fmt.Errorf("%w: the index changed after the loss was read", ErrLossChanged))
	}
	j.Index = indexPlan(now, targetTree)
	if err := writeJournal(dir, j); err != nil {
		return refuse(err)
	}
	if err := replaceIndex(ctx, copied, j.Index, func(p asideIndexPath) []vcs.IndexEntry { return p.Wrote }); err != nil {
		return refuse(err)
	}
	for _, p := range written {
		var missing []string
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			if _, err := os.Lstat(filepath.Join(entry.CopyPath, d)); errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, d)
			}
		}
		for i := len(missing) - 1; i >= 0; i-- {
			if !slices.Contains(j.Created, missing[i]) {
				j.Created = append(j.Created, missing[i])
			}
		}
	}
	if err := writeJournal(dir, j); err != nil {
		return refuse(err)
	}
	if err := copied.CheckoutMissing(ctx, written); err != nil {
		return refuse(fmt.Errorf("%w: a path the discard writes from the default branch's or HEAD's tree stands again: %w", ErrLossChanged, err))
	}
	for _, p := range written {
		s, err := stateOf(filepath.Join(entry.CopyPath, p))
		if err != nil {
			return refuse(err)
		}
		j.Written[p] = s
	}
	if err := writeJournal(dir, j); err != nil {
		return refuse(err)
	}
	// HEAD, when its commit is lost, onto the target, its reflog set aside
	// first.
	if j.Detached {
		if err := c.moveLogAside(ctx, copied, "HEAD", before.logs, dir, &j); err != nil {
			return refuse(err)
		}
		if err := copied.MoveHEAD(ctx, j.Against, loss.Head); err != nil {
			return refuse(fmt.Errorf("%w: HEAD could not be moved off the lost commit %s: %w", ErrLossChanged, loss.Head, err))
		}
		j.HeadMoved = true
		if err := writeJournal(dir, j); err != nil {
			return refuse(err)
		}
	}
	// The names of what is lost, each reflog set aside first.
	for _, r := range j.Refs {
		if err := c.moveLogAside(ctx, copied, r.Name, before.logs, dir, &j); err != nil {
			return refuse(err)
		}
		if err := copied.DeleteRefAt(ctx, r.Name, r.Object); err != nil {
			return refuse(fmt.Errorf("%w: %s no longer points at %s: %w", ErrLossChanged, r.Name, r.Object, err))
		}
	}
	return nil
}

// indexPlan is every path whose index entries (now) are not exactly the
// target's tree's entry there at stage 0, with what it holds and what the
// discard writes.
func indexPlan(now []vcs.IndexEntry, targetTree map[string]vcs.TreeEntry) []asideIndexPath {
	held := map[string][]vcs.IndexEntry{}
	for _, e := range now {
		held[e.Path] = append(held[e.Path], e)
	}
	wrote := map[string][]vcs.IndexEntry{}
	for p, e := range targetTree {
		if e.Type == "blob" || e.Type == "commit" {
			wrote[p] = []vcs.IndexEntry{{Mode: e.Mode, Object: e.Object, Stage: 0, Path: p}}
		}
	}
	paths := map[string]bool{}
	for p := range held {
		paths[p] = true
	}
	for p := range wrote {
		paths[p] = true
	}
	var plan []asideIndexPath
	for p := range paths {
		if !slices.Equal(held[p], wrote[p]) {
			plan = append(plan, asideIndexPath{Path: p, Held: held[p], Wrote: wrote[p]})
		}
	}
	slices.SortFunc(plan, func(a, b asideIndexPath) int { return strings.Compare(a.Path, b.Path) })
	return plan
}

// replaceIndex writes, at every path of plan, the entries entriesOf gives.
func replaceIndex(ctx context.Context, copied *vcs.Repository, plan []asideIndexPath, entriesOf func(asideIndexPath) []vcs.IndexEntry) error {
	var paths []string
	var entries []vcs.IndexEntry
	for _, p := range plan {
		paths = append(paths, p.Path)
		entries = append(entries, entriesOf(p)...)
	}
	return copied.ReplaceIndexEntries(ctx, paths, entries)
}

// moveLogAside moves the reflog of name (HEAD, or a reference in full)
// into dir, journalled first, held to what it was before the loss was read
// (logs), so an entry written since refuses. A reflog that stands neither
// then nor now is nothing to move.
func (c *Cleaner) moveLogAside(ctx context.Context, copied *vcs.Repository, name string, logs copyState, dir string, j *asideJournal) error {
	path, err := copied.GitPath(ctx, "logs/"+name)
	if err != nil {
		return err
	}
	l := asideLog{Path: path, Aside: filepath.Join("logs", strconv.Itoa(len(j.Logs)))}
	j.Logs = append(j.Logs, l)
	if err := writeJournal(dir, *j); err != nil {
		return err
	}
	if _, err := moveAsRead(path, filepath.Join(dir, l.Aside), logs, "the reflog of "+name); err != nil {
		return err
	}
	return nil
}

// moveAsRead moves from to to, refusing with ErrLossChanged when what it
// holds once moved is not what before recorded of from, entry by entry
// both ways, named as what. It reports whether anything was moved: nothing
// stood at from, and nothing was recorded there, is not a change.
func moveAsRead(from, to string, before copyState, what string) (bool, error) {
	_, had := before[from]
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(from, to); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !had {
				return false, nil
			}
			return false, fmt.Errorf("%w: %s, shown as lost, is gone from the copy", ErrLossChanged, what)
		}
		return false, err
	}
	now, err := readCopyState(to)
	if err != nil {
		return true, err
	}
	for p, s := range now {
		was, ok := before[filepath.Join(from, strings.TrimPrefix(p, to))]
		if !ok || was != s {
			return true, fmt.Errorf("%w: %s is not what the copy held when the loss was read", ErrLossChanged,
				filepath.Join(what, strings.TrimPrefix(p, to)))
		}
	}
	for p := range before {
		if p == from || strings.HasPrefix(p, from+string(filepath.Separator)) {
			if _, ok := now[filepath.Join(to, strings.TrimPrefix(p, from))]; !ok {
				return true, fmt.Errorf("%w: %s, shown as lost, is gone from the copy", ErrLossChanged, filepath.Join(what, strings.TrimPrefix(p, from)))
			}
		}
	}
	return true, nil
}

// restoreAside puts back what the journal in dir says a discard set aside,
// in the reverse order, on what stands now, each item only from where the
// discard put it, and strikes each from the journal once it is back, so a
// restore that stopped takes up where it stopped: a reference is written
// back where it is gone, HEAD is put back on its branch or, detached,
// moved back anchored to the target, each reflog is moved back only where
// none stands, every path written from the target's tree is taken out
// again only while it is what was written and every directory that made
// only while it is empty, each index path is put back only while it holds
// what the discard wrote, every directory emptied is made again with its
// mode, and every path set aside is moved back only where nothing has
// taken its place. What could not be put back stays in dir and the
// journal, named in the ErrAsideStands it returns; once all of it is back,
// dir is removed.
func (c *Cleaner) restoreAside(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, dir string) error {
	j, err := readJournal(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// Nothing was set aside before the journal was written.
		return os.Remove(dir)
	}
	if err != nil {
		return fmt.Errorf("%w at %s: its journal could not be read: %w", ErrAsideStands, dir, err)
	}
	var left []string
	// save writes the journal as it stands, what stays for a later restore.
	save := func() error {
		if err := writeJournal(dir, j); err != nil {
			return fmt.Errorf("%w at %s: its journal could not be written: %w", ErrAsideStands, dir, err)
		}
		return nil
	}

	var refs []asideRef
	for i := len(j.Refs) - 1; i >= 0; i-- {
		r := j.Refs[i]
		if err := copied.CreateRefAt(ctx, r.Name, r.Object); err != nil && !refNames(ctx, copied, r) {
			left = append(left, "the reference "+r.Name+" at "+r.Object)
			refs = append([]asideRef{r}, refs...)
		}
	}
	j.Refs = refs
	if err := save(); err != nil {
		return err
	}
	if j.Detached {
		if what := restoreHEAD(ctx, copied, j); what != "" {
			left = append(left, what)
		} else {
			j.Detached, j.HeadMoved = false, false
			if err := save(); err != nil {
				return err
			}
		}
	}
	if len(j.Refs) == 0 && !j.Detached {
		// A reflog goes back only once its reference and HEAD stand as
		// they did, so writing them back appends nothing to it.
		var logs []asideLog
		for i := len(j.Logs) - 1; i >= 0; i-- {
			l := j.Logs[i]
			from := filepath.Join(dir, l.Aside)
			if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
				left = append(left, "the reflog at "+l.Path+": "+err.Error())
				logs = append([]asideLog{l}, logs...)
				continue
			}
			if err := renameNoReplace(from, l.Path); err != nil {
				left = append(left, "the reflog at "+l.Path+", since one stands there: "+err.Error())
				logs = append([]asideLog{l}, logs...)
			}
		}
		j.Logs = logs
		if err := save(); err != nil {
			return err
		}
	}
	for _, p := range slices.Sorted(maps.Keys(j.Written)) {
		wrote := j.Written[p]
		path := filepath.Join(entry.CopyPath, p)
		now, err := stateOf(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil || now != wrote:
			left = append(left, p+", which changed after the discard wrote it from HEAD's tree")
			continue
		default:
			if err := os.Remove(path); err != nil {
				left = append(left, p+": "+err.Error())
				continue
			}
		}
		delete(j.Written, p)
		if err := save(); err != nil {
			return err
		}
	}
	var created []string
	for i := len(j.Created) - 1; i >= 0; i-- {
		if err := os.Remove(filepath.Join(entry.CopyPath, j.Created[i])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, j.Created[i]+", a directory the discard made that holds something now: "+err.Error())
			created = append([]string{j.Created[i]}, created...)
		}
	}
	j.Created = created
	if err := save(); err != nil {
		return err
	}
	if len(j.Index) > 0 {
		now, err := copied.IndexEntries(ctx)
		if err != nil {
			left = append(left, "the index: "+err.Error())
		} else {
			at := map[string][]vcs.IndexEntry{}
			for _, e := range now {
				at[e.Path] = append(at[e.Path], e)
			}
			var back, stays []asideIndexPath
			for _, p := range j.Index {
				switch {
				case slices.Equal(at[p.Path], p.Held):
				case slices.Equal(at[p.Path], p.Wrote):
					back = append(back, p)
				default:
					left = append(left, "the index entries of "+p.Path+", which changed after the discard wrote them")
					stays = append(stays, p)
				}
			}
			if err := replaceIndex(ctx, copied, back, func(p asideIndexPath) []vcs.IndexEntry { return p.Held }); err != nil {
				left = append(left, "the index: "+err.Error())
				stays = append(stays, back...)
			}
			j.Index = stays
			if err := save(); err != nil {
				return err
			}
		}
	}
	var emptied []asideDirEntry
	for i := len(j.Emptied) - 1; i >= 0; i-- {
		d := j.Emptied[i]
		path := filepath.Join(entry.CopyPath, d.Path)
		if err := os.Mkdir(path, d.Mode.Perm()); err != nil {
			if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
				left = append(left, d.Path+": "+err.Error())
				emptied = append([]asideDirEntry{d}, emptied...)
				continue
			}
		}
		// Mkdir applies the umask, and one an earlier restore made may not
		// have had its mode yet; the mode is put back as it was.
		if err := os.Chmod(path, d.Mode.Perm()); err != nil {
			left = append(left, d.Path+": "+err.Error())
			emptied = append([]asideDirEntry{d}, emptied...)
		}
	}
	var moved []string
	for i := len(j.Moved) - 1; i >= 0; i-- {
		rel := j.Moved[i]
		from := filepath.Join(dir, "tree", rel)
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		to := filepath.Join(entry.CopyPath, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			left = append(left, rel+": "+err.Error())
			moved = append([]string{rel}, moved...)
			continue
		}
		if err := renameNoReplace(from, to); err != nil {
			left = append(left, rel+", since something stands at its path: "+err.Error())
			moved = append([]string{rel}, moved...)
		}
	}
	// What was made again and moved back is struck with the rest; a
	// restore that stopped before writing that finds the directory
	// standing and the path gone from dir, and does nothing for either.
	j.Emptied, j.Moved = emptied, moved
	if len(left) > 0 {
		if err := save(); err != nil {
			return err
		}
		return fmt.Errorf("%w at %s: %s could not be put back", ErrAsideStands, dir, strings.Join(left, "; "))
	}
	return os.RemoveAll(dir)
}

// restoreHEAD puts HEAD back where the journal j says it stood, only from
// where the discard put it, and says what it could not put back. HEAD on
// the target is put back, a move the journal did not record yet among
// them; HEAD already as it stood is left; and HEAD anywhere else is left
// where it is: moved there by somebody else, after the discard moved it
// (j.HeadMoved), which is named, since putting HEAD back would leave what
// it stands on unnamed, or before, which the discard never undid.
func restoreHEAD(ctx context.Context, copied *vcs.Repository, j asideJournal) string {
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return "HEAD at " + j.HeadCommit + ": " + err.Error()
	}
	target, err := copied.HEADTarget(ctx)
	if err != nil {
		return "HEAD at " + j.HeadCommit + ": " + err.Error()
	}
	switch {
	case j.HeadBranch != "" && target == j.HeadBranch, j.HeadBranch == "" && target == "" && inv.Head.Commit == j.HeadCommit:
		return ""
	case target == "" && inv.Head.Commit == j.Against:
		if j.HeadBranch != "" {
			err = copied.PointHEADAt(ctx, j.HeadBranch)
		} else {
			err = copied.MoveHEAD(ctx, j.HeadCommit, j.Against)
		}
		if err != nil {
			return "HEAD at " + j.HeadCommit + ": " + err.Error()
		}
		return ""
	case j.HeadMoved:
		return "HEAD at " + j.HeadCommit + ", since HEAD was moved to " + inv.Head.Commit + " after the discard moved it"
	}
	return ""
}

// refNames reports whether the reference r names already points at what it
// did, which a restore taking up where an earlier one stopped finds.
func refNames(ctx context.Context, copied *vcs.Repository, r asideRef) bool {
	refs, err := copied.ListRefs(ctx, r.Name)
	if err != nil {
		return false
	}
	for _, ref := range refs {
		if ref.Name == r.Name && ref.Object == r.Object {
			return true
		}
	}
	return false
}

// beyondShown refuses, with ErrLossChanged naming what remains, a copy
// whose loss read once the confirmed items were set aside (after) loses
// anything the loss confirmed (before) did not name: a commit or an object
// it did not list, any path of the working tree or the index, which every
// discard sets aside, or any entry of the repository directory but HEAD
// that is not as the loss read it. What the confirmed loss named and the
// discard left in place, a reflog's entries among it, the removal then
// deletes as confirmed. Git's three indicators are not read apart: a
// tracked change or a file no commit records is a path the read lists, and
// a stash is a commit it lists.
func beyondShown(before, after Loss) error {
	var left []string
	for _, c := range after.Lacks {
		if !slices.Contains(before.Lacks, c) {
			left = append(left, c)
		}
	}
	shownObjects := map[string]bool{}
	for _, o := range before.Objects {
		shownObjects[o.Object] = true
	}
	for _, o := range after.Objects {
		if !shownObjects[o.Object] {
			left = append(left, o.Type+" "+o.Object)
		}
	}
	shown := map[string]string{}
	for _, f := range before.files {
		if f.Kind == LostRepository {
			shown[f.Path] = f.held
		}
	}
	for _, f := range after.files {
		if f.Kind == LostRepository && (f.Path == "HEAD" || shown[f.Path] == f.held) {
			continue
		}
		left = append(left, string(f.Kind)+" "+f.Path)
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: once what was shown was set aside, the copy still holds %s, which was not shown", ErrLossChanged, strings.Join(left, ", "))
	}
	return nil
}
