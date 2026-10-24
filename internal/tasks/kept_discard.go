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
// (lostFrom), all decided by git from the copy's own objects as they record
// them, never through a replacement reference or a graft file, which every
// repository the Cleaner opens is read without (vcs.WithoutReplaceObjects),
// so neither can hide a commit from the walk. Held is the
// default branch, read fresh as Keep reads it; and what the upstream holds
// now (upstreamHeld): every object the copy names that the upstream
// advertises now, compared by identifier, a tag cloned with the copy and a
// branch the upstream also has among them, and everything the commit the
// upstream's branch of the same name as one of the copy's branches or
// remote-tracking references stands on now reaches, fetched by identifier
// when the copy does not hold it. A remote-tracking reference holds nothing
// of itself: it is the copy's record of a remote at its last fetch, so what
// it names that the upstream no longer reaches, a branch deleted or rewound
// there since, is lost with the copy. And every
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
// same way: each of these fails toward keeping. The upstream is the one
// repository read as holding anything beyond the default branch: a commit
// the person's checkout or the gate holds and the upstream does not is
// shown as lost though the removal leaves it there, which fails toward
// showing. What the upstream holds is read as it advertises it now,
// through what the copy names: a commit the upstream holds only under a
// reference whose object the copy does not name and whose name no branch
// or remote-tracking reference of the copy's ends in is shown as lost,
// which fails toward showing too. A commit shown as lost that the
// default branch or the upstream takes up afterwards is not lost then,
// which refuses the confirmation, since Discard holds the commits lost to
// those shown exactly. A stash entry
// records no worktree, so a run's own entries are the ones made on a
// commit its own names or either value of an entry of its own reflogs
// record (ownStash), a commit the gate's reflogs record too among them:
// one another worktree made on such a commit is shown as this one's, and
// one this worktree made on a commit none of them records any more is not
// shown, nor destroyed, since the gate's stash stands.
//
// # The removal
//
// Discard deletes what was shown and confirmed and nothing else, and
// removes the copy only once that leaves it empty of work (the owner's
// decision of 2026-10-24, which replaced removing the copy as it was read):
// it sets the confirmed items aside recoverably beside the copy, reads the
// loss again, and removes the directory only when that read holds nothing
// but what this file already showed of the repository directory, by a
// removal that refuses when anything is left, a run's worktree by git's
// own removal without force and a task's clone on P12's proof; any
// refusal puts everything back. kept_aside.go owns how and what it does not
// cover. Which record is a run's copy's is read from the gate's own
// registry and never by following the copy's .git, and a copy whose files
// disagree with it is refused before anything is read (copyRecord). What
// stands behind the deletion is the digest match against the person's
// answer; a task's clone is removed on P12's proof as well, so one whose
// remaining commits no proof covers is refused and stays. A copy already
// gone is not refused: the answer and the digest it carried say the
// removal was wanted, and nothing stands to lose.

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
	// root references, every entry of their reflogs by both its values
	// (reflogValues), and the stash entries it made (ownStash), never
	// another worktree's, and never the gate's shared references or their
	// reflogs, which the removal leaves. Held is the default branch and
	// what the upstream holds now (upstreamHeld) and nothing else: a
	// remote-tracking reference holds nothing of itself, and neither do the
	// gate's references, so a commit the worktree's own names reach is
	// listed whatever the gate records. It is wider than what Keep records,
	// which leaves the stash and the reflogs out.
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
	// against is the commit the default branch stood on when this was
	// read, which a discard moves a lost HEAD onto.
	against string
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
// reflog entry as ref@{n}. One the upstream advertises now is held and not
// listed; any other is listed whatever else holds it, a remote-tracking
// reference's among them, failing toward showing.
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

// ErrLayoutUnsupported is returned by Loss, Discard and PutBack for a copy
// whose repository is laid out in a way they do not support, naming the
// layout, before anything of it is read or changed.
var ErrLayoutUnsupported = errors.New("tasks: the copy's repository is laid out in a way a discard does not support, so nothing of it was read or changed")

