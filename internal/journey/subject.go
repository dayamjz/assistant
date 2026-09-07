package journey

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dayamjz/assistant/internal/fixture"
)

// built is the fixture this process drives. It is built once, because a build
// writes a git object graph from nothing and costs seconds, and because the
// catalog it produces is the same for every test that reads it.
var built struct {
	once  sync.Once
	root  string
	value *fixture.Fixture
	err   error
}

// Subject returns the built fixture and the catalog of what is planted in it.
//
// The fixture is built rather than checked in, which is internal/fixture's own
// rule: no git object graph lives in this repository, so there is none a test
// depends on and none nobody can regenerate. It is built from this module's
// own source even when the binary under test came from elsewhere, because the
// subject a run is pointed at is not part of what ships.
func Subject() (*fixture.Fixture, error) {
	built.once.Do(func() {
		built.root, built.err = os.MkdirTemp("", "assistant-fixture")
		if built.err != nil {
			built.err = fmt.Errorf("journey: making a directory to build the fixture into: %w", built.err)
			return
		}
		built.value, built.err = fixture.Build(built.root)
	})
	return built.value, built.err
}

// claims records which test has taken a scenario for its exclusive use.
var claims struct {
	mu sync.Mutex
	by map[fixture.ScenarioName]string
}

// ErrScenarioClaimed reports that two tests would have driven one scenario.
var ErrScenarioClaimed = errors.New("journey: this scenario is already claimed")

// Claim takes a scenario for the exclusive use of the named test and returns
// it.
//
// A scenario is a directory on disk with a remote, a working copy, and a
// tripwire file, and a test that initializes a gate in it, pushes to its
// origin, or reads its tripwires has made those facts about the scenario its
// own. Two tests sharing one would interfere in ways that read as flakiness
// rather than as the mistake they are, so the second one is refused here and
// names the first.
//
// A test that only reads a scenario - the bytes of a planted agent response,
// a commit on its origin - does not claim it. Reading does not interfere, and
// requiring a claim for it would force scenarios apart for no reason.
func Claim(scenario fixture.ScenarioName, by string) (fixture.Scenario, error) {
	subject, err := Subject()
	if err != nil {
		return fixture.Scenario{}, err
	}
	found, ok := subject.Scenario(scenario)
	if !ok {
		return fixture.Scenario{}, fmt.Errorf("journey: this fixture has no scenario named %s", scenario)
	}
	claims.mu.Lock()
	defer claims.mu.Unlock()
	if holder, taken := claims.by[scenario]; taken {
		return fixture.Scenario{}, fmt.Errorf("%w: %s took %s, and %s wants it too; a scenario is driven by "+
			"one test", ErrScenarioClaimed, holder, scenario, by)
	}
	if claims.by == nil {
		claims.by = map[fixture.ScenarioName]string{}
	}
	claims.by[scenario] = by
	return found, nil
}

// Git runs one git command inside a scenario, under the isolation the scenario
// was built with, and returns what it printed with surrounding space trimmed.
//
// It goes through fixture.GitInvocation rather than through internal/vcs on
// purpose, for the reason internal/fixture states for its own plants: a
// subject read with the code under validation cannot show that code wrong. A
// harness that confirmed a push landed by asking internal/vcs would be
// reporting that internal/vcs agrees with itself.
func Git(scenario fixture.Scenario, dir string, args ...string) (string, error) {
	return GitWith(scenario, nil, dir, args...)
}

// GitWith is Git with more written over the environment, for a condition whose
// plant is a git configuration file reaching one invocation.
func GitWith(scenario fixture.Scenario, env map[string]string, dir string, args ...string) (string, error) {
	binary, base, err := scenario.GitInvocation()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = writeOver(base, env)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("journey: git %s in %s: %w\n%s", strings.Join(args, " "), dir, err, text)
	}
	return text, nil
}

// Fired is the tripwire identifiers a scenario recorded, which is how "nothing
// planted was executed" is checked as a fact rather than assumed.
//
// It reports the empty list for a scenario nothing fired in, so a caller
// comparing against what a condition says must stay quiet writes one
// comparison rather than one for the file being absent and one for it being
// empty.
func Fired(scenario fixture.Scenario) ([]string, error) {
	fired, _, err := scenario.Fired()
	if err != nil {
		return nil, err
	}
	if fired == nil {
		return []string{}, nil
	}
	return fired, nil
}

// Condition returns one planted condition from the catalog. A harness that
// spelled an identifier wrong would otherwise check nothing and report a pass.
func Condition(id fixture.ID) (fixture.Condition, error) {
	subject, err := Subject()
	if err != nil {
		return fixture.Condition{}, err
	}
	condition, ok := subject.Condition(id)
	if !ok {
		return fixture.Condition{}, fmt.Errorf("journey: this fixture plants no condition named %s", id)
	}
	return condition, nil
}

// Carries reports which of the substrings a condition's outcome requires are
// missing from the message the product produced, so a failure names what was
// not said rather than printing two paragraphs side by side.
func Carries(message string, want []string) []string {
	var missing []string
	for _, substring := range want {
		if !strings.Contains(message, substring) {
			missing = append(missing, substring)
		}
	}
	return missing
}

// Clone makes a working copy of a scenario's origin at path, checked out on
// the branch under validation, and returns the path.
//
// It exists so that a test needing a repository to point a run at does not
// have to take a scenario's own working copy away from the test that needs
// what is planted in it. A clone is what a person has: a real working copy
// with a real origin, made by real git, and a gate is filed under a hash of
// the working copy's path, so two clones of one origin are two repositories as
// far as the product is concerned.
//
// What a clone does not carry is a scenario's local git configuration, which
// is not committed. A condition resting on one - the base scenario's
// core.hooksPath, which is what makes its committed hook scripts the hooks git
// runs - is only reachable in the scenario's own working copy, so a test about
// one claims the scenario instead of cloning it.
func Clone(scenario fixture.Scenario, path string) (string, error) {
	if _, err := Git(scenario, filepath.Dir(path), "clone", "--quiet", scenario.Origin, path); err != nil {
		return "", err
	}
	if _, err := Git(scenario, path, "checkout", "--quiet", scenario.Branch); err != nil {
		return "", err
	}
	return path, nil
}
