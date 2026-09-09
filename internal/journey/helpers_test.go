package journey_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// platformIdentifiesPeers reports whether internal/ipc has a read for local
// socket peer credentials here, written down on the same terms that package's
// own tests and internal/cli's write it down.
func platformIdentifiesPeers() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// requiresIdentifiedPeer skips a test that drives a run through the service.
//
// Identification is authority in internal/ipc, so on a platform with no read
// for it every method that starts, answers, cancels or reruns a run is refused
// and a check driving one would fail for the platform rather than for the
// product. The skip is itself a claim, and the claim is narrow: what is
// established on such a platform is only the checks that drive no run.
// README.md's limits table and the Coverage note of every principle whose
// binary reach passes through here both say so, because a skipped check that
// reads as a pass is the same defect as a clause that cannot fail.
func requiresIdentifiedPeer(t *testing.T) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s reports no local socket peer credentials, so every method that drives a run is "+
			"refused and this check establishes nothing here", runtime.GOOS)
	}
}

// requiresLocalSocket skips a check whose service did not come up, on a
// platform that has no local socket transport to serve this protocol over.
//
// It is the sibling packages' second guard on their terms, and their terms are
// the whole point: internal/service and internal/cli both take the cause and
// skip only where a service actually failed, bounded by the same written-down
// platform predicate, so a failure anywhere the transport does exist stays a
// failure. Skipping on what happened rather than up front is what keeps this
// from resting on a platform fact nothing here checks. Go offers a local
// socket on more platforms than internal/ipc can identify a peer on, and
// whether one binds here is answered by trying.
func requiresLocalSocket(t *testing.T, cause error) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, cause)
	}
}

// stagesWithoutABody is the stages this harness DECLARES this build has no
// implementation for, in the order a run takes them.
//
// It is a declaration read back, never a measurement. Subtracting what the
// build implements from the stage list is what let this harness quietly become
// an eight-boundary harness mid-run and report success: a harness that derives
// its expectations from the build can only fail when the build disagrees with
// itself. journey.StagesWithoutABody is the named list, and
// journey.DeclaresEveryStage goes red when those names and the build stop
// accounting for each other in either direction, so a body that lands or
// disappears is a change somebody has to write down here.
//
// requireStageListMatchesThePRD is asked first, so a check resting on this is
// resting on a stage list the PRD owns rather than one the product supplied.
func stagesWithoutABody(t *testing.T) []pipeline.Stage {
	t.Helper()
	requireStageListMatchesThePRD(t)
	pending := journey.StagesWithoutABody()
	if len(pending) < 2 {
		t.Fatalf("this harness declares %d stage(s) without a body, so a run no longer walks from one "+
			"hold to the next; the checks resting on this need rewriting against whatever now holds a run",
			len(pending))
	}
	return pending
}

// stagesARunStopsAt is the declared list of stages a run this harness drives
// stops at, behind the same PRD gate as stagesWithoutABody and refused on the
// same terms when it thins below two, because the checks resting on it walk a
// run from one hold to the next. journey.StagesARunStopsAt says why the list
// is its own declaration rather than the body-less one under another name.
func stagesARunStopsAt(t *testing.T) []pipeline.Stage {
	t.Helper()
	requireStageListMatchesThePRD(t)
	holding := journey.StagesARunStopsAt()
	if len(holding) < 2 {
		t.Fatalf("this harness declares %d stage(s) a run stops at, so a run no longer walks from one "+
			"hold to the next; the checks resting on this need rewriting against whatever now holds a run",
			len(holding))
	}
	return holding
}

// requireStageListMatchesThePRD fails the test unless PRD section 5's stage
// table, internal/pipeline's order and this harness's body-less declaration all
// account for each other.
//
// Every check that names a stage rests on this, so it is asked wherever such a
// check begins rather than once in a test of its own that a filtered run might
// skip.
func requireStageListMatchesThePRD(t *testing.T) {
	t.Helper()
	prd, err := journey.PRDStages(moduleRoot(t))
	if err != nil {
		t.Fatalf("reading the stage list out of the PRD: %v", err)
	}
	if err := journey.AgreesWithPRD(prd, pipeline.Order()); err != nil {
		t.Fatalf("the product's stage order and the PRD's list disagree: %v", err)
	}
	if err := journey.DeclaresEveryStage(journey.Implemented(), journey.StagesWithoutABody()); err != nil {
		t.Fatalf("%v", err)
	}
}