// ErrRecordNotTheCopys is returned by Discard and PutBack for a run's copy
// that the gate's own registry does not tie to exactly one record, or
// whose own files disagree with that record (copyRecord), before anything
// of it is read or changed.
var ErrRecordNotTheCopys = errors.New("tasks: the worktree's record is not this copy's in the gate, so nothing of it was read or removed")

// ErrLossChanged is returned by Discard when the copy no longer holds
// exactly what was shown, or still holds something once what was shown is
// set aside, so nothing of it was deleted: what was set aside was put
// back, or, where it could not be, the error is ErrAsideStands too.
var ErrLossChanged = errors.New("tasks: the copy no longer holds exactly what was shown, so it was not removed")

// Loss reads what discarding the entry's copy would lose, on this file's
// terms.
func (c *Cleaner) Loss(ctx context.Context, entry store.KeptWork) (Loss, error) {
	if _, err := os.Lstat(asideDir(entry.CopyPath)); err == nil {
		return Loss{}, fmt.Errorf("%w: it is at %s, and the next discard answer of the entry puts it back", ErrAsideStands, asideDir(entry.CopyPath))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Loss{}, err
	}
	return c.loss(ctx, entry)
}

// loss is Loss without the check for what a discard set aside, which
// Discard makes itself.
func (c *Cleaner) loss(ctx context.Context, entry store.KeptWork) (Loss, error) {
	standing, err := c.standing(entry.CopyPath)
	if err != nil {
		return Loss{}, err
	}
	if !standing {
		return Loss{}, fmt.Errorf("%w: nothing stands at %s", ErrCopyGone, entry.CopyPath)
	}
	if err := c.supportedLayout(ctx, entry); err != nil {
		return Loss{}, err
	}
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, c.git...)
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
	values, err := reflogValues(ctx, entry, copied, refs)
	if err != nil {
		return Loss{}, err
	}
	// A task's clone's stash is its own and goes with it, as its reflog
	// values and its indicator. A run's worktree shares the gate's, which
	// stands after the removal, so only the entries the worktree made
	// count: those are its work, and nothing else will ever name them.
	stash := inv.Stash
	if !entry.TaskID.IsKnown() {
		own, err := ownStash(ctx, entry, copied, append(slices.Clone(named.objects), values...))
		if err != nil {
			return Loss{}, err
		}
		for _, e := range own {
			named.add(e.Object, e.Ref+"@{"+strconv.Itoa(e.Index)+"}")
		}
		stash = len(own) > 0
	}
	// Besides every name read, every value a reflog the removal destroys
	// records, and for a task's clone, removed whole, every reference and
	// every worktree's HEAD as git walks them.
	objects := append(slices.Clone(named.objects), values...)
	reach := vcs.Reach{Objects: objects, Refs: entry.TaskID.IsKnown()}
	held, others := lostFrom(ctx, l, reach, objects, refs)
	if !held.established() {
		return Loss{}, fmt.Errorf("%w: %s", ErrLossUnestablished, held.unestablished)
	}
	loss := Loss{Head: inv.Head.Commit, Lacks: held.lacks, Dirty: inv.Dirty, Untracked: inv.Untracked, Stash: stash, against: held.against}
	for _, object := range others {
		kind, err := copied.ObjectType(ctx, object)
		if err != nil {
			return Loss{}, fmt.Errorf("tasks: reading what %s is in the kept copy at %s: %w", object, entry.CopyPath, err)
		}
		loss.Objects = append(loss.Objects, LostObject{Object: object, Type: kind, Names: named.names[object]})
	}
	if loss.files, err = c.copyFiles(ctx, entry, copied, inv); err != nil {
		return Loss{}, err
	}
	loss.Files = make([]LostPath, len(loss.files))
	for i, f := range loss.files {
		loss.Files[i] = f.LostPath
	}
	loss.Digest = seal(loss)
	return loss, nil
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
// made on (its first parent), is one of what the worktree's own names
// record. That is mine: its HEAD, its own references and root references
// and the value each entry of their reflogs moved to (namesOf over
// copyNames' read for a run), and every value, previous or new, an entry
// of the reflogs of its HEAD and its own references records
// (reflogValues), so a base an expiry left only as the value the oldest
// standing entry moved from, or the one an entry after a gap moved from,
// is still its own. Those are the worktree's alone, so a base the
// gate's shared references or their reflogs record too is still its own
// when one of them records it, and a base only the shared ones record is
// not. git records no worktree on a stash entry, so this is the closest
// the stash says: an entry another worktree made on a commit this one's
// names record is taken for this one's, which fails toward showing, and
// one this worktree made on a commit no entry of its own logs records any
// more, expired from them whole, is not. A repository whose references
// are not kept as files refuses the read (vcs.ErrRefStorageUnsupported).
func ownStash(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, mine []string) ([]vcs.ReflogEntry, error) {
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
		if slices.Contains(mine, base) {
			own = append(own, e)
		}
	}
	return own, nil
}

