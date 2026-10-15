//go:build linux || darwin

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/managed"
)

// managedWorld is a user home, a search path holding the stand-in service
// manager, and where the stand-in records its calls, for one test.
type managedWorld struct {
	userHome string
	bin      string
	calls    string
	getenv   func(string) string
}

// newManagedWorld puts the stand-in manager on a fresh search path in front of
// this process's own, so git and sh are still found, and gives the surface a
// user home of the test's own.
func newManagedWorld(t *testing.T, h *home.Home) managedWorld {
	t.Helper()
	w := managedWorld{userHome: t.TempDir(), bin: t.TempDir(), calls: t.TempDir()}
	probe, err := managed.ForThisPlatform(func(k string) string {
		if k == "HOME" {
			return w.userHome
		}
		return ""
	}, h.Root())
	if err != nil {
		t.Fatalf("finding this platform's manager: %v", err)
	}
	units := filepath.Dir(probe.Unit())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"systemctl", "launchctl"} {
		script := "#!/bin/sh\n" + standInManagerDirVar + "='" + w.calls + "' " + standInManagerUnitsVar + "='" + units +
			"' exec '" + self + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(w.bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	searchPath := w.bin + string(os.PathListSeparator) + os.Getenv("PATH")
	w.getenv = func(k string) string {
		switch k {
		case "HOME":
			return w.userHome
		case "PATH":
			return searchPath
		}
		return ""
	}
	// Whatever a test leaves up is stopped before its home is removed.
	t.Cleanup(func() { w.run(t, h, "", "service", "stop", "--force") })
	return w
}

// run drives the surface in this world's environment.
func (w managedWorld) run(t *testing.T, h *home.Home, workingDir string, args ...string) invocation {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if workingDir == "" {
		workingDir = h.Root()
	}
	var out, errs bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:       append([]string{"--home", h.Root()}, args...),
		Stdout:     &out,
		Stderr:     &errs,
		Getenv:     w.getenv,
		WorkingDir: workingDir,
		Executable: self,
		Version:    "assistant (test)",
	})
	return invocation{code: code, stdout: out.String(), stderr: errs.String()}
}

// manager is this world's manager for h, as the surface resolves it.
func (w managedWorld) manager(t *testing.T, h *home.Home) *managed.Manager {
	t.Helper()
	m, err := managed.ForThisPlatform(w.getenv, h.Root())
	if err != nil {
		t.Fatalf("resolving the manager: %v", err)
	}
	return m
}

