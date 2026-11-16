//go:build unix

package q1_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/machine"
)

// armAttempt is one attempt of gate_by_arms.py's report, where a null
// review_established reads "unknown"; README.md's "Gate verdicts by arm" owns
// the format.
type armAttempt struct {
	Attempt, Arm    string
	GateObservation struct {
		Validity, State, Reason string
		Runs                    []struct{ ID string }
	} `json:"gate_observation"`
	StageExecution struct {
		Verdict           string
		ReviewEstablished any `json:"review_established"`
		EstablishedRuns   []struct {
			Established any `json:"review_established"`
		} `json:"review_established_runs"`
	} `json:"stage_execution"`
}

type armReport struct {
	Ablated            []string
	CaliperAttempts    int `json:"caliper_attempts"`
	Records            int
	MissingRecords     int `json:"missing_records"`
	RecordsBeyondSaved int `json:"records_beyond_saved"`
	Counts             struct {
		GateObservation map[string]int `json:"gate_observation"`
	}
	StageExecution struct {
		Verdicts                   map[string]int
		ReviewEstablished          map[string]int `json:"review_established"`
		ReviewExecutedOverMeasured map[string]int `json:"review_executed_over_measured"`
	} `json:"stage_execution"`
	Attempts []armAttempt
}

type byArm struct {
	AsAssigned map[string]armReport `json:"as_assigned"`
	AsRun      struct {
		Measured bool
		Reason   string
	} `json:"as_run"`
}

