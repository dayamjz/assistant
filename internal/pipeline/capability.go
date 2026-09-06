package pipeline

import (
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/agents"
)

// requirement is one adapter capability some part of this pipeline needs, and
// the path that needs it. The path is what a refusal names, so a person reads
// which part of their gate could not be built rather than only what was
// missing.
type requirement struct {
	capability agents.Capability
	path       string
}

// requirements is everything this pipeline needs of the resolved adapter, in a
// stable order: the run-wide requirements first, then the nine stages in the
// order a run takes them, then the fixer.
//
// Gathering them in one place is what keeps the answer to "what does this gate
// need of an agent" from being spread over the nodes that need it. A caller
// declares a path's requirement on the path, and this is where every such
// declaration is read.
//
// Two of the three sources are declarations a caller writes, and one is not.
// PRD section 10 makes instruction suppression a property of the run rather
// than of any one stage: a run configured to suppress a repository's own
// instructions fails before launching an agent that has no mechanism for it,
// whichever stage would have launched it first. So that row is read from the
// configuration the pipeline was built with and no stage can decline it.
func requirements(o Options) []requirement {
	var out []requirement
	if o.SuppressProjectInstructions {
		out = append(out, requirement{
			capability: agents.CapabilitySuppressProjectInstructions,
			path:       "suppressing the repository's own instructions",
		})
	}
	for _, row := range stageTable {
		impl := row.implementation(&o.Stages)
		for _, capability := range impl.Requires {
			out = append(out, requirement{
				capability: capability,
				path:       "the " + row.stage.String() + " stage",
			})
		}
	}
	return out
}

// fixerRequirements is what the fixer declares it needs, and nothing when no
// Fixer was supplied at all.
//
// Whether the fix round limits gate this depends on which question is being
// asked of it, and checkRequirements asks both. It is returned whole here so
// that the gating lives in one place rather than in this function and again at
// the caller.
func fixerRequirements(o Options) []requirement {
	if o.Fixer.NewBody == nil {
		return nil
	}
	out := make([]requirement, 0, len(o.Fixer.Requires))
	for _, capability := range o.Fixer.Requires {
		out = append(out, requirement{capability: capability, path: "the fixer"})
	}
	return out
}

// checkRequirements refuses the pipeline when the adapter has not declared
// something a path it would build needs.
//
// It is the pipeline's half of PRD section 8's rule. The refusal happens while
// the topology is being built, before a run starts and so before any adapter
// is launched, and it names the path and the capability.
//
// Whether a second half stands underneath it differs by capability, and a
// reader should not take the resumable-sessions story for the general one.
// For agents.CapabilityResumableSessions there is one: agents.OpenFixer will
// not hand a session to an adapter that has not declared one whatever any
// declaration here said, so a fix path that forgot to declare it is refused
// late and by name rather than served by a weaker path, and what this check
// buys is that the refusal arrives before the run instead of partway through
// it. For agents.CapabilitySuppressProjectInstructions there is none:
// internal/agents implements no suppression and so has nothing to refuse at,
// which makes this check on Options.SuppressProjectInstructions the only
// enforcement of PRD section 10's rule that such a run fails before an agent
// is launched.
// Two questions are asked of every requirement and they are not gated alike.
//
// Whether a capability is a word internal/agents defines is a question about
// the declaration itself, so it is asked of every declaration this pipeline
// holds, whatever the fix round limits are. A typo is a typo under limits of
// zero, and P7 re-reads those limits from the default branch, so an answer
// that varied with them would vary under a running service. That is the same
// stance ErrUnmergeableFixerWrite takes on the fixer's writes.
//
// Whether the adapter declared it is a question about a path, so it is asked
// only of paths this pipeline builds. A pipeline whose every limit is zero
// builds no fix node, so nothing the fixer declared is ever needed and
// refusing then would refuse a path the run cannot take. That leaves a
// fixer's availability requirement unread under limits that build no fixer,
// which is correct rather than a gap: PRD section 8 has an adapter without
// resumable sessions keep no memory across rounds, and a pipeline that takes
// no rounds has none to keep.
func checkRequirements(o Options, fixing bool) error {
	run, fixer := requirements(o), fixerRequirements(o)
	for _, need := range slices.Concat(run, fixer) {
		if !need.capability.Recognized() {
			return fmt.Errorf("%w: %s names %q", ErrUnknownCapability, need.path, need.capability)
		}
	}
	built := run
	if fixing {
		built = slices.Concat(run, fixer)
	}
	for _, need := range built {
		if !o.Adapter.Has(need.capability) {
			return &agents.CapabilityError{Capability: need.capability, Path: need.path}
		}
	}
	return nil
}