// gitDirShownAsObjects is the entries of a repository directory that
// copyFiles does not walk, because git rewrites them without changing what
// they hold, and Loss's own read writes some of them: the object database,
// which the read's fetch adds to and git's maintenance repacks; the
// references and their logs, in files, the one form supportedLayout lets
// through, which that fetch writes a reference under for the read's length and
// maintenance may pack; and the index, whose recorded file times a
// person's own git status refreshes. What they hold is shown instead: the
// commits and objects their names reach (lostFrom) and the index's content
// as the paths it stages (lostPaths). What the object database holds that
// none of those name is the residual gap the header states.
var gitDirShownAsObjects = []string{"objects", "refs", "packed-refs", "logs", "index"}

// runWorktreeRefs is the references a run's worktree holds of its own,
// which the discard's removal of the worktree takes with it. Everything
// else a run's worktree reads is the gate's, shared with every other run
// and left standing by the removal, the stash among it, whose entries the
// worktree made are read on their own (ownStash).
var runWorktreeRefs = []string{"refs/worktree/", "refs/bisect/", "refs/rewritten/"}

// reflogValues is every value a reflog the removal destroys records, by
// either value and of any type: for a task's clone, removed whole, every
// reflog it keeps, HEAD's among them (vcs.ReflogValues), a clone having no
// linked worktree (supportedLayout);
// for a run's worktree, the logs of its own HEAD and of its own references
// among refs, copyNames' read, and no other (vcs.ReflogValuesOf). The logs
// of the references it shares with the gate stand after the removal and
// are not read, so a value only they record is not counted; and they hold
// nothing either, so a value the worktree's own logs record counts
// whatever they record too, by either of its values alike.
func reflogValues(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, refs []vcs.Ref) ([]string, error) {
	var values []string
	var err error
	if entry.TaskID.IsKnown() {
		values, err = copied.ReflogValues(ctx)
	} else {
		values, err = copied.ReflogValuesOf(ctx, ownLogs(refs)...)
	}
	if err != nil {
		return nil, fmt.Errorf("tasks: reading what the reflogs of the kept copy at %s record: %w", entry.CopyPath, err)
	}
	return values, nil
}

// ownLogs is the reflogs a run's worktree keeps of its own: its HEAD's, and
// those of its own references among refs, copyNames' read for it, whose
// root references keep none.
func ownLogs(refs []vcs.Ref) []string {
	logs := []string{"HEAD"}
	for _, ref := range refs {
		if strings.HasPrefix(ref.Name, "refs/") {
			logs = append(logs, ref.Name)
		}
	}
	return logs
}

