//go:build unix

package q1_test

import (
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
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
	StageExecution stageVerdict `json:"stage_execution"`
	CIExecution    struct {
		Verdict, Reason string
		Pushes          map[string]int
	} `json:"ci_execution"`
}

type report struct {
	Attempts []judged
	Counts   struct {
		Attempts               int
		GateObservation        map[string]int `json:"gate_observation"`
		ValidWithFailedCleanup int            `json:"valid_with_failed_cleanup"`
	}
	StageExecution struct {
		Verdicts                      map[string]int
		ReviewExecutedWithTrouble     int            `json:"review_executed_with_trouble"`
		ReviewEstablished             map[string]int `json:"review_established"`
		RunsUnread                    int            `json:"runs_unread"`
		RunsReviewStartedNotCompleted int            `json:"runs_review_started_not_completed"`
	} `json:"stage_execution"`
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
func (a *attempt) records() string { return filepath.Join(a.tmpdir, "caliper-q1-gate") }

// instrument runs gate_record.py and returns its exit code, stdout and stderr.
// It runs as run-full.sh runs it, after caliper removed the attempt root: from
// the hooks' TMPDIR, which outlives the root, with HOME there too, never the
// attempt's, so nothing it writes (Apple's python3 keeps its bytecode cache
// under $HOME/Library/Caches) recreates a removed root.
func (a *attempt) instrument(args ...string) (int, string, string) {
	a.t.Helper()
	judge := *a
	judge.env = append(append([]string{}, a.env...), "HOME="+a.tmpdir) // the last HOME wins (os/exec Cmd.Env)
	return judge.run(a.tmpdir, "python3", append([]string{filepath.Join(spec, "gate_record.py")}, args...)...)
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
	wantStage(t, j, "review executed", 4)
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
	wantStage(t, j, "no stage executed")
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
	wantStage(t, j, "no stage executed", 11) // terminated, and the record shows why it is not ran
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
	wantStage(t, j, "no stage executed", 11)

	// Two runs in one attempt: the donor's terminated run beside a pending
	// copy of it is ran, because one run has an end status.
	from.ID, from.Status = "0000000000000002", ""
	donor.recordPending(from)
	donor.cleanup()
	j = donor.judge(rev)
	wantJudged(t, j, "ran", "success")
	want(t, "runs", len(j.GateObservation.Runs), 2)
	wantStage(t, j, "no stage executed", 11, 11)
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

// TestGateRecordReadsARunningRunAsStartedNotFinished: a run whose agent is
// still held when cleanup reads the runs is started-not-finished, never ran.
// Each stand-in agent waits as standin.HeldWaitIdentified's held process,
// never a loop of its own (.claude/rules/agents-standin.md), so it ends on
// the release, with this test binary (TestMain's RunOwned), or with the
// service stop's signal to its group, whichever comes first, and no shell
// trap is needed. The run is started with --intent, as every run here is,
// since the gate refuses a push with no terminal above it, which is how CI
// runs; --intent blocks while the run advances, so it runs beside the test.
func TestGateRecordReadsARunningRunAsStartedNotFinished(t *testing.T) {
	a := newAttempt(t)
	release, names := filepath.Join(a.root, "release"), []string{"claude", "cursor"}
	for _, name := range names {
		script := "#!/bin/sh\n" + standin.HeldWaitIdentified(t, release, a.heldRecord(name)) + "\nexit 1\n"
		want(t, "stand-in "+name, procgroup.WriteProgram(filepath.Join(a.root, "stub", name), []byte(script), 0o755), nil)
	}
	a.serve()
	ended, output := make(chan struct{}), ""
	var failed error // set, as output is, before ended is closed
	go func() {
		defer close(ended)
		_, out, errs, err := a.execute(a.checkout, a.bin, "--home", a.home, "--json", "--intent", "ship the branch")
		output, failed = out+errs, err
	}()
	t.Cleanup(func() {
		// Released, a held stand-in's shell exits 1; the stop ends --intent
		// and any stand-in still running, even when the test failed before
		// cleanup, and is execute's so --intent is always awaited.
		want(t, "release", os.WriteFile(release, nil, 0o644), nil)
		if _, _, errs, err := a.execute(a.root, a.bin, "--home", a.home, "service", "stop", "--force"); err != nil {
			t.Errorf("service stop: %v\n%s", err, errs)
		}
		<-ended
		if failed != nil {
			t.Errorf("--intent: %v\n%s", failed, output)
		}
		for _, p := range a.held(names) {
			if !p.GoneWithin(30 * time.Second) {
				t.Errorf("held stand-in %v still runs 30s after its release", p)
			}
		}
	})
	for deadline := time.Now().Add(60 * time.Second); len(a.held(names)) == 0; time.Sleep(50 * time.Millisecond) {
		select {
		case <-ended:
			t.Fatalf("--intent returned before any stand-in agent was held: %v\n%s", failed, output)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no stand-in agent was held 60s after --intent started")
		}
	}
	if _, out, rec := a.cleanup(); rec.Outcome != "read" {
		t.Fatalf("outcome %q, want read\n%s", rec.Outcome, out)
	}
	r := a.report(a.records(), a.built(), a.name())
	j := r.Attempts[0]
	wantJudged(t, j, "started-not-finished", "success")
	if len(j.GateObservation.Runs) != 1 || j.GateObservation.Runs[0].Status != "running" {
		t.Errorf("runs = %+v, want the one running run", j.GateObservation.Runs)
	}
	want(t, "ran, started-not-finished", []int{r.Counts.GateObservation["ran"], r.Counts.GateObservation["started-not-finished"]}, []int{0, 1})
}

// heldRecord is where the stand-in agent name records its identity once held.
func (a *attempt) heldRecord(name string) string { return filepath.Join(a.root, name+".held") }

// held is every named stand-in agent that has recorded its identity, as the
// process it read itself to be, start time included, never a bare pid.
func (a *attempt) held(names []string) []standin.Process {
	var ps []standin.Process
	for _, name := range names {
		if id, err := standin.ReadHeldIdentity(a.heldRecord(name)); err == nil {
			ps = append(ps, standin.Process{Identity: id})
		}
	}
	return ps
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
	wantStage(t, j, "no stage executed")
	if v, ok := a.rawStage()["review_established"]; ok {
		t.Errorf("review_established = %v on an attempt-level verdict, want the key absent", v)
	}

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
		j := a.judge(a.built())
		wantJudged(t, j, "no-checkout", cleanup)
		wantStage(t, j, "cannot tell")
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
	wantStage(t, j, "cannot tell")

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
	wantStage(t, j, "cannot tell") // the service down at capture: row 1

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
	wantStage(t, j, "cannot tell")
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
	wantStage(t, j, "cannot tell")
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
// another attempt's name, and with one field hand-edited (malformed input,
// which no real home writes, so edited). Whether submitted_head is a commit of
// the checkout is the head check's, which TestGateRecordReadsTheHeadCheckFailClosed covers.
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

}

// TestGateRecordCountsEveryNamedAttempt: one denominator over every attempt
// named, a missing record and a malformed one (hand-written, so malformed)
// included as indeterminate, the failed cleanup counted beside "ran", and a
// record nobody named listed rather than dropped. The stage verdict counts the
// same five: the failed-stop attempt's record is edited to schema 1 (labeled
// edit: a#925's record), which is not measured and keeps its gate state.
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
	schemaOne := func(rec map[string]any) { rec["schema"] = 1; delete(rec, "stages") }
	names := []string{ran.copyRecord(dir, ran.name(), nil), failed.copyRecord(dir, failed.name(), schemaOne),
		nohome.copyRecord(dir, nohome.name(), nil), "never-written", "malformed"}
	ran.copyRecord(dir, "unlisted", nil)
	want(t, "malformed", os.WriteFile(filepath.Join(dir, "malformed.record.json"), []byte("{"), 0o644), nil)
	r := ran.report(dir, ran.built(), names...)
	want(t, "attempts", r.Counts.Attempts, 5)
	want(t, "by gate_observation", r.Counts.GateObservation, map[string]int{"ran": 2, "started-not-finished": 0, "reached-no-stage": 0,
		"never-reached": 0, "no-home": 1, "no-checkout": 0, "indeterminate": 2})
	want(t, "valid with failed cleanup", r.Counts.ValidWithFailedCleanup, 1)
	want(t, "unlisted", r.UnlistedRecords, []string{"unlisted.record.json"})
	want(t, "by stage verdict", r.StageExecution.Verdicts, map[string]int{"review executed": 1, "review started, not completed": 0,
		"stages executed, review not executed": 0, "no stage executed": 1, "cannot tell": 2, "not measured": 1})
	want(t, "by review_established", r.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 1, "null": 4})
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

// in is the same attempt reporting to t, a subtest of its own test, so a
// helper that fails stops the subtest rather than its parent.
func (a *attempt) in(t *testing.T) *attempt {
	c := *a
	c.t = t
	return &c
}

// removedAttempt is an attempt made inside its own TMPDIR, as caliper makes
// one, with a run the gate held and cleanup.sh run on it; then the attempt
// root is removed, as caliper 0.19.0 removes it when cleanup returns
// (runner.py _run_attempt, workdir.py AttemptWorkdir.__exit__). Only the
// record, under the TMPDIR, is left to judge.
func removedAttempt(t *testing.T) (*attempt, string) {
	t.Helper()
	tmpdir, err := standin.TempDir("q1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpdir) })
	a := newAttemptIn(t, tmpdir, "caliper-r1")
	a.serve()
	id := a.startRun()
	a.cleanup()
	if !a.stopped() {
		t.Fatal("the service still runs after cleanup")
	}
	if err := os.RemoveAll(a.root); err != nil {
		t.Fatal(err)
	}
	return a, id
}

// TestGateRecordJudgesARunAfterCaliperRemovesTheAttempt is #941's finding A:
// the run's submitted_head was checked in the checkout by cleanup.sh while it
// existed, so the record judges after the attempt root is gone, as every
// record under run-full.sh is judged. Before the head check moved into
// cleanup.sh this was indeterminate: the judge asked a checkout that no
// longer exists.
func TestGateRecordJudgesARunAfterCaliperRemovesTheAttempt(t *testing.T) {
	a, id := removedAttempt(t)
	j := a.judge(a.built())
	wantJudged(t, j, "ran", "success")
	wantStage(t, j, "review executed", 4)
	if len(j.GateObservation.Runs) != 1 || j.GateObservation.Runs[0].ID != id {
		t.Fatalf("runs = %+v, want the one held run %s", j.GateObservation.Runs, id)
	}
}

// TestGateRecordReadsTheHeadCheckFailClosed: with the checkout gone, nothing
// but a head check cleanup.sh wrote for exactly this attempt, run and sha can
// make a run count, and a verified head is not a judged outcome. The first
// case is real: a head the checkout did not hold at capture (its refs deleted
// and pruned before cleanup.sh ran) stays cannot tell once the checkout is
// gone. The rest are hand edits of a real record (malformed input, which
// cleanup.sh does not write, so edited), each cannot tell.
func TestGateRecordReadsTheHeadCheckFailClosed(t *testing.T) {
	t.Run("a head the capture did not find", func(t *testing.T) {
		tmpdir, err := standin.TempDir("q1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmpdir) })
		a := newAttemptIn(t, tmpdir, "caliper-p1")
		a.serve()
		head := a.stored(a.startRun()).SubmittedHead
		prune := `git checkout -q --detach main && git for-each-ref --contains "$1" --format='%(refname)' | while read -r ref; do git update-ref -d "$ref"; done` +
			` && git reflog expire --expire=now --all && git -c gc.auto=0 gc -q --prune=now && ! git cat-file -e "$1^{commit}"`
		if code, _, errs := a.run(a.checkout, "sh", "-c", prune, "prune", head); code != 0 {
			t.Fatalf("pruning %s from the checkout: exit %d\n%s", head, code, errs)
		}
		_, _, r := a.cleanup()
		want(t, "is_commit at capture", len(r.HeadChecks) == 1 && r.HeadChecks[0]["is_commit"] == false, true)
		if err := os.RemoveAll(a.root); err != nil {
			t.Fatal(err)
		}
		j := a.judge(a.built())
		wantJudged(t, j, "", "success")
		wantReason(t, j, "no head check")
		wantStage(t, j, "cannot tell")
	})

	a, id := removedAttempt(t)
	rev := a.built()
	edited := filepath.Join(a.tmpdir, "edited")
	checks := func(rec map[string]any) []any { return rec["head_checks"].([]any) }
	entry := func(rec map[string]any) map[string]any { return checks(rec)[0].(map[string]any) }
	clone := func(m map[string]any) map[string]any {
		c := map[string]any{}
		for k, v := range m {
			c[k] = v
		}
		return c
	}
	other := strings.Repeat("e", 40)
	for _, c := range []struct {
		name, reason string
		edit         func(map[string]any)
	}{
		{"no head_checks field", "no head check", func(rec map[string]any) { delete(rec, "head_checks") }},
		{"a schema 3 record", "no head check", func(rec map[string]any) { rec["schema"] = 3; delete(rec, "head_checks") }},
		{"no entry", "no head check", func(rec map[string]any) { rec["head_checks"] = []any{} }},
		{"head_checks not a list", "not a list", func(rec map[string]any) { rec["head_checks"] = entry(rec) }},
		{"an entry not an object", "not a list", func(rec map[string]any) { rec["head_checks"] = []any{"ok"} }},
		{"a duplicate entry", "no head check", func(rec map[string]any) { rec["head_checks"] = append(checks(rec), clone(entry(rec))) }},
		{"an entry for no listed run", "entries for 1 runs", func(rec map[string]any) {
			extra := clone(entry(rec))
			extra["run_id"] = "another-run"
			rec["head_checks"] = append(checks(rec), extra)
		}},
		{"another run's entry", "no head check", func(rec map[string]any) { entry(rec)["run_id"] = "another-run" }},
		{"another sha", "no head check", func(rec map[string]any) { entry(rec)["submitted_head"] = other }},
		{"another task", "no head check", func(rec map[string]any) { entry(rec)["task_id"] = "task-002" }},
		{"another attempt number", "no head check", func(rec map[string]any) { entry(rec)["caliper_attempt"] = 2 }},
		{"an attempt number as a string", "no head check", func(rec map[string]any) { entry(rec)["caliper_attempt"] = "1" }},
		{"an empty task id", "no head check", func(rec map[string]any) { rec["task_id"], entry(rec)["task_id"] = "", "" }},
		{"a record with no key", "no head check", func(rec map[string]any) {
			rec["task_id"], rec["caliper_attempt"] = nil, nil
			entry(rec)["task_id"], entry(rec)["caliper_attempt"] = nil, nil
		}},
		{"is_commit false", "no head check", func(rec map[string]any) { entry(rec)["is_commit"] = false }},
		{"is_commit a string", "no head check", func(rec map[string]any) { entry(rec)["is_commit"] = "true" }},
		{"is_commit absent", "no head check", func(rec map[string]any) { delete(entry(rec), "is_commit") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := a.in(t)
			j := a.report(edited, rev, a.copyRecord(edited, a.name(), c.edit)).Attempts[0]
			wantJudged(t, j, "", "success")
			wantReason(t, j, c.reason)
			wantStage(t, j, "cannot tell")
		})
	}

	t.Run("a repeated key in the record", func(t *testing.T) {
		a := a.in(t)
		data, err := os.ReadFile(filepath.Join(a.records(), a.name()+".record.json"))
		if err != nil {
			t.Fatal(err)
		}
		// A second head_checks key that would win last, stating the run verified.
		doubled := strings.Replace(string(data), "{", `{"head_checks": [], `, 1)
		if err := errors.Join(os.MkdirAll(edited, 0o755), os.WriteFile(filepath.Join(edited, a.name()+".record.json"), []byte(doubled), 0o644)); err != nil {
			t.Fatal(err)
		}
		j := a.report(edited, rev, a.name()).Attempts[0]
		wantJudged(t, j, "", "failure")
		wantReason(t, j, "more than once")
	})

	t.Run("a verified head with no stage evidence", func(t *testing.T) {
		a := a.in(t)
		j := a.report(edited, rev, a.copyRecord(edited, a.name(), func(rec map[string]any) {
			delete(rec["stages"].(map[string]any), id)
		})).Attempts[0]
		wantJudged(t, j, "ran", "success")
		wantStage(t, j, "cannot tell", 2)
	})
}
