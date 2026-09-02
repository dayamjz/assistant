package graph_test

import (
	"context"
	"fmt"
	"log"

	"github.com/dayamjz/assistant/internal/graph"
)

// Example builds the shape this package exists for: a stage that reports
// findings, a decision the run stops before, a fixer, and a bounded back edge
// that sends the run around again.
func Example() {
	g, err := graph.NewBuilder().
		Start("review").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Key(graph.Key{Name: "fixes", Kind: graph.KindInt, Merge: graph.MergeSum}).
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		// What the second pass sees is decided by declared state, never by a
		// Go variable a body closes over: the executor constructs a fresh body
		// for every Run, Resume, and Answer call, so a counter kept in the
		// closure would be shared across concurrent runs and lost across this
		// graph's halt point.
		Node(graph.Node{
			Name:   "review",
			Reads:  []string{"fixes"},
			Writes: []string{"findings"},
			NewBody: graph.Stateless(func(_ context.Context, r graph.Reader, w graph.Writer) error {
				fixes, err := r.Get("fixes")
				if err != nil {
					return err
				}
				if applied, _ := fixes.Int(); applied > 0 {
					return w.Set("findings", graph.IntValue(0))
				}
				return w.Set("findings", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{
			Name:  "gate",
			Reads: []string{"answer"},
			Halt:  &graph.Halt{Question: "apply the fix?", Options: []string{"fix", "stop"}, Into: "answer"},
			NewBody: graph.Stateless(func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
				return nil
			}),
		}).
		Node(graph.Node{
			Name:   "fix",
			Writes: []string{"fixes"},
			NewBody: graph.Stateless(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				return w.Set("fixes", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{
			Name: "done",
			NewBody: graph.Stateless(func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
				return nil
			}),
		}).
		Edge(graph.Edge{From: "review", To: "gate", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "review", To: "done"}).
		Edge(graph.Edge{From: "gate", To: "fix", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("fix"),
		}}).
		Edge(graph.Edge{From: "gate", To: "done"}).
		// The one cycle in the graph, and the bound it must declare.
		Edge(graph.Edge{From: "fix", To: "review", Rounds: 2}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	store := graph.NewMemoryStore()
	exec, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: 20})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	initial, err := g.NewState(nil)
	if err != nil {
		log.Fatal(err)
	}
	held, err := exec.Run(ctx, "example", initial)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s before %q: %s %v\n",
		held.Status, held.Position, held.Decision.Question, held.Decision.Options)

	// The decision is collected outside the graph and supplied on the next
	// call. The halted node has not started, so nothing is re-executed.
	done, err := exec.Answer(ctx, "example", "fix")
	if err != nil {
		log.Fatal(err)
	}
	fixes, _ := done.State.Get("fixes")
	fmt.Printf("%s after %d steps, %s applied\n", done.Status, done.Steps, fixes)

	history, err := store.History(ctx, "example")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d checkpoints written\n", len(history))

	// Output:
	// halted before "gate": apply the fix? [fix stop]
	// completed after 5 steps, 1 applied
	// 7 checkpoints written
}
