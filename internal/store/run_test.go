package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func completeRun() Run {
	return Run{
		ID: "run-1", RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push",
		Build: testBuild(), ConfigDigest: "cfg-1",
	}
}

func seedRepository(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.UpsertRepository(context.Background(), Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
}

// Every run records the build that produced it, so a surprising verdict can be
// traced to the software that reached it. This is the refusing side.
func TestCreateRunRefusesAnUntraceableRun(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)

	cases := []struct {
		name   string
		mutate func(Run) Run
		want   error
	}{
		{"no build at all", func(r Run) Run { r.Build = Build{}; return r }, ErrBuildIdentityMissing},
		{"no toolchain", func(r Run) Run { r.Build.Go = ""; return r }, ErrBuildIdentityMissing},
		{"neither version nor revision", func(r Run) Run {
			r.Build.Version, r.Build.Revision = "", ""
			return r
		}, ErrBuildIdentityMissing},
		{"no configuration", func(r Run) Run { r.ConfigDigest = " "; return r }, ErrConfigDigestMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateRun(ctx, tc.mutate(completeRun()))
			if !errors.Is(err, tc.want) {
				t.Fatalf("CreateRun accepted an untraceable run: %v", err)
			}
			if _, err := s.Run(ctx, "run-1"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a refused CreateRun left a row behind: %v", err)
			}
		})
	}
}

// A run that names either a version or a revision is traceable, so it is
// accepted. Without this the refusals above would also pass if CreateRun
// refused everything.
func TestCreateRunAcceptsEitherHalfOfTheBuildIdentity(t *testing.T) {
	ctx := context.Background()
	for name, build := range map[string]Build{
		"version only":  {Version: "v1.2.3", Go: "go1.25.0"},
		"revision only": {Revision: "deadbeef", Modified: true, Go: "go1.25.0"},
		"both":          testBuild(),
	} {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			seedRepository(t, s)
			r := completeRun()
			r.Build = build
			stored, err := s.CreateRun(ctx, r)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			if stored.Build != build {
				t.Fatalf("the build came back as %+v, want %+v", stored.Build, build)
			}
			if stored.Build.Modified != build.Modified {
				t.Fatalf("the dirty-working-copy flag did not survive the round trip")
			}
		})
	}
}

func TestCurrentBuildIsUsable(t *testing.T) {
	b, err := CurrentBuild()
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("CurrentBuild returned an identity that does not validate: %v", err)
	}
	if b.String() == "unidentified build" {
		t.Fatal("CurrentBuild rendered as an unidentified build")
	}
}

func TestRunOptionalFieldsStartUnknown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)

	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if r.Status != RunPending {
		t.Fatalf("a new run has status %q, want %q", r.Status, RunPending)
	}
	for name, o := range map[string]Optional[string]{
		"current head":    r.CurrentHead,
		"approved commit": r.ApprovedCommit,
		"push binding":    r.PushBinding,
		"pull request":    r.PullRequest,
	} {
		if o.IsKnown() {
			t.Fatalf("a new run reports a known %s: %q", name, o)
		}
		if o.Or("fallback") != "fallback" {
			t.Fatalf("Or on an unknown %s did not return the fallback", name)
		}
	}

	for _, set := range []struct {
		name string
		fn   func() error
		read func(Run) Optional[string]
		want string
	}{
		{"head", func() error { return s.SetRunHead(ctx, r.ID, "cccc") }, func(r Run) Optional[string] { return r.CurrentHead }, "cccc"},
		{"approval", func() error { return s.SetRunApprovedCommit(ctx, r.ID, "cccc") }, func(r Run) Optional[string] { return r.ApprovedCommit }, "cccc"},
		{"push binding", func() error { return s.SetRunPushBinding(ctx, r.ID, "gate/topic") }, func(r Run) Optional[string] { return r.PushBinding }, "gate/topic"},
		{"pull request", func() error { return s.SetRunPullRequest(ctx, r.ID, "pr-7") }, func(r Run) Optional[string] { return r.PullRequest }, "pr-7"},
	} {
		if err := set.fn(); err != nil {
			t.Fatalf("setting the %s: %v", set.name, err)
		}
		got, err := s.Run(ctx, r.ID)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if value, known := set.read(got).Get(); !known || value != set.want {
			t.Fatalf("the %s came back as %q (known=%v), want %q", set.name, value, known, set.want)
		}
	}
}

