package gate_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestInitializeRefusesASpecItCannotTrust covers the refusals that happen
// before anything is created. Each one is a fact about the gate that would
// otherwise be decided by whatever the environment happened to contain.
func TestInitializeRefusesASpecItCannotTrust(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	notExecutable := filepath.Join(t.TempDir(), "not-executable")
	writeFile(t, notExecutable, "#!/bin/sh\nexit 0\n")

	cases := []struct {
		name string
		spec gate.Spec
	}{
		{"no home", gate.Spec{WorkingPath: wc.path, Command: command}},
		{"relative home", gate.Spec{Home: "home", WorkingPath: wc.path, Command: command}},
		{"no working path", gate.Spec{Home: home, Command: command}},
		{"relative working path", gate.Spec{Home: home, WorkingPath: "work", Command: command}},
		{"working path is not a directory", gate.Spec{Home: home, WorkingPath: filepath.Join(wc.path, "file.txt"), Command: command}},
		{"working path does not exist", gate.Spec{Home: home, WorkingPath: filepath.Join(wc.path, "absent"), Command: command}},
		{"no command", gate.Spec{Home: home, WorkingPath: wc.path}},
		{"command is not executable", gate.Spec{Home: home, WorkingPath: wc.path, Command: notExecutable}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := gate.Initialize(ctx(t), c.spec, opts()...)
			if !errors.Is(err, gate.ErrInvalidSpec) {
				t.Fatalf("Initialize error = %v, want ErrInvalidSpec", err)
			}
			if entries, err := os.ReadDir(filepath.Join(home, "repos")); err == nil && len(entries) > 0 {
				t.Fatalf("the refused initialization created %d gate repositories", len(entries))
			}
			if url, ok := remoteURL(t, wc.path, gate.RemoteName); ok {
				t.Fatalf("the refused initialization set the %s remote to %q", gate.RemoteName, url)
			}
		})
	}
}

// TestARelativeHookCommandIsRefusedEvenWhenPATHResolvesIt is the absolute-path
// rule on its own. A command that PATH can find is exactly the case the rule
// exists for, and it is the case the executable check does not catch, so it
// gets a test where nothing else could produce the refusal.
func TestARelativeHookCommandIsRefusedEvenWhenPATHResolvesIt(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	t.Setenv("PATH", filepath.Dir(command)+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := exec.LookPath(filepath.Base(command)); err != nil {
		t.Fatalf("the fixture command is not resolvable through PATH, so this test asks nothing: %v", err)
	}
	_, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: filepath.Base(command)}, opts()...)
	if !errors.Is(err, gate.ErrInvalidSpec) {
		t.Fatalf("Initialize error = %v, want ErrInvalidSpec", err)
	}
}

// TestPathAtPushTimeCannotChooseWhatAdmissionRuns is why the hook command has
// to be an absolute path. A push happens in whoever's environment, and an
// executable of the same name earlier in their PATH must not become the thing
// that decides whether the push is admitted.
func TestPathAtPushTimeCannotChooseWhatAdmissionRuns(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, log := recorderCommand(t, 0)

	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// A decoy of the same name, earlier in PATH, that would admit nothing.
	decoy, decoyLog := stubCommand(t, t.TempDir(), stubName, 1)
	if filepath.Base(decoy) != filepath.Base(command) {
		t.Fatalf("the decoy is named %q and the recorded command %q, so PATH could never have chosen it and this test asks nothing",
			filepath.Base(decoy), filepath.Base(command))
	}
	t.Setenv("PATH", filepath.Dir(decoy)+string(os.PathListSeparator)+os.Getenv("PATH"))

	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	if got := invocations(t, decoyLog); len(got) != 0 {
		t.Fatalf("the decoy on PATH was invoked: %v", got)
	}
	if got := invocations(t, log); len(got) == 0 {
		t.Fatal("the recorded command was not invoked either, so this test proved nothing")
	}
}

// TestHookCommandSurvivesAPathThatNeedsQuoting keeps the hooks correct for a
// person whose home directory has a space or a quote in it, which is where a
// script assembled by concatenation usually breaks.
func TestHookCommandSurvivesAPathThatNeedsQuoting(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	opts := indexOptions(t)
	home := filepath.Join(t.TempDir(), "an odd 'home'")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}

	dir := filepath.Join(t.TempDir(), "bin dir with 'quotes'")
	command, log := stubCommand(t, dir, "assistant stub", 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "push", "--quiet", gate.RemoteName, "main")

	got := invocations(t, log)
	want := hookInvocation(g, "admit")
	if len(got) == 0 || got[0] != want {
		t.Fatalf("invocations = %v, want the first to be %q", got, want)
	}
}

// TestInstallationLeavesExactlyTheTwoExecutableHooks checks the state
// installation leaves behind: both hooks exist, both are executable where the
// host records that, and nothing else was put in the hooks directory.
//
// Which program those hooks actually run is not asked here, because reading it
// out of the generated text would prove nothing a rewrite of the script could
// not break while the behavior held. TestPathAtPushTimeCannotChooseWhatAdmissionRuns
// and TestHookCommandSurvivesAPathThatNeedsQuoting settle it by pushing and
// watching which program runs.
func TestInstallationLeavesExactlyTheTwoExecutableHooks(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	for _, name := range []string{gate.AdmissionHook, gate.NotificationHook} {
		path := filepath.Join(g.Repository(), "hooks", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if recordsAnExecutableBit() && info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable, so it is not usable as a hook: mode %v", path, info.Mode())
		}
	}
	if got, want := activeHookNames(t, g.Repository()), 2; len(got) != want {
		t.Fatalf("the gate has hooks %v, want exactly the two this package installs", got)
	}
}

// recordsAnExecutableBit reports whether this host keeps a permission bit that
// says whether a file may be run.
//
// Windows does not: os.Stat there reports a writable regular file as 0666
// whatever mode it was created with, so the bit answers nothing about a hook
// git runs perfectly well, and asserting it would fail every gate on that host.
// What makes a hook usable is that git runs it, and that is asked on every
// platform by the tests that push: TestPathAtPushTimeCannotChooseWhatAdmissionRuns,
// TestHookCommandSurvivesAPathThatNeedsQuoting, and every refusal test that
// pushes to check the gate is closed.
func recordsAnExecutableBit() bool {
	return runtime.GOOS != "windows"
}
