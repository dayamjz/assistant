package journey_test

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// emptied is what a run did over the empty-after-rebase scenario, read off
// the answer the binary gave, the remote it would have pushed to, and the
// home's records.
type emptied struct {
	// completed is whether the run's record says it passed.
	completed bool
	// after is every stage after rebase with the outcome the run recorded.
	after map[string]pipeline.Outcome
	// agentCalls is how many times the stand-in agent was invoked, which a
	// review that ran would make at least one of.
	agentCalls int
	// remoteBefore and remoteAfter are where the branch stood on the remote
	// either side of the run, empty for absent.
	remoteBefore, remoteAfter string
	// submitted is the branch's own commit, and landed the default branch's
	// commit making the same edit.
	submitted, landed string
	// record is the run's nothing-to-deliver record, and recorded whether it
	// has one.
	record   store.NothingToDeliver
	recorded bool
	// tasks is how many tasks the home holds, and landedTasks how many of
	// them the catch-up read lists as landed.
	tasks, landedTasks int
	// caughtUp is whether the catch-up read answered.
	caughtUp bool
}

// TestARunWhoseRebaseLeavesNothingToDeliverEndsThroughTheBinary drives the
// fixture's stage-no-diff-after-rebase condition, and PRD section 13's
// "Nothing to deliver" row's run half (P6, P8), through the binary that
// ships. The branch makes the edit the default branch already carries under
// another commit, so the run's real rebase replays the commit and drops it.
//
// Run by a person on a branch the remote does not hold, which no task's
// attempt names: the run completes with every stage after rebase skipped
// and no agent invoked, the remote gains no branch, and the home records one
// nothing-to-deliver record naming the run, the default branch's commit and
// the submitted commit, while no task is changed or listed as landed. Run
// over the scenario as planted, with the branch already on the remote: the
// same short circuit, the remote's branch where it stood, and no record,
// because a published branch never lands this way.
func TestARunWhoseRebaseLeavesNothingToDeliverEndsThroughTheBinary(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)
	requireStageListMatchesThePRD(t)

	t.Run("an unpublished branch", func(t *testing.T) {
		observed := walkEmptied(t, false)
		if err := emptiedCheck(true).Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a published branch", func(t *testing.T) {
		observed := walkEmptied(t, true)
		if err := emptiedCheck(false).Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// walkEmptied copies the empty-after-rebase scenario's remote for this test,
// clones a working copy on its branch, removes the branch from the remote
// unless published, and runs the branch once through the binary.
func walkEmptied(t *testing.T, published bool) emptied {
	t.Helper()
	from := scenarioNamed(t, fixture.ScenarioEmptyAfterRebase)
	own := from
	own.Paths = maps.Clone(from.Paths)
	own.Origin = filepath.Join(t.TempDir(), "remote.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := journey.Git(own, dir, args...)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return out
	}
	git(filepath.Dir(own.Origin), "clone", "--quiet", "--bare", from.Origin, own.Origin)
	work, err := journey.Clone(own, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("cloning: %v", err)
	}
	observed := emptied{submitted: from.Commits["branch-change"], landed: from.Commits["default-branch-same-change"]}
	if got := git(work, "rev-parse", "HEAD"); got != observed.submitted {
		t.Fatalf("the working copy stands at %s, want the branch's own commit %s", got, observed.submitted)
	}
	if !published {
		git(own.Origin, "update-ref", "-d", "refs/heads/"+own.Branch)
	}
	observed.remoteBefore = remoteRef(own, own.Branch)

	agent := standin.New(t, standin.Script{})
	j := open(t, own, func(o *journey.Options) { o.Dir = work; o.AgentArguments = agent.Arguments() })
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	run := startRun(t, j, "--intent", "narrow the Total loop bound")
	observed.completed = run.Record.Status == store.RunPassed
	observed.after = map[string]pipeline.Outcome{}
	for _, v := range run.Stages {
		if v.Stage != pipeline.StageIntent.String() && v.Stage != pipeline.StageRebase.String() {
			observed.after[v.Stage] = v.Outcome
		}
	}
	observed.agentCalls = len(agent.Calls())
	observed.remoteAfter = remoteRef(own, own.Branch)

	records := records(t, j)
	observed.record, observed.recorded, err = records.NothingToDeliver(t.Context(), run.Record.ID)
	if err != nil {
		t.Fatalf("reading the run's nothing-to-deliver record: %v", err)
	}
	tasks, err := records.TaskSnapshots(t.Context())
	if err != nil {
		t.Fatalf("reading the tasks: %v", err)
	}
	observed.tasks = len(tasks)
	var caught machine.CatchUp
	if err := succeeds(t, j.Command("watch", "--once")).Decode(&caught); err != nil {
		t.Fatalf("reading the catch-up: %v", err)
	}
	observed.landedTasks, observed.caughtUp = len(caught.Landed), true
	return observed
}

// remoteRef is where branch stands on the scenario's remote, and empty when
// the remote does not hold it.
func remoteRef(s fixture.Scenario, branch string) string {
	out, err := journey.Git(s, s.Origin, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// emptiedCheck is what both walks are held to; lands says whether the run's
// branch was unpublished, so it writes the record.
func emptiedCheck(lands bool) journey.Check[emptied] {
	check := journey.Check[emptied]{
		What: "Nothing to deliver: a run whose rebase leaves nothing to change",
		Clauses: []journey.Clause[emptied]{
			{
				States: "the run passed, with every stage after rebase skipped",
				Holds: func(e emptied) error {
					if !e.completed {
						return errors.New("the run's record does not say it passed")
					}
					want := len(pipeline.Order()) - 2
					if len(e.after) != want {
						return fmt.Errorf("the run reported %d stages after rebase, want %d", len(e.after), want)
					}
					for _, stage := range slices.Sorted(maps.Keys(e.after)) {
						if e.after[stage] != pipeline.OutcomeSkipped {
							return fmt.Errorf("the %s stage ended %s rather than skipped", stage, e.after[stage])
						}
					}
					return nil
				},
			},
			{
				States:  "no agent was invoked, so no review ran on a change that no longer exists",
				Absence: true,
				Possible: func(e emptied) error {
					if e.submitted == "" || e.submitted == e.landed {
						return errors.New("the branch carried no commit of its own a review could have read")
					}
					return nil
				},
				Holds: func(e emptied) error {
					if e.agentCalls != 0 {
						return fmt.Errorf("the stand-in agent was invoked %d time(s)", e.agentCalls)
					}
					return nil
				},
			},
			{
				States: "the remote's branch stands where it stood before the run",
				Holds: func(e emptied) error {
					if e.remoteAfter != e.remoteBefore {
						return fmt.Errorf("the remote's branch moved from %q to %q", e.remoteBefore, e.remoteAfter)
					}
					return nil
				},
			},
			{
				States:  "no task was changed or listed as landed",
				Absence: true,
				Possible: func(e emptied) error {
					if !e.caughtUp {
						return errors.New("the catch-up read never answered, so it could have listed nothing")
					}
					return nil
				},
				Holds: func(e emptied) error {
					if e.tasks != 0 || e.landedTasks != 0 {
						return fmt.Errorf("the home holds %d task(s), %d listed as landed", e.tasks, e.landedTasks)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[emptied]{
			{Named: "the run carried on past the short circuit to review", Break: func(e emptied) emptied {
				e.after = maps.Clone(e.after)
				e.after[pipeline.StageReview.String()] = pipeline.OutcomePassed
				return e
			}},
			{Named: "the run failed", Break: func(e emptied) emptied { e.completed = false; return e }},
			{Named: "a review agent was invoked", Break: func(e emptied) emptied { e.agentCalls = 1; return e }},
			{Named: "the remote's branch moved", Break: func(e emptied) emptied { e.remoteAfter = e.landed; return e }},
			{Named: "a task was listed as landed", Break: func(e emptied) emptied { e.tasks, e.landedTasks = 1, 1; return e }},
		},
	}
	if lands {
		check.Clauses = append(check.Clauses, journey.Clause[emptied]{
			States: "one nothing-to-deliver record names the run's commit and the default branch's",
			Holds: func(e emptied) error {
				if !e.recorded {
					return errors.New("the run wrote no nothing-to-deliver record")
				}
				if e.record.DefaultCommit != e.landed || e.record.Submitted != e.submitted ||
					!slices.Equal(e.record.Examined, []string{e.submitted}) || e.record.Checkpoint < 1 {
					return fmt.Errorf("the record reads %+v, want %s checked present in %s", e.record, e.submitted, e.landed)
				}
				return nil
			},
		})
		check.Counterfeits = append(check.Counterfeits,
			journey.Counterfeit[emptied]{Named: "no record was written", Break: func(e emptied) emptied {
				e.recorded = false
				return e
			}},
			journey.Counterfeit[emptied]{Named: "the record names the branch's commit as the default branch's",
				Break: func(e emptied) emptied {
					e.record.DefaultCommit = e.submitted
					return e
				}},
		)
		return check
	}
	check.Clauses = append(check.Clauses, journey.Clause[emptied]{
		States:  "no nothing-to-deliver record was written for a published branch",
		Absence: true,
		Possible: func(e emptied) error {
			if !e.completed || len(e.after) == 0 {
				return errors.New("the run never reached the end the record is written at")
			}
			return nil
		},
		Holds: func(e emptied) error {
			if e.recorded {
				return fmt.Errorf("the run wrote %+v over a branch already on the remote", e.record)
			}
			return nil
		},
	})
	check.Counterfeits = append(check.Counterfeits, journey.Counterfeit[emptied]{
		Named: "a record was written for the published branch",
		Break: func(e emptied) emptied { e.recorded = true; return e },
	})
	return check
}
