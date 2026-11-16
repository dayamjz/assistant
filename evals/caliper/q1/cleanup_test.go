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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/procgroup"
)

var spec string // this directory, where cleanup.sh and build-subject.sh are

// TestMain lets this binary act as the scripted agent (standin.Main, then
// journey.ActAsShim, which refuses an agent call nothing scripted) and as a
// held process (standin.HeldWait). Every process of this binary the tests
// start ends with the test binary (standin.WatchOwner), and once the tests
// return the package fails if any is still running (standin.RunOwned).
// Nothing here pushes, so no standin.AtTerminal: every run starts with --intent.
func TestMain(m *testing.M) {
	standin.WatchOwner()
	standin.Main()
	journey.ActAsShim()
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
	os.Exit(standin.RunOwned(func() int {
		code := m.Run()
		journey.Cleanup()
		return code
	}))
}

// attempt is one Caliper attempt root: hooks run in <root>/work, the checkout
// is <root>/work/work, the gate home <root>/.assistant. HOME is the root, as
// the agent's HOME is under Caliper, and TMPDIR is the root too unless the
// attempt is made in an arm's TMPDIR (newAttemptIn), so nothing written leaves it.
type attempt struct {
	t                    *testing.T
	root, bin, path      string
	home, work, checkout string
	tmpdir               string   // the hooks' TMPDIR, where cleanup.sh writes caliper-q1-gate
	key                  []string // the attempt key the patched caliper gives a hook; nil as a stock caliper
	env                  []string // added to every command's environment
}

// keyed gives the attempt's hooks the key caliper 0.19.0+q1attemptenv passes:
// task id and attempt number, as its saved result names the attempt.
func (a *attempt) keyed(task string, n int) *attempt {
	a.key = []string{"CALIPER_TASK_ID=" + task, "CALIPER_ATTEMPT=" + strconv.Itoa(n)}
	return a
}

func newAttempt(t *testing.T) *attempt {
	t.Helper()
	root, err := standin.TempDir("q1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return newAttemptAt(t, root, root)
}

// newAttemptIn makes the attempt root name inside tmpdir, as caliper makes
// every attempt root of a run inside that run's TMPDIR, which its hooks inherit.
func newAttemptIn(t *testing.T, tmpdir, name string) *attempt {
	t.Helper()
	root := filepath.Join(tmpdir, name)
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return newAttemptAt(t, root, tmpdir)
}

// newAttemptAt is the attempt at root, an existing directory, with the hooks
// given tmpdir as TMPDIR.
func newAttemptAt(t *testing.T, root, tmpdir string) *attempt {
	t.Helper()
	bin, err := journey.Binary()
	if err != nil {
		t.Fatal(err)
	}
	a := &attempt{t: t, root: root, bin: bin, home: filepath.Join(root, ".assistant"), work: filepath.Join(root, "work"), tmpdir: tmpdir}
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
	return a.keyed("task-001", 1)
}

// environ is the attempt's whole environment for a command it starts.
func (a *attempt) environ() []string {
	return append([]string{"HOME=" + a.root, "TMPDIR=" + a.tmpdir, "PATH=" + a.path,
		"CALIPER_WORKDIR=" + a.work, "CALIPER_SPEC_DIR=" + spec, "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false"},
		append(append([]string{}, a.key...), a.env...)...)
}

// run starts name in dir with the attempt's environment: exit code, stdout, stderr.
func (a *attempt) run(dir, name string, args ...string) (int, string, string) {
	a.t.Helper()
	code, stdout, stderr, err := a.execute(dir, name, args...)
	if err != nil {
		a.t.Fatalf("%s %v: %v\n%s", name, args, err, stderr)
	}
	return code, stdout, stderr
}

// execute is run returning, rather than failing the test on, what is not an
// exit status: a start that failed, output cut short, or the command still
// running 3 minutes on, which procgroup.Run reports as the status of the kill
// that ended it, so it is told by ctx. A goroutine beside the test may call it.
func (a *attempt) execute(dir, name string, args ...string) (int, string, string, error) {
	cmd := procgroup.Command(name, args...)
	cmd.Dir, cmd.Env = dir, a.environ()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := procgroup.Run(ctx, cmd, 5*time.Second)
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("still running 3m on, so it was ended: %w", errors.Join(ctx.Err(), err))
	case errors.As(err, &exit) && !errors.Is(err, exec.ErrWaitDelay):
		err = nil // the exit status is the answer
	}
	return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String(), err
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

// fault puts a sitecustomize.py holding code on the hook's PYTHONPATH, so a
// fault is injected into cleanup.sh's Python while that Python runs unmodified
// and nothing the binary prints is faked.
func (a *attempt) fault(code string) {
	a.t.Helper()
	dir := filepath.Join(a.root, "fault")
	if err := errors.Join(os.Mkdir(dir, 0o755), os.WriteFile(filepath.Join(dir, "sitecustomize.py"), []byte(code), 0o644)); err != nil {
		a.t.Fatal(err)
	}
	a.env = []string{"PYTHONPATH=" + dir}
}

// failStop puts an assistant ahead of the real one on PATH that, asked to
// stop, first stops the service itself, then waits until status no longer
// reports it running (a stop exits on Accepted while the socket may still
// answer), and only then hands the same stop to the real binary, so the stop
// cleanup sees is the real binary's answer to a service that is down. A
// service still up after at least 30s by the clock (date counts whole
// seconds, hence +31) exits 97, which fails the test loudly.
func (a *attempt) failStop() {
	a.t.Helper()
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
		a.t.Fatal(err)
	}
	if err := procgroup.WriteProgram(filepath.Join(wrap, "assistant"), []byte(script), 0o755); err != nil {
		a.t.Fatal(err)
	}
	a.path = wrap + ":" + a.path
}

