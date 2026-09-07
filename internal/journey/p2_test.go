package journey_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestAPassMeansTheSameThingEverywhere drives PRD principle P2 through the
// binary that ships.
//
// It answers section 13's three-part test for P2 in one scenario, because the
// three parts are one claim: a run walks the nine stages in the specified
// order, a person may skip stages for one run on purpose, and a configuration
// document that asks for a standing skip is refused before the service serves
// anything.
//
// The order itself is internal/pipeline's, which makes another order unsayable
// rather than checked, and internal/service renders it by iterating that order
// rather than by carrying one of its own. So no clause here asserts it: over a
// run's answer the order cannot come back wrong, and a clause that cannot fail
// is what this package refuses. What this establishes that the owner cannot is
// that the shipped binary reaches every one of those stages, runs each, and
// carries each away with the outcome it was given.
//
// The third part is refused by internal/config's key table rather than by a
// rule written against standing skips: the table admits no key named skip, so
// a document asking for one that way names a key the schema does not admit and
// the service stops before it binds a socket. Two facts are observable here
// and both are narrow - that the table carries no such name, and that a key it
// does not carry is refused at parse time - so a second key of another name is
// driven alongside and the check claims only those two. Whether some row the
// table does carry would apply a standing skip is a question about what a row
// means; internal/config's key table is its one owner, and nothing here can
// enumerate it.
func TestAPassMeansTheSameThingEverywhere(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P2)

	j := inClone(t)

	// Which stages a run holds at is derived rather than named, because a body
	// that lands moves it: a stage with one reports what it established and
	// never sees the answer this test gives, so a clause holding every stage
	// to that answer would fail for a reason that is not P2.
	held := map[string]bool{}
	for _, stage := range stagesWithoutABody(t) {
		held[stage.String()] = true
	}

	walked := journey.Check[machine.Run]{
		What: "a run through the binary comes back carrying a report for every stage of the gate, every " +
			"one of them ran, each stage this build has no body for carries the answer its hold was " +
			"given, none was skipped, and the run reached the end. The order they come back in is not " +
			"established here and no clause claims it: internal/service builds this list by iterating " +
			"internal/pipeline's fixed order and appending one view per stage, so another order is " +
			"unsayable on this wire and a clause over it could not fail. internal/pipeline owns that, " +
			"and its nine named fields are what make another order unsayable in the first place",
		Clauses: []journey.Clause[machine.Run]{
			{
				// The list's length is the one thing about its shape that is
				// not fixed by construction. internal/service answers a run
				// that never began executing before it builds any views at
				// all, so an empty list is a shape this surface really does
				// produce, and over one every clause below passes having
				// looked at nothing.
				States: "the answer carries a report for every stage the gate has",
				Holds: func(run machine.Run) error {
					if got, want := len(run.Stages), len(stageOrder(t)); got != want {
						return fmt.Errorf("the run reported %d stage report(s) and the gate has %d stages, "+
							"so the clauses below have nothing to be about", got, want)
					}
					return nil
				},
			},
			{
				States: "every stage of a run that skipped nothing ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if !stage.Ran {
							return fmt.Errorf("the %s stage did not run, and this run skipped nothing", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				// Every hold in this run was answered with the same option, so
				// every stage that held has to carry it. A stage reporting
				// another outcome was answered by something other than the
				// answer this test gave, which internal/machine says a stage's
				// Ran flag alone cannot tell apart from being skipped past at
				// its hold.
				States: "every stage this build has no body for carries the outcome this run's holds " +
					"were answered with",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if !held[stage.Stage] {
							continue
						}
						if stage.Outcome != pipeline.OutcomeApproved {
							return fmt.Errorf("the %s stage has no body in this build and came back %q, "+
								"and every hold in this run was approved", stage.Stage, stage.Outcome)
						}
					}
					return nil
				},
			},
			{
				// The stages with a body report what their bodies established,
				// which this cannot state in advance. What it can state is the
				// one outcome no stage of this run may carry, which is the
				// shape the clause above catches for the stages it covers.
				States: "no stage of a run that skipped nothing came back skipped",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if stage.Outcome == pipeline.OutcomeSkipped {
							return fmt.Errorf("the %s stage came back %q, and this run skipped nothing",
								stage.Stage, stage.Outcome)
						}
					}
					return nil
				},
			},
			{
				States: "the run reached the end of the gate",
				Holds: func(run machine.Run) error {
					if run.Outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s rather than reaching the end of the gate", run.Outcome)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[machine.Run]{
			// Reordering the list, dropping one entry from it and appending a
			// tenth were counterfeits here until review found that this
			// surface can produce none of the three, so each showed a clause
			// failing against a failure the mechanism cannot reach. What it
			// can produce is no list at all, which is what a run that never
			// began executing is answered with.
			{Named: "the run came back carrying no stage reports at all", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages = nil
				return run
			}},
			{Named: "a stage nobody skipped did not run", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Stages[4].Ran = false
				return run
			}},
			// The stage this one changes is found rather than counted to,
			// because the clause it has to reach covers the stages this build
			// has no body for and which those are moves as bodies land.
			{Named: "a stage came back carrying an outcome nobody gave it", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if held[stage.Stage] {
						run.Stages[i].Outcome = pipeline.OutcomeSkipped
						break
					}
				}
				return run
			}},
			{Named: "the run stopped without reaching the end", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				run.Outcome = machine.OutcomeFailed
				return run
			}},
		},
	}
	whole := last(answerHolds(t, j, startRun(t, j, "--intent", "narrow the Total loop bound on purpose"), "approved"))
	if err := walked.Verify(whole); err != nil {
		t.Fatalf("%v\n\nthe run stands at %q, outcome %s, over stages %v",
			err, whole.Position, whole.Outcome, stageNames(whole))
	}

	asked := []string{pipeline.StageReview.String(), pipeline.StageLint.String()}
	skipped := journey.Check[machine.Run]{
		What: "a per-run skip takes exactly the stages it named out of that one run, and no others",
		Clauses: []journey.Clause[machine.Run]{
			{
				// The same clause the check above carries, for the same
				// reason: internal/service answers a run it has no checkpoint
				// for before it builds any stage view, and a run recorded
				// passed comes back reporting that whether or not it has one.
				// Over an empty list every clause below passes having looked
				// at nothing, and this check would report a per-run skip it
				// never saw.
				States: "the answer carries a report for every stage the gate has",
				Holds: func(run machine.Run) error {
					if got, want := len(run.Stages), len(stageOrder(t)); got != want {
						return fmt.Errorf("the run reported %d stage report(s) and the gate has %d stages, "+
							"so the clauses below have nothing to be about", got, want)
					}
					return nil
				},
			},
			{
				States: "no stage this run was told to skip ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if slices.Contains(asked, stage.Stage) && stage.Ran {
							return fmt.Errorf("the %s stage was skipped for this run and ran anyway", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				States: "every stage this run was told to skip came back reported as skipped",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if slices.Contains(asked, stage.Stage) && stage.Outcome != pipeline.OutcomeSkipped {
							return fmt.Errorf("the %s stage was skipped for this run and came back %q",
								stage.Stage, stage.Outcome)
						}
					}
					return nil
				},
			},
			{
				States: "every stage nobody asked to skip ran",
				Holds: func(run machine.Run) error {
					for _, stage := range run.Stages {
						if !slices.Contains(asked, stage.Stage) && !stage.Ran {
							return fmt.Errorf("the %s stage was not skipped and did not run", stage.Stage)
						}
					}
					return nil
				},
			},
			{
				States: "the run reached the end of the gate",
				Holds: func(run machine.Run) error {
					if run.Outcome != machine.OutcomeChecksPassed {
						return fmt.Errorf("the run ended %s rather than reaching the end of the gate", run.Outcome)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[machine.Run]{
			{Named: "the run carrying the skip came back with no stage reports at all",
				Break: func(run machine.Run) machine.Run {
					run = cloneRun(run)
					run.Stages = nil
					return run
				}},
			{Named: "a stage the run skipped ran anyway", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageReview.String() {
						run.Stages[i].Ran = true
					}
				}
				return run
			}},
			{Named: "a stage nobody asked to skip was skipped too", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageTest.String() {
						run.Stages[i].Ran = false
						run.Stages[i].Outcome = pipeline.OutcomeSkipped
					}
				}
				return run
			}},
			{Named: "a skipped stage came back reporting it had passed", Break: func(run machine.Run) machine.Run {
				run = cloneRun(run)
				for i, stage := range run.Stages {
					if stage.Stage == pipeline.StageLint.String() {
						run.Stages[i].Outcome = pipeline.OutcomeApproved
					}
				}
				return run
			}},
			{Named: "the run carrying the skip stopped without reaching the end",
				Break: func(run machine.Run) machine.Run {
					run = cloneRun(run)
					run.Outcome = machine.OutcomeFailed
					return run
				}},
		},
	}
	second := last(answerHolds(t, j,
		startRun(t, j, "--intent", "the same change, checked without two stages", "--skip", "review,lint"),
		"approved"))
	if err := skipped.Verify(second); err != nil {
		t.Fatalf("%v", err)
	}

	// The standing skip is asked for the way an operator would ask for one, in
	// the home's own configuration document, and the service is then started
	// in the foreground so that its refusal is this command's answer rather
	// than a line in a log of a process nobody is holding.
	//
	// A second document is offered alongside it, differing only in what its
	// key is called. What refuses both is the same thing, and naming it is the
	// point: internal/config's key table is a closed set with no row a
	// standing skip could be asked for through, so the refusal answers a key
	// outside the table rather than this one word.
	if err := j.Kill(); err != nil {
		t.Fatalf("ending the service before changing its configuration: %v", err)
	}
	offered := standingSkip{}
	for _, key := range []string{standingSkipKey, unadmittedKeyOfAnotherName} {
		if err := j.WriteConfiguration(map[string]any{
			key: []string{pipeline.StageReview.String()},
		}); err != nil {
			t.Fatalf("writing a configuration naming %s: %v", key, err)
		}
		offered.attempts = append(offered.attempts, unserved{
			key:    key,
			answer: startsService(t, j, standingSkipBound, "service", "start", "--foreground"),
		})
	}
	for _, key := range config.Keys() {
		offered.schemaKeys = append(offered.schemaKeys, string(key))
	}

	standing := journey.Check[standingSkip]{
		What: "internal/config's key table admits no key named skip, and the service refuses to serve " +
			"over a document naming a key that table does not admit, naming the key it refused; it " +
			"answers a key of another name the same way, so the refusal is not read off one word. " +
			"Whether some row the table does carry would apply a standing skip is what a row means " +
			"rather than what its name is, and that table is its one owner",
		Clauses: []journey.Clause[standingSkip]{
			{
				States: "the key table admits no key named skip",
				Holds: func(s standingSkip) error {
					if slices.Contains(s.schemaKeys, standingSkipKey) {
						return fmt.Errorf("the schema now admits %q, so a document can name it and this no "+
							"longer establishes that a document asking for a standing skip that way is "+
							"refused; what that row does is internal/config's to say", standingSkipKey)
					}
					return nil
				},
			},
			{
				States: "the second key offered is one the table does not admit either",
				Holds: func(s standingSkip) error {
					if slices.Contains(s.schemaKeys, unadmittedKeyOfAnotherName) {
						return fmt.Errorf("the schema now admits %q, so offering it establishes nothing "+
							"about a key outside the table", unadmittedKeyOfAnotherName)
					}
					return nil
				},
			},
			{
				States: "both documents were offered, so what follows is not answered over one key alone",
				Holds: func(s standingSkip) error {
					if len(s.attempts) < 2 {
						return fmt.Errorf("%d document(s) were offered, and a refusal seen against one key "+
							"says nothing about whether that key's name is what produced it", len(s.attempts))
					}
					return nil
				},
			},
			{
				// The command is bounded rather than left to run, because the
				// failure this exists to catch is the service accepting the
				// document and serving. Unbounded, that failure never returns
				// and the suite hangs to the test timeout instead of reporting
				// it, so the one check here whose whole subject is a refusal
				// would be the one that cannot fail.
				States: "every document offered ended the command on its own rather than serving until " +
					"this harness stopped it",
				Holds: func(s standingSkip) error {
					return eachOffered(s, func(key string, answer journey.Answer) error {
						if answer.Ended {
							return fmt.Errorf("the service was still serving over the document naming %q "+
								"when this harness stopped it: %s", key, answer)
						}
						return nil
					})
				},
			},
			{
				States: "every document offered was refused rather than exiting successfully",
				Holds: func(s standingSkip) error {
					return eachOffered(s, func(key string, answer journey.Answer) error {
						if answer.Code == machine.ExitOK {
							return fmt.Errorf("the service accepted the document naming %q: %s", key, answer)
						}
						return nil
					})
				},
			},
			{
				States: "every refusal was reported as a document a driving agent can read",
				Holds: func(s standingSkip) error {
					return eachOffered(s, func(key string, answer journey.Answer) error {
						if _, ok := answer.Failure(); !ok {
							return fmt.Errorf("the refusal of the document naming %q was not reported as "+
								"a document: %s", key, answer)
						}
						return nil
					})
				},
			},
			{
				States: "every refusal names the key it refused",
				Holds: func(s standingSkip) error {
					return eachOffered(s, func(key string, answer journey.Answer) error {
						failure, ok := answer.Failure()
						if !ok {
							return fmt.Errorf("the refusal of the document naming %q was not reported as "+
								"a document: %s", key, answer)
						}
						if !strings.Contains(failure.Error, key) {
							return fmt.Errorf("the refusal does not name the key it refused, %q: %q",
								key, failure.Error)
						}
						return nil
					})
				},
			},
		},
		Counterfeits: []journey.Counterfeit[standingSkip]{
			{Named: "the key table gained a key a standing skip could be asked for through",
				Break: func(s standingSkip) standingSkip {
					s.schemaKeys = append(slices.Clone(s.schemaKeys), standingSkipKey)
					return s
				}},
			{Named: "the key table gained the second key, so offering it shows nothing about the table",
				Break: func(s standingSkip) standingSkip {
					s.schemaKeys = append(slices.Clone(s.schemaKeys), unadmittedKeyOfAnotherName)
					return s
				}},
			{Named: "only the document asking for a standing skip was offered",
				Break: func(s standingSkip) standingSkip {
					s.attempts = slices.Clone(s.attempts)[:1]
					return s
				}},
			{Named: "the service served over one of the documents and had to be stopped",
				Break: func(s standingSkip) standingSkip {
					s.attempts = slices.Clone(s.attempts)
					s.attempts[0].answer.Ended = true
					return s
				}},
			{Named: "the service started over one of the documents", Break: func(s standingSkip) standingSkip {
				s.attempts = slices.Clone(s.attempts)
				s.attempts[1].answer.Code = machine.ExitOK
				return s
			}},
			{Named: "a refusal reached standard error alone, where a driving agent would not find it",
				Break: func(s standingSkip) standingSkip {
					s.attempts = slices.Clone(s.attempts)
					s.attempts[0].answer.Stdout = ""
					return s
				}},
			{Named: "a refusal never says which key it refused", Break: func(s standingSkip) standingSkip {
				s.attempts = slices.Clone(s.attempts)
				s.attempts[1].answer.Stdout = `{"error":"something went wrong","code":"internal"}`
				return s
			}},
		},
	}
	if err := standing.Verify(offered); err != nil {
		t.Fatalf("%v", err)
	}
}