// armDir is an arm's TMPDIR, as run-full.sh gives one to each caliper run.
func armDir(t *testing.T) string {
	t.Helper()
	dir, err := standin.TempDir("q1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// result writes the committed caliper result from, cut to its first task's
// first n attempts (labeled edit: the shape is caliper 0.19.0's own), into dir.
func result(t *testing.T, dir, from string, n int) string {
	t.Helper()
	return editedResult(t, dir, from, n, nil)
}

// editedResult is result with edit applied to the result's run (labeled edit).
func editedResult(t *testing.T, dir, from string, n int, edit func(run map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(spec, from))
	var doc map[string]any
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err != nil {
		t.Fatal(err)
	}
	task := doc["task_results"].([]any)[0].(map[string]any)
	task["attempts"] = task["attempts"].([]any)[:n]
	doc["task_results"] = []any{task}
	if edit != nil {
		edit(doc["run"].(map[string]any))
	}
	path := filepath.Join(dir, from)
	if data, err = json.Marshal(doc); err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// byArm runs gate_by_arms.py with b's stamp; its exit code, stdout and stderr.
func (a *attempt) byArm(b built, arms ...string) (int, string, string) {
	a.t.Helper()
	return a.run(a.root, "python3", append(append([]string{filepath.Join(spec, "gate_by_arms.py")}, b.args()...), arms...)...)
}

// arms is gate_by_arms.py's report, failing the test unless it answers with one.
func (a *attempt) arms(b built, arms ...string) byArm {
	a.t.Helper()
	code, out, errs := a.byArm(b, arms...)
	var r byArm
	if err := json.Unmarshal([]byte(out), &r); code != 0 || err != nil {
		a.t.Fatalf("gate_by_arms.py: exit %d, %v\n%s%s", code, err, out, errs)
	}
	return r
}

// TestGateByArmGroupsEachArmsRecords: each arm's records are its own TMPDIR's,
// judged by gate_record.py and reported under the arm with the run ids. A
// saved attempt beyond the records is cannot tell, never did not run. A null
// review_established reads unknown, and review executed is counted over the
// measured attempts with not measured beside it. "As run" is not measured.
func TestGateByArmGroupsEachArmsRecords(t *testing.T) {
	withSkill, ablated := armDir(t), armDir(t)
	a := newAttemptIn(t, withSkill, "caliper-a1")
	a.serve()
	id := a.startRun()
	a.cleanup()
	b := newAttemptIn(t, ablated, "caliper-b1")
	b.cleanup()
	rev := a.built()
	args := []string{"--arm", "with-skill", withSkill, result(t, withSkill, "result-q1-with-skill.json", 2),
		"--arm", "ablated", ablated, result(t, ablated, "result-q1-ablated.json", 1)}
	r := a.arms(rev, args...)

	ws, ab := r.AsAssigned["with-skill"], r.AsAssigned["ablated"]
	want(t, "arms", len(r.AsAssigned), 2)
	want(t, "as run", r.AsRun.Measured, false)
	if !strings.Contains(r.AsRun.Reason, "no attempt id") {
		t.Errorf("as_run reason = %q, want it to say caliper saves no attempt id", r.AsRun.Reason)
	}
	want(t, "with-skill attempts, records, missing", []int{ws.CaliperAttempts, ws.Records, ws.MissingRecords}, []int{2, 1, 1})
	want(t, "with-skill gate", []int{ws.Counts.GateObservation["ran"], ws.Counts.GateObservation["indeterminate"]}, []int{1, 1})
	if len(ws.Attempts) != 2 {
		t.Fatalf("with-skill attempts = %+v, want the record and the missing one", ws.Attempts)
	}
	got := ws.Attempts[0]
	want(t, "attempt, arm, state", []string{got.Attempt, got.Arm, got.GateObservation.State}, []string{a.name(), "with-skill", "ran"})
	if len(got.GateObservation.Runs) != 1 || got.GateObservation.Runs[0].ID != id {
		t.Errorf("runs = %+v, want the run %s", got.GateObservation.Runs, id)
	}
	missing := ws.Attempts[1]
	want(t, "missing record", []string{missing.Attempt, missing.Arm, missing.GateObservation.Validity, missing.StageExecution.Verdict},
		[]string{"", "with-skill", "indeterminate", "cannot tell"})
	want(t, "with-skill review executed over measured", ws.StageExecution.ReviewExecutedOverMeasured,
		map[string]int{"review_executed": 1, "not_measured": 0, "measured": 2})
	want(t, "with-skill review_established", ws.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 1, "unknown": 1})
	want(t, "ablated", []any{ab.Ablated, ab.CaliperAttempts, ab.Records, ab.MissingRecords, ab.Counts.GateObservation["no-home"]},
		[]any{[]string{"assistant"}, 1, 1, 0, 1})
	want(t, "ablated attempt", []string{ab.Attempts[0].Attempt, ab.Attempts[0].Arm}, []string{b.name(), "ablated"})
	want(t, "ablated review_established", ab.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 0, "unknown": 1})

	// Labeled edit: the usage ceiling's not-started finding on both reads of
	// the review, as TestStageReadsAnEstablishedReview plants it. The
	// review executed and whether it established is unknown, never false.
	held, err := json.Marshal(machine.CeilingHeldFinding(machine.UsageCeiling{Standing: machine.CeilingReached, Window: "1h"}))
	var finding map[string]any
	if err == nil {
		err = json.Unmarshal(held, &finding)
	}
	want(t, "the ceiling's finding", err, nil)
	a.copyRecord(a.records(), a.name(), both(func(m map[string]any) {
		report := reviewOf(m)["report"].(map[string]any)
		found, _ := report["findings"].([]any)
		report["findings"] = append(found, finding)
	}))
	ws = a.arms(rev, args...).AsAssigned["with-skill"]
	got = ws.Attempts[0]
	want(t, "verdict", got.StageExecution.Verdict, "review executed")
	want(t, "attempt review_established", got.StageExecution.ReviewEstablished, "unknown")
	if len(got.StageExecution.EstablishedRuns) != 1 || got.StageExecution.EstablishedRuns[0].Established != "unknown" {
		t.Errorf("review_established_runs = %+v, want one unknown", got.StageExecution.EstablishedRuns)
	}
	want(t, "review_established counts", ws.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 0, "unknown": 2})

	// Labeled edit: the record as a#925's cleanup.sh wrote it (schema 1). It
	// is not measured, so it leaves the rate's denominator and is named beside it.
	a.copyRecord(a.records(), a.name(), func(rec map[string]any) { rec["schema"] = 1; delete(rec, "stages") })
	ws = a.arms(rev, args...).AsAssigned["with-skill"]
	want(t, "schema 1 attempt", ws.Attempts[0].StageExecution.Verdict, "not measured")
	want(t, "review executed over measured", ws.StageExecution.ReviewExecutedOverMeasured,
		map[string]int{"review_executed": 0, "not_measured": 1, "measured": 1})
}

