//go:build unix

package q1_test

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// judged is one attempt in gate_record.py's report; README.md's "Gate record
// instrument" owns the format.
type judged struct {
	Attempt         string
	GateObservation struct {
		Validity, State, Reason string
		Runs                    []struct {
			ID, Status    string
			SubmittedHead string `json:"submitted_head"`
			Build         struct {
				Revision string
				Modified bool
			}
			ConfigDigest string `json:"config_digest"`
		}
	} `json:"gate_observation"`
	CleanupStatus struct {
		Status, Reason string
		Exit, Stderr   any
	} `json:"cleanup_status"`
}

type report struct {
	Attempts []judged
	Counts   struct {
		Attempts               int
		GateObservation        map[string]int `json:"gate_observation"`
		ValidWithFailedCleanup int            `json:"valid_with_failed_cleanup"`
	}
	StageExecution  string   `json:"stage_execution"`
	UnlistedRecords []string `json:"unlisted_records"`
}

// built is the vcs.revision and vcs.modified the toolchain stamped into a
// binary, as gate_record.py's --revision and --modified take them.
type built struct{ revision, modified string }

// args are the instrument's arguments for this build.
func (b built) args() []string { return []string{"--revision", b.revision, "--modified", b.modified} }

// built reads the built binary's stamp from the file rather than from
// anything the binary reports.
func (a *attempt) built() built {
	a.t.Helper()
	info, err := buildinfo.ReadFile(a.bin)
	if err != nil {
		a.t.Fatal(err)
	}
	var b built
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.revision = s.Value
		case "vcs.modified":
			b.modified = s.Value
		}
	}
	if b.revision == "" || b.modified == "" {
		a.t.Fatalf("%s carries no vcs.revision or vcs.modified: %+v", a.bin, b)
	}
	return b
}

func (a *attempt) name() string    { return filepath.Base(a.root) }
func (a *attempt) records() string { return filepath.Join(a.root, "caliper-q1-gate") }

// instrument runs gate_record.py and returns its exit code, stdout and stderr.
func (a *attempt) instrument(args ...string) (int, string, string) {
	a.t.Helper()
	return a.run(a.root, "python3", append([]string{filepath.Join(spec, "gate_record.py")}, args...)...)
}

// report runs the instrument over records for the named attempts, failing
// the test unless it answers with a report.
func (a *attempt) report(records string, b built, attempts ...string) report {
	a.t.Helper()
	code, out, errs := a.instrument(append(append(b.args(), "--records", records), attempts...)...)
	var r report
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil || len(r.Attempts) != len(attempts) {
		a.t.Fatalf("gate_record.py: exit %d, %v\n%s%s", code, err, out, errs)
	}
	return r
}

// judge is the instrument's answer for this attempt's own record alone.
func (a *attempt) judge(b built) judged {
	a.t.Helper()
	return a.report(a.records(), b, a.name()).Attempts[0]
}

// wantJudged checks both fields; state is the valid state, or "" for indeterminate.
func wantJudged(t *testing.T, j judged, state, cleanup string) {
	t.Helper()
	validity := "valid"
	if state == "" {
		validity = "indeterminate"
	}
	if got := j.GateObservation.Validity + " " + j.GateObservation.State + " / " + j.CleanupStatus.Status; got != validity+" "+state+" / "+cleanup {
		t.Errorf("gate_observation / cleanup_status = %q, want %q: %+v", got, validity+" "+state+" / "+cleanup, j)
	}
}

func wantReason(t *testing.T, j judged, part string) {
	t.Helper()
	if !strings.Contains(j.GateObservation.Reason, part) {
		t.Errorf("reason = %q, want it to name %q", j.GateObservation.Reason, part)
	}
}

// startRun starts a run the gate holds at review and returns its id.
func (a *attempt) startRun() string {
	a.t.Helper()
	var started struct{ Record struct{ ID string } }
	if err := json.Unmarshal([]byte(a.assistant(a.checkout, "--json", "--intent", "ship the branch")), &started); err != nil || started.Record.ID == "" {
		a.t.Fatalf("no run started: %v", err)
	}
	return started.Record.ID
}

// stored reads one run from the home's database through internal/store.
func (a *attempt) stored(id string) store.Run {
	a.t.Helper()
	h, err := home.Open(a.home)
	var s *store.Store
	if err == nil {
		s, err = store.OpenReadOnly(a.t.Context(), h.Database())
	}
	if err != nil {
		a.t.Fatal(err)
	}
	defer s.Close()
	run, err := s.Run(a.t.Context(), id)
	if err != nil {
		a.t.Fatal(err)
	}
	return run
}