// copyFiles is every path the removal destroys whose content or deletion
// nothing else holds, with what each is lost holding, on lostPaths' terms,
// decided from what the copy holds and never from what git reports
// changed: the working tree is walked by the filesystem whatever git
// ignores or reports, then the repository directory the removal deletes
// with it, a task's clone's .git or the directory the gate keeps for a
// run's worktree, but for what git rewrites without changing what it holds
// (gitDirShownAsObjects), and git is asked for HEAD's tree and the index.
func (c *Cleaner) copyFiles(ctx context.Context, entry store.KeptWork, copied *vcs.Repository, inv vcs.Inventory) ([]lostFile, error) {
	index, err := copied.IndexEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the index of the kept copy at %s: %w", entry.CopyPath, err)
	}
	obs, err := vcs.Observe(ctx, entry.CopyPath, c.git...)
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
	fresh, err := freshRepository(ctx, shownAsObjects, c.git...)
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
// loss shows and nothing else. HEAD; the commits lost; every object lost by identifier with its type and names;
// every path lost by kind and path with what it is lost holding
// (lostFile); and the three indicators. Each set is sealed in an order of
// its own, sorted, so a set read in another order seals the same. The
// default branch's tip is not sealed: it is not the copy's, so a commit
// landing on it that the copy does not hold changes nothing a discard
// destroys, and sealing it would refuse a recorded discard for good
// whenever the upstream moved before the removal. What it can change is
// what of the copy is lost, which the digest seals.
func seal(loss Loss) string {
	d := newDigest()
	d.record("head", loss.Head)
	d.record(append([]string{"lacks"}, slices.Sorted(slices.Values(loss.Lacks))...)...)
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

// Discard deletes what digest was shown for and confirmed, on
// kept_aside.go's terms, and removes the entry's copy only once that leaves
// it empty of work, and returns the loss it checked the digest on. shown is
// the commits the discard was shown as lost, which digest seals: the copy
// read now must lose exactly those commits, none more and none fewer, and
// must seal to digest, so everything shown is as it was. gate is the gate
// repository a run's copy is a worktree of; a run's copy is held to the
// record the gate's registry keeps for it (copyRecord) before anything
// else is read.
//
// Every refusal leaves the copy as it was and is ErrLossChanged, naming
// what differs: commits lost now that were not shown or shown that are not
// lost now; a digest that no longer matches; a path shown as lost that is
// gone or holds something else when it is set aside; an index or a
// reflog changed since the loss was read; a path written where the
// discard writes one; a reference that moved; HEAD that moved; anything
// the copy would still lose once what was shown is set aside that the
// loss did not show (beyondShown); and a removal git or P12's proof
// refuses. What was set aside is put back first, and what could not be is
// ErrAsideStands as well, naming where it is. beforeRemoval, when not nil, runs once the digest is
// checked and before anything is set aside, and an error it returns is
// returned with nothing touched. A copy already gone returns the zero Loss
// and no error, and runs nothing but the removal of what a discard that
// stopped once it had removed the copy left set aside, all of it confirmed.
//
// The Loss returned is the one read to check the digest, before anything
// was set aside, so its commits are the ones shown and its Digest is
// digest.
//
// What the commit check does not cover: a default branch that moved only
// forward over commits the copy does not hold changes nothing here and
// proceeds; one that took up a commit shown, or was rewound past one it
// had, changes the commits lost, which is refused.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, digest string, shown []string, gatePath string, beforeRemoval func() error) (Loss, error) {
	if _, err := os.Lstat(entry.CopyPath); errors.Is(err, os.ErrNotExist) {
		// A discard that stopped once the copy was removed leaves what it
		// set aside, every item of it confirmed, which goes now.
		if err := os.RemoveAll(asideDir(entry.CopyPath)); err != nil {
			return Loss{}, fmt.Errorf("tasks: the copy at %s is gone, and what a discard set aside beside it could not be removed: %w", entry.CopyPath, err)
		}
		return Loss{}, nil
	} else if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the copy at %s: %w", entry.CopyPath, err)
	}
	bare, copied, err := c.openForDiscard(ctx, entry, gatePath)
	if err != nil {
		return Loss{}, err
	}
	// What a discard that did not finish set aside is put back before
	// anything is read.
	dir := asideDir(entry.CopyPath)
	if err := c.putBack(ctx, entry, copied); err != nil {
		return Loss{}, err
	}
	before, err := readAsideBefore(ctx, entry, copied)
	if err != nil {
		return Loss{}, fmt.Errorf("tasks: reading the copy at %s: %w", entry.CopyPath, err)
	}
	loss, err := c.loss(ctx, entry)
	if errors.Is(err, ErrCopyGone) {
		return Loss{}, nil
	}
	if err != nil {
		return Loss{}, err
	}
	if unshown, held := setDifference(loss.Lacks, shown), setDifference(shown, loss.Lacks); len(unshown) > 0 || len(held) > 0 {
		return Loss{}, fmt.Errorf("%w: the commits lost are not the ones the discard was shown as lost: %s",
			ErrLossChanged, describeCommits(unshown, held))
	}
	if loss.Digest != digest {
		return Loss{}, fmt.Errorf("%w: it was shown as %s and holds what digests as %s now: %s", ErrLossChanged, digest, loss.Digest, describeLoss(loss))
	}
	if beforeRemoval != nil {
		if err := beforeRemoval(); err != nil {
			return Loss{}, err
		}
	}
	if c.betweenDiscardReadAndCheck != nil {
		c.betweenDiscardReadAndCheck()
	}
	if err := c.setAside(ctx, entry, copied, loss, before, dir); err != nil {
		return Loss{}, err
	}
	refuse := func(why error) (Loss, error) {
		if err := c.restoreAside(ctx, entry, copied, dir); err != nil {
			return Loss{}, fmt.Errorf("%w; %w", why, err)
		}
		return Loss{}, why
	}
	if c.betweenAsideAndCheck != nil {
		if err := c.betweenAsideAndCheck(); err != nil {
			return Loss{}, err
		}
	}
	after, err := c.loss(ctx, entry)
	if err != nil {
		return refuse(fmt.Errorf("%w: the copy could not be read once what was shown was set aside: %w", ErrLossChanged, err))
	}
	if err := beyondShown(loss, after); err != nil {
		return refuse(err)
	}
	if entry.TaskID.IsKnown() {
		if err := c.removeOnProof(ctx, entry.TaskID.Or(""), copied); err != nil {
			return refuse(err)
		}
	} else {
		lock, err := gate.LockCopies(ctx, bare.Path())
		if err != nil {
			return refuse(fmt.Errorf("tasks: waiting to remove the worktree at %s: %w", entry.CopyPath, err))
		}
		err = bare.RemoveWorktree(ctx, entry.CopyPath)
		_ = lock.Release()
		if err != nil {
			return refuse(fmt.Errorf("%w: git refused to remove the worktree at %s, so something is left in it: %w", ErrLossChanged, entry.CopyPath, err))
		}
	}
	// The copy is gone, so what was set aside, every item of it what the
	// loss showed and the person confirmed, goes too.
	if err := os.RemoveAll(dir); err != nil {
		return loss, fmt.Errorf("tasks: the copy at %s was discarded, and what was set aside at %s could not be removed: %w", entry.CopyPath, dir, err)
	}
	return loss, nil
}

