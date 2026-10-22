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
// Loss is what the copy holds that exists nowhere its removal leaves
// intact, as far as this program can see, read from the copy as it stands
// when it is shown. Only what the removal destroys counts: a task's clone
// is removed whole, while a run's worktree is removed from a gate that
// stands after it, with every reference the worktree shares with the gate
// and every reflog those keep; the stash it shares is the gate's too, but
// the entries the worktree made in it are its work, which nothing will
// name once it is gone, so those count (ownStash). So it is the commits
// that anything the removal destroys names reaches, HEAD, a reference, a
// root reference such as ORIG_HEAD, or a reflog entry by the value it moved
// to or the one it moved from (reflogValues), a commit reset away and a
// stash among them, that nothing held reaches, with every object such a
// name or value records that is not a commit and is not itself held
// (lostFrom), all decided by git from the copy's own objects. Held is the
// default branch, read fresh as Keep reads it; every object the copy names
// that the upstream advertises now, compared by identifier, a tag cloned
// with the copy and a branch the upstream also has among them; and every
// remote-tracking reference the copy holds, which records a remote's own
// branches. And every
// path the removal destroys whose content or deletion nothing else holds, a
// file git ignores and a deletion git has not staged among them, decided
// from what the copy holds on lostPaths' terms (kept_lost.go); and the
// three indicators vcs.Inventory reads as git reports them, a tracked
// change, a file no commit records that git does not ignore, and for a
// task's clone a stash. The entry's recorded basis is not what is shown,
// because the copy may hold more since it was kept and a discard destroys
// the copy as it is. A default branch that cannot be read now refuses the
// preview with ErrLossUnestablished rather than showing a list that may
// overstate or understate. A copy that is gone is ErrCopyGone.
//
// The digest seals exactly what Loss shows and nothing else (seal): HEAD;
// the commits lost; every object lost by identifier, with its type and the
// names shown for it; every path lost by kind and path with what it is lost
// holding (lostPaths: a file's bytes by their hash or a link's target with
// the mode git records, a directory lost whole by every entry under it, a
// staged path by the index entries it stages, a deletion by HEAD's entry);
// and the three indicators. Each record is framed (digest), so no name,
// target or content can stand for a boundary. What is shown and what is
// sealed are one set by construction: a change to anything shown moves
// the digest, which lets Discard refuse a copy that no longer holds exactly
// what was shown (ErrLossChanged), and a change to nothing shown leaves it
// as it was, since the removal destroys nothing more for it: a branch made
// at a commit already shown or held upstream, references packed, the
// object database repacked, a file rewritten with the bytes it held, or an
// edit staged whose content the working tree holds as shown. Every path is
// decided from what the copy holds, never from what git reports changed,
// so neither git's ignore rules nor its configuration can keep one off the
// list or out of the digest. It is a confirmation and not a secret.
//
// The default branch's tip is not in it: it is not the copy's, and a
// discard answered and stopped before its removal would otherwise be
// refused for good by an unrelated commit landing upstream. Discard holds
// the commits lost to the ones shown instead: none lost now that was not
// shown, and none shown that the copy no longer holds, unless the default
// branch has taken it up.
//
// What it does not cover, and what that costs. An object nothing names, no
// reference, reflog entry or index entry, is not shown: one written since
// the discard was shown leaves the digest as it was, and a stash dropped
// before it and not yet pruned, which only git fsck finds, is destroyed
// with the copy and named by neither the digest nor the list. What git
// keeps outside its references names commits the list does not walk from:
// FETCH_HEAD and MERGE_HEAD, which git lists as no reference, and the state
// of an operation in progress (a rebase's or a sequencer's directory); each
// is shown as a repository path, its file and not the commits it names. A
// tree or a blob a name or a reflog value records is listed whatever else
// holds it unless the upstream advertises it, and so is an annotated tag
// the upstream does not advertise, the person's checkout's own tags cloned
// with the copy among them, which fails toward showing; one git's walk
// of the reflogs leaves out because a commit or tree it lists holds it is
// held by that commit or tree, which is listed or which the default branch
// holds (vcs.ReflogValues). Every value is passed to
// git on its command line, so a copy whose reflogs record more values than
// the system lets a command line carry refuses the preview as unestablished
// rather than showing less. And git's own
// maintenance, which a fetch may start, can expire the one reflog entry
// that keeps a commit shown in the copy, which refuses the confirmation the
// same way: each of these fails toward keeping. What is held is taken as
// held without asking more of it: a remote-tracking reference is the
// copy's record of the remote at its last fetch, so a branch the remote
// has since deleted or moved, or a reference written there by hand, holds
// what it names though nothing outside the copy may; and a commit shown
// as lost that the upstream took up afterwards through a name the copy no
// longer holds reads as gone, which refuses the confirmation. A stash entry
// records no worktree, so a run's own entries are the ones made on its
// HEAD or on a commit its own reflogs record: one another worktree made on
// the same commit is shown as this one's, and one this worktree made on a
// commit its reflogs no longer record is not shown, nor destroyed, since
// the gate's stash stands.
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
	// removal destroys names reaches and nothing held reaches (lostFrom),
	// read from the copy as it stands, never the entry's record of it when
	// it was kept: for a
	// task's clone HEAD, every reference, every worktree's HEAD, its root
	// references and every reflog entry by both its values, the stash list
	// whole among them; for a run's worktree HEAD, its own references and
	// root references, and the values of the reflogs git reads for it alone
	// less the ones the gate's own reflogs record, which stand after the
	// removal, and the stash entries it made (ownStash), never another
	// worktree's. It is wider than what Keep records, which leaves the
	// stash and the reflogs out.
	Lacks []string
	// Dirty, Untracked and Stash are the inventory's indicators of the copy
	// as it stands now, as git reports them: a tracked change, a file no
	// commit records that git does not ignore, and a stash, none of which a
	// commit the default branch could lack records. For a run's worktree,
	// whose stash is the gate's, Stash is whether it made an entry of it
	// (ownStash). Files names every path they speak of, and the ignored
	// ones they do not.
	Dirty, Untracked, Stash bool
	// Files is every path the removal destroys whose content or deletion
	// nothing else holds, decided from what the copy holds (lostPaths), in
	// Kind's order and by path within it. What each holds is not shown and
	// is sealed (files).
	Files []LostPath
	// Objects is every object a name the removal destroys points at that
	// is not a commit, an annotated tag, a tree or a blob, with the names
	// that point at it, in the order they are named (lostFrom).
	Objects []LostObject
	// Digest is the confirmation of exactly this, everything above as
	// shown, each path by what it is lost holding (seal).
	Digest string
	// files is Files with what each path is lost holding, which the digest
	// seals.
	files []lostFile
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
// reflog entry as ref@{n}. One the upstream advertises, or a
// remote-tracking reference names, is held and not listed; any other is
// listed whatever else holds it, failing toward showing.
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
	// deletes, not among those shown as what they hold
	// (gitDirShownAsObjects), whose content differs from what git writes
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
	loss, _, err := c.loss(ctx, entry, nil)
	return loss, err
}

