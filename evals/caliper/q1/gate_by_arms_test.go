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
	CIExecution     verdictOf `json:"ci_execution"`
	ReviewExecution verdictOf `json:"review_execution"`
	Contamination   struct {
		Verdict, Caveat string
		GateObservation string `json:"gate_observation"`
	}
}

// verdictOf is a checker's verdict on one attempt, with its reason when it has one.
type verdictOf struct{ Verdict, Reason string }

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
	CIExecution     map[string]int `json:"ci_execution"`
	ReviewExecution map[string]int `json:"review_execution"`
	Contamination   struct {
		Counts   map[string]int
		Attempts []struct {
			TaskID         string   `json:"task_id"`
			CaliperAttempt int      `json:"caliper_attempt"`
			RunIDs         []string `json:"run_ids"`
			Contamination  struct{ Verdict string }
		}
	}
	JudgeCalls map[string]int `json:"judge_calls"`
	Attempts   []armAttempt
}

// asRun is gate state, then review execution, by task outcome; README.md's
// "As run" owns the format.
type asRun map[string]map[string]map[string]int

type byArm struct {
	Schema     int
	AsAssigned map[string]armReport `json:"as_assigned"`
	AsRun      struct {
		ByArm   map[string]asRun `json:"by_arm"`
		AllArms asRun            `json:"all_arms"`
	} `json:"as_run"`
}

