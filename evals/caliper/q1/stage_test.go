//go:build unix

package q1_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// stageVerdict is one attempt's stage_execution; README.md's "Stage
// execution" owns the format.
type stageVerdict struct {
	Verdict, Reason   string
	ReviewEstablished *bool `json:"review_established"`
	EstablishedRuns   []struct {
		ID          string
		Established *bool `json:"review_established"` // null when a not_started finding is in the report
		From        any
	} `json:"review_established_runs"`
	Trouble struct {
		Unread                    int
		ReviewStartedNotCompleted int `json:"review_started_not_completed"`
	}
	Runs []struct {
		ID, State        string
		Row              int
		ReviewExecutions int `json:"review_executions"`
	}
}

// step is one call as cleanup.sh records it.
type step struct {
	Exit   any
	Stderr string
	Output map[string]any
}

// stageEntry is one run's stage evidence in the record.
type stageEntry struct {
	Report      step
	ReportAfter step `json:"report_after"`
	Logs        map[string]step
}

// stageEntries is the record's stages object, keyed by run id.
func (a *attempt) stageEntries() map[string]stageEntry {
	a.t.Helper()
	var rec struct{ Stages map[string]stageEntry }
	data, err := os.ReadFile(filepath.Join(a.records(), a.name()+".record.json"))
	if err == nil {
		err = json.Unmarshal(data, &rec)
	}
	if err != nil {
		a.t.Fatal(err)
	}
	return rec.Stages
}

// wantStage checks an attempt's verdict and the table row each of its runs
// matched, in the order the runs step listed them.
func wantStage(t *testing.T, j judged, verdict string, rows ...int) {
	t.Helper()
	got := []int{}
	for _, r := range j.StageExecution.Runs {
		got = append(got, r.Row)
	}
	if j.StageExecution.Verdict != verdict || fmt.Sprint(got) != fmt.Sprint(rows) {
		t.Errorf("stage_execution = %q with rows %v, want %q with rows %v: %+v", j.StageExecution.Verdict, got, verdict, rows, j.StageExecution)
	}
}

// tri renders a review_established value: true, false or null.
func tri(v *bool) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

// establishedRuns is each review-completed run's review_established, by run id.
func establishedRuns(j judged) map[string]string {
	got := map[string]string{}
	for _, r := range j.StageExecution.EstablishedRuns {
		got[r.ID] = tri(r.Established)
	}
	return got
}

// field walks a decoded JSON document by keys.
func field(doc any, keys ...string) any {
	for _, k := range keys {
		m, _ := doc.(map[string]any)
		doc = m[k]
	}
	return doc
}

// reviewOf is the review entry of a run report's stages.
func reviewOf(report map[string]any) map[string]any {
	stages, _ := report["stages"].([]any)
	for _, s := range stages {
		if m, _ := s.(map[string]any); m["stage"] == "review" {
			return m
		}
	}
	return nil
}

// reportEdit edits one read of every run's stage entry in a copied record.
func reportEdit(which string, edit func(report map[string]any)) func(map[string]any) {
	return func(rec map[string]any) {
		for _, entry := range rec["stages"].(map[string]any) {
			edit(entry.(map[string]any)[which].(map[string]any)["output"].(map[string]any))
		}
	}
}

// both applies edit to both report reads.
func both(edit func(map[string]any)) func(map[string]any) {
	return func(rec map[string]any) { reportEdit("report", edit)(rec); reportEdit("report_after", edit)(rec) }
}

// logPath is where the service says run's log for stage is.
func (a *attempt) logPath(run, stage string) string {
	a.t.Helper()
	var log struct{ Path string }
	if err := json.Unmarshal([]byte(a.assistant(a.root, "--json", "runs", run, "--log="+stage)), &log); err != nil || log.Path == "" {
		a.t.Fatalf("no log path for %s %s: %v", run, stage, err)
	}
	return log.Path
}

// restart starts the stopped service again, so the same home is captured
// once more. Tests do this to reuse a real home; cleanup.sh never does.
func (a *attempt) restart() { a.assistant(a.root, "service", "start") }

