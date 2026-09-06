package runs_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// internal/pipeline builds a new fix body for every advance segment, so the
// conversation a run's fix rounds share has to survive one. It survives
// because the fixer role is one object per run: the second segment asks for it
// and is handed the first segment's, so the round it runs continues the
// conversation rather than opening a second one.
func TestTheFixerSessionCrossesASegmentBoundary(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: answering("opened", "sess-1")},
		{Reply: answering("continued", "sess-1")},
	}})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")

	first, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	if _, err := first.Apply(ctx, fixInvocation(t, "first round")); err != nil {
		t.Fatalf("first round: %v", err)
	}

	// What a new advance segment does: ask for the run's fixer again.
	second, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer for the second segment: %v", err)
	}
	if second != first {
		t.Fatal("the second segment was handed a different fixer, so the run has two conversations")
	}
	if _, err := second.Apply(ctx, fixInvocation(t, "second round")); err != nil {
		t.Fatalf("second round: %v", err)
	}

	calls := ag.Calls()
	if len(calls) != 2 {
		t.Fatalf("the agent saw %d invocations, want 2: %v", len(calls), calls)
	}
	if got := calls[0].Session(); got != "" {
		t.Errorf("the round that opened the session asked to resume %q", got)
	}
	if got := calls[1].Session(); got != "sess-1" {
		t.Errorf("the round after the segment boundary asked to resume %q, want sess-1", got)
	}
}

// A restart loses the Go value holding the conversation, so what is left is
// the run's record. This is that restart: the database is closed and reopened,
// and a service built over it continues the conversation the first one opened.
func TestTheFixerSessionCrossesAProcessRestart(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: answering("opened", "sess-1")},
		{Reply: answering("continued", "sess-1")},
	}})
	before, path := openStore(t)
	svc := service(t, before, resolution(ag), true)
	run := seedRun(t, before, svc, "run-1")

	fixer, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	if _, err := fixer.Apply(ctx, fixInvocation(t, "first round")); err != nil {
		t.Fatalf("first round: %v", err)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	after := openStoreAt(t, path)
	restarted := service(t, after, resolution(ag), true)
	revived, err := restarted.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer after the restart: %v", err)
	}
	if got := revived.Reference(); got != "sess-1" {
		t.Fatalf("the restarted fixer would resume %q, want sess-1", got)
	}
	if _, err := revived.Apply(ctx, fixInvocation(t, "second round")); err != nil {
		t.Fatalf("round after the restart: %v", err)
	}

	calls := ag.Calls()
	if len(calls) != 2 {
		t.Fatalf("the agent saw %d invocations, want 2: %v", len(calls), calls)
	}
	if got := calls[1].Session(); got != "sess-1" {
		t.Errorf("the round after the restart asked to resume %q, want sess-1", got)
	}
}

// A caller holding a round's result holds one whose conversation the run
// already knows about. There is no window in which the two disagree, so this
// asserts the record the moment Apply has returned.
func TestARoundsSessionIsRecordedBeforeItsResultIsReturned(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: answering("opened", "sess-1")},
		{Reply: answering("moved", "sess-2")},
	}})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")
	fixer, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}

	if reference, known := sessionOf(t, s, run.ID); known {
		t.Fatalf("the run carries a session %q before any round has run", reference)
	}
	for _, want := range []string{"sess-1", "sess-2"} {
		if _, err := fixer.Apply(ctx, fixInvocation(t, "round for "+want)); err != nil {
			t.Fatalf("round for %s: %v", want, err)
		}
		reference, known := sessionOf(t, s, run.ID)
		if !known || reference != want {
			t.Fatalf("the run records %q (known=%v) once the round returned, want %s", reference, known, want)
		}
		if got := fixer.Reference(); got != want {
			t.Fatalf("the fixer reports %q, want %s", got, want)
		}
	}
}

// A round whose reference cannot be recorded fails rather than handing back a
// result the run's own history cannot account for. The round itself already
// happened, which is why the failure names both halves.
func TestARoundWhoseSessionCannotBeRecordedFails(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: answering("applied", "sess-1")},
	}})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")
	fixer, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	result, err := fixer.Apply(ctx, fixInvocation(t, "the round that cannot be recorded"))
	if !errors.Is(err, store.ErrClosed) {
		t.Fatalf("Apply answered %v, want a refusal naming the store", err)
	}
	if result.Text != "" || result.Record.Agent != "" {
		t.Errorf("Apply handed back a result alongside its refusal: %+v", result)
	}
	if calls := ag.Calls(); len(calls) != 1 {
		t.Errorf("the agent saw %d invocations, want 1: the round did run and its work stands", len(calls))
	}
	if got := fixer.Reference(); got != "" {
		t.Errorf("the fixer moved onto %q, which the run does not record", got)
	}
}

// Session reuse off is a run with no memory across rounds, which is what PRD
// section 8 leaves an adapter that cannot keep a session. It is not a session
// under another name: nothing is resumed and nothing is recorded.
func TestWithoutSessionReuseEveryRoundIsSessionFree(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: answering("first", "sess-1")},
		{Reply: answering("second", "sess-2")},
	}})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), false)
	run := seedRun(t, s, svc, "run-1")
	fixer, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}

	for _, round := range []string{"first round", "second round"} {
		if _, err := fixer.Apply(ctx, fixInvocation(t, round)); err != nil {
			t.Fatalf("%s: %v", round, err)
		}
	}
	for _, call := range ag.Calls() {
		if got := call.Session(); got != "" {
			t.Errorf("a session-free round asked to resume %q", got)
		}
	}
	if reference, known := sessionOf(t, s, run.ID); known {
		t.Errorf("a run with no session reuse records the session %q", reference)
	}
	if got := fixer.Reference(); got != "" {
		t.Errorf("a session-free fixer reports the reference %q", got)
	}
}

