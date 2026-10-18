package service_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// A completed run whose upstream is local reports why the pull request and
// checks stages skipped. Removing StageSkipReason's guard leaves the JSON
// field empty while the stages still skip.
func TestANoCodeHostRunReportsWhyPullRequestAndChecksSkipped(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		for run.Outcome == machine.OutcomeDecision {
			run = answer(t, running.client, run.Record.ID, string(pipeline.OutcomeApproved))
		}
		if run.Outcome != machine.OutcomePushed {
			t.Fatalf("run outcome = %s, want pushed", run.Outcome)
		}
		for _, stage := range []pipeline.Stage{pipeline.StagePR, pipeline.StageCI} {
			view := stageView(t, run, stage)
			if view.SkipReason != machine.SkipReasonNoCodeHost {
				t.Fatalf("%s skip_reason = %q, want %q", stage, view.SkipReason, machine.SkipReasonNoCodeHost)
			}
			if got := machine.StageOutcomeLabel(view); got != "skipped (did not run: no-code-host)" {
				t.Fatalf("%s listing label = %q, want the no-code-host skip label", stage, got)
			}
		}
	})
}
