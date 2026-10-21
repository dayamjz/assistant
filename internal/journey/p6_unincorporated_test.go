package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// unincorporated is what a run did whose branch on the remote already held a
// commit the run's own commit lacks when the run began, read off the answers
// the binary gave and off the remote it pushed to.
type unincorporated struct {
	// stops is the stage of every hold the run reached, and expected is where
	// stages.Holding says it stops.
	stops    []string
	expected []string
	// colleague is the commit somebody else landed on the branch before the
	// run started, and so the commit the rebase stage observed it at.
	colleague string
	// own is the subject of the run's own commit, empty where the working
	// copy committed nothing.
	own string
	// forwarded is the head the lint stage last checked, which is the commit
	// the push stage forwards.
	forwarded string
	// pushed is the push stage's summary, empty if it never reported.
	pushed string
	// after is where the branch stood on the remote once the run stopped,
	// and history is "<commit> <subject>" for every commit it reaches.
	after   string
	history []string
}

// reaches reports whether the remote's branch reaches a commit, and
// reachesSubject whether it reaches one with the given subject.
func (u unincorporated) reaches(commit string) bool {
	return slices.ContainsFunc(u.history, func(line string) bool { return strings.HasPrefix(line, commit+" ") })
}

func (u unincorporated) reachesSubject(subject string) bool {
	return slices.ContainsFunc(u.history, func(line string) bool { return strings.HasSuffix(line, " "+subject) })
}

// without returns a copy of u whose remote history lacks every line holding s.
func (u unincorporated) without(s string) unincorporated {
	u.history = slices.DeleteFunc(slices.Clone(u.history), func(line string) bool {
		return strings.Contains(line, s)
	})
	return u
}

// TestARunIncorporatesCommitsItsBranchHeldBeforeItBegan drives issue #396
// through the binary: the branch under validation already holds a commit
// somebody else pushed when the run starts, and the run's own commit does not
// contain it. The rebase stage replays the run's commits onto the branch as it
// observed it, so the push is a fast-forward anchored on the colleague's
// commit and the colleague's commit survives on the remote.
//
// Two shapes, both from the issue: a working copy that committed beside the
// colleague's commit (diverged), and one that committed nothing, which stands
// behind it. Before the rebase stage incorporated the branch both held at
// the push stage on a would-discard refusal; that refusal is still the
// backstop, held at package reach by internal/stages' push tests, because no
// walk here carries a run past the rebase stage without incorporating.
//
// The P6 cite covers this: the push forwarded the head the lint stage checked
// as a fast-forward, its anchor is the colleague's commit, and the remote
// reaches that commit and, in the diverged shape, the run's own change.
func TestARunIncorporatesCommitsItsBranchHeldBeforeItBegan(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	requireStageListMatchesThePRD(t)

	for _, tc := range []struct {
		name string
		own  string
	}{
		{"diverged", "add a change beside the colleague's"},
		{"behind", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed, _ := walkOverUnincorporated(t, "colleague.md", tc.own)
			if err := incorporatedCheck(tc.own != "").Verify(observed); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}
}

// TestARunThatChangedAFileItsBranchChangedHoldsAtRebase is the overlapping
// shape of #396: the run's own commit and the colleague's change the same
// file. The rebase stage replays nothing and holds for a person, naming the
// commit it observed and the head it left in place, and the remote still
// stands at the colleague's commit.
func TestARunThatChangedAFileItsBranchChangedHoldsAtRebase(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)
	requireStageListMatchesThePRD(t)

	observed, run := walkOverUnincorporated(t, "shared.md", "change the paragraph the colleague also changed")
	held := overlapped{unincorporated: observed}
	if run.Decision != nil {
		held.stage = run.Decision.Stage
		held.findings = run.Decision.Findings
	}
	if err := overlapCheck().Verify(held); err != nil {
		t.Fatalf("%v", err)
	}
}

