package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// upstreamRemote is the remote the run's isolated copy reaches the change's
// destination by: the remote a pull request is eventually opened against, and
// the one PRD section 5 means by a freshly fetched upstream.
//
// It is deliberately not internal/gate's RemoteName. The gate is where the
// change arrived from, and PRD principle P1 makes it the one remote this
// product writes to a person's working copy; the branch target this stage
// fetches and the push stage later updates lives on the person's own remote
// instead.
//
// No package owns this name yet, and nothing in this build sets it. The copy a
// run works in is a linked worktree of the gate, so its remotes are the gate's
// own configuration, and internal/gate creates that repository with none: it
// sets one remote, on the working copy, pointing the other way. PRD section 7
// records the upstream on the repository and says the credentialed URL is
// recovered at run time, and no code here does that yet.
//
// So this stage names the git default and refuses loudly rather than fetching
// from whatever happens to be configured. A copy with no remote of this name
// fails the stage with a message naming what is missing, which is what a run
// meets today, and it is the one fact this body needs that its dependencies do
// not carry.
const upstreamRemote = "origin"

// Rebase is the rebase stage: it brings the change onto a freshly fetched
// upstream and branch target, so every stage after it reads current code.
//
// # This is where P6 first bites
//
// This stage is the first that moves history, and PRD principle P6's anchor
// rule is the reason it does one thing before it does anything else: it reads
// where the branch target stands on the remote and records that reading in
// pipeline.KeyObservation. internal/safety owns what an anchor is and what may
// be done on one; nothing here decides whether an update may proceed, and this
// stage constructs no safety.Decision.
//
// The anchor is taken here rather than at the push stage because P6 names the
// alternative outright: a tip read immediately before writing always matches
// and therefore protects nothing. Stage 2 observes, stage 7 decides, and the
// record between them is state, which is what survives the restart PRD section
// 13 requires a run to survive.
//
// So the observation is taken at most once per run. A fix round re-runs this
// stage, and re-observing then would replace an anchor taken before the work
// with one taken after it, which is the same worthless anchor arriving by a
// slower route. A run that already carries a record restores it through
// safety.RestoreObservedFromCheckpoint and observes nothing.
//
// What that buys is bounded, and the bound is internal/safety's rather than
// this stage's: a restored record's provenance rests on the checkpoint it came
// out of, not on the type. Read that package's documentation for the residual
// gaps there. What this stage adds is that the record is written before the
// fetch and the rebase, so the anchor describes the target as it stood before
// this run touched anything.
//
// # What it rebases onto
//
// The freshly fetched branch target, except when the branch under validation
// has itself advanced on the remote past what this run holds. Then the
// replay is onto the branch's own fetched tip, so the commits somebody else
// landed on the branch are carried rather than dropped; that is what makes the
// recovery from a refused push a re-run rather than a manual repair.
//
// One shape is refused rather than resolved. A branch that advanced on the
// remote and whose tip does not contain the freshly fetched target needs two
// replays, the branch's own work onto the target and then this run's work onto
// that, and which of the two goes first is a judgment about somebody else's
// commits. This stage reports it as an ask finding and stops, which is P3's
// fail-closed answer, and it does not attempt the two-step replay.
//
// The fetch writes remote-tracking references, and the copy shares the gate's
// reference store with every other copy of the same repository, so what this
// stage writes there is visible to a concurrent run of another branch. The
// only value either of them can write for the target is a tip the upstream
// advertised, which is what every run of that repository is asking for, so the
// sharing costs a run a fresher target than the one it fetched and nothing
// else. The branch under validation is not shared that way, because one branch
// has one run.
//
// # Nothing left to change is an outcome
//
// A change already contained in the target rebases to nothing, and PRD section
// 5 ends that run successfully with the stages after this one skipped rather
// than failed. This stage sets pipeline.KeyDiffEmpty and internal/pipeline's
// stage nodes read it; no finding is reported against a change that no longer
// exists, because there is nothing left to report about.
//
// The comparison is the diff between what was rebased onto and the result, not
// the commit identifiers: a replay that drops every commit and one that keeps
// a commit whose content is already present are both nothing left to change,
// and reading the identifiers would only see the first.
//
// # Fix rounds
//
// config.FixRounds.Rebase governs this stage, and a conflict is what it is
// for: the conflicting paths are reported as one fix-eligible finding.
// internal/vcs aborts the stopped rebase before returning, so the copy is
// never left mid-rebase, and what a round therefore changes is the history
// being replayed rather than a conflicted index. Nothing continues a stopped
// rebase; a round is followed by this stage rebasing again from the start.
//
// # The residual gap: unpushed default-branch commits
//
// PRD section 5 has this stage stop if the branch would quietly carry commits
// from the person's local default branch that were never pushed. What this
// stage can see is the copy: it compares the copy's own reference for the
// target branch against the freshly fetched one, and any commit the first
// holds and the second does not is a commit that never reached the upstream.
// A branch carrying one is held for a person.
//
// That bounds the check to what the copy holds, and the bound is worth stating
// because it is where the check is weakest. A commit that reached the copy
// only inside the branch under validation is indistinguishable there from the
// branch's own work, so this comparison does not see it. And a copy with no
// reference of its own for the target branch gives the comparison nothing to
// make: this stage reports that it could not be made rather than reporting
// that nothing was carried, because a check that concludes without comparing
// anything is worse than no check.
func Rebase(deps StageDeps) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{
			pipeline.KeyRepository, pipeline.KeyRun, pipeline.KeyBranch,
			pipeline.KeyBase, pipeline.KeyHead, pipeline.KeyObservation,
		},
		Writes: []pipeline.Key{pipeline.KeyHead, pipeline.KeyDiffEmpty, pipeline.KeyObservation},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return rebaseOntoFresh(ctx, deps, in)
			}
		},
	}
}

