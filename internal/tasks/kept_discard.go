package tasks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Discarding kept work: PRD section 6's "you name the copy, the program
// shows exactly what will be lost, and it removes the copy only on that
// answer". This file is the two halves of that the copy itself decides:
// what would be lost, read from the copy and the entry now, and the removal,
// made only when the copy still holds exactly what was shown. Who may answer
// discard, and the record of who did, are internal/service's and
// internal/store's; nothing here records anything.
//
// # What would be lost
//
// Loss is what the copy holds that is nowhere safe as far as this program
// can see, read from the copy as it stands when it is shown: the commits
// that anything the removal destroys names reaches, HEAD, a reference, a
// root reference such as ORIG_HEAD, or a reflog entry by the value it moved
// to or the one it moved from, a commit reset away and every stash among
// them, and that the default branch, read fresh as Keep reads it, lacks,
// with every object such a name points at that is not a commit (lostFrom),
// all decided by git from the copy's own objects; every path the removal destroys whose content or
// deletion nothing else holds, a file git ignores and a deletion git has not
// staged among them, decided from what the copy holds on lostPaths' terms
// (kept_lost.go); and the three indicators vcs.Inventory reads as git
// reports them, a tracked change, a file no commit records that git does not
// ignore, and a stash. The entry's recorded basis is not what is shown,
// because the copy may hold more since it was kept and a discard destroys
// the copy as it is. A default branch that cannot be read now refuses the
// preview with ErrLossUnestablished rather than showing a list that may
// overstate or understate. A copy that is gone is ErrCopyGone.
//
// The digest is over the copy's state as a discard would destroy it
// (copyState), each piece a framed record (digest), so no name, target or
// content can stand for a boundary, and the commits shown as lost (seal).
// It is decided from what the copy holds, never from what git reports
// changed, so neither git's ignore rules nor its configuration can keep a
// file out of it:
//
//   - the copy's path and the commit HEAD stands on;
//   - every entry under the copy, walked by the filesystem, a file git
//     ignores and a nested repository's own .git among them: its path, its
//     type, its full mode, and a hash of a file's bytes or a link's target;
//   - the repository directory the removal deletes with it, a task's
//     clone's .git or the directory the gate keeps for a run's worktree,
//     walked the same way (its configuration, hooks, and any operation in
//     progress), except the entries git rewrites without changing what they
//     hold, which Loss's own fetch writes to (gitDirSealedByContent), sealed
//     instead by what they hold: every reference and the object it names,
//     every entry of every reflog, the stash list whole among them, and
//     every entry of the index at every stage by git's own object identity;
//     for a run's worktree the references are its own and the stash
//     (runWorktreeRefs) and the reflogs HEAD's and theirs, since everything
//     else it reads is the gate's, shared with every other run;
//   - the three indicators;
//   - the paths shown as lost, by kind and path (seal), which the copy's
//     state decides but for what git writes into a new repository, so a
//     change to that between showing and confirming refuses too.
//
// The default branch's tip is not in it: it is not the copy's, and a
// discard answered and stopped before its removal would otherwise be
// refused for good by an unrelated commit landing upstream. The same copy
// in the same state digests the same, and anything the removal would
// destroy that changes since moves it, which is what lets Discard refuse a
// copy that no longer holds exactly what was shown (ErrLossChanged). It is
// a confirmation and not a secret.
//
// What it does not cover, and what that costs. An object nothing names, no
// reference, reflog entry or index entry, is not sealed: one written since
// the discard was shown leaves the digest as it was, and a stash dropped
// before it and not yet pruned, which only git fsck finds, is destroyed
// with the copy and named by neither the digest nor the list. What git
// keeps outside its references names commits the list does not walk from:
// FETCH_HEAD and MERGE_HEAD, which git lists as no reference, and the state
// of an operation in progress (a rebase's or a sequencer's directory); each
// is shown as a repository path, its file and not the commits it names. A
// reflog entry whose previous value is a tree, a blob or a tag is not
// listed by that value (vcs.NotReachedFrom says when git writes one). A tree, a
// blob or an annotated tag a name points at is listed whatever else holds
// it, which fails toward showing. A run's
// worktree's stash is the gate's, which the removal leaves standing; it is
// sealed all the same, so a stash made in another worktree of the gate in
// between refuses the confirmation, which is shown again. And git's own
// maintenance, which a fetch may start, can rewrite what is sealed by what
// it holds, expiring an old reflog entry among other things, which refuses
// the confirmation the same way: each of these fails toward keeping.
//
// # The removal
//
// Discard removes a task's clone with os.RemoveAll and a run's linked
// worktree through vcs.RemoveWorktreeDiscardingChanges under the gate's
// copies lock, the discarding removal cleanup.go names as reachable and
// unused: this is its one use, and what stands behind it is the digest
// match against the person's answer rather than a P12 proof, which a
// discard by its nature has none of. A copy already gone is not refused:
// the answer and the digest it carried say the removal was wanted, and
// nothing stands to lose.
//
// What the removal does not cover: the moment between Discard's last read
// of the copy, the one that checks the digest, and the removal is not
// closed. Anything written into the copy in it, a file git ignores among
// it, is destroyed without having been shown, and what Discard returns as
// lost does not name it; nothing here stops a process writing into the
// copy while it is removed. internal/service's keptdiscard.go says what
// the record is left holding then.

