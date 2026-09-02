package store

import (
	"context"
	"errors"
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
	if err := s.SetRunStatus(ctx, "no-such-run", RunPassed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunStatus on a missing run: %v", err)
	}
	if err := s.SetRunHead(ctx, "no-such-run", "aaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunHead on a missing run: %v", err)
	}
	if err := s.SetRunStatus(ctx, "no-such-run", ""); err == nil {
		t.Fatal("SetRunStatus accepted an empty status")
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

func TestCheckpointRevisionIncreases(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.Checkpoint(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Checkpoint before any was written: %v", err)
	}
	first, err := s.WriteCheckpoint(ctx, Checkpoint{RunID: run.ID, State: []byte("s1"), Position: "review"})
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if first.Revision != 1 {
		t.Fatalf("the first checkpoint has revision %d, want 1", first.Revision)
	}
	if first.OpenDecision.IsKnown() {
		t.Fatal("a checkpoint with no open decision reported one")
	}
	second, err := s.WriteCheckpoint(ctx, Checkpoint{
		RunID: run.ID, State: []byte("s2"), Position: "fix", OpenDecision: Known("hold-1"),
	})
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("the second checkpoint has revision %d, want 2", second.Revision)
	}
	got, err := s.Checkpoint(ctx, run.ID)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if got.Position != "fix" || string(got.State) != "s2" || got.Revision != 2 {
		t.Fatalf("the checkpoint did not replace the previous one: %+v", got)
	}
	if decision, known := got.OpenDecision.Get(); !known || decision != "hold-1" {
		t.Fatalf("the open decision came back as %q (known=%v)", decision, known)
	}
}

// A checkpoint recorded with no state reads back the way it was written. In
// this package a nil slice is unknown, and state is a column that is never
// NULL, so an empty state must not come back as one.
func TestCheckpointWithNoStateReadsBackEmptyNotNil(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	written, err := s.WriteCheckpoint(ctx, Checkpoint{RunID: run.ID, Position: "review"})
	if err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	if written.State == nil {
		t.Fatal("WriteCheckpoint returned a nil state for a column that is never NULL")
	}
	got, err := s.Checkpoint(ctx, run.ID)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if got.State == nil {
		t.Fatal("a checkpoint written with an empty state read back as nil, which in this package means unknown")
	}
	if len(got.State) != 0 {
		t.Fatalf("the state read back as %q", got.State)
	}
}
