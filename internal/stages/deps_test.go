package stages_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestStageDepsIsNoRouteToAFixerSession is where P4 binds the wiring rather
// than one type.
//
// internal/agents keeps reviewing and fixing in separate memory, and
// agents.StageAgent is what a body holds so that agents.OpenFixer has no
// Runner to be given. None of that survives a StageDeps that carries a route
// to one anyway: a field of a type exposing a Runner, an adapter handed over
// whole, or a method returning one would each put a fixer session back within
// a review body's reach, and every stage body in this package is handed this
// struct.
//
// So the question is asked of the type graph. An assertion that a StageDeps is
// not a Runner would answer whether it is one, and what matters is whether a
// body can obtain one: agents.StageAgent given a Runner accessor is not a
// Runner and hands one over, which is the regression that motivated
// internal/agents/route and is recorded there.
//
// That the walk itself still inspects anything is established in
// internal/agents/route, which holds it to a type that really does expose a
// Runner, so this does not re-establish it.
//
// This is the test the review and pull request stage bodies inherit. A field
// added to StageDeps that reaches an agent adapter fails here rather than in
// review.
func TestStageDepsIsNoRouteToAFixerSession(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P4)

	if found := route.ToFixerSession(reflect.TypeOf(stages.StageDeps{})); len(found) != 0 {
		t.Fatalf("a stage body can reach a fixer session from the dependencies it is given, "+
			"so P4 is a rule callers follow rather than a mechanism:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// TestTheAgentOnAZeroStageDepsRefusesAsATypedResult checks the narrow thing
// its name says and is not the positive control for the guarantee above.
//
// Every assertion about routes would hold of a StageDeps whose agent was
// inert, so something does have to establish that the agent this seam hands
// over still works. That is TestAStageAgentRunsTheAgentItWraps in
// internal/agents, which drives a real invocation through the production
// adapter and reads the agent's own words back. A StageAgent that always
// errored would pass everything here, so this may not be read as standing in
// for it.
//
// What it does check is the zero value a caller can reach: a StageDeps nobody
// wired refuses with ErrNoStageAgent rather than panicking at the first
// invocation, so a body given no agent fails as a result its caller handles.
func TestTheAgentOnAZeroStageDepsRefusesAsATypedResult(t *testing.T) {
	t.Parallel()

	_, err := stages.StageDeps{}.Agent.Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "say what this change does",
		Shape:  agents.ShapeText,
		Dir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("the agent on a zero StageDeps ran an invocation, so a body wired with no agent " +
			"would report a stage it did not establish")
	}
	if !errors.Is(err, agents.ErrNoStageAgent) {
		t.Fatalf("the agent on a zero StageDeps refused with %v, want ErrNoStageAgent", err)
	}
}

// TestACopyPathIsDerivedFromTheRunsOwnKeys is what holds the run-scoped half
// of this seam to its shape.
//
// The isolated copy's path is not carried anywhere. PRD section 8's ordering
// rule has the run's row written before its directory, so a directory with no
// row is safe to remove, and a durable second copy of the path would give that
// rule a second fact to stay consistent with. So the path is derived, from the
// two run-input keys and internal/home's layout, every time it is needed.
//
// This drives a real stage body through a real pipeline and checks the
// derivation end to end: the keys reach the body carrying what Start supplied,
// and the path Copy resolves is the one internal/home names for them. A body
// that read the path from somewhere else, or a Copy that composed it itself,
// would resolve something the service does not know to reclaim, and that is
// the failure this catches.
//
// Copy is asked for a copy nothing created, so it fails. That is the point
// rather than a limitation: the failure names the path it tried, which is the
// derivation this is about, and creating the copy is the service's job.
func TestACopyPathIsDerivedFromTheRunsOwnKeys(t *testing.T) {
	t.Parallel()

	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a home: %v", err)
	}
	deps := stages.NewStageDeps(agents.StageAgent{}, h, config.Config{}, nil, redact.New())

	const repository, run = "repo-under-test", "run-under-test"
	var tried string
	body := pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				gotRepository, err := text(in, pipeline.KeyRepository)
				if err != nil {
					return pipeline.Output{}, err
				}
				gotRun, err := text(in, pipeline.KeyRun)
				if err != nil {
					return pipeline.Output{}, err
				}
				if gotRepository != repository || gotRun != run {
					t.Errorf("the body was given repository %q and run %q, want %q and %q",
						gotRepository, gotRun, repository, run)
				}
				if _, err := deps.Copy(ctx, gotRepository, gotRun); err != nil {
					tried = err.Error()
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "read the run's own keys",
				}}, nil
			}
		},
	}

	runPipeline(t, body, pipeline.Start{
		Repository: repository, Run: run,
		Branch: "topic", Base: "main", Submitted: "9f2c1ab",
	})

	want := h.Worktree(repository, run)
	if tried == "" {
		t.Fatal("Copy resolved a copy nothing created, so what path it derived is unproven")
	}
	if !strings.Contains(tried, want) {
		t.Errorf("Copy tried a path that is not the one internal/home names for this run.\n"+
			" got: %s\nwant it to name: %s", tried, want)
	}
}

// text reads one declared text key, so the body above states each read once.
func text(in pipeline.Input, key pipeline.Key) (string, error) {
	v, err := in.State.Get(key)
	if err != nil {
		return "", err
	}
	s, _ := v.Text()
	return s, nil
}