// Loss is what discarding a kept copy would lose, as read now.
type Loss struct {
	// Head is the commit the copy's HEAD stands on.
	Head string
	// Lacks is the commits the default branch lacks that anything the
	// removal destroys names reaches (lostFrom), read from the copy as it
	// stands, never the entry's record of it when it was kept: for a
	// task's clone HEAD, every reference, every worktree's HEAD, its root
	// references and every reflog entry by both its values, the stash list
	// whole among them; for a run's worktree HEAD, its own references and
	// root references, and the reflogs of HEAD, of those and of the stash.
	// It is wider than what Keep records, which leaves the stash and the
	// reflogs out.
	Lacks []string
	// Dirty, Untracked and Stash are the inventory's indicators of the copy
	// as it stands now, as git reports them: a tracked change, a file no
	// commit records that git does not ignore, and a stash, none of which a
	// commit the default branch could lack records. Files names every path
	// they speak of, and the ignored ones they do not.
	Dirty, Untracked, Stash bool
	// Files is every path the removal destroys whose content or deletion
	// nothing else holds, decided from what the copy holds (lostPaths), in
	// Kind's order and by path within it.
	Files []LostPath
	// Objects is every object a name the removal destroys points at that
	// is not a commit, an annotated tag, a tree or a blob, with the names
	// that point at it, in the order they are named (lostFrom).
	Objects []LostObject
	// Digest is the confirmation of exactly this: the copy's state, the
	// commits lacking and the files, as shown (seal).
	Digest string
	// state is copyState's digest of the copy's state, which the digest
	// seals with the commits shown as lost.
	state string
}

// LostPath is one path a discard destroys whose content or deletion
// nothing else holds: where it is and why it is lost. Path is relative to
// the copy for every kind but LostRepository, whose path is relative to
// the repository directory the removal deletes, and a Path ending in a
// slash is a directory lost whole, everything under it with it.
type LostPath struct {
	Kind LostKind
	Path string
}

// LostObject is an object a name the removal destroys points at that is
// not a commit: its identifier, its type as git records it (tag, tree or
// blob), and every name the removal destroys that points at it, HEAD, a
// reference's full name, a root reference's name such as AUTO_MERGE, or a
// reflog entry as ref@{n}. It is listed
// whatever else holds it, failing toward showing: an annotated tag the
// upstream holds under its own name too is listed all the same.
type LostObject struct {
	Object string
	Type   string
	Names  []string
}

// LostKind is why a path is lost. The set is closed.
type LostKind string

// The kinds, in the order Files lists them.
const (
	// LostChanged is a working-tree file at a path HEAD's tree holds a file
	// at, holding other content, another mode or another type.
	LostChanged LostKind = "changed"
	// LostNew is a working-tree entry at a path HEAD's tree holds nothing
	// at, or a directory where it holds a file or a submodule: a file git
	// ignores, one it reports untracked, and a nested repository alike.
	LostNew LostKind = "new"
	// LostStaged is an index entry holding content neither HEAD's tree
	// nor the working tree holds at its path.
	LostStaged LostKind = "staged"
	// LostDeleted is a path HEAD's tree holds an entry at that the working
	// tree or the index holds nothing at: a deletion, staged or not, whose
	// content HEAD holds and whose deletion nothing else does.
	LostDeleted LostKind = "deleted"
	// LostRepository is an entry of the repository directory the removal
	// deletes, not among those sealed by what they hold
	// (gitDirSealedByContent), whose content differs from what git writes
	// into a new repository at that path: its configuration, a hook, the
	// state of an operation in progress, and the pointer files git keeps
	// there.
	LostRepository LostKind = "repository"
)

