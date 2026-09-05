package fixture

import (
	"fmt"
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

// TestAWriteNamingABranchHeadThatWasNeverPublishedIsRefused holds the guard
// Build applies after every scenario. A plant that names the branch head in a
// scenario whose branch is never published leaves a file that was never
// written, and the catalog would still tell a harness to read it.
func TestAWriteNamingABranchHeadThatWasNeverPublishedIsRefused(t *testing.T) {
	b := &builder{}
	if err := b.requireHeadWritesMade(); err != nil {
		t.Fatalf("a builder nothing registered against is not settled: %v", err)
	}

	s := &Scenario{Name: ScenarioBase}
	made := ""
	b.afterBranchHead(s, "a canned answer", func(head string) error {
		made = head
		return nil
	})
	err := b.requireHeadWritesMade()
	if err == nil {
		t.Fatal("a registered write that was never made was not reported")
	}
	if !strings.Contains(err.Error(), "a canned answer") {
		t.Errorf("the refusal does not name the write left pending: %v", err)
	}
	if made != "" {
		t.Errorf("the write ran without a branch head, with %q", made)
	}
}

// TestAPathSurvivesBeingWrittenAsAGitConfigurationValue holds the format the
// two planted configuration files rest on. Each path is written into a
// configuration file and read back with git, because a value git refuses to
// parse and a value git parses into a different path are both invisible to
// anything that reads the file's bytes instead.
//
// The paths are strings rather than directories anybody creates. git parses a
// configuration value without resolving it, so nothing here needs the path to
// exist, and not creating it is what lets the quote case run on every platform:
// a double quote is a character Windows reserves in a filename, and it is
// exactly the character the escaping half of gitConfigPathValue is for. That a
// planted path really resolves to a template git uses is held separately, by
// the scenario test that creates a repository under the planted file.
//
// Each case carries a control: the same path concatenated raw, which is what
// this package did before. It has to come back different, or the quoting is
// doing nothing and the assertion beside it would hold either way. The control
// fails on every platform this runs on, for a reason that differs by platform:
// where the separator is a backslash it is an invalid escape, and elsewhere the
// comment character truncates the value.
func TestAPathSurvivesBeingWrittenAsAGitConfigurationValue(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	g, err := newGitRunner("", t.TempDir())
	if err != nil {
		t.Fatalf("build the runner: %v", err)
	}
	home := t.TempDir()
	for i, component := range []string{
		"a dir #1",     // a space and a comment character
		`a dir #1 "x"`, // and a quote, which is what the escaping is for
	} {
		planted := filepath.Join(home, component, "template")

		stated := filepath.Join(home, fmt.Sprintf("stated-%d", i))
		if err := os.WriteFile(stated,
			[]byte("[init]\n\ttemplateDir = "+gitConfigPathValue(planted)+"\n"), 0o600); err != nil {
			t.Fatalf("write the configuration file: %v", err)
		}
		got, err := g.run(home, "config", "--file", stated, "--get", "init.templateDir")
		if err != nil {
			t.Errorf("git cannot read %q back: %v", planted, err)
			continue
		}
		if want := filepath.ToSlash(planted); got != want {
			t.Errorf("git reads %q back as %q, want %q", planted, got, want)
		}

		raw := filepath.Join(home, fmt.Sprintf("raw-%d", i))
		if err := os.WriteFile(raw, []byte("[init]\n\ttemplateDir = "+planted+"\n"), 0o600); err != nil {
			t.Fatalf("write the control: %v", err)
		}
		control, err := g.run(home, "config", "--file", raw, "--get", "init.templateDir")
		if err == nil && control == filepath.ToSlash(planted) {
			t.Errorf("%q round-trips raw too, so the assertion beside it does not hold the format: %q",
				planted, control)
		}
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
	// A template that exists and carries a hook no default template ships,
	// because git init honours GIT_TEMPLATE_DIR whatever the configuration
	// says. A template directory that was not there would leave a variable
	// that survived and one that was dropped producing the same repository,
	// so the assertion below would hold under both.
	template := filepath.Join(t.TempDir(), "template")
	const templateHook = "pre-receive"
	if err := os.MkdirAll(filepath.Join(template, "hooks"), 0o755); err != nil {
		t.Fatalf("create the template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(template, "hooks", templateHook),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write the template hook: %v", err)
	}
	t.Setenv("GIT_DIR", elsewhere)
	t.Setenv("GIT_TEMPLATE_DIR", template)
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
	// The template channel, which is the one that would put a hook into every
	// repository this package creates: each scenario's working copy, each
	// origin, and the second clone.
	dropped := filepath.Join(t.TempDir(), "dropped.git")
	if _, err := g.run(work, "init", "--bare", "--quiet", dropped); err != nil {
		t.Fatalf("create a repository through the runner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dropped, "hooks", templateHook)); err == nil {
		t.Errorf("a repository the runner created carries %s from %s, so the GIT_TEMPLATE_DIR in the "+
			"environment reached git init", templateHook, template)
	}
	// And the same probe with the variable put back, which has to show the
	// hook arriving. Without it the assertion above would hold over a git that
	// ignores the variable, over a template git would not read, and over a
	// hook name git overwrites, and would report the channel closed in every
	// one of those cases without having closed anything.
	honoured := filepath.Join(t.TempDir(), "honoured.git")
	restored := exec.Command(g.binary, "init", "--bare", "--quiet", honoured)
	restored.Dir = work
	restored.Env = append(append([]string{}, g.env...), "GIT_TEMPLATE_DIR="+template)
	if out, err := restored.CombinedOutput(); err != nil {
		t.Fatalf("create a repository with the variable put back: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(honoured, "hooks", templateHook)); err != nil {
		t.Fatalf("the template hook does not arrive even when GIT_TEMPLATE_DIR is honoured, so the "+
			"assertion above cannot tell a variable that survived from one that was dropped: %v", err)
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
