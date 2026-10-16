package service_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// A run's answer carries when each stage it executed started and ended, read
// from the store's record, so it survives the process that executed it: a
// second service reports the same executions without having run anything.
// A stage the run skipped executed no body and carries none, which is the
// control that the record is per execution rather than per stage named.
func TestARunsRecordShowsHowLongEachStageTook(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var runID string
	var first machine.Run
	withService(t, h, func(running serviceUnderTest) {
		first = startRunSkipping(t, running.client, subject, pipeline.StageReview)
		holdingAt(t, first, holdingStage(t, 0))
		runID = first.Record.ID
	})
	homeIsFree(t, h)

	withService(t, h, func(running serviceUnderTest) {
		var run machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunGet, machine.RunRequest{Run: runID}, &run); err != nil {
			t.Fatalf("reading the run: %v", err)
		}
		executed := 0
		for _, stage := range pipeline.Order() {
			view := stageView(t, run, stage)
			before := stageView(t, first, stage)
			if len(view.Executions) != len(before.Executions) {
				t.Fatalf("the %s stage reports %d executions after a restart and %d before",
					stage, len(view.Executions), len(before.Executions))
			}
			switch {
			case stage == pipeline.StageReview && len(view.Executions) != 0:
				t.Fatalf("the skipped review stage reports executions: %+v", view.Executions)
			case view.Ran && stage != pipeline.StageReview && len(view.Executions) == 0:
				t.Fatalf("the %s stage ran and reports no execution", stage)
			}
			for _, e := range view.Executions {
				if e.Stage != stage.String() || e.Run != runID || e.Ended.Before(e.Started) || e.Started.IsZero() {
					t.Fatalf("the %s stage reports the execution %+v", stage, e)
				}
				executed++
			}
		}
		if executed == 0 {
			t.Fatal("no stage reports an execution, so the checks above read nothing")
		}
	})
}