// lostKinds is the kinds in Files' order.
var lostKinds = []LostKind{LostChanged, LostNew, LostStaged, LostDeleted, LostRepository}

// ErrLossUnestablished is returned by Loss and Discard when what the
// default branch lacks cannot be read now: what would be lost cannot be
// shown exactly.
var ErrLossUnestablished = errors.New("tasks: what the copy holds that the default branch lacks is not established, so what a discard would lose cannot be shown")

// ErrCopyGone is returned by Loss for an entry whose copy is not standing.
var ErrCopyGone = errors.New("tasks: the kept copy is gone")

// ErrLossChanged is returned by Discard when the copy no longer holds
// exactly what was shown: the digest the answer carried is not the copy's.
var ErrLossChanged = errors.New("tasks: the copy no longer holds exactly what was shown, so it was not removed")

// Loss reads what discarding the entry's copy would lose, on this file's
// terms.
func (c *Cleaner) Loss(ctx context.Context, entry store.KeptWork) (Loss, error) {
	standing, err := c.standing(entry.CopyPath)
	if err != nil {
		return Loss{}, err
	}
	if !standing {
		return Loss{}, fmt.Errorf("%w: nothing stands at %s", ErrCopyGone, entry.CopyPath)
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	// What is lost is read from the copy now, from every name the removal
	// destroys, so what is shown is what the copy holds when it is shown
	// and the digest seals exactly that, never the entry's record of the
	// copy when it was kept.
	refs, logs, err := copyNames(ctx, entry, copied)
	if err != nil {
		return Loss{}, err
	}
	var l landing
	if taskID, ok := entry.TaskID.Get(); ok {
		task, err := c.store.Task(ctx, taskID)
		if err != nil {
			return Loss{}, err
		}
		if l, err = c.taskLanding(ctx, task, copied, nil); err != nil {
			return Loss{}, err
		}
	} else {
		run, err := c.store.Run(ctx, entry.RunID.Or(""))
		if err != nil {
			return Loss{}, err
		}
		if l, err = c.runLanding(ctx, run, copied, nil); err != nil {
			return Loss{}, err
		}
	}
	// The default branch is fetched under a namespace of this read's own,
	// never the cleanup decision's: a cleanup of the same copy may run at
	// once (recovery asks it of every finished run's standing copy), and
	// its forget would delete the reference this read is about to resolve.
	if l.namespace, err = discardNamespace(entry); err != nil {
		return Loss{}, err
	}
	named := namesOf(inv.Head.Commit, refs, logs)
	// Besides every name read, git's own sets, read as it walks: for a
	// task's clone, removed whole, every reference and every worktree's
	// HEAD; and every reflog entry's previous value as well as its new one,
	// for a run's worktree its own and the stash's alone.
	reach := vcs.Reach{Objects: named.objects, Refs: entry.TaskID.IsKnown(), Reflogs: true, ThisWorktree: !entry.TaskID.IsKnown()}
	held, others := lostFrom(ctx, l, reach)
	if !held.established() {
		return Loss{}, fmt.Errorf("%w: %s", ErrLossUnestablished, held.unestablished)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: held.lacks, Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: inv.Stash}
	for _, object := range others {
		kind, err := copied.ObjectType(ctx, object)
		if err != nil {
			return Loss{}, fmt.Errorf("tasks: reading what %s is in the kept copy at %s: %w", object, entry.CopyPath, err)
		}
		loss.Objects = append(loss.Objects, LostObject{Object: object, Type: kind, Names: named.names[object]})
	}
	if loss.state, loss.Files, err = copyState(ctx, entry, copied, inv, refs, logs); err != nil {
		return Loss{}, err
	}
	loss.Digest = seal(loss.state, loss.Lacks, loss.Files, loss.Objects)
	return loss, nil
}

// copyNames is every name the removal destroys that git lists, read from
// the copy: for a task's clone, removed whole, every reference in it, the
// program's own included, its root references (vcs.RootRefs: ORIG_HEAD and
// the others an operation writes, HEAD aside, which Loss reads from the
// inventory), and every reflog it keeps; for a run's worktree, its own
// references (runWorktreeRefs), its own root references, and the reflogs of
// HEAD and of its references. A reflog entry's previous value is not among
// them, since no read here lists it; lostFrom reaches it through git's own
// walk of the reflogs.
func copyNames(ctx context.Context, entry store.KeptWork, copied *vcs.Repository) ([]vcs.Ref, []vcs.ReflogEntry, error) {
	var refs []vcs.Ref
	var logs []vcs.ReflogEntry
	var err error
	roots, err := copied.RootRefs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("tasks: reading the root references of the kept copy at %s: %w", entry.CopyPath, err)
	}
	roots = slices.DeleteFunc(roots, func(ref vcs.Ref) bool { return ref.Name == "HEAD" })
	if entry.TaskID.IsKnown() {
		if refs, err = copied.ListRefs(ctx); err != nil {
			return nil, nil, fmt.Errorf("tasks: reading the references of the kept copy at %s: %w", entry.CopyPath, err)
		}
		if logs, err = copied.AllReflogs(ctx); err != nil {
			return nil, nil, fmt.Errorf("tasks: reading the reflogs of the kept copy at %s: %w", entry.CopyPath, err)
		}
		return append(roots, refs...), logs, nil
	}
	if refs, err = copied.ListRefs(ctx, runWorktreeRefs...); err != nil {
		return nil, nil, fmt.Errorf("tasks: reading the references of the kept copy at %s: %w", entry.CopyPath, err)
	}
	names := []string{"HEAD"}
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	if logs, err = copied.Reflogs(ctx, names...); err != nil {
		return nil, nil, fmt.Errorf("tasks: reading the reflogs of the kept copy at %s: %w", entry.CopyPath, err)
	}
	return append(roots, refs...), logs, nil
}