// intent starts a run with --intent and the extra arguments, and returns its id.
func (a *attempt) intent(extra ...string) string {
	a.t.Helper()
	var started struct{ Record struct{ ID string } }
	out := a.assistant(a.checkout, append([]string{"--json", "--intent", "ship the branch"}, extra...)...)
	if err := json.Unmarshal([]byte(out), &started); err != nil || started.Record.ID == "" {
		a.t.Fatalf("no run started: %v\n%s", err, out)
	}
	return started.Record.ID
}

// faultRead makes the nth stage read matching match (a Python expression on
// argv) raise in cleanup.sh's Python, so that call is recorded as failed while
// every other call reaches the real binary.
func (a *attempt) faultRead(match string, nth int) {
	a.t.Helper()
	a.fault(fmt.Sprintf(`import subprocess
_run, _seen = subprocess.run, [0]
def run(argv, *args, **kwargs):
    if %s:
        _seen[0] += 1
        if _seen[0] == %d:
            raise OSError("injected fault in a stage read")
    return _run(argv, *args, **kwargs)
subprocess.run = run
`, match, nth))
}

// scriptAgent has the service's agent be the scripted stand-in: the claude
// shim ahead of the exit-1 stubs on PATH, and the home's configuration naming
// it with the stand-in's arguments, with no fix role, as
// journey.WriteConfiguration writes it, with the keys in more beside them.
// It is called before serve.
func (a *attempt) scriptAgent(script standin.Script, more ...map[string]any) *standin.Agent {
	a.t.Helper()
	agent := standin.New(a.t, script)
	shims, err := journey.Shims()
	if err != nil {
		a.t.Fatal(err)
	}
	document := map[string]any{"agent": strings.Join(append([]string{journey.AgentShimName}, agent.Arguments()...), " "),
		"roles": map[string]any{"fix": map[string]any{}}}
	for _, m := range more {
		for k, v := range m {
			document[k] = v
		}
	}
	a.configure(document)
	a.path = shims + ":" + a.path
	return agent
}

// configure writes the home's configuration document, through internal/home
// so no home path is spelled here. It is called before serve.
func (a *attempt) configure(document map[string]any) {
	a.t.Helper()
	h, err := home.Open(a.home)
	if err == nil {
		err = h.Create()
	}
	var body []byte
	if err == nil {
		body, err = json.Marshal(document)
	}
	if err == nil {
		err = os.WriteFile(h.ConfigFile(), append(body, '\n'), 0o600)
	}
	if err != nil {
		a.t.Fatal(err)
	}
}

// rawStage is this attempt's stage_execution exactly as the instrument wrote
// it, so a key that is absent is told from one that is null.
func (a *attempt) rawStage() map[string]any {
	a.t.Helper()
	var raw struct {
		Attempts []struct {
			StageExecution map[string]any `json:"stage_execution"`
		}
	}
	_, out, errs := a.instrument(append(a.built().args(), "--records", a.records(), a.name())...)
	if err := json.Unmarshal([]byte(out), &raw); err != nil || len(raw.Attempts) != 1 {
		a.t.Fatalf("gate_record.py: %v\n%s%s", err, out, errs)
	}
	return raw.Attempts[0].StageExecution
}

// intentHeld starts a run whose review blocks, and returns the file that
// releases it once the review has begun. The stand-in agents mark that they
// started, then wait as standin.HeldWait's held process, then exit 1, so a
// released review ends as case A's does, and an ended one runs nothing after.
// --intent returns only at the run's next decision point, so it runs in the
// background through procgroup on a bounded context. A cleanup writes the
// release, then waits up to 60s for --intent to return; past that it cancels
// the context and fails the test, naming what was still running.
func (a *attempt) intentHeld() string {
	a.t.Helper()
	release, started := filepath.Join(a.root, "release"), filepath.Join(a.root, "started")
	script := fmt.Sprintf("#!/bin/sh\n: > %s\n%s\nexit 1\n", procgroup.ShellQuote(started), standin.HeldWait(a.t, release))
	for _, name := range []string{"claude", "cursor"} {
		if err := procgroup.WriteProgram(filepath.Join(a.root, "stub", name), []byte(script), 0o755); err != nil {
			a.t.Fatal(err)
		}
	}
	cmd := procgroup.Command(a.bin, "--home", a.home, "--json", "--intent", "ship the branch")
	cmd.Dir, cmd.Env = a.checkout, a.environ()
	ctx, cancel := procgroup.Bound(context.Background(), 3*time.Minute)
	finished := make(chan struct{})
	go func() { defer close(finished); _ = procgroup.Run(ctx, cmd, 5*time.Second) }()
	a.t.Cleanup(func() {
		defer cancel()
		if err := os.WriteFile(release, nil, 0o644); err != nil {
			a.t.Error(err)
		}
		select {
		case <-finished:
		case <-time.After(60 * time.Second):
			cancel()
			<-finished
			a.t.Errorf("assistant --intent still ran 60s after the release, so its context was cancelled")
		}
	})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			return release
		}
		select {
		case <-finished:
			a.t.Fatal("assistant --intent returned before the held stand-in started")
		default:
		}
		if time.Now().After(deadline) {
			a.t.Fatal("the held stand-in did not start within 30s")
		}
	}
}