// TestGateRecordReadsAHeldRunAsRan is the (read, stop ok) row: the gate held
// a run, and its id, status, submitted_head, build.revision and config_digest
// reach the report exactly as the database holds them, through runs.
func TestGateRecordReadsAHeldRunAsRan(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	id := a.startRun()
	a.cleanup()
	rev := a.built()
	r := a.report(a.records(), rev, a.name())
	j := r.Attempts[0]
	wantJudged(t, j, "ran", "success")
	want(t, "stop exit", j.CleanupStatus.Exit, 0)
	want(t, "counts", []int{r.Counts.Attempts, r.Counts.GateObservation["ran"], r.Counts.ValidWithFailedCleanup}, []int{1, 1, 0})
	want(t, "stage_execution (#926)", r.StageExecution, "not measured")
	if len(j.GateObservation.Runs) != 1 {
		t.Fatalf("runs = %+v, want the one held run", j.GateObservation.Runs)
	}
	got, db := j.GateObservation.Runs[0], a.stored(id)
	want(t, "run", []any{got.ID, got.Status, got.SubmittedHead, got.Build.Revision, got.Build.Modified, got.ConfigDigest},
		[]any{db.ID, string(db.Status), db.SubmittedHead, db.Build.Revision, db.Build.Modified, db.ConfigDigest})
	want(t, "status", got.Status, "held")
	want(t, "build is the built binary's", []any{got.Build.Revision, got.Build.Modified}, []any{rev.revision, rev.modified == "true"})
}

// TestGateRecordReadsNoRunAsNeverReached: a served home with no run.
func TestGateRecordReadsNoRunAsNeverReached(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.cleanup()
	j := a.judge(a.built())
	wantJudged(t, j, "never-reached", "success")
	want(t, "runs", len(j.GateObservation.Runs), 0)
}

// breakTrustedConfig publishes an assistant.json on main that is not JSON, so
// the gate refuses the next run before any stage under P7.
func (a *attempt) breakTrustedConfig() {
	a.t.Helper()
	if code, _, errs := a.run(a.checkout, "sh", "-c", "git checkout -q main && printf '{' > assistant.json && git add assistant.json"+
		" && git -c user.name=q1 -c user.email=q1@example.invalid commit -qm broken && git push -q origin main && git checkout -q feature/unlimited"); code != 0 {
		a.t.Fatalf("breaking the trusted configuration: exit %d\n%s", code, errs)
	}
}

// TestGateRecordReadsAPendingRunAsReachedNoStage, the limit beside it, and
// two runs in one attempt.
// The donor's run is refused before any stage (a broken trusted config) and
// ends terminated, which status alone reports as ran: the record carries no
// stage evidence. The second home's run is written through internal/store
// with the donor's build and config digest and this home's own repository,
// branch and heads, while no service runs: a run recorded and never started,
// which the service reports pending.
func TestGateRecordReadsAPendingRunAsReachedNoStage(t *testing.T) {
	donor := newAttempt(t)
	donor.serve()
	donor.breakTrustedConfig()
	if code, out, _ := donor.run(donor.checkout, donor.bin, "--home", donor.home, "--json", "--intent", "ship the branch"); code == 0 {
		t.Fatalf("a run under a broken trusted config was not refused:\n%s", out)
	}
	donor.cleanup()
	rev := donor.built()
	j := donor.judge(rev)
	wantJudged(t, j, "ran", "success")
	if len(j.GateObservation.Runs) != 1 || j.GateObservation.Runs[0].Status != "terminated" {
		t.Fatalf("donor runs = %+v, want one terminated run", j.GateObservation.Runs)
	}
	from := donor.stored(j.GateObservation.Runs[0].ID)

	a := newAttempt(t)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	var initialized struct{ Repository struct{ ID string } }
	if err := json.Unmarshal([]byte(a.assistant(a.checkout, "--json", "init")), &initialized); err != nil || initialized.Repository.ID == "" {
		t.Fatalf("init named no repository: %v", err)
	}
	_, head, _ := a.run(a.checkout, "git", "rev-parse", "feature/unlimited")
	_, base, _ := a.run(a.checkout, "git", "rev-parse", "main")
	a.recordPending(store.Run{ID: "0000000000000001", RepositoryID: initialized.Repository.ID,
		Branch: "feature/unlimited", SubmittedHead: strings.TrimSpace(head), Base: strings.TrimSpace(base),
		Intent: from.Intent, IntentSource: from.IntentSource, Build: from.Build, ConfigDigest: from.ConfigDigest})
	a.cleanup()
	j = a.judge(rev)
	wantJudged(t, j, "reached-no-stage", "success")
	if len(j.GateObservation.Runs) != 1 || j.GateObservation.Runs[0].Status != "pending" {
		t.Errorf("runs = %+v, want the one pending run", j.GateObservation.Runs)
	}

	// Two runs in one attempt: the donor's terminated run beside a pending
	// copy of it is ran, because one run has an end status.
	from.ID, from.Status = "0000000000000002", ""
	donor.recordPending(from)
	donor.cleanup()
	j = donor.judge(rev)
	wantJudged(t, j, "ran", "success")
	want(t, "runs", len(j.GateObservation.Runs), 2)
}

