package tasks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The three proofs PRD principle P12 names, each recording the commit
// identifiers it examined (P6 applied to a removal rather than to a push),
// and each fresh: a remote is read with vcs.RemoteRefs at the time of the
// check and the tip it advertises is fetched by identifier, so what the
// proof compares against is the object the remote named then and not a
// remote-tracking reference that proves only that something was once
// pushed. The upstream is the repository record's, never the gate: a
// reference in the gate is a push the gate admitted and not a landing.
//
// # A landing the gate rebased
//
// The gate's rebase stage gives the commits it replays new identifiers and
// publishes those, so a task copy still holding what its worker pushed holds
// commits no upstream reaches under their own identifiers, however surely
// they landed. The first two proofs therefore ask of each commit of the work
// whether it is reachable from the tip they read, or else whether a run
// rebase record of one of the task's own runs (store.RunRebase) carries it
// there: the commit is in the history of the commit that run was submitted,
// read in the copy, the head that run's rebase left, which is the identifier
// the run published, is reachable from the tip, and integrating the commit
// into that head would change nothing, by internal/safety's Incorporation.
// The record is a fact the program observed when it made the rewrite, and it
// says which published commit to look at; it never stands in for looking. A
// rebase can return without a conflict and still leave work out, and the
// stage refuses to record one its own content check catches, but this proof
// makes the same check again over the copy's own commit rather than trust
// that, because the copy is what a removal would destroy. What the content
// check costs: a chain of rebases with a fix round between them whose commit
// rewrote the worker's own lines does not hold the worker's commit as it
// stood, so such a landing is not followed and its copy is kept unless
// another proof holds. The record is
// internal/stages' to write and to state the limits of, in rebaserecord.go;
// what this adds to them is that only the runs internal/store attributes to
// the task are read (TaskSnapshot.Runs), so a rebase recorded by a run the
// snapshot no longer attributes, one of an earlier attempt's, is not followed
// and its copy is kept unless another proof holds. A run copy's work is the
// head the run itself left, so its landing follows no record.
//
// No transaction is claimed around any of this. A fetch lands objects and
// a reference under refs/assistant/cleanup/ in the repository the check
// runs in, the gate's repository for a run copy and the clone itself for a
// task copy, which is why vcs.Inventory excludes everything outside
// refs/heads/, refs/tags/ and refs/stash. The references are deleted after
// the check on a best-effort basis; one left behind is overwritten by the
// next check of the same copy and is read by nothing.

// landing is what the proofs are made over.
type landing struct {
	// repo is the repository the fetches land in and the comparisons run
	// in.
	repo *vcs.Repository
	// upstream is the remote the work must have landed on, as a URL.
	upstream string
	// defaultBranch is the repository record's default branch, which the
	// upstream is asked to confirm fresh before it is relied on.
	defaultBranch string
	// branch is the remote branch the work may be reachable from, and empty
	// when the record names none.
	branch string
	// work is the commits that must have landed.
	work []string
	// carried is the run rebase records of the task's runs, which the first
	// two proofs may follow from a commit of the work to the commit the gate
	// published for it; empty for a run's copy and for a task none of whose
	// runs recorded one.
	carried []store.RunRebase
	// pullRequest is the merged pull request the record names, when a merge
	// record names one.
	pullRequest *pullRequestFact
	// namespace is where this check's fetched references go, under
	// refs/assistant/cleanup/, named for the copy so two checks of different
	// copies in one repository do not overwrite each other.
	namespace string
	// local is a task delivered under the local posture. Its work landed in
	// the person's own default branch and nowhere upstream, so upstream is
	// the repository's working path, and the one proof made is P12's content
	// check against that branch, localDefault.
	local bool
}

// pullRequestFact is what the merge record says about the pull request the
// merged-pull-request proof reads: the repository on the code host, the
// number, and the head the merge was recorded at.
type pullRequestFact struct {
	repository string
	number     int
	head       string
}