// wrapLogRead puts an assistant ahead of the real one on PATH that acts on
// the review log read only, then hands the same call to the real binary. With
// a release file it writes it, then polls the real binary until the run's
// position leaves review (exit 97 past 30s, which fails the test loudly), so
// the run moves between cleanup's two report reads. Without one it only waits
// a second, so the run's activity.active_ns moves and nothing compared does.
func (a *attempt) wrapLogRead(release string) {
	a.t.Helper()
	act := "sleep 1"
	if release != "" {
		act = fmt.Sprintf(`: > %[1]s
	end=$(($(date +%%s) + 31))
	while %[2]s --home %[3]s --json runs "$rid" 2>/dev/null | grep -q '"position": *"review"'; do
		[ "$(date +%%s)" -lt "$end" ] || { echo "wrap: the review still runs 30s after its release" >&2; exit 97; }
		sleep 0.1
	done`, procgroup.ShellQuote(release), procgroup.ShellQuote(a.bin), procgroup.ShellQuote(a.home))
	}
	script := fmt.Sprintf(`#!/bin/sh
prev= rid=
for arg in "$@"; do
	[ "$arg" = --log=review ] && rid=$prev
	prev=$arg
done
if [ -n "$rid" ]; then
	%s
fi
exec %s "$@"
`, act, procgroup.ShellQuote(a.bin))
	wrap := filepath.Join(a.root, "wrap")
	if err := os.Mkdir(wrap, 0o755); err != nil {
		a.t.Fatal(err)
	}
	if err := procgroup.WriteProgram(filepath.Join(wrap, "assistant"), []byte(script), 0o755); err != nil {
		a.t.Fatal(err)
	}
	a.path = wrap + ":" + a.path
}