// rebaseFacts are the run's own facts this stage reads out of state.
type rebaseFacts struct {
	repository string
	run        string
	branch     string
	base       string
	head       string
	// observation is the encoded anchor an earlier execution of this stage
	// recorded, empty when this run has not observed its target yet.
	observation string
}

// readRebaseFacts reads what this stage needs from the run's state. A refused
// read is returned rather than reported: the reader has already recorded it
// and internal/pipeline fails the step whatever the body returns, so a body
// that softened it would be claiming a leniency the mechanism does not have.
func readRebaseFacts(state pipeline.Reader) (rebaseFacts, error) {
	var facts rebaseFacts
	for _, field := range []struct {
		key  pipeline.Key
		into *string
	}{
		{pipeline.KeyRepository, &facts.repository},
		{pipeline.KeyRun, &facts.run},
		{pipeline.KeyBranch, &facts.branch},
		{pipeline.KeyBase, &facts.base},
		{pipeline.KeyHead, &facts.head},
		{pipeline.KeyObservation, &facts.observation},
	} {
		value, err := state.Get(field.key)
		if err != nil {
			return rebaseFacts{}, err
		}
		text, _ := value.Text()
		*field.into = strings.TrimSpace(text)
	}
	return facts, nil
}

// rebaseOntoFresh is the stage body.
func rebaseOntoFresh(ctx context.Context, deps StageDeps, in pipeline.Input) (pipeline.Output, error) {
	facts, err := readRebaseFacts(in.State)
	if err != nil {
		return pipeline.Output{}, err
	}
	copyOf, err := deps.Copy(ctx, facts.repository, facts.run)
	if err != nil {
		return pipeline.Output{}, err
	}
	if _, err := copyOf.RemoteURL(ctx, upstreamRemote); err != nil {
		if errors.Is(err, vcs.ErrRemoteNotFound) {
			return pipeline.Output{}, fmt.Errorf(
				"stages: the isolated copy for run %s at %s has no %q remote, so the upstream this change is destined for cannot be fetched",
				facts.run, copyOf.Path(), upstreamRemote)
		}
		return pipeline.Output{}, err
	}

	target := safety.Target{Remote: upstreamRemote, Ref: "refs/heads/" + facts.branch}
	observation, record, err := anchor(ctx, safety.New(copyOf), target, facts.observation)
	if err != nil {
		return pipeline.Output{}, err
	}
	writes := map[pipeline.Key]graph.Value{}
	if record != "" {
		writes[pipeline.KeyObservation] = graph.TextValue(record)
	}

	if err := fetchFresh(ctx, copyOf, facts, observation); err != nil {
		return pipeline.Output{}, err
	}
	baseCommit, err := copyOf.ResolveCommit(ctx, remoteRef(facts.base))
	if err != nil {
		return pipeline.Output{}, fmt.Errorf(
			"stages: reading the freshly fetched %s of run %s: %w", facts.base, facts.run, err)
	}

	carry, err := carriedUnpushed(ctx, copyOf, facts, baseCommit)
	if err != nil {
		return pipeline.Output{}, err
	}
	if carry.holds() {
		return withWrites(report(carry.summary, carry.finding), writes), nil
	}

	onto, refusal, err := replayOnto(ctx, copyOf, facts, observation, baseCommit)
	if err != nil {
		return pipeline.Output{}, err
	}
	if refusal != nil {
		return withWrites(report(refusal.summary, refusal.finding, carry.finding), writes), nil
	}

	rebased, err := copyOf.Rebase(ctx, vcs.RebaseSpec{Onto: onto, Commit: facts.head})
	if err != nil {
		var conflict *vcs.RebaseConflict
		if !errors.As(err, &conflict) {
			return pipeline.Output{}, err
		}
		return withWrites(report(
			fmt.Sprintf("Rebasing %s onto %s stopped on a conflict, and the copy was left as it was found.",
				short(facts.head), short(onto)),
			conflictFinding(conflict, facts), carry.finding), writes), nil
	}
	writes[pipeline.KeyHead] = graph.TextValue(rebased)

	changed, err := copyOf.ChangedFiles(ctx, onto, rebased)
	if err != nil {
		return pipeline.Output{}, err
	}
	if len(changed) == 0 {
		writes[pipeline.KeyDiffEmpty] = graph.BoolValue(true)
		return withWrites(report(fmt.Sprintf(
			"Rebasing %s onto %s left nothing to change, so this run has no diff to validate.",
			short(facts.head), short(onto))), writes), nil
	}
	return withWrites(report(fmt.Sprintf(
		"The change was rebased onto %s, and %d file(s) differ from it.", short(onto), len(changed)),
		carry.finding), writes), nil
}

