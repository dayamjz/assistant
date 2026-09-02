package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestReopeningKeepsEverything(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	s := openStoreAt(t, path)
	run := seedRun(t, s)
	task := seedTask(t, s, "task-1")
	if _, err := s.SetTaskState(ctx, task.ID, "running", "supervisor", Known("stage review")); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openStoreAt(t, path)
	if got, err := again.Run(ctx, run.ID); err != nil || got.Build != run.Build {
		t.Fatalf("the run did not survive reopening: %+v, %v", got, err)
	}
	st, err := again.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if st.State != "running" || st.Revision != 2 {
		t.Fatalf("the task state did not survive reopening: %+v", st)
	}
	open, err := again.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d holds survived reopening, want 1", len(open))
	}
}

func TestAccessorsRefuseAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}

	checks := map[string]func() error{
		"Repository":  func() error { _, err := s.Repository(ctx, "repo-1"); return err },
		"Run":         func() error { _, err := s.Run(ctx, "run-1"); return err },
		"CreateRun":   func() error { _, err := s.CreateRun(ctx, completeRun()); return err },
		"StageResult": func() error { _, err := s.StageResult(ctx, "run-1", "review"); return err },
		"Rounds":      func() error { _, err := s.Rounds(ctx, "run-1"); return err },
		"Checkpoint":  func() error { _, err := s.Checkpoint(ctx, "run-1"); return err },
		"TaskState":   func() error { _, err := s.TaskState(ctx, "task-1"); return err },
		"TaskEvents":  func() error { _, err := s.TaskEvents(ctx, "task-1", 0, 0); return err },
		"OpenHolds":   func() error { _, err := s.OpenHolds(ctx); return err },
		"AppendEvent": func() error { _, err := s.AppendTaskEvent(ctx, "task-1", "kind", ""); return err },
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrClosed) {
			t.Fatalf("%s after Close returned %v, want ErrClosed", name, err)
		}
	}
}

func TestOpenRefusesAnEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), "", WithRedactor(workingRedactor())); err == nil {
		t.Fatal("Open accepted an empty path")
	}
}

func TestPathIsAbsolute(t *testing.T) {
	s := openStore(t)
	if !filepath.IsAbs(s.Path()) {
		t.Fatalf("Path returned %q, which is not absolute", s.Path())
	}
}

// The settings the package comment's durability and concurrency claims rest on
// are in effect on both pools of a store that opened, which is what the request
// in the connection string is for and what a request alone does not establish.
// This is the accepting path for the check openPool applies, so it cannot pass
// by refusing everything.
func TestOpenedStoreReportsTheSettingsItsGuaranteesRestOn(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	for pool, db := range map[string]*sql.DB{"writer": s.write, "reader": s.read} {
		for _, want := range requiredSettings() {
			var got string
			if err := db.QueryRowContext(ctx, "PRAGMA "+want.name).Scan(&got); err != nil {
				t.Fatalf("reading %s from the %s pool: %v", want.name, pool, err)
			}
			if !strings.EqualFold(got, want.want) {
				t.Fatalf("the %s pool was opened asking for %s(%s) and reports %s, want %s",
					pool, want.name, want.arg, got, want.want)
			}
		}
	}
}

// otherArg is a value each setting can be asked for that a connection reports
// differently from the value requiredSettings asks for. It is the counterexample
// the refusal below needs, and a setting with no entry here fails rather than
// going untested.
var otherArg = map[string]string{
	"busy_timeout": "1",
	"journal_mode": "DELETE",
	"foreign_keys": "0",
	"synchronous":  "FULL",
}

// A pool whose connection does not come back carrying one of those settings is
// refused rather than handed out, so the store never runs on a configuration
// its documentation does not describe.
func TestOpenPoolRefusesASettingThatDidNotApply(t *testing.T) {
	for _, s := range requiredSettings() {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")

			other, ok := otherArg[s.name]
			if !ok {
				t.Fatalf("%s has no counterexample value, so its refusal is untested", s.name)
			}
			asked := poolDSN(path, false)
			wrong := strings.Replace(asked,
				s.name+"("+s.arg+")", s.name+"("+other+")", 1)
			if wrong == asked {
				t.Fatalf("the connection string does not ask for %s(%s), so this case tests nothing",
					s.name, s.arg)
			}

			// The same path opens cleanly with the settings this package asks
			// for, so the refusal below is the changed setting and not the
			// database.
			accepted, err := openPool(ctx, asked, false)
			if err != nil {
				t.Fatalf("openPool refused the settings this package asks for: %v", err)
			}
			if err := accepted.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			db, err := openPool(ctx, wrong, false)
			if err == nil {
				_ = db.Close()
				t.Fatal("openPool handed back a pool whose settings are not the ones asked for")
			}
			if db != nil {
				t.Fatal("openPool returned a pool alongside its refusal")
			}
			if !errors.Is(err, ErrSettingNotApplied) {
				t.Fatalf("the refusal is not one a caller can branch on: %v", err)
			}
			if !strings.Contains(err.Error(), s.name) {
				t.Fatalf("the refusal does not name the setting: %v", err)
			}
		})
	}
}
