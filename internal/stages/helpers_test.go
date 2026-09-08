package stages_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// git runs a git command in a directory, for building the subject repository a
// stage body reads. It shells out on the same terms internal/vcs's,
// internal/gate's and internal/service's own tests do: a working copy
// assembled with the code under test could not show that code wrong.
//
// It runs with the developer's own git configuration out of the way and with
// the author and committer identity stated, so what a test builds does not
// depend on the machine it runs on.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, ".gitconfig-absent"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newHome opens a home under a temporary root.
func newHome(t *testing.T) *home.Home {
	t.Helper()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a home: %v", err)
	}
	return h
}

// write puts a file in a working copy, creating the directories above it.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
}

// stateReader is the pipeline.Reader a stage body is given: it answers the
// keys the implementation declared and refuses the rest, so a test cannot read
// a key the real stage node would not have handed over. What it reads from is
// a real run state, so every key holds the typed zero the graph would give it.
//
// It has no way to fail a declared read, and that is deliberate. The real
// reader records every refusal it hands back and the node adapter takes the
// recorded error whatever the body returned, so a fake that failed a read
// without that consequence would state a shape the mechanism cannot produce
// and would let a body look like it had recovered from something no body can.
type stateReader struct {
	allowed map[pipeline.Key]bool
	state   graph.State
}

// Get implements pipeline.Reader.
func (r stateReader) Get(key pipeline.Key) (graph.Value, error) {
	if !r.allowed[key] {
		return graph.Value{}, errors.New("the stage did not declare a read of " + string(key))
	}
	value, _ := r.state.Get(string(key))
	return value, nil
}
