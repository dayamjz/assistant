//go:build unix

package stages

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/pipeline"
)

// A command something else ended reports no exit status of its own, which is
// what the stage turns into a hold rather than into a verdict. Telling that
// apart from an exit is the whole of the difference between a check that
// answered and one that did not, and a command that exits cannot exercise it.
//
// It is here rather than beside the other command tests because ending a
// process this way is this platform's; internal/agents splits its own process
// tests the same way and for the same reason. What that leaves is that the
// other platform's answer to a signalled command is not checked anywhere.
func TestACommandEndedBySomethingElseReportsNoStatus(t *testing.T) {
	t.Parallel()
	var whole strings.Builder
	result := runCommand(t.Context(), commandSpec{
		command:    "kill -KILL $$",
		dir:        t.TempDir(),
		record:     &whole,
		projection: checkProjectionBytes,
	})
	if result.exited {
		t.Fatalf("a command ended by a signal reported exit status %d as its own", result.code)
	}

	record := openCheckEvidence(filepath.Join(t.TempDir(), "run-1", "test.log"), "test stage")
	record.close()
	report := testReport(pipeline.StageTest, "kill -KILL $$",
		configuredCheck{commit: "0123456789abcdef", record: record, result: result}).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a command ended by a signal produced %+v, which does not hold for a person", report)
	}
}