// claim takes a scenario for this test's exclusive use. A second test wanting
// the same one is refused here rather than interfering in a way that reads as
// flakiness.
func claim(t *testing.T, name fixture.ScenarioName) fixture.Scenario {
	t.Helper()
	scenario, err := journey.Claim(name, t.Name())
	if err != nil {
		t.Fatalf("claiming the %s scenario: %v", name, err)
	}
	return scenario
}

// open starts a journey against a scenario, with a stand-in agent the run
// resolves off its own PATH, and closes it when the test ends.
func open(t *testing.T, scenario fixture.Scenario, opts ...func(*journey.Options)) *journey.Journey {
	t.Helper()
	agent := standin.New(t, standin.Script{})
	options := journey.Options{Scenario: scenario, AgentArguments: agent.Arguments()}
	for _, opt := range opts {
		opt(&options)
	}
	j, err := journey.Open(options)
	if err != nil {
		t.Fatalf("opening a journey against %s: %v", scenario.Name, err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Errorf("closing the journey: %v", err)
		}
	})
	return j
}

// serving starts the service in a process this test owns, so it can be killed
// the way a crash kills it, and reports what starting it came to.
//
// It takes no platform guard, and that is the difference between it and serve.
// The guard answers a service that was expected to come up and did not, so it
// belongs to a caller that expects one and has not already taken it. No caller
// reaching for this is that: the harness checks whose whole subject is a
// service they arranged to fail expect the failure, and the P6 boundary walk
// re-serves a home whose first service came up through serve, which took the
// guard before the walk began. A guard here would fire on an arranged failure
// and skip the check for the failure it exists to observe, which is what it
// did until the day a platform without the transport ran it.
//
// It and startsService are the two ways a service is started here, because a
// service this harness owns as a child and a service the command surface
// starts are different things: only the first can be killed at a stage
// boundary, only the second answers as a document, and only the first hands
// back an error a caller can condition on.
//
// The rule they exist for is that no test starts a service another way, and
// the residual gap is that nothing enforces it. Go cannot close the door on an
// exported method, and a test asserting it by reading this package's source
// would prove a pattern rather than a behaviour, which is the shape this
// repository rejects. So it is a rule a reader keeps, and what makes it
// keepable is that every answer a caller could want is here: serve for a
// service that has to come up, this for a test whose subject is one that did
// not and for one serving a home again after a kill, and startsService for one
// asked for over the surface.
func serving(t *testing.T, j *journey.Journey) error {
	t.Helper()
	return j.Serve()
}

// serve starts the service and fails the test unless it came up.
//
// This is where the platform guard is taken, on the failure the sibling
// packages take it on: a service that had to come up and did not, which is the
// one failure a missing transport can be read off. That keeps a check from
// failing for the transport rather than for the product, and it keeps the skip
// off every other way a service can fail to start.
func serve(t *testing.T, j *journey.Journey) {
	t.Helper()
	if err := serving(t, j); err != nil {
		requiresLocalSocket(t, err)
		t.Fatalf("serving: %v", err)
	}
}

// startsService runs a command of the surface that serves in the foreground,
// bounded, and returns the document it answered.
//
// A caller uses it for a service that is expected to refuse rather than serve,
// which is why the bound is its own argument: unbounded, a service that
// accepted what it should have refused never returns, and the check whose
// whole subject is a refusal would hang instead of reporting.
//
// It takes no platform guard of its own, and there is nothing here it could
// take: what comes back is a document, so a configuration the service refused
// and a socket it could not bind both read as an answer that did not exit
// successfully, and a guard conditioned on that would skip the refusal this
// exists to observe. Its caller carries one instead. The one it has drives a
// run and so already skips through requiresIdentifiedPeer, on the same
// predicate a transport guard would use, and a caller added later that drives
// no run has to carry a guard itself.
func startsService(t *testing.T, j *journey.Journey, within time.Duration, args ...string) journey.Answer {
	t.Helper()
	return j.CommandBounded(j.Dir(), within, args...)
}

// succeeds fails the test unless the command answered successfully, and
// returns what it answered.
func succeeds(t *testing.T, answer journey.Answer) journey.Answer {
	t.Helper()
	if answer.Code != machine.ExitOK {
		t.Fatalf("this command was expected to succeed:\n%s", answer)
	}
	return answer
}

// decodeRun reads a run out of an answer.
func decodeRun(t *testing.T, answer journey.Answer) machine.Run {
	t.Helper()
	var run machine.Run
	if err := answer.Decode(&run); err != nil {
		t.Fatalf("reading the run: %v", err)
	}
	return run
}

