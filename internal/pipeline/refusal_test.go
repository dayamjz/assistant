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
		Stages: ConstantStages(passingSummary),
		Rounds: config.FixRounds{Review: 1},
		Budget: 100,
	})
	if !errors.Is(err, ErrMissingFixer) {
		t.Fatalf("New with rounds and no fixer: %v, want ErrMissingFixer", err)
	}
	if _, err := New(Options{Stages: ConstantStages(passingSummary), Budget: 100}); err != nil {
		t.Fatalf("New with no rounds and no fixer: %v, want no refusal", err)
	}
}

func TestANegativeRoundLimitIsRefused(t *testing.T) {
	_, err := New(Options{
		Stages: ConstantStages(passingSummary),
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
		{"write of the supplied-intent flag", nil, []Key{KeyIntentSupplied}, ErrReservedKey},
		{"write of the cancelled flag", nil, []Key{KeyCancelled}, ErrReservedKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stages := ConstantStages(passingSummary)
			impl := Constant(passingSummary)
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
		Stages: ConstantStages(passingSummary),
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
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	// Each case is a complete start with one fact removed, rather than a
	// literal listing the others, so a fact added to Start is refused here by
	// adding one row and a case cannot quietly drift into naming something
	// else.
	for _, c := range []struct {
		missing string
		blank   func(*Start)
	}{
		{"repository", func(s *Start) { s.Repository = "" }},
		{"run", func(s *Start) { s.Run = "" }},
		{"branch", func(s *Start) { s.Branch = "" }},
		{"base", func(s *Start) { s.Base = "" }},
		{"submitted", func(s *Start) { s.Submitted = "" }},
	} {
		s := complete()
		c.blank(&s)
		if _, err := p.NewState(s); !errors.Is(err, ErrIncompleteRun) {
			t.Errorf("NewState with no %s: %v, want ErrIncompleteRun", c.missing, err)
		}
	}
	if _, err := p.NewState(complete()); err != nil {
		t.Errorf("NewState with everything: %v, want no refusal", err)
	}
}

func TestARunCannotSkipAStageThatDoesNotExist(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	begin := complete()
	begin.Skip = []Stage{Stage(200)}
	if _, err := p.NewState(begin); !errors.Is(err, ErrUnknownStage) {
		t.Fatalf("NewState with an invented stage: %v, want ErrUnknownStage", err)
	}
}

// TestARunCannotStartAlreadyHoldingAHoldAnswer is the graph's rule reaching
// this package: a run must not begin with an answer nobody gave.
func TestARunCannotStartAlreadyHoldingAHoldAnswer(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
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
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
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

// TestASwallowedReadRefusalStillFailsTheStep is the half of the declaration a
// body could otherwise decide for itself. The bodies in
// TestABodyThatStepsOutsideItsDeclarationFailsTheStep hand the refusal back,
// so they prove only that the error reaches the caller. These discard it and
// carry on reporting a clean stage, which is what an implementation does by
// accident, and the step must fail anyway.
func TestASwallowedReadRefusalStillFailsTheStep(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(in Input, _ int) (Output, error) {
		v, _ := in.State.Get(KeySkip)
		if skipped, _ := v.Bool(); skipped {
			return Output{Report: reportWith(findings.ActionAsk, "unreachable")}, nil
		}
		// A second refused read, so the reported refusal can be pinned to the
		// first one rather than to whichever happened last.
		if _, err := in.State.Get(KeyApproved); err == nil {
			t.Error("a read of an undeclared key returned no error to the body")
		}
		return Output{Report: passing()}, nil
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
	result, err := exec.Run(context.Background(), "run", state)
	if !errors.Is(err, ErrUndeclaredRead) {
		t.Fatalf("Run: %v (status %s), want ErrUndeclaredRead", err, result.Status)
	}
	if !strings.Contains(err.Error(), string(KeySkip)) {
		t.Errorf("refusal %q does not name %q: the first refusal is the one that happened", err, KeySkip)
	}
	if n := c.stageCount(StageTest); n != 0 {
		t.Errorf("test ran %d times after review read outside its declaration, want 0", n)
	}
}

// TestASwallowedFixerReadRefusalStillFailsTheStep is the same for the fix node,
// which reaches a body through the same reader.
func TestASwallowedFixerReadRefusalStillFailsTheStep(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "unused import")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	fixer := recordingFixer(c, nil, nil, func(in FixInput, _ int) (FixOutput, error) {
		v, _ := in.State.Get(KeyHead)
		head, _ := v.Text()
		return FixOutput{Summary: "removed it at " + head}, nil
	})
	p := build(t, Options{
		Stages: stages,
		Fixer:  fixer,
		Rounds: config.FixRounds{Lint: 1},
		Budget: 100,
	})
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	result, err := exec.Run(context.Background(), "run", state)
	if !errors.Is(err, ErrUndeclaredRead) {
		t.Fatalf("Run: %v (status %s), want ErrUndeclaredRead", err, result.Status)
	}
	if n := c.stageCount(StageLint); n != 1 {
		t.Errorf("lint ran %d times, want 1: the fix round it was verifying failed", n)
	}
	if n := c.stageCount(StagePush); n != 0 {
		t.Errorf("push ran %d times after the fixer read outside its declaration, want 0", n)
	}
}

// TestARunThatClaimsASuppliedIntentMustSupplyOne pins the guard to the pair
// rather than to emptiness alone: an absent intent falls back to inference and
// stays legal, and only the claim of authoritative criteria with none behind it
// is refused.
func TestARunThatClaimsASuppliedIntentMustSupplyOne(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	for _, tc := range []struct {
		name   string
		intent string
	}{
		{"empty", ""},
		{"whitespace only", "  \n\t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := complete()
			s.IntentSupplied, s.Intent = true, tc.intent
			if _, err := p.NewState(s); !errors.Is(err, ErrEmptyIntent) {
				t.Fatalf("NewState: %v, want ErrEmptyIntent", err)
			}
		})
	}

	t.Run("no intent claimed", func(t *testing.T) {
		s := complete()
		s.IntentSupplied, s.Intent = false, ""
		if _, err := p.NewState(s); err != nil {
			t.Fatalf("NewState: %v, want no refusal: an inferred intent is the fallback", err)
		}
	})
	t.Run("supplied intent padded with whitespace", func(t *testing.T) {
		s := complete()
		s.IntentSupplied, s.Intent = true, "\n  make the gate refuse an unverified push  \n"
		state, err := p.NewState(s)
		if err != nil {
			t.Fatalf("NewState: %v, want no refusal: there is real text here", err)
		}
		v, _ := state.Get(string(KeyIntent))
		if got, _ := v.Text(); got != s.Intent {
			t.Errorf("intent %q, want it seeded verbatim: this package does not edit it", got)
		}
	})
}

// TestAStageMayRecordAnInferredIntentButNotCallItSupplied pins which half of
// the intent pair is narrowed and which is not. Recording what a stage inferred
// is the intent stage's job, so the intent itself stays writable; the flag
// saying a person supplied it is a run input, because no stage can make that
// true and a stage that set it would have every downstream prompt frame a guess
// as requirements.
func TestAStageMayRecordAnInferredIntentButNotCallItSupplied(t *testing.T) {
	refused := ConstantStages(passingSummary)
	claiming := Constant(passingSummary)
	claiming.Writes = []Key{KeyIntentSupplied}
	set(&refused, StageIntent, claiming)
	if _, err := New(Options{Stages: refused, Budget: 100}); !errors.Is(err, ErrReservedKey) {
		t.Fatalf("a stage declaring a write of %q: %v, want ErrReservedKey", KeyIntentSupplied, err)
	}

	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageIntent, recording(c, nil, []Key{KeyIntent}, func(Input, int) (Output, error) {
		return Output{
			Report: passing(),
			Writes: map[Key]graph.Value{KeyIntent: graph.TextValue("inferred from the diff")},
		}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	v, _ := result.State.Get(string(KeyIntent))
	if got, _ := v.Text(); got != "inferred from the diff" {
		t.Errorf("intent %q, want what the stage inferred: recording one is still allowed", got)
	}
	supplied, _ := result.State.Get(string(KeyIntentSupplied))
	if flag, _ := supplied.Bool(); flag {
		t.Error("intent.supplied is true after a run that supplied none")
	}
}

// TestAFixerMayOnlyWriteAKeyThatDeclaresAMergeRule pins the constraint to the
// schema row rather than to the configuration. One Fixer serves every fix node,
// so a key it declares has as many writers as there are stages taking rounds,
// and the graph refuses a second writer of a key with no merge rule. Deciding
// that in checkDeclared is what keeps the answer the same across every
// configuration: P7 re-reads those limits from the default branch, so a Fixer
// legal today would otherwise be refused tomorrow without having changed.
//
// The three cases are the three counts a configuration can produce - no fix
// node, one, and one per fix-capable stage - because the answer has to be the
// same at both boundaries, zero against one and one against several.
func TestAFixerMayOnlyWriteAKeyThatDeclaresAMergeRule(t *testing.T) {
	// The five stages PRD section 5 gives automatic fix rounds, written out
	// here rather than read from the package, so the node count below is an
	// expectation this test can fail against rather than a restatement of it.
	fixCapable := []string{"rebase", "review", "test", "lint", "ci"}
	for _, tc := range []struct {
		name   string
		limits config.FixRounds
		nodes  int
	}{
		{"no fix node at all", config.FixRounds{}, 0},
		{"one fix node", config.FixRounds{Lint: 1}, 1},
		{"one fix node per fix-capable stage", config.Defaults().FixRounds, len(fixCapable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := fixNodes(t, tc.limits); n != tc.nodes {
				t.Fatalf("this configuration builds %d fix nodes, want %d: the case does not cover what it is named for", n, tc.nodes)
			}
			_, err := New(Options{
				Stages: ConstantStages(passingSummary),
				Fixer:  recordingFixer(newCalls(), nil, []Key{KeyDiffEmpty}, nil),
				Rounds: tc.limits,
				Budget: 100,
			})
			if !errors.Is(err, ErrUnmergeableFixerWrite) {
				t.Fatalf("a fixer writing %q: %v, want ErrUnmergeableFixerWrite", KeyDiffEmpty, err)
			}
		})
	}

	t.Run("a stage may still write it", func(t *testing.T) {
		stages := ConstantStages(passingSummary)
		impl := Constant(passingSummary)
		impl.Writes = []Key{KeyDiffEmpty}
		set(&stages, StageRebase, impl)
		if _, err := New(Options{Stages: stages, Budget: 100}); err != nil {
			t.Fatalf("a stage writing %q: %v, want no refusal: a stage node is its own only writer", KeyDiffEmpty, err)
		}
	})
}

// fixNodes counts the fix nodes a configuration builds, so the test above
// asserts which case is which rather than assuming it. A fixer declaring the
// one mergeable key is used here, because this helper must build.
func fixNodes(t *testing.T, limits config.FixRounds) int {
	t.Helper()
	p := build(t, Options{
		Stages: ConstantStages(passingSummary),
		Fixer:  recordingFixer(newCalls(), nil, []Key{KeyHead}, nil),
		Rounds: limits,
		Budget: 100,
	})
	n := 0
	for _, stage := range Order() {
		if _, ok := p.Graph().Node(stage.FixNode()); ok {
			n++
		}
	}
	return n
}

// TestAFixerMayWriteTheHeadItCommits is the control: the one key the schema
// admits to a fixer builds and reaches state, so the refusal above is about the
// missing merge rule and not about fixers writing at all.
func TestAFixerMayWriteTheHeadItCommits(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "unused import")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	fixer := recordingFixer(c, nil, []Key{KeyHead}, func(FixInput, int) (FixOutput, error) {
		return FixOutput{
			Summary: "removed it",
			Writes:  map[Key]graph.Value{KeyHead: graph.TextValue("c1")},
		}, nil
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
	v, _ := result.State.Get(string(KeyHead))
	if head, _ := v.Text(); head != "c1" {
		t.Errorf("head %q, want the commit the fixer wrote", head)
	}
}

// TestAStageThatSaysNothingFailsTheStep is the fail-open this guard closes. A
// report with no summary normalizes cleanly and classifies as passed, so
// without the validation an empty or truncated stage output would be recorded
// as a stage that found nothing and the run would advance on it.
func TestAStageThatSaysNothingFailsTheStep(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{}}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	store := graph.NewMemoryStore()
	exec, err := p.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	result, err := exec.Run(context.Background(), "run", state)
	if !errors.Is(err, ErrUnusableReport) {
		t.Fatalf("Run: %v (status %s), want ErrUnusableReport", err, result.Status)
	}
	if !strings.Contains(err.Error(), string(findings.DefectMissingSummary)) {
		t.Errorf("refusal %q does not name the defect, so a caller cannot read what was wrong", err)
	}

	latest, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got := StageOutcome(latest.State, StageReview); got != OutcomePending {
		t.Errorf("review outcome %q, want %q: a step that failed records nothing", got, OutcomePending)
	}
	if StageRan(latest.State, StageReview) {
		t.Error("review recorded a report, and the step that would have recorded it failed")
	}
	if n := c.stageCount(StageTest); n != 0 {
		t.Errorf("test ran %d times after review failed, want 0", n)
	}
}

// TestAStageThatSaysSomethingPasses is the control: the refusal above is about
// the missing summary and not about reporting at all.
func TestAStageThatSaysSomethingPasses(t *testing.T) {
	c := newCalls()
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "read the diff, found nothing"}}, nil
	}))
	p := build(t, Options{Stages: stages, Budget: 100})
	_, result := start(t, p, complete())
	if result.Status != graph.StatusCompleted {
		t.Fatalf("status %s, reason %q, want completed", result.Status, result.Reason)
	}
	if got := StageOutcome(result.State, StageReview); got != OutcomePassed {
		t.Errorf("review outcome %q, want %q", got, OutcomePassed)
	}
}
