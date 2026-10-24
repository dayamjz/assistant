package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The finding identifiers of the four holds incorporating the branch can
// raise.
const (
	// findingBranchReplayUnverified is the hold for a replay that applied but
	// whose result could not be shown to carry every change of the run's own
	// commits.
	findingBranchReplayUnverified = "rebase-branch-replay-unverified"
	// findingBranchOverlap is the hold for a branch whose commits changed a
	// path the run's change also changed.
	findingBranchOverlap = "rebase-branch-overlap"
	// findingBranchConflict is the hold for the run's own commits not
	// replaying cleanly onto commits the branch held that the run was not
	// given.
	findingBranchConflict = "rebase-branch-conflict"
	// findingBranchBehindBase is the hold for a branch whose commits the run
	// incorporated but that does not contain the base as the upstream holds it
	// now.
	findingBranchBehindBase = "rebase-branch-behind-base"
)

// incorporating is what incorporateBranch decides from: the run's facts and
// the observation this pass of the rebase stage took.
type incorporating struct {
	branch    string
	base      string
	submitted string
	anchor    safety.Observation
	// baseCommit is the base as the upstream holds it now, fetched by the
	// caller, and empty when the upstream holds no base.
	baseCommit string
	// recorder records a replay that applied and was kept.
	recorder rebaseRecorder
}

