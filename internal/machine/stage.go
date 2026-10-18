package machine

import "github.com/dayamjz/assistant/internal/pipeline"

// SkipReasonNoCodeHost is why the pull request and checks stages skipped when
// the run's upstream has no code host. It is pipeline's vocabulary on the
// wire as skip_reason.
const SkipReasonNoCodeHost = pipeline.SkipReasonNoCodeHost

// stageListingLabels are the outcome words beside one stage in a run listing.
// A renderer reaches them through StageOutcomeLabel rather than composing
// its own sentence.
var (
	stageLabelPending = "pending"
	stageLabelSkipped = "skipped (did not run)"
)

// stageLabelBySkipReason names a skip the run's own facts carry beyond the
// generic skipped outcome.
var stageLabelBySkipReason = map[string]string{
	SkipReasonNoCodeHost: "skipped (did not run: no-code-host)",
}

// StageOutcomeLabel is the outcome word beside one stage in a run listing.
func StageOutcomeLabel(s Stage) string {
	if !s.Ran && s.Outcome != "" {
		if s.Outcome == pipeline.OutcomeSkipped {
			if label, ok := stageLabelBySkipReason[s.SkipReason]; ok {
				return label
			}
			return stageLabelSkipped
		}
		return string(s.Outcome) + " (did not run)"
	}
	if s.Outcome == "" {
		return stageLabelPending
	}
	return string(s.Outcome)
}

// StageListingLabels returns every stage listing label the table holds, for
// tests that hold renderers to it.
func StageListingLabels() []string {
	out := []string{stageLabelPending, stageLabelSkipped}
	for _, label := range stageLabelBySkipReason {
		out = append(out, label)
	}
	return out
}