// standingSkipKey is the name a configuration document asking for a standing
// skip would use.
//
// PRD section 2 has such a document stop the service before it serves, and
// what stops it is internal/config's key table having no row by this name
// rather than a rule written against standing skips: config.Keys is the one
// owner of what a document may say, and a key it does not list is refused
// where the document is walked. That a row of some other name does not apply a
// standing skip is not a fact about names and is not checked here.
const standingSkipKey = "skip"

// unadmittedKeyOfAnotherName is a second key that table does not admit,
// sharing no part of its name with standingSkipKey.
//
// It is offered beside the standing skip so that the refusal is held to a key
// outside the table rather than to one word. Without it, a check would pass
// over a document that happens to spell its key "skip" and would say nothing
// about what a document asking for a standing skip runs into.
const unadmittedKeyOfAnotherName = "parallelism"

// unserved is one configuration document the service was offered, and what
// starting it in the foreground over that document produced.
type unserved struct {
	// key is the key the document named, which is the only thing that differs
	// between the documents offered.
	key string
	// answer is what the command produced.
	answer journey.Answer
}

// standingSkip is what the service did with the documents it cannot accept,
// together with the schema that decides which those are.
type standingSkip struct {
	// attempts are the documents offered, the one asking for a standing skip
	// first.
	attempts []unserved
	// schemaKeys is every key internal/config's table admits, asked of that
	// package rather than restated here: a key added there is a key a document
	// could then name, and this is what notices.
	schemaKeys []string
}

