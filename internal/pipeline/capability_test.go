package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
)

// sessions is the declaration of an adapter that can reopen a conversation,
// which Claude Code is and a second adapter may not be.
var sessions = agents.Declare(agents.CapabilityResumableSessions)

// requiring returns the nine stages with one of them needing a capability.
func requiring(stage Stage, capability agents.Capability) Stages {
	stages := ConstantStages(passingSummary)
	row, ok := stage.spec()
	if !ok {
		panic("no such stage")
	}
	impl := *row.implementation(&stages)
	impl.Requires = []agents.Capability{capability}
	set(&stages, stage, impl)
	return stages
}

// A stage that needs something the adapter has not declared is refused when
// the pipeline is built, which is before any run starts and so before the
// adapter is launched. The refusal names the stage and the capability, so a
// person reads which part of their gate could not be built.
func TestAStageNeedingAnUndeclaredCapabilityIsRefused(t *testing.T) {
	_, err := New(Options{
		Stages:  requiring(StageReview, agents.CapabilityResumableSessions),
		Budget:  100,
		Adapter: agents.Capabilities{},
	})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) {
		t.Fatalf("New answered %v, want a *agents.CapabilityError", err)
	}
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("the refusal does not match ErrUndeclaredCapability: %v", err)
	}
	if refusal.Capability != agents.CapabilityResumableSessions {
		t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityResumableSessions)
	}
	if !strings.Contains(refusal.Path, StageReview.String()) {
		t.Errorf("the refusal names the path %q, want it to name the review stage", refusal.Path)
	}
}

// The same pipeline builds once the adapter declares it, so the refusal above
// is about the declaration and not about the field being set at all.
func TestAStageNeedingADeclaredCapabilityBuilds(t *testing.T) {
	build(t, Options{
		Stages:  requiring(StageReview, agents.CapabilityResumableSessions),
		Budget:  100,
		Adapter: sessions,
	})
}

// Every one of the nine is checked, whatever a run then skips: a skip is a
// per-run choice on Start and a pipeline is built once for many runs.
func TestEveryStagesRequirementIsChecked(t *testing.T) {
	for _, stage := range Order() {
		t.Run(stage.String(), func(t *testing.T) {
			_, err := New(Options{
				Stages: requiring(stage, agents.CapabilityResumableSessions),
				Budget: 100,
			})
			var refusal *agents.CapabilityError
			if !errors.As(err, &refusal) {
				t.Fatalf("New answered %v, want a *agents.CapabilityError", err)
			}
			if !strings.Contains(refusal.Path, stage.String()) {
				t.Errorf("the refusal names the path %q, want it to name %s", refusal.Path, stage)
			}
		})
	}
}

// The case the declaration exists for. A fixer that keeps one durable session
// across a stage's rounds cannot be built against an adapter that has not
// declared resumable sessions, so the fix loop is refused rather than run with
// a fixer whose memory would have to come from somewhere else.
func TestAFixerNeedingSessionsIsRefusedAgainstAnAdapterWithoutThem(t *testing.T) {
	c := newCalls()
	fixer := recordingFixer(c, nil, nil, nil)
	fixer.Requires = []agents.Capability{agents.CapabilityResumableSessions}

	_, err := New(Options{
		Stages: recordingStages(c),
		Fixer:  fixer,
		Rounds: rounds(1),
		Budget: 100,
	})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) {
		t.Fatalf("New answered %v, want a *agents.CapabilityError", err)
	}
	if refusal.Capability != agents.CapabilityResumableSessions {
		t.Errorf("the refusal names %q, want %q", refusal.Capability, agents.CapabilityResumableSessions)
	}
	if !strings.Contains(refusal.Path, "fixer") {
		t.Errorf("the refusal names the path %q, want it to name the fixer", refusal.Path)
	}

	// The same fixer against an adapter that has sessions builds, which is
	// what the fixer path looks like when it is available rather than absent.
	build(t, Options{
		Stages:  recordingStages(c),
		Fixer:   fixer,
		Rounds:  rounds(1),
		Budget:  100,
		Adapter: sessions,
	})
}

// A fixer's availability requirement is read only where a fix node is built.
// PRD section 8 leaves an adapter without resumable sessions a run with no
// memory across rounds, and a pipeline whose every round limit is zero has no
// rounds to keep memory across, so refusing it would refuse a path the run
// cannot take.
//
// This is the one question whose answer depends on configuration, and the
// asymmetry with the stage check above is deliberate rather than an oversight:
// a stage is in the topology whatever the limits are. Whether the capability
// is a word at all is not gated this way, which the test below pins.
func TestAFixerRequirementIsUnreadWhereNoFixNodeIsBuilt(t *testing.T) {
	c := newCalls()
	fixer := recordingFixer(c, nil, nil, nil)
	fixer.Requires = []agents.Capability{agents.CapabilityResumableSessions}

	build(t, Options{
		Stages: recordingStages(c),
		Fixer:  fixer,
		Rounds: rounds(0),
		Budget: 100,
	})

	// One stage taking one round is enough to build a fix node, and that is
	// the difference the refusal turns on.
	_, err := New(Options{
		Stages: recordingStages(c),
		Fixer:  fixer,
		Rounds: config.FixRounds{Lint: 1},
		Budget: 100,
	})
	if !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("New answered %v with one stage taking a round, want the fixer path refused", err)
	}
}

