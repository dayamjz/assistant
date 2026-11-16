//go:build unix

package q1_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
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
	TaskID          any    `json:"task_id"`         // nil for a record without a key
	CaliperAttempt  any    `json:"caliper_attempt"` // a JSON number, or nil
	TaskOutcome     string `json:"task_outcome"`    // caliper's outcome, "cannot tell" or "not saved"
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
	MissingRecords     int          `json:"missing_records"`
	RecordsBeyondSaved int          `json:"records_beyond_saved"`
	RecordsWithoutKey  []armAttempt `json:"records_without_key"`
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

// asRun is gate state by task outcome; README.md's "As run" owns the format.
type asRun map[string]map[string]int

type byArm struct {
	AsAssigned map[string]armReport `json:"as_assigned"`
	AsRun      struct {
		ByArm    map[string]asRun `json:"by_arm"`
		BothArms asRun            `json:"both_arms"`
	} `json:"as_run"`
}

// cells is the table's non-empty cells as "state/outcome=n", sorted, so a
// test states the whole table and an extra cell fails it.
func (r asRun) cells() []string {
	var out []string
	for state, outcomes := range r {
		for outcome, n := range outcomes {
			out = append(out, fmt.Sprintf("%s/%s=%d", state, outcome, n))
		}
	}
	sort.Strings(out)
	return out
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

// saved is one attempt a caliper result saved: its key and its outcome.
type saved struct {
	task    string
	n       int
	outcome string
}

// keyedResult writes the committed caliper result from into dir saving exactly
// the attempts given, each a copy of the file's first attempt under its own
// key and outcome, with run.interrupted as given (labeled edit: the shape is
// caliper 0.19.0's own).
func keyedResult(t *testing.T, dir, from string, interrupted bool, attempts ...saved) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(spec, from))
	var doc map[string]any
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err != nil {
		t.Fatal(err)
	}
	first := doc["task_results"].([]any)[0].(map[string]any)
	tasks, byTask := []any{}, map[string]map[string]any{}
	for _, s := range attempts {
		task, ok := byTask[s.task]
		if !ok {
			task = maps.Clone(first)
			task["task_id"], task["attempts"] = s.task, []any{}
			byTask[s.task], tasks = task, append(tasks, task)
		}
		attempt := maps.Clone(first["attempts"].([]any)[0].(map[string]any))
		attempt["attempt"], attempt["outcome"] = s.n, s.outcome
		task["attempts"] = append(task["attempts"].([]any), attempt)
	}
	doc["task_results"] = tasks
	doc["run"].(map[string]any)["interrupted"] = interrupted
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
// measured attempts with not measured beside it. A record whose key is gone
// (a#925's schema 1) is listed beside the arm, and its attempt is cannot tell.
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
	want(t, "missing record", []any{missing.Attempt, missing.Arm, missing.GateObservation.Validity, missing.StageExecution.Verdict,
		missing.TaskID, missing.CaliperAttempt, missing.TaskOutcome}, []any{"", "with-skill", "indeterminate", "cannot tell", "task-001", 2, "task_fail"})
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

	// Labeled edit: the record as a#925's cleanup.sh wrote it (schema 1), with
	// no key. It names no attempt, so it is listed beside the arm with its
	// verdicts, and both saved attempts are cannot tell, never did not run.
	a.copyRecord(a.records(), a.name(), func(rec map[string]any) { rec["schema"] = 1; delete(rec, "stages") })
	ws = a.arms(rev, args...).AsAssigned["with-skill"]
	if len(ws.RecordsWithoutKey) != 1 || len(ws.Attempts) != 2 {
		t.Fatalf("records without key %+v, attempts %+v: want the record beside two saved attempts", ws.RecordsWithoutKey, ws.Attempts)
	}
	keyless := ws.RecordsWithoutKey[0]
	want(t, "schema 1 record", []any{keyless.Attempt, keyless.TaskID, keyless.TaskOutcome, keyless.GateObservation.State, keyless.StageExecution.Verdict},
		[]any{a.name(), nil, "cannot tell", "ran", "not measured"})
	want(t, "records, missing", []int{ws.Records, ws.MissingRecords}, []int{1, 2})
	want(t, "review executed over measured", ws.StageExecution.ReviewExecutedOverMeasured,
		map[string]int{"review_executed": 0, "not_measured": 0, "measured": 2})
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

