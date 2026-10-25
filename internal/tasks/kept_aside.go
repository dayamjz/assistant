package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/safety"
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
//     so content staged between that read and the hold refuses; then
//     every path git reports the
//     working tree lacking of that index (vcs.IndexPathsMissing) is
//     written from it without force (vcs.CheckoutMissing), one at a time,
//     each journalled with the blob it is written from before it is
//     written, so a file written there meanwhile refuses and a restore
//     knows every file the discard may have written: only a path under
//     one set aside, one the loss shows deleted, or, when HEAD moves, one
//     HEAD's tree does not hold as the target's does, and never a
//     submodule's; any other missing path is a deletion nobody was shown,
//     which refuses;
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
//   - An index entry is replaced without an anchor of git's, and the
//     window differs by where the content is staged (round 31):
//     - at a path whose entries the discard replaces or a restore puts
//       back, content staged after the discard's hold of the index, or
//       after the restore's own read of it, is replaced unread, since
//       vcs.ReplaceIndexEntries reads nothing at the paths it is given;
//       staged before that read, the discard refuses and the restore
//       leaves it where it is;
//     - at a path an entry written is a file or a directory of, content
//       staged after vcs.ReplaceIndexEntries' own read of the index is
//       taken out unread; staged before it, the write refuses, and a
//       restore leaves it where it is (restoreIndex).
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

// supportedLayout refuses, with ErrLayoutUnsupported naming the layout,
// a copy whose repository is not laid out the one way Loss, Discard and
// PutBack support, before anything of it is read or changed (round 25:
// each layout the set-aside tried to handle opened another way to lose
// work, so they are refused rather than handled; P6, a safety fact that
// cannot be verified refuses). The one layout is:
//
//   - for a task's clone, .git a directory of the clone's own, holding no
//     commondir file and no linked worktree; for a run's worktree, .git
//     the file git writes, which copyRecord holds to the gate's record of
//     the worktree;
//   - no symbolic link anywhere in the repository directory git reads for
//     the copy, its references, reflogs, objects and index among it;
//   - core.symlinks not false, no sparse checkout, and the references in
//     files (no reftable);
//   - the whole history (partialHistory, round 32): not shallow (git
//     rev-parse --is-shallow-repository), no graft file (info/grafts),
//     no replacement references (refs/replace/), and not a partial clone
//     (extensions.partialClone unset, no remote.<name>.promisor true, and
//     no promisor pack), since in each git reads the commits and objects a
//     preview is built from as other than the copy holds, and a discard
//     removes what the preview never showed.
//
// Before any of that, git is asked which working tree and repository
// directory it reads for the copy (vcs.Observe: rev-parse --show-toplevel
// --absolute-git-dir), and both must be the copy's own: the working tree
// the copy itself, and for a clone the repository directory its own .git,
// each compared once links are resolved.
// So a copy git reads as part of another checkout is refused rather than
// read or recovered into, whether through a .git git does not take for a
// repository, a core.worktree of the copy naming another directory, or
// another repository's core.worktree naming the copy. The configuration is read as git
// reads it for the copy, the user's and the system's included. The
// repository directory is walked through the file system, so a link is
// refused wherever it stands.
func (c *Cleaner) supportedLayout(ctx context.Context, entry store.KeptWork, gatePath string) error {
	if err := c.VerifyKeptCopy(ctx, entry, gatePath); err != nil {
		return err
	}
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s %s", ErrLayoutUnsupported, entry.CopyPath, why)
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, c.git...)
	if err != nil {
		return refuse("could not be opened: " + err.Error())
	}
	if entry.TaskID.IsKnown() {
		dotGit := filepath.Join(entry.CopyPath, ".git")
		if _, err := os.Lstat(filepath.Join(dotGit, "commondir")); !errors.Is(err, fs.ErrNotExist) {
			return refuse("has a commondir file in its .git, naming another directory its references and objects are in")
		}
		if linked, err := os.ReadDir(filepath.Join(dotGit, "worktrees")); err == nil && len(linked) > 0 {
			return refuse("has a linked worktree")
		}
	}
	if symlinks, set, err := copied.ConfigBool(ctx, "core.symlinks"); err != nil {
		return refuse("has a core.symlinks that could not be read: " + err.Error())
	} else if set && !symlinks {
		return refuse("sets core.symlinks false, so git checks a link out as a file")
	}
	if sparse, _, err := copied.ConfigBool(ctx, "core.sparseCheckout"); err != nil || sparse {
		return refuse("is a sparse checkout, whose paths left out read as deleted")
	}
	if format, set, err := copied.ConfigValue(ctx, "extensions.refStorage"); err != nil || set && format != "files" {
		return refuse("keeps its references in a " + format + " store rather than in files")
	}
	return partialHistory(ctx, copied, refuse)
}

