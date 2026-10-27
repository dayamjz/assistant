package tasks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P15's "kept work is never silent" and section 6's kept work:
// a copy kept because its work has not landed is recorded as a kept work
// entry, store.KeptWork, the moment it is kept. This file is the one writer
// of that record, beside the cleanup record it already writes, because the
// decision that keeps a copy is this package's: Keep turns a kept copy into
// its entry, and KeptOf says which cleanup refusals are a copy kept for its
// work.
//
// What the entry says the copy holds is read from the copy by Keep, not
// taken from the caller: the commits its references and HEAD stand on for a
// task's clone, and the commit HEAD stands on for a run's worktree (whose
// references are the gate's), which is the copy's state and the entry's
// identity. What of that the default branch lacks, the PRD row's wording,
// is read from the default branch's tip, fresh, by lacking, and is the
// entry's basis: an entry about a copy that is not standing is refused
// rather than written from what a caller remembered, and one whose default
// branch could not be read says so rather than listing a guess, and is
// filled in by a later Keep of the unchanged copy that can read it.

// KeptPromotionWindow is the reason an investigation's copy is kept once its
// worker is ended at the end of promotion_window, PRD section 6's "when the
// window passes, the worker is ended and its copy is kept". It answers to
// #438, which ends that worker and raises the entry through internal/service;
// nothing in this build produces it yet.
const KeptPromotionWindow = "promotion-window-passed"

// Kept names a copy kept because its work has not landed: its one owner, a
// task or a run, and why it was kept. It carries nothing about what the copy
// holds, which Keep reads, or about the project and branch, which the
// owner's row holds.
type Kept struct {
	// Task names the task whose copy was kept, for a task's copy; Run names
	// the run, for a run's copy. Exactly one is set.
	Task string
	Run  string
	// Reason is the word the entry records for why: a refusal's reason, as
	// KeptOf gives it, or KeptPromotionWindow.
	Reason string
	// Detail is the account the entry carries as evidence, redacted by the
	// store before it is written.
	Detail string
}

// Unlanded reports whether the refusal is a copy kept because its work has
// not landed: none of P12's proofs held, or the copy holds a change, a file
// or a stash no commit records. Every other reason keeps the copy for a fact
// that was not established about the copy rather than for work in it: a
// worker that may still be writing, a copy that changed under the proof, a
// report or hold not yet registered, a shape that moved, or a copy that is
// not there. Those are asked again by the next cleanup and are not kept work.
func (r *CleanupRefusal) Unlanded() bool {
	switch r.Reason {
	case ReasonUnproven, ReasonCopyDirty, ReasonCopyUntracked, ReasonCopyStash:
		return true
	}
	return false
}

// KeptOf is the kept copy an unlanded refusal describes, with the refusal's
// reason and its account, each proof's attempt included. It answers false
// for a refusal Unlanded does not report.
func KeptOf(r *CleanupRefusal) (Kept, bool) {
	if r == nil || !r.Unlanded() {
		return Kept{}, false
	}
	detail := r.Detail
	for _, a := range r.Attempts {
		detail += "; " + string(a.Proof) + ": " + a.Detail
	}
	return Kept{Task: r.Task, Run: r.Run, Reason: string(r.Reason), Detail: detail}, true
}

// KeptEntry is what Keep did: the entry as it stands, and whether this call
// wrote it or established it, or that the copy holds nothing to keep.
type KeptEntry struct {
	// Entry is the kept work entry, and the zero value when NothingKept.
	Entry store.KeptWork
	// Recorded is whether this call wrote it; false is an entry already
	// recorded for the copy state, or nothing kept.
	Recorded bool
	// Established is whether this call filled in the basis of an entry
	// already recorded unestablished, through store.EstablishKeptWork.
	Established bool
	// NothingKept is a copy that holds nothing the default branch at
	// Against lacks as far as vcs.Inventory sees: no commit the tip does
	// not reach, and no tracked change, untracked file or stash. A file
	// git is configured to ignore is invisible to that inventory, as it
	// is to the cleanup decision, so a copy whose only unique content is
	// ignored reads NothingKept too; doc.go lists the gap. No entry is
	// written for it, and none stands for it: a copy in this state that
	// an entry already records is that entry, established with nothing
	// lacking, and not NothingKept.
	NothingKept bool
	// Against is the default branch tip NothingKept was read against.
	Against string
}