// recordPending writes run through internal/store while no service runs, so
// it is recorded and never started, then starts the service, which reports
// such a run pending.
func (a *attempt) recordPending(run store.Run) {
	a.t.Helper()
	h, err := home.Open(a.home)
	var s *store.Store
	if err == nil {
		s, err = store.Open(a.t.Context(), h.Database(), store.WithRedactor(redact.New()))
	}
	if err == nil {
		_, err = s.CreateRun(a.t.Context(), run)
		err = errors.Join(err, s.Close())
	}
	if err != nil {
		a.t.Fatal(err)
	}
	a.assistant(a.root, "service", "start")
	a.t.Cleanup(func() { a.run(a.root, a.bin, "--home", a.home, "service", "stop", "--force") })
}

// TestGateRecordReadsARunningRunAsStartedNotFinished: a stand-in agent that
// blocks until released keeps a run running through capture. That is
// started, not finished, and never counted as ran. The run is started with
// --intent, as every other run here is, because the gate refuses a push from
// a process with no terminal above it, which is how CI runs; --intent blocks
// while the run advances, so it runs beside the test, and its exit is
// awaited before the test ends.
func TestGateRecordReadsARunningRunAsStartedNotFinished(t *testing.T) {
	a := newAttempt(t)
	agents, release := filepath.Join(a.root, "agents"), filepath.Join(a.root, "release")
	want(t, "agents dir", os.Mkdir(agents, 0o755), nil)
	// Each stand-in names itself by pid in agents and blocks, bounded at 60s by
	// the clock, until released. The test waits until every named pid is gone,
	// which holds however it ended: released, or killed with the service's
	// agents, where a shell such as dash runs no exit trap.
	script := fmt.Sprintf("#!/bin/sh\n: > %[1]s/$$\nend=$(($(date +%%s) + 60))\n"+
		"while [ ! -e %[2]s ] && [ \"$(date +%%s)\" -lt \"$end\" ]; do sleep 0.1; done\nexit 1\n",
		procgroup.ShellQuote(agents), procgroup.ShellQuote(release))
	for _, name := range []string{"claude", "cursor"} {
		want(t, "stand-in "+name, procgroup.WriteProgram(filepath.Join(a.root, "stub", name), []byte(script), 0o755), nil)
	}
	started := func() []int {
		entries, _ := os.ReadDir(agents)
		pids := make([]int, 0, len(entries))
		for _, e := range entries {
			if pid, err := strconv.Atoi(e.Name()); err == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	a.serve()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o644)
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			alive := []int{}
			for _, pid := range started() {
				if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
					alive = append(alive, pid)
				}
			}
			if len(alive) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("stand-in agents %v still run 30s after their release", alive)
				return
			}
		}
	})
	intent := a.command(a.checkout, a.bin, "--home", a.home, "--json", "--intent", "ship the branch")
	var output strings.Builder // read only once ended is closed
	intent.Stdout, intent.Stderr = &output, &output
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	ended := make(chan error, 1)
	go func() { ended <- procgroup.Run(ctx, intent, 5*time.Second) }()
	var once sync.Once
	reap := func() error { // ends --intent, waiting up to 30s for it to return on its own
		var err error
		once.Do(func() {
			select {
			case err = <-ended:
			case <-time.After(30 * time.Second):
				cancel()
				<-ended // its exit error is dropped, so a stop for timing out is never read as an ordinary exit
				err = errors.New("--intent was still running 30s on, so it was stopped")
			}
			cancel()
		})
		return err
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644); _ = reap() })
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if len(started()) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = os.WriteFile(release, nil, 0o644)
			t.Fatalf("no stand-in agent started 30s after --intent: %v\n%s", reap(), output.String())
		}
	}
	a.cleanup()
	_ = os.WriteFile(release, nil, 0o644)
	if err := reap(); err != nil && !errors.As(err, new(*exec.ExitError)) {
		t.Errorf("%v\n%s", err, output.String())
	}
	r := a.report(a.records(), a.built(), a.name())
	j := r.Attempts[0]
	wantJudged(t, j, "started-not-finished", "success")
	if len(j.GateObservation.Runs) != 1 || j.GateObservation.Runs[0].Status != "running" {
		t.Errorf("runs = %+v, want the one running run", j.GateObservation.Runs)
	}
	want(t, "counts", []int{r.Counts.GateObservation["ran"], r.Counts.GateObservation["started-not-finished"]}, []int{0, 1})
}