// taskLanding builds the landing for a task's copy: the fetches land in the
// clone, the branch is the task's, and the pull request is the merge record
// of the run internal/taskstate names as the one that landed the task.
func (c *Cleaner) taskLanding(ctx context.Context, task store.Task, copied *vcs.Repository, work []string) (landing, error) {
	l := landing{repo: copied, branch: task.Branch.Or(""), work: work, namespace: "refs/assistant/cleanup/task/" + task.ID}
	if id, ok := task.RepositoryID.Get(); ok {
		repository, err := c.store.Repository(ctx, id)
		if err != nil {
			return landing{}, fmt.Errorf("tasks: reading the repository task %s works in: %w", task.ID, err)
		}
		l.upstream, l.defaultBranch = repository.UpstreamURL, repository.DefaultBranch
		if posture, ok := task.Delivery.Get(); ok && posture == config.DeliveryLocal {
			l.local, l.upstream = true, repository.WorkingPath
		}
	}
	snap, err := c.store.TaskSnapshot(ctx, task.ID)
	if err != nil {
		return landing{}, err
	}
	for _, run := range snap.Runs {
		rebases, err := c.store.RunRebases(ctx, run.ID)
		if err != nil {
			return landing{}, fmt.Errorf("tasks: reading the rebases run %s of task %s recorded: %w", run.ID, task.ID, err)
		}
		l.carried = append(l.carried, rebases...)
	}
	resolved := taskstate.Resolve(taskstate.Inputs{Task: snap.Task, Runs: snap.Runs, Holds: snap.Holds,
		Events: snap.Events, MergeRecords: snap.MergeRecords, Cleanup: snap.Cleanup, LocalLanding: snap.LocalLanding})
	if landed := resolved.Basis.LandedRun; landed != "" {
		for _, rec := range snap.MergeRecords {
			if rec.Run == landed && rec.End == store.PullRequestMerged {
				l.pullRequest = &pullRequestFact{number: rec.PullRequest, head: rec.Head}
			}
		}
	}
	if l.pullRequest != nil {
		l.pullRequest.repository, _, _ = forge.RepositoryOf(l.upstream)
	}
	return l, nil
}

// runLanding builds the landing for a run's copy: the fetches land in the
// gate's repository, the branch is the run's, and the pull request is the
// run's own merge record.
func (c *Cleaner) runLanding(ctx context.Context, run store.Run, bare *vcs.Repository, work []string) (landing, error) {
	repository, err := c.store.Repository(ctx, run.RepositoryID)
	if err != nil {
		return landing{}, fmt.Errorf("%w: reading the repository run %s is of: %w", ErrCleanupRecord, run.ID, err)
	}
	l := landing{repo: bare, upstream: repository.UpstreamURL, defaultBranch: repository.DefaultBranch,
		branch: run.Branch, work: work, namespace: "refs/assistant/cleanup/run/" + run.ID}
	record, found, err := c.store.MergeRecord(ctx, run.ID)
	if err != nil {
		return landing{}, err
	}
	if found && record.End == store.PullRequestMerged {
		specifier, _, _ := forge.RepositoryOf(l.upstream)
		l.pullRequest = &pullRequestFact{repository: specifier, number: record.PullRequest, head: record.Head}
	}
	return l, nil
}

// prove tries the three proofs in P12's order and returns the first that is
// established, or every attempt's account of why it was not. A proof that
// could not be computed is an attempt and never a proof.
func (c *Cleaner) prove(ctx context.Context, l landing) (Removal, []ProofAttempt) {
	defer l.forget(ctx)
	if l.upstream == "" {
		detail := "the record names no repository, so there is no upstream to read"
		return Removal{}, []ProofAttempt{
			{Proof: store.CleanupProofRemoteBranch, Detail: detail},
			{Proof: store.CleanupProofMergedPullRequest, Detail: detail},
			{Proof: store.CleanupProofDefaultBranch, Detail: detail},
		}
	}
	proofs := []func(context.Context, landing) (Removal, string){c.remoteBranch, c.mergedPullRequest, c.defaultBranch}
	if l.local {
		proofs = []func(context.Context, landing) (Removal, string){c.localDefault}
	}
	var attempts []ProofAttempt
	for _, proof := range proofs {
		r, detail := proof(ctx, l)
		if r.proven {
			return r, nil
		}
		attempts = append(attempts, ProofAttempt{Proof: r.proof, Detail: detail})
	}
	return Removal{}, attempts
}

// remoteBranch is the first proof: every commit of the work is reachable
// from the tip the upstream advertises for the branch, read now and fetched
// by identifier, itself or through a rebase the task's runs recorded.
func (c *Cleaner) remoteBranch(ctx context.Context, l landing) (Removal, string) {
	proof := store.CleanupProofRemoteBranch
	if l.branch == "" {
		return Removal{proof: proof}, "the record names no branch"
	}
	refs, err := l.repo.RemoteRefs(ctx, l.upstream, "refs/heads/"+l.branch)
	if err != nil {
		return Removal{proof: proof}, "the upstream could not be read: " + err.Error()
	}
	tip := ""
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+l.branch {
			tip = ref.Commit
		}
	}
	if tip == "" {
		return Removal{proof: proof}, "the upstream advertises no branch " + l.branch
	}
	if detail := l.fetch(ctx, tip, "remote-branch"); detail != "" {
		return Removal{proof: proof}, detail
	}
	examined, detail := l.allReached(ctx, tip, "the upstream's "+l.branch+" at "+tip, func(w string) string {
		return "commit " + w + " is not reachable from the upstream's " + l.branch + " at " + tip
	})
	if detail != "" {
		return Removal{proof: proof}, detail
	}
	return Removal{proof: proof, examined: append(examined, tip), proven: true}, ""
}