// anchor returns the run's observation of its branch target, and the record to
// write when this run took it here. A run that already carries one restores it
// and observes nothing, which is what keeps a fix round from replacing an
// anchor taken before the work with one taken after it.
func anchor(ctx context.Context, guard *safety.Guard, target safety.Target, recorded string) (safety.Observation, string, error) {
	if recorded != "" {
		var rec safety.ObservationRecord
		if err := json.Unmarshal([]byte(recorded), &rec); err != nil {
			return safety.Observation{}, "", fmt.Errorf(
				"stages: reading this run's recorded observation of %s: %w", target, err)
		}
		observation, err := safety.RestoreObservedFromCheckpoint(rec)
		if err != nil {
			return safety.Observation{}, "", err
		}
		if observation.Target() != target {
			return safety.Observation{}, "", fmt.Errorf(
				"stages: this run recorded an observation of %s, and its branch target is %s",
				observation.Target(), target)
		}
		return observation, "", nil
	}
	observation, err := guard.Observe(ctx, target)
	if err != nil {
		return safety.Observation{}, "", err
	}
	encoded, err := json.Marshal(observation.Record())
	if err != nil {
		return safety.Observation{}, "", fmt.Errorf("stages: recording the observation of %s: %w", target, err)
	}
	return observation, string(encoded), nil
}

// fetchFresh brings the branch target, and the branch under validation when
// the remote has one, into the copy under remote-tracking names.
//
// The branch is fetched because the run may have to replay onto it, and the
// commits behind it have to be in the copy before any comparison can be
// answered locally. It is fetched only when the observation found one, so a
// branch that has never been pushed is not a refspec git refuses.
func fetchFresh(ctx context.Context, copyOf *vcs.Repository, facts rebaseFacts, observation safety.Observation) error {
	refspecs := []string{"+refs/heads/" + facts.base + ":" + remoteRef(facts.base)}
	if observation.State().Exists {
		refspecs = append(refspecs, "+refs/heads/"+facts.branch+":"+remoteRef(facts.branch))
	}
	if err := copyOf.Fetch(ctx, vcs.FetchSpec{Remote: upstreamRemote, Refspecs: refspecs}); err != nil {
		return fmt.Errorf("stages: fetching %s for run %s: %w", upstreamRemote, facts.run, err)
	}
	return nil
}