// TestGateRecordKeepsAValidReadWhenTheStopFails is the (read, stop failed)
// row: the observation stays valid, the cleanup is a failure, and the count
// of valid attempts with a failed cleanup says so beside "ran".
func TestGateRecordKeepsAValidReadWhenTheStopFails(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.startRun()
	a.failStop()
	a.cleanup()
	r := a.report(a.records(), a.built(), a.name())
	j := r.Attempts[0]
	wantJudged(t, j, "ran", "failure")
	want(t, "stop exit", j.CleanupStatus.Exit, 1)
	if s, _ := j.CleanupStatus.Stderr.(string); s == "" {
		t.Errorf("the failed stop's stderr is not carried: %+v", j.CleanupStatus)
	}
	want(t, "ran", r.Counts.GateObservation["ran"], 1)
	want(t, "valid with failed cleanup", r.Counts.ValidWithFailedCleanup, 1)

	// A stop whose call raised is recorded with exit null: a failure too.
	a = newAttempt(t)
	a.serve()
	a.startRun()
	a.fault(`import subprocess
_run = subprocess.run
def run(argv, *args, **kwargs):
    if argv[-2:] == ["stop", "--force"]:
        raise OSError("injected fault in the stop")
    return _run(argv, *args, **kwargs)
subprocess.run = run
`)
	a.cleanup()
	a.env = nil
	j = a.judge(a.built())
	wantJudged(t, j, "ran", "failure")
	want(t, "stop exit", j.CleanupStatus.Exit, nil)
	want(t, "stop stderr", j.CleanupStatus.Stderr, "injected fault in the stop")
}

// TestGateRecordNamesNoHomeAndNoCheckout: the (no-home, no stop), (no-checkout,
// stop ok) and (no-checkout, stop failed) rows are named states, neither
// indeterminate nor "never reached".
func TestGateRecordNamesNoHomeAndNoCheckout(t *testing.T) {
	a := newAttempt(t)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	a.cleanup()
	j := a.judge(a.built())
	wantJudged(t, j, "no-home", "not-attempted")
	want(t, "stop exit", j.CleanupStatus.Exit, nil)

	for _, failing := range []bool{false, true} {
		a := newAttempt(t)
		a.serve()
		if err := os.Rename(a.checkout, a.checkout+".moved"); err != nil {
			t.Fatal(err)
		}
		cleanup := "success"
		if failing {
			a.failStop()
			cleanup = "failure"
		}
		a.cleanup()
		wantJudged(t, a.judge(a.built()), "no-checkout", cleanup)
	}
}

// TestGateRecordTreatsAFailedReadAsIndeterminateWhateverTheStop: the three
// error rows. A failing read with the service up stops cleanly; a service
// down at capture fails its stop too; a raise at the home check, outside
// capture()'s try, records no stop. Each is indeterminate.
func TestGateRecordTreatsAFailedReadAsIndeterminateWhateverTheStop(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	if err := errors.Join(os.Rename(a.checkout, a.checkout+".moved"), os.Mkdir(a.checkout, 0o755)); err != nil {
		t.Fatal(err)
	}
	a.cleanup()
	j := a.judge(a.built())
	wantJudged(t, j, "", "success")
	wantReason(t, j, "cleanup outcome error: runs: exit 1")

	a = newAttempt(t)
	a.serve()
	a.assistant(a.root, "service", "stop", "--force")
	if !a.stopped() {
		t.Fatal("the service still runs 30s after its stop")
	}
	a.cleanup()
	j = a.judge(a.built())
	wantJudged(t, j, "", "failure")
	wantReason(t, j, "cleanup outcome error: ")
	want(t, "stop exit", j.CleanupStatus.Exit, 1)

	a = newAttempt(t)
	a.serve()
	a.fault(`import os.path
_isdir = os.path.isdir
def isdir(p):
    if p.endswith(".assistant"):
        raise RuntimeError("injected fault before the read")
    return _isdir(p)
os.path.isdir = isdir
`)
	if _, _, r := a.cleanup(); r.Outcome != "error" || r.steps["stop"] != nil {
		t.Fatalf("outcome %q with stop %s, want error with no stop", r.Outcome, r.steps["stop"])
	}
	a.env = nil
	j = a.judge(a.built())
	wantJudged(t, j, "", "failure")
	wantReason(t, j, "cleanup outcome error: cleanup: RuntimeError('injected fault before the read')")
}