// loss is Loss, and of shown, the commits a discard was shown as lost, the
// ones the copy no longer holds that the default branch does not hold
// either (lostFrom's gone).
func (c *Cleaner) loss(ctx context.Context, entry store.KeptWork, shown []string) (Loss, []string, error) {
	standing, err := c.standing(entry.CopyPath)
	if err != nil {
		return Loss{}, nil, err
	}
	if !standing {
		return Loss{}, nil, fmt.Errorf("%w: nothing stands at %s", ErrCopyGone, entry.CopyPath)
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return Loss{}, nil, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return Loss{}, nil, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	// What is lost is read from the copy now, from every name the removal
	// destroys, so what is shown is what the copy holds when it is shown
	// and the digest seals exactly that, never the entry's record of the
	// copy when it was kept.
	refs, logs, err := copyNames(ctx, entry, copied)
	if err != nil {
		return Loss{}, nil, err
	}
	var l landing
	if taskID, ok := entry.TaskID.Get(); ok {
		task, err := c.store.Task(ctx, taskID)
		if err != nil {
			return Loss{}, nil, err
		}
		if l, err = c.taskLanding(ctx, task, copied, nil); err != nil {
			return Loss{}, nil, err
		}
	} else {
		run, err := c.store.Run(ctx, entry.RunID.Or(""))
		if err != nil {
			return Loss{}, nil, err
		}
		if l, err = c.runLanding(ctx, run, copied, nil); err != nil {
			return Loss{}, nil, err
		}
	}
	// The default branch is fetched under a namespace of this read's own,
	// never the cleanup decision's: a cleanup of the same copy may run at
	// once (recovery asks it of every finished run's standing copy), and
	// its forget would delete the reference this read is about to resolve.
	if l.namespace, err = discardNamespace(entry); err != nil {
		return Loss{}, nil, err
	}
	named := namesOf(inv.Head.Commit, refs, logs)
	values, err := reflogValues(ctx, entry, copied)
	if err != nil {
		return Loss{}, nil, err
	}
	// A task's clone's stash is its own and goes with it, as its reflog
	// values and its indicator. A run's worktree shares the gate's, which
	// stands after the removal, so only the entries the worktree made
	// count: those are its work, and nothing else will ever name them.
	stash := inv.Stash
	if !entry.TaskID.IsKnown() {
		own, err := ownStash(ctx, entry, copied, inv.Head.Commit, values)
		if err != nil {
			return Loss{}, nil, err
		}
		for _, e := range own {
			named.add(e.Object, e.Ref+"@{"+strconv.Itoa(e.Index)+"}")
		}
		stash = len(own) > 0
	}
	var remotes []string
	for _, ref := range refs {
		if strings.HasPrefix(ref.Name, "refs/remotes/") {
			remotes = append(remotes, ref.Object)
		}
	}
	// Besides every name read, every value a reflog the removal destroys
	// records, and for a task's clone, removed whole, every reference and
	// every worktree's HEAD as git walks them.
	objects := append(slices.Clone(named.objects), values...)
	reach := vcs.Reach{Objects: objects, Refs: entry.TaskID.IsKnown()}
	held, others, gone := lostFrom(ctx, l, reach, objects, remotes, shown)
	if !held.established() {
		return Loss{}, nil, fmt.Errorf("%w: %s", ErrLossUnestablished, held.unestablished)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: held.lacks, Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: stash}
	for _, object := range others {
		kind, err := copied.ObjectType(ctx, object)
		if err != nil {
			return Loss{}, nil, fmt.Errorf("tasks: reading what %s is in the kept copy at %s: %w", object, entry.CopyPath, err)
		}
		loss.Objects = append(loss.Objects, LostObject{Object: object, Type: kind, Names: named.names[object]})
	}
	if loss.files, err = copyFiles(ctx, entry, copied, inv); err != nil {
		return Loss{}, nil, err
	}
	loss.Files = make([]LostPath, len(loss.files))
	for i, f := range loss.files {
		loss.Files[i] = f.LostPath
	}
	loss.Digest = seal(loss, loss.Lacks)
	return loss, gone, nil
}

// copyNames is every name the removal destroys that git lists, read from
// the copy: for a task's clone, removed whole, every reference in it, the
// program's own included, its root references (vcs.RootRefs: ORIG_HEAD and
// the others an operation writes, HEAD aside, which Loss reads from the
// inventory), and every reflog it keeps; for a run's worktree, its own
// references (runWorktreeRefs), its own root references, and the reflogs of
// HEAD and of its references. A reflog entry's previous value is not among
// them, since no read here names it; reflogValues reads it as a value.
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
	n.add(head, "HEAD")
	for _, ref := range refs {
		n.add(ref.Object, ref.Name)
	}
	for _, l := range logs {
		n.add(l.Object, l.Ref+"@{"+strconv.Itoa(l.Index)+"}")
	}
	return n
}