// incorporateBranch brings into the run's head the commits the branch under
// validation held when this pass observed it and that the run was not given,
// which is PRD section 5's rebase onto "the branch target" (#396).
//
// The commit it incorporates is the one the anchor names, not whatever a later
// read finds, so the push stage's fast-forward from that anchor is exactly the
// update this prepares. Those commits are somebody else's when the observed
// commit is not in the history submitted to the run: the run was never given
// them, and the push stage's basis rule refuses an update that drops them.
// Where the observed commit is in that history, nothing on the branch is
// anybody else's, and the caller's ordinary rebase onto the base applies; this
// reports handled false and does nothing.
//
// Otherwise the run's own commits are replayed onto the observed commit, which
// keeps every commit the branch holds as it is, so the push that follows is a
// fast-forward from the anchor. A replay that conflicts is abandoned, leaving
// the copy where it stood, and holds with an ask: what conflicting content
// should be, between the run's change and somebody else's, is not this run's
// to decide. A replay that applies is then held to the base: the base is
// already in the result when the branch contained it, and when it did not,
// bringing it in would mean rewriting the commits just kept, which the push
// would refuse, so that holds with an ask too rather than passing as rebased
// onto current code.
//
// A replay is attempted only where replayBase finds one merge base and
// expectedReplay finds the branch touched none of the paths the run changed;
// a branch that did holds for a person before anything is replayed, so a
// colleague's edit to a file the run also changed always comes to a person.
// A replay that applies is not taken on its word either: its tree has to be
// exactly the branch's tree with the run's entries, built without any apply
// or merge. Each refusal keeps the copy at the run's head and asks a person.
//
// What it does not establish: the branch is read once, so commits pushed to
// it after this pass observed it are not incorporated, and the push stage
// refuses those as a moved target.
func incorporateBranch(ctx context.Context, copied *vcs.Repository, f incorporating) (pipeline.Output, bool, error) {
	state := f.anchor.State()
	if !state.Exists {
		return pipeline.Output{}, false, nil
	}
	target := f.anchor.Target()
	if err := fetchTargetForDecision(ctx, copied, target); err != nil {
		return pipeline.Output{}, false, err
	}
	observed := state.Commit
	if _, err := copied.ResolveCommit(ctx, observed); err != nil {
		return pipeline.Output{}, false, fmt.Errorf(
			"stages: reading %s, the commit %s stood at when this run observed it: %w", observed, target, err)
	}
	given, err := copied.IsAncestor(ctx, observed, f.submitted)
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf(
			"stages: establishing whether %s holds commits this run was not given: %w", target, err)
	}
	if given {
		return pipeline.Output{}, false, nil
	}

	before, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf("stages: resolving HEAD: %w", err)
	}
	head := before
	held, err := copied.IsAncestor(ctx, observed, before)
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf(
			"stages: establishing whether %s already carries %s: %w", before, observed, err)
	}
	if !held {
		replayed := "none, it was not attempted"
		base, unverified, err := replayBase(ctx, copied, before, observed)
		if err != nil {
			return pipeline.Output{}, false, err
		}
		expected := ""
		if unverified == "" {
			var overlap []string
			expected, overlap, unverified, err = expectedReplay(ctx, copied, base, before, observed)
			if err != nil {
				return pipeline.Output{}, false, err
			}
			if len(overlap) > 0 {
				return pipeline.Output{Report: branchOverlap(f, target, observed, before, overlap)}, true, nil
			}
		}
		if unverified == "" {
			if err := copied.Rebase(ctx, vcs.RebaseSpec{Onto: observed}); err != nil {
				if !errors.Is(err, vcs.ErrRebaseConflict) {
					return pipeline.Output{}, false, fmt.Errorf("stages: replaying %s onto %s: %w", before, observed, err)
				}
				if err := copied.RebaseAbort(ctx); err != nil {
					return pipeline.Output{}, false, fmt.Errorf(
						"stages: abandoning the replay of %s onto %s: %w", before, observed, err)
				}
				return pipeline.Output{Report: branchConflict(f, target, observed, before)}, true, nil
			}
			if head, err = copied.ResolveCommit(ctx, "HEAD"); err != nil {
				return pipeline.Output{}, false, fmt.Errorf("stages: resolving HEAD after replaying it: %w", err)
			}
			replayed = head
			if unverified, err = replayUnverified(ctx, copied, expected, head); err != nil {
				return pipeline.Output{}, false, err
			}
			if unverified != "" {
				if err := copied.Detach(ctx, before); err != nil {
					return pipeline.Output{}, false, fmt.Errorf(
						"stages: returning the copy to %s after an unverified replay: %w", before, err)
				}
			}
		}
		if unverified != "" {
			own, err := copied.CommitsNotIn(ctx, before, observed)
			if err != nil {
				return pipeline.Output{}, false, fmt.Errorf(
					"stages: listing this run's commits on %s: %w", before, err)
			}
			return pipeline.Output{Report: replayNotVerified(f, target, observed, before, replayed, own, unverified)},
				true, nil
		}
		if err := f.recorder.record(ctx, before, observed, head); err != nil {
			return pipeline.Output{}, false, err
		}
	}
	writes := map[pipeline.Key]graph.Value{pipeline.KeyHead: graph.TextValue(head)}

	if f.baseCommit == "" {
		return pipeline.Output{
			Report: findings.Report{
				Summary: fmt.Sprintf("%s held %s, which this run was not given, and %s is the run's head on top "+
					"of it. %s holds no %s, so there is nothing newer to bring in beside it.",
					target, observed, head, UpstreamRemote, f.base),
				Revision: head,
			},
			Writes: writes,
		}, true, nil
	}
	current, err := copied.IsAncestor(ctx, f.baseCommit, head)
	if err != nil {
		return pipeline.Output{}, false, fmt.Errorf(
			"stages: establishing whether %s contains the base %s: %w", head, f.baseCommit, err)
	}
	if !current {
		return pipeline.Output{Report: branchBehindBase(f, target, observed, head), Writes: writes}, true, nil
	}
	out, err := checkDiffEmpty(ctx, copied, head, f.baseCommit, f.submitted)
	if err != nil {
		return pipeline.Output{}, false, err
	}
	if _, empty := out.Writes[pipeline.KeyDiffEmpty]; !empty {
		out.Report.Summary = fmt.Sprintf("%s held %s, which this run was not given, so the run's own commits "+
			"were replayed onto it: %s is the new head, and it contains that commit and %s at %s. The push "+
			"is a fast-forward from %s. %s",
			target, observed, head, f.base, f.baseCommit, observed, out.Report.Summary)
	}
	return withWrites(out, writes), true, nil
}

// branchConflict is the hold for the run's own commits not replaying cleanly
// onto commits the branch held that the run was not given.
func branchConflict(f incorporating, target safety.Target, observed, head string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("Replaying this run's commits onto %s at %s, which holds commits this run was not "+
			"given, stopped on a conflict, so it was abandoned and the run's head stays at %s.",
			target, observed, head),
		Findings: []findings.Finding{{
			ID:       findingBranchConflict,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("%s holds commits somebody else pushed that the change submitted to this "+
				"run does not contain, and the run's own commits do not apply cleanly on top of them. Deciding "+
				"what the conflicting content should be is a judgment about somebody else's work, so this run "+
				"does not make it: the replay was abandoned and nothing was rewritten.\n\nbranch: %s\n"+
				"observed: %s\nhead: %s\n\nBring %s into your change yourself and push the result through the "+
				"gate, which starts a new run. Asking for a fix round has a fixer work on the head as it "+
				"stands, and this stage tries the replay again over its result. Approving or skipping carries "+
				"the run on without the branch's commits, and the push stage then refuses to replace them "+
				"(would-discard). Cancelling ends the run.",
				target, f.branch, observed, head, f.branch),
		}},
	}
}