func TestRunUpdatesRefuseAMissingRun(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.TransitionRun(ctx, "no-such-run", []RunStatus{RunPending}, RunPassed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TransitionRun on a missing run: %v", err)
	}
	if err := s.SetRunHead(ctx, "no-such-run", "aaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunHead on a missing run: %v", err)
	}
	if err := s.SetRunFixerSession(ctx, "no-such-run", "sess-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunFixerSession on a missing run: %v", err)
	}
}

func TestRunsForRepository(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)

	for _, id := range []string{"run-1", "run-2"} {
		r := completeRun()
		r.ID = id
		if _, err := s.CreateRun(ctx, r); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
	runs, err := s.RunsForRepository(ctx, "repo-1")
	if err != nil {
		t.Fatalf("RunsForRepository: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].ID != "run-2" {
		t.Fatalf("runs are not newest first: %s then %s", runs[0].ID, runs[1].ID)
	}
	empty, err := s.RunsForRepository(ctx, "no-such-repository")
	if err != nil {
		t.Fatalf("RunsForRepository on a missing repository: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("a missing repository reported %d runs", len(empty))
	}
}

func TestRunRequiresItsRepository(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateRun(context.Background(), completeRun()); err == nil {
		t.Fatal("CreateRun accepted a run whose repository does not exist")
	}
}

func TestStageResultKeepsUnknownApart(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	running, err := s.UpsertStageResult(ctx, StageResult{
		RunID: run.ID, Stage: "review", Status: StageRunning, LogPath: "/logs/run-1/review.log",
	})
	if err != nil {
		t.Fatalf("UpsertStageResult: %v", err)
	}
	if running.Duration.IsKnown() || running.LastActivity.IsKnown() || running.EffectiveFixLimit.IsKnown() {
		t.Fatalf("a running stage reported a duration, an activity, or a fix limit: %+v", running)
	}

	activity := time.Date(2026, 9, 2, 10, 30, 0, 123456789, time.UTC)
	done, err := s.UpsertStageResult(ctx, StageResult{
		RunID: run.ID, Stage: "review", Status: StagePassed, FindingCount: 3,
		Duration: Known(time.Duration(0)), LogPath: "/logs/run-1/review.log",
		LastActivity: Known(activity), EffectiveFixLimit: Known(0),
	})
	if err != nil {
		t.Fatalf("UpsertStageResult: %v", err)
	}
	// A stage that took no measurable time and a fix limit of zero are
	// genuine zeroes, and they must not read back the way an absent value does.
	if d, known := done.Duration.Get(); !known || d != 0 {
		t.Fatalf("a zero duration read back as %v (known=%v)", d, known)
	}
	if limit, known := done.EffectiveFixLimit.Get(); !known || limit != 0 {
		t.Fatalf("a zero fix limit read back as %v (known=%v)", limit, known)
	}
	if got, known := done.LastActivity.Get(); !known || !got.Equal(activity) {
		t.Fatalf("the last activity read back as %v (known=%v), want %v", got, known, activity)
	}

	all, err := s.StageResults(ctx, run.ID)
	if err != nil {
		t.Fatalf("StageResults: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("upserting the same stage twice made %d rows", len(all))
	}
	if _, err := s.StageResult(ctx, run.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StageResult on a missing stage: %v", err)
	}
}

func TestRoundsAreHistoryInAppendOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	for i := 1; i <= 3; i++ {
		if _, err := s.AppendRound(ctx, Round{
			RunID: run.ID, Stage: "review", Number: i,
			Findings: []byte(`[{"id":"f1"}]`), Selected: []byte(`["f1"]`),
			SelectedBy: "pipeline", FixerPayload: []byte("payload"), Summary: "round",
		}); err != nil {
			t.Fatalf("AppendRound: %v", err)
		}
	}
	rounds, err := s.Rounds(ctx, run.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if len(rounds) != 3 {
		t.Fatalf("got %d rounds, want 3", len(rounds))
	}
	for i, r := range rounds {
		if r.Number != i+1 {
			t.Fatalf("round %d is numbered %d", i, r.Number)
		}
		if i > 0 && r.ID <= rounds[i-1].ID {
			t.Fatalf("round identifiers do not increase: %d then %d", rounds[i-1].ID, r.ID)
		}
		if string(r.FixerPayload) != "payload" {
			t.Fatalf("the fixer payload came back as %q", r.FixerPayload)
		}
	}
	if _, err := s.AppendRound(ctx, Round{RunID: run.ID, Stage: "review", Number: 0}); err == nil {
		t.Fatal("AppendRound accepted a round numbered zero")
	}
}

// An empty payload is a fact somebody recorded. It must not read back the way
// an absent one would.
func TestRoundPayloadsAreNeverNull(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.AppendRound(ctx, Round{RunID: run.ID, Stage: "review", Number: 1}); err != nil {
		t.Fatalf("AppendRound: %v", err)
	}
	rounds, err := s.Rounds(ctx, run.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	if rounds[0].Findings == nil || rounds[0].Selected == nil || rounds[0].FixerPayload == nil {
		t.Fatalf("a round with no payloads read back with nil ones: %+v", rounds[0])
	}
	if len(rounds[0].Findings) != 0 {
		t.Fatalf("an empty findings payload read back as %q", rounds[0].Findings)
	}
	var isNull bool
	if err := s.read.QueryRowContext(ctx, `SELECT findings IS NULL FROM round WHERE id = ?`, rounds[0].ID).
		Scan(&isNull); err != nil {
		t.Fatalf("reading round.findings: %v", err)
	}
	if isNull {
		t.Fatal("an empty findings payload was stored as NULL, which in this package means unknown")
	}
}

func TestRunFixerSessionIsUnknownUntilItIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)

	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if r.FixerSession.IsKnown() {
		t.Fatalf("a new run reports a known fixer session: %q", r.FixerSession)
	}
	if err := s.SetRunFixerSession(ctx, r.ID, "sess-1"); err != nil {
		t.Fatalf("SetRunFixerSession: %v", err)
	}
	got, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if value, known := got.FixerSession.Get(); !known || value != "sess-1" {
		t.Fatalf("the fixer session came back as %q (known=%v), want sess-1", value, known)
	}
	// A later round reports a different reference and it replaces the first,
	// which is what makes the column answer where a restart would resume.
	if err := s.SetRunFixerSession(ctx, r.ID, "sess-2"); err != nil {
		t.Fatalf("SetRunFixerSession again: %v", err)
	}
	if got, err = s.Run(ctx, r.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if value, _ := got.FixerSession.Get(); value != "sess-2" {
		t.Fatalf("the fixer session came back as %q, want sess-2", value)
	}
	if err := s.SetRunFixerSession(ctx, r.ID, ""); err == nil {
		t.Fatal("SetRunFixerSession accepted an empty reference")
	}
}

// runFixerSessionVersion is the shipped migration the test below is named for.
// A shipped version never moves, so pinning to it holds the test's subject
// still as the list grows, which a slice taken relative to the end does not.
const runFixerSessionVersion = 4

// A run recorded before the column existed reads back unknown rather than as a
// session of the empty string, which is the promise Optional exists to keep.
func TestTheFixerSessionMigrationReachesAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schemaBefore(t, runFixerSessionVersion)); err != nil {
		t.Fatalf("migrate to the older schema: %v", err)
	}
	now := encodeTime(nowUTC())
	if _, err := older.ExecContext(ctx, `
		INSERT INTO repository (id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		"repo-1", "/checkouts/one", "https://example.test/one.git", "main", now, now); err != nil {
		t.Fatalf("write a repository against the older schema: %v", err)
	}
	if _, err := older.ExecContext(ctx, `
		INSERT INTO run (id, repository_id, branch, submitted_head, base, status,
			intent, intent_source, build_version, build_revision, build_modified,
			build_go, config_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-1", "repo-1", "topic", "aaaa", "bbbb", string(RunPending),
		"validate", "push", "v0.1.0", "0123456789abcdef", 0, "go1.25.0", "cfg-1",
		now, now); err != nil {
		t.Fatalf("write a run against the older schema: %v", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the older database: %v", err)
	}

	s := openStoreAt(t, path)
	got, err := s.Run(ctx, "run-1")
	if err != nil {
		t.Fatalf("the run written against the older schema did not survive: %v", err)
	}
	if got.FixerSession.IsKnown() {
		t.Fatalf("a run recorded before the column existed reports a fixer session: %q", got.FixerSession)
	}
	if err := s.SetRunFixerSession(ctx, "run-1", "sess-1"); err != nil {
		t.Fatalf("SetRunFixerSession after the migration: %v", err)
	}
}

// A transition is anchored to the status the caller expected, so a run that
// has moved since is refused rather than written over.
func TestTransitionRunHoldsTheRunToTheStatusTheCallerExpected(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	moved, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	if moved.Status != RunRunning {
		t.Fatalf("the run came back %s, want running", moved.Status)
	}

	// The same move again, from a status the run has left.
	_, err = s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunHeld)
	var refused *RunStatusError
	if !errors.As(err, &refused) {
		t.Fatalf("moving a running run out of pending: %v", err)
	}
	if refused.Actual != RunRunning || refused.To != RunHeld {
		t.Fatalf("the refusal reports %+v, want a run found running on the way to held", refused)
	}
	if !errors.Is(err, ErrRunStatus) {
		t.Fatalf("the refusal does not match ErrRunStatus: %v", err)
	}
	after, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after.Status != RunRunning || !after.UpdatedAt.Equal(moved.UpdatedAt) {
		t.Fatalf("the refused transition changed the run: %+v", after)
	}
}

// A run already in the status it is being moved to is left alone, so a
// transition retried after a failure that had already committed reports the
// state it established.
func TestTransitionRunToTheStatusTheRunAlreadyHoldsWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	first, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	// Nothing here names running as an origin, so only the destination rule
	// can be what lets this through.
	again, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatalf("running to running: %v", err)
	}
	if again.Status != RunRunning {
		t.Fatalf("the run came back %s, want running", again.Status)
	}
	if !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("the repeated transition moved updated_at from %s to %s", first.UpdatedAt, again.UpdatedAt)
	}
}

// The refusals that are about the request rather than about the run.
func TestTransitionRunRefusesARequestItCannotActOn(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunStatus("elsewhere")); err == nil {
		t.Fatal("TransitionRun accepted a status this package does not define")
	}
	if _, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, ""); err == nil {
		t.Fatal("TransitionRun accepted an empty status")
	}
	if _, err := s.TransitionRun(ctx, r.ID, nil, RunRunning); err == nil {
		t.Fatal("TransitionRun accepted a transition with no origin")
	}
	after, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after.Status != RunPending {
		t.Fatalf("a refused request moved the run to %s", after.Status)
	}
}

// Every status this package defines is recognized, and nothing else is.
func TestRunStatusesAreAClosedSet(t *testing.T) {
	for _, status := range RunStatuses() {
		if !status.Recognized() {
			t.Fatalf("%q is in the set and not recognized", status)
		}
	}
	for _, status := range []RunStatus{"", "running ", "Running", "cancelled"} {
		if status.Recognized() {
			t.Fatalf("%q is recognized and is not one of the statuses", status)
		}
	}
	listed := RunStatuses()
	listed[0] = "rewritten"
	if RunStatuses()[0] == "rewritten" {
		t.Fatal("writing to the returned slice changed the set")
	}
}