// partialHistory refuses, through refuse, a copy whose history git reads
// as other than every commit its objects record, or whose objects may name
// what it does not hold: shallow, a graft file, replacement references, or
// a partial clone. Each is read the way git reads it for the copy, through
// internal/vcs, and one that cannot be read refuses as well (round 32).
func partialHistory(ctx context.Context, copied *vcs.Repository, refuse func(string) error) error {
	unread := func(what string, err error) error {
		return refuse("could not be read for " + what + ": " + err.Error())
	}
	if shallow, err := copied.IsShallow(ctx); err != nil {
		return unread("whether its history is shallow", err)
	} else if shallow {
		return refuse("is shallow, so the commits its history leaves out are read as not there and no preview shows them")
	}
	// The graft file is found through its directory: the Cleaner reads
	// with GIT_GRAFT_FILE set to the null device (vcs.WithoutReplaceObjects),
	// which git's --git-path info/grafts answers with.
	info, err := copied.GitPath(ctx, "info")
	if err != nil {
		return unread("a graft file", err)
	}
	grafts := filepath.Join(info, "grafts")
	if _, err := os.Lstat(grafts); !errors.Is(err, fs.ErrNotExist) {
		return refuse("has a graft file at " + grafts + ", which git reads a commit's parents through")
	}
	if replace, err := copied.ListRefs(ctx, "refs/replace/"); err != nil {
		return unread("replacement references", err)
	} else if len(replace) > 0 {
		return refuse("has replacement references, " + replace[0].Name + " among them, which git reads an object through as another")
	}
	if remote, set, err := copied.ConfigValue(ctx, "extensions.partialClone"); err != nil {
		return unread("extensions.partialClone", err)
	} else if set {
		return refuse("is a partial clone: extensions.partialClone names " + remote + ", so its objects may name what it does not hold")
	}
	promisors, err := copied.ConfigNames(ctx, `^remote\..*\.promisor$`)
	if err != nil {
		return unread("promisor remotes", err)
	}
	for _, key := range promisors {
		if promisor, _, err := copied.ConfigBool(ctx, key); err != nil {
			return unread(key, err)
		} else if promisor {
			return refuse("is a partial clone: " + key + " is true, so its objects may name what it does not hold")
		}
	}
	packs, err := copied.GitPath(ctx, "objects/pack")
	if err != nil {
		return unread("promisor packs", err)
	}
	marked, err := filepath.Glob(filepath.Join(packs, "*.promisor"))
	if err != nil {
		return unread("promisor packs", err)
	}
	if len(marked) > 0 {
		return refuse("is a partial clone: it holds the promisor pack " + marked[0] + ", whose objects may name what it does not hold")
	}
	return nil
}