// add names object, once among the objects, with name among its names.
func (n *names) add(object, name string) {
	if _, ok := n.names[object]; !ok {
		n.objects = append(n.objects, object)
	}
	n.names[object] = append(n.names[object], name)
}

// ownStash is, for a run's worktree, the entries of the stash it shares
// with the gate that it made: each whose base, the commit the stash was
// made on (its first parent), is the worktree's HEAD or a value its own
// reflogs record (mine, reflogValues' for a run). git records no worktree
// on a stash entry, so this is the closest the stash says: an entry another
// worktree made on the same commit is taken for this one's, which fails
// toward showing, and one this worktree made on a commit its reflogs no
// longer record is not.
func ownStash(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, head string, mine []string) ([]vcs.ReflogEntry, error) {
	fail := func(err error) ([]vcs.ReflogEntry, error) {
		return nil, fmt.Errorf("tasks: reading the stash the kept copy at %s shares: %w", entry.CopyPath, err)
	}
	standing, err := copied.ListRefs(ctx, "refs/stash")
	if err != nil {
		return fail(err)
	}
	if !slices.ContainsFunc(standing, func(ref vcs.Ref) bool { return ref.Name == "refs/stash" }) {
		return nil, nil
	}
	entries, err := copied.Reflogs(ctx, "refs/stash")
	if err != nil {
		return fail(err)
	}
	var own []vcs.ReflogEntry
	for _, e := range entries {
		base, err := copied.ResolveCommit(ctx, e.Object+"^1")
		if err != nil {
			return fail(err)
		}
		if base == head || slices.Contains(mine, base) {
			own = append(own, e)
		}
	}
	return own, nil
}