// eachOffered answers a question about every document offered and reports the
// first it fails on, naming the key that document carried.
func eachOffered(s standingSkip, ask func(key string, answer journey.Answer) error) error {
	for _, attempt := range s.attempts {
		if err := ask(attempt.key, attempt.answer); err != nil {
			return err
		}
	}
	return nil
}

// standingSkipBound is how long a service refusing a document it cannot accept
// is given to answer before this harness stops it and reports that it served.
//
// It is generous for a process that reads one configuration document and
// refuses, and it is what turns the regression the standing-skip check exists
// for into an assertion failure rather than a suite that hangs.
const standingSkipBound = 15 * time.Second

// stageOrder is the stage names PRD section 5 names, in its order, spelled the
// way internal/pipeline spells them.
//
// It comes from the PRD rather than from pipeline.Order() so that a check
// comparing against it fails when the product's table and the specification
// disagree, instead of agreeing with whatever the product says. journey's own
// mapping is what carries the two spellings the PRD and the code differ on, and
// requireStageListMatchesThePRD is what holds the two lists together.
func stageOrder(t *testing.T) []string {
	t.Helper()
	requireStageListMatchesThePRD(t)
	order := pipeline.Order()
	names := make([]string, 0, len(order))
	for _, stage := range order {
		names = append(names, stage.String())
	}
	return names
}