// startRun starts or attaches to this branch's run and returns what the
// surface answered.
func startRun(t *testing.T, j *journey.Journey, args ...string) machine.Run {
	t.Helper()
	return decodeRun(t, succeeds(t, j.Command(args...)))
}

// walkableRun starts a run a walk can carry from one hold to the next, which
// in this build means skipping the review stage.
//
// The review stage has a body, and that body opens the run's isolated copy
// before it launches anything. Nothing in this build creates one, so a run
// that takes the stage fails there rather than holding, and a walk that took
// it would end at review however its holds were answered. The skip is a run
// input, which PRD principle P2 makes a person's per-run choice, so this
// drives the surface a person would drive rather than weakening the stage:
// the internal/cli and internal/service tests that walk a run carry the same
// skip for the same reason. The stage is named rather than derived, and the
// name goes away when the build creates the isolated copy a run works in.
func walkableRun(t *testing.T, j *journey.Journey, intent string) machine.Run {
	t.Helper()
	return startRun(t, j, "--skip", pipeline.StageReview.String(), "--intent", intent)
}

// answerHolds answers every decision the run reaches with the same option
// until the run stops moving, and returns every answer in order, the one it
// was given first among them.
//
// It bounds the loop at more answers than a run of nine stages can need, so a
// run that holds forever fails here rather than hanging the suite.
func answerHolds(t *testing.T, j *journey.Journey, first machine.Run, option string) []machine.Run {
	t.Helper()
	walk := []machine.Run{first}
	current := first
	for range 20 {
		if current.Outcome != machine.OutcomeDecision {
			return walk
		}
		// The answer is not required to exit successfully. A run a bound parked
		// is reported as an operational failure, which is the right code for
		// it and is one of the answers this loop has to be able to reach.
		current = decodeRun(t, j.Command("--answer", option))
		walk = append(walk, current)
	}
	t.Fatalf("the run was answered %d times and never stopped holding; it stands at %s",
		len(walk), current.Position)
	return nil
}

// last is the final answer of a walk.
func last(walk []machine.Run) machine.Run { return walk[len(walk)-1] }

// stageNames is the stages an answer reports, in the order it reports them.
func stageNames(run machine.Run) []string {
	names := make([]string, 0, len(run.Stages))
	for _, stage := range run.Stages {
		names = append(names, stage.Stage)
	}
	return names
}

// cloneRun copies a run far enough that a counterfeit can change one stage
// without reaching the observation it was derived from.
func cloneRun(run machine.Run) machine.Run {
	run.Stages = slices.Clone(run.Stages)
	return run
}

// writeInto writes a file into a directory, for a test that needs the subject
// to hold something before it commits.
func writeInto(dir, name, content string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing %s in %s: %w", name, dir, err)
	}
	return nil
}

// subject returns the fixture built for this process.
func subject(t *testing.T) *fixture.Fixture {
	t.Helper()
	built, err := journey.Subject()
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	return built
}

// scenario returns one scenario without claiming it, for a test that only
// reads what is planted in it.
func scenarioNamed(t *testing.T, name fixture.ScenarioName) fixture.Scenario {
	t.Helper()
	found, ok := subject(t).Scenario(name)
	if !ok {
		t.Fatalf("the fixture has no %s scenario", name)
	}
	return found
}

// cloned returns a working copy of a scenario's origin that belongs to this
// test alone, for a test that needs a repository to point a run at rather than
// a particular planted condition.
//
// It clones the scenario carrying no planted executable and no local
// configuration a run could trip over, so a run against it is a run against an
// ordinary repository. The trusted configuration document on its default
// branch does not parse, which nothing in this build reads, and
// TestATrustedConfigurationThatCannotBeReadIsNotFallenBackFrom is where that
// is driven rather than relied on.
func cloned(t *testing.T) (fixture.Scenario, string) {
	t.Helper()
	from := scenarioNamed(t, fixture.ScenarioUnparseableTrustedConfig)
	path, err := journey.Clone(from, filepath.Join(t.TempDir(), "work"))
	if err != nil {
		t.Fatalf("cloning a working copy to run against: %v", err)
	}
	return from, path
}

// inClone opens a journey against a working copy of this test's own, with a
// gate created and a service serving.
func inClone(t *testing.T) *journey.Journey {
	t.Helper()
	return inCloneConfigured(t, nil)
}