// gitDirShownAsObjects is the entries of a repository directory that
// copyFiles does not walk, because git rewrites them without changing what
// they hold, and Loss's own read writes some of them: the object database,
// which the read's fetch adds to and git's maintenance repacks; the
// references and their logs, in whichever form the repository keeps them,
// which that fetch writes a reference under for the read's length and
// maintenance may pack; and the index, whose recorded file times a
// person's own git status refreshes. What they hold is shown instead: the
// commits and objects their names reach (lostFrom) and the index's content
// as the paths it stages (lostPaths). What the object database holds that
// none of those name is the residual gap the header states.
var gitDirShownAsObjects = []string{"objects", "refs", "packed-refs", "logs", "reftable", "index"}

// runWorktreeRefs is the references a run's worktree holds of its own,
// which the discard's removal of the worktree takes with it. Everything
// else a run's worktree reads is the gate's, shared with every other run
// and left standing by the removal, the stash among it, whose entries the
// worktree made are read on their own (ownStash).
var runWorktreeRefs = []string{"refs/worktree/", "refs/bisect/", "refs/rewritten/"}

// reflogValues is every value a reflog the removal destroys records, by
// either value and of any type (vcs.ReflogValues): for a task's clone,
// removed whole, every reflog it keeps, every worktree's HEAD's among
// them; for a run's worktree, the ones git reads for it alone, less every
// value the gate's own reads record, since the logs of the references the
// worktree shares with the gate stand after the removal and so does what
// they record.
func reflogValues(ctx context.Context, entry store.KeptWork, copied *vcs.Repository) ([]string, error) {
	fail := func(err error) ([]string, error) {
		return nil, fmt.Errorf("tasks: reading what the reflogs of the kept copy at %s record: %w", entry.CopyPath, err)
	}
	if entry.TaskID.IsKnown() {
		values, err := copied.ReflogValues(ctx, false)
		if err != nil {
			return fail(err)
		}
		return values, nil
	}
	mine, err := copied.ReflogValues(ctx, true)
	if err != nil {
		return fail(err)
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return fail(err)
	}
	gate, err := vcs.OpenBare(ctx, obs.CommonDir, vcs.WithRedactor(redact.New()))
	if err != nil {
		return fail(err)
	}
	shared, err := gate.ReflogValues(ctx, true)
	if err != nil {
		return fail(err)
	}
	return slices.DeleteFunc(mine, func(v string) bool { return slices.Contains(shared, v) }), nil
}

// copyFiles is every path the removal destroys whose content or deletion
// nothing else holds, with what each is lost holding, on lostPaths' terms,
// decided from what the copy holds and never from what git reports
// changed: the working tree is walked by the filesystem whatever git
// ignores or reports, then the repository directory the removal deletes
// with it, a task's clone's .git or the directory the gate keeps for a
// run's worktree, but for what git rewrites without changing what it holds
// (gitDirShownAsObjects), and git is asked for HEAD's tree and the index.
func copyFiles(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, inv vcs.Inventory) ([]lostFile, error) {
	index, err := copied.IndexEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return nil, fmt.Errorf("tasks: finding the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	gitDir, err := os.Stat(obs.GitDir)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	lost, err := newLostPaths(ctx, copied, inv.Head.Commit, index)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading what the kept copy at %s holds against HEAD: %w", entry.CopyPath, err)
	}
	// The working tree, everything under the copy but the repository
	// directory standing in it, which is walked on its own terms below;
	// a nested repository's .git, a submodule's among them, is walked
	// here as files like any other.
	err = walkTree(entry.CopyPath, func(rel string, info os.FileInfo) bool {
		return rel == ".git" && info.IsDir() && os.SameFile(info, gitDir)
	}, lost.worktree(entry.CopyPath))
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	shownAsObjects := func(rel string, _ os.FileInfo) bool { return slices.Contains(gitDirShownAsObjects, rel) }
	fresh, err := freshRepository(ctx, shownAsObjects)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading what git writes into a new repository: %w", err)
	}
	if err := walkTree(obs.GitDir, shownAsObjects, lost.repository(fresh)); err != nil {
		return nil, fmt.Errorf("tasks: reading the repository directory of the kept copy at %s: %w", entry.CopyPath, err)
	}
	if err := lost.stored(ctx); err != nil {
		return nil, fmt.Errorf("tasks: reading what git would store for the files of the kept copy at %s: %w", entry.CopyPath, err)
	}
	return lost.list(), nil
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

