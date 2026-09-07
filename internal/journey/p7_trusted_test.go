package journey_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/vcs"
)

// trusted is what happened when the configuration document on the default
// branch was read and parsed, and what the same read found on the branch under
// validation.
type trusted struct {
	// condition names the planted condition.
	condition fixture.ID
	// failed is whether reading or parsing the default branch's document
	// failed at all.
	failed bool
	// message is what it failed with.
	message string
	// sentinel is whether the failure matches the error the condition names a
	// caller should match.
	sentinel bool
	// mistakenForAbsent is whether the failure reads as the path simply not
	// being there, which is the one answer that would let a caller fall back
	// to defaults believing the repository set nothing.
	mistakenForAbsent bool
	// branchNamesAnotherAgent is whether the branch's own copy of the same
	// document names an agent other than the default branch's, which is what
	// makes which layer was read observable from the outcome rather than
	// inferred.
	branchNamesAnotherAgent bool
	// branchAgentRejected is whether resolving the branch's own copy dropped
	// the agent it named, and branchAgent is what it resolved to instead.
	branchAgentRejected bool
	branchAgent         []string
}

// TestATrustedConfigurationThatCannotBeReadIsNotFallenBackFrom drives the
// second of PRD section 13's tests for principle P7 at package reach.
//
// The section asks for two cases to be distinguished: an unreadable or
// unparseable trusted configuration aborts before any agent starts, and a
// readable tree with no configuration file proceeds with defaults. The two
// failures internal/fixture plants are of different shapes on purpose - one
// document that will not parse, and one path that is a directory and so cannot
// be read as a document at all - and neither may be mistaken for the absence
// that permits defaults.
//
// The binary cannot reach this and nothing in this build can. internal/service
// states it: the trusted layer is read from the default branch at a freshly
// fetched commit and nothing here fetches, so a run resolves the operator's
// own layer and the schema defaults and never reads a repository's document at
// all. That is a gap against PRD section 10 rather than a hole in P7, since no
// branch's configuration is read either, and the last subtest observes it on a
// run rather than taking internal/service's word for it.
func TestATrustedConfigurationThatCannotBeReadIsNotFallenBackFrom(t *testing.T) {
	principles.Cite(t, principles.P7)

	for _, planted := range []struct {
		scenario  fixture.ScenarioName
		condition fixture.ID
		// notAbsent is the error a caller would match if the failure were
		// merely a missing path, which neither of these may be.
		absent error
	}{
		{fixture.ScenarioUnparseableTrustedConfig, "refusal-unparseable-trusted-config", vcs.ErrPathNotFound},
		{fixture.ScenarioUnreadableTrustedConfig, "refusal-unreadable-trusted-config", vcs.ErrPathNotFound},
	} {
		t.Run(string(planted.condition), func(t *testing.T) {
			scenario := scenarioNamed(t, planted.scenario)
			condition, err := journey.Condition(planted.condition)
			if err != nil {
				t.Fatalf("%v", err)
			}
			observed := trusted{condition: planted.condition}

			repository, err := vcs.OpenWorktree(t.Context(), scenario.WorkingCopy, vcs.WithRedactor(redact.New()))
			if err != nil {
				t.Fatalf("opening the subject working copy: %v", err)
			}
			path := subject(t).ConfigPath

			// The trusted commit is resolved by reading the remote, which is
			// where PRD section 10 puts it, rather than from whatever this
			// working copy last checked out.
			body, err := repository.FileAt(t.Context(), remoteDefaultBranch(t, scenario, repository), path)
			switch {
			case err != nil:
				observed.failed = true
				observed.message = err.Error()
				observed.mistakenForAbsent = errors.Is(err, planted.absent)
			default:
				_, err = config.Parse(config.OriginTrusted, body)
				observed.failed = err != nil
				if err != nil {
					observed.message = err.Error()
					observed.sentinel = errors.Is(err, config.ErrMalformed)
				}
			}

			// The same path on the branch under validation, which reads and
			// parses and names another agent. Without it a run that read the
			// pushed copy as trusted would fail for the same reason and which
			// layer was read would not be observable from the outcome.
			branch, err := repository.FileAt(t.Context(), scenario.Commits["branch-head"], path)
			if err != nil {
				t.Fatalf("reading %s from the branch under validation: %v", path, err)
			}
			layer, err := config.Parse(config.OriginPushed, branch)
			if err != nil {
				t.Fatalf("the branch's own copy has to parse, and did not: %v", err)
			}
			resolved, err := config.Resolve(config.Absent(config.OriginGlobal), layer)
			if err != nil {
				t.Fatalf("resolving the branch's own copy: %v", err)
			}
			observed.branchNamesAnotherAgent = strings.Contains(string(branch), pushedAgentName)
			observed.branchAgent = resolved.Config.Agent
			for _, rejection := range resolved.Rejected {
				if rejection.Key == config.KeyAgent {
					observed.branchAgentRejected = true
				}
			}

			clauses := []journey.Clause[trusted]{
				{
					States: "reading or parsing the trusted document failed",
					Holds: func(tr trusted) error {
						if !tr.failed {
							return errors.New("the trusted document was read and parsed, so nothing here " +
								"stops a run from proceeding on what it said")
						}
						return nil
					},
				},
				{
					States: "the failure is not one a caller could read as the path simply being absent",
					Holds: func(tr trusted) error {
						if tr.mistakenForAbsent {
							return errors.New("the failure reads as the path not being there, which is the " +
								"one answer that lets a caller fall back to defaults believing the " +
								"repository set nothing")
						}
						return nil
					},
				},
				{
					States: "the failure says what the condition requires it to say",
					Holds: func(tr trusted) error {
						if missing := journey.Carries(tr.message, condition.Expect.MessageContains); len(missing) > 0 {
							return fmt.Errorf("the failure does not say %q; it said:\n%s", missing, tr.message)
						}
						return nil
					},
				},
				{
					States: "the branch's own copy names an agent of its own, so which layer was read is visible",
					Holds: func(tr trusted) error {
						if !tr.branchNamesAnotherAgent {
							return errors.New("the branch's own copy names no agent of its own, so a run that " +
								"read it as trusted would be indistinguishable from one that read the " +
								"default branch's")
						}
						return nil
					},
				},
				{
					States: "resolving the branch's own copy dropped the agent it named",
					Holds: func(tr trusted) error {
						if !tr.branchAgentRejected {
							return errors.New("resolving the branch's own copy kept the agent it named, and " +
								"an agent is a key a pushed branch may not set")
						}
						return nil
					},
				},
				{
					States: "the branch's own copy did not resolve to the agent it named",
					Holds: func(tr trusted) error {
						if slices.Contains(tr.branchAgent, pushedAgentName) {
							return fmt.Errorf("the branch's own copy resolved to the agent it named, %v", tr.branchAgent)
						}
						return nil
					},
				},
			}
			counterfeits := []journey.Counterfeit[trusted]{
				{Named: "the trusted document read and parsed after all", Break: func(tr trusted) trusted {
					tr.failed = false
					return tr
				}},
				{Named: "the failure was the path simply being absent, which permits defaults",
					Break: func(tr trusted) trusted {
						tr.mistakenForAbsent = true
						return tr
					}},
				{Named: "the failure says none of what the condition requires",
					Break: func(tr trusted) trusted {
						tr.message = "something went wrong"
						tr.sentinel = false
						return tr
					}},
				{Named: "the branch's own copy names no agent, so which layer was read is invisible",
					Break: func(tr trusted) trusted {
						tr.branchNamesAnotherAgent = false
						return tr
					}},
				{Named: "the agent the branch named was applied rather than dropped",
					Break: func(tr trusted) trusted {
						tr.branchAgentRejected = false
						tr.branchAgent = []string{pushedAgentName}
						return tr
					}},
			}
			// Only one of the two conditions records an error a caller matches
			// on. Asked of the other, this clause would hold over an
			// observation nothing could make it report, and the counterfeit
			// that clears the flag would reach nothing.
			if condition.Expect.Sentinel != "" {
				clauses = append(clauses, journey.Clause[trusted]{
					States: "the failure matches the error the condition says a caller matches on",
					Holds: func(tr trusted) error {
						if !tr.sentinel {
							return fmt.Errorf("the failure does not match %s, which the condition says a "+
								"caller matches on", condition.Expect.Sentinel)
						}
						return nil
					},
				})
			}
			aborts := journey.Check[trusted]{
				What: string(planted.condition) + ": reading or parsing the default branch's own " +
					"configuration document failed, the failure is not one a caller could read as the " +
					"path simply being absent, and it says what the condition records it has to say; " +
					"the branch's own copy names an agent of its own and resolving that copy drops it, " +
					"so which layer a reader was handed is visible in the outcome. What the run then " +
					"does with the failure is not established here, because nothing in this build reads " +
					"a repository's document at all",
				Clauses:      clauses,
				Counterfeits: counterfeits,
			}
			if err := aborts.Verify(observed); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}

	t.Run("what a run actually resolves", func(t *testing.T) {
		// The gap, observed rather than taken from a document. The subject
		// here is a clone of the scenario whose default branch carries a
		// document that will not parse, and PRD section 10 has a run over it
		// stop before launching anything.
		//
		// What this cannot observe is execution. That scenario plants no
		// executable at all - a malformed document, a paragraph of prose, and
		// a well-formed document on the branch - so its tripwire file could
		// not be written whatever the product did, and a clause reading it
		// would hold over a world nothing could have made it report in. No
		// test here establishes it over a branch that does plant executables
		// either: every one of those is reached only through a stage body and
		// this build has none, which
		// TestTheBranchUnderValidationChoosesNothingThatRuns says in its own
		// terms.
		j := inClone(t)
		answer := j.Command("--intent", "a run whose default branch carries a document that will not parse")
		observed := resolvedRun{started: answer.Code == machine.ExitOK, message: answer.Message()}
		if observed.started {
			observed.outcome = decodeRun(t, answer).Outcome
		}

		reads := journey.Check[resolvedRun]{
			What: "a run whose default branch carries a trusted document that will not parse either stops " +
				"before launching anything, or starts and reports an outcome; which document it read, " +
				"and which agent it resolved, are not established here, because no shipped surface " +
				"reports either and this subject plants nothing whose execution could stand in for " +
				"them. Nothing else here establishes it either: the branch-installation family's " +
				"planted executables are reached only through a stage body and this build has none",
			Clauses: []journey.Clause[resolvedRun]{
				{
					States: "a run that did not start stopped for the configuration",
					Holds: func(r resolvedRun) error {
						if !r.started && !strings.Contains(r.message, "config") {
							return fmt.Errorf("the run stopped for a reason that is not the configuration: %s", r.message)
						}
						return nil
					},
				},
				{
					States: "a run that started reported an outcome",
					Holds: func(r resolvedRun) error {
						if r.started && r.outcome == "" {
							return errors.New("the run started and reported no outcome, so this observed neither " +
								"the refusal nor the gap")
						}
						return nil
					},
				},
			},
			Counterfeits: []journey.Counterfeit[resolvedRun]{
				{Named: "the run stopped for a reason that has nothing to do with configuration",
					Break: func(r resolvedRun) resolvedRun {
						r.started = false
						r.message = "the disk is full"
						return r
					}},
				{Named: "the run started and answered nothing at all", Break: func(r resolvedRun) resolvedRun {
					r.started = true
					r.outcome = ""
					return r
				}},
			},
		}
		if err := reads.Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
		if observed.started {
			t.Logf("KNOWN GAP: a run started (%s) against a repository whose default branch carries a "+
				"trusted configuration document that does not parse. PRD section 10 has a run stop "+
				"before launching anything when the trusted copy cannot be read and parsed. Nothing in "+
				"this build reads a repository's own document from anywhere, which internal/service "+
				"states, so this is a gap against section 10 rather than a hole in P7. That nothing a "+
				"branch names is executed is established nowhere in this build: every planted "+
				"executable of that family is reached only through a stage body, and internal/stages "+
				"holds none.", observed.outcome)
		}
	})
}

// resolvedRun is what a run over a repository whose trusted document cannot be
// parsed came to.
type resolvedRun struct {
	// started is whether the run was allowed to begin at all.
	started bool
	// outcome is where it stopped, empty when it never started.
	outcome machine.Outcome
	// message is what the surface said about it.
	message string
}

// remoteDefaultBranch is the commit the default branch stands at on the
// scenario's origin, read fresh off the remote rather than out of whatever
// this working copy last checked out.
func remoteDefaultBranch(t *testing.T, scenario fixture.Scenario, repository *vcs.Repository) string {
	t.Helper()
	refs, err := repository.RemoteRefs(t.Context(), scenario.Origin, "refs/heads/"+fixture.DefaultBranch)
	if err != nil {
		t.Fatalf("reading %s on the origin of %s: %v", fixture.DefaultBranch, scenario.Name, err)
	}
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+fixture.DefaultBranch {
			return ref.Object
		}
	}
	t.Fatalf("the origin of %s advertises no %s", scenario.Name, fixture.DefaultBranch)
	return ""
}
