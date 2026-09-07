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

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
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

// requiresLocalSocket skips a test whose service did not come up, on a
// platform that has no local socket transport to serve this protocol over.
//
// It is the sibling packages' second guard, on their terms: internal/service
// and internal/cli both carry it, both bound it by the same written-down
// platform predicate, and a platform that does identify peers is one where a
// service that failed to come up is a failure rather than a skip. Its one
// caller is serving, which is where every service these tests start is started,
// and it answers both questions a caller could be asking there: on such a
// platform a service that had to come up did not, and a test whose subject is
// why one did not is reading the transport rather than what it named.
func requiresLocalSocket(t *testing.T, cause error) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, cause)
	}
}

// stagesWithoutABody is the stages this build has no implementation for, in
// the order a run takes them.
//
// Those are the stages a run holds at, so a check about where a run stops
// derives its answer from here rather than naming a stage or counting to nine.
// A body that lands moves where a run first stops, and internal/stages says as
// much: internal/cli and internal/service read Implemented for the same
// reason, and a check written against the count would fail the day a body
// lands for a reason that has nothing to do with what it asserts.
//
// It reads the table compiled into this test binary. That is the same table
// the binary under test carries unless BinaryVariable names an artifact built
// from another tree, and one whose bodies differ makes the checks resting on
// this fail loudly rather than quietly measure something else.
func stagesWithoutABody(t *testing.T) []pipeline.Stage {
	t.Helper()
	implemented := map[pipeline.Stage]bool{}
	for _, stage := range stages.Implemented() {
		implemented[stage] = true
	}
	var pending []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if !implemented[stage] {
			pending = append(pending, stage)
		}
	}
	if len(pending) < 2 {
		t.Fatalf("this build has %d stage(s) without a body, so a run no longer walks from one hold to "+
			"the next; the checks resting on this need rewriting against whatever now holds a run",
			len(pending))
	}
	return pending
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
// It is the one call to journey.Serve in this package's tests, and it is that
// so the platform guard cannot be forgotten: a service that did not come up is
// a failure everywhere the transport it binds exists and a skip where it does
// not, which is the sibling packages' answer, and a test that reached past
// this would take neither. The residual gap is that nothing enforces it. Go
// has no way to close the door on an exported method, and a test asserting it
// by reading this package's source would be proving a pattern rather than a
// behaviour, which is the shape this repository rejects. So it is a rule a
// reader keeps, and the one thing that makes it keepable is that both answers
// a caller could want are here: serve for a service that has to come up, and
// this for a test whose subject is one that did not.
func serving(t *testing.T, j *journey.Journey) error {
	t.Helper()
	err := j.Serve()
	if err != nil {
		requiresLocalSocket(t, err)
	}
	return err
}

// serve starts the service and fails the test unless it came up.
func serve(t *testing.T, j *journey.Journey) {
	t.Helper()
	if err := serving(t, j); err != nil {
		t.Fatalf("serving: %v", err)
	}
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

// shimSuffix is the executable suffix on this platform, which a shim's file
// name carries.
func shimSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

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
	return nil
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
