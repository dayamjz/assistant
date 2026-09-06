package pipeline

import (
	"fmt"

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

// fixerRequirements is what the fixer needs, read only when a fix node is
// actually built.
//
// This is the one declaration whose reading depends on the fix round limits,
// and the reason is that a capability answers for a path rather than for a
// shape. A pipeline whose every limit is zero builds no fix node, so no fixer
// body runs and nothing it declared is ever needed; refusing then would refuse
// a path the run cannot take. That is the opposite of ErrUnmergeableFixerWrite,
// which is checked whenever a Fixer is supplied, because what that one asks is
// whether the declaration is legal at all and the answer must not vary with
// configuration.
//
// It leaves a fixer's requirement unread under limits that build no fixer, and
// that is correct rather than a gap: PRD section 8 has an adapter without
// resumable sessions keep no memory across rounds, and a pipeline that takes
// no rounds has none to keep.
func fixerRequirements(o Options, fixing bool) []requirement {
	if !fixing {
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
func checkRequirements(o Options, fixing bool) error {
	needed := append(requirements(o), fixerRequirements(o, fixing)...)
	for _, need := range needed {
		if !need.capability.Recognized() {
			return fmt.Errorf("%w: %s names %q", ErrUnknownCapability, need.path, need.capability)
		}
		if !o.Adapter.Has(need.capability) {
			return &agents.CapabilityError{Capability: need.capability, Path: need.path}
		}
	}
	return nil
}
