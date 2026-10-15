package graph_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/principles"
)

// lateLoop builds the fix loop a run checkpoints under before and after a key
// is added to it. With added nil the graph is the older build, which has no
// such key. With added set, the fix node reads the key into seen and, when
// rename is set, writes a new value to it every round.
//
// check fails on the execution numbered failOn, counting from one, so a test
// can end a segment at a point of its choosing the way an interrupted process
// would: a node error writes no checkpoint, so the run's latest is the one
// before it.
type lateLoop struct {
	added  *graph.Key
	rename bool
	failOn int
	checks int
	seen   []string
}

func (l *lateLoop) build(t *testing.T) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Node(graph.Node{
			Name:   "check",
			Writes: []string{"findings"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				l.checks++
				if l.checks == l.failOn {
					return errors.New("the segment ends here")
				}
				return w.Set("findings", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{Name: "done", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error { return nil })}).
		Edge(graph.Edge{From: "check", To: "fix", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "check", To: "done"}).
		Edge(graph.Edge{From: "fix", To: "check", Rounds: 5})
	fix := graph.Node{Name: "fix", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error { return nil })}
	if l.added != nil {
		name := l.added.Name
		b = b.Key(*l.added)
		fix.Reads = []string{name}
		if l.rename {
			fix.Writes = []string{name}
		}
		fix.NewBody = body(func(_ context.Context, r graph.Reader, w graph.Writer) error {
			v, err := r.Get(name)
			if err != nil {
				return err
			}
			got, _ := v.Text()
			l.seen = append(l.seen, got)
			if l.rename {
				return w.Set(name, graph.TextValue(fmt.Sprintf("round %d", len(l.seen))))
			}
			return nil
		})
	}
	return mustBuild(t, b.Node(fix))
}

// olderCheckpoint runs the older build of the loop until its second check
// fails, which leaves a latest checkpoint standing at check with the back
// edge traversed once and its fingerprint recorded over a state that has no
// added key.
func olderCheckpoint(t *testing.T) (graph.CheckpointStore, graph.Checkpoint) {
	t.Helper()
	older := &lateLoop{failOn: 2}
	g := older.build(t)
	store := graph.NewMemoryStore()
	if _, err := mustExecutor(t, g, store, 50).Run(context.Background(), "run", mustState(t, g, nil)); err == nil {
		t.Fatal("the older build's run did not stop where the test ended its segment")
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if cp.Position != "check" || traversals(t, g, store, "run")["fix->check"] != 1 {
		t.Fatalf("the older run stands at %q with traversals %v, want check after one round",
			cp.Position, traversals(t, g, store, "run"))
	}
	if _, held := cp.State.Get("subject"); held {
		t.Fatal("the older run's checkpoint already holds the key the newer build adds")
	}
	return store, cp
}

// TestALateKeyLetsACheckpointFromBeforeItResumeAtItsZeroValue resumes a run
// that checkpointed under a graph without a key, under the same graph with
// that key declared late. The resume is accepted, the body reads the key's
// zero value, the checkpoints the resumed run writes hold it, and the
// convergence bound still fires on the first round after the resume, because
// a late key at its zero value is outside the fingerprint the older build
// recorded.
//
// Two controls keep it from passing by looking at nothing: the same key
// declared without Late is refused on the same checkpoint, as every added key
// was before, and a late key whose value changes every round keeps the loop
// from converging, so the key is in the fingerprint whenever it holds
// anything (P6: a run a build checkpointed keeps a path forward under the next
// build that adds a key, with its bounds intact).
func TestALateKeyLetsACheckpointFromBeforeItResumeAtItsZeroValue(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	store, older := olderCheckpoint(t)

	strict := (&lateLoop{added: &graph.Key{Name: "subject", Kind: graph.KindText}}).build(t)
	err := strict.Validate(older)
	var mismatch *graph.CheckpointError
	if !errors.As(err, &mismatch) || mismatch.Field != "state" || !strings.Contains(err.Error(), `"subject"`) {
		t.Fatalf("a key added without Late: Validate = %v, want a state mismatch naming the key", err)
	}
	if _, err := mustExecutor(t, strict, store, 50).Resume(ctx, "run"); !errors.As(err, &mismatch) {
		t.Fatalf("a key added without Late: Resume = %v, want the checkpoint refused", err)
	}

	newer := &lateLoop{added: &graph.Key{Name: "subject", Kind: graph.KindText, Merge: graph.MergeLastWriteWins, Late: true}}
	g := newer.build(t)
	if err := g.Validate(older); err != nil {
		t.Fatalf("a late key: Validate refused the older checkpoint: %v", err)
	}
	got, err := mustExecutor(t, g, store, 50).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("a late key: Resume: %v", err)
	}
	if got.Status != graph.StatusConverged {
		t.Fatalf("Status = %s (%s), want converged on the first round after the resume", got.Status, got.Reason)
	}
	if len(newer.seen) != 1 || newer.seen[0] != "" {
		t.Fatalf("the fix body read %q, want the late key's zero value once", newer.seen)
	}
	latest, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if text(t, latest.State, "subject") != "" {
		t.Fatalf("the resumed run's checkpoint holds %q under the late key, want its zero value", text(t, latest.State, "subject"))
	}
	if err := g.Validate(latest); err != nil {
		t.Fatalf("the checkpoint the resumed run wrote does not validate: %v", err)
	}

	renaming := &lateLoop{
		added:  &graph.Key{Name: "subject", Kind: graph.KindText, Merge: graph.MergeLastWriteWins, Late: true},
		rename: true,
	}
	rg := renaming.build(t)
	fresh := graph.NewMemoryStore()
	parked, err := mustExecutor(t, rg, fresh, 50).Run(ctx, "fresh", mustState(t, rg, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusRoundsExhausted {
		t.Fatalf("a late key changed every round: Status = %s (%s), want rounds_exhausted rather than converged",
			parked.Status, parked.Reason)
	}
}

// TestARunMayStartFromAStateWithoutALateKey starts a run from a state that
// lacks a late key, which is what a state carried out of an older run's
// result looks like, and finds the key filled in at its zero value. The
// control is the same state against a key that is not late, which is refused
// as it always was.
func TestARunMayStartFromAStateWithoutALateKey(t *testing.T) {
	ctx := context.Background()
	_, older := olderCheckpoint(t)

	// check fails on its first execution, so the run's only checkpoint is the
	// one Run writes from the state it was handed.
	store := graph.NewMemoryStore()
	g := (&lateLoop{added: &graph.Key{Name: "subject", Kind: graph.KindText, Late: true}, failOn: 1}).build(t)
	if _, err := mustExecutor(t, g, store, 50).Run(ctx, "late", older.State); err == nil {
		t.Fatal("the run did not stop where the test ended its segment")
	}
	first, err := store.Latest(ctx, "late")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if text(t, first.State, "subject") != "" {
		t.Fatalf("the run's first checkpoint holds %q under the late key, want its zero value", text(t, first.State, "subject"))
	}

	strict := (&lateLoop{added: &graph.Key{Name: "subject", Kind: graph.KindText}}).build(t)
	if _, err := mustExecutor(t, strict, graph.NewMemoryStore(), 50).Run(ctx, "strict", older.State); !errors.Is(err, graph.ErrStateMismatch) {
		t.Fatalf("a key that is not late: Run = %v, want ErrStateMismatch", err)
	}
}