// TestStageReadsACompletedReviewThatEstablishedNothing is case A: the
// stand-in reviewer exits 1 and the run is held at review. The review stage
// executed (row 4) and established nothing, and both numbers show. The same
// real record, with labeled edits, then shows the consistency window on run
// identity (row 3), schema 1 as not measured (row 0), an unknown schema as
// cannot tell (row 1), and a dropped execution row (#928's shape) still
// executed. The same home captured again shows a failed second read (row 3)
// and a missing review log beside ran: true (row 4). Last, #927: a checkout
// whose own repository is gone, inside an enclosing one, is never valid.
func TestStageReadsACompletedReviewThatEstablishedNothing(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	id := a.startRun()
	code, out, _ := a.cleanup()
	want(t, "exit", code, 0)
	e := a.stageEntries()[id]
	want(t, "reads", []any{e.Report.Exit, e.ReportAfter.Exit, len(e.Logs), e.Logs["review"].Exit}, []any{0, 0, 1, 0})
	want(t, "review log written", e.Logs["review"].Output["written"], true)
	want(t, "ran in both reads", []any{reviewOf(e.Report.Output)["ran"], reviewOf(e.ReportAfter.Output)["ran"]}, []any{true, true})
	rev := a.built()
	r := a.report(a.records(), rev, a.name())
	j := r.Attempts[0]
	wantStage(t, j, "review executed", 4)
	if se := j.StageExecution; tri(se.ReviewEstablished) != "false" || len(se.EstablishedRuns) != 1 ||
		tri(se.EstablishedRuns[0].Established) != "false" || !strings.Contains(fmt.Sprint(se.EstablishedRuns[0].From), "review-not-established") ||
		se.Runs[0].ReviewExecutions != 1 || se.Trouble.Unread+se.Trouble.ReviewStartedNotCompleted != 0 {
		t.Errorf("stage_execution = %+v, want review executed, established false from review-not-established, 1 execution, no trouble\n%s", se, out)
	}
	want(t, "verdict counts", r.StageExecution.Verdicts["review executed"], 1)
	want(t, "established counts", r.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 1, "null": 0})

	edited := filepath.Join(a.root, "edited")
	judgeEdit := func(edit func(map[string]any)) judged {
		t.Helper()
		return a.report(edited, rev, a.copyRecord(edited, a.name(), edit)).Attempts[0]
	}
	// Labeled edits: the run moved to another identity between the reads, or
	// both reads name a run other than the one the runs step listed.
	wantStage(t, judgeEdit(reportEdit("report_after", func(m map[string]any) { m["record"].(map[string]any)["id"] = "0000000000000009" })), "cannot tell", 3)
	wantStage(t, judgeEdit(both(func(m map[string]any) { m["record"].(map[string]any)["id"] = "0000000000000009" })), "cannot tell", 3)
	// Labeled edit: the execution row dropped, as a forced stop drops it (#928); ran alone decides.
	wantStage(t, judgeEdit(both(func(m map[string]any) { delete(reviewOf(m), "executions") })), "review executed", 4)
	// Labeled edit: the record as a#925's cleanup.sh wrote it, schema 1 with no stages.
	j = judgeEdit(func(rec map[string]any) { rec["schema"] = 1; delete(rec, "stages") })
	wantJudged(t, j, "ran", "success")
	var raw struct {
		Attempts []struct {
			StageExecution map[string]any `json:"stage_execution"`
		}
	}
	_, rawOut, _ := a.instrument(append(rev.args(), "--records", edited, a.name())...)
	want(t, "decode", json.Unmarshal([]byte(rawOut), &raw), nil)
	want(t, "schema 1 stage_execution, exactly", raw.Attempts[0].StageExecution, map[string]any{"verdict": "not measured"})
	for _, unknown := range []any{4, true} { // JSON true is not schema 1
		j = judgeEdit(func(rec map[string]any) { rec["schema"] = unknown })
		wantJudged(t, j, "", "success")
		wantStage(t, j, "cannot tell")
	}

	// The same home again: the second report read fails (row 3, exit 1).
	a.restart()
	a.faultRead(fmt.Sprintf("argv[-2:] == ['runs', %q]", id), 2)
	code, out, _ = a.cleanup()
	a.env = nil
	want(t, "exit with a failed second read", code, 1)
	if !strings.Contains(out, "run-after:"+id+": exit None: injected fault in a stage read") {
		t.Errorf("the failed second read is not on stderr:\n%s", out)
	}
	j = a.judge(rev)
	wantJudged(t, j, "ran", "success")
	wantStage(t, j, "cannot tell", 3)

	// The same home again with review.log removed (labeled edit): ran decides, not the log.
	a.restart()
	want(t, "remove review.log", os.Remove(a.logPath(id, "review")), nil)
	a.cleanup()
	want(t, "review log written", a.stageEntries()[id].Logs["review"].Output["written"], false)
	wantStage(t, a.judge(rev), "review executed", 4)

	// #927: the checkout's .git moved away inside an enclosing repository, and
	// the record's submitted_head set to the enclosing commit (labeled edits).
	if code, _, errs := a.run(a.root, "sh", "-c", "git init -q . && git -c user.name=q1 -c user.email=q1@example.invalid commit -q --allow-empty -m enclosing"); code != 0 {
		t.Fatalf("an enclosing repository: exit %d\n%s", code, errs)
	}
	_, enclosing, _ := a.run(a.root, "git", "rev-parse", "HEAD")
	want(t, "move .git", os.Rename(filepath.Join(a.checkout, ".git"), filepath.Join(a.root, "checkout.git")), nil)
	j = judgeEdit(func(rec map[string]any) {
		rec["runs"].(map[string]any)["output"].(map[string]any)["runs"].([]any)[0].(map[string]any)["submitted_head"] = strings.TrimSpace(enclosing)
	})
	wantJudged(t, j, "", "success")
	wantReason(t, j, "submitted_head")
	wantStage(t, j, "cannot tell")
}