// TestGateRecordTreatsARaiseAfterTheReadAsIndeterminate (#923): a fault
// raised after cleanup.sh set outcome read leaves the record saying read with
// a cleanup error beside it. That is never a successful gate read.
func TestGateRecordTreatsARaiseAfterTheReadAsIndeterminate(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.startRun()
	a.fault(`import builtins
_sorted = builtins.sorted
def sorted(iterable, key=None, reverse=False):
    if key is str:
        raise RuntimeError("injected fault after the read")
    return _sorted(iterable, key=key, reverse=reverse)
builtins.sorted = sorted
`)
	if _, _, r := a.cleanup(); r.Outcome != "read" || len(r.Runs.Output.Runs) != 1 {
		t.Fatalf("outcome %q with runs %v, want the producer's read with the held run (#923)", r.Outcome, r.Runs.Output.Runs)
	}
	a.env = nil
	j := a.judge(a.built())
	wantJudged(t, j, "", "success")
	wantReason(t, j, "injected fault after the read")
}

// copyRecord writes this attempt's record as name's in dir, edited by edit
// when it is not nil, and returns name.
func (a *attempt) copyRecord(dir, name string, edit func(map[string]any)) string {
	a.t.Helper()
	data, err := os.ReadFile(filepath.Join(a.records(), a.name()+".record.json"))
	var rec map[string]any
	if err == nil {
		err = json.Unmarshal(data, &rec)
	}
	if err == nil && edit != nil {
		edit(rec)
		data, err = json.Marshal(rec)
	}
	if err == nil {
		err = errors.Join(os.MkdirAll(dir, 0o755), os.WriteFile(filepath.Join(dir, name+".record.json"), data, 0o644))
	}
	if err != nil {
		a.t.Fatal(err)
	}
	return name
}

// TestGateRecordChecksTheRunsAgainstTheCheckoutAndTheBinary: the same real
// record is valid, then indeterminate against another revision or modified
// flag, under
// another attempt's name, with one field hand-edited (malformed input, which
// no real home writes, so edited), and once the checkout no longer holds
// submitted_head.
func TestGateRecordChecksTheRunsAgainstTheCheckoutAndTheBinary(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.startRun()
	a.cleanup()
	rev := a.built()
	wantJudged(t, a.judge(rev), "ran", "success")

	other := built{strings.Repeat("0", 40), rev.modified}
	if rev.revision == other.revision {
		other.revision = strings.Repeat("1", 40)
	}
	j := a.judge(other)
	wantJudged(t, j, "", "success")
	wantReason(t, j, "build.revision")
	// The same revision from a build that was (or was not) modified is not this build.
	other = built{rev.revision, map[string]string{"true": "false", "false": "true"}[rev.modified]}
	j = a.judge(other)
	wantJudged(t, j, "", "success")
	wantReason(t, j, "build.modified")

	edited := filepath.Join(a.root, "edited")
	j = a.report(edited, rev, a.copyRecord(edited, "another-attempt", nil)).Attempts[0]
	wantJudged(t, j, "", "success")
	wantReason(t, j, "not this attempt's")
	run := func(rec map[string]any) map[string]any {
		return rec["runs"].(map[string]any)["output"].(map[string]any)["runs"].([]any)[0].(map[string]any)
	}
	for _, c := range []struct {
		reason string
		edit   func(map[string]any)
	}{
		{"config_digest", func(rec map[string]any) { run(rec)["config_digest"] = "cfg" }},
		{"unknown status", func(rec map[string]any) { run(rec)["status"] = "shipped" }},
		{"repositories", func(rec map[string]any) { run(rec)["repository_id"] = nil }},
		{"not made from the record's checkout", func(rec map[string]any) { rec["runs"].(map[string]any)["cwd"] = a.root }},
		{"not a row of the mapping table", func(rec map[string]any) { rec["outcome"] = "maybe" }},
		{"not a successful runs document", func(rec map[string]any) { rec["runs"].(map[string]any)["output"] = map[string]any{} }},
		{"not this attempt's", func(rec map[string]any) { rec["home"] = filepath.Join(a.root, "elsewhere") }},
	} {
		j = a.report(edited, rev, a.copyRecord(edited, a.name(), c.edit)).Attempts[0]
		wantJudged(t, j, "", "success")
		wantReason(t, j, c.reason)
	}

	if err := os.Rename(a.checkout, a.checkout+".moved"); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := a.run(a.work, "sh", "-c", "git init -q work && git -C work -c user.name=q1 -c user.email=q1@example.invalid commit -q --allow-empty -m other"); code != 0 {
		t.Fatalf("another repository at the checkout: exit %d\n%s", code, errs)
	}
	j = a.judge(rev)
	wantJudged(t, j, "", "success")
	wantReason(t, j, "submitted_head")
}

