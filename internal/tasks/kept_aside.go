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
	"strings"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A discard deletes what the person was shown and confirmed and nothing
// else (the owner's decision of 2026-10-24, narrowing the discard). It
// never removes the copy's directory by force. It first sets the confirmed
// items aside, recoverably, into a directory of its own beside the copy
// (asideDir), with a journal of what it did written before it does it:
//
//   - the reflogs the removal would destroy (the copy's own logs
//     directory, HEAD's among them);
//   - every path the loss shows of the working tree, moved there whole,
//     each held to the state read before the loss was (moveItemsAside),
//     so a change made since refuses;
//   - when HEAD's commit is lost, every file of HEAD's tree that the tree
//     of the commit the default branch stood on when the loss was read
//     (the target) does not hold as it is, moved there the same way: the
//     content of a commit the person confirmed as lost;
//   - every directory those moves leave empty, removed;
//   - the index, moved there, after which the index is the target's tree
//     (HEAD's commit's when it is not lost) and every path of it the
//     working tree no longer holds as it is is written from it without
//     force (vcs.CheckoutMissing), so a file written there meanwhile
//     refuses;
//   - HEAD, when its commit is lost, detached onto the target anchored to
//     the commit it was read at, touching no file (vcs.MoveHEAD);
//   - every reference and root reference pointing at an object the loss
//     shows as lost, deleted anchored to that object (vcs.DeleteRefAt).
//
// A submodule HEAD's tree and the target's tree hold differently is not
// set aside: the discard refuses.
//
// Then the copy is read again (the same raw-graph loss read) and must hold
// no commit, object or path the default branch or the upstream lacks but
// the repository directory's entries this discard already read and left,
// and none of git's indicators. Only then is the directory removed, by a
// removal that refuses when anything is left: a run's linked worktree by
// git's own worktree removal without force (vcs.RemoveWorktree), a task's
// clone on P12's proof (removeOnProof). A refusal anywhere puts everything
// back as the journal says (restoreAside), so nothing is deleted but what
// was confirmed, and that only once the copy is gone. A stop part way
// leaves the journal, and the next discard of the copy puts everything
// back before it reads anything; until then Loss refuses with ErrAsideStands.

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
// whole before the first step and read by restoreAside.
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
	// Logs is the copy's own logs directory, set aside whole as logs.
	Logs string `json:"logs,omitempty"`
	// Index is the copy's index file, set aside as index.
	Index string `json:"index"`
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
	// Refs is every reference deleted, with what it pointed at.
	Refs []asideRef `json:"refs,omitempty"`
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
func asidePaths(ctx context.Context, copied *vcs.Repository, copyPath string, loss Loss, target string, before copyState) (moved, written []string, err error) {
	head, err := copied.AllTreeEntries(ctx, loss.Head)
	if err != nil {
		return nil, nil, err
	}
	targetTree := head
	if target != loss.Head {
		if targetTree, err = copied.AllTreeEntries(ctx, target); err != nil {
			return nil, nil, err
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
			return nil, nil, fmt.Errorf("%w: the submodule at %s is not set aside, and the default branch's tree does not hold it as HEAD's does", ErrLossChanged, p)
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
			return nil, nil, fmt.Errorf("%w: the submodule at %s is not set aside, and the default branch's tree does not hold it as HEAD's does", ErrLossChanged, p)
		}
		written = append(written, p)
	}
	return moved, written, nil
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
// journalled in dir before each is touched. before is the working tree's
// state read before the loss was. lost is every object the loss shows as
// lost. A refusal puts back what was set aside and returns
// ErrLossChanged; one that could not be put back returns ErrAsideStands.
func (c *Cleaner) setAside(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, loss Loss, before copyState, dir string) error {
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
	logsHEAD, err := copied.GitPath(ctx, "logs/HEAD")
	if err != nil {
		return refuse(err)
	}
	index, err := copied.GitPath(ctx, "index")
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
	j := asideJournal{HeadCommit: loss.Head, HeadBranch: head, Detached: lost[loss.Head], Against: loss.against,
		Logs: filepath.Dir(logsHEAD), Index: index, Written: map[string]stateEntry{}}
	target := loss.Head
	if j.Detached {
		target = j.Against
	}
	moved, written, err := asidePaths(ctx, copied, entry.CopyPath, loss, target, before)
	if err != nil {
		return refuse(err)
	}
	j.Moved = moved
	refs, err := c.lostNames(ctx, entry, copied, lost)
	if err != nil {
		return refuse(err)
	}
	j.Refs = refs
	if err := writeJournal(dir, j); err != nil {
		return refuse(err)
	}

	// The reflogs, whole.
	if _, err := os.Lstat(j.Logs); err == nil {
		if err := os.Rename(j.Logs, filepath.Join(dir, "logs")); err != nil {
			return refuse(err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return refuse(err)
	}
	// The working tree's paths, each held to the state read before the
	// loss was, then the directories that leaves empty.
	for _, rel := range moved {
		if err := moveItemAside(entry.CopyPath, dir, rel, before); err != nil {
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
	// The index, then the target's tree written where the working tree no
	// longer holds it, never over a file that stands.
	if _, err := os.Lstat(index); err == nil {
		if err := os.Rename(index, filepath.Join(dir, "index")); err != nil {
			return refuse(err)
		}
	}
	if err := copied.ResetIndexTo(ctx, target); err != nil {
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
	// HEAD, when its commit is lost, onto the target.
	if j.Detached {
		if err := copied.MoveHEAD(ctx, j.Against, loss.Head); err != nil {
			return refuse(fmt.Errorf("%w: HEAD could not be moved off the lost commit %s: %w", ErrLossChanged, loss.Head, err))
		}
		j.HeadMoved = true
		if err := writeJournal(dir, j); err != nil {
			return refuse(err)
		}
	}
	// The names of what is lost.
	for _, r := range j.Refs {
		if err := copied.DeleteRefAt(ctx, r.Name, r.Object); err != nil {
			return refuse(fmt.Errorf("%w: %s no longer points at %s: %w", ErrLossChanged, r.Name, r.Object, err))
		}
	}
	return nil
}

// moveItemAside moves the working-tree path rel of the copy into dir's
// tree, refusing with ErrLossChanged when it is gone or when what it holds
// once moved is not what before recorded of it.
func moveItemAside(copyPath, dir, rel string, before copyState) error {
	from := filepath.Join(copyPath, rel)
	to := filepath.Join(dir, "tree", rel)
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s, shown as lost, is gone from the copy", ErrLossChanged, rel)
		}
		return err
	}
	now, err := readCopyState(to)
	if err != nil {
		return err
	}
	for p, s := range now {
		was, ok := before[filepath.Join(copyPath, rel, strings.TrimPrefix(p, to))]
		if !ok || was != s {
			return fmt.Errorf("%w: %s is not what the copy held when the loss was read", ErrLossChanged,
				filepath.Join(rel, strings.TrimPrefix(p, to)))
		}
	}
	prefix := filepath.Join(copyPath, rel)
	for p := range before {
		if p == prefix || strings.HasPrefix(p, prefix+string(filepath.Separator)) {
			if _, ok := now[filepath.Join(to, strings.TrimPrefix(p, prefix))]; !ok {
				return fmt.Errorf("%w: %s, shown as lost, is gone from the copy", ErrLossChanged, strings.TrimPrefix(p, copyPath+string(filepath.Separator)))
			}
		}
	}
	return nil
}

// lostNames is every reference and root reference the discard deletes:
// those the removal destroys, on copyNames' terms, that point at an object
// lost. The object a reference names directly decides: a tag object that
// leads to a lost commit is itself an object the loss shows as lost, since
// nothing that holds the tag lacks its commit.
func (c *Cleaner) lostNames(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, lost map[string]bool) ([]asideRef, error) {
	refs, _, err := copyNames(ctx, entry, copied)
	if err != nil {
		return nil, err
	}
	var out []asideRef
	for _, r := range refs {
		if lost[r.Object] {
			out = append(out, asideRef{Name: r.Name, Object: r.Object})
		}
	}
	return out, nil
}

// restoreAside puts back what the journal in dir says a discard set aside,
// in the reverse order, on what stands now: a reference is written back
// where it is gone, HEAD is put back on its branch or, detached, moved back
// anchored to the target, every path written from the target's tree is
// taken out again only while it is what was written and every directory
// that made only while it is empty, the index is put back, every directory
// emptied is made again with its mode where none stands, every path set
// aside is moved back only where nothing has taken its place, and the logs
// directory only where none stands. What could not be put back is left in
// dir, named in the ErrAsideStands it returns; once all of it is back, dir
// is removed.
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
	for i := len(j.Refs) - 1; i >= 0; i-- {
		r := j.Refs[i]
		if err := copied.CreateRefAt(ctx, r.Name, r.Object); err != nil && !refNames(ctx, copied, r) {
			left = append(left, "the reference "+r.Name+" at "+r.Object)
		}
	}
	if j.Detached {
		if what := restoreHEAD(ctx, copied, j); what != "" {
			left = append(left, what)
		}
	}
	for p, wrote := range j.Written {
		path := filepath.Join(entry.CopyPath, p)
		now, err := stateOf(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || now != wrote {
			left = append(left, p+", which changed after the discard wrote it from HEAD's tree")
			continue
		}
		if err := os.Remove(path); err != nil {
			left = append(left, p+": "+err.Error())
		}
	}
	for i := len(j.Created) - 1; i >= 0; i-- {
		if err := os.Remove(filepath.Join(entry.CopyPath, j.Created[i])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, j.Created[i]+", a directory the discard made that holds something now: "+err.Error())
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "index")); err == nil {
		if err := os.Rename(filepath.Join(dir, "index"), j.Index); err != nil {
			left = append(left, "the index: "+err.Error())
		}
	}
	for i := len(j.Emptied) - 1; i >= 0; i-- {
		d := j.Emptied[i]
		path := filepath.Join(entry.CopyPath, d.Path)
		if err := os.Mkdir(path, d.Mode.Perm()); err != nil {
			if info, statErr := os.Lstat(path); statErr == nil && info.IsDir() {
				continue
			}
			left = append(left, d.Path+": "+err.Error())
			continue
		}
		// Mkdir applies the umask; the mode is put back as it was.
		if err := os.Chmod(path, d.Mode.Perm()); err != nil {
			left = append(left, d.Path+": "+err.Error())
		}
	}
	for i := len(j.Moved) - 1; i >= 0; i-- {
		rel := j.Moved[i]
		from := filepath.Join(dir, "tree", rel)
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		to := filepath.Join(entry.CopyPath, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			left = append(left, rel+": "+err.Error())
			continue
		}
		if err := renameNoReplace(from, to); err != nil {
			left = append(left, rel+", since something stands at its path: "+err.Error())
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "logs")); err == nil {
		if err := renameNoReplace(filepath.Join(dir, "logs"), j.Logs); err != nil {
			left = append(left, "the reflogs, since logs stand at "+j.Logs+": "+err.Error())
		}
	}
	if len(left) > 0 {
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
	if err == nil {
		for _, ref := range refs {
			if ref.Name == r.Name && ref.Object == r.Object {
				return true
			}
		}
	}
	roots, err := copied.RootRefs(ctx)
	if err == nil {
		for _, ref := range roots {
			if ref.Name == r.Name && ref.Object == r.Object {
				return true
			}
		}
	}
	return false
}

// emptyOfWork refuses, with ErrLossChanged naming what remains, a copy
// whose loss read once the confirmed items were set aside (after) still
// holds any commit or object lost, any path of the working tree or the
// index, or any entry of the repository directory but HEAD and those the
// loss read before (before) showed as they were: the copy is provably empty
// of work only when nothing else is left. Git's three indicators are not
// read apart: a tracked change or a file no commit records is a path the
// read lists, and a stash is a commit it lists.
func emptyOfWork(before, after Loss) error {
	var left []string
	left = append(left, after.Lacks...)
	for _, o := range after.Objects {
		left = append(left, o.Type+" "+o.Object)
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
		return fmt.Errorf("%w: once what was shown was set aside, the copy still holds %s", ErrLossChanged, strings.Join(left, ", "))
	}
	return nil
}