// afterReview is the stages a run whose review this test is about skips: the
// subject's own test command, and the fix round its failure starts, depend on
// what the machine has (go on the attempt's PATH), which no stage verdict here
// is about.
const afterReview = "test,document,lint,push,pr,ci"

// TestStageReadsAnEstablishedReview: the reviewer is the scripted stand-in
// and returns a review with no finding, so the review stage executed and a
// review was established (row 4, true). A labeled edit adding a
// not-established finding id to the second read alone is row 3: the finding
// ids are in the consistency window, because review_established reads them.
func TestStageReadsAnEstablishedReview(t *testing.T) {
	a := newAttempt(t)
	agent := a.scriptAgent(standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
	}})
	a.serve()
	id := a.intent("--skip", afterReview)
	a.cleanup()
	reviews := 0
	for _, c := range agent.Calls() {
		if c.Answered() && c.Step == 0 {
			reviews++
		}
	}
	want(t, "scripted reviews", reviews, 1)
	rev := a.built()
	j := a.judge(rev)
	wantStage(t, j, "review executed", 4)
	if se := j.StageExecution; tri(se.ReviewEstablished) != "true" || len(se.EstablishedRuns) != 1 ||
		se.EstablishedRuns[0].ID != id || tri(se.EstablishedRuns[0].Established) != "true" {
		t.Errorf("stage_execution = %+v, want run %s established", se, id)
	}
	for _, found := range j.StageExecution.EstablishedRuns[0].From.([]any) {
		if strings.HasPrefix(found.(string), "review-") {
			t.Errorf("established from finding ids %v, which name %s", j.StageExecution.EstablishedRuns[0].From, found)
		}
	}
	edited := filepath.Join(a.root, "edited")
	j = a.report(edited, rev, a.copyRecord(edited, a.name(), reportEdit("report_after", func(m map[string]any) {
		report := reviewOf(m)["report"].(map[string]any)
		report["findings"] = []any{map[string]any{"id": "review-not-established"}}
	}))).Attempts[0]
	wantStage(t, j, "cannot tell", 3)

	// The r4 case (labeled edit): the same reviewed report with the usage
	// ceiling's finding appended in both reads, as internal/pipeline's stage
	// node appends what a fix round the ceiling refused held. The finding is
	// the product's own (machine.CeilingHeldFinding). Through the CLI it
	// needs the ceiling's window to expire between the refused round and the
	// stage's immediate re-run, since a retry drops the inherited hold, so it
	// is planted. A reviewer did run here, and the mark cannot say whose it
	// is: review executed, review_established null, never false.
	held, err := json.Marshal(machine.CeilingHeldFinding(machine.UsageCeiling{Standing: machine.CeilingReached, Window: "1h"}))
	var finding map[string]any
	if err == nil {
		err = json.Unmarshal(held, &finding)
	}
	want(t, "the ceiling's finding", err, nil)
	want(t, "it is marked not started", finding["not_started"], true)
	j = a.report(edited, rev, a.copyRecord(edited, a.name(), both(func(m map[string]any) {
		report := reviewOf(m)["report"].(map[string]any)
		found, _ := report["findings"].([]any)
		report["findings"] = append(found, finding)
	}))).Attempts[0]
	wantStage(t, j, "review executed", 4)
	want(t, "established runs, inherited hold", establishedRuns(j), map[string]string{id: "null"})
	want(t, "attempt review_established, inherited hold", tri(j.StageExecution.ReviewEstablished), "null")
}

// TestStageFoldsEstablishedAsAny: the first review call fails and the second
// returns a review. Run 1 is held not established and cancelled, run 2 is
// established, and the attempt is established because one run is.
func TestStageFoldsEstablishedAsAny(t *testing.T) {
	a := newAttempt(t)
	a.scriptAgent(standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Reply: standin.Failed("the reviewer gave up")},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
	}})
	a.serve()
	first := a.intent()
	a.assistant(a.checkout, "--json", "--cancel="+first)
	second := a.intent("--skip", afterReview)
	a.cleanup()
	j := a.judge(a.built())
	wantStage(t, j, "review executed", 4, 4)
	want(t, "runs", []string{j.StageExecution.Runs[0].ID, j.StageExecution.Runs[1].ID}, []string{second, first})
	want(t, "established runs", establishedRuns(j), map[string]string{first: "false", second: "true"})
	want(t, "attempt review_established: one run established", tri(j.StageExecution.ReviewEstablished), "true")
}

