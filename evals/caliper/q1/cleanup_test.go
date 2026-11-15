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

var spec string // this directory, where cleanup.sh and build-subject.sh are

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
	env                  []string // added to every command's environment
}

func newAttempt(t *testing.T) *attempt {
	t.Helper()
	bin, err := journey.Binary()
	root := ""
	if err == nil {
		root, err = standin.TempDir("q1")
	}
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
	// Stand-in agents that fail at once: the gate admits a run, and no model is asked.
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
	cmd.Env = append(cmd.Env, a.env...)
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

// serve builds the subject as setup does, then does what the agent does first:
// init in the checkout and start the service (ready when start returns).
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

// stopped waits until status no longer reports the service running, because a
// stop exits on Accepted while the outgoing service may still answer its
// socket. It is false if the service still runs 30s after stopped is called.
func (a *attempt) stopped() bool {
	a.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); a.running(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}

// record is what cleanup.sh writes; README.md's "Gate record" owns the format.
type record struct {
	Attempt, Home, Outcome string
	CapturedAt             string `json:"captured_at"`
	ServiceAnswered        bool   `json:"service_answered"`
	Errors, Repositories   []string
	Usage, Runs, Stop      struct {
		Cwd, Stderr string
		Exit        any // a JSON number, or nil when the step is absent or its call raised
		Output      struct{ Runs []map[string]any }
	}
	steps map[string]json.RawMessage // the record's top-level keys, to tell an absent step from exit nil
}

// cleanup runs the hook as Caliper does and returns its exit code, its
// output, and the record it wrote, failing the test when there is none.
func (a *attempt) cleanup() (int, string, record) {
	a.t.Helper()
	code, out, errs := a.run(a.work, "sh", filepath.Join(spec, "cleanup.sh"))
	var r record
	data, err := os.ReadFile(filepath.Join(a.root, "caliper-q1-gate", filepath.Base(a.root)+".record.json"))
	if err == nil {
		err = errors.Join(json.Unmarshal(data, &r), json.Unmarshal(data, &r.steps))
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

// TestCleanupReadsTheRunsThenStopsTheService is the good read: the run the gate
// held, read from the attempt's checkout while the service answers, then the stop.
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
	_, err := time.Parse(time.RFC3339, r.CapturedAt)
	want(t, "captured_at parse error", err, nil)
	want(t, "service answered at capture", r.ServiceAnswered, true)
	want(t, "stop exit", r.Stop.Exit, 0)
	want(t, "service down after cleanup", a.stopped(), true)
	want(t, "runs cwd", r.Runs.Cwd, a.checkout)
	want(t, "runs exit", r.Runs.Exit, 0)
	want(t, "usage exit", r.Usage.Exit, 0)
	if len(r.Runs.Output.Runs) != 1 {
		t.Fatalf("runs = %v, want the one run\n%s", r.Runs.Output.Runs, out)
	}
	run := r.Runs.Output.Runs[0]
	want(t, "run id", run["id"], started.Record.ID)
	want(t, "run status", run["status"], "held")
	want(t, "repositories", r.Repositories, []any{run["repository_id"]})
	build, _ := run["build"].(map[string]any)
	for i, v := range []any{run["submitted_head"], run["config_digest"], build["revision"]} {
		want(t, fmt.Sprintf("field %d of submitted_head, config_digest, build.revision missing from %v", i, run), v == nil || v == "", false)
	}
}

// TestCleanupStillReadsWhenTheUsageFileCannotBeWritten: a directory where the
// usage file goes fails that side write alone. The usage step is kept, the
// held run is still read, the service is still stopped, and the hook exits 1.
func TestCleanupStillReadsWhenTheUsageFileCannotBeWritten(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.assistant(a.checkout, "--json", "--intent", "ship the branch")
	want(t, "mkdir", os.MkdirAll(filepath.Join(a.root, "caliper-q1-gate", filepath.Base(a.root)+"-usage.json"), 0o755), nil)
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "read")
	want(t, "usage exit", r.Usage.Exit, 0)
	want(t, "runs exit", r.Runs.Exit, 0)
	if len(r.Runs.Output.Runs) != 1 || r.Runs.Output.Runs[0]["status"] != "held" {
		t.Errorf("runs = %v, want the held run", r.Runs.Output.Runs)
	}
	want(t, "stop exit", r.Stop.Exit, 0)
	want(t, "service down after cleanup", a.stopped(), true)
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "usage: writing ") || !strings.Contains(out, "usage: writing ") {
		t.Errorf("errors = %v, want the failed usage write alone, on stderr too:\n%s", r.Errors, out)
	}
}

// TestCleanupStopsAndRecordsErrorWhenCaptureRaises injects a fault no call()
// catches: a sitecustomize on the hook's PYTHONPATH makes the checkout check
// inside capture() raise, after the usage read and before runs. Nothing the
// binary prints is faked. The stop must still run and be recorded as its own
// step, and the outcome must be error (indeterminate), never a quiet no-home
// or read.
func TestCleanupStopsAndRecordsErrorWhenCaptureRaises(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	fault := filepath.Join(a.root, "fault")
	want(t, "fault dir", os.Mkdir(fault, 0o755), nil)
	want(t, "fault", os.WriteFile(filepath.Join(fault, "sitecustomize.py"), []byte(`import os.path
_isdir = os.path.isdir
def isdir(p):
    if p.endswith(os.path.join("work", "work")):
        raise RuntimeError("injected fault in capture")
    return _isdir(p)
os.path.isdir = isdir
`), 0o644), nil)
	a.env = []string{"PYTHONPATH=" + fault}
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "error")
	want(t, "usage exit", r.Usage.Exit, 0)
	want(t, "runs exit (never asked)", r.Runs.Exit, nil)
	want(t, "stop exit", r.Stop.Exit, 0)
	a.env = nil
	want(t, "service down after cleanup", a.stopped(), true)
	if !strings.Contains(strings.Join(r.Errors, "\n"), "injected fault in capture") || !strings.Contains(out, "injected fault in capture") {
		t.Errorf("errors = %v, want the injected fault, on stderr too:\n%s", r.Errors, out)
	}
}