// seal is the digest a discard is shown and confirmed with: everything
// loss shows and nothing else, with lacks for the commits shown as lost
// (Loss gives its own, and Discard the ones the confirmation was shown, so
// a default branch that took some up since is no change). HEAD; the
// commits lost; every object lost by identifier with its type and names;
// every path lost by kind and path with what it is lost holding
// (lostFile); and the three indicators. Each set is sealed in an order of
// its own, sorted, so a set read in another order seals the same. The
// default branch's tip is not sealed: it is not the copy's, so a commit
// landing on it that the copy does not hold changes nothing a discard
// destroys, and sealing it would refuse a recorded discard for good
// whenever the upstream moved before the removal. What it can change is
// what of the copy is lost, which Discard holds to what was shown instead.
func seal(loss Loss, lacks []string) string {
	d := newDigest()
	d.record("head", loss.Head)
	d.record(append([]string{"lacks"}, slices.Sorted(slices.Values(lacks))...)...)
	objects := slices.Clone(loss.Objects)
	slices.SortFunc(objects, func(a, b LostObject) int { return strings.Compare(a.Object, b.Object) })
	for _, o := range objects {
		d.record(append([]string{"object", o.Object, o.Type}, slices.Sorted(slices.Values(o.Names))...)...)
	}
	for _, f := range loss.files {
		d.record("path", string(f.Kind), f.Path, f.held)
	}
	d.record("indicators", strconv.FormatBool(loss.Dirty), strconv.FormatBool(loss.Untracked), strconv.FormatBool(loss.Stash))
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

// walkTree gives visit every entry under root, root itself aside, walked
// by the filesystem in lexical order and never through a link, with its
// path relative to root and what it holds: a link's target, a regular
// file's bytes by their hash, and nothing for anything else, a directory,
// a pipe or a socket. An entry skip answers true for, by its path relative
// to root, is not given, and nothing under it is walked.
func walkTree(root string, skip func(rel string, info os.FileInfo) bool, visit treeVisit) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
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
		if skip(rel, info) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		held := ""
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if held, err = os.Readlink(p); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if held, err = hashFile(p); err != nil {
				return err
			}
		}
		return visit(rel, info, held)
	})
}

// treeVisit is what walkTree gives each entry: its path relative to the
// root, what Lstat read of it, and what it holds, a link's target or a
// file's hash, empty for anything else.
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
// the commits the discard was shown as lost, which digest seals: the copy
// read now must seal with them to digest, so everything shown but the
// commits is as it was; every commit lost now must be among them, so one
// that would be lost now and was not shown is refused; and every one of
// them must be lost now or held by the default branch, so one the copy no
// longer holds is refused. Each refusal is ErrLossChanged. gate is the gate
// repository a run's copy is a worktree of, and is not read for a task's. A
// copy already gone returns the zero Loss and no error.
//
// What the commit check does not cover: a default branch that moved only
// forward over commits the copy does not hold changes nothing here, and one
// that took up commits the copy holds shrinks the loss, both of which
// proceed; one that was rewound or force-pushed past a commit it had can
// make the copy the only place that commit is, which is refused.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, digest string, shown []string, gatePath string) (Loss, error) {
	loss, gone, err := c.loss(ctx, entry, shown)
	if errors.Is(err, ErrCopyGone) {
		return Loss{}, nil
	}
	if err != nil {
		return Loss{}, err
	}
	if sealed := seal(loss, shown); sealed != digest {
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
	if len(gone) > 0 {
		return Loss{}, fmt.Errorf("%w: the copy no longer holds %s, which the discard was shown as lost and the default branch does not hold",
			ErrLossChanged, strings.Join(gone, ", "))
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