// TestGateByArmRefusesAnArmItCannotAssignExactly: a result whose run.ablated
// is not the arm's, a record whose attempt root is not under the arm's
// TMPDIR, one DIR for two arms, an arm unknown or named twice, an arm with
// no records directory, and an unreadable result each refuse (exit 2).
func TestGateByArmRefusesAnArmItCannotAssignExactly(t *testing.T) {
	withSkill, ablated := armDir(t), armDir(t)
	a, b := newAttemptIn(t, withSkill, "caliper-a1"), newAttemptIn(t, ablated, "caliper-b1")
	a.cleanup()
	b.cleanup()
	rev := a.built()
	wsResult, abResult := result(t, withSkill, "result-q1-with-skill.json", 1), result(t, ablated, "result-q1-ablated.json", 1)
	good := []string{"--arm", "with-skill", withSkill, wsResult, "--arm", "ablated", ablated, abResult}
	a.arms(rev, good...) // the positive control: the same arms, as assigned, answer
	moved := armDir(t)
	want(t, "mkdir", os.Mkdir(filepath.Join(moved, "caliper-q1-gate"), 0o755), nil)
	b.copyRecord(filepath.Join(moved, "caliper-q1-gate"), b.name(), nil)
	empty := armDir(t)
	for _, args := range [][]string{
		{"--arm", "with-skill", withSkill, abResult},
		{"--arm", "ablated", ablated, wsResult},
		{"--arm", "ablated", moved, abResult},
		{"--arm", "with-skill", withSkill, wsResult, "--arm", "ablated", withSkill, abResult},
		{"--arm", "with-skill", withSkill, wsResult, "--arm", "with-skill", ablated, wsResult},
		{"--arm", "gated", withSkill, wsResult},
		{"--arm", "with-skill", empty, wsResult},
		{"--arm", "with-skill", withSkill, filepath.Join(withSkill, "caliper-q1-gate", a.name()+".record.json")},
	} {
		if code, out, _ := a.byArm(rev, args...); code != 2 {
			t.Errorf("gate_by_arms.py %v: exit %d, want 2\n%s", args, code, out)
		}
	}
}

// TestGateByArmCountsRecordsBeyondSavedOnlyWhenInterrupted: two records
// against one saved attempt. In a run that was not interrupted nothing
// explains the extra record, so the report refuses (exit 2). In an
// interrupted run caliper saves no attempt whose agent a cancellation killed
// though its cleanup ran, so the extra record is counted as
// records_beyond_saved, never silently, and stays in the arm.
func TestGateByArmCountsRecordsBeyondSavedOnlyWhenInterrupted(t *testing.T) {
	arm := armDir(t)
	a, b := newAttemptIn(t, arm, "caliper-a1"), newAttemptIn(t, arm, "caliper-a2")
	a.cleanup()
	b.cleanup()
	rev := a.built()
	whole := result(t, arm, "result-q1-with-skill.json", 1)
	if code, out, _ := a.byArm(rev, "--arm", "with-skill", arm, whole); code != 2 {
		t.Errorf("two records, one saved attempt, not interrupted: exit %d, want 2\n%s", code, out)
	}
	cut := editedResult(t, arm, "result-q1-with-skill.json", 1, func(run map[string]any) { run["interrupted"] = true })
	ws := a.arms(rev, "--arm", "with-skill", arm, cut).AsAssigned["with-skill"]
	want(t, "attempts saved, records, missing, beyond", []int{ws.CaliperAttempts, ws.Records, ws.MissingRecords, ws.RecordsBeyondSaved},
		[]int{1, 2, 0, 1})
	want(t, "both records in the arm", ws.Counts.GateObservation["no-home"], 2)
}