// walkOverUnincorporated lands a colleague's commit writing file on the
// remote's branch, commits a change of the run's own writing the same file
// with other content unless own is empty, starts a run and walks it as far as
// it goes, answering every hold before the push stage's but the rebase
// stage's.
func walkOverUnincorporated(t *testing.T, file, own string) (unincorporated, machine.Run) {
	t.Helper()
	subject := ownSubject(t, true)
	colleague := subject.scenario.Paths["colleague"]
	if err := writeInto(colleague, file, "A paragraph somebody else landed first.\n"); err != nil {
		t.Fatalf("%v", err)
	}
	subject.git(t, colleague, "add", file)
	subject.git(t, colleague, "commit", "--quiet", "-m", "land a paragraph somebody else wrote")
	subject.git(t, colleague, "push", "--quiet", "origin", subject.scenario.Branch)
	observed := unincorporated{colleague: subject.git(t, colleague, "rev-parse", "HEAD"), own: own}
	if got := subject.remoteBranch(t); got != observed.colleague {
		t.Fatalf("the colleague's push left the remote at %s rather than at %s", got, observed.colleague)
	}
	if own != "" {
		name := file
		if file == "colleague.md" {
			name = "mine.md"
		}
		subject.commit(t, name, "A paragraph of this run's own.\n", own)
	}

	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly"),
	}}})
	j := open(t, subject.scenario, func(o *journey.Options) { o.Dir = subject.work; o.AgentArguments = agent.Arguments() })
	const command = "true"
	if err := j.WriteConfiguration(map[string]any{"commands": map[string]any{"test": command, "lint": command}}); err != nil {
		t.Fatalf("writing the home's configuration: %v", err)
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)

	skipped := []pipeline.Stage{pipeline.StagePR, pipeline.StageCI}
	observed.expected = expectedThenPush(configuredStops(command, command, skipped))
	observed.expected = observed.expected[:len(observed.expected)-1]
	run := startRun(t, j, "--skip", pipeline.StagePR.String()+","+pipeline.StageCI.String(),
		"--intent", "deliver a change to a branch somebody else already pushed to")
	if run.Decision == nil || run.Decision.Stage != pipeline.StageRebase.String() {
		observed.stops, run = walkToPush(t, j, run, func(string) {})
	} else {
		observed.stops = []string{run.Decision.Stage}
	}
	for _, v := range run.Stages {
		if v.Report == nil {
			continue
		}
		switch v.Stage {
		case pipeline.StageLint.String():
			observed.forwarded = v.Report.Revision
		case pipeline.StagePush.String():
			observed.pushed = v.Report.Summary
		}
	}
	observed.after = subject.remoteBranch(t)
	observed.history = strings.Split(strings.TrimSpace(
		subject.git(t, subject.scenario.Origin, "log", "--format=%H %s", observed.after)), "\n")
	return observed, run
}

