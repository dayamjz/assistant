package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

func TestAStageThatTakesFixRoundsNeedsAFixer(t *testing.T) {
	_, err := New(Options{
		Stages: ConstantStages(passing()),
		Rounds: config.FixRounds{Review: 1},
		Budget: 100,
	})
	if !errors.Is(err, ErrMissingFixer) {
		t.Fatalf("New with rounds and no fixer: %v, want ErrMissingFixer", err)
	}
	if _, err := New(Options{Stages: ConstantStages(passing()), Budget: 100}); err != nil {
		t.Fatalf("New with no rounds and no fixer: %v, want no refusal", err)
	}
}

func TestANegativeRoundLimitIsRefused(t *testing.T) {
	_, err := New(Options{
		Stages: ConstantStages(passing()),
		Fixer:  recordingFixer(newCalls(), nil, nil, nil),
		Rounds: config.FixRounds{Lint: -1},
		Budget: 100,
	})
	if !errors.Is(err, ErrNegativeRounds) {
		t.Fatalf("New with a negative limit: %v, want ErrNegativeRounds", err)
	}
	if !strings.Contains(err.Error(), "lint") {
		t.Errorf("refusal %q does not name the stage", err)
	}
}

func TestADeclarationOutsideTheSchemaIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads []Key
		wants []Key
		want  error
	}{
		{"read of a key nobody declared", []Key{"invented"}, nil, ErrUndeclaredKey},
		{"write of a key nobody declared", nil, []Key{"invented"}, ErrUndeclaredKey},
		{"the same key read twice", []Key{KeyHead, KeyHead}, nil, ErrUndeclaredKey},
		{"write of a stage outcome", nil, []Key{StageReview.OutcomeKey()}, ErrReservedKey},
		{"write of a stage report", nil, []Key{StageLint.ReportKey()}, ErrReservedKey},
		{"write of a hold answer", nil, []Key{StagePush.AnswerKey()}, ErrReservedKey},
		{"write of a fix summary", nil, []Key{StageTest.FixKey()}, ErrReservedKey},
		{"write of a run input", nil, []Key{KeyBranch}, ErrReservedKey},
		{"write of the cancelled flag", nil, []Key{KeyCancelled}, ErrReservedKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stages := ConstantStages(passing())
			impl := Constant(passing())
			impl.Reads, impl.Writes = tc.reads, tc.wants
			set(&stages, StageDocument, impl)
			_, err := New(Options{Stages: stages, Budget: 100})
			if !errors.Is(err, tc.want) {
				t.Fatalf("New: %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAFixerDeclarationOutsideTheSchemaIsRefused(t *testing.T) {
	fixer := recordingFixer(newCalls(), nil, []Key{StageReview.OutcomeKey()}, nil)
	_, err := New(Options{
		Stages: ConstantStages(passing()),
		Fixer:  fixer,
		Rounds: config.FixRounds{Review: 1},
		Budget: 100,
	})
	if !errors.Is(err, ErrReservedKey) {
		t.Fatalf("New with a fixer writing a pipeline key: %v, want ErrReservedKey", err)
	}
}

// TestABodyThatStepsOutsideItsDeclarationFailsTheStep is what makes the
// declaration load-bearing at run time as well as at build time. The node
// declares more keys than the implementation did, because this package's own
// adapter reads and writes some, so without the check a body could reach them.
func TestABodyThatStepsOutsideItsDeclarationFailsTheStep(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(in Input, call int) (Output, error)
		want error
	}{
		{"reading a key the adapter declared", func(in Input, _ int) (Output, error) {
			_, err := in.State.Get(KeySkip)
			return Output{Report: passing()}, err
		}, ErrUndeclaredRead},
		{"reading a key nothing declared", func(in Input, _ int) (Output, error) {
			_, err := in.State.Get(KeyApproved)
			return Output{Report: passing()}, err
		}, ErrUndeclaredRead},
		{"writing a key it did not declare", func(Input, int) (Output, error) {
			return Output{
				Report: passing(),
				Writes: map[Key]graph.Value{KeyApproved: graph.TextValue("c9")},
			}, nil
		}, ErrUndeclaredWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCalls()
			stages := recordingStages(c)
			set(&stages, StageReview, recording(c, nil, nil, tc.run))
			p := build(t, Options{Stages: stages, Budget: 100})
			exec, err := p.Executor(graph.NewMemoryStore())
			if err != nil {
				t.Fatalf("Executor: %v", err)
			}
			state, err := p.NewState(complete())
			if err != nil {
				t.Fatalf("NewState: %v", err)
			}
			_, err = exec.Run(context.Background(), "run", state)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run: %v, want %v", err, tc.want)
			}
			if n := c.stageCount(StageTest); n != 0 {
				t.Errorf("test ran %d times after review failed, want 0", n)
			}
		})
	}
}