// branchBehindBase is the hold for a branch whose commits the run kept but that
// does not contain the base as the upstream holds it now.
func branchBehindBase(f incorporating, target safety.Target, observed, head string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("%s held %s, which this run was not given, and the run's head %s now stands on it, "+
			"but it does not contain %s at %s.", target, observed, head, f.base, f.baseCommit),
		Revision: head,
		Findings: []findings.Finding{{
			ID:       findingBranchBehindBase,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("%s holds commits somebody else pushed, and this run replayed its own "+
				"commits onto them so that they survive the push. The base %s has moved past what the branch "+
				"contains, and bringing it in would mean rewriting those commits, which this run does not do to "+
				"somebody else's work and the push stage would refuse.\n\nbranch: %s\nobserved: %s\nhead: %s\n"+
				"base: %s\n\nApproving carries the run on from %s, without the base's newest commits, and the "+
				"push is a fast-forward from %s. To run against the current base, update %s with whoever owns "+
				"those commits and push it through the gate. Cancelling ends the run.",
				target, f.base, f.branch, observed, head, f.baseCommit, head, observed, f.branch),
		}},
	}
}

// replayBase is the one merge base of before and observed, which the replay's
// check measures the run's change from, and why no replay is attempted when
// there is none or more than one: the run's change then has no single
// starting point to be measured from, so nothing could show a replay keeps it
// (P6).
func replayBase(ctx context.Context, copied *vcs.Repository, before, observed string) (string, string, error) {
	bases, err := copied.MergeBases(ctx, before, observed)
	switch {
	case errors.Is(err, vcs.ErrNoMergeBase):
		return "", fmt.Sprintf("%s and %s share no history to measure this run's change from", before, observed), nil
	case err != nil:
		return "", "", fmt.Errorf("stages: reading where %s and %s diverged: %w", before, observed, err)
	case len(bases) != 1:
		return "", fmt.Sprintf("%s and %s have %d best common ancestors, so this run's change has no one "+
			"starting point to be measured from", before, observed, len(bases)), nil
	}
	return bases[0], "", nil
}

// expectedReplay decides, before any replay, whether the run's change and the
// branch's commits may be combined without a person, and what the combination
// has to be. It reads trees only; no patch is applied and no merge runs.
//
// The run's paths are every path whose entry differs between base and before,
// recursing into directories with no rename detection, so a rename counts at
// both ends and a file added or deleted counts once. The branch touched one of
// them when observed's entry there, blob and mode or absence, is not base's.
// Any such path is overlap, which the caller holds on before replaying: a
// colleague's edit to a file the run also changed always goes to a person,
// whatever git's merge would make of it. That is the price of never placing
// one side's edit by matching context: merges, merge drivers and patch offsets
// all could, and five review rounds showed each check built on them agreeing
// with a replay that lost or misplaced the run's work (#436).
//
// Where nothing overlaps, the expected result is fixed by entries alone:
// observed's tree with each of the run's paths taking before's entry, or taken
// out where before has none (vcs.TreeWithEntries). The replay is then accepted
// only when its tree is that tree by object identifier (replayUnverified), so
// a commit git left out as already applied, a dropped mode change or a
// replayed commit that touched anything else makes the trees differ. Whatever
// backend or configuration the replay ran under, it can only be accepted
// where it produced exactly that tree, which is why the backend is not forced.
//
// reason is why no expected tree could be built, which holds too: a path that
// is a directory on one side where the other needs a file.
func expectedReplay(ctx context.Context, copied *vcs.Repository, base, before, observed string) (
	expected string, overlap []string, reason string, err error) {
	paths, err := copied.TreePathsDiffer(ctx, base, before)
	if err != nil {
		return "", nil, "", fmt.Errorf("stages: listing the paths this run changed since %s: %w", base, err)
	}
	if len(paths) == 0 {
		tree, err := copied.TreeOf(ctx, observed)
		return tree, nil, "", err
	}
	read := func(rev string) (map[string]vcs.TreeEntry, error) {
		entries, err := copied.TreeEntries(ctx, rev, paths)
		if err != nil {
			return nil, fmt.Errorf("stages: reading %s's entries for the paths this run changed: %w", rev, err)
		}
		return entries, nil
	}
	atBase, err := read(base)
	if err != nil {
		return "", nil, "", err
	}
	atObserved, err := read(observed)
	if err != nil {
		return "", nil, "", err
	}
	for _, path := range paths {
		if atObserved[path] != atBase[path] {
			overlap = append(overlap, path)
		}
	}
	if len(overlap) > 0 {
		return "", overlap, "", nil
	}
	atBefore, err := read(before)
	if err != nil {
		return "", nil, "", err
	}
	set := make(map[string]vcs.TreeEntry, len(paths))
	var remove []string
	for _, path := range paths {
		if entry, ok := atBefore[path]; ok {
			set[path] = entry
		} else {
			remove = append(remove, path)
		}
	}
	tree, built, err := copied.TreeWithEntries(ctx, observed, set, remove)
	if err != nil {
		return "", nil, "", fmt.Errorf("stages: building the tree this run's change makes of %s: %w", observed, err)
	}
	if !built {
		return "", nil, fmt.Sprintf("%s does not take this run's entries for %s, where a path is a directory "+
			"on one side and a file on the other", observed, strings.Join(paths, ", ")), nil
	}
	return tree, nil, "", nil
}

