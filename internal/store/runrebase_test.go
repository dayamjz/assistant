package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A run rebase record is held to its run: it refuses a record naming no
// commit, a run that does not exist, and a submitted commit the run was not
// given, and writes nothing on any of them. The same rebase written twice is
// one row, read back unchanged; a second rebase of the run is a second row,
// in the order written. The table's seals refuse an update, and a delete
// while the run stands.
func TestARunRebaseRecordIsWrittenOnceAndHeldToItsRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	first := RunRebase{Run: run.ID, Submitted: run.SubmittedHead, Before: run.SubmittedHead, Onto: "bbbb", After: "cccc"}

	for name, r := range map[string]RunRebase{
		"no run":             {Submitted: first.Submitted, Before: first.Before, Onto: first.Onto, After: first.After},
		"no submitted":       {Run: run.ID, Before: first.Before, Onto: first.Onto, After: first.After},
		"no before":          {Run: run.ID, Submitted: first.Submitted, Onto: first.Onto, After: first.After},
		"no onto":            {Run: run.ID, Submitted: first.Submitted, Before: first.Before, After: first.After},
		"no after":           {Run: run.ID, Submitted: first.Submitted, Before: first.Before, Onto: first.Onto},
		"a spaced commit":    {Run: run.ID, Submitted: first.Submitted, Before: first.Before, Onto: first.Onto, After: "cc cc"},
		"another submission": {Run: run.ID, Submitted: "ffff", Before: first.Before, Onto: first.Onto, After: first.After},
		"a missing run":      {Run: "nope", Submitted: first.Submitted, Before: first.Before, Onto: first.Onto, After: first.After},
	} {
		_, err := s.RecordRunRebase(ctx, r)
		switch {
		case err == nil:
			t.Fatalf("%s: the record %+v was written", name, r)
		case name == "another submission" && !errors.Is(err, ErrRebaseNotOfRun):
			t.Fatalf("%s: %v, want ErrRebaseNotOfRun", name, err)
		case name == "a missing run" && !errors.Is(err, ErrNotFound):
			t.Fatalf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if got, err := s.RunRebases(ctx, run.ID); err != nil || len(got) != 0 {
		t.Fatalf("after the refusals the run has %+v (%v), want no record", got, err)
	}

	written, err := s.RecordRunRebase(ctx, first)
	if err != nil {
		t.Fatalf("RecordRunRebase: %v", err)
	}
	if written.Run != first.Run || written.Submitted != first.Submitted || written.Before != first.Before ||
		written.Onto != first.Onto || written.After != first.After || written.RecordedAt.IsZero() {
		t.Fatalf("the record came back as %+v", written)
	}
	again, err := s.RecordRunRebase(ctx, first)
	if err != nil || again != written {
		t.Fatalf("the same rebase written again read back %+v (%v), want %+v", again, err, written)
	}
	second := RunRebase{Run: run.ID, Submitted: run.SubmittedHead, Before: "dddd", Onto: "eeee", After: "9999"}
	if _, err := s.RecordRunRebase(ctx, second); err != nil {
		t.Fatalf("a second rebase: %v", err)
	}
	all, err := s.RunRebases(ctx, run.ID)
	if err != nil || len(all) != 2 || all[0] != written || all[1].After != second.After {
		t.Fatalf("the run's rebases read %+v (%v), want the first and then the second", all, err)
	}

	if _, err := s.write.ExecContext(ctx, `UPDATE run_rebase SET after = 'abcd' WHERE run = ?`, run.ID); err == nil ||
		!strings.Contains(err.Error(), "written once") {
		t.Fatalf("an update of a rebase record = %v, want the seal's refusal", err)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM run_rebase WHERE run = ?`, run.ID); err == nil ||
		!strings.Contains(err.Error(), "while its run stands") {
		t.Fatalf("a delete of a rebase record while its run stands = %v, want the seal's refusal", err)
	}
}