// recorded is every call the stand-in manager was asked, in order.
func (w managedWorld) recorded(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.calls, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// service is the structured answer to a service command that answers with one.
func (w managedWorld) service(t *testing.T, h *home.Home, args ...string) machine.Service {
	t.Helper()
	got := w.run(t, h, "", append([]string{"--json", "service"}, args...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("service %v exited %s:\n%s%s", args, got.code, got.stdout, got.stderr)
	}
	var state machine.Service
	if err := json.Unmarshal([]byte(got.stdout), &state); err != nil {
		t.Fatalf("service %v does not decode: %v\n%s", args, err, got.stdout)
	}
	return state
}

// untilDown waits for no service to answer for h.
func (w managedWorld) untilDown(t *testing.T, h *home.Home) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for w.service(t, h, "status").Running {
		if time.Now().After(deadline) {
			t.Fatal("the service still answers")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop stops the service for h, requires the stop to succeed, and waits
// until nothing answers.
func (w managedWorld) stop(t *testing.T, h *home.Home) {
	t.Helper()
	got := w.run(t, h, "", "service", "stop")
	if got.code != machine.ExitOK {
		t.Fatalf("stopping the service exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	w.untilDown(t, h)
}

// startCalls, installCalls and removeCalls are what this platform's manager
// is asked for each, written out here from what each manager documents
// rather than read from internal/managed.
func installCalls(m *managed.Manager) []string {
	if m.Kind() == managed.KindLaunchd {
		return []string{"bootstrap gui/" + strconv.Itoa(os.Getuid()) + " " + m.Unit()}
	}
	return []string{"--user daemon-reload", "--user enable --now " + m.Name()}
}

func startCalls(m *managed.Manager) []string {
	if m.Kind() == managed.KindLaunchd {
		return []string{"kickstart gui/" + strconv.Itoa(os.Getuid()) + "/" + m.Name()}
	}
	return []string{"--user start " + m.Name()}
}

func removeCalls(m *managed.Manager) []string {
	if m.Kind() == managed.KindLaunchd {
		return []string{"bootout gui/" + strconv.Itoa(os.Getuid()) + "/" + m.Name()}
	}
	return []string{"--user disable --now " + m.Name(), "--user daemon-reload"}
}

// assistant service start --install hands the home's service to this
// platform's manager and answers once the service the manager started is
// up: the definition is in the user's folder for that manager, and the
// manager was asked to load and start it and nothing else. The service it
// started is the one the definition says, because the stand-in reads it from
// there. Status reports the service as handed over, in both renderings. A
// stop the service is asked for leaves it down and the definition in place,
// and a plain start then goes through the manager rather than launching a
// second service beside it. assistant service stop --uninstall stops the
// service through its own stop and only then has the manager unload the
// definition, which is gone afterwards; asked again with nothing set up, it
// is refused as incorrect usage, phrased, and asks the manager nothing.
//
// The control for the one-service rule is the first step: an install while
// a service launched the ordinary way is up is refused, and writes and asks
// nothing.
func TestAManagedServiceIsInstalledStartedAndRemovedThroughTheManager(t *testing.T) {
	requiresIdentifiedPeer(t)
	vocabtest.Control(t)
	h := newHome(t)
	w := newManagedWorld(t, h)
	m := w.manager(t, h)

	if up := w.service(t, h, "start"); !up.Running || up.Managed != nil {
		t.Fatalf("an ordinary start answered %+v, want a service up and nothing handed over", up)
	}
	refused := w.run(t, h, "", "service", "start", "--install")
	if refused.code != machine.ExitFailure || !strings.Contains(refused.stderr, machine.PhraseManagedServiceUp.String()) {
		t.Fatalf("an install beside a service that is up exited %s:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	if got := w.recorded(t); len(got) != 0 {
		t.Fatalf("a refused install asked the manager %q", got)
	}
	if installed, _ := m.Installed(); installed {
		t.Fatal("a refused install wrote a definition")
	}
	w.stop(t, h)

	installed := w.service(t, h, "start", "--install")
	if !installed.Running || installed.Managed == nil || installed.Managed.Unit != m.Unit() || installed.Managed.Manager != string(m.Kind()) {
		t.Fatalf("the install answered %+v, want a service up and handed to %s at %s", installed, m.Kind(), m.Unit())
	}
	definition, err := os.ReadFile(m.Unit())
	if err != nil {
		t.Fatalf("the definition is not where the answer says: %v", err)
	}
	if !strings.Contains(string(definition), "--managed") || !strings.Contains(string(definition), h.Root()) {
		t.Fatalf("the definition does not start the managed service for this home:\n%s", definition)
	}
	if got := w.recorded(t); !slices.Equal(got, installCalls(m)) {
		t.Fatalf("the install asked the manager %q, want %q", got, installCalls(m))
	}

	status := w.service(t, h, "status")
	if !status.Running || status.Managed == nil {
		t.Fatalf("status answered %+v, want the service up and handed over", status)
	}
	read := w.run(t, h, "", "service", "status")
	if !strings.Contains(read.stdout, machine.PhraseServiceManaged.String()) {
		t.Fatalf("status does not say the service is handed over:\n%s", read.stdout)
	}
	vocabtest.Assert(t, read.stdout)

	w.stop(t, h)
	if still, _ := m.Installed(); !still {
		t.Fatal("a stop removed the definition")
	}
	if again := w.service(t, h, "start"); !again.Running || again.Managed == nil {
		t.Fatalf("a start with a definition set up answered %+v", again)
	}
	if got := w.recorded(t); !slices.Equal(got, slices.Concat(installCalls(m), startCalls(m))) {
		t.Fatalf("the start asked the manager %q, want %q after the install", got, startCalls(m))
	}

	removed := w.run(t, h, "", "--json", "service", "stop", "--uninstall")
	var removal machine.ManagedRemoval
	if err := json.Unmarshal([]byte(removed.stdout), &removal); err != nil || removed.code != machine.ExitOK {
		t.Fatalf("the removal exited %s as %q (%v)%s", removed.code, removed.stdout, err, removed.stderr)
	}
	if !removal.Stopped || removal.Managed.Unit != m.Unit() {
		t.Fatalf("the removal answered %+v, want the service stopped and the definition at %s removed", removal, m.Unit())
	}
	if still, _ := m.Installed(); still {
		t.Fatal("the definition is still there after the removal")
	}
	if w.service(t, h, "status").Running {
		t.Fatal("a service still answers after the removal")
	}
	want := slices.Concat(installCalls(m), startCalls(m), removeCalls(m))
	if got := w.recorded(t); !slices.Equal(got, want) {
		t.Fatalf("the manager was asked %q, want %q", got, want)
	}

	row := machine.ManagedRefusalPhrase(managed.ReasonNotInstalled).String()
	nothing := w.run(t, h, "", "service", "stop", "--uninstall")
	if nothing.code != machine.ExitUsage || !strings.Contains(nothing.stderr, row) {
		t.Fatalf("a removal with nothing set up exited %s:\n%s%s", nothing.code, nothing.stdout, nothing.stderr)
	}
	if got := w.recorded(t); !slices.Equal(got, want) {
		t.Fatalf("a refused removal asked the manager %q", got[len(want):])
	}
}

// A flag only the managed service takes is refused as incorrect usage where
// it does not belong, before anything is asked of anyone.
func TestTheManagedFlagsAreRefusedWhereTheyDoNotBelong(t *testing.T) {
	h := newHome(t)
	w := newManagedWorld(t, h)
	for _, args := range [][]string{
		{"service", "start", "--managed"},
		{"service", "start", "--install", "--foreground"},
		{"service", "start", "--full-disk-access-granted"},
		{"service", "restart", "--uninstall"},
	} {
		if got := w.run(t, h, "", args...); got.code != machine.ExitUsage {
			t.Fatalf("%v exited %s, want %s:\n%s%s", args, got.code, machine.ExitUsage, got.stdout, got.stderr)
		}
	}
	if got := w.recorded(t); len(got) != 0 {
		t.Fatalf("a refused flag asked the manager %q", got)
	}
}

// A service started under a manager, with --managed, ends with a failure
// status and internal/machine's row when it is asked to restart, and launches
// no successor of its own: bringing up the replacement is the manager's
// restart policy, which the stand-in does not model, so after the restart
// nothing answers and the restart's own wait for a successor fails. A
// successor launched here would answer, which is what a service started
// without --managed does on the same request.
func TestARestartUnderAManagerEndsTheServiceAndLaunchesNothing(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	w := newManagedWorld(t, h)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan invocation, 1)
	go func() {
		var out, errs bytes.Buffer
		code := cli.Run(t.Context(), cli.Environment{
			Args:       []string{"--home", h.Root(), "service", "start", "--foreground", "--managed"},
			Stdout:     &out,
			Stderr:     &errs,
			Getenv:     w.getenv,
			WorkingDir: h.Root(),
			Executable: self,
			Version:    "assistant (test)",
		})
		served <- invocation{code: code, stdout: out.String(), stderr: errs.String()}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for !w.service(t, h, "status").Running {
		if time.Now().After(deadline) {
			t.Fatal("the managed service never answered")
		}
		time.Sleep(50 * time.Millisecond)
	}

	restart := w.run(t, h, "", "service", "restart")
	var ended invocation
	select {
	case ended = <-served:
	case <-time.After(30 * time.Second):
		t.Fatal("the managed service did not end on a restart")
	}
	if ended.code != machine.ExitFailure || !strings.Contains(ended.stderr, machine.PhraseManagedRestart.String()) {
		t.Fatalf("the managed service ended %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}
	if restart.code == machine.ExitOK {
		t.Fatalf("the restart found a successor answering, so the managed service launched one:\n%s", restart.stdout)
	}
	if w.service(t, h, "status").Running {
		t.Fatal("a service answers after a managed restart with no manager to bring one up")
	}
}
