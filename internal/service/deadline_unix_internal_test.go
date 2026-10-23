//go:build unix

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// hangOnPath puts an executable called name first on PATH for the rest of
// the test. While the file marker exists it records its process identifier
// and never exits; otherwise it runs the real name found on PATH before, or
// exits 127 when there is none, so a service that runs it for its own
// purposes before the test creates the marker is served as it would be
// without the stand-in.
//
// It returns what the stand-in recorded: every invocation that waited. A
// waiting invocation still running when the test ends is killed then, so a
// test that failed does not leave one behind. PATH is the process's, so a
// test using this cannot run in parallel; t.Setenv refuses that.
func hangOnPath(t *testing.T, name, marker string) (recorded func() []int) {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, name+".pids")
	fallback := "echo 'no " + name + " on PATH' >&2; exit 127"
	if real, err := exec.LookPath(name); err == nil {
		fallback = "exec '" + real + "' \"$@\""
	}
	script := "#!/bin/sh\n" +
		"if [ -e '" + marker + "' ]; then\n" +
		"  echo $$ >> '" + pidFile + "'\n" +
		"  exec sleep 600\n" +
		"fi\n" +
		fallback + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stand-in %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	recorded = func() []int {
		data, err := os.ReadFile(pidFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("reading what the stand-in %s recorded: %v", name, err)
		}
		var pids []int
		for _, line := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(line)
			if err != nil {
				t.Fatalf("the stand-in %s recorded %q, not a process identifier", name, line)
			}
			pids = append(pids, pid)
		}
		return pids
	}
	t.Cleanup(func() {
		for _, pid := range recorded() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return recorded
}

// requireEnded fails the test for each process in pids still running a
// moment after the invocation that started it returned.
func requireEnded(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Errorf("process %d is still running after the invocation that started it returned", pid)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// openWithOperatorFile opens a service over a fresh home whose operator file
// is document, with the code host the service builds itself.
func openWithOperatorFile(t *testing.T, document string) *Service {
	t.Helper()
	root, err := standin.TempDir("h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(document), 0o600); err != nil {
		t.Fatalf("writing the operator's configuration: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	runner := standin.New(t, standin.Script{}).Runner()
	running, err := Open(t.Context(), Options{
		Home:      h,
		NewStages: func(stages.StageDeps) pipeline.Stages { return pipeline.ConstantStages("unused") },
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   agents.NewCatalog(fixedRunner{runner: runner}, cursorFactory{runner: runner}),
		LockWait:  -1,
	})
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	t.Cleanup(func() { _ = running.Close() })
	return running
}

// PRD section 8's process model and P15 for gh, through the code host the
// service builds: with gh_deadline set to its floor in the operator's file, a
// gh that never exits, read under a context with no deadline of its own, is
// ended with its group at that deadline and not at the two-minute default.
// The read is the one a run's checks stage and the merge read make, on the
// host every run of the service opens its provider through.
func TestTheOperatorsGHDeadlineEndsAGHThatNeverExits(t *testing.T) {
	principles.Cite(t, principles.P15)
	const configured = 10 * time.Second
	marker := filepath.Join(t.TempDir(), "hang")
	recorded := hangOnPath(t, "gh", marker)
	running := openWithOperatorFile(t, "gh_deadline: "+configured.String()+"\n")
	provider, err := running.forge.Open("owner/name")
	if err != nil {
		t.Fatalf("opening a provider on the service's code host: %v", err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("arming the stand-in: %v", err)
	}
	// Well short of the default, so a read the configured deadline did not
	// reach fails here rather than at the default.
	limit := configured + time.Minute
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := provider.Checks(context.WithoutCancel(t.Context()), 12)
		done <- err
	}()
	select {
	case err = <-done:
	case <-time.After(limit):
		t.Fatalf("a gh that never exits was still being waited on after %s; the operator's %s did not bound it", limit, configured)
	}
	elapsed := time.Since(start)
	if elapsed < configured {
		t.Errorf("the read returned after %s, before the operator's %s", elapsed, configured)
	}
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != forge.ReasonUnavailable || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the read answered %v; want the provider refused as unavailable at its deadline", err)
	}
	pids := recorded()
	if len(pids) != 1 {
		t.Fatalf("the stand-in recorded %d waiting invocations, want the one read", len(pids))
	}
	requireEnded(t, pids)
}