// VerifyKeptCopy refuses, before anything of the entry's copy is read or
// written, a copy that is not the one its registry recorded, on
// internal/safety's terms (safety.VerifyKeptCopy): the registry is the
// entry's copy path and its own .git for a task's clone, and for a run's
// worktree the gate at gatePath, whose registry names the worktree's
// record (copyRecord, which also holds the worktree's .git file to it).
// Every path that reads or writes a kept copy asks it first: the loss a
// discard shows and a land reads, the discard, its set-aside and its
// put-back, and carry on, before its claim and, when a stopped carry on is
// taken up, on the copy where it stands, at the reserved task's path once
// moved (internal/service's verifyCarriedCopy, with the entry's copy path
// set to where it stands). A refusal matches ErrLayoutUnsupported, and
// safety.ErrCopyNotVerified or ErrRecordNotTheCopys as well.
func (c *Cleaner) VerifyKeptCopy(ctx context.Context, entry store.KeptWork, gatePath string) error {
	want := safety.KeptCopy{Worktree: entry.CopyPath, GitDir: filepath.Join(entry.CopyPath, ".git")}
	if !entry.TaskID.IsKnown() {
		bare, err := vcs.OpenBare(ctx, gatePath, c.git...)
		if err != nil {
			return fmt.Errorf("tasks: opening the gate repository at %s: %w", gatePath, err)
		}
		if want.GitDir, err = copyRecord(ctx, bare, entry.CopyPath); err != nil {
			return fmt.Errorf("%w: %w", ErrLayoutUnsupported, err)
		}
	}
	if err := safety.VerifyKeptCopy(ctx, want, c.git...); err != nil {
		return fmt.Errorf("%w: %w", ErrLayoutUnsupported, err)
	}
	return nil
}

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
	// is every path the discard writes from the target's tree, with the
	// entry it writes there, each journalled before it is written.
	Moved   []string        `json:"moved,omitempty"`
	Emptied []asideDirEntry `json:"emptied,omitempty"`
	Created []string        `json:"created,omitempty"`
	Written []writtenPath   `json:"written,omitempty"`
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

// writtenPath is a path a discard writes from the target's tree and the
// tree's entry there, git's mode and the blob.
type writtenPath struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Object string `json:"object"`
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