// openForDiscard opens what a discard of the entry's copy works on: for a
// run's copy the gate repository at gatePath, with the copy held to the
// record the gate's registry keeps for it (copyRecord), and the copy, held
// to the one layout a discard supports (supportedLayout).
func (c *Cleaner) openForDiscard(ctx context.Context, entry store.KeptWork, gatePath string) (bare, copied *vcs.Repository, err error) {
	if !entry.TaskID.IsKnown() {
		if bare, err = vcs.OpenBare(ctx, gatePath, c.git...); err != nil {
			return nil, nil, fmt.Errorf("tasks: opening the gate repository at %s: %w", gatePath, err)
		}
		if _, err := copyRecord(ctx, bare, entry.CopyPath); err != nil {
			return nil, nil, fmt.Errorf("tasks: the worktree at %s: %w", entry.CopyPath, err)
		}
	}
	if err := c.supportedLayout(ctx, entry); err != nil {
		return nil, nil, err
	}
	if copied, err = vcs.OpenWorktree(ctx, entry.CopyPath, c.git...); err != nil {
		return nil, nil, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	return bare, copied, nil
}

// PutBack puts back what a discard of the entry's copy that did not finish
// left set aside beside it, on kept_aside.go's terms, so Loss reads the
// copy again; it does nothing when nothing stands aside or the copy is
// gone. gatePath is Discard's. What could not be put back is
// ErrAsideStands, naming it, and stays. The caller keeps a discard of the
// same copy from running meanwhile, as it does for Discard.
func (c *Cleaner) PutBack(ctx context.Context, entry store.KeptWork, gatePath string) error {
	if _, err := os.Lstat(asideDir(entry.CopyPath)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(entry.CopyPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_, copied, err := c.openForDiscard(ctx, entry, gatePath)
	if err != nil {
		return err
	}
	return c.putBack(ctx, entry, copied)
}

// putBack is PutBack on the copy opened.
func (c *Cleaner) putBack(ctx context.Context, entry store.KeptWork, copied *vcs.Repository) error {
	dir := asideDir(entry.CopyPath)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return c.restoreAside(ctx, entry, copied, dir)
}

// removeOnProof removes a task's clone a discard emptied of its work, on
// P12's proof as CleanUp decides one: a tree git reports clean, the commits
// its references and HEAD stand on proven landed (prove), the inventory
// unchanged across the proof, and the clone removed on that proof
// (removeTaskCopy). The cleanup record is not written: the removal is the
// discard's, recorded on the entry. Anything short of the proof is
// ErrLossChanged.
func (c *Cleaner) removeOnProof(ctx context.Context, taskID string, copied *vcs.Repository) error {
	task, err := c.store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return err
	}
	if _, detail, refused := treeRefusal(inv); refused {
		return fmt.Errorf("%w: once what was shown was set aside, %s", ErrLossChanged, detail)
	}
	work := inventoryCommits(inv)
	l, err := c.taskLanding(ctx, task, copied, work)
	if err != nil {
		return err
	}
	removal, attempts := c.prove(ctx, l)
	if !removal.proven {
		var why []string
		for _, a := range attempts {
			why = append(why, string(a.Proof)+": "+a.Detail)
		}
		return fmt.Errorf("%w: once what was shown was set aside, none of P12's proofs holds for the clone: %s", ErrLossChanged, strings.Join(why, "; "))
	}
	again, err := copied.Inventory(ctx)
	if err != nil {
		return err
	}
	if !inventoryEqual(inv, again) {
		return fmt.Errorf("%w: the clone's references, HEAD, tree or stash changed while its proof was read", ErrLossChanged)
	}
	return removeTaskCopy(copied.Path(), removal)
}

// setDifference is every element of a that b does not hold, in a's order.
func setDifference(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// describeCommits names the commits lost now that the discard was not
// shown (unshown) and those it was shown as lost that are not lost now
// (held), a commit the upstream took up since or the copy no longer holds.
func describeCommits(unshown, held []string) string {
	var parts []string
	if len(unshown) > 0 {
		parts = append(parts, "lost now and not shown: "+strings.Join(unshown, ", "))
	}
	if len(held) > 0 {
		parts = append(parts, "shown and not lost now: "+strings.Join(held, ", "))
	}
	return strings.Join(parts, "; ")
}

// describeLoss names what a loss read holds, for a refusal to say what the
// copy holds now beside what was shown.
func describeLoss(l Loss) string {
	var parts []string
	if len(l.Lacks) > 0 {
		parts = append(parts, "commits "+strings.Join(l.Lacks, ", "))
	}
	for _, o := range l.Objects {
		parts = append(parts, o.Type+" "+o.Object)
	}
	for _, f := range l.Files {
		parts = append(parts, string(f.Kind)+" "+f.Path)
	}
	if l.Dirty {
		parts = append(parts, "a tracked change")
	}
	if l.Untracked {
		parts = append(parts, "a file no commit records")
	}
	if l.Stash {
		parts = append(parts, "a stash")
	}
	if len(parts) == 0 {
		return "nothing lost"
	}
	return strings.Join(parts, "; ")
}

// LeftBehind is what of loss, as Loss read it, a landing of HEAD does not
// carry, each named for a refusal: every commit lost that HEAD does not reach, and every
// other object lost that HEAD's history does not hold (vcs.NotHeldBy), an
// annotated tag always among them, since a landing carries no tag. It
// reads the copy through this Cleaner's handles, as Loss does, so what
// HEAD reaches is decided from the object graph Loss decided the loss
// from (NewCleaner's vcs.WithoutReplaceObjects), never through a graft
// file or a replacement reference.
func (c *Cleaner) LeftBehind(ctx context.Context, entry store.KeptWork, loss Loss) ([]string, error) {
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, c.git...)
	if err != nil {
		return nil, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	var behind []string
	for _, lost := range loss.Lacks {
		reaches, err := copied.IsAncestor(ctx, lost, loss.Head)
		if err != nil {
			return nil, fmt.Errorf("tasks: reading whether HEAD of the kept copy at %s reaches %s: %w", entry.CopyPath, lost, err)
		}
		if !reaches {
			behind = append(behind, lost)
		}
	}
	objects := make([]string, len(loss.Objects))
	for i, o := range loss.Objects {
		objects[i] = o.Object
	}
	missing, err := copied.NotHeldBy(ctx, loss.Head, objects)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading what HEAD of the kept copy at %s holds: %w", entry.CopyPath, err)
	}
	for _, o := range loss.Objects {
		if slices.Contains(missing, o.Object) {
			behind = append(behind, o.Type+" "+o.Object+" ("+strings.Join(o.Names, ", ")+")")
		}
	}
	return behind, nil
}

// copyRecord is the run's copy's record in the gate, the directory a
// discard removes with the worktree, established from the gate's side
// alone and never by following anything in the copy: the one record the
// gate's registry (vcs.WorktreeRecords, read from the gate and held to
// git's own listing) names the copy's path as its worktree, the path read
// with its parent's links resolved and the copy's own name as it stands.
// The copy's own files must then agree with that record, read without
// following them: the copy's path is a directory and no link, its .git is
// a regular file and no link, and that file names exactly that record.
// Anything else is refused with ErrRecordNotTheCopys and nothing is read
// or removed, so no record but the copy's own, another worktree's of the
// same gate among them, is ever taken for it, whatever the copy's .git
// says or points at.
func copyRecord(ctx context.Context, bare *vcs.Repository, copyPath string) (string, error) {
	info, err := os.Lstat(copyPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrRecordNotTheCopys, copyPath)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(copyPath))
	if err != nil {
		return "", err
	}
	worktree := filepath.Join(parent, filepath.Base(copyPath))
	records, err := bare.WorktreeRecords(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: the gate's records could not be read: %w", ErrRecordNotTheCopys, err)
	}
	var mine []string
	for _, r := range records {
		if r.Worktree == worktree {
			mine = append(mine, r.Record)
		}
	}
	// WorktreeRecords names each worktree git lists once.
	if len(mine) == 0 {
		return "", fmt.Errorf("%w: the gate records no worktree at %s", ErrRecordNotTheCopys, worktree)
	}
	record := mine[0]
	dotGit := filepath.Join(copyPath, ".git")
	gitInfo, err := os.Lstat(dotGit)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRecordNotTheCopys, err)
	}
	if !gitInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrRecordNotTheCopys, dotGit)
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", err
	}
	pointer, ok := strings.CutPrefix(strings.TrimSuffix(string(raw), "\n"), "gitdir: ")
	if !ok || pointer == "" || strings.ContainsAny(pointer, "\n\r\x00") {
		return "", fmt.Errorf("%w: %s reads %q, which names no one record", ErrRecordNotTheCopys, dotGit, raw)
	}
	if !filepath.IsAbs(pointer) {
		pointer = filepath.Join(copyPath, pointer)
	}
	if filepath.Clean(pointer) != record {
		return "", fmt.Errorf("%w: %s names the record %s, and the gate records %s for this copy", ErrRecordNotTheCopys, dotGit, pointer, record)
	}
	return record, nil
}