// P4 keeps a review out of the memory a fix round holds, and the rule is about
// the role rather than about whether the round happens to keep memory. So it
// holds in both modes, and in both it is refused before any process starts.
func TestAReviewShapeIsRefusedAtTheFixerInEitherMode(t *testing.T) {
	principles.Cite(t, principles.P4)
	for _, reuse := range []bool{true, false} {
		t.Run(map[bool]string{true: "with session reuse", false: "without session reuse"}[reuse], func(t *testing.T) {
			ctx := t.Context()
			ag := standin.New(t, standin.Script{Steps: []standin.Step{
				{Times: standin.Always, Reply: standin.Text("the fixer must not answer this")},
			}})
			s, _ := openStore(t)
			svc := service(t, s, resolution(ag), reuse)
			run := seedRun(t, s, svc, "run-1")
			fixer, err := svc.Fixer(ctx, run.ID)
			if err != nil {
				t.Fatalf("Fixer: %v", err)
			}

			// Valid in every other way, so the shape is the only thing wrong.
			review := fixInvocation(t, "review this change")
			review.Shape = agents.ShapeReview
			review.Review = findings.Demand{Revision: "aaaa", Touched: []string{"main.go"}}
			if err := review.Validate(); err != nil {
				t.Fatalf("the invocation is unrunnable for a second reason: %v", err)
			}

			_, err = fixer.Apply(ctx, review)
			if !errors.Is(err, agents.ErrReviewInFixerSession) {
				t.Fatalf("Apply answered %v, want ErrReviewInFixerSession", err)
			}
			if calls := ag.Calls(); len(calls) != 0 {
				t.Errorf("the agent saw %d invocations, want none: the refusal is before anything starts", len(calls))
			}
		})
	}
}

// One run has one fixer role, so two fix nodes of one run cannot open two
// conversations about one change, and two runs never share one.
func TestOneFixerPerRun(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	one := seedRun(t, s, svc, "run-1")
	two := seedRun(t, s, svc, "run-2")

	first, err := svc.Fixer(ctx, one.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	again, err := svc.Fixer(ctx, one.ID)
	if err != nil {
		t.Fatalf("Fixer again: %v", err)
	}
	other, err := svc.Fixer(ctx, two.ID)
	if err != nil {
		t.Fatalf("Fixer for the second run: %v", err)
	}
	if again != first {
		t.Error("two asks for one run's fixer answered with two objects")
	}
	if other == first {
		t.Error("two runs were handed one fixer, so they share the agent's memory of each other's changes")
	}
}

// A run nothing can take further has no next fix round to answer, so the fixer
// role is refused for it rather than handed out.
func TestFixerRefusesARunThatHasEnded(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)

	for _, end := range []struct {
		name string
		to   func(id string) error
	}{
		{"terminated", func(id string) error { _, err := svc.Terminate(ctx, id); return err }},
		{"passed", func(id string) error {
			if _, err := svc.Start(ctx, id); err != nil {
				return err
			}
			_, err := svc.Pass(ctx, id)
			return err
		}},
	} {
		t.Run(end.name, func(t *testing.T) {
			run := seedRun(t, s, svc, "run-"+end.name)
			if _, err := svc.Fixer(ctx, run.ID); err != nil {
				t.Fatalf("Fixer while the run was still going: %v", err)
			}
			if err := end.to(run.ID); err != nil {
				t.Fatalf("ending the run: %v", err)
			}
			if _, err := svc.Fixer(ctx, run.ID); !errors.Is(err, runs.ErrRunEnded) {
				t.Fatalf("Fixer on an ended run answered %v, want ErrRunEnded", err)
			}
		})
	}
}

// A run that does not exist has no fixer role either, and the refusal is the
// store's so a caller handles one answer to "no such run".
func TestFixerRefusesARunThatDoesNotExist(t *testing.T) {
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	if _, err := svc.Fixer(t.Context(), "no-such-run"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Fixer on a missing run answered %v, want ErrNotFound", err)
	}
}

// What decides whether a run still has a fixer role is the run's own record,
// not this service's memory of having handed one out. A run ended by something
// that did not go through the service is refused too.
func TestFixerReadsTheRunsRecordAndNotItsOwnIndex(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")

	if _, err := svc.Fixer(ctx, run.ID); err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	// Ending the run without the service, which is the bypass its documented
	// ownership leaves open.
	if _, err := s.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := svc.Fixer(ctx, run.ID); !errors.Is(err, runs.ErrRunEnded) {
		t.Fatalf("Fixer answered %v, want ErrRunEnded", err)
	}
}

// A run whose recorded status is a word this build does not define is a run
// nothing here can move, so it is refused rather than served on the strength
// of not being understood.
//
// It is recorded through the store, because the service's own Create refuses
// anything but a pending run; reaching around it is what makes this a run the
// service did not produce, which is the only way such a status arrives.
func TestFixerRefusesARunInAStatusThisBuildDoesNotDefine(t *testing.T) {
	ctx := t.Context()
	ag := standin.New(t, standin.Script{})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	if _, err := s.CreateRun(ctx, store.Run{
		ID: "run-1", RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa", Base: "bbbb", Status: store.RunStatus("elsewhere"),
		Intent: "validate", IntentSource: "push",
		Build:        store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		ConfigDigest: "cfg-1",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if _, err := svc.Fixer(ctx, "run-1"); !errors.Is(err, runs.ErrRunEnded) {
		t.Fatalf("Fixer on a run in an unrecognized status answered %v, want ErrRunEnded", err)
	}
}