// cells is the table's non-empty cells as "state/review/outcome=n", sorted, so
// a test states the whole table and an extra cell fails it.
func (r asRun) cells() []string {
	var out []string
	for state, reviews := range r {
		for review, outcomes := range reviews {
			for outcome, n := range outcomes {
				out = append(out, fmt.Sprintf("%s/%s/%s=%d", state, review, outcome, n))
			}
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
	b := newAttemptIn(t, ablated, "caliper-b1").inArm("agent-ci")
	b.cleanup()
	rev := a.built()
	args := []string{"--arm", "full-gate", withSkill, result(t, withSkill, "result-q1-with-skill.json", 2),
		"--arm", "agent-ci", ablated, result(t, ablated, "result-q1-ablated.json", 1)}
	r := a.arms(rev, args...)

	ws, ab := r.AsAssigned["full-gate"], r.AsAssigned["agent-ci"]
	want(t, "arms", len(r.AsAssigned), 2)
	want(t, "with-skill attempts, records, missing", []int{ws.CaliperAttempts, ws.Records, ws.MissingRecords}, []int{2, 1, 1})
	want(t, "with-skill gate", []int{ws.Counts.GateObservation["ran"], ws.Counts.GateObservation["indeterminate"]}, []int{1, 1})
	if len(ws.Attempts) != 2 {
		t.Fatalf("with-skill attempts = %+v, want the record and the missing one", ws.Attempts)
	}
	got := ws.Attempts[0]
	want(t, "attempt, arm, state", []string{got.Attempt, got.Arm, got.GateObservation.State}, []string{a.name(), "full-gate", "ran"})
	if len(got.GateObservation.Runs) != 1 || got.GateObservation.Runs[0].ID != id {
		t.Errorf("runs = %+v, want the run %s", got.GateObservation.Runs, id)
	}
	missing := ws.Attempts[1]
	want(t, "missing record", []any{missing.Attempt, missing.Arm, missing.GateObservation.Validity, missing.StageExecution.Verdict,
		missing.TaskID, missing.CaliperAttempt, missing.TaskOutcome}, []any{"", "full-gate", "indeterminate", "cannot tell", "task-001", 2, "task_fail"})
	want(t, "with-skill review executed over measured", ws.StageExecution.ReviewExecutedOverMeasured,
		map[string]int{"review_executed": 1, "not_measured": 0, "measured": 2})
	want(t, "with-skill review_established", ws.StageExecution.ReviewEstablished, map[string]int{"true": 0, "false": 1, "unknown": 1})
	want(t, "agent-ci", []any{ab.Ablated, ab.CaliperAttempts, ab.Records, ab.MissingRecords, ab.Counts.GateObservation["no-home"]},
		[]any{[]string{"assistant"}, 1, 1, 0, 1})
	want(t, "ablated attempt", []string{ab.Attempts[0].Attempt, ab.Attempts[0].Arm}, []string{b.name(), "agent-ci"})
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
	ws = a.arms(rev, args...).AsAssigned["full-gate"]
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
	a.copyRecord(a.records(), a.name(), func(rec map[string]any) { rec["schema"] = 1; delete(rec, "stages"); delete(rec, "wait_down") })
	ws = a.arms(rev, args...).AsAssigned["full-gate"]
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
// no records directory, an unreadable result, a result that saves one key
// twice, and review entries in an arm with no reviewer each refuse (exit 2).
func TestGateByArmRefusesAnArmItCannotAssignExactly(t *testing.T) {
	withSkill, ablated := armDir(t), armDir(t)
	a, b := newAttemptIn(t, withSkill, "caliper-a1"), newAttemptIn(t, ablated, "caliper-b1").inArm("agent-ci")
	a.cleanup()
	b.cleanup()
	rev := a.built()
	wsResult, abResult := result(t, withSkill, "result-q1-with-skill.json", 1), result(t, ablated, "result-q1-ablated.json", 1)
	good := []string{"--arm", "full-gate", withSkill, wsResult, "--arm", "agent-ci", ablated, abResult}
	a.arms(rev, good...) // the positive control: the same arms, as assigned, answer
	moved := armDir(t)
	want(t, "mkdir", os.Mkdir(filepath.Join(moved, "caliper-q1-gate"), 0o755), nil)
	b.copyRecord(filepath.Join(moved, "caliper-q1-gate"), b.name(), nil)
	empty := armDir(t)
	twice := keyedResult(t, empty, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}, saved{"task-001", 1, "task_fail"})
	for _, args := range [][]string{
		{"--arm", "full-gate", withSkill, twice},
		{"--arm", "full-gate", withSkill, abResult},
		{"--arm", "agent-ci", ablated, wsResult},
		{"--arm", "agent-ci", moved, abResult},
		{"--arm", "full-gate", withSkill, wsResult, "--arm", "agent-ci", withSkill, abResult},
		{"--arm", "full-gate", withSkill, wsResult, "--arm", "full-gate", ablated, wsResult},
		{"--arm", "with-skill", withSkill, wsResult},
		{"--arm", "full-gate", empty, wsResult},
		{"--arm", "full-gate", withSkill, filepath.Join(withSkill, "caliper-q1-gate", a.name()+".record.json")},
	} {
		if code, out, _ := a.byArm(rev, args...); code != 2 {
			t.Errorf("gate_by_arms.py %v: exit %d, want 2\n%s", args, code, out)
		}
	}
	// Labeled edit: review entries in an arm with no reviewer, which only a
	// harness fault writes, so the report refuses rather than count them.
	b.copyRecord(b.records(), b.name(), func(rec map[string]any) {
		rec["review_checks"] = map[string]any{"files": map[string]any{"push-1": "{}"}}
	})
	if code, out, errs := a.byArm(rev, "--arm", "agent-ci", ablated, abResult); code != 2 || !strings.Contains(errs, "holds review entries") {
		t.Errorf("review entries in agent-ci: exit %d, want 2 naming them\n%s%s", code, out, errs)
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
	o := newAttemptIn(t, other, "caliper-b1").inArm("agent-ci")
	o.cleanup()
	rev := a.built()
	two := []saved{{"task-001", 1, "pass"}, {"task-001", 2, "task_fail"}}
	whole := keyedResult(t, arm, "result-q1-with-skill.json", false, two...)
	if code, out, _ := a.byArm(rev, "--arm", "full-gate", arm, whole); code != 2 {
		t.Errorf("a record the run did not save, not interrupted: exit %d, want 2\n%s", code, out)
	}
	cut := keyedResult(t, arm, "result-q1-with-skill.json", true, two...)
	ablated := keyedResult(t, other, "result-q1-ablated.json", false, saved{"task-001", 1, "task_fail"}, saved{"task-001", 3, "infra_error"})
	newAttemptIn(t, other, "caliper-b2").inArm("agent-ci").keyed("task-001", 3).cleanup()
	r := a.arms(rev, "--arm", "full-gate", arm, cut, "--arm", "agent-ci", other, ablated)
	ws := r.AsAssigned["full-gate"]
	want(t, "attempts saved, records, missing, beyond", []int{ws.CaliperAttempts, ws.Records, ws.MissingRecords, ws.RecordsBeyondSaved},
		[]int{2, 2, 1, 1})
	want(t, "all three in the arm", ws.Counts.GateObservation["no-home"]+ws.Counts.GateObservation["indeterminate"], 3)
	want(t, "with-skill as run", r.AsRun.ByArm["full-gate"].cells(), []string{"indeterminate/not assigned/task_fail=1", "no-home/not assigned/not saved=1", "no-home/not assigned/pass=1"})
	want(t, "ablated as run", r.AsRun.ByArm["agent-ci"].cells(), []string{"no-home/not assigned/infra_error=1", "no-home/not assigned/task_fail=1"})
}

// TestGateByArmJoinsEachRecordToItsSavedAttemptExactly is the join over both
// arms: each record joins the attempt its key names in its own arm's result,
// whatever order the files and the result list them in, and a saved attempt
// no record names is cannot tell, never did not run. "As run" is gate state
// by task outcome per arm and over all arms, and every attempt says its key.
func TestGateByArmJoinsEachRecordToItsSavedAttemptExactly(t *testing.T) {
	withSkill, ablated := armDir(t), armDir(t)
	// File order (a1, a2) is the reverse of key order, so a join by order fails.
	a := newAttemptIn(t, withSkill, "caliper-a1").keyed("task-002", 1)
	a.serve()
	a.startRun()
	a.cleanup()
	newAttemptIn(t, withSkill, "caliper-a2").keyed("task-001", 1).cleanup()
	newAttemptIn(t, ablated, "caliper-b1").inArm("agent-ci").keyed("task-002", 1).cleanup()
	rev := a.built()
	r := a.arms(rev, "--arm", "full-gate", withSkill,
		keyedResult(t, withSkill, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}, saved{"task-002", 1, "task_fail"}),
		"--arm", "agent-ci", ablated,
		keyedResult(t, ablated, "result-q1-ablated.json", false, saved{"task-001", 1, "infra_error"}, saved{"task-002", 1, "pass"}))

	want(t, "with-skill as run", r.AsRun.ByArm["full-gate"].cells(), []string{"no-home/not assigned/pass=1", "ran/not assigned/task_fail=1"})
	want(t, "ablated as run", r.AsRun.ByArm["agent-ci"].cells(), []string{"indeterminate/not assigned/infra_error=1", "no-home/not assigned/pass=1"})
	want(t, "all arms as run", r.AsRun.AllArms.cells(),
		[]string{"indeterminate/not assigned/infra_error=1", "no-home/not assigned/pass=2", "ran/not assigned/task_fail=1"})
	for state := range r.AsRun.AllArms {
		if _, ok := r.AsAssigned["full-gate"].Counts.GateObservation[state]; !ok {
			t.Errorf("as run state %q is not a gate_observation state", state)
		}
	}
	var got []string
	for _, arm := range []string{"full-gate", "agent-ci"} {
		ar := r.AsAssigned[arm]
		want(t, arm+" missing, beyond, keyless", []int{ar.MissingRecords, ar.RecordsBeyondSaved, len(ar.RecordsWithoutKey)}, []int{map[string]int{"full-gate": 0, "agent-ci": 1}[arm], 0, 0})
		for _, at := range ar.Attempts {
			got = append(got, fmt.Sprintf("%s %s %v/%v %s %s", at.Arm, at.Attempt, at.TaskID, at.CaliperAttempt, at.TaskOutcome, at.StageExecution.Verdict))
		}
	}
	want(t, "attempts", got, []string{
		"full-gate caliper-a1 task-002/1 task_fail review executed",
		"full-gate caliper-a2 task-001/1 pass no stage executed",
		"agent-ci caliper-b1 task-002/1 pass no stage executed",
		"agent-ci  task-001/1 infra_error cannot tell"})
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
	a.arms(rev, "--arm", "full-gate", arm, res) // the positive control
	b.keyed("task-001", 1).cleanup()
	if code, out, errs := a.byArm(rev, "--arm", "full-gate", arm, res); code != 2 || !strings.Contains(errs, "both name attempt") {
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
		r := a.arms(rev, "--arm", "full-gate", arm, res)
		ws := r.AsAssigned["full-gate"]
		if len(ws.RecordsWithoutKey) != 1 || len(ws.Attempts) != 1 {
			t.Fatalf("records without key %+v, attempts %+v: want one of each", ws.RecordsWithoutKey, ws.Attempts)
		}
		k, at := ws.RecordsWithoutKey[0], ws.Attempts[0]
		want(t, "record without key", []any{k.Attempt, k.TaskID, k.TaskOutcome, k.GateObservation.State}, []any{a.name(), nil, "cannot tell", "no-home"})
		want(t, "its saved attempt", []any{at.Attempt, at.TaskID, at.TaskOutcome, at.GateObservation.Validity}, []any{"", "task-001", "pass", "indeterminate"})
		want(t, "records, missing", []int{ws.Records, ws.MissingRecords}, []int{1, 1})
		want(t, "as run", r.AsRun.ByArm["full-gate"].cells(), []string{"indeterminate/not assigned/pass=1"})
	}
}

// TestGateByArmRefusesARecordFromAnotherArm: a schema 6 record names the arm
// its cleanup ran in. In another arm's folder, or with no arm, it is not this
// arm's attempt, so the report refuses (exit 2) rather than count it there.
func TestGateByArmRefusesARecordFromAnotherArm(t *testing.T) {
	arm := armDir(t)
	a := newAttemptIn(t, arm, "caliper-a1").inArm("agent-ci")
	a.cleanup()
	rev := a.built()
	res := keyedResult(t, arm, "result-q1-ablated.json", false, saved{"task-001", 1, "pass"})
	a.arms(rev, "--arm", "agent-ci", arm, res) // the positive control
	for _, other := range []string{"full-gate", ""} {
		a.arm = other
		a.cleanup()
		if code, out, errs := a.byArm(rev, "--arm", "agent-ci", arm, res); code != 2 || !strings.Contains(errs, "was written in arm") {
			t.Errorf("a record of arm %q: exit %d, want 2 naming its arm\n%s%s", other, code, out, errs)
		}
	}
}

// setUp is an attempt in arm with key (task-001, n) whose setup built the
// benign subject, so its remote has the arm's hooks, and which then pushed
// nothing and started no service before its cleanup: gate no-home, CI not
// invoked, and in agent-ci-review no push, not reviewed.
func setUp(t *testing.T, dir, name, arm string, n int) *attempt {
	t.Helper()
	a := newAttemptIn(t, dir, name).inArm(arm).keyed("task-001", n)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	a.cleanup()
	return a
}

// reviewStates is a report's review_execution counts with every state named.
func reviewStates(executed, noPush, cannot, notAssigned int) map[string]int {
	return map[string]int{"review executed": executed, "no push, not reviewed": noPush, "cannot tell": cannot, "not assigned": notAssigned}
}

// TestGateByArmReadsMissingReviewEvidenceAsCannotTell: agent-ci-review
// assigns a reviewer, so an attempt there whose review evidence is missing is
// cannot tell, never a review that did not run, in its verdict, the arm's
// counts and "As run": a record with no review_checks (cleanup could not read
// the review directory) and one whose setup.json is gone (labeled edits), and
// a saved attempt no record names. The same record whole is no push, not
// reviewed (the positive control). agent-ci assigns no reviewer: there every
// attempt is not assigned, the one no record names included.
func TestGateByArmReadsMissingReviewEvidenceAsCannotTell(t *testing.T) {
	arm, other := armDir(t), armDir(t)
	a := setUp(t, arm, "caliper-r1", "agent-ci-review", 1)
	setUp(t, other, "caliper-c1", "agent-ci", 1)
	rev := a.built()
	two := []saved{{"task-001", 1, "pass"}, {"task-001", 2, "task_fail"}}
	args := []string{"--arm", "agent-ci-review", arm, keyedResult(t, arm, "result-q1-ablated.json", false, two...),
		"--arm", "agent-ci", other, keyedResult(t, other, "result-q1-ablated.json", false, two...)}
	r := a.arms(rev, args...)
	want(t, "report schema", r.Schema, 3)
	ar := r.AsAssigned["agent-ci-review"]
	want(t, "the record whole, and the attempt no record names", []string{ar.Attempts[0].ReviewExecution.Verdict, ar.Attempts[1].ReviewExecution.Verdict},
		[]string{"no push, not reviewed", "cannot tell"})
	want(t, "agent-ci assigns no reviewer", r.AsAssigned["agent-ci"].ReviewExecution, reviewStates(0, 0, 0, 2))
	for _, c := range []struct {
		what string
		edit func(map[string]any)
	}{
		{"no review_checks", func(rec map[string]any) { delete(rec, "review_checks") }},
		{"no setup.json", func(rec map[string]any) {
			delete(rec["review_checks"].(map[string]any)["files"].(map[string]any), "setup.json")
		}},
	} {
		a.cleanup() // the record as cleanup.sh wrote it, then this case's edit
		a.copyRecord(a.records(), a.name(), c.edit)
		r = a.arms(rev, args...)
		ar = r.AsAssigned["agent-ci-review"]
		if got := ar.Attempts[0].ReviewExecution; got.Verdict != "cannot tell" || !strings.Contains(got.Reason, "review evidence") {
			t.Errorf("%s: review_execution = %+v, want cannot tell from the review evidence", c.what, got)
		}
		want(t, c.what+": review counts", ar.ReviewExecution, reviewStates(0, 0, 2, 0))
		want(t, c.what+": as run", r.AsRun.ByArm["agent-ci-review"].cells(),
			[]string{"indeterminate/cannot tell/task_fail=1", "no-home/cannot tell/pass=1"})
	}
}

// TestGateByArmNeedsSchema7ForReview: review_checks came with record schema 7
// (a#964), so in agent-ci-review a record before it cannot show the reviewer
// ran and is cannot tell, never not measured: schema 6 (arm and CI, no review
// evidence), 5 and 4 (labeled edits of the same record). Before schema 6 no
// record shows CI either, and CI is assigned in every arm, so ci_execution is
// cannot tell too. In agent-ci the schema 5 record's reviewer is not assigned.
// The schema 7 record is the positive control.
func TestGateByArmNeedsSchema7ForReview(t *testing.T) {
	arm, other := armDir(t), armDir(t)
	a := setUp(t, arm, "caliper-r1", "agent-ci-review", 1)
	b := setUp(t, other, "caliper-c1", "agent-ci", 1)
	rev := a.built()
	one := saved{"task-001", 1, "pass"}
	args := []string{"--arm", "agent-ci-review", arm, keyedResult(t, arm, "result-q1-ablated.json", false, one),
		"--arm", "agent-ci", other, keyedResult(t, other, "result-q1-ablated.json", false, one)}
	got := a.arms(rev, args...).AsAssigned["agent-ci-review"].Attempts[0]
	want(t, "schema 7", []string{got.ReviewExecution.Verdict, got.CIExecution.Verdict}, []string{"no push, not reviewed", "not invoked"})
	b.copyRecord(b.records(), b.name(), func(rec map[string]any) {
		rec["schema"] = 5
		delete(rec, "arm")
		delete(rec, "ci_checks")
		delete(rec, "review_checks")
	})
	for _, c := range []struct {
		schema int
		ci     string
	}{{6, "not invoked"}, {5, "cannot tell"}, {4, "cannot tell"}} {
		schema := c.schema
		a.cleanup() // the schema 7 record as cleanup.sh wrote it, then this case's edit
		a.copyRecord(a.records(), a.name(), func(rec map[string]any) {
			rec["schema"] = schema
			delete(rec, "review_checks")
			if schema < 6 {
				delete(rec, "arm")
				delete(rec, "ci_checks")
			}
			if schema < 5 {
				delete(rec, "wait_down")
			}
		})
		r := a.arms(rev, args...)
		got = r.AsAssigned["agent-ci-review"].Attempts[0]
		want(t, fmt.Sprintf("schema %d in agent-ci-review", schema), []string{got.ReviewExecution.Verdict, got.CIExecution.Verdict},
			[]string{"cannot tell", c.ci})
		old := r.AsAssigned["agent-ci"].Attempts[0]
		want(t, "schema 5 in agent-ci", []string{old.ReviewExecution.Verdict, old.CIExecution.Verdict}, []string{"not assigned", "cannot tell"})
	}
}

// TestGateByArmListsContaminationPerAttempt: agent-ci withholds the gate, so
// each attempt says whether it used the gate anyway, and each contaminated or
// cannot-tell attempt is listed by its key with its run ids, never folded
// into a rate. Its attempts: one whose gate ran (contaminated, with the run's
// id), one never served (no-home: clean, carrying the caveat that no persisted
// home is not proof the gate never started, and not listed), one whose service
// was down at cleanup (indeterminate: cannot tell, never clean), and a saved
// attempt no record names (cannot tell). agent-ci-review withholds the gate
// too, so its attempt no record names is listed; full-gate does not, so its
// attempt is not withheld and nothing is listed.
func TestGateByArmListsContaminationPerAttempt(t *testing.T) {
	ci, review, full := armDir(t), armDir(t), armDir(t)
	ran := newAttemptIn(t, ci, "caliper-a1").inArm("agent-ci").keyed("task-001", 1)
	ran.serve()
	id := ran.startRun()
	ran.cleanup()
	newAttemptIn(t, ci, "caliper-a2").inArm("agent-ci").keyed("task-001", 2).cleanup()
	down := newAttemptIn(t, ci, "caliper-a3").inArm("agent-ci").keyed("task-001", 3)
	down.serve()
	down.assistant(down.root, "service", "stop", "--force")
	if !down.stopped() {
		t.Fatal("the service still runs 30s after its stop")
	}
	down.cleanup()
	want(t, "mkdir", os.Mkdir(filepath.Join(review, "caliper-q1-gate"), 0o755), nil)
	newAttemptIn(t, full, "caliper-f1").keyed("task-001", 1).cleanup()
	rev := ran.built()
	r := ran.arms(rev,
		"--arm", "agent-ci", ci, keyedResult(t, ci, "result-q1-ablated.json", false,
			saved{"task-001", 1, "pass"}, saved{"task-001", 2, "pass"}, saved{"task-001", 3, "task_fail"}, saved{"task-001", 4, "infra_error"}),
		"--arm", "agent-ci-review", review, keyedResult(t, review, "result-q1-ablated.json", false, saved{"task-001", 1, "pass"}),
		"--arm", "full-gate", full, keyedResult(t, full, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}))
	listed := func(arm string) []string {
		var got []string
		for _, at := range r.AsAssigned[arm].Contamination.Attempts {
			got = append(got, fmt.Sprintf("%s/%d %s %v", at.TaskID, at.CaliperAttempt, at.Contamination.Verdict, at.RunIDs))
		}
		return got
	}
	counts := func(contaminated, cannot, clean, notWithheld int) map[string]int {
		return map[string]int{"contaminated": contaminated, "cannot tell": cannot, "clean": clean, "not withheld": notWithheld}
	}
	ac := r.AsAssigned["agent-ci"]
	want(t, "agent-ci listed", listed("agent-ci"), []string{"task-001/1 contaminated [" + id + "]", "task-001/3 cannot tell []", "task-001/4 cannot tell []"})
	want(t, "agent-ci counts", ac.Contamination.Counts, counts(1, 2, 1, 0))
	clean := ac.Attempts[1].Contamination
	want(t, "the no-home attempt", []any{clean.Verdict, clean.GateObservation, strings.Contains(clean.Caveat, "not proof that the gate never started")},
		[]any{"clean", "no-home", true})
	want(t, "the down attempt", []string{ac.Attempts[2].GateObservation.Validity, ac.Attempts[2].Contamination.Verdict}, []string{"indeterminate", "cannot tell"})
	want(t, "agent-ci-review listed", listed("agent-ci-review"), []string{"task-001/1 cannot tell []"})
	want(t, "full-gate", []any{listed("full-gate"), r.AsAssigned["full-gate"].Contamination.Counts}, []any{[]string(nil), counts(0, 0, 0, 1)})
}

// TestGateByArmAsRunSeparatesTheCheckers: "As run" rows are what checked the
// attempt, the gate's state and then the reviewer's, so two attempts of
// agent-ci-review with one gate state (no-home) and one task outcome are in two
// cells when one push was reviewed and approved and the other attempt pushed
// nothing. A saved attempt no record names is cannot tell for both, agent-ci's
// attempt is not assigned a reviewer, and each gate state names every review
// state. The arm also counts its CI and review verdicts.
func TestGateByArmAsRunSeparatesTheCheckers(t *testing.T) {
	a, _, _ := reviewArm(t, standin.Script{Steps: []standin.Step{{Reply: standin.Text(approve)}}})
	if code, errs := a.push(); code != 0 {
		t.Fatalf("the approved push was refused: exit %d\n%s", code, errs)
	}
	a.cleanup()
	setUp(t, a.tmpdir, "caliper-r2", "agent-ci-review", 2)
	other := armDir(t)
	setUp(t, other, "caliper-c1", "agent-ci", 1)
	r := a.arms(a.built(),
		"--arm", "agent-ci-review", a.tmpdir, keyedResult(t, a.tmpdir, "result-q1-ablated.json", false,
			saved{"task-001", 1, "pass"}, saved{"task-001", 2, "pass"}, saved{"task-001", 3, "infra_error"}),
		"--arm", "agent-ci", other, keyedResult(t, other, "result-q1-ablated.json", false, saved{"task-001", 1, "pass"}))
	reviewed := []string{"indeterminate/cannot tell/infra_error=1", "no-home/no push, not reviewed/pass=1", "no-home/review executed/pass=1"}
	want(t, "agent-ci-review as run", r.AsRun.ByArm["agent-ci-review"].cells(), reviewed)
	want(t, "all arms as run", r.AsRun.AllArms.cells(), append(reviewed[:2:2], "no-home/not assigned/pass=1", reviewed[2]))
	for state, reviews := range r.AsRun.AllArms {
		want(t, state+"'s review states", len(reviews), 4)
	}
	ar := r.AsAssigned["agent-ci-review"]
	want(t, "review counts", ar.ReviewExecution, reviewStates(1, 1, 1, 0))
	want(t, "CI counts", ar.CIExecution, map[string]int{"ci ran": 1, "not invoked": 1, "cannot tell": 1})
}

// TestGateByArmCountsJudgeCallsOnlyWhereRecorded: an attempt has a judge call
// when caliper recorded its judge_seconds; null is no call recorded, counted
// apart and never as a call measured at 0 s. A saved attempt with no
// judge_seconds, or one that is not a number or null, is not a caliper 0.19.0
// attempt, so the result is refused (exit 2).
func TestGateByArmCountsJudgeCallsOnlyWhereRecorded(t *testing.T) {
	arm := armDir(t)
	a := newAttemptIn(t, arm, "caliper-a1")
	a.cleanup()
	rev := a.built()
	res := keyedResult(t, arm, "result-q1-with-skill.json", false, saved{"task-001", 1, "pass"}, saved{"task-001", 2, "pass"})
	judged := func(edit func(second map[string]any)) {
		data, err := os.ReadFile(res)
		var doc map[string]any
		if err == nil {
			err = json.Unmarshal(data, &doc)
		}
		must(t, err)
		edit(doc["task_results"].([]any)[0].(map[string]any)["attempts"].([]any)[1].(map[string]any))
		data, err = json.Marshal(doc)
		must(t, err)
		must(t, os.WriteFile(res, data, 0o644))
	}
	judged(func(second map[string]any) { second["judge_seconds"] = 12.5 })
	want(t, "judge calls", a.arms(rev, "--arm", "full-gate", arm, res).AsAssigned["full-gate"].JudgeCalls,
		map[string]int{"attempts_with_a_call": 1, "attempts_none_recorded": 1})
	for what, edit := range map[string]func(map[string]any){
		"missing": func(second map[string]any) { delete(second, "judge_seconds") },
		"boolean": func(second map[string]any) { second["judge_seconds"] = true },
	} {
		judged(edit)
		if code, out, errs := a.byArm(rev, "--arm", "full-gate", arm, res); code != 2 || !strings.Contains(errs, "judge_seconds") {
			t.Errorf("judge_seconds %s: exit %d, want 2 naming it\n%s%s", what, code, out, errs)
		}
	}
}