// mergedPullRequest is the second proof: the pull request the merge record
// names reads merged on the code host, into the default branch the upstream
// confirms fresh, at the head the merge was recorded at, and that head,
// fetched by identifier, contains every commit of the work, itself or
// through a rebase the task's runs recorded.
func (c *Cleaner) mergedPullRequest(ctx context.Context, l landing) (Removal, string) {
	proof := store.CleanupProofMergedPullRequest
	if l.pullRequest == nil {
		return Removal{proof: proof}, "no merge record names a merged pull request"
	}
	pr := l.pullRequest
	if pr.repository == "" {
		return Removal{proof: proof}, "the upstream is not a repository on the code host this build addresses"
	}
	provider, err := c.forge.Open(pr.repository)
	if err != nil {
		return Removal{proof: proof}, "the code host for " + pr.repository + " could not be opened: " + err.Error()
	}
	got, err := provider.Get(ctx, pr.number)
	if err != nil {
		return Removal{proof: proof}, "pull request " + strconv.Itoa(pr.number) + " could not be read: " + err.Error()
	}
	if got.State != forge.PullRequestStateMerged {
		return Removal{proof: proof}, "pull request " + strconv.Itoa(pr.number) + " reads " + got.State.String() + ", not merged"
	}
	if _, detail := l.freshDefault(ctx); detail != "" {
		return Removal{proof: proof}, detail
	}
	if got.Base != l.defaultBranch {
		return Removal{proof: proof}, "pull request " + strconv.Itoa(pr.number) + " merged into " + got.Base +
			", which is not the default branch " + l.defaultBranch
	}
	if got.HeadCommit == "" || got.HeadCommit != pr.head {
		return Removal{proof: proof}, "pull request " + strconv.Itoa(pr.number) + "'s head is " + got.HeadCommit +
			" and the merge was recorded at " + pr.head + ", so the head moved after the merge"
	}
	if detail := l.fetch(ctx, got.HeadCommit, "pull-request-head"); detail != "" {
		return Removal{proof: proof}, detail
	}
	examined, detail := l.allReached(ctx, got.HeadCommit, "the merged head "+got.HeadCommit, func(w string) string {
		return "commit " + w + " is not in the merged head " + got.HeadCommit
	})
	if detail != "" {
		return Removal{proof: proof}, detail
	}
	return Removal{proof: proof, examined: append(examined, got.HeadCommit), proven: true}, ""
}

// allReached asks reached of every commit of the work against tip, which the
// caller has fetched and names as where for a detail and states a commit not
// reached in with absent, and returns the commits
// the answers rest on, each once: every commit of the work in order, and the
// submitted commit and published head of each rebase record an answer
// followed. A detail is the first commit not reached, or the first answer
// that could not be established.
func (l landing) allReached(ctx context.Context, tip, where string, absent func(string) string) ([]string, string) {
	var examined []string
	for _, w := range l.work {
		via, detail := l.reached(ctx, w, tip, where, absent)
		if detail != "" {
			return nil, detail
		}
		for _, c := range append([]string{w}, via...) {
			if !slices.Contains(examined, c) {
				examined = append(examined, c)
			}
		}
	}
	return examined, ""
}

// reached answers whether commit w is reachable from tip: itself, or through
// a rebase record of the task's runs, in record order, whose submitted commit
// w is in the history of, read in the copy, and whose published head is
// reachable from tip. It returns the record's submitted commit and published
// head when it followed one, and nothing when w is reachable itself. A commit
// a record names that the copy does not hold is not a way there, since the
// tip's history is in the copy once it is fetched; any other failure is a
// detail and never an answer. A commit not reached is stated with absent,
// followed by how many records were looked through.
func (l landing) reached(ctx context.Context, w, tip, where string, absent func(string) string) ([]string, string) {
	direct, err := l.repo.IsAncestor(ctx, w, tip)
	if err != nil {
		return nil, "whether " + w + " is reachable from " + where + " could not be established: " + err.Error()
	}
	if direct {
		return nil, ""
	}
	var lacking []string
	for _, r := range l.carried {
		followed, lacks, detail := l.follows(ctx, r, w, tip)
		if detail != "" {
			return nil, detail
		}
		if followed {
			return []string{r.Submitted, r.After}, ""
		}
		if lacks != "" {
			lacking = append(lacking, lacks)
		}
	}
	detail := absent(w) + ", itself or through " + recordedRebases(len(l.carried))
	if len(lacking) > 0 {
		detail += ": " + strings.Join(lacking, "; ")
	}
	return nil, detail
}