// record is what cleanup.sh writes; README.md's "Gate record" owns the format.
type record struct {
	Attempt, Home, Outcome string
	TaskID                 any    `json:"task_id"`         // a string, or nil without a key
	CaliperAttempt         any    `json:"caliper_attempt"` // a JSON number, or nil without a key
	CapturedAt             string `json:"captured_at"`
	ServiceAnswered        bool   `json:"service_answered"`
	Errors, Repositories   []string
	HeadChecks             []map[string]any `json:"head_checks"`
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
	data, err := os.ReadFile(filepath.Join(a.tmpdir, "caliper-q1-gate", filepath.Base(a.root)+".record.json"))
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
	// The stage evidence (schema 2, still in 4): the report, the review log alone
	// because the report has stages, and the report again, each read from the root.
	want(t, "schema", string(r.steps["schema"]), "4")
	want(t, "attempt key", []any{r.TaskID, r.CaliperAttempt}, []any{"task-001", 1})
	// The head check (schema 4), made while the checkout exists: one entry,
	// bound to the attempt key, the run and its submitted_head.
	want(t, "head_checks", r.HeadChecks, []map[string]any{{"task_id": "task-001", "caliper_attempt": 1,
		"run_id": started.Record.ID, "submitted_head": run["submitted_head"], "is_commit": true}})
	stages := a.stageEntries()
	e, ok := stages[started.Record.ID]
	if len(stages) != 1 || !ok {
		t.Fatalf("stages = %v, want the one run", stages)
	}
	want(t, "reads", []any{e.Report.Exit, len(e.Logs), e.Logs["review"].Exit, e.ReportAfter.Exit}, []any{0, 1, 0, 0})
	want(t, "read run", []any{field(e.Report.Output, "record", "id"), e.Logs["review"].Output["run"], e.Logs["review"].Output["stage"]},
		[]any{started.Record.ID, started.Record.ID, "review"})
}

// TestCleanupStillReadsWhenTheUsageFileCannotBeWritten: a directory where the
// usage file goes fails that side write alone. The usage step is kept, the
// held run is still read, the service is still stopped, and the hook exits 1.
func TestCleanupStillReadsWhenTheUsageFileCannotBeWritten(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.assistant(a.checkout, "--json", "--intent", "ship the branch")
	want(t, "mkdir", os.MkdirAll(filepath.Join(a.tmpdir, "caliper-q1-gate", filepath.Base(a.root)+"-usage.json"), 0o755), nil)
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
	a.fault(`import os.path
_isdir = os.path.isdir
def isdir(p):
    if p.endswith(os.path.join("work", "work")):
        raise RuntimeError("injected fault in capture")
    return _isdir(p)
os.path.isdir = isdir
`)
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
	_, err := os.Stat(filepath.Join(a.tmpdir, "caliper-q1-gate", filepath.Base(a.root)+"-usage.json"))
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
	a.failStop()
	code, out, r := a.cleanup()
	want(t, "exit", code, 1)
	want(t, "outcome", r.Outcome, "read")
	want(t, "stop exit", r.Stop.Exit, 1)
	if r.Stop.Stderr == "" || !strings.Contains(out, "stop: exit 1") {
		t.Errorf("the failed stop is not in the record and on stderr: %q\n%s", r.Stop.Stderr, out)
	}
}

// TestCleanupRecordsTheAttemptKeyOrSaysWhyNot: under the patched caliper the
// record carries the attempt's key in caliper's saved result (schema 3 on). Under
// a stock caliper, which passes no key, and with a key that is not one, the
// key is null and the record, stderr and exit status say so, never a silent
// blank. The missing key is not a read-path error: whether the gate ran is
// still judged (README "As run").
func TestCleanupRecordsTheAttemptKeyOrSaysWhyNot(t *testing.T) {
	a := newAttempt(t).keyed("task-007", 3)
	code, out, r := a.cleanup()
	if want(t, "schema, key, errors", []any{code, string(r.steps["schema"]), r.TaskID, r.CaliperAttempt, r.Errors},
		[]any{0, "4", "task-007", 3, []string{}}); t.Failed() {
		t.Logf("keyed cleanup output:\n%s", out)
	}
	for _, key := range [][]string{nil, {"CALIPER_TASK_ID=task-007", "CALIPER_ATTEMPT=0"}, {"CALIPER_TASK_ID=", "CALIPER_ATTEMPT=3"}} {
		a.key = key
		code, out, r = a.cleanup()
		want(t, fmt.Sprintf("%v: exit, key", key), []any{code, r.TaskID, r.CaliperAttempt}, []any{1, nil, nil})
		if len(r.Errors) != 1 || !strings.HasPrefix(r.Errors[0], "key: ") || !strings.Contains(out, "0.19.0+q1attemptenv") {
			t.Errorf("%v: errors = %v, want the missing key alone, on stderr too:\n%s", key, r.Errors, out)
		}
		wantJudged(t, a.judge(a.built()), "no-home", "not-attempted")
	}
}
