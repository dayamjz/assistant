package fixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPlantedExecutableIsCommittedExecutableWhereGitIgnoresTheFilesystemBit
// reproduces the condition the harness-installation plant has to survive.
// Where core.fileMode is false, which is what git configures itself with on a
// filesystem that carries no executable bit, git records 100644 for a file it
// stages by mode alone. The executable bit is part of what the planted
// installation is rather than a detail of how it was written, so the commit
// states the mode in the index instead of letting it be inferred.
//
// The second file is the control: it is written the same way apart from the
// index entry, and it has to come out non-executable. Without it this test
// would pass on a platform where the filesystem bit is read and would be
// asserting nothing.
func TestAPlantedExecutableIsCommittedExecutableWhereGitIgnoresTheFilesystemBit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	g, err := newGitRunner("", t.TempDir())
	if err != nil {
		t.Fatalf("build the runner: %v", err)
	}
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("create the working copy: %v", err)
	}
	if _, err := g.run(work, "init", "--quiet", "."); err != nil {
		t.Fatalf("initialize the working copy: %v", err)
	}
	if _, err := g.run(work, "config", "core.fileMode", "false"); err != nil {
		t.Fatalf("configure the working copy: %v", err)
	}

	const script = "#!/bin/sh\nexit 0\n"
	const stated, inferred = "hooks/stated.sh", "hooks/inferred.sh"
	if err := g.writeExecutable(work, stated, script); err != nil {
		t.Fatalf("write the planted executable: %v", err)
	}
	if err := writeFile(work, inferred, 0o755, script); err != nil {
		t.Fatalf("write the control: %v", err)
	}
	// Neither file carries an executable bit git could read, which is the
	// state on a platform where core.fileMode is false because there is none.
	for _, rel := range []string{stated, inferred} {
		if err := os.Chmod(filepath.Join(work, filepath.FromSlash(rel)), 0o644); err != nil {
			t.Fatalf("clear the executable bit on %s: %v", rel, err)
		}
	}
	if _, err := g.commitAll(work, "plant two scripts"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := committedMode(t, g, work, stated); got != "100755" {
		t.Errorf("%s is committed with mode %s, want 100755: the mode has to be stated in the index, "+
			"because git does not read the filesystem's executable bit here", stated, got)
	}
	if got := committedMode(t, g, work, inferred); got != "100644" {
		t.Errorf("%s is committed with mode %s, want 100644: the control is what makes the assertion "+
			"above about the index entry rather than about the filesystem", inferred, got)
	}
	if len(g.executables) != 0 {
		t.Errorf("%d planted executables are still awaiting a commit that already happened", len(g.executables))
	}
}

// committedMode reads the mode a path is committed with at HEAD.
func committedMode(t *testing.T, g *gitRunner, work, rel string) string {
	t.Helper()
	out, err := g.run(work, "ls-tree", "HEAD", "--", rel)
	if err != nil {
		t.Fatalf("read the committed mode of %s: %v", rel, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		t.Fatalf("%s is not committed", rel)
	}
	return fields[0]
}
