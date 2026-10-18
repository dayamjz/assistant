package machine_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

func TestStageOutcomeLabelNamesNoCodeHostSkip(t *testing.T) {
	t.Parallel()
	got := machine.StageOutcomeLabel(machine.Stage{
		Stage:      pipeline.StagePR.String(),
		Outcome:    pipeline.OutcomeSkipped,
		SkipReason: machine.SkipReasonNoCodeHost,
	})
	if got != "skipped (did not run: no-code-host)" {
		t.Fatalf("label = %q, want the no-code-host skip label", got)
	}
}
