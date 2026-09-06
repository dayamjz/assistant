package runs_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// One background service drives concurrent runs, so a run's fixer role is
// asked for and used from more than one goroutine. Concurrent asks answer with
// one object, and concurrent rounds enter one conversation one at a time:
// exactly one of them opens it and every other resumes it.
func TestConcurrentRoundsShareOneConversation(t *testing.T) {
	const workers = 4
	ctx := t.Context()
	ag := standin.New(t, standin.Script{Steps: []standin.Step{
		{Times: standin.Always, Reply: answering("applied", "sess-1")},
	}})
	s, _ := openStore(t)
	svc := service(t, s, resolution(ag), true)
	run := seedRun(t, s, svc, "run-1")

	fixers := make([]*runs.Fixer, workers)
	errs := make([]error, workers)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(workers)
	for i := range workers {
		go func() {
			defer done.Done()
			start.Wait()
			fixer, err := svc.Fixer(ctx, run.ID)
			if err != nil {
				errs[i] = err
				return
			}
			fixers[i] = fixer
			_, errs[i] = fixer.Apply(ctx, fixInvocation(t, "concurrent round"))
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if fixers[i] != fixers[0] {
			t.Fatalf("worker %d was handed a different fixer role", i)
		}
	}

	calls := ag.Calls()
	if len(calls) != workers {
		t.Fatalf("the agent saw %d invocations, want %d", len(calls), workers)
	}
	opened := 0
	for _, call := range calls {
		switch call.Session() {
		case "":
			opened++
		case "sess-1":
		default:
			t.Errorf("a round asked to resume %q, want sess-1", call.Session())
		}
	}
	if opened != 1 {
		t.Errorf("%d rounds opened a conversation, want exactly 1", opened)
	}
	if reference, known := sessionOf(t, s, run.ID); !known || reference != "sess-1" {
		t.Errorf("the run records %q (known=%v), want sess-1", reference, known)
	}
}

// Two verdicts racing for one run end with one verdict. The move is anchored,
// so the one that arrives second is refused rather than writing over the
// first, and every caller that was told its move succeeded was told the same
// thing.
func TestRacingVerdictsSettleOnOne(t *testing.T) {
	const workers = 4
	ctx := t.Context()
	s, _ := openStore(t)
	svc := service(t, s, resolution(standin.New(t, standin.Script{})), false)
	run := seedRun(t, s, svc, "run-1")
	if _, err := svc.Start(ctx, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	settled := make([]store.RunStatus, workers)
	refused := make([]error, workers)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(workers)
	for i := range workers {
		verdict := svc.Pass
		if i%2 == 1 {
			verdict = svc.Fail
		}
		go func() {
			defer done.Done()
			start.Wait()
			after, err := verdict(ctx, run.ID)
			if err != nil {
				refused[i] = err
				return
			}
			settled[i] = after.Status
		}()
	}
	start.Done()
	done.Wait()

	final, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final.Status != store.RunPassed && final.Status != store.RunFailed {
		t.Fatalf("the run ended %s, want one of the two verdicts", final.Status)
	}
	succeeded := 0
	for i := range workers {
		switch {
		case refused[i] != nil:
			if !errors.Is(refused[i], store.ErrRunStatus) {
				t.Errorf("worker %d was refused with %v, want ErrRunStatus", i, refused[i])
			}
		case settled[i] != final.Status:
			t.Errorf("worker %d was told the run was %s, and it is %s", i, settled[i], final.Status)
		default:
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Error("no caller was told its verdict was recorded, and the run carries one")
	}
}