// TestABodyMayReachEveryKeyItDeclared is the control for the test above: the
// refusals there are about the declaration and not about the key, so the same
// reads and writes succeed once they are declared.
func TestABodyMayReachEveryKeyItDeclared(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, []Key{KeySubmitted}, []Key{KeyApproved},
		func(in Input, _ int) (Output, error) {
			v, err := in.State.Get(KeySubmitted)
			if err != nil {
				return Output{}, err
			}
			submitted, _ := v.Text()
			return Output{
				Report: passing(),
				Writes: map[Key]graph.Value{KeyApproved: graph.TextValue(submitted)},
			}, nil
		}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	v, _ := result.State.Get(string(KeyApproved))
	if approved, _ := v.Text(); approved != "c0" {
		t.Errorf("approved %q, want the submitted commit", approved)
	}
}

func TestARunNeedsToSayWhatItIsValidating(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passing()), Budget: 100})
	for _, s := range []Start{
		{Base: "main", Submitted: "c0"},
		{Branch: "topic", Submitted: "c0"},
		{Branch: "topic", Base: "main"},
	} {
		if _, err := p.NewState(s); !errors.Is(err, ErrIncompleteRun) {
			t.Errorf("NewState(%+v): %v, want ErrIncompleteRun", s, err)
		}
	}
	if _, err := p.NewState(complete()); err != nil {
		t.Errorf("NewState with everything: %v, want no refusal", err)
	}
}

func TestARunCannotSkipAStageThatDoesNotExist(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passing()), Budget: 100})
	begin := complete()
	begin.Skip = []Stage{Stage(200)}
	if _, err := p.NewState(begin); !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("NewState with an invented stage: %v, want ErrUnknownStage", err)
	}
}

// TestARunCannotStartAlreadyHoldingAHoldAnswer is the graph's rule reaching
// this package: a run must not begin with an answer nobody gave.
func TestARunCannotStartAlreadyHoldingAHoldAnswer(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passing()), Budget: 100})
	_, err := p.Graph().NewState(map[string]graph.Value{
		string(StageReview.AnswerKey()): graph.TextValue(string(OutcomeApproved)),
	})
	if !errors.Is(err, graph.ErrAnswerPreseeded) {
		t.Fatalf("a state pre-seeding a hold answer: %v, want ErrAnswerPreseeded", err)
	}
}

// TestARecordedReportThatCannotBeReadIsRefused checks the report is not read
// back as empty when it cannot be decoded, since an empty report presents a
// stage that found something as one that found nothing.
func TestARecordedReportThatCannotBeReadIsRefused(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passing()), Budget: 100})
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := StageReport(state, StageReview); err != nil {
		t.Fatalf("a stage that has not run: %v, want the zero report", err)
	}
	broken, err := p.Graph().NewState(map[string]graph.Value{
		string(StageReview.ReportKey()): graph.TextValue("{not json"),
	})
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := StageReport(broken, StageReview); !errors.Is(err, ErrBadReport) {
		t.Fatalf("StageReport on an undecodable report: %v, want ErrBadReport", err)
	}
}

// TestAStageThatCannotRunStopsTheRun separates a finding from a failure: a
// stage that returns an error stops the run, and its writes are discarded.
func TestAStageThatCannotRunStopsTheRun(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	boom := errors.New("the agent never started")
	set(&stages, StageTest, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{
			Report: reportWith(findings.ActionFix, "never recorded"),
		}, boom
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := exec.Run(context.Background(), "run", state); !errors.Is(err, boom) {
		t.Fatalf("Run: %v, want the stage's own error", err)
	}
}

// TestAStageMayDeclareAKeyTheAdapterAlsoReads guards the one place a stage's
// declaration and this package's own meet. The graph refuses a node that lists
// a key twice, so a stage reading the empty-diff flag or the skip list has to
// be merged into the node's declaration rather than appended to it.
func TestAStageMayDeclareAKeyTheAdapterAlsoReads(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	var saw bool
	set(&stages, StagePush, recording(c, []Key{KeyDiffEmpty, KeySkip}, nil, func(in Input, _ int) (Output, error) {
		if _, err := in.State.Get(KeyDiffEmpty); err != nil {
			return Output{}, err
		}
		if _, err := in.State.Get(KeySkip); err != nil {
			return Output{}, err
		}
		saw = true
		return Output{Report: passing()}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if !saw {
		t.Error("the push stage never read the keys it declared")
	}
}

// TestAFixerMayDeclareAKeyTheAdapterAlsoReads is the same meeting point on the
// fix node.
func TestAFixerMayDeclareAKeyTheAdapterAlsoReads(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "unused import")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	var saw bool
	fixer := recordingFixer(c, []Key{StageLint.ReportKey(), StageLint.FixKey()}, nil,
		func(in FixInput, _ int) (FixOutput, error) {
			if _, err := in.State.Get(StageLint.ReportKey()); err != nil {
				return FixOutput{}, err
			}
			if _, err := in.State.Get(StageLint.FixKey()); err != nil {
				return FixOutput{}, err
			}
			saw = true
			return FixOutput{Summary: "removed it"}, nil
		})
	p := build(t, Options{
		Stages: stages,
		Fixer:  fixer,
		Rounds: config.FixRounds{Lint: 1},
		Budget: 100,
	})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if !saw {
		t.Error("the fixer never read the keys it declared")
	}
}