// TestStageReadsARunRefusedBeforeAnyStage is case B: a broken trusted
// configuration refuses the run before any stage. The report has no stages,
// both reads agree, and all nine logs, pipeline.Order's names, read unwritten
// (row 11). Labeled edits: any one log missing (each of the nine in turn) is
// row 13, never row 11; status passed, which no row names with no stages, is
// the catch-all (row 14). A log planted for the run is row 13. Then, with the
// configuration restored, a second run held at review folds to executed.
func TestStageReadsARunRefusedBeforeAnyStage(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.breakTrustedConfig()
	if code, out, _ := a.run(a.checkout, a.bin, "--home", a.home, "--json", "--intent", "ship the branch"); code == 0 {
		t.Fatalf("a run under a broken trusted config was not refused:\n%s", out)
	}
	a.cleanup()
	entries := a.stageEntries()
	if len(entries) != 1 {
		t.Fatalf("stages = %v, want the one refused run", entries)
	}
	var id string
	var e stageEntry
	for id, e = range entries {
	}
	var names, logged []string
	for _, s := range pipeline.Order() {
		names = append(names, s.String())
	}
	for s, l := range e.Logs {
		logged = append(logged, s)
		want(t, s+" log", []any{l.Exit, l.Output["written"]}, []any{0, false})
	}
	sort.Strings(names)
	sort.Strings(logged)
	want(t, "logs read", logged, names)
	_, has := e.Report.Output["stages"]
	want(t, "stages key", has, false)
	rev := a.built()
	wantStage(t, a.judge(rev), "no stage executed", 11)

	edited := filepath.Join(a.root, "edited")
	for _, s := range names {
		j := a.report(edited, rev, a.copyRecord(edited, a.name(), func(rec map[string]any) {
			delete(rec["stages"].(map[string]any)[id].(map[string]any)["logs"].(map[string]any), s)
		})).Attempts[0]
		wantStage(t, j, "cannot tell", 13)
	}
	// Labeled edit: an out-of-table input, a known status rows 11 and 12 do not name.
	j := a.report(edited, rev, a.copyRecord(edited, a.name(), both(func(m map[string]any) {
		m["record"].(map[string]any)["status"] = "passed"
	}))).Attempts[0]
	wantStage(t, j, "cannot tell", 14)

	// Labeled edit of the real home: a review log planted where the service says it is.
	a.restart()
	planted := a.logPath(id, "review")
	want(t, "plant", errors.Join(os.MkdirAll(filepath.Dir(planted), 0o755), os.WriteFile(planted, []byte("planted\n"), 0o644)), nil)
	a.cleanup()
	wantStage(t, a.judge(rev), "cannot tell", 13)
	want(t, "unplant", os.Remove(planted), nil)

	// The configuration restored on main, and a second run held at review. main
	// goes back to the commit before the break rather than gaining a revert, so
	// the branch is still on it and the run's rebase writes no commit, which
	// needs a committer identity a CI runner does not have. The push names the
	// remote's path: cleanup.sh dropped the URL rewrite origin used.
	if code, _, errs := a.run(a.checkout, "sh", "-c", "git checkout -q main && git reset -q --hard HEAD~1"+
		" && git push -q --force ../remote.git main && git checkout -q feature/unlimited"); code != 0 {
		t.Fatalf("restoring the trusted configuration: exit %d\n%s", code, errs)
	}
	a.restart()
	a.startRun()
	a.cleanup()
	j = a.judge(rev)
	wantStage(t, j, "review executed", 4, 11) // the runs step lists the newest first
	want(t, "trouble", j.StageExecution.Trouble.Unread+j.StageExecution.Trouble.ReviewStartedNotCompleted, 0)
}