// remoteRef is the remote-tracking name a fetched branch lands under.
func remoteRef(branch string) string { return "refs/remotes/" + upstreamRemote + "/" + branch }

// carriedCheck is what the unpushed-default-branch comparison concluded: a
// summary and finding when there is something to say, and whether it stops the
// stage.
type carriedCheck struct {
	summary string
	finding findings.Finding
	stop    bool
}

// holds reports whether the comparison stops the stage before it replays
// anything.
func (c carriedCheck) holds() bool { return c.stop }

// carriedUnpushed asks whether replaying this change would carry commits the
// copy's own reference for the target branch holds and the freshly fetched one
// does not. Those are commits that never reached the upstream, and PRD section
// 5 stops the stage rather than folding them into the branch quietly.
//
// A copy with no reference of its own for the target branch has nothing to
// compare, and that is reported as a note rather than passed over: a
// comparison that concludes without comparing anything would report the same
// clean result whether or not the condition was there.
func carriedUnpushed(ctx context.Context, copyOf *vcs.Repository, facts rebaseFacts, baseCommit string) (carriedCheck, error) {
	local, err := copyOf.ListRefs(ctx, "refs/heads/"+facts.base)
	if err != nil {
		return carriedCheck{}, err
	}
	if len(local) == 0 {
		return carriedCheck{finding: findings.Finding{
			ID:       "rebase-unpushed-check-not-made",
			Severity: findings.SeverityInfo,
			Action:   findings.ActionNote,
			Description: fmt.Sprintf(
				"This copy carries no %s reference of its own, so there was nothing to compare the "+
					"freshly fetched %s against and the check for commits that never reached %s was "+
					"not made. It was not made rather than passed: a commit reaching this copy only "+
					"inside %s is not distinguishable here from the change's own work.",
				facts.base, facts.base, upstreamRemote, facts.branch),
		}}, nil
	}
	unpushed, err := copyOf.CommitsNotIn(ctx, local[0].Commit, baseCommit)
	if err != nil {
		return carriedCheck{}, err
	}
	if len(unpushed) == 0 {
		return carriedCheck{}, nil
	}
	replayed, err := copyOf.CommitsNotIn(ctx, facts.head, baseCommit)
	if err != nil {
		return carriedCheck{}, err
	}
	carried := intersect(replayed, unpushed)
	if len(carried) == 0 {
		return carriedCheck{}, nil
	}
	return carriedCheck{
		summary: fmt.Sprintf("The change carries %d commit(s) from %s that never reached %s, so it was not rebased.",
			len(carried), facts.base, upstreamRemote),
		finding: findings.Finding{
			ID:       "rebase-carries-unpushed-default-commits",
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"%s holds %d commit(s) that %s does not, and rebasing would fold them into %s as if "+
					"they were part of this change:\n\n%s\n\nNothing was rebased. Pushing those "+
					"commits to %s first, or rewriting the change so it does not build on them, is a "+
					"decision about work that is not this change's.",
				facts.base, len(carried), remoteRef(facts.base), facts.branch,
				strings.Join(carried, "\n"), upstreamRemote),
		},
		stop: true,
	}, nil
}

// replayRefusal is a shape this stage reports and stops on rather than
// resolving.
type replayRefusal struct {
	summary string
	finding findings.Finding
}

