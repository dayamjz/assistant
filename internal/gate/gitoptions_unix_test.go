//go:build unix

package gate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The options WithGitOptions sets reach the two handles it says they do: the
// working copy the default opener opens and the gate repository Initialize
// creates. Both run the git those options name, which here logs each call
// before running the real one, so the log holds the working copy's open and
// the gate's init. The control is the same initialization without the option
// in a fresh home: the logging git is not run at all, so the log is not filled
// by something other than the options.
func TestGitOptionsReachTheWorkingCopyAndTheGateRepository(t *testing.T) {
	gitEnvironment(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	logging := filepath.Join(dir, "git")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := procgroup.WriteProgram(logging, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the logging git: %v", err)
	}
	command, _ := recorderCommand(t, 0)

	control := newWorkingCopy(t)
	home, _, opts := homeWithIndex(t)
	if _, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: control.path, Command: command}, opts()...); err != nil {
		t.Fatalf("Initialize without the option: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("the logging git ran without the option (%v), so the log proves nothing", err)
	}

	wc := newWorkingCopy(t)
	home, _, opts = homeWithIndex(t)
	built, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command},
		opts(gate.WithGitOptions(vcs.WithGitBinary(logging)))...)
	if err != nil {
		t.Fatalf("Initialize with the option: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading the logging git's calls: %v", err)
	}
	calls := string(data)
	if !strings.Contains(calls, resolved(t, wc.path)) {
		t.Errorf("no call named the working copy %s, so its open did not use the options:\n%s", wc.path, calls)
	}
	var initialized bool
	for _, line := range strings.Split(calls, "\n") {
		if strings.Contains(line, " init ") && strings.Contains(line, built.Repository()) {
			initialized = true
		}
	}
	if !initialized {
		t.Errorf("no init of the gate repository %s ran through the options:\n%s", built.Repository(), calls)
	}
}