// names is every object a name the removal destroys points at directly, in
// the order they are named (HEAD, each reference, each reflog entry), each
// once, and the names of each: HEAD, a reference's full name, and a reflog
// entry as its reference and position, ref@{n}.
type names struct {
	objects []string
	names   map[string][]string
}

func namesOf(head string, refs []vcs.Ref, logs []vcs.ReflogEntry) names {
	n := names{names: map[string][]string{}}
	add := func(object, name string) {
		if _, ok := n.names[object]; !ok {
			n.objects = append(n.objects, object)
		}
		n.names[object] = append(n.names[object], name)
	}
	add(head, "HEAD")
	for _, ref := range refs {
		add(ref.Object, ref.Name)
	}
	for _, l := range logs {
		add(l.Object, l.Ref+"@{"+strconv.Itoa(l.Index)+"}")
	}
	return n
}

// gitDirSealedByContent is the entries of a repository directory whose
// bytes copyState does not read, because git rewrites them without changing
// what they hold, and Loss's own read writes some of them: the object
// database, which the read's fetch adds to and git's maintenance repacks;
// the references and their logs, in whichever form the repository keeps
// them, which that fetch writes a reference under for the read's length
// and maintenance may pack; and the index, whose recorded file times a
// person's own git status refreshes. Each is read by what it holds
// instead: every reference and its object, every entry of every reflog,
// and every entry of the index. What the object database holds that none
// of those name is the residual gap the header states.
var gitDirSealedByContent = []string{"objects", "refs", "packed-refs", "logs", "reftable", "index"}

// runWorktreeRefs is the references a run's worktree holds of its own,
// which the discard's removal of the worktree takes with it, and the stash
// a person makes there, which the gate keeps for every worktree of it:
// everything else a run's worktree reads is the gate's, shared with every
// other run.
var runWorktreeRefs = []string{"refs/worktree/", "refs/bisect/", "refs/rewritten/", "refs/stash"}

