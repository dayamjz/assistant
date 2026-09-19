package journey_test

import (
	"errors"
	"fmt"
	"path/filepath"
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
// The binary reaches this now. internal/service reads the trusted copy at a
// freshly fetched default branch before a run's record moves to running, so
// the last subtest drives the refusal through the shipped surface: a run over
// a clone of the unparseable scenario is refused before launching anything,
// naming the trusted configuration. The in-process subtests stay, because the
// refusal's exact shape - the sentinel, the not-absent distinction, the
// condition's message - is held there, where a clause can read the error
// rather than a rendering of it.
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
				What:         "P7: " + string(planted.condition),
				Clauses:      clauses,
				Counterfeits: counterfeits,
			}
			if err := aborts.Verify(observed); err != nil {
				t.Fatalf("%v", err)
			}
		})
	}

	t.Run("a run over that repository is refused before launching", func(t *testing.T) {
		requiresIdentifiedPeer(t)

		// The refusal, driven through the binary: a clone of the scenario
		// whose default branch carries the document that will not parse, a
		// gate, a service, and one attempt to start a run. internal/service
		// reads the trusted copy at a freshly fetched default branch before
		// the run's record moves to running, so the surface answers with the
		// refusal rather than with a run.
		condition, err := journey.Condition("refusal-unparseable-trusted-config")
		if err != nil {
			t.Fatalf("%v", err)
		}
		from := scenarioNamed(t, fixture.ScenarioUnparseableTrustedConfig)
		path, err := journey.Clone(from, filepath.Join(t.TempDir(), "work"))
		if err != nil {
			t.Fatalf("cloning a working copy to run against: %v", err)
		}
		j := open(t, from, func(o *journey.Options) { o.Dir = path })
		succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
		serve(t, j)
		answer := j.Command("--intent", "a run whose default branch carries a document that will not parse")
		observed := resolvedRun{started: answer.Code == machine.ExitOK, message: answer.Message()}

		reads := journey.Check[resolvedRun]{
			What: "P7: a trusted document that will not parse stops the run",
			Clauses: []journey.Clause[resolvedRun]{
				{
					States: "the run was refused rather than started",
					Holds: func(r resolvedRun) error {
						if r.started {
							return errors.New("the run started against a trusted document nobody could " +
								"read, so it proceeded on guessed defaults")
						}
						return nil
					},
				},
				{
					States: "the refusal names the trusted configuration rather than some other failure",
					Holds: func(r resolvedRun) error {
						if !strings.Contains(r.message, "trusted configuration") {
							return fmt.Errorf("the refusal is about something else: %s", r.message)
						}
						return nil
					},
				},
				{
					States: "the refusal says what the condition requires it to say",
					Holds: func(r resolvedRun) error {
						if missing := journey.Carries(r.message, condition.Expect.MessageContains); len(missing) > 0 {
							return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, r.message)
						}
						return nil
					},
				},
			},
			Counterfeits: []journey.Counterfeit[resolvedRun]{
				{Named: "the run started anyway", Break: func(r resolvedRun) resolvedRun {
					r.started = true
					return r
				}},
				{Named: "the run stopped for a reason that has nothing to do with configuration",
					Break: func(r resolvedRun) resolvedRun {
						r.message = "the disk is full"
						return r
					}},
			},
		}
		if err := reads.Verify(observed); err != nil {
			t.Fatalf("%v", err)
		}
	})
}

// resolvedRun is what asking for a run over a repository whose trusted
// document cannot be parsed came to.
type resolvedRun struct {
	// started is whether the run was allowed to begin at all.
	started bool
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
