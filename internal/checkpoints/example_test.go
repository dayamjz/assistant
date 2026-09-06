package checkpoints_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A run stops at a decision, the process that was driving it goes away, and
// what comes back is where the run stood rather than only the fact that it
// existed. The second executor is given nothing but the database.
func Example() {
	ctx := context.Background()

	home, err := os.MkdirTemp("", "assistant-checkpoints-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(home) }()
	path := filepath.Join(home, "state.db")

	held, err := driveToTheDecision(ctx, path)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("before the restart:", held.Status, "at", held.Position)
	fmt.Println("asking:", held.Decision.Question)

	// Everything the first process held is gone. Only the file is left.
	stood, done, err := answerAfterTheRestart(ctx, path)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("after the restart:", stood.Status, "at", stood.Position, "on checkpoint", stood.ID())
	fmt.Println("resumed to:", done.Status, "having spent", done.Steps, "steps")

	// Output:
	// before the restart: halted at ship
	// asking: ship it?
	// after the restart: halted at ship on checkpoint release#2
	// resumed to: completed having spent 3 steps
}

// driveToTheDecision starts the run and leaves it standing at its halt point.
func driveToTheDecision(ctx context.Context, path string) (graph.Result, error) {
	s, err := openExampleStore(ctx, path)
	if err != nil {
		return graph.Result{}, err
	}
	defer func() { _ = s.Close() }()

	g, err := exampleGraph()
	if err != nil {
		return graph.Result{}, err
	}
	exec, err := graph.NewExecutor(g, graph.Config{Store: checkpoints.New(s), Budget: 10})
	if err != nil {
		return graph.Result{}, err
	}
	initial, err := g.NewState(nil)
	if err != nil {
		return graph.Result{}, err
	}
	return exec.Run(ctx, "release", initial)
}

// answerAfterTheRestart opens the same database again and takes the run the
// rest of the way, without being told anything about where it was.
func answerAfterTheRestart(ctx context.Context, path string) (graph.Checkpoint, graph.Result, error) {
	s, err := openExampleStore(ctx, path)
	if err != nil {
		return graph.Checkpoint{}, graph.Result{}, err
	}
	defer func() { _ = s.Close() }()

	g, err := exampleGraph()
	if err != nil {
		return graph.Checkpoint{}, graph.Result{}, err
	}
	durable := checkpoints.New(s)
	stood, err := durable.Latest(ctx, "release")
	if err != nil {
		return graph.Checkpoint{}, graph.Result{}, err
	}
	exec, err := graph.NewExecutor(g, graph.Config{Store: durable, Budget: 10})
	if err != nil {
		return graph.Checkpoint{}, graph.Result{}, err
	}
	done, err := exec.Answer(ctx, "release", "approve")
	return stood, done, err
}

// openExampleStore opens the database the checkpoints are kept in. A real
// caller passes the redact module PRD section 8 names; this one covers the same
// shape internal/vcs does.
func openExampleStore(ctx context.Context, path string) (*store.Store, error) {
	userinfo := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)
	redactor := vcs.RedactorFunc(func(s string) string {
		return userinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
	return store.Open(ctx, path, store.WithRedactor(redactor))
}

// exampleGraph builds a run that prepares something, stops to ask whether to
// ship it, and then does.
func exampleGraph() (*graph.Graph, error) {
	nothing := graph.Stateless(func(context.Context, graph.Reader, graph.Writer) error { return nil })
	return graph.NewBuilder().
		Start("prepare").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{Name: "prepare", NewBody: nothing}).
		Node(graph.Node{
			Name:    "ship",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "ship it?", Options: []string{"approve", "cancel"}, Into: "answer"},
			NewBody: nothing,
		}).
		Node(graph.Node{Name: "report", NewBody: nothing}).
		Edge(graph.Edge{From: "prepare", To: "ship"}).
		Edge(graph.Edge{From: "ship", To: "report"}).
		Build()
}