// copyState is the digest of the copy's state as a discard would destroy
// it, on this file's terms, every record framed in one stream, and the
// paths lost with it, on lostPaths' terms. Both are decided from what the
// copy holds, never from what git reports changed: the working tree is
// walked by the filesystem whatever git ignores or reports, and git is
// asked only for what its own files hold in a form they are rewritten in
// (gitDirSealedByContent) and for HEAD's tree.
func copyState(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, inv vcs.Inventory, refs []vcs.Ref,
	logs []vcs.ReflogEntry) (string, []LostPath, error) {
	d := newDigest()
	d.record("copy", entry.CopyPath, inv.Head.Commit)
	for _, ref := range refs {
		d.record("ref", ref.Name, ref.Object, ref.Commit)
	}
	for _, l := range logs {
		d.record("reflog", l.Ref, strconv.Itoa(l.Index), l.Object)
	}
	index, err := copied.IndexEntries(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	for _, e := range index {
		d.record("index", e.Mode, e.Object, strconv.Itoa(e.Stage), e.Path)
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return "", nil, fmt.Errorf("tasks: finding the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	gitDir, err := os.Stat(obs.GitDir)
	if err != nil {
		return "", nil, fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	lost, err := newLostPaths(ctx, copied, inv.Head.Commit, index)
	if err != nil {
		return "", nil, fmt.Errorf("tasks: reading what the kept copy at %s holds against HEAD: %w", entry.CopyPath, err)
	}
	// The working tree, everything under the copy but the repository
	// directory standing in it, which is walked on its own terms below;
	// a nested repository's .git, a submodule's among them, is walked
	// here as files like any other.
	err = recordTree(d, "tree", entry.CopyPath, func(rel string, info os.FileInfo) bool {
		return rel == ".git" && info.IsDir() && os.SameFile(info, gitDir)
	}, lost.worktree(entry.CopyPath))
	if err != nil {
		return "", nil, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	// The repository directory: a task's clone's .git, or the directory
	// the gate keeps for a run's worktree, which its removal deletes.
	sealedByContent := func(rel string, _ os.FileInfo) bool { return slices.Contains(gitDirSealedByContent, rel) }
	fresh, err := freshRepository(ctx, sealedByContent)
	if err != nil {
		return "", nil, fmt.Errorf("tasks: reading what git writes into a new repository: %w", err)
	}
	if err := recordTree(d, "gitdir", obs.GitDir, sealedByContent, lost.repository(fresh)); err != nil {
		return "", nil, fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	d.record("indicators", strconv.FormatBool(inv.Dirty), strconv.FormatBool(inv.Untracked), strconv.FormatBool(inv.Stash))
	return d.sum(), lost.list(), nil
}

// discardNamespace is a namespace no other read uses, under which one Loss
// fetches what it compares against: the entry's owner and a random suffix,
// so two reads of one copy at once never share a reference either.
func discardNamespace(entry store.KeptWork) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("tasks: naming the references a discard reads under: %w", err)
	}
	owner := "run/" + entry.RunID.Or("")
	if id, ok := entry.TaskID.Get(); ok {
		owner = "task/" + id
	}
	return "refs/assistant/discard/" + owner + "/" + hex.EncodeToString(suffix[:]), nil
}

// seal is the digest a discard is shown and confirmed with: the copy's
// state, copyState's digest of it, the commits shown as lost, and the paths
// shown as lost. The
// default branch's tip is not sealed. It is not the copy's, so a commit
// landing on it that the copy does not hold changes nothing a discard
// destroys, and sealing it would refuse a recorded discard for good
// whenever the upstream moved before the removal. What it can change is
// what of the copy is lost, which Discard holds to what was shown instead.
func seal(state string, lacks []string, files []LostPath, objects []LostObject) string {
	d := newDigest()
	d.record("state", state)
	d.record(append([]string{"lacks"}, lacks...)...)
	for _, f := range files {
		d.record("file", string(f.Kind), f.Path)
	}
	for _, o := range objects {
		d.record(append([]string{"object", o.Object, o.Type}, o.Names...)...)
	}
	return d.sum()[:12]
}

// digest is the framing every digest here is written in: each record is a
// count of its fields and then each field as its length and its bytes, so
// no byte string in a name, a link's target or a content's hash can be read
// as a boundary between fields or records, and two different sequences of
// records never write the same bytes.
type digest struct{ h hash.Hash }

func newDigest() *digest { return &digest{h: sha256.New()} }

// record writes one record of fields.
func (d *digest) record(fields ...string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(fields)))
	_, _ = d.h.Write(n[:])
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		_, _ = d.h.Write(n[:])
		_, _ = d.h.Write([]byte(f))
	}
}

// sum is the digest of every record written, in hexadecimal.
func (d *digest) sum() string { return hex.EncodeToString(d.h.Sum(nil)) }

// recordTree writes one record for every entry under root, root itself
// included, walked by the filesystem in lexical order and never through a
// link: its path relative to root, its type, its full mode (the type, the
// nine permission bits git keeps the executable ones of, and the rest a
// discard destroys too), and what it holds: a link's target, a regular
// file's bytes by their hash, nothing more for a directory, and nothing
// for anything else, a pipe or a socket, which has no bytes to read. An
// entry skip answers true for, by its path relative to root, is not
// recorded, and nothing under it is walked. Every record is framed on
// digest's terms, so no name, target or content can stand for a boundary.
// visit, when not nil, is given every entry recorded but root, with what
// its record says it holds, in the walk's order.
func recordTree(d *digest, label, root string, skip func(rel string, info os.FileInfo) bool, visit treeVisit) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if p != root && skip(rel, info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mode := strconv.FormatUint(uint64(info.Mode()), 8)
		held := ""
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if held, err = os.Readlink(p); err != nil {
				return err
			}
			d.record(label, rel, "link", mode, held)
		case info.IsDir():
			d.record(label, rel, "dir", mode)
		case info.Mode().IsRegular():
			if held, err = hashFile(p); err != nil {
				return err
			}
			d.record(label, rel, "file", mode, held)
		default:
			d.record(label, rel, "other", mode)
		}
		if visit != nil && p != root {
			return visit(rel, info, held)
		}
		return nil
	})
}

