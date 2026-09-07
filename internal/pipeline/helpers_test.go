package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// calls records how many times each stage body and each stage's fixer ran. It
// is what a test asks "did this body run at all", which no state key answers:
// a body that did not run leaves the same state as one that ran and wrote
// nothing.
// It also records how many times each of those bodies was constructed, which
// is a different question: a body that ran twice was either built twice or
// built once and reused, and only the constructor count tells the two apart.
type calls struct {
	mu        sync.Mutex
	stages    map[Stage]int
	fixes     map[Stage]int
	stageNews map[Stage]int
	fixNews   int
}

func newCalls() *calls {
	return &calls{
		stages:    make(map[Stage]int),
		fixes:     make(map[Stage]int),
		stageNews: make(map[Stage]int),
	}
}

// newStage records one construction of the body of the stage this
// implementation was placed in. A constructor is handed no stage, so the
// caller names the field it filled.
func (c *calls) newStage(s Stage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stageNews[s]++
}

// newFix records one construction of a fix body. A pipeline has one Fixer
// serving every fix node, and its constructor is told no more than a stage
// body's is, so this is a total across the run rather than a per-stage count.
func (c *calls) newFix() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fixNews++
}

func (c *calls) stageNewCount(s Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stageNews[s]
}

func (c *calls) fixNewCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fixNews
}

// stage records one execution of a stage body and returns how many there have
// now been, counting from one.
func (c *calls) stage(s Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stages[s]++
	return c.stages[s]
}

// fix records one execution of a stage's fixer.
func (c *calls) fix(s Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fixes[s]++
	return c.fixes[s]
}

func (c *calls) stageCount(s Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stages[s]
}

func (c *calls) fixCount(s Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fixes[s]
}

// recording returns an implementation that counts its executions and returns
// whatever run says. A nil run reports nothing and writes nothing.
func recording(c *calls, reads, writes []Key, run func(in Input, call int) (Output, error)) Implementation {
	return Implementation{
		Reads:  reads,
		Writes: writes,
		NewBody: func() Body {
			return func(ctx context.Context, in Input) (Output, error) {
				call := c.stage(in.Stage)
				if run == nil {
					return Output{Report: passing()}, nil
				}
				return run(in, call)
			}
		},
	}
}

// recordingStages returns the nine stages, each counting its executions and
// each reporting nothing.
func recordingStages(c *calls) Stages {
	var s Stages
	for _, row := range stageTable {
		*row.implementation(&s) = recording(c, nil, nil, nil)
	}
	return s
}

// set replaces one stage's implementation.
func set(s *Stages, stage Stage, impl Implementation) {
	row, ok := stage.spec()
	if !ok {
		panic("no such stage")
	}
	*row.implementation(s) = impl
}

// recordingFixer returns a fixer that counts its executions per stage.
func recordingFixer(c *calls, reads, writes []Key, fix func(in FixInput, call int) (FixOutput, error)) Fixer {
	return Fixer{
		Reads:  reads,
		Writes: writes,
		NewBody: func() FixBody {
			return func(ctx context.Context, in FixInput) (FixOutput, error) {
				call := c.fix(in.Stage)
				if fix == nil {
					return FixOutput{}, nil
				}
				return fix(in, call)
			}
		},
	}
}

// constructing wraps an implementation so its constructions are counted as
// well as its executions. stage names the field the result is placed in, since
// a constructor is told nothing about which stage it serves.
func constructing(c *calls, stage Stage, impl Implementation) Implementation {
	inner := impl.NewBody
	impl.NewBody = func() Body {
		c.newStage(stage)
		return inner()
	}
	return impl
}

// constructingFixer wraps a fixer so its constructions are counted as well as
// its executions.
func constructingFixer(c *calls, fixer Fixer) Fixer {
	inner := fixer.NewBody
	fixer.NewBody = func() FixBody {
		c.newFix()
		return inner()
	}
	return fixer
}

// passing is a report a stage returns when it found nothing.
func passing() findings.Report { return findings.Report{Summary: passingSummary} }

// passingSummary is the summary a stage that found nothing reports. Constant
// panics on a report the pipeline would refuse, so the helper cannot hand a
// test a summary-less one.
const passingSummary = "nothing to report"

// reportWith is a report carrying one finding with the given action.
func reportWith(action findings.Action, description string) findings.Report {
	return findings.Report{
		Summary:  "one finding",
		Findings: []findings.Finding{{Action: action, Description: description}},
	}
}

// rounds returns fix round limits with every stage set to n.
func rounds(n int) config.FixRounds {
	return config.FixRounds{Rebase: n, Review: n, Test: n, Lint: n, Checks: n}
}

// build builds a pipeline or fails the test.
func build(t *testing.T, o Options) *Pipeline {
	t.Helper()
	p, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// start runs a pipeline from a complete Start and returns where it stopped.
func start(t *testing.T, p *Pipeline, s Start) (*graph.Executor, graph.Result) {
	t.Helper()
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(s)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	result, err := exec.Run(context.Background(), "run", state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return exec, result
}

// complete is a Start naming what a run validates.
func complete() Start {
	return Start{Repository: "repo-1", Run: "run-1", Branch: "topic", Base: "main", Submitted: "c0"}
}