// asidePaths is what the discard moves aside, and the target's tree
// (target, HEAD's commit when it is not lost), which the index is then
// made to hold. Moved is every path of the loss the working tree holds (a
// path lost whole once, as the directory) and, when target is not HEAD's
// commit, every file of HEAD's tree the target's does not hold as it is
// that the working tree holds (before) under no path already moved. A
// submodule the two trees hold differently refuses with ErrLossChanged.
// What is written back is not decided here but from what git reports
// missing once the index is the target's (setAside).
func asidePaths(ctx context.Context, copied *vcs.Repository, copyPath string, loss Loss, target string, before copyState) (moved []string, head, targetTree map[string]vcs.TreeEntry, err error) {
	head, err = copied.AllTreeEntries(ctx, loss.Head)
	if err != nil {
		return nil, nil, nil, err
	}
	targetTree = head
	if target != loss.Head {
		if targetTree, err = copied.AllTreeEntries(ctx, target); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, f := range loss.Files {
		if f.Kind == LostNew || f.Kind == LostChanged {
			moved = append(moved, strings.TrimSuffix(f.Path, "/"))
		}
	}
	submodule := func(p string) error {
		return fmt.Errorf("%w: the submodule at %s is not set aside, and the default branch's tree does not hold it as HEAD's does", ErrLossChanged, p)
	}
	for _, p := range sortedPaths(head) {
		e := head[p]
		if e.Type == "tree" || targetTree[p] == e {
			continue
		}
		if e.Type == "commit" {
			return nil, nil, nil, submodule(p)
		}
		if _, stands := before[filepath.Join(copyPath, p)]; stands && !underAny(p, moved) {
			moved = append(moved, p)
		}
	}
	for _, p := range sortedPaths(targetTree) {
		if e := targetTree[p]; e.Type == "commit" && head[p] != e {
			return nil, nil, nil, submodule(p)
		}
	}
	return moved, head, targetTree, nil
}

// underAny reports whether p is one of paths, each relative to the copy
// with no trailing slash, or lies under one.
func underAny(p string, paths []string) bool {
	for _, q := range paths {
		if p == q || strings.HasPrefix(p, q+"/") {
			return true
		}
	}
	return false
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
	j := asideJournal{HeadCommit: loss.Head, HeadBranch: head, Detached: clone && lost[loss.Head], Against: loss.against}
	target := loss.Head
	if j.Detached {
		target = j.Against
	}
	moved, headTree, targetTree, err := asidePaths(ctx, copied, entry.CopyPath, loss, target, before.tree)
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
	// What git reports the working tree lacking of the index, now the
	// target's tree, is written, each path journalled with the entry it is
	// written from before it is written, so a restore knows every file the
	// discard may have written however far it got. Only a path whose absence
	// the discard made or the person was shown is written: one under a path
	// set aside, one the loss shows deleted, and, when HEAD moves, one
	// HEAD's tree does not hold as the target's does; any other missing
	// path is a deletion nobody was shown, which refuses. A gitlink is
	// never written: a submodule's directory missing stays missing, and the
	// read once everything is set aside refuses it.
	var deleted []string
	for _, f := range loss.Files {
		if f.Kind == LostDeleted {
			deleted = append(deleted, strings.TrimSuffix(f.Path, "/"))
		}
	}
	missing, err := copied.IndexPathsMissing(ctx)
	if err != nil {
		return refuse(err)
	}
	for _, p := range missing {
		e, ok := targetTree[p]
		if !ok || e.Type != "blob" {
			continue
		}
		if !underAny(p, moved) && !underAny(p, deleted) && headTree[p] == e {
			return refuse(fmt.Errorf("%w: %s is deleted from the working tree, which the discard was not shown", ErrLossChanged, p))
		}
		for d := filepath.Dir(p); d != "."; d = filepath.Dir(d) {
			if _, err := os.Lstat(filepath.Join(entry.CopyPath, d)); errors.Is(err, fs.ErrNotExist) && !slices.Contains(j.Created, d) {
				j.Created = append(j.Created, d)
			}
		}
		slices.SortStableFunc(j.Created, func(a, b string) int { return strings.Count(a, "/") - strings.Count(b, "/") })
		j.Written = append(j.Written, writtenPath{Path: p, Mode: e.Mode, Object: e.Object})
		if err := writeJournal(dir, j); err != nil {
			return refuse(err)
		}
		if c.beforeWrite != nil {
			c.beforeWrite(p)
		}
		if err := copied.CheckoutMissing(ctx, []string{p}); err != nil {
			return refuse(fmt.Errorf("%w: %s, which the discard writes from the default branch's or HEAD's tree, stands again: %w", ErrLossChanged, p, err))
		}
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

// keptIndexPath is an index path a restore leaves aside, and why.
type keptIndexPath struct {
	path asideIndexPath
	why  string
}

// restoreIndex puts back the entries each path of back held, leaving
// aside, each named in kept, every path whose entries would take out an
// entry the restore does not put back (vcs.IndexDisplaceError): content
// staged beside it since the discard wrote the index, at a path it is a
// file or a directory of (round 30), which the restore did not set aside.
// That holds for what vcs.ReplaceIndexEntries reads there, not for content
// staged after its read, which the header's gaps name. An error is
// returned with failed, the paths it did not put back besides kept.
func restoreIndex(ctx context.Context, copied *vcs.Repository, back []asideIndexPath) (kept []keptIndexPath, failed []asideIndexPath, err error) {
	for len(back) > 0 {
		err := replaceIndex(ctx, copied, back, func(p asideIndexPath) []vcs.IndexEntry { return p.Held })
		var displace *vcs.IndexDisplaceError
		if err == nil {
			return kept, nil, nil
		}
		if !errors.As(err, &displace) {
			return kept, back, err
		}
		displacing := map[string][]string{}
		for _, d := range displace.Displacing {
			displacing[d.Path] = append(displacing[d.Path], d.Displaced)
		}
		var rest []asideIndexPath
		for _, p := range back {
			if what, ok := displacing[p.Path]; ok {
				kept = append(kept, keptIndexPath{path: p, why: "the index entries of " + p.Path +
					", since putting them back would take out what is staged at " + strings.Join(what, ", ")})
			} else {
				rest = append(rest, p)
			}
		}
		if len(rest) == len(back) {
			// The refusal names no path being put back, so leaving some
			// aside cannot answer it: all of back stays.
			return kept, back, err
		}
		back = rest
	}
	return kept, nil, nil
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
// none stands, every path journalled as written from the target's tree is
// taken out again only while git would store it as the blob written there
// with its mode (holdsBlob), whoever wrote it, since its content is the
// tree's, and every directory that made only while it is empty, each
// index path is put back only while it holds what the discard wrote when
// the restore reads the index, and only where that takes out no entry
// beside it (restoreIndex), the windows after each read being the
// header's, every
// directory emptied is made again with its mode, and every path set aside
// is moved back only where nothing has taken its place. What could not
// be put back stays in dir and the journal, named in the ErrAsideStands it
// returns; once all of it is back, dir is removed.
func (c *Cleaner) restoreAside(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, dir string) error {
	j, err := readJournal(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return clearUnjournalled(dir)
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
	var written []writtenPath
	var format string
	for i := len(j.Written) - 1; i >= 0; i-- {
		w := j.Written[i]
		path := filepath.Join(entry.CopyPath, filepath.FromSlash(w.Path))
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if format == "" {
			if format, err = copied.ObjectFormat(ctx); err != nil {
				left = append(left, w.Path+": "+err.Error())
				written = append([]writtenPath{w}, written...)
				continue
			}
		}
		ours, err := holdsBlob(ctx, copied, format, entry.CopyPath, w)
		switch {
		case err != nil:
			left = append(left, w.Path+": "+err.Error())
		case !ours:
			left = append(left, w.Path+", which does not hold what the discard writes there from HEAD's or the default branch's tree")
		default:
			if err := os.Remove(path); err != nil {
				left = append(left, w.Path+": "+err.Error())
			} else {
				continue
			}
		}
		written = append([]writtenPath{w}, written...)
	}
	j.Written = written
	if err := save(); err != nil {
		return err
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
			kept, failed, err := restoreIndex(ctx, copied, back)
			for _, k := range kept {
				left = append(left, k.why)
				stays = append(stays, k.path)
			}
			if err != nil {
				left = append(left, "the index: "+err.Error())
				stays = append(stays, failed...)
			}
			slices.SortFunc(stays, func(a, b asideIndexPath) int { return strings.Compare(a.Path, b.Path) })
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

// clearUnjournalled removes dir, a set-aside directory with no journal,
// in which nothing was set aside: setAside writes the first journal before
// it moves anything. What it can hold is writeJournal's partial file,
// journal.json.tmp, left by a discard that stopped during its first write
// before the rename (round 32), which is the program's own and is
// removed. Anything else in it is no file this program writes there
// without a journal, so it is left, refused with ErrAsideStands naming it.
func clearUnjournalled(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%w at %s: it could not be read: %w", ErrAsideStands, dir, err)
	}
	var others []string
	for _, e := range entries {
		if e.Name() != journalName+".tmp" {
			others = append(others, e.Name())
		}
	}
	if len(others) > 0 {
		return fmt.Errorf("%w at %s: it has no journal and holds %s, which no discard writes there without one", ErrAsideStands, dir, strings.Join(others, ", "))
	}
	if err := os.Remove(filepath.Join(dir, journalName+".tmp")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w at %s: %w", ErrAsideStands, dir, err)
	}
	return os.Remove(dir)
}

// holdsBlob reports whether the working-tree entry at w.Path of the copy
// is what the discard writes there: git's mode w.Mode and, by the blob git
// would store for it (its bytes as they stand, or with its clean filters
// and line-ending conversion applied, vcs.StoredObjects), w.Object. A file
// that holds it is the discard's to take out, whoever wrote it: what it
// holds is the tree's.
func holdsBlob(ctx context.Context, copied *vcs.Repository, format, copyPath string, w writtenPath) (bool, error) {
	path := filepath.Join(copyPath, filepath.FromSlash(w.Path))
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if gitMode(info) != w.Mode {
		return false, nil
	}
	object, err := vcs.BlobObject(format, path)
	if err != nil || object == w.Object || !info.Mode().IsRegular() {
		return object == w.Object, err
	}
	stored, err := copied.StoredObjects(ctx, []string{w.Path})
	if err != nil {
		return false, err
	}
	return stored[0] == w.Object, nil
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