// inCloneConfigured is inClone with the home's own configuration document
// written before the service reads it.
//
// The ordering is the point. A service resolves the home's configuration when
// it opens, so a document written after it started is one no run is held to,
// and a test that wrote one and then drove a run would be reporting that the
// setting did nothing when what did nothing was the write.
func inCloneConfigured(t *testing.T, document map[string]any) *journey.Journey {
	t.Helper()
	scenario, path := cloned(t)
	j := open(t, scenario, func(o *journey.Options) { o.Dir = path })
	if len(document) > 0 {
		if err := j.WriteConfiguration(document); err != nil {
			t.Fatalf("writing the home's configuration: %v", err)
		}
	}
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)
	return j
}

// pushedAgentName is the agent the branch under validation names in its own
// configuration document, in the scenarios whose branch carries one.
//
// internal/fixture's own constant is unexported, so this is a restatement of
// what it plants; keeping one here rather than a literal at each site is what
// stops four hand-copied spellings of a name whose owner is another package.
// A run that resolved it had read a repository's document as trusted, which is
// what makes it worth naming at all.
const pushedAgentName = "fixture-pushed-agent"

// firstDocument decodes the first document of a stream into v, which is what a
// verb that writes as it goes leaves on standard output.
//
// It refuses a field the shape does not declare, and it refuses an object with
// no field at all. Without both, decoding says only that standard output began
// with a JSON object: internal/machine's shapes share few keys and none has a
// required field encoding/json would miss, so a failure envelope handed back
// where a run was promised decodes into all-zero values and reads as the right
// shape. What it establishes with them is that the document declared nothing
// the promised shape does not have and was not empty, which tells every pair
// of shapes on this surface apart. What it still cannot tell apart is a
// document whose keys are a subset of the promised shape's, so a caller
// wanting more than that asserts a field of its own.
//
// Two mechanisms are needed for the first of those, because one of them stops
// at a shape that decodes itself. DisallowUnknownFields is a setting on this
// decoder, and a shape carrying its own UnmarshalJSON is handed the raw bytes
// and never sees it: machine.Run is one, so that setting alone accepts a
// failure envelope as a run. The second is the shape's own answer - what it
// encodes back - and a key the document carried that the shape does not put
// back is a key the shape does not hold, whichever way it decodes. A shape
// this surface answered survives it, because the bytes came from encoding that
// same shape, so every key present was one it emits.
func firstDocument(stdout string, v any) error {
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(stdout)).Decode(&fields); err != nil {
		return fmt.Errorf("the first document is not an object: %w", err)
	}
	if len(fields) == 0 {
		return errors.New("the first document is an object carrying no field, which decodes into every " +
			"shape this surface answers")
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("re-encoding the shape the first document decoded into: %w", err)
	}
	var held map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &held); err != nil {
		return fmt.Errorf("the shape the first document decoded into does not encode as an object: %w", err)
	}
	var undeclared []string
	for name := range fields {
		if _, declared := held[name]; !declared {
			undeclared = append(undeclared, name)
		}
	}
	if len(undeclared) > 0 {
		slices.Sort(undeclared)
		return fmt.Errorf("the first document carries %s, which the shape it was decoded into does not "+
			"hold, so it is a document of some other shape", strings.Join(undeclared, ", "))
	}
	return nil
}

// runsRecorded is how many runs this home has recorded, across every
// repository it knows.
//
// It reads the store rather than the surface because the surface's answer
// comes from a service, and a check that only needs to know whether a run
// exists should not have to start one to find out.
func runsRecorded(t *testing.T, j *journey.Journey) int {
	t.Helper()
	opened := records(t, j)
	repositories, err := opened.Repositories(t.Context())
	if err != nil {
		t.Fatalf("reading the repositories this home holds: %v", err)
	}
	total := 0
	for _, repository := range repositories {
		runs, err := opened.RunsForRepository(t.Context(), repository.ID)
		if err != nil {
			t.Fatalf("reading the runs of %s: %v", repository.ID, err)
		}
		total += len(runs)
	}
	return total
}

// records opens the home's database through the path internal/home owns.
//
// A test composing that path itself would open a fresh empty database the day
// the layout moved, and go on reporting about records nobody wrote.
func records(t *testing.T, j *journey.Journey) *store.Store {
	t.Helper()
	path, err := j.Database()
	if err != nil {
		t.Fatalf("finding the home's database: %v", err)
	}
	opened, err := store.Open(t.Context(), path, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the home's records: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}
