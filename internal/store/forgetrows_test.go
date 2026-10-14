package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// runKeyedTables derives, from the database's own catalog, every table that
// keeps rows per run: a table with a foreign key into the run table, a table
// with a column named run or run_id, and, transitively, a table with a foreign
// key into one of those. The derivation stops at the tables named in stop,
// whose rows forgetting a repository refuses over rather than removes, so
// nothing reachable only through them is ever forgotten.
//
// It reads the catalog rather than a list, so a table a later migration adds
// that keeps rows per run is found whether or not anybody remembered it.
func runKeyedTables(t *testing.T, s *Store, stop map[string]bool) []string {
	t.Helper()
	ctx := context.Background()
	refs := map[string][]string{} // table -> tables it references
	rows, err := s.read.QueryContext(ctx, `
		SELECT m.name, f."table"
		FROM sqlite_master AS m JOIN pragma_foreign_key_list(m.name) AS f
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("reading the foreign keys: %v", err)
	}
	for rows.Next() {
		var table, target string
		if err := rows.Scan(&table, &target); err != nil {
			t.Fatal(err)
		}
		refs[table] = append(refs[table], target)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	keyed := map[string]bool{}
	named, err := s.read.QueryContext(ctx, `
		SELECT m.name
		FROM sqlite_master AS m JOIN pragma_table_info(m.name) AS c
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name <> 'run'
			AND c.name IN ('run', 'run_id')`)
	if err != nil {
		t.Fatalf("reading the columns: %v", err)
	}
	for named.Next() {
		var table string
		if err := named.Scan(&table); err != nil {
			t.Fatal(err)
		}
		keyed[table] = true
	}
	if err := named.Close(); err != nil {
		t.Fatal(err)
	}
	for changed := true; changed; {
		changed = false
		for table, targets := range refs {
			if keyed[table] || table == "run" {
				continue
			}
			for _, target := range targets {
				if target == "run" || (keyed[target] && !stop[target]) {
					keyed[table] = true
					changed = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(keyed))
	for table := range keyed {
		out = append(out, table)
	}
	sort.Strings(out)
	return out
}

// tableRows counts every row of one table. The table name comes from the
// catalog, never from input.
func tableRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.read.QueryRowContext(context.Background(), `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

// refusedRunTables is refusedRunRecords as the set runKeyedTables stops at.
func refusedRunTables() map[string]bool {
	out := map[string]bool{}
	for table := range refusedRunRecords {
		out[table] = true
	}
	return out
}

// unseedableRunTables are run-keyed tables no row can be written to, each
// with why. The test proves the reason rather than taking it: an INSERT is
// attempted and must be refused.
var unseedableRunTables = map[string]string{
	"checkpoint": `INSERT INTO checkpoint (run_id, state, position, revision, written_at) VALUES ('run-1', x'00', 'review', 1, 'now')`,
}

// keyedThroughAKind are tables that keep rows per run through a key whose
// meaning a kind column decides, which the catalog cannot derive, each with
// why. The forget test seeds each and requires it emptied, and the placement
// test requires runRecords to name it.
var keyedThroughAKind = map[string]string{
	"seen_mark": "a mark of kind merge is keyed by the merge record's run",
}

// Issue #177: a repository whose run has a row in every table that keeps rows
// per run is forgotten whole, in one transaction, and not one row of any of
// those tables is left. The tables are derived from the catalog, and each is
// shown to hold a row before the removal, so the assertion afterwards is not
// passing over an empty table and a table a later migration adds fails here
// until it is seeded and handled.
func TestForgettingARepositoryRemovesItsRowsFromEveryRunKeyedTable(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := runningRun(t, s)

	if _, err := s.UpsertStageResult(ctx, StageResult{RunID: run.ID, Stage: "review", Status: StagePassed}); err != nil {
		t.Fatalf("UpsertStageResult: %v", err)
	}
	if _, err := s.AppendRound(ctx, Round{RunID: run.ID, Stage: "review", Number: 1, Summary: "nothing found"}); err != nil {
		t.Fatalf("AppendRound: %v", err)
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: "hold-1", RunID: Known(run.ID), Subject: "a decision", Detail: "about it"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.AppendGraphCheckpoint(ctx, run.ID, "", 0, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("AppendGraphCheckpoint: %v", err)
	}
	now := time.Now().UTC()
	if _, err := s.AppendStageExecution(ctx, StageExecution{Run: run.ID, Stage: "review", Started: now, Ended: now}); err != nil {
		t.Fatalf("AppendStageExecution: %v", err)
	}
	answer, err := s.AppendDecisionAnswer(ctx, DecisionAnswer{
		Run: run.ID, Stage: "review", Check: "trace-reason", Path: "a.go", Model: "m-1", Probability: Known(0.5), Cutoff: 0.8,
	})
	if err != nil {
		t.Fatalf("AppendDecisionAnswer: %v", err)
	}
	if n, err := s.AppendDecisionFollowUp(ctx, run.ID, "a.go", FollowUpFinding, "review", "note", answer.Seq); err != nil || n != 1 {
		t.Fatalf("AppendDecisionFollowUp = %d, %v; want one follow-up", n, err)
	}
	if _, err := s.AppendAgentInvocation(ctx, AgentInvocation{Agent: "claude", Purpose: "review", Started: now,
		Run: Known(run.ID), Stage: Known("review")}); err != nil {
		t.Fatalf("AppendAgentInvocation: %v", err)
	}
	if _, err := s.ArmChecksWait(ctx, run.ID, "bbbb", now); err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if _, err := s.RecordRunCopySweep(ctx, RunCopySweep{Run: run.ID, Outcome: SweepNothingStanding, Occasion: SweptAtFinish}); err != nil {
		t.Fatalf("RecordRunCopySweep: %v", err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofDefaultBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatalf("RecordRunCleanup: %v", err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, now); err != nil {
		t.Fatalf("MarkRunCleanupRemoved: %v", err)
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, run.ID, []RunStatus{RunRunning}, RunPassed,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}, HoldClosure{}); err != nil {
		t.Fatalf("running to passed on a merged pull request: %v", err)
	}
	if _, err := s.MarkSeen(ctx, []SeenRecord{{Kind: SeenMerge, Key: run.ID}}); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	refused := refusedRunTables()
	tables := runKeyedTables(t, s, refused)
	for table := range keyedThroughAKind {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	if len(tables) == 0 {
		t.Fatal("the catalog names no table keyed by run, so the derivation looked at nothing")
	}
	for _, table := range tables {
		if refused[table] {
			continue
		}
		if insert, ok := unseedableRunTables[table]; ok {
			if _, err := s.write.ExecContext(ctx, insert); err == nil || !strings.Contains(err.Error(), "is not a record") {
				t.Fatalf("%s was named unseedable and its seal did not refuse a row: %v", table, err)
			}
			continue
		}
		if tableRows(t, s, table) == 0 {
			t.Errorf("%s keeps rows per run and this test wrote it none; seed it here and decide what forgetting does with it", table)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	removed, err := s.ForgetRepository(ctx, run.RepositoryID)
	if err != nil {
		t.Fatalf("ForgetRepository: %v", err)
	}
	if removed != 1 {
		t.Fatalf("ForgetRepository removed %d runs, want 1", removed)
	}
	var left []string
	for _, table := range append(tables, "run", "repository") {
		if n := tableRows(t, s, table); n != 0 {
			left = append(left, table)
		}
	}
	if len(left) != 0 {
		t.Fatalf("forgetting the repository left rows in %s", strings.Join(left, ", "))
	}
}

// Every table the catalog says keeps rows per run is placed: either its rows
// go with the run (runRecords) or a row there refuses the removal
// (refusedRunRecords), and neither list names a table the catalog does not
// derive. A table a later migration adds that keeps rows per run fails here
// until it is placed in one of the two, which is what keeps issue #177 from
// recurring with the next table. The positive control is that the derivation
// finds the run's oldest dependants and a table found only through a refused
// one is not derived, so the comparison is not over an empty or unbounded set.
func TestEveryRunKeyedTableIsForgottenOrRefusedOver(t *testing.T) {
	s := openStore(t)
	derived := runKeyedTables(t, s, refusedRunTables())
	for table := range keyedThroughAKind {
		derived = append(derived, table)
	}
	sort.Strings(derived)
	for _, want := range []string{"stage_result", "round", "hold", "task"} {
		if i := sort.SearchStrings(derived, want); i == len(derived) || derived[i] != want {
			t.Fatalf("the derivation %v does not find %s, which references run(id)", derived, want)
		}
	}
	if i := sort.SearchStrings(derived, "task_event"); i < len(derived) && derived[i] == "task_event" {
		t.Fatalf("the derivation %v went through task, whose rows are refused over rather than forgotten", derived)
	}

	placed := map[string]bool{}
	for _, r := range runRecords {
		if placed[r.table] {
			t.Fatalf("runRecords names %s twice", r.table)
		}
		placed[r.table] = true
	}
	for table, refusal := range refusedRunRecords {
		if placed[table] {
			t.Fatalf("%s is both forgotten and refused over", table)
		}
		if refusal == nil {
			t.Fatalf("%s is refused over with no refusal", table)
		}
		placed[table] = true
	}
	got := make([]string, 0, len(placed))
	for table := range placed {
		got = append(got, table)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, derived) {
		t.Fatalf("the tables ForgetRepository places are %v, and the catalog derives %v", got, derived)
	}
}

// A run process record is the only thing a later recovery reaps an agent
// group from, so a repository whose finished run still has one is refused
// with ErrRunProcessRecorded, by the question and by the removal alike, and
// nothing is removed. Clearing the record, which is what a recovery that
// verified the group gone does, is the control: the same removal then
// completes, so what was refused was the record and not the repository.
func TestForgettingARepositoryIsRefusedWhileARunProcessIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.RecordProcess(ctx, ProcessRecord{RunID: run.ID, Stage: "review", Group: 4242}); err != nil {
		t.Fatalf("RecordProcess: %v", err)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}

	if err := s.RepositoryRemovable(ctx, run.RepositoryID); !errors.Is(err, ErrRunProcessRecorded) {
		t.Fatalf("RepositoryRemovable = %v, want ErrRunProcessRecorded", err)
	}
	if _, err := s.ForgetRepository(ctx, run.RepositoryID); !errors.Is(err, ErrRunProcessRecorded) {
		t.Fatalf("ForgetRepository = %v, want ErrRunProcessRecorded", err)
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the refused removal took the run: %v", err)
	}
	if records, err := s.ProcessRecords(ctx); err != nil || len(records) != 1 {
		t.Fatalf("the refused removal left (%d process records, %v), want the one", len(records), err)
	}

	if err := s.ClearProcess(ctx, run.ID, 4242); err != nil {
		t.Fatalf("ClearProcess: %v", err)
	}
	if _, err := s.ForgetRepository(ctx, run.RepositoryID); err != nil {
		t.Fatalf("ForgetRepository once the record was cleared: %v", err)
	}
}

// The written-once seals still refuse a deletion while what they describe
// stands, so narrowing them for ForgetRepository opened no other path: a
// merge record and a run cleanup record of a run that is still there, and a
// seen mark of a merge record that is still there, cannot be deleted by any
// statement. The forget test above is the control that the
// same rows do go once their run does.
func TestWrittenOnceRunRecordsAreNotDeletedWhileTheirRunStands(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := runningRun(t, s)
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofDefaultBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatalf("RecordRunCleanup: %v", err)
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, run.ID, []RunStatus{RunRunning}, RunPassed,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}, HoldClosure{}); err != nil {
		t.Fatalf("running to passed on a merged pull request: %v", err)
	}
	if _, err := s.MarkSeen(ctx, []SeenRecord{{Kind: SeenMerge, Key: run.ID}}); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	for table, remove := range map[string]string{
		"merge_record": `DELETE FROM merge_record WHERE run = ?`,
		"run_cleanup":  `DELETE FROM run_cleanup WHERE run = ?`,
		"seen_mark":    `DELETE FROM seen_mark WHERE kind = 'merge' AND key = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, remove, run.ID); err == nil ||
			!strings.Contains(err.Error(), "while") {
			t.Fatalf("deleting the %s row of a standing run = %v, want the seal's refusal", table, err)
		}
		if n := tableRows(t, s, table); n != 1 {
			t.Fatalf("%s holds %d rows after the refused delete, want 1", table, n)
		}
	}
}
