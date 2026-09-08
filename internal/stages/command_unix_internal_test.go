//go:build unix

package stages

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		projection: testProjectionBytes,
		grace:      commandGrace,
	})
	if result.exited {
		t.Fatalf("a command ended by a signal reported exit status %d as its own", result.code)
	}

	record := openTestEvidence(filepath.Join(t.TempDir(), "run-1", testEvidenceFile))
	record.close()
	report := testReport("kill -KILL $$", "0123456789abcdef", record, result).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a command ended by a signal produced %+v, which does not hold for a person", report)
	}
}

// A command that exits zero and leaves a descendant holding its output pipe is
// one os/exec answers twice about: the exit status is the command's, and
// ErrWaitDelay says the copy of its output was abandoned once the grace ran
// out. The record is then short by whatever was still in the pipe, and every
// write to it succeeded, so nothing the record can see would notice. The stage
// must therefore stop offering it as the full output.
//
// Without the guard the result carries no truncation fact at all, the report
// names the evidence file, and an operator is told a short file is whole.
//
// It is unix-only because leaving a background descendant holding a pipe is
// written here as shell job control, and no portable equivalent is available;
// what that leaves is that the other platform's answer to a cut-off record is
// not checked anywhere. The grace is short so the give-up path is reached in
// milliseconds rather than in the stage's five seconds, and the result comes
// from a real runCommand rather than being assembled by hand.
func TestOutputCutOffAfterAZeroExitIsNotOfferedAsTheFullOutput(t *testing.T) {
	t.Parallel()
	record := openTestEvidence(filepath.Join(t.TempDir(), "run-1", testEvidenceFile))
	if !record.recorded() {
		t.Fatalf("opening the record: %v", record.err)
	}
	const command = "sleep 30 & echo started"
	result := runCommand(t.Context(), commandSpec{
		command:    command,
		dir:        t.TempDir(),
		record:     record,
		projection: testProjectionBytes,
		grace:      50 * time.Millisecond,
	})
	if !result.exited || result.code != 0 {
		t.Fatalf("the command exited %d (reported a status: %t), and it exits zero: %v",
			result.code, result.exited, result.err)
	}
	if result.err != nil {
		t.Fatalf("a command that reported its own status also reported %v as a failure to obtain one",
			result.err)
	}
	if result.short == nil {
		t.Fatal("os/exec gave up copying the output and the result says nothing was cut off, " +
			"so a short record would be offered as whole")
	}
	if !errors.Is(result.short, exec.ErrWaitDelay) {
		t.Fatalf("the output was cut off by %v, and the grace expiring is what cut it off", result.short)
	}

	record.cutShort(result.short)
	record.close()
	if record.recorded() {
		t.Fatal("the record reports itself whole after the command's output was cut off")
	}

	report := testReport(command, "0123456789abcdef", record, result).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if len(report.Evidence) != 0 {
		t.Fatalf("the report offers %+v as the full output, and that file is short", report.Evidence)
	}
	if !report.AllNotes() || len(report.Findings) != 1 {
		t.Fatalf("a passing check with a cut-off record reported %+v, and it owes exactly the one note",
			report.Findings)
	}
	if !strings.Contains(report.Findings[0].Description, record.err.Error()) {
		t.Fatalf("the note does not say what went wrong: %s", report.Findings[0].Description)
	}
}