// follows reports whether rebase record r carries w to tip: w is in the
// history of r's submitted commit, r's published head is reachable from tip,
// and integrating w into that published head would change nothing, checked
// here through internal/safety's Incorporation rather than taken from the
// record, so a removal never rests on the writer's word that the rewrite
// kept the work. A commit the copy does not hold answers false. A published
// head that does not hold w's content, or whose comparison with it could not
// be made, answers false with what was found, for the detail of a commit no
// record carried. Any other failure is a detail.
func (l landing) follows(ctx context.Context, r store.RunRebase, w, tip string) (bool, string, string) {
	for _, step := range [][2]string{{w, r.Submitted}, {r.After, tip}} {
		ok, err := l.repo.IsAncestor(ctx, step[0], step[1])
		switch {
		case errors.Is(err, vcs.ErrRefNotFound):
			return false, "", ""
		case err != nil:
			return false, "", "whether " + step[0] + " is reachable from " + step[1] + ", which run " + r.Run +
				"'s recorded rebase rests on, could not be established: " + err.Error()
		case !ok:
			return false, "", ""
		}
	}
	result, err := safety.New(l.repo).Incorporation(ctx, []string{w}, r.After)
	switch {
	case errors.Is(err, safety.ErrNotEstablished):
		return false, "whether " + r.After + ", the head run " + r.Run + "'s recorded rebase left, holds " + w +
			" was not established: " + err.Error(), ""
	case err != nil:
		return false, "", "whether " + r.After + ", the head run " + r.Run + "'s recorded rebase left, holds " + w +
			" could not be established: " + err.Error()
	case !result.Incorporated():
		return false, "integrating " + w + " into " + r.After + ", the head run " + r.Run +
			"'s recorded rebase left, would change it", ""
	}
	return true, "", ""
}

// recordedRebases names how many rebase records the task's runs left, for a
// detail saying none of them carried a commit where it was looked for.
func recordedRebases(n int) string {
	switch n {
	case 0:
		return "a rebase, since none of the task's runs recorded one"
	case 1:
		return "the one rebase the task's runs recorded"
	}
	return "any of the " + strconv.Itoa(n) + " rebases the task's runs recorded"
}

// defaultBranch is the third proof: the work is already present, by
// content, in the default branch's tip, read fresh, fetched by identifier,
// checked through internal/safety's incorporation, and read again
// afterwards so a tip that moved during the check is not the one recorded.
func (c *Cleaner) defaultBranch(ctx context.Context, l landing) (Removal, string) {
	proof := store.CleanupProofDefaultBranch
	tip, detail := l.freshDefault(ctx)
	if detail != "" {
		return Removal{proof: proof}, detail
	}
	if detail := l.fetch(ctx, tip, "default-branch"); detail != "" {
		return Removal{proof: proof}, detail
	}
	result, err := safety.New(l.repo).Incorporation(ctx, l.work, tip)
	if err != nil {
		return Removal{proof: proof}, err.Error()
	}
	if !result.Incorporated() {
		return Removal{proof: proof}, "integrating " + strings.Join(result.Commits(), ", ") + " into " + result.Into() + " would change it"
	}
	if again, detail := l.freshDefault(ctx); detail != "" {
		return Removal{proof: proof}, detail
	} else if again != tip {
		return Removal{proof: proof}, "the default branch moved from " + tip + " to " + again + " during the check"
	}
	return Removal{proof: proof, examined: append(result.Commits(), result.Into()), proven: true}, ""
}