// replayOnto chooses the commit the change is replayed onto: the freshly
// fetched branch target, or the branch's own observed tip when the branch has
// advanced on the remote past what this run holds.
//
// It refuses the one shape it will not resolve, which is a branch that
// advanced and whose tip does not contain the freshly fetched target. Bringing
// both onto one base takes two replays, and which of them goes first is a
// judgment about commits this run did not write.
func replayOnto(ctx context.Context, copyOf *vcs.Repository, facts rebaseFacts, observation safety.Observation, baseCommit string) (string, *replayRefusal, error) {
	state := observation.State()
	if !state.Exists {
		return baseCommit, nil, nil
	}
	tip, err := copyOf.ResolveCommit(ctx, state.Commit)
	if err != nil {
		return "", &replayRefusal{
			summary: fmt.Sprintf("The commit this run observed on %s is not in the copy after fetching, so nothing was rebased.", facts.branch),
			finding: findings.Finding{
				ID:       "rebase-observed-branch-tip-missing",
				Severity: findings.SeverityError,
				Action:   findings.ActionAsk,
				Description: fmt.Sprintf(
					"This run observed %s standing at %s on %s, and fetching %s did not bring that "+
						"commit into the copy, which is what a branch rewritten on the remote since "+
						"the observation looks like. Nothing was rebased, because replaying onto a "+
						"target this run cannot see would be replaying onto a guess.",
					facts.branch, state.Commit, upstreamRemote, facts.branch),
			},
		}, nil
	}
	held, err := copyOf.IsAncestor(ctx, tip, facts.head)
	if err != nil {
		return "", nil, err
	}
	if held {
		return baseCommit, nil, nil
	}
	onBase, err := copyOf.IsAncestor(ctx, baseCommit, tip)
	if err != nil {
		return "", nil, err
	}
	if onBase {
		return tip, nil, nil
	}
	ahead, err := copyOf.CommitsNotIn(ctx, tip, facts.head)
	if err != nil {
		return "", nil, err
	}
	return "", &replayRefusal{
		summary: fmt.Sprintf("%s has advanced on %s onto a target this run cannot replay onto in one step, so nothing was rebased.",
			facts.branch, upstreamRemote),
		finding: findings.Finding{
			ID:       "rebase-branch-advanced-off-target",
			Severity: findings.SeverityError,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"%s on %s holds %d commit(s) this run does not, and its tip %s does not contain the "+
					"freshly fetched %s, so bringing both this change and those commits onto the "+
					"target takes two replays and an order to do them in:\n\n%s\n\nNothing was "+
					"rebased. Which work goes first is a judgment about commits this run did not "+
					"write, so it is not one this stage makes.",
				facts.branch, upstreamRemote, len(ahead), short(state.Commit), facts.base,
				strings.Join(ahead, "\n")),
		},
	}, nil
}

// conflictFinding turns a stopped rebase into the one fix-eligible finding
// config.FixRounds.Rebase exists for.
func conflictFinding(conflict *vcs.RebaseConflict, facts rebaseFacts) findings.Finding {
	where := "The paths it stopped on could not be read."
	var location findings.Location
	if len(conflict.Paths) > 0 {
		where = "It stopped on:\n\n" + strings.Join(conflict.Paths, "\n")
		location = findings.Location{Path: conflict.Paths[0]}
	}
	return findings.Finding{
		ID:       "rebase-conflict",
		Severity: findings.SeverityError,
		Action:   findings.ActionFix,
		Location: location,
		Description: fmt.Sprintf(
			"Replaying %s onto %s stopped on a conflict. %s\n\nThe rebase was aborted, so the copy "+
				"holds the change as it stood and not a half-finished replay. Resolving this means "+
				"changing the commits being replayed so they apply to %s; there is no stopped rebase "+
				"to continue.",
			facts.branch, short(conflict.Onto), where, facts.base),
	}
}

// intersect returns the members of first that also appear in second, in
// first's order.
func intersect(first, second []string) []string {
	in := make(map[string]struct{}, len(second))
	for _, item := range second {
		in[item] = struct{}{}
	}
	var both []string
	for _, item := range first {
		if _, ok := in[item]; ok {
			both = append(both, item)
		}
	}
	return both
}

// short renders a commit the way a person reads one in a summary.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// report builds this stage's output. A zero finding is one a caller had
// nothing to say for, and it is dropped rather than reported as a finding with
// no description, which is what internal/findings refuses.
func report(summary string, found ...findings.Finding) pipeline.Output {
	var kept []findings.Finding
	for _, finding := range found {
		if finding.Description != "" {
			kept = append(kept, finding)
		}
	}
	return pipeline.Output{Report: findings.Report{Summary: summary, Findings: kept}}
}

// withWrites attaches the state writes to an output.
func withWrites(out pipeline.Output, writes map[pipeline.Key]graph.Value) pipeline.Output {
	if len(writes) > 0 {
		out.Writes = writes
	}
	return out
}
