package gate_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/principles"
)

// maintenanceKeys are the two keys a gate repository is born with, and the
// value each is given.
var maintenanceKeys = [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}}

// localConfig reads one key from a repository's own configuration file and
// nothing above it, and reports whether it is set there. The addressing
// arguments name the repository the way git is asked about it.
func localConfig(t *testing.T, dir string, addressing []string, key string) (string, bool) {
	t.Helper()
	args := append(append([]string{}, addressing...), "config", "--local", "--get-all", key)
	out, err := tryRawGit(dir, args...)
	if err != nil {
		// git config reports an unset key with a failure and no output.
		if strings.TrimSpace(out) != "" {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return "", false
	}
	return strings.TrimSpace(out), true
}

// TestAGateIsBornWithAutomaticMaintenanceOff reads the gate repository's own
// configuration back after an initialization and finds both keys at the
// values internal/vcs writes, once each.
//
// The working copy's own configuration is read the same way and carries
// neither key, which is PRD principle P1's half: the person's checkout is not
// written. That read is also the positive control for the reader, because it
// shows the same read coming back unset where nothing set it.
func TestAGateIsBornWithAutomaticMaintenanceOff(t *testing.T) {
	principles.Cite(t, principles.P1)
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	for _, kv := range maintenanceKeys {
		got, ok := localConfig(t, wc.path, []string{"--git-dir=" + g.Repository()}, kv[0])
		if !ok || got != kv[1] {
			t.Errorf("the gate's own configuration has %s = %q (set: %v), want exactly %q", kv[0], got, ok, kv[1])
		}
		if got, ok := localConfig(t, wc.path, []string{"-C", wc.path}, kv[0]); ok {
			t.Errorf("the working copy's own configuration gained %s = %q from the initialization", kv[0], got)
		}
	}
}

// TestARefreshGivesAnExistingGateAutomaticMaintenanceOff is the path a gate
// made before the keys were written takes to acquire them: the next
// initialization of its working copy. The keys are taken out of a gate to
// stand for one that never had them, and the read is shown coming back unset
// before the refresh so the read after it is not a read that could not fail.
func TestARefreshGivesAnExistingGateAutomaticMaintenanceOff(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)
	spec := gate.Spec{Home: home, WorkingPath: wc.path, Command: command}

	g, err := gate.Initialize(ctx(t), spec, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	gitDir := []string{"--git-dir=" + g.Repository()}
	for _, kv := range maintenanceKeys {
		rawGit(t, wc.path, append(append([]string{}, gitDir...), "config", "--local", "--unset-all", kv[0])...)
		if got, ok := localConfig(t, wc.path, gitDir, kv[0]); ok {
			t.Fatalf("%s is still %q in the gate after it was unset, so the check below could not fail", kv[0], got)
		}
	}

	if _, err := gate.Initialize(ctx(t), spec, opts()...); err != nil {
		t.Fatalf("refreshing the gate: %v", err)
	}
	for _, kv := range maintenanceKeys {
		if got, ok := localConfig(t, wc.path, gitDir, kv[0]); !ok || got != kv[1] {
			t.Errorf("after a refresh the gate's own configuration has %s = %q (set: %v), want exactly %q", kv[0], got, ok, kv[1])
		}
	}
}