// TestStageReadsASkippedReview: --skip review is row 6, stages executed and
// review not. Captured again with the review log read failing, it is row 5,
// never not executed. With a review log planted (labeled edit of the real
// home) it is row 7, cannot tell, never started-not-completed (row 8).
func TestStageReadsASkippedReview(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	id := a.intent("--skip", "review,"+afterReview)
	a.cleanup()
	rev := a.built()
	wantStage(t, a.judge(rev), "stages executed, review not executed", 6)
	if v, ok := a.rawStage()["review_established"]; !ok || v != nil {
		t.Errorf("review_established = %v (present %v), want null: the verdict is the fold's, with no completed review", v, ok)
	}

	a.restart()
	a.faultRead("argv[-1] == '--log=review'", 1)
	code, _, _ := a.cleanup()
	a.env = nil
	want(t, "exit with a failed log read", code, 1)
	wantStage(t, a.judge(rev), "cannot tell", 5)

	a.restart()
	planted := a.logPath(id, "review")
	want(t, "plant", errors.Join(os.MkdirAll(filepath.Dir(planted), 0o755), os.WriteFile(planted, []byte("planted\n"), 0o644)), nil)
	a.cleanup()
	wantStage(t, a.judge(rev), "cannot tell", 7)
}

// TestStageReadsAReviewInFlightAsStartedNotCompleted is case C captured
// mid-review: the held reviewer has begun, so the review log is written and
// review has not run (row 8), which is its own verdict and never executed or
// not executed. It is also the capture race's positive control: the review
// log read waits a second through the wrapper, so activity.active_ns moves
// between the two report reads and nothing compared does, and they agree.
// This asserts the stage verdict only; gate_observation's
// started-not-finished is TestGateRecordReadsARunningRunAsStartedNotFinished's.
func TestStageReadsAReviewInFlightAsStartedNotCompleted(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.intentHeld()
	a.wrapLogRead("")
	a.cleanup()
	entries := a.stageEntries()
	if len(entries) != 1 {
		t.Fatalf("stages = %v, want the one run", entries)
	}
	for _, e := range entries {
		want(t, "first read", []any{reviewOf(e.Report.Output)["ran"], e.Report.Output["position"]}, []any{false, "review"})
		if field(e.Report.Output, "activity", "active_ns") == field(e.ReportAfter.Output, "activity", "active_ns") {
			t.Errorf("activity.active_ns did not move between the reads, so this controls nothing: %v", field(e.Report.Output, "activity"))
		}
	}
	r := a.report(a.records(), a.built(), a.name())
	wantStage(t, r.Attempts[0], "review started, not completed", 8)
	want(t, "trouble", r.Attempts[0].StageExecution.Trouble.ReviewStartedNotCompleted, 1)
	want(t, "verdicts", r.StageExecution.Verdicts, map[string]int{"review executed": 0, "review started, not completed": 1,
		"stages executed, review not executed": 0, "no stage executed": 0, "cannot tell": 0, "not measured": 0})
	want(t, "runs started not completed", r.StageExecution.RunsReviewStartedNotCompleted, 1)
	want(t, "established", r.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 0, "null": 1})
}

// TestStageReadsARunThatMovedDuringCaptureAsUnread is the capture race: the
// review log read releases the held reviewer and waits until the run leaves
// review, so the first report read says review in flight and the second says
// held after it ran. Both are recorded, and the run is unread (row 3).
func TestStageReadsARunThatMovedDuringCaptureAsUnread(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	a.wrapLogRead(a.intentHeld())
	a.cleanup()
	for _, e := range a.stageEntries() {
		want(t, "first read", []any{reviewOf(e.Report.Output)["ran"], e.Report.Output["position"]}, []any{false, "review"})
		want(t, "second read", []any{reviewOf(e.ReportAfter.Output)["ran"], e.ReportAfter.Output["position"]}, []any{true, "hold:review"})
	}
	wantStage(t, a.judge(a.built()), "cannot tell", 3)
}

