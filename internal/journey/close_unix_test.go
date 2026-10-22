//go:build linux || darwin

package journey_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/principles"
)

// Close leaves no home behind even when a git the service started outlives
// the service (P15). The service's git runs in a process group of its own,
// so a kill of the service alone leaves it running, and a git worktree add
// building a run's copy re-creates the leading directories of its target
// under the home after the home was removed. Here that git is held just
// before it runs, through a git on the service's PATH that records its own
// identity, and released once Close has returned, when it makes its target
// as such a git does. Before, Close killed the
// service alone, the released git made the home again (the root, worktrees/
// and the copy's directory), and CI's whole-run leftover check reported it.
// The control is that the held git did start inside the service's run.
func TestCloseLeavesNoHomeAGitTheServiceStartedWouldRemake(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentifiedPeer(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("finding git: %v", err)
	}
	shims, err := journey.Shims()
	if err != nil {
		t.Fatalf("the harness's shims: %v", err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	entered, release := filepath.Join(dir, "entered"), filepath.Join(dir, "release")
	// Held, the git has not started, and released it would start in a
	// working directory Close removed and fail before writing anything. So
	// once released it first makes its target, which is what the git CI's
	// leftover came from had done once past its start: the home's root,
	// worktrees/ and the copy's directory, and nothing else.
	wrapper := "#!/bin/sh\n" +
		"case \" $* \" in *\" worktree add \"*)\n" +
		"  echo $$ > '" + entered + ".tmp' && mv '" + entered + ".tmp' '" + entered + "'\n" +
		"  " + standin.HeldWait(t, release) + " || exit 1\n" +
		"  target=; next=; for arg; do [ -n \"$next\" ] && target=$arg && break; [ \"$arg\" = -- ] && next=1; done\n" +
		"  mkdir -p \"$target\";;\n" +
		"esac\n" +
		"exec '" + git + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}

	scenario, path := cloned(t)
	j := open(t, scenario, func(o *journey.Options) {
		o.Dir = path
		o.Env = map[string]string{"PATH": strings.Join([]string{bin, shims, os.Getenv("PATH")}, string(os.PathListSeparator))}
	})
	succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch))
	serve(t, j)
	if accepted, message := pushToTheGate(t, scenario, nil, path); !accepted {
		t.Fatalf("the push to the gate was declined; git said:\n%s", message)
	}

	held := heldGit(t, entered)
	t.Cleanup(func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Errorf("releasing the held git: %v", err)
		}
	})
	if err := j.Close(); err != nil {
		t.Fatalf("closing the journey: %v", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("releasing the held git: %v", err)
	}
	if !(standin.Process{Identity: held}).GoneWithin(30 * time.Second) {
		t.Fatalf("the held git %d was still running 30s after its release", held.PID)
	}
	if _, err := os.Lstat(j.Root()); !os.IsNotExist(err) {
		var made []string
		_ = filepath.Walk(j.Root(), func(p string, _ os.FileInfo, _ error) error {
			made = append(made, p)
			return nil
		})
		t.Fatalf("the home %s stood again after Close, made by a git the service started: %v (%v)", j.Root(), made, err)
	}
}

// heldGit waits for the git on the service's PATH to record that it is held
// before a worktree add, and returns its identity.
func heldGit(t *testing.T, entered string) agents.Identity {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		raw, err := os.ReadFile(entered)
		if err == nil {
			var pid int
			if _, err := fmt.Sscanf(string(raw), "%d", &pid); err != nil {
				t.Fatalf("the held git recorded %q: %v", raw, err)
			}
			id, found, err := agents.Identify(pid)
			if err != nil || !found {
				t.Fatalf("the held git %d: %+v, %v, %v", pid, id, found, err)
			}
			return id
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the service never ran a git worktree add, so nothing outlived it and this shows nothing")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
