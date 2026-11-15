//go:build unix

package q1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// spec is this directory, where cleanup.sh and build-subject.sh are.
var spec string

func TestMain(m *testing.M) {
	var err error
	if spec, err = os.Getwd(); err == nil {
		if err = standin.RegisterTempTag(); err == nil {
			err = standin.SweepTempDirs()
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	journey.Cleanup()
	os.Exit(code)
}

// attempt is one Caliper attempt root: hooks run in <root>/work, the checkout
// is <root>/work/work, the gate home <root>/.assistant. HOME and TMPDIR are the
// root, as the agent's HOME is under Caliper, so nothing written leaves it.
type attempt struct {
	t                    *testing.T
	root, bin, path      string
	home, work, checkout string
}

func newAttempt(t *testing.T) *attempt {
	t.Helper()
	bin, err := journey.Binary()
	if err != nil {
		t.Fatal(err)
	}
	root, err := standin.TempDir("q1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a := &attempt{t: t, root: root, bin: bin, home: filepath.Join(root, ".assistant"), work: filepath.Join(root, "work")}
	a.checkout = filepath.Join(a.work, "work")
	stub := filepath.Join(root, "stub")
	git, err := exec.LookPath("git")
	if err == nil {
		err = errors.Join(os.Mkdir(a.work, 0o755), os.Mkdir(stub, 0o755))
	}
	// Stand-in agents that fail at once: the gate admits a run, review holds it, and no model is asked.
	for _, name := range []string{"claude", "cursor"} {
		if err == nil {
			err = procgroup.WriteProgram(filepath.Join(stub, name), []byte("#!/bin/sh\nexit 1\n"), 0o755)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	a.path = strings.Join([]string{stub, filepath.Dir(bin), filepath.Dir(git), "/usr/bin:/bin:/usr/sbin:/sbin"}, ":")
	return a
}

// run starts name in dir with the attempt's environment: exit code, stdout, stderr.
func (a *attempt) run(dir, name string, args ...string) (int, string, string) {
	a.t.Helper()
	cmd := procgroup.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + a.root, "TMPDIR=" + a.root, "PATH=" + a.path,
		"CALIPER_WORKDIR=" + a.work, "CALIPER_SPEC_DIR=" + spec, "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false"}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := procgroup.Run(ctx, cmd, 5*time.Second)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		a.t.Fatalf("%s %v: %v\n%s", name, args, err, stderr.String())
	}
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String()
}

// assistant runs the built binary on the home and fails the test unless it exits 0.
func (a *attempt) assistant(dir string, args ...string) string {
	a.t.Helper()
	code, out, errs := a.run(dir, a.bin, append([]string{"--home", a.home}, args...)...)
	if code != 0 {
		a.t.Fatalf("assistant %v: exit %d\n%s\n%s", args, code, out, errs)
	}
	return out
}

// serve builds the benign subject as the setup hook does, then does what the
// agent does before it sends anything: init in the checkout and start the
// service, which answers a readiness check before start returns.
func (a *attempt) serve() {
	a.t.Helper()
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		a.t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	a.assistant(a.checkout, "--json", "init")
	a.assistant(a.root, "service", "start")
	a.t.Cleanup(func() { a.run(a.root, a.bin, "--home", a.home, "service", "stop", "--force") }) // a stop after cleanup's fails
}

func (a *attempt) running() bool {
	var status struct{ Running bool }
	return json.Unmarshal([]byte(a.assistant(a.root, "--json", "service", "status")), &status) == nil && status.Running
}

// record is what cleanup.sh writes; README.md's "Gate record" owns the format.
type record struct {
	Attempt, Home, Outcome string
	CapturedAt             string `json:"captured_at"`
	ServiceAnswered        bool   `json:"service_answered"`
	Errors, Repositories   []string
	Usage, Runs, Stop      struct {
		Cwd, Stderr string
		Exit        any // a JSON number, or nil when the call never ran
		Output      struct{ Runs []map[string]any }
	}
}

// cleanup runs the hook as Caliper does and returns its exit code, its
// output, and the record it wrote, failing the test when there is none.
func (a *attempt) cleanup() (int, string, record) {
	a.t.Helper()
	code, out, errs := a.run(a.work, "sh", filepath.Join(spec, "cleanup.sh"))
	var r record
	data, err := os.ReadFile(filepath.Join(a.root, "caliper-q1-gate", filepath.Base(a.root)+".record.json"))
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	if err != nil {
		a.t.Fatalf("no record after cleanup (exit %d): %v\n%s%s", code, err, out, errs)
	}
	return code, out + errs, r
}

func want(t *testing.T, what string, got, wanted any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(wanted) {
		t.Errorf("%s = %v, want %v", what, got, wanted)
	}
}

// TestCleanupReadsTheRunsThenStopsTheService is the good read: a run the gate
// really held, read from the attempt's checkout while the service answers,
// and only then the stop.
func TestCleanupReadsTheRunsThenStopsTheService(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	var started struct{ Record struct{ ID string } }
	if err := json.Unmarshal([]byte(a.assistant(a.checkout, "--json", "--intent", "ship the branch")), &started); err != nil || started.Record.ID == "" {
		t.Fatalf("no run started: %v", err)
	}
	code, out, r := a.cleanup()
	want(t, "exit", code, 0)
	want(t, "outcome", r.Outcome, "read")
	want(t, "errors", r.Errors, []string{})
	want(t, "attempt", r.Attempt, filepath.Base(a.root))
	want(t, "home", r.Home, a.home)
	if _, err := time.Parse(time.RFC3339, r.CapturedAt); err != nil {
		t.Errorf("captured_at: %v", err)
	}
	t.Run("good read", func(t *testing.T) {
		want(t, "runs cwd", r.Runs.Cwd, a.checkout)
		want(t, "runs exit", r.Runs.Exit, 0)
		want(t, "usage exit", r.Usage.Exit, 0)
		if len(r.Runs.Output.Runs) != 1 {
			t.Fatalf("runs = %v, want the one run", r.Runs.Output.Runs)
		}
		run := r.Runs.Output.Runs[0]
		want(t, "run id", run["id"], started.Record.ID)
		want(t, "repositories", r.Repositories, []any{run["repository_id"]})
		build, _ := run["build"].(map[string]any)
		for field, v := range map[string]any{"status": run["status"], "submitted_head": run["submitted_head"],
			"config_digest": run["config_digest"], "build.revision": build["revision"]} {
			if s, _ := v.(string); s == "" {
				t.Errorf("run has no %s: %v", field, run)
			}
		}
	})
	t.Run("read then stop", func(t *testing.T) {
		want(t, "service answered at capture", r.ServiceAnswered, true)
		want(t, "stop exit", r.Stop.Exit, 0)
		if a.running() {
			t.Errorf("the service still answers after cleanup:\n%s", out)
		}
	})
}