// replayUnverified reports why the replayed head is not the expected tree
// expectedReplay built, and "" when its tree is that tree by object
// identifier, which covers every path's content, mode and placement.
func replayUnverified(ctx context.Context, copied *vcs.Repository, expected, head string) (string, error) {
	got, err := copied.TreeOf(ctx, head)
	if err != nil {
		return "", fmt.Errorf("stages: reading the tree of the replayed head %s: %w", head, err)
	}
	if got == expected {
		return "", nil
	}
	paths, err := copied.TreePathsDiffer(ctx, expected, got)
	if err != nil {
		return "", fmt.Errorf("stages: listing where the replayed head %s differs: %w", head, err)
	}
	return fmt.Sprintf("the replayed head's tree %s is not %s, the branch with this run's entries, at %s, so the "+
		"replay left out or changed some of the run's work", got, expected, strings.Join(paths, ", ")), nil
}

// branchOverlap is the hold for a branch that touched a path the run
// changed. Nothing was replayed: the copy stands at the run's head.
func branchOverlap(f incorporating, target safety.Target, observed, head string, paths []string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("%s at %s changed %s, which this run changed too, so this run's commits were not "+
			"replayed onto it and the run's head stays at %s.", target, observed, strings.Join(paths, ", "), head),
		Findings: []findings.Finding{{
			ID:       findingBranchOverlap,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("%s holds commits somebody else pushed that change files this run also "+
				"changes. This run combines its change with theirs only where the two touch different files, "+
				"because combining edits to one file means placing one side's lines by matching the other's, "+
				"which can put a change in the wrong place or drop it without a conflict. A colleague's edit to "+
				"a file the run also changed therefore always comes to you.\n\nbranch: %s\nobserved: %s\n"+
				"head: %s\npaths: %s\n\nBring %s into your change yourself and push the result through the gate, "+
				"which starts a new run. Approving or skipping carries the run on without the branch's commits, "+
				"and the push stage then refuses to replace them (would-discard). Cancelling ends the run.",
				target, f.branch, observed, head, strings.Join(paths, ", "), f.branch),
		}},
	}
}

// replayNotVerified is the hold for a replay that could not be shown to carry
// every change of the run's own commits, whether replayBase kept it from
// being attempted or replayUnverified refused its result. The copy stands at
// the head the run stood on either way, so nothing of a replay is carried on.
func replayNotVerified(f incorporating, target safety.Target, observed, before, replayed string, own []string,
	reason string) findings.Report {
	return findings.Report{
		Summary: fmt.Sprintf("This run's commits were not carried onto %s at %s, because it could not be shown "+
			"that a replay keeps all of them, and the run's head stays at %s.", target, observed, before),
		Findings: []findings.Finding{{
			ID:       findingBranchReplayUnverified,
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf("%s holds commits somebody else pushed, and this run's own commits would be "+
				"replayed onto them, but %s. Carrying such a replay on could lose work you submitted, so none "+
				"is carried on and nothing was rewritten.\n\nbranch: %s\nobserved: %s\nhead: %s\nreplayed: %s\n"+
				"run commits: %s\n\nBring %s into your change yourself and push the result through the gate, "+
				"which starts a new run. Approving or skipping carries the run on without the branch's commits, "+
				"and the push stage then refuses to replace them (would-discard). Cancelling ends the run.",
				target, reason, f.branch, observed, before, replayed, strings.Join(own, ", "), f.branch),
		}},
	}
}
