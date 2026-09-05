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

// TestAPlantedExecutableLeftUnstagedIsRefused holds the guard Build applies
// after every scenario. A plant that writes an executable and lets some other
// plant commit that working copy hands git a file whose mode it infers, which
// is the lost executable bit above with nothing reporting it.
func TestAPlantedExecutableLeftUnstagedIsRefused(t *testing.T) {
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
	if err := g.requireDrained(); err != nil {
		t.Fatalf("a runner that has planted nothing is not drained: %v", err)
	}
	if err := g.writeExecutable(work, "hooks/planted.sh", "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatalf("write the planted executable: %v", err)
	}
	err = g.requireDrained()
	if err == nil {
		t.Fatal("a planted executable that no commit staged was not reported")
	}
	if !strings.Contains(err.Error(), "hooks/planted.sh") {
		t.Errorf("the refusal does not name the executable left pending: %v", err)
	}
	if _, err := g.commitAll(work, "plant one script"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.requireDrained(); err != nil {
		t.Fatalf("the commit that staged it did not drain it: %v", err)
	}
}

// TestAPathSurvivesBeingWrittenAsAGitConfigurationValue holds the format the
// two planted configuration files rest on. A path is written into them and read
// back with git, because a value git refuses to parse and a value git parses
// into a different path are both invisible to anything that reads the file's
// bytes instead.
//
// The control is the same path concatenated raw, which is what this package did
// before: it has to come back different, or the quoting is doing nothing and
// the assertion above would hold either way.
func TestAPathSurvivesBeingWrittenAsAGitConfigurationValue(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	g, err := newGitRunner("", t.TempDir())
	if err != nil {
		t.Fatalf("build the runner: %v", err)
	}
	home := t.TempDir()
	// A space, a comment character and a quote: the three a raw value loses.
	// Backslash is the fourth and cannot be put in a path component here,
	// because it is a separator on the platform where it matters.
	planted := filepath.Join(home, `a dir #1 "x"`, "template")
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatalf("create the path being written: %v", err)
	}

	stated := filepath.Join(home, "stated")
	if err := os.WriteFile(stated,
		[]byte("[init]\n\ttemplateDir = "+gitConfigPathValue(planted)+"\n"), 0o600); err != nil {
		t.Fatalf("write the configuration file: %v", err)
	}
	got, err := g.run(home, "config", "--file", stated, "--get", "init.templateDir")
	if err != nil {
		t.Fatalf("git cannot read the value back: %v", err)
	}
	if want := filepath.ToSlash(planted); got != want {
		t.Errorf("git reads the value back as %q, want %q", got, want)
	}

	raw := filepath.Join(home, "raw")
	if err := os.WriteFile(raw, []byte("[init]\n\ttemplateDir = "+planted+"\n"), 0o600); err != nil {
		t.Fatalf("write the control: %v", err)
	}
	control, err := g.run(home, "config", "--file", raw, "--get", "init.templateDir")
	if err == nil && control == filepath.ToSlash(planted) {
		t.Errorf("the raw value round-trips too, so the assertion above does not hold the format: %q",
			control)
	}
}

// TestTheEnvironmentCarriesThePlatformThroughAndDropsWhatRedirects holds both
// halves of what gitEnvironment now does. It has to carry the caller's own
// environment through, because git and the processes it starts need what the
// platform put there, and it has to drop the variables that would let whatever
// ran the build decide which repository an invocation resolves to or what a
// repository it creates is born with.
//
// The redirect half is asserted through git rather than by reading the slice
// back: a GIT_DIR that survived would make git answer about another repository,
// which is the failure, and a list comparison would pass on a variable git
// stopped honouring.
func TestTheEnvironmentCarriesThePlatformThroughAndDropsWhatRedirects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.git")
	carried := filepath.Join(t.TempDir(), "carried")
	t.Setenv("GIT_DIR", elsewhere)
	t.Setenv("GIT_TEMPLATE_DIR", filepath.Join(t.TempDir(), "template"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(t.TempDir(), "hooks"))
	t.Setenv("FIXTURE_PLATFORM_VARIABLE", carried)

	g, err := newGitRunner("", t.TempDir())
	if err != nil {
		t.Fatalf("build the runner: %v", err)
	}
	// What the platform put there is carried through. Standing in for
	// SystemRoot and its neighbours, which exist only where this test does not
	// run.
	if !containsEntry(g.env, "FIXTURE_PLATFORM_VARIABLE="+carried) {
		t.Errorf("the caller's own environment does not reach git, so a platform that needs its own "+
			"variables would not work: %v", g.env)
	}
	// And what redirects does not. git resolving the working copy rather than
	// the GIT_DIR in the environment is the observable form of that.
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("create the working copy: %v", err)
	}
	if _, err := g.run(work, "init", "--quiet", "."); err != nil {
		t.Fatalf("initialize the working copy: %v", err)
	}
	top, err := g.run(work, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("ask git which working copy it resolved: %v", err)
	}
	if _, statErr := os.Stat(elsewhere); statErr == nil {
		t.Errorf("git created %s, so GIT_DIR reached it", elsewhere)
	}
	if !sameDir(t, top, work) {
		t.Errorf("git resolved %s, want %s: the GIT_DIR in the environment reached it", top, work)
	}
	hooks, err := g.run(work, "rev-parse", "--git-path", "hooks")
	if err != nil {
		t.Fatalf("ask git where it looks for hooks: %v", err)
	}
	if filepath.IsAbs(hooks) {
		t.Errorf("git looks for hooks in %s, so the GIT_CONFIG_COUNT pair in the environment reached it",
			hooks)
	}
}

func containsEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// sameDir compares two directory paths through the filesystem, so a symlinked
// temporary directory does not read as a different one.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(ai, bi)
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
