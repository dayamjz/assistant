package pipeline

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/graph"
)

// Round is where one stage stands in its fix loop, read off a run's bound
// accounting rather than kept anywhere of its own: internal/graph's counters
// are the one record of how often each edge was taken, and this is what they
// say about the edges this package wired for one stage.
type Round struct {
	// Taken is how many fix rounds the stage has entered so far, automatic
	// and requested together. While the stage's own body runs it is the
	// number of rounds before this execution, so zero is the first look; while
	// a round runs it counts that round.
	Taken int
	// Limit is the automatic round limit the run is held to at this stage,
	// read off the edge into the stage's fixer that carries it, and zero for
	// a stage that takes no automatic round in this run. A requested round is
	// bounded by RequestedFixRounds and not by this.
	Limit int
}

// RoundOf reads a stage's Round off counters, which must have been kept
// against g, the graph this package built for the run they belong to.
// Counters g would refuse to resume are refused here too, by
// graph.Graph.Entered, so a round is never read against edges that did not
// produce it.
func RoundOf(g *graph.Graph, counters graph.Counters, stage Stage) (Round, error) {
	if !stage.Valid() {
		return Round{}, fmt.Errorf("pipeline: %d names no stage", stage)
	}
	automatic, err := g.Entered(counters, stage.FixNode())
	if err != nil {
		return Round{}, err
	}
	requested, err := g.Entered(counters, stage.RequestNode())
	if err != nil {
		return Round{}, err
	}
	round := Round{Taken: automatic + requested}
	for _, e := range g.Edges() {
		if e.From == stage.Node() && e.To == stage.FixNode() {
			round.Limit = e.Rounds
		}
	}
	return round, nil
}