// localDefault is the proof for a task delivered under the local posture,
// PRD section 6's "P12's content check, read against the local default branch
// that fast-forward moved": the work is present by content in the tip the
// repository's working copy holds for its default branch, read now from that
// working copy, fetched by identifier, checked through internal/safety's
// incorporation, and read again afterwards so a tip that moved during the
// check is not the one recorded. It is recorded as the default-branch proof,
// because it is that proof read against the branch the work landed in. No
// HEAD is compared, unlike freshDefault: which branch is the default is the
// repository record's, and the working copy's HEAD is wherever its owner
// stands.
func (c *Cleaner) localDefault(ctx context.Context, l landing) (Removal, string) {
	proof := store.CleanupProofDefaultBranch
	tip, detail := l.localTip(ctx)
	if detail != "" {
		return Removal{proof: proof}, detail
	}
	if detail := l.fetch(ctx, tip, "local-default-branch"); detail != "" {
		return Removal{proof: proof}, detail
	}
	result, err := safety.New(l.repo).Incorporation(ctx, l.work, tip)
	if err != nil {
		return Removal{proof: proof}, err.Error()
	}
	if !result.Incorporated() {
		return Removal{proof: proof}, "integrating " + strings.Join(result.Commits(), ", ") + " into the local " +
			l.defaultBranch + " at " + result.Into() + " would change it"
	}
	if again, detail := l.localTip(ctx); detail != "" {
		return Removal{proof: proof}, detail
	} else if again != tip {
		return Removal{proof: proof}, "the local " + l.defaultBranch + " moved from " + tip + " to " + again + " during the check"
	}
	return Removal{proof: proof, examined: append(result.Commits(), result.Into()), proven: true}, ""
}

// localTip reads, now, the tip the repository's working copy holds for its
// default branch, which is where a local task's work lands.
func (l landing) localTip(ctx context.Context) (string, string) {
	if l.defaultBranch == "" {
		return "", "the repository record names no default branch"
	}
	refs, err := l.repo.RemoteRefs(ctx, l.upstream, "refs/heads/"+l.defaultBranch)
	if err != nil {
		return "", "the working copy could not be read: " + err.Error()
	}
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+l.defaultBranch {
			return ref.Commit, ""
		}
	}
	return "", "the working copy has no branch " + l.defaultBranch
}

// defaultTip is the default branch tip work is compared against, read now:
// the working copy's for a local task, through localTip, and otherwise the
// upstream's, through freshDefault.
func (l landing) defaultTip(ctx context.Context) (string, string) {
	if l.local {
		return l.localTip(ctx)
	}
	return l.freshDefault(ctx)
}

// freshDefault reads the upstream's default branch tip now, and confirms
// that the branch the record names is the one the upstream's HEAD stands on
// (PRD principle P7: the default branch is read from the upstream, never
// from a local setting). The confirmation is by commit, which is what an
// advertisement carries; a second branch standing on the same commit as
// HEAD would pass it, which is a residual gap.
func (l landing) freshDefault(ctx context.Context) (string, string) {
	if l.defaultBranch == "" {
		return "", "the repository record names no default branch"
	}
	refs, err := l.repo.RemoteRefs(ctx, l.upstream, "HEAD", "refs/heads/"+l.defaultBranch)
	if err != nil {
		return "", "the upstream could not be read: " + err.Error()
	}
	head, tip := "", ""
	for _, ref := range refs {
		switch ref.Name {
		case "HEAD":
			head = ref.Commit
		case "refs/heads/" + l.defaultBranch:
			tip = ref.Commit
		}
	}
	switch {
	case tip == "":
		return "", "the upstream advertises no branch " + l.defaultBranch
	case head == "":
		return "", "the upstream advertises no HEAD, so which branch is its default is not established"
	case head != tip:
		return "", "the upstream's HEAD is " + head + " and its " + l.defaultBranch + " is " + tip + ", so " +
			l.defaultBranch + " is not established as the default branch"
	}
	return tip, ""
}

// fetch brings one commit into the repository by identifier, under the
// check's namespace, and confirms the reference then names that commit.
func (l landing) fetch(ctx context.Context, commit, name string) string {
	ref := l.namespace + "/" + name
	if err := l.repo.Fetch(ctx, vcs.FetchSpec{Remote: l.upstream, Refspecs: []string{"+" + commit + ":" + ref}}); err != nil {
		return "commit " + commit + " could not be fetched from the upstream: " + err.Error()
	}
	got, err := l.repo.ResolveCommit(ctx, ref)
	if err != nil {
		return "the fetched commit " + commit + " does not resolve: " + err.Error()
	}
	if got != commit {
		return "the fetch for " + commit + " landed " + got + " instead"
	}
	return ""
}

// forget deletes the references the check fetched under, best effort.
func (l landing) forget(ctx context.Context) {
	if l.repo == nil {
		return
	}
	for _, name := range []string{"remote-branch", "pull-request-head", "default-branch", "local-default-branch", keptDefaultRef} {
		_ = l.repo.DeleteRef(ctx, l.namespace+"/"+name)
	}
}