// Whether a requirement names a capability that exists is a question about the
// declaration and not about a path, so it is answered the same way whatever
// the fix round limits are. A typo in the fixer's Requires is refused under
// limits of zero, where no fix node is built and the availability half of the
// same declaration goes unread.
//
// The limits are why this must not be gated. P7 re-reads them from the default
// branch, so a pipeline built under zero rounds is built again under nonzero
// ones by a service already running, and a declaration that was legal on the
// first build and not the second is a defect that surfaces as a difference
// between two builds rather than as itself.
func TestAFixerRequirementOnAnUnknownCapabilityIsRefusedWhateverTheLimits(t *testing.T) {
	c := newCalls()
	fixer := recordingFixer(c, nil, nil, nil)
	fixer.Requires = []agents.Capability{"resumable_session"}

	for _, limits := range []struct {
		name   string
		rounds config.FixRounds
	}{
		{name: "no stage takes a round", rounds: rounds(0)},
		{name: "one stage takes a round", rounds: config.FixRounds{Lint: 1}},
	} {
		t.Run(limits.name, func(t *testing.T) {
			_, err := New(Options{
				Stages:  recordingStages(c),
				Fixer:   fixer,
				Rounds:  limits.rounds,
				Budget:  100,
				Adapter: agents.Declare(agents.CapabilityResumableSessions),
			})
			if !errors.Is(err, ErrUnknownCapability) {
				t.Fatalf("New answered %v, want ErrUnknownCapability", err)
			}
			if !strings.Contains(err.Error(), "resumable_session") {
				t.Errorf("the refusal reads %q, want it to name the capability", err)
			}
		})
	}
}

// Instruction suppression is a property of the run rather than of any stage,
// per PRD section 10: a run configured to suppress a repository's own
// instructions fails before launching an agent that has no mechanism for it.
// No stage declares it and none can decline it.
func TestSuppressingInstructionsIsRefusedAgainstAnAdapterWithoutTheMechanism(t *testing.T) {
	_, err := New(Options{
		Stages:                      ConstantStages(passingSummary),
		Budget:                      100,
		Adapter:                     sessions,
		SuppressProjectInstructions: true,
	})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) {
		t.Fatalf("New answered %v, want a *agents.CapabilityError", err)
	}
	if refusal.Capability != agents.CapabilitySuppressProjectInstructions {
		t.Errorf("the refusal names %q, want %q",
			refusal.Capability, agents.CapabilitySuppressProjectInstructions)
	}

	// An adapter that declares the mechanism builds, so the refusal is about
	// the declaration rather than about the key being set.
	build(t, Options{
		Stages:                      ConstantStages(passingSummary),
		Budget:                      100,
		Adapter:                     agents.Declare(agents.CapabilitySuppressProjectInstructions),
		SuppressProjectInstructions: true,
	})

	// And a run that did not ask for suppression builds against an adapter
	// that has no mechanism for it, which is every adapter this build ships.
	build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
}

// A requirement naming a word internal/agents does not define is refused as a
// mistake rather than reported as a capability the adapter lacks, which would
// send a reader looking for an adapter feature that does not exist.
func TestARequirementOnAnUnknownCapabilityIsRefused(t *testing.T) {
	_, err := New(Options{
		Stages:  requiring(StageTest, "resumable_session"),
		Budget:  100,
		Adapter: sessions,
	})
	if !errors.Is(err, ErrUnknownCapability) {
		t.Fatalf("New answered %v, want ErrUnknownCapability", err)
	}
	if errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Error("a capability nothing defines was reported as one the adapter has not declared")
	}
}

// A declaration this pipeline needs nothing of builds against an adapter that
// declares nothing, which is what keeps the rule a refusal of paths rather
// than a requirement that every adapter declare everything.
func TestAPipelineThatNeedsNothingBuildsAgainstAnAdapterThatDeclaresNothing(t *testing.T) {
	c := newCalls()
	build(t, Options{
		Stages: recordingStages(c),
		Fixer:  recordingFixer(c, nil, nil, nil),
		Rounds: rounds(2),
		Budget: 100,
	})
}