// Keep records the kept work entry for a kept copy and reports whether this
// call wrote it: a copy in the state an entry already records is that entry,
// on store.RecordKeptWork's terms, so asking again of an unchanged copy
// writes no second entry whatever the default branch read as either time.
// It reads what the copy holds from the copy, and what of that the default
// branch lacks from the default branch, read now and fetched by identifier
// (lacking): the entry lists exactly those commits as Lacks, with the tip
// they were computed against, or, when that cannot be computed, states so
// with the account and lists nothing as lacking. An entry already standing
// for the copy state is read first, before anything is decided, so what it
// becomes is decided on it: an open one recorded unestablished is
// established by this call when it can compute the set, once, which
// Established reports, whatever the set holds; an answered or closed one is
// returned as it stands. Only a copy no entry records, for which the set is
// established and empty and whose tree holds nothing the inventory sees as
// uncommitted, is NothingKept, and no entry is written: the same
// computation that lists a copy's work says there is none, so the scratch
// case is not a second rule. An entry established with nothing lacking
// stays open until a person answers it or the cleanup decision removes
// the copy on a proof, which closes it in the removal's transaction: those
// are the record's two closings, this call removes nothing, and the next
// cleanup of the copy is what the sites in internal/service ask.
//
// It refuses with ErrCleanupRecord a request naming no owner or both, a
// task whose reserved copy is not where this home puts it, and a copy that
// is not standing.
func (c *Cleaner) Keep(ctx context.Context, k Kept) (KeptEntry, error) {
	if (k.Task == "") == (k.Run == "") {
		return KeptEntry{}, fmt.Errorf("%w: a kept copy names exactly one owner, a task or a run", ErrCleanupRecord)
	}
	var path string
	var task store.Task
	var run store.Run
	var err error
	if k.Task != "" {
		task, err = c.store.Task(ctx, k.Task)
		if err != nil {
			return KeptEntry{}, err
		}
		if path, err = c.home.TaskCopy(task.ID); err != nil {
			return KeptEntry{}, err
		}
		if filepath.Clean(task.WorktreePath) != path {
			return KeptEntry{}, fmt.Errorf("%w: task %s reserved its copy at %s and this home puts it at %s",
				ErrCleanupRecord, task.ID, task.WorktreePath, path)
		}
	} else {
		run, err = c.store.Run(ctx, k.Run)
		if err != nil {
			return KeptEntry{}, err
		}
		path = c.home.Worktree(run.RepositoryID, run.ID)
	}
	standing, err := c.standing(path)
	if err != nil {
		return KeptEntry{}, err
	}
	if !standing {
		return KeptEntry{}, fmt.Errorf("%w: nothing stands at %s, so no copy was kept there", ErrCleanupRecord, path)
	}
	copied, err := vcs.OpenWorktree(ctx, path, c.git...)
	if err != nil {
		return KeptEntry{}, fmt.Errorf("tasks: opening the kept copy at %s: %w", path, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return KeptEntry{}, fmt.Errorf("tasks: reading the kept copy at %s: %w", path, err)
	}
	holds, reaches := inventoryCommits(inv), committedTips(inv)
	var l landing
	if k.Task != "" {
		l, err = c.taskLanding(ctx, task, copied, nil)
	} else {
		holds, reaches = []string{inv.Head.Commit}, []string{inv.Head.Commit}
		l, err = c.runLanding(ctx, run, copied, nil)
	}
	if err != nil {
		return KeptEntry{}, err
	}
	held := lacking(ctx, l, reaches)
	recorded, err := c.store.KeptWorkOf(ctx, path, holds)
	switch {
	case err == nil:
		return c.establish(ctx, recorded, held)
	case !errors.Is(err, store.ErrNotFound):
		return KeptEntry{}, err
	}
	if held.established() && len(held.lacks) == 0 && !inv.Dirty && !inv.Untracked && !inv.Stash {
		return KeptEntry{NothingKept: true, Against: held.against}, nil
	}
	entry, wrote, err := c.store.RecordKeptWork(ctx, store.KeptCopy{
		TaskID: k.Task, RunID: k.Run, CopyPath: path, Holds: holds, Lacks: held.lacks, Against: held.against,
		Unestablished: held.unestablished, Reason: k.Reason, Detail: k.Detail,
	})
	if err != nil {
		return KeptEntry{}, err
	}
	if wrote {
		return KeptEntry{Entry: entry, Recorded: true}, nil
	}
	// Another writer recorded the copy state between the read and the
	// write; what the entry becomes is decided on it the same way.
	return c.establish(ctx, entry, held)
}

// Unheld is what the entry recorded its copy standing on that the copy no
// longer holds: each of store.KeptWork.Holds, the commits and other
// objects HEAD and the references stood on when the copy was kept, as Keep
// records them, that no value a name of the copy records now is or reaches.
// The names are the copy's as copyNames reads them for a discard's loss,
// HEAD, every reference, every root reference such as ORIG_HEAD, and every
// reflog entry by either of its values (reflogValues); a value is what a
// name points at and what that peels to, so a tag on a blob names the
// blob. A hold that is one of those values is held, whatever its type;
// one that is a commit a commit value descends from is held, reached by
// ancestry; one that is a tree or a blob a commit value's history records,
// in any commit it reaches (vcs.NotHeldBy), is held; any other, a commit
// nothing names or reaches, a tag nothing names, a tree or blob no commit
// records, or an object the copy no longer has (vcs.ErrObjectNotFound),
// is unheld, in Holds' order. A copy that cannot be read is an error,
// never an answer, and so is an object the copy has but cannot read. It is
// read in the Cleaner's layout, without replacement references or a graft
// file, as Keep reads. internal/service's carry on asks it of the kept
// copy wherever it stands, by the entry with CopyPath set there, before it
// claims, before it moves and after it has moved the copy, and a promotion
// asks it of its copy for each entry its flip answers (Queue.Promote,
// through unheld), so every answer that would close an entry accepts and
// refuses the same copies: a copy whose kept commit was amended away is
// carried on while its reflog records it, and refused once that entry has
// expired and the object is pruned.
func (c *Cleaner) Unheld(ctx context.Context, entry store.KeptWork) ([]string, error) {
	return unheld(ctx, c.git, entry)
}

// unheld is Unheld over the git options given, so a Queue, which holds no
// Cleaner, asks the one reading and not a second one.
func unheld(ctx context.Context, git []vcs.Option, entry store.KeptWork) ([]string, error) {
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, git...)
	if err != nil {
		return nil, fmt.Errorf("tasks: opening the kept copy at %s: %w", entry.CopyPath, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the kept copy at %s: %w", entry.CopyPath, err)
	}
	refs, _, err := copyNames(ctx, entry, copied)
	if err != nil {
		return nil, err
	}
	logged, err := reflogValues(ctx, entry, copied, refs)
	if err != nil {
		return nil, err
	}
	values := []string{}
	add := func(v string) {
		if v != "" && !slices.Contains(values, v) {
			values = append(values, v)
		}
	}
	add(inv.Head.Commit)
	for _, ref := range refs {
		add(ref.Object)
		add(ref.Commit)
	}
	for _, v := range logged {
		add(v)
	}
	// The commits among the values, each once, are what ancestry and a
	// history's objects are read from.
	var commits []string
	for _, v := range values {
		id, err := copied.ResolveCommit(ctx, v)
		if errors.Is(err, vcs.ErrRefNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("tasks: reading whether %s names a commit in the kept copy at %s: %w", v, entry.CopyPath, err)
		}
		if !slices.Contains(commits, id) {
			commits = append(commits, id)
		}
	}
	var missing []string
	for _, h := range entry.Holds {
		if slices.Contains(values, h) {
			continue
		}
		held := false
		kind, err := copied.ObjectType(ctx, h)
		switch {
		case errors.Is(err, vcs.ErrObjectNotFound):
			// Not an object the copy has: unheld.
		case err != nil:
			// An object the copy has but could not read, or a read that
			// failed, is not an answer (round 46): naming it lost would
			// refuse a carry on and a promotion over work the copy holds.
			return nil, fmt.Errorf("tasks: reading what %s is in the kept copy at %s: %w", h, entry.CopyPath, err)
		case kind == "commit":
			for _, c := range commits {
				reaches, err := copied.IsAncestor(ctx, h, c)
				if err != nil {
					return nil, fmt.Errorf("tasks: reading whether the kept copy at %s reaches %s from %s: %w", entry.CopyPath, h, c, err)
				}
				if reaches {
					held = true
					break
				}
			}
		case kind == "tree" || kind == "blob":
			for _, c := range commits {
				notHeld, err := copied.NotHeldBy(ctx, c, []string{h})
				if err != nil {
					return nil, fmt.Errorf("tasks: reading whether the history of %s in the kept copy at %s records %s: %w", c, entry.CopyPath, h, err)
				}
				if len(notHeld) == 0 {
					held = true
					break
				}
			}
		}
		if !held && !slices.Contains(missing, h) {
			missing = append(missing, h)
		}
	}
	return missing, nil
}

// establish is what Keep does with an entry already standing for the copy
// state: fills in its basis when it is open, unestablished and the set is
// now established, and otherwise returns it as it stands.
func (c *Cleaner) establish(ctx context.Context, entry store.KeptWork, held held) (KeptEntry, error) {
	if entry.Established() || !entry.Open() || !held.established() {
		return KeptEntry{Entry: entry}, nil
	}
	entry, err := c.store.EstablishKeptWork(ctx, entry.ID, held.against, held.lacks)
	if err != nil {
		return KeptEntry{}, err
	}
	return KeptEntry{Entry: entry, Established: true}, nil
}

// committedTips is the tips a task copy's committed work is reached from:
// inventoryCommits without refs/stash, because a stash is uncommitted state,
// reported by the inventory's Stash indicator and kept for as such, and not
// a commit the default branch could lack.
func committedTips(inv vcs.Inventory) []string {
	stash := vcs.Inventory{Head: inv.Head}
	for _, ref := range inv.Refs {
		if ref.Name != "refs/stash" {
			stash.Refs = append(stash.Refs, ref)
		}
	}
	return inventoryCommits(stash)
}

// keptDefaultRef is the name, under a landing's namespace, the default
// branch tip a kept copy is compared against is fetched under.
const keptDefaultRef = "kept-default-branch"

// held is what a kept copy holds that the default branch lacks, or why that
// could not be established.
type held struct {
	// lacks is, when against is set, every commit the copy reaches that
	// against does not, each once, in the order of the references that
	// reach them and most recent first within each; and nil otherwise.
	lacks []string
	// against is the default branch tip lacks was computed against.
	against string
	// unestablished is why it could not be, when against is empty.
	unestablished string
}

func (h held) established() bool { return h.against != "" }

// lacking computes what the copy holds that the default branch lacks, the
// set PRD section 8's kept work row names: the default branch tip is read
// now, as the proofs read it (the upstream's, confirmed by its HEAD, or a
// local task's working copy's), fetched into the copy by identifier, and
// every commit reached from one of reaches and not from that tip is listed.
// Any step that fails leaves the set unestablished with that step's
// account and lists nothing, never a guess at the difference (PRD
// principle P6: refuse and report). The fetched reference is deleted
// afterwards, best effort, on landing.forget's terms.
func lacking(ctx context.Context, l landing, reaches []string) held {
	defer l.forget(ctx)
	unestablished := func(detail string) held { return held{unestablished: detail} }
	tip, detail := keptDefaultTip(ctx, l)
	if detail != "" {
		return unestablished(detail)
	}
	lacks := []string{}
	for _, reach := range reaches {
		lacked, err := l.repo.CommitsNotIn(ctx, reach, tip)
		if err != nil {
			return unestablished("which commits " + reach + " reaches that the default branch at " + tip +
				" lacks could not be read: " + err.Error())
		}
		for _, c := range lacked {
			if !slices.Contains(lacks, c) {
				lacks = append(lacks, c)
			}
		}
	}
	return held{lacks: lacks, against: tip}
}

// keptDefaultTip is the default branch tip a kept copy is compared against,
// read now and fetched into the copy by identifier, on lacking's terms, or
// why it could not be.
func keptDefaultTip(ctx context.Context, l landing) (string, string) {
	if l.upstream == "" {
		return "", "the record names no repository, so there is no default branch to read"
	}
	tip, detail := l.defaultTip(ctx)
	if detail != "" {
		return "", detail
	}
	if detail := l.fetch(ctx, tip, keptDefaultRef); detail != "" {
		return "", detail
	}
	return tip, ""
}

// lostFrom is what a discard of the copy loses: what nothing held reaches,
// held being the default branch and what the upstream holds now and
// nothing else, the gate a run's worktree belongs to included, decided
// from the copy's own objects and from everything that names one in what the
// removal destroys (reach, as Loss composes it) rather than from the
// references Keep reads. It is the commits any of it reaches that neither
// the default branch nor anything the upstream holds reaches, a commit
// only a reflog entry's previous value, a root reference such as
// ORIG_HEAD or the stash records among them, and every object reach names
// that is not a commit and is not itself held, a reflog's value of any
// type among them (vcs.NotReachedFrom). The default branch is read on
// lacking's terms. What the upstream holds now is held (upstreamHeld):
// every object named, of names, that the upstream advertises, by identity,
// an annotated tag cloned with the copy and a branch the upstream also
// has among them, with everything it reaches; and everything the commit
// the upstream's branch of the same name as one of refs, the copy's
// branches and remote-tracking references, stands on now reaches. A
// remote-tracking reference holds nothing of itself. Any read that fails
// leaves it unestablished.
func lostFrom(ctx context.Context, l landing, reach vcs.Reach, names []string, refs []vcs.Ref) (lost held, others []string) {
	defer l.forget(ctx)
	defer l.forgetHeld(ctx)
	tip, detail := keptDefaultTip(ctx, l)
	if detail != "" {
		return held{unestablished: detail}, nil
	}
	holding, detail := upstreamHeld(ctx, l, names, refs)
	if detail != "" {
		return held{unestablished: detail}, nil
	}
	commits, others, err := l.repo.NotReachedFrom(ctx, reach, tip, holding...)
	if err != nil {
		return held{unestablished: "which commits and objects the copy's names hold that the default branch at " + tip +
			" and the upstream lack could not be read: " + err.Error()}, nil
	}
	if commits == nil {
		commits = []string{}
	}
	return held{lacks: commits, against: tip}, others
}

// keptHeldRef is the name, under a landing's namespace, of the references
// upstreamHeld fetches what the upstream's branches stand on under, one
// per commit, deleted with the read (forgetHeld).
const keptHeldRef = "kept-upstream-held"

// upstreamHeld is what lostFrom takes as held beside the default branch:
// what the upstream holds now, read from what it advertises now and never
// from the copy's record of it. It is every object of names the upstream
// advertises, as an object a reference points at or as the commit an
// annotated tag it advertises peels to, compared by identifier, so a tag
// or a branch the copy holds that the upstream holds too, unchanged, is
// held and one the upstream does not hold or holds at another object is
// not; and, for each of the copy's branches and remote-tracking references
// (refs), the commit the upstream's branch of the same name stands on now,
// fetched into the copy by identifier under the read's namespace when
// names does not hold it, so what the copy's reference names is held when
// that commit reaches it, a branch the upstream moved on since among them.
// A remote-tracking reference is the copy's record of a remote as last
// fetched, so it holds nothing of itself: what it names that the upstream
// no longer reaches, a branch deleted or rewound there since, is not held.
// The same name is only where this looks for a branch; what is held is
// decided by what the advertised commit reaches. A remote-tracking
// reference is looked up as every branch name its name could end in, since
// a remote's name may hold a slash, and as the upstream's HEAD when it
// ends in HEAD. Every object returned is one the copy holds, so the walk
// can exclude it. Or why the upstream could not be read, or a commit it
// advertises could not be fetched.
func upstreamHeld(ctx context.Context, l landing, names []string, refs []vcs.Ref) ([]string, string) {
	advertised, err := l.repo.RemoteRefs(ctx, l.upstream)
	if err != nil {
		return nil, "the references the upstream holds could not be read: " + err.Error()
	}
	upstream := map[string]bool{}
	branches := map[string]string{}
	for _, ref := range advertised {
		upstream[ref.Object], upstream[ref.Commit] = true, true
		if ref.Name == "HEAD" || strings.HasPrefix(ref.Name, "refs/heads/") {
			branches[ref.Name] = ref.Commit
		}
	}
	named := map[string]bool{}
	for _, object := range names {
		named[object] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, object := range names {
		if upstream[object] && !seen[object] {
			seen[object] = true
			out = append(out, object)
		}
	}
	var fetch []string
	for _, ref := range refs {
		for _, name := range upstreamBranchNames(ref.Name) {
			commit, ok := branches[name]
			if !ok || commit == "" || seen[commit] {
				continue
			}
			seen[commit] = true
			out = append(out, commit)
			if !named[commit] {
				fetch = append(fetch, commit)
			}
		}
	}
	if len(fetch) > 0 {
		spec := vcs.FetchSpec{Remote: l.upstream}
		for i, commit := range fetch {
			spec.Refspecs = append(spec.Refspecs, "+"+commit+":"+l.namespace+"/"+keptHeldRef+"/"+strconv.Itoa(i))
		}
		// A commit the fetch did not land is not held by the copy, and the
		// walk excluding it fails on it rather than reading past it.
		if err := l.repo.Fetch(ctx, spec); err != nil {
			return nil, "what the upstream's branches stand on could not be fetched: " + err.Error()
		}
	}
	return out, ""
}

// upstreamBranchNames is every name the upstream could advertise the branch
// a copy's reference stands for under: a branch's own name, and for a
// remote-tracking reference refs/remotes/<remote>/<branch> every branch its
// name could end in, or the upstream's HEAD for one ending in HEAD. Any
// other reference stands for none.
func upstreamBranchNames(ref string) []string {
	if strings.HasPrefix(ref, "refs/heads/") {
		return []string{ref}
	}
	rest, ok := strings.CutPrefix(ref, "refs/remotes/")
	if !ok {
		return nil
	}
	var out []string
	for i := strings.Index(rest, "/"); i >= 0 && i < len(rest)-1; {
		branch := rest[i+1:]
		if branch == "HEAD" {
			out = append(out, "HEAD")
		} else {
			out = append(out, "refs/heads/"+branch)
		}
		next := strings.Index(branch, "/")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return out
}

// forgetHeld deletes the references upstreamHeld fetched under, best
// effort, as forget does the others.
func (l landing) forgetHeld(ctx context.Context) {
	if l.repo == nil || l.namespace == "" {
		return
	}
	refs, err := l.repo.ListRefs(ctx, l.namespace+"/"+keptHeldRef+"/")
	if err != nil {
		return
	}
	for _, ref := range refs {
		_ = l.repo.DeleteRef(ctx, ref.Name)
	}
}
