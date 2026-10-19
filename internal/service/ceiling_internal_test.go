package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// ceilingFixture is a service over a seeded store whose operator set the
// given ceiling, and a stand-in that answers every call, recorded by the
// service's own recorder and wrapped by containRunner the way driverFor
// wraps the resolved agent.
func ceilingFixture(t *testing.T, ceiling config.UsageCeiling) (*Service, *standin.Agent, agents.Runner) {
	t.Helper()
	database := filepath.Join(t.TempDir(), "state.db")
	seedDatabase(t, database)
	st, err := store.Open(t.Context(), database, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Defaults()
	cfg.UsageCeiling = ceiling
	s := &Service{store: st, cfg: cfg, stopCtx: t.Context(), registry: newRegistry()}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("done")}}},
		agents.WithRecorder(invocationRecorder{s: s}))
	return s, agent, s.containRunner(agent.Runner())
}

func inputCeiling(n int64) config.UsageCeiling {
	return config.UsageCeiling{Window: time.Hour, InputTokens: n}
}

func ceilingRefusalOf(t *testing.T, err error) machine.UsageCeiling {
	t.Helper()
	standing, ok := ceilingRefusal(err)
	if !ok {
		t.Fatalf("error %v, want a usage ceiling refusal", err)
	}
	return standing
}

// A ceiling whose record cannot be read refuses the start rather than
// passing it, because missing information is never room: the store is
// closed under a set ceiling, the call is refused unread, and the stand-in is
// never asked. A ceiling with no count set reads nothing and so admits over
// the same closed store, which is the positive control that the refusal is
// the unread record's and not the closed store failing every call. A context
// already ended is that ending and never a ceiling refusal.
func TestACeilingTheRecordCannotBeReadAgainstRefuses(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	s, agent, runner := ceilingFixture(t, inputCeiling(1_000_000))
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{Prompt: "review", Dir: t.TempDir()})
	if got := ceilingRefusalOf(t, err); got.Standing != machine.CeilingUnread || got.Unread == "" {
		t.Fatalf("an unreadable record answered %+v, want unread naming why", got)
	}
	if len(agent.Calls()) != 0 {
		t.Fatal("a start the ceiling could not weigh still reached the agent")
	}
	if !s.registry.empty() {
		t.Fatal("a refused start registered a process group")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, ended := runner.Run(ctx, agents.PurposeReview, agents.Invocation{Prompt: "review", Dir: t.TempDir()})
	if !errors.Is(ended, context.Canceled) {
		t.Fatalf("an ended context answered %v, want the cancellation", ended)
	}
	if _, refused := ceilingRefusal(ended); refused {
		t.Fatal("an ended context was read as a ceiling refusal")
	}

	s.cfg.UsageCeiling = config.Defaults().UsageCeiling
	if _, err := runner.Run(t.Context(), agents.PurposeReview, agents.Invocation{Prompt: "review", Dir: t.TempDir()}); err != nil {
		t.Fatalf("an unset ceiling refused over a closed store: %v", err)
	}
	if len(agent.Calls()) != 1 {
		t.Fatalf("the stand-in was asked %d times, want the one unset-ceiling call", len(agent.Calls()))
	}
}

// A fix round is held to the ceiling on the same terms as a stage body's
// call, and the observe wrapper turns the refusal into the round's hold
// rather than a failed step: the round changes nothing, the fixer is never
// asked, and its one hold is the ceiling's ask for a person, which
// internal/pipeline adds to the stage's next report. The first round runs
// under the ceiling and its recorded cost reaches it, so the second is the
// one refused; the first is the control that a round under the ceiling runs.
func TestAFixRoundTheCeilingRefusesHoldsTheStage(t *testing.T) {
	t.Parallel()
	cost, _ := standin.DefaultUsage().InputTokens.Value()
	s, agent, runner := ceilingFixture(t, inputCeiling(cost))
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("opening the fixer: %v", err)
	}
	dir := t.TempDir()
	round := s.observeFixer(pipeline.Fixer{NewBody: func() pipeline.FixBody {
		return func(ctx context.Context, _ pipeline.FixInput) (pipeline.FixOutput, error) {
			if _, err := fixer.Apply(ctx, agents.Invocation{Prompt: "fix", Dir: dir}); err != nil {
				return pipeline.FixOutput{}, err
			}
			return pipeline.FixOutput{Summary: "fixed"}, nil
		}
	}}, nil)
	in := pipeline.FixInput{Stage: pipeline.StageTest, State: mapReader{pipeline.KeyRun: graph.TextValue("run-1")}}
	segment := withAdvancing(t.Context(), "run-1")

	under, err := round.NewBody()(segment, in)
	if err != nil || under.Summary != "fixed" || len(under.Holds) != 0 {
		t.Fatalf("the round under the ceiling returned %+v, %v, want it run", under, err)
	}
	held, err := round.NewBody()(segment, in)
	if err != nil {
		t.Fatalf("the round past the ceiling failed rather than holding: %v", err)
	}
	if len(held.Holds) != 1 || held.Holds[0].ID != machine.UsageCeilingFindingID || held.Holds[0].Action != findings.ActionAsk ||
		len(held.Writes) != 0 {
		t.Fatalf("the round past the ceiling returned %+v, want no change and the ceiling's hold", held)
	}
	if len(agent.Calls()) != 1 {
		t.Fatalf("the fixer was asked %d times, want only the round under the ceiling", len(agent.Calls()))
	}
}
