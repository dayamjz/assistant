package journey_test

import (
	"encoding/json"
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
)

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

// serve starts the service in a process this test owns, so it can be killed
// the way a crash kills it.
func serve(t *testing.T, j *journey.Journey) {
	t.Helper()
	if err := j.Serve(); err != nil {
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
// TestTheBranchUnderValidationDoesNotChooseWhatRuns is where that is driven
// rather than relied on.
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
func firstDocument(stdout string, v any) error {
	return json.NewDecoder(strings.NewReader(stdout)).Decode(v)
}
