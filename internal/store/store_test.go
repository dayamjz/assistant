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
		"TaskState":   func() error { _, err := s.TaskState(ctx, "task-1"); return err },
		"TaskEvents":  func() error { _, err := s.TaskEvents(ctx, "task-1", 0, 0); return err },
		"OpenHolds":   func() error { _, err := s.OpenHolds(ctx); return err },
		"AppendEvent": func() error { _, err := s.AppendTaskEvent(ctx, "task-1", "kind", ""); return err },
		"AppendGraphCheckpoint": func() error {
			_, err := s.AppendGraphCheckpoint(ctx, "run-1", "", 0, []byte("cp"))
			return err
		},
		"CopyGraphCheckpoints": func() error {
			_, err := s.CopyGraphCheckpoints(ctx, "fork-1", [][]byte{[]byte("cp")})
			return err
		},
		"LatestGraphCheckpoint": func() error { _, err := s.LatestGraphCheckpoint(ctx, "run-1"); return err },
		"GraphCheckpointHistory": func() error {
			_, err := s.GraphCheckpointHistory(ctx, "run-1")
			return err
		},
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

// guaranteedSetting is this test's own statement about one connection setting:
// what a connection carrying it reports, and a value it can be asked for
// instead that reports differently.
type guaranteedSetting struct {
	// want is what a connection carrying the setting reports.
	want string
	// other is a value the setting can be asked for that a connection reports
	// differently from want, which is the counterexample the refusal needs.
	other string
}

// guaranteedSettings is the connection settings this store guarantees, written
// out here by hand and deliberately not derived from requiredSettings.
//
// requiredSettings is the one owner of what the store asks for and checks at
// run time, and that is why this list exists: a test that read its expectations
// back out of that list could not fail when a row is deleted, because the
// setting, its verification, and its coverage would go together and the suite
// would stay green while the database ran in a mode the package comment does
// not describe.
//
// So changing a setting is two edits on purpose. One in requiredSettings, and
// one here, and the second is what records the decision. Do not resolve the
// duplication by deriving this from the package.
var guaranteedSettings = map[string]guaranteedSetting{
	"busy_timeout": {want: "5000", other: "1"},
	"journal_mode": {want: "wal", other: "DELETE"},
	"foreign_keys": {want: "1", other: "0"},
	"synchronous":  {want: "1", other: "FULL"},
}

// The settings the store asks for and checks are exactly the ones above, in
// both directions: one that goes missing is a setting the store would open
// without, and one that appears is a setting nobody decided belongs.
func TestRequiredSettingsAreTheSettingsThisStoreGuarantees(t *testing.T) {
	asked := make(map[string]setting)
	for _, s := range requiredSettings() {
		if _, twice := asked[s.name]; twice {
			t.Fatalf("requiredSettings lists %s twice", s.name)
		}
		asked[s.name] = s
	}

	for name, guaranteed := range guaranteedSettings {
		s, ok := asked[name]
		if !ok {
			t.Fatalf("requiredSettings no longer asks for or checks %s, so the store would open without it", name)
		}
		if !strings.EqualFold(s.want, guaranteed.want) {
			t.Fatalf("requiredSettings expects %s to report %s, and this store guarantees %s",
				name, s.want, guaranteed.want)
		}
	}
	for name := range asked {
		if _, ok := guaranteedSettings[name]; !ok {
			t.Fatalf("requiredSettings asks for %s, which is not one of the settings this test states the store guarantees; add it there on purpose or drop it", name)
		}
	}
}

// Every setting this store guarantees is in effect on both of its pools, which
// is what the request in the connection string is for and what a request alone
// does not establish. This is the accepting path for the check openPool
// applies, so it cannot pass by refusing everything.
func TestOpenedStoreReportsTheSettingsItsGuaranteesRestOn(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	for pool, db := range map[string]*sql.DB{"writer": s.write, "reader": s.read} {
		for name, guaranteed := range guaranteedSettings {
			var got string
			if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
				t.Fatalf("reading %s from the %s pool: %v", name, pool, err)
			}
			if !strings.EqualFold(got, guaranteed.want) {
				t.Fatalf("the %s pool reports %s = %s, and this store guarantees %s",
					pool, name, got, guaranteed.want)
			}
		}
	}
}

// A pool whose connection does not come back carrying one of those settings is
// refused rather than handed out, so the store never runs on a configuration
// its documentation does not describe.
func TestOpenPoolRefusesASettingThatDidNotApply(t *testing.T) {
	for name, guaranteed := range guaranteedSettings {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")

			var asked setting
			for _, s := range requiredSettings() {
				if s.name == name {
					asked = s
				}
			}
			if asked.name == "" {
				t.Fatalf("the store no longer asks for %s, so nothing checks it", name)
			}

			dsn := poolDSN(path, false)
			wrong := strings.Replace(dsn,
				asked.name+"("+asked.arg+")", asked.name+"("+guaranteed.other+")", 1)
			if wrong == dsn {
				t.Fatalf("the connection string does not ask for %s(%s), so this case tests nothing",
					asked.name, asked.arg)
			}

			// The same path opens cleanly with the settings this package asks
			// for, so the refusal below is the changed setting and not the
			// database.
			accepted, err := openPool(ctx, dsn, false)
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
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("the refusal does not name the setting: %v", err)
			}
		})
	}
}
