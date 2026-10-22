package stages_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
)

// The stand-in agent is this test binary re-executed, so a package whose tests
// run an agent has to answer as one, and the stand-in code host the checks
// tests read a pull request from is the same binary again. Each stand-in's
// first New in a process refuses loudly when its Main was not called, rather
// than letting the resulting failures read as the stage under test
// misbehaving.
//
// It also turns automatic git maintenance off for the git the stage bodies
// run through internal/vcs, not only for the test helper's own git. A
// maintenance run forked into the background outlives the test and races its
// TempDir cleanup ("directory not empty", #119). internal/vcs strips the
// GIT_CONFIG_COUNT pairs the helper uses, by design, but keeps
// GIT_CONFIG_GLOBAL, so the setting is carried in a global file of this
// process's own, which includes whatever global file was in effect before it so
// nothing else about the configuration changes.
func TestMain(m *testing.M) {
	standin.Main()
	forgetest.Main()
	if err := standin.SweepTempDirs(standin.TempTagStages); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cleanup, err := maintenanceOff()
	if err != nil {
		fmt.Fprintln(os.Stderr, "turning git maintenance off for the stage tests:", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// maintenanceOff points GIT_CONFIG_GLOBAL at a file that includes the global
// file in effect before it, if any, and then sets gc.auto to 0 and
// maintenance.auto to false, which the later lines win. It returns what
// removes the file.
func maintenanceOff() (func(), error) {
	dir, err := standin.TempDir(standin.TempTagStages, "gitconfig")
	if err != nil {
		return nil, err
	}
	previous := os.Getenv("GIT_CONFIG_GLOBAL")
	if previous == "" {
		if home, err := os.UserHomeDir(); err == nil {
			previous = filepath.Join(home, ".gitconfig")
		}
	}
	body := ""
	if previous != "" {
		body += fmt.Sprintf("[include]\n\tpath = %q\n", previous)
	}
	body += "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", path); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}
