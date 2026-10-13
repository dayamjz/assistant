package runs_test

import (
	"context"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
)

// OnMove hears of every committed change and of nothing else: a refused move
// changed nothing, so it is not announced.
func TestOnMoveHearsOfCommittedChangesOnly(t *testing.T) {
	ctx := t.Context()
	s, _ := openStore(t)
	var heard []string
	svc, err := runs.New(runs.Options{
		Store:  s,
		Agent:  resolution(standin.New(t, standin.Script{})),
		OnMove: func(_ context.Context, id string) { heard = append(heard, id) },
	})
	if err != nil {
		t.Fatalf("runs.New: %v", err)
	}
	run := seedRun(t, s, svc, "run-1")
	if _, err := svc.Start(ctx, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Pass(ctx, run.ID); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if _, err := svc.Hold(ctx, run.ID); err == nil {
		t.Fatal("Hold of a passed run was accepted")
	}
	if want := []string{"run-1", "run-1", "run-1"}; !slices.Equal(heard, want) {
		t.Fatalf("OnMove heard %v, want %v: the create, the start and the pass only", heard, want)
	}
}