// TestGateByArmCountsRecordsBeyondSavedOnlyWhenInterrupted: two records, two
// saved attempts, and one record's key is not one the arm saved. In a run that
// was not interrupted nothing explains that record, so the report refuses
// (exit 2), though the counts match. In an interrupted run caliper saves no
// attempt whose agent a cancellation killed though its cleanup ran, so the
// record is counted exactly as records_beyond_saved, "not saved", and stays
// in the arm, and the saved attempt no record names is missing. The other
// arm saved that key: it never joins there, since an arm's records join only
// its own result.
func TestGateByArmCountsRecordsBeyondSavedOnlyWhenInterrupted(t *testing.T) {
	arm, other := armDir(t), armDir(t)
	a, b := newAttemptIn(t, arm, "caliper-a1"), newAttemptIn(t, arm, "caliper-a2").keyed("task-001", 3)
	a.cleanup()
	b.cleanup()
	o := newAttemptIn(t, other, "caliper-b1")
	o.cleanup()
	rev := a.built()
	two := []saved{{"task-001", 1, "pass"}, {"task-001", 2, "task_fail"}}
	whole := keyedResult(t, arm, "result-q1-with-skill.json", false, two...)
	if code, out, _ := a.byArm(rev, "--arm", "with-skill", arm, whole); code != 2 {
		t.Errorf("a record the run did not save, not interrupted: exit %d, want 2\n%s", code, out)
	}
	cut := keyedResult(t, arm, "result-q1-with-skill.json", true, two...)
	ablated := keyedResult(t, other, "result-q1-ablated.json", false, saved{"task-001", 1, "task_fail"}, saved{"task-001", 3, "infra_error"})
	newAttemptIn(t, other, "caliper-b2").keyed("task-001", 3).cleanup()
	r := a.arms(rev, "--arm", "with-skill", arm, cut, "--arm", "ablated", other, ablated)
	ws := r.AsAssigned["with-skill"]
	want(t, "attempts saved, records, missing, beyond", []int{ws.CaliperAttempts, ws.Records, ws.MissingRecords, ws.RecordsBeyondSaved},
		[]int{2, 2, 1, 1})
	want(t, "all three in the arm", ws.Counts.GateObservation["no-home"]+ws.Counts.GateObservation["indeterminate"], 3)
	want(t, "with-skill as run", r.AsRun.ByArm["with-skill"].cells(), []string{"indeterminate/task_fail=1", "no-home/not saved=1", "no-home/pass=1"})
	want(t, "ablated as run", r.AsRun.ByArm["ablated"].cells(), []string{"no-home/infra_error=1", "no-home/task_fail=1"})
}

// TestGateByArmJoinsEachRecordToItsSavedAttemptExactly is the join over both
// arms: each record joins the attempt its key names in its own arm's result,
// whatever order the files and the result list them in, and a saved attempt
// no record names is cannot tell, never did not run. "As run" is gate state
// by task outcome per arm and over both, and every attempt says its key.
func TestGateByArmJoinsEachRecordToItsSavedAttemptExactly(t *testing.T) {
	withSkill, ablated := armDir(t), armDir(t)
	// File order (a1, a2) is the reverse of key order, so a join by order fails.
	a := newAttemptIn(t, withSkill, "caliper-a1").keyed("task-002", 1)
	a.serve()
	a.startRun()
	a.cleanup()
	newAttemptIn(t, withSkill, "caliper-a2").keyed("task-001", 1).cleanup()
	newAttemptIn(t, ablated, "caliper-b1").keyed("task-002", 1).cleanup()
	rev := a.built()
	r := a.arms(rev, "--arm", "with-skill", withSkill,
		keyedResult(t, withSkill, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}, saved{"task-002", 1, "task_fail"}),
		"--arm", "ablated", ablated,
		keyedResult(t, ablated, "result-q1-ablated.json", false, saved{"task-001", 1, "infra_error"}, saved{"task-002", 1, "pass"}))

	want(t, "with-skill as run", r.AsRun.ByArm["with-skill"].cells(), []string{"no-home/pass=1", "ran/task_fail=1"})
	want(t, "ablated as run", r.AsRun.ByArm["ablated"].cells(), []string{"indeterminate/infra_error=1", "no-home/pass=1"})
	want(t, "both arms as run", r.AsRun.BothArms.cells(),
		[]string{"indeterminate/infra_error=1", "no-home/pass=2", "ran/task_fail=1"})
	for state := range r.AsRun.BothArms {
		if _, ok := r.AsAssigned["with-skill"].Counts.GateObservation[state]; !ok {
			t.Errorf("as run state %q is not a gate_observation state", state)
		}
	}
	var got []string
	for _, arm := range []string{"with-skill", "ablated"} {
		ar := r.AsAssigned[arm]
		want(t, arm+" missing, beyond, keyless", []int{ar.MissingRecords, ar.RecordsBeyondSaved, len(ar.RecordsWithoutKey)}, []int{map[string]int{"with-skill": 0, "ablated": 1}[arm], 0, 0})
		for _, at := range ar.Attempts {
			got = append(got, fmt.Sprintf("%s %s %v/%v %s %s", at.Arm, at.Attempt, at.TaskID, at.CaliperAttempt, at.TaskOutcome, at.StageExecution.Verdict))
		}
	}
	want(t, "attempts", got, []string{
		"with-skill caliper-a1 task-002/1 task_fail review executed",
		"with-skill caliper-a2 task-001/1 pass no stage executed",
		"ablated caliper-b1 task-002/1 pass no stage executed",
		"ablated  task-001/1 infra_error cannot tell"})
}