// TestGateRecordCountsEveryNamedAttempt: one denominator over every attempt
// named, a missing record and a malformed one (hand-written, so malformed)
// included as indeterminate, the failed cleanup counted beside "ran", and a
// record nobody named listed rather than dropped.
func TestGateRecordCountsEveryNamedAttempt(t *testing.T) {
	ran, failed, nohome := newAttempt(t), newAttempt(t), newAttempt(t)
	ran.serve()
	ran.startRun()
	ran.cleanup()
	failed.serve()
	failed.startRun()
	failed.failStop()
	failed.cleanup()
	if code, _, errs := nohome.run(nohome.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	nohome.cleanup()
	dir := filepath.Join(ran.root, "all")
	names := []string{ran.copyRecord(dir, ran.name(), nil), failed.copyRecord(dir, failed.name(), nil),
		nohome.copyRecord(dir, nohome.name(), nil), "never-written", "malformed"}
	ran.copyRecord(dir, "unlisted", nil)
	want(t, "malformed", os.WriteFile(filepath.Join(dir, "malformed.record.json"), []byte("{"), 0o644), nil)
	r := ran.report(dir, ran.built(), names...)
	want(t, "attempts", r.Counts.Attempts, 5)
	want(t, "by gate_observation", r.Counts.GateObservation, map[string]int{"ran": 2, "started-not-finished": 0, "reached-no-stage": 0,
		"never-reached": 0, "no-home": 1, "no-checkout": 0, "indeterminate": 2})
	want(t, "valid with failed cleanup", r.Counts.ValidWithFailedCleanup, 1)
	want(t, "unlisted", r.UnlistedRecords, []string{"unlisted.record.json"})
	wantJudged(t, r.Attempts[3], "", "failure")
	wantReason(t, r.Attempts[3], "no record file")
	wantJudged(t, r.Attempts[4], "", "failure")
	wantReason(t, r.Attempts[4], "record unreadable")
}

// TestGateRecordRefusesWithoutACheckableRevision: with no full revision, no
// modified flag, no attempts, or an attempt named twice (it would count twice
// in the one denominator), the instrument refuses rather than reporting.
func TestGateRecordRefusesWithoutACheckableRevision(t *testing.T) {
	a := newAttempt(t)
	want(t, "mkdir", os.Mkdir(a.records(), 0o755), nil)
	b := a.built()
	for _, args := range [][]string{
		{"--revision", "", "--modified", b.modified, "--records", a.records(), a.name()},
		{"--revision", "abc", "--modified", b.modified, "--records", a.records(), a.name()},
		{"--revision", b.revision, "--records", a.records(), a.name()},
		{"--revision", b.revision, "--modified", "maybe", "--records", a.records(), a.name()},
		{"--revision", b.revision, "--modified", b.modified, "--records", a.records()},
		{"--revision", b.revision, "--modified", b.modified, "--records", a.records(), a.name(), a.name()},
		{"--revision", b.revision, "--modified", b.modified, "--records", filepath.Join(a.root, "absent"), a.name()},
	} {
		if code, out, _ := a.instrument(args...); code != 2 {
			t.Errorf("gate_record.py %v: exit %d, want 2\n%s", args, code, out)
		}
	}
}
