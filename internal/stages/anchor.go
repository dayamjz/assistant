package stages

import (
	"encoding/json"
	"fmt"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/safety"
)

// The push target, and the anchor the push to it is made on.
//
// Two stages share both. The rebase stage observes where the target stands
// before the run does its work, and the push stage decides its update against
// that observation. They are here, together, because the two halves only mean
// anything as a pair: an observation of one target and an update to another
// would satisfy every type in sight and protect nothing.

// pushTarget is the branch a run forwards its verified commit to.
//
// It is the branch under validation and not the base. The base is what the
// pull request asks to merge into, and a run that pushed its commit there
// would have put the change on the base branch itself - past review, past the
// pull request, and past whatever policy the code host applies to that branch.
// PRD section 5 has the push stage forward the commit and the pull request
// stage propose merging it, which are two different references.
//
// One function answers this for both stages that need it. An observation taken
// against one reference and an update made to another is the failure the
// anchor exists to prevent, and it is not one either stage could notice on its
// own.
func pushTarget(branch string) safety.Target {
	return safety.Target{Remote: UpstreamRemote, Ref: "refs/heads/" + branch}
}

// anchorRecord is the durable form of an observation as it travels in run
// state. It is safety.ObservationRecord's fields under names that will not
// change when that struct is refactored, because what is written here has to
// be readable by the build that reads the checkpoint back.
type anchorRecord struct {
	Remote string `json:"remote"`
	Ref    string `json:"ref"`
	Exists bool   `json:"exists"`
	Commit string `json:"commit,omitempty"`
}

// encodeAnchor renders an observation as the value pipeline.KeyPushAnchor
// holds.
//
// It refuses an unobserved value rather than encoding one. A record that
// decoded back to something safety refuses would be a write that failed at the
// far end of a checkpoint, in a later stage, with nothing left to say where it
// came from.
func encodeAnchor(o safety.Observation) (graph.Value, error) {
	if !o.Observed() {
		return graph.Value{}, fmt.Errorf("stages: refusing to record an anchor nothing observed")
	}
	rec := o.Record()
	encoded, err := json.Marshal(anchorRecord{
		Remote: rec.Remote,
		Ref:    rec.Ref,
		Exists: rec.Exists,
		Commit: rec.Commit,
	})
	if err != nil {
		return graph.Value{}, fmt.Errorf("stages: recording the anchor for %s: %w", o.Target(), err)
	}
	return graph.TextValue(string(encoded)), nil
}

// decodeAnchor reads back what encodeAnchor wrote, and reports whether the run
// recorded one at all.
//
// An empty value is no anchor rather than a malformed one: a run whose
// observing stage was skipped never wrote the key, and that is a state of the
// run rather than a corrupt record. The caller decides what to do about it,
// and the push stage refuses.
//
// It restores through safety.RestoreObservedFromCheckpoint, so a record that
// could not have come from a read - an absent target naming a commit, or a
// present one naming none - is refused there rather than becoming an anchor
// this package assembled.
func decodeAnchor(text string) (observation safety.Observation, recorded bool, err error) {
	if text == "" {
		return safety.Observation{}, false, nil
	}
	var rec anchorRecord
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		return safety.Observation{}, false, fmt.Errorf(
			"stages: the anchor this run recorded cannot be read: %w", err)
	}
	restored, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: rec.Remote,
		Ref:    rec.Ref,
		Exists: rec.Exists,
		Commit: rec.Commit,
	})
	if err != nil {
		return safety.Observation{}, false, fmt.Errorf(
			"stages: the anchor this run recorded is not one a read could have produced: %w", err)
	}
	return restored, true, nil
}

// readAnchor reads the anchor out of the run's state.
func readAnchor(state pipeline.Reader) (observation safety.Observation, recorded bool, err error) {
	value, err := state.Get(pipeline.KeyPushAnchor)
	if err != nil {
		return safety.Observation{}, false, err
	}
	text, _ := value.Text()
	return decodeAnchor(text)
}

// withWrites returns out carrying these writes as well as its own.
//
// The output's own writes win where the two name one key, because they are the
// more specific answer: the caller assembled them for the report it is
// returning. Nothing here writes a key an output already carries, so that rule
// settles a case rather than resolving a live conflict.
func withWrites(out pipeline.Output, writes map[pipeline.Key]graph.Value) pipeline.Output {
	if len(writes) == 0 {
		return out
	}
	merged := make(map[pipeline.Key]graph.Value, len(writes)+len(out.Writes))
	for key, value := range writes {
		merged[key] = value
	}
	for key, value := range out.Writes {
		merged[key] = value
	}
	out.Writes = merged
	return out
}