// usageFile reports whether cleanup saved a usage read beside the record.
func (a *attempt) usageFile() bool {
	_, err := os.Stat(filepath.Join(a.root, "caliper-q1-gate", filepath.Base(a.root)+"-usage.json"))
	return err == nil
}

// TestCleanupMarksAnAttemptWithNoHome: no <attempt>/.assistant is a record
// saying so, not a missing file and not an error, and nothing is stopped.
func TestCleanupMarksAnAttemptWithNoHome(t *testing.T) {
	a := newAttempt(t)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	code, out, r := a.cleanup()
	want(t, "exit", code, 0)
	want(t, "outcome", r.Outcome, "no-home")
	want(t, "errors", r.Errors, []string{})
	if stop, ok := r.steps["stop"]; ok {
		t.Errorf("a stop step was recorded for an attempt with no home: %s", stop)
	}
	if a.usageFile() {
		t.Errorf("a usage file was written for an attempt with no home:\n%s", out)
	}
}

// TestCleanupMarksAMissingCheckout: a home with no work/work is its own
// outcome, its usage is still saved, and the service is still stopped.
func TestCleanupMarksAMissingCheckout(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	if err := os.Rename(a.checkout, a.checkout+".moved"); err != nil {
		t.Fatal(err)
	}
	code, out, r := a.cleanup()
	want(t, "exit", code, 0)
	want(t, "outcome", r.Outcome, "no-checkout")
	want(t, "usage file", a.usageFile(), true)
	want(t, "stop exit", r.Stop.Exit, 0)
	if !a.stopped() {
		t.Errorf("the service still runs 30s after cleanup:\n%s", out)
	}
}

// TestCleanupRecordsAServiceDownAtCaptureAsError: a home whose service is
// down cannot be read. That is error and exit 1, never "no runs", and there
// is no usage file to mistake for zero usage.
func TestCleanupRecordsAServiceDownAtCaptureAsError(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.assistant(a.root, "service", "stop", "--force")
	if !a.stopped() {
		t.Fatal("the service still runs 30s after its stop")
	}
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "error")
	want(t, "service answered at capture", r.ServiceAnswered, false)
	want(t, "usage file", a.usageFile(), false)
	if fmt.Sprint(r.Runs.Exit) == "0" || !strings.Contains(out, "runs: exit ") {
		t.Errorf("runs exit = %v with the service down, and stderr:\n%s", r.Runs.Exit, out)
	}
}

// TestCleanupRecordsAFailingRead: a checkout that is not a working copy makes
// runs fail with the service up. Its exit status is in the record, the stop
// still runs, and the hook fails.
func TestCleanupRecordsAFailingRead(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	if err := errors.Join(os.Rename(a.checkout, a.checkout+".moved"), os.Mkdir(a.checkout, 0o755)); err != nil {
		t.Fatal(err)
	}
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "error")
	want(t, "runs exit", r.Runs.Exit, 1)
	want(t, "stop exit", r.Stop.Exit, 0)
	if !strings.Contains(out, "runs: exit 1") {
		t.Errorf("the failed read is not on stderr:\n%s", out)
	}
}

// TestCleanupSurfacesAFailingStop: the read succeeds and the stop fails, here
// because the service went down between the two, which is what the real
// binary answers a stop with then. The failure is in the record, on stderr,
// and in the exit status.
func TestCleanupSurfacesAFailingStop(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	// An assistant ahead of the real one on PATH that, asked to stop, first
	// stops the service itself, then waits until status no longer reports it
	// running (a stop exits on Accepted while the socket may still answer),
	// and only then hands the same stop to the real binary, so the stop
	// cleanup sees is the real binary's answer to a service that is down. A
	// service still up after at least 30s by the clock (date counts whole
	// seconds, hence +31) exits 97, which fails the test loudly.
	wrap := filepath.Join(a.root, "wrap")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in *" service stop "*)
	%[1]s "$@" >/dev/null 2>&1
	end=$(($(date +%%s) + 31))
	while %[1]s --home %[2]s --json service status 2>/dev/null | grep -q '"running":true'; do
		[ "$(date +%%s)" -lt "$end" ] || { echo "wrap: the service still runs 30s after its stop" >&2; exit 97; }
		sleep 0.1
	done;;
esac
exec %[1]s "$@"
`, procgroup.ShellQuote(a.bin), procgroup.ShellQuote(a.home))
	if err := os.Mkdir(wrap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := procgroup.WriteProgram(filepath.Join(wrap, "assistant"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	a.path = wrap + ":" + a.path
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "read")
	want(t, "stop exit", r.Stop.Exit, 1)
	if r.Stop.Stderr == "" || !strings.Contains(out, "stop: exit 1") {
		t.Errorf("the failed stop is not in the record and on stderr: %q\n%s", r.Stop.Stderr, out)
	}
}