// TestGateByArmRefusesTwoRecordsOfOneAttempt: two records in one arm naming
// one key leave the attempt with no one record, so the report refuses (exit 2)
// rather than pick one. The same two records under distinct keys answer.
func TestGateByArmRefusesTwoRecordsOfOneAttempt(t *testing.T) {
	arm := armDir(t)
	a := newAttemptIn(t, arm, "caliper-a1")
	a.cleanup()
	b := newAttemptIn(t, arm, "caliper-a2").keyed("task-001", 2)
	b.cleanup()
	rev := a.built()
	res := keyedResult(t, arm, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}, saved{"task-001", 2, "pass"})
	a.arms(rev, "--arm", "with-skill", arm, res) // the positive control
	b.keyed("task-001", 1).cleanup()
	if code, out, errs := a.byArm(rev, "--arm", "with-skill", arm, res); code != 2 || !strings.Contains(errs, "both name attempt") {
		t.Errorf("two records of one attempt: exit %d, want 2 naming both\n%s%s", code, out, errs)
	}
}

// TestGateByArmSetsAsideARecordWithoutAKey: a record cleanup.sh wrote under a
// stock caliper (schema 3, key null) and one from before the key (schema 2)
// name no attempt. Each is listed beside the arm with its gate verdict and
// task outcome cannot tell, never joined or counted in the denominator, and
// the saved attempt is cannot tell, never did not run.
func TestGateByArmSetsAsideARecordWithoutAKey(t *testing.T) {
	arm := armDir(t)
	a := newAttemptIn(t, arm, "caliper-a1")
	a.key = nil
	a.cleanup()
	rev := a.built()
	res := keyedResult(t, arm, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"})
	for _, edit := range []func(map[string]any){nil, func(rec map[string]any) {
		rec["schema"], rec["task_id"], rec["caliper_attempt"] = 2, "task-001", 1 // labeled edit: a key a schema 2 record cannot have
	}} {
		a.copyRecord(a.records(), a.name(), edit)
		r := a.arms(rev, "--arm", "with-skill", arm, res)
		ws := r.AsAssigned["with-skill"]
		if len(ws.RecordsWithoutKey) != 1 || len(ws.Attempts) != 1 {
			t.Fatalf("records without key %+v, attempts %+v: want one of each", ws.RecordsWithoutKey, ws.Attempts)
		}
		k, at := ws.RecordsWithoutKey[0], ws.Attempts[0]
		want(t, "record without key", []any{k.Attempt, k.TaskID, k.TaskOutcome, k.GateObservation.State}, []any{a.name(), nil, "cannot tell", "no-home"})
		want(t, "its saved attempt", []any{at.Attempt, at.TaskID, at.TaskOutcome, at.GateObservation.Validity}, []any{"", "task-001", "pass", "indeterminate"})
		want(t, "records, missing", []int{ws.Records, ws.MissingRecords}, []int{1, 1})
		want(t, "as run", r.AsRun.ByArm["with-skill"].cells(), []string{"indeterminate/pass=1"})
	}
}