// incorporatedCheck is what the diverged and behind walks are held to, with
// the counterfeits that show each clause can fail. withOwn adds the clause
// that the run's own change reached the remote, which only the diverged
// shape has a change for.
func incorporatedCheck(withOwn bool) journey.Check[unincorporated] {
	check := journey.Check[unincorporated]{
		What: "P6: a run over a branch holding a commit it was not given",
		Clauses: []journey.Clause[unincorporated]{
			{
				States: "the run stopped only at the stages stages.Holding gives before the push, in order",
				Holds: func(u unincorporated) error {
					if !slices.Equal(u.stops, u.expected) {
						return fmt.Errorf("the run stopped at %v and was expected at %v", u.stops, u.expected)
					}
					return nil
				},
			},
			{
				States: "the push forwarded the head the lint stage checked, and the remote stands there",
				Holds: func(u unincorporated) error {
					if u.forwarded == "" || !strings.HasPrefix(u.pushed, "Forwarded "+u.forwarded+" ") {
						return fmt.Errorf("the push stage reported %q, and the lint stage checked %q", u.pushed, u.forwarded)
					}
					if u.after != u.forwarded {
						return fmt.Errorf("the remote stands at %s, not at the forwarded %s", u.after, u.forwarded)
					}
					return nil
				},
			},
			{
				States: "the push was a fast-forward anchored on the colleague's commit",
				Holds: func(u unincorporated) error {
					want := fmt.Sprintf(": %s, anchored on %s ", safety.KindFastForward, u.colleague)
					if !strings.Contains(u.pushed, want) {
						return fmt.Errorf("the push stage reported %q, which does not say %q", u.pushed, want)
					}
					return nil
				},
			},
			{
				States: "the remote's branch still reaches the colleague's commit",
				Holds: func(u unincorporated) error {
					if !u.reaches(u.colleague) {
						return fmt.Errorf("the remote's branch at %s no longer reaches the colleague's commit %s",
							u.after, u.colleague)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[unincorporated]{
			{Named: "the push held as it did before the rebase stage incorporated", Break: func(u unincorporated) unincorporated {
				u.stops = append(slices.Clone(u.stops), pipeline.StagePush.String())
				return u
			}},
			{Named: "the remote stands somewhere the push did not forward", Break: func(u unincorporated) unincorporated {
				u.after = u.colleague + "^"
				return u
			}},
			{Named: "the push was an anchored force", Break: func(u unincorporated) unincorporated {
				u.pushed = strings.Replace(u.pushed, string(safety.KindFastForward), string(safety.KindAnchoredForce), 1)
				return u
			}},
			{Named: "the push was anchored on the branch absent", Break: func(u unincorporated) unincorporated {
				u.pushed = strings.Replace(u.pushed, "anchored on "+u.colleague, "anchored on absent", 1)
				return u
			}},
			{Named: "the colleague's commit was dropped from the branch", Break: func(u unincorporated) unincorporated {
				return u.without(u.colleague)
			}},
		},
	}
	if withOwn {
		check.Clauses = append(check.Clauses, journey.Clause[unincorporated]{
			States: "the remote's branch reaches the run's own change",
			Holds: func(u unincorporated) error {
				if !u.reachesSubject(u.own) {
					return fmt.Errorf("the remote's branch at %s reaches no commit named %q", u.after, u.own)
				}
				return nil
			},
		})
		check.Counterfeits = append(check.Counterfeits, journey.Counterfeit[unincorporated]{
			Named: "the run's own change never reached the remote",
			Break: func(u unincorporated) unincorporated { return u.without(u.own) },
		})
	}
	return check
}

// overlapped is the overlapping walk's observation: where it held and with
// what, beside what the remote held afterwards.
type overlapped struct {
	unincorporated
	stage    string
	findings []findings.Finding
}

// overlap is the rebase stage's overlap finding, if it held with one.
func (c overlapped) overlap() (findings.Finding, error) {
	f, ok := heldWith(c.findings, "rebase-branch-overlap")
	if !ok {
		return f, fmt.Errorf("the run held at %q with %d finding(s) and none is rebase-branch-overlap",
			c.stage, len(c.findings))
	}
	return f, nil
}

// withOverlap returns a copy of c whose overlap finding is changed.
func (c overlapped) withOverlap(change func(*findings.Finding)) overlapped {
	c.findings = slices.Clone(c.findings)
	for i := range c.findings {
		if c.findings[i].ID == "rebase-branch-overlap" {
			change(&c.findings[i])
		}
	}
	return c
}

// overlapCheck is what the overlapping walk is held to.
func overlapCheck() journey.Check[overlapped] {
	return journey.Check[overlapped]{
		What: "P6: a run whose change overlaps its branch's commits",
		Clauses: []journey.Clause[overlapped]{
			{
				States: "the run's first hold is the rebase stage's, on an ask naming the overlap",
				Holds: func(c overlapped) error {
					if c.stage != pipeline.StageRebase.String() {
						return fmt.Errorf("the run first held at %q, want the rebase stage", c.stage)
					}
					f, err := c.overlap()
					if err != nil {
						return err
					}
					if f.Action != findings.ActionAsk {
						return fmt.Errorf("the overlap finding's action is %q, want an ask", f.Action)
					}
					return nil
				},
			},
			{
				States: "the hold names the colleague's commit as the one the branch was observed at",
				Holds: func(c overlapped) error {
					f, err := c.overlap()
					if err != nil {
						return err
					}
					if got := lineAfter(f.Description, "observed: "); got != c.colleague {
						return fmt.Errorf("the hold names %q as observed, and the branch stood at %s", got, c.colleague)
					}
					return nil
				},
			},
			{
				States:  "the remote's branch still stands at the colleague's commit",
				Absence: true,
				Possible: func(c overlapped) error {
					f, err := c.overlap()
					if err != nil {
						return err
					}
					if head := lineAfter(f.Description, "head: "); head == "" || head == c.colleague {
						return errors.New("the run names no head of its own beside the colleague's commit, " +
							"so nothing could have replaced it")
					}
					return nil
				},
				Holds: func(c overlapped) error {
					if c.after != c.colleague {
						return fmt.Errorf("the remote's branch stands at %s and no longer at the colleague's %s",
							c.after, c.colleague)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[overlapped]{
			{Named: "the run went past the rebase stage", Break: func(c overlapped) overlapped {
				c.stage = pipeline.StagePush.String()
				return c
			}},
			{Named: "the overlap was offered to a fixer", Break: func(c overlapped) overlapped {
				return c.withOverlap(func(f *findings.Finding) { f.Action = findings.ActionFix })
			}},
			{Named: "the hold named the run's head as observed", Break: func(c overlapped) overlapped {
				return c.withOverlap(func(f *findings.Finding) {
					f.Description = strings.Replace(f.Description, "observed: "+c.colleague,
						"observed: "+lineAfter(f.Description, "head: "), 1)
				})
			}},
			{Named: "the run's commit replaced the colleague's", Break: func(c overlapped) overlapped {
				c.after = c.colleague + "^"
				return c
			}},
		},
	}
}