// TestStageFoldsAnUnreadRunBesideACompletedReviewAsTrouble: two runs, the
// first cancelled and the second held at review, and the first run's report
// read failing. One completed review decides the verdict, and the unread run
// shows beside it in trouble and in the counts.
func TestStageFoldsAnUnreadRunBesideACompletedReviewAsTrouble(t *testing.T) {
	a := newAttempt(t)
	a.serve()
	first := a.startRun()
	a.assistant(a.checkout, "--json", "--cancel="+first)
	a.startRun()
	a.faultRead(fmt.Sprintf("argv[-2:] == ['runs', %q]", first), 1)
	code, _, _ := a.cleanup()
	a.env = nil
	want(t, "exit with a failed report read", code, 1)
	r := a.report(a.records(), a.built(), a.name())
	j := r.Attempts[0]
	wantJudged(t, j, "ran", "success")
	wantStage(t, j, "review executed", 4, 2) // the runs step lists the newest first
	want(t, "trouble", j.StageExecution.Trouble, struct {
		Unread                    int
		ReviewStartedNotCompleted int `json:"review_started_not_completed"`
	}{1, 0})
	want(t, "with trouble", r.StageExecution.ReviewExecutedWithTrouble, 1)
	want(t, "runs unread", r.StageExecution.RunsUnread, 1)
}

// TestStageReadsARunHeldAtRebaseAsReviewNotReached is row 10: main gains a
// commit that conflicts with the branch, and with no automatic rebase round
// (fix_rounds.rebase 0, so no fixer runs) the run is held at its rebase. It
// has a checkpoint, its position is before review, and the review log is
// unwritten: stages executed, review not executed.
func TestStageReadsARunHeldAtRebaseAsReviewNotReached(t *testing.T) {
	a := newAttempt(t)
	a.configure(map[string]any{"fix_rounds": map[string]any{"rebase": 0}})
	a.serve()
	if code, _, errs := a.run(a.checkout, "sh", "-c", `f=$(git diff --name-only main feature/unlimited | head -n 1) && [ -n "$f" ]`+
		` && git checkout -q main && echo 'main moved this line' > "$f" && git add -- "$f"`+
		` && git -c user.name=q1 -c user.email=q1@example.invalid commit -qm conflicting && git push -q ../remote.git main && git checkout -q feature/unlimited`); code != 0 {
		t.Fatalf("a conflicting commit on main: exit %d\n%s", code, errs)
	}
	id := a.startRun()
	a.cleanup()
	e := a.stageEntries()[id]
	want(t, "first read", []any{e.Report.Output["position"], reviewOf(e.Report.Output)["ran"], e.Logs["review"].Output["written"]},
		[]any{"hold:rebase", false, false})
	j := a.judge(a.built())
	wantStage(t, j, "stages executed, review not executed", 10)
	if v, ok := a.rawStage()["review_established"]; !ok || v != nil {
		t.Errorf("review_established = %v (present %v), want null", v, ok)
	}
}

// TestStageReadsACeilingHeldReviewAsUnknownEstablished is the r3 case: the
// operator's usage ceiling refuses the review's agent call, so the review
// stage completes (ran: true) with the ceiling's own report, the
// usage-ceiling finding marked not_started, and no reviewer ran. That is
// review executed with review_established null: the report cannot tell this
// refusal from a hold a refused fix round left (the r4 case, in
// TestStageReadsAnEstablishedReview), so it is never true and never false.
// The ceiling is real: run 1's failed review reports the stand-in's usage,
// which reaches a one-token output ceiling, and run 1 is cancelled, so run
// 2's review call is the one refused. One run false and one null fold to
// null for the attempt: unknown is not "none established".
func TestStageReadsACeilingHeldReviewAsUnknownEstablished(t *testing.T) {
	a := newAttempt(t)
	agent := a.scriptAgent(standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Failed("the reviewer gave up")},
	}}, map[string]any{"usage_ceiling": map[string]any{"window": "1h", "output_tokens": 1}})
	a.serve()
	first := a.intent()
	a.assistant(a.checkout, "--json", "--cancel="+first)
	second := a.intent()
	a.cleanup()
	want(t, "reviewer calls", len(agent.Calls()), 1)
	var ceiling map[string]any
	if report, _ := reviewOf(a.stageEntries()[second].Report.Output)["report"].(map[string]any); report != nil {
		for _, f := range report["findings"].([]any) {
			if f := f.(map[string]any); f["id"] == "usage-ceiling" {
				ceiling = f
			}
		}
	}
	want(t, "the ceiling's finding, not started", field(ceiling, "not_started"), true)
	j := a.judge(a.built())
	wantStage(t, j, "review executed", 4, 4)
	want(t, "established runs", establishedRuns(j), map[string]string{first: "false", second: "null"})
	want(t, "attempt review_established", tri(j.StageExecution.ReviewEstablished), "null")
}