// treeVisit is what recordTree gives each entry it records: its path
// relative to the root, what Lstat read of it, and what its record says it
// holds, a link's target or a file's hash, empty for anything else.
type treeVisit func(rel string, info os.FileInfo, held string) error

// hashFile is a hash of the file's bytes, read as a stream.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Discard removes the entry's copy when it still holds exactly what digest
// was shown for, on this file's terms, and returns what was lost. shown is
// the commits the discard was shown as lost, which digest seals with the
// copy's state: the copy read now must seal with them to digest, so it
// holds exactly what it held when shown, and what the default branch read
// now lacks of it must be among them, so a commit that would be lost now
// and was not shown is refused with ErrLossChanged. gate is the gate repository a run's
// copy is a worktree of, and is not read for a task's. A copy already gone
// returns the zero Loss and no error.
//
// What the commit check does not cover: a default branch that moved only
// forward over commits the copy does not hold changes nothing here, and one
// that took up commits the copy holds shrinks the loss, both of which
// proceed; one that was rewound or force-pushed past a commit it had can
// make the copy the only place that commit is, which is refused.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, digest string, shown []string, gatePath string) (Loss, error) {
	loss, err := c.Loss(ctx, entry)
	if errors.Is(err, ErrCopyGone) {
		return Loss{}, nil
	}
	if err != nil {
		return Loss{}, err
	}
	if sealed := seal(loss.state, shown, loss.Files, loss.Objects); sealed != digest {
		return Loss{}, fmt.Errorf("%w: it was shown as %s and holds what digests as %s now", ErrLossChanged, digest, sealed)
	}
	var unshown []string
	for _, commit := range loss.Lacks {
		if !slices.Contains(shown, commit) {
			unshown = append(unshown, commit)
		}
	}
	if len(unshown) > 0 {
		return Loss{}, fmt.Errorf("%w: the default branch now lacks %s, which the discard was not shown as lost", ErrLossChanged, strings.Join(unshown, ", "))
	}
	if c.betweenDiscardReadAndRemoval != nil {
		c.betweenDiscardReadAndRemoval()
	}
	if entry.TaskID.IsKnown() {
		if err := os.RemoveAll(entry.CopyPath); err != nil {
			return Loss{}, fmt.Errorf("tasks: discarding the copy at %s: %w", entry.CopyPath, err)
		}
		return loss, nil
	}
	bare, err := vcs.OpenBare(ctx, gatePath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: opening the gate repository at %s: %w", gatePath, err)
	}
	lock, err := gate.LockCopies(ctx, bare.Path())
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: waiting to discard the copy at %s: %w", entry.CopyPath, err)
	}
	defer func() { _ = lock.Release() }()
	if err := bare.RemoveWorktreeDiscardingChanges(ctx, entry.CopyPath); err != nil {
		return Loss{}, fmt.Errorf("tasks: discarding the copy at %s: %w", entry.CopyPath, err)
	}
	return loss, nil
}
