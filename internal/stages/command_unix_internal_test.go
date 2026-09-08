//go:build unix

package stages

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
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

	record := openTestEvidence(scratchEvidencePath(t))
	record.close()
	report := testReport("kill -KILL $$", "0123456789abcdef", record, result).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a command ended by a signal produced %+v, which does not hold for a person", report)
	}
}

// A command that leaves a descendant holding its output pipe is one whose
// output never reaches its end: the command has gone, the pipe is still open,
// and whatever is still unread stays unread. The read is given the grace and
// then abandoned, and the result says so - whatever exit status the command
// reported.
//
// Both exit statuses are run because the guard this replaces covered only one
// of them. It inferred truncation from os/exec's error, and os/exec answers an
// ExitError on a non-zero exit and discards the wait-delay error behind it, so
// the branch that produces the fix finding - the one that hands an operator
// and a fixer agent a path to the output - was exactly the branch where the
// old guard could not fire. Reading the pipe here makes the fact the same on
// both.
//
// It is unix-only because leaving a background descendant holding a pipe is
// written here as shell job control and no portable equivalent is available;
// what that leaves is that the other platform's answer to an abandoned read is
// not checked anywhere. The grace is short so the give-up path is reached in
// milliseconds rather than in the stage's five seconds, and every value comes
// from a real runCommand call rather than being assembled by hand.
func TestOutputStillHeldAfterTheCommandEndsIsNotOfferedAsWhole(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		command string
		code    int
	}{
		{"exiting zero", "sleep 30 & echo started", 0},
		{"exiting non-zero", "sleep 30 & echo started; exit 3", 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			record := openTestEvidence(scratchEvidencePath(t))
			if !record.recorded() {
				t.Fatalf("opening the record: %v", record.err)
			}
			result := runCommand(t.Context(), commandSpec{
				command:    c.command,
				dir:        t.TempDir(),
				record:     record,
				projection: testProjectionBytes,
				grace:      50 * time.Millisecond,
			})
			if !result.exited || result.code != c.code {
				t.Fatalf("the command exited %d (reported a status: %t), and it exits %d: %v",
					result.code, result.exited, c.code, result.err)
			}
			if result.err != nil {
				t.Fatalf("a command that reported its own status also reported %v as a failure to "+
					"obtain one", result.err)
			}
			if result.short == nil {
				t.Fatal("the read of the output was abandoned and the result says it was whole, " +
					"so a short record would be offered as the full output")
			}
			if !errors.Is(result.short, errOutputAbandoned) {
				t.Fatalf("the output was cut off by %v, and giving up on the read is what cut it off",
					result.short)
			}

			record.cutShort(result.short)
			record.close()
			if record.recorded() {
				t.Fatal("the record reports itself whole after the read of the output was abandoned")
			}

			report := testReport(c.command, "0123456789abcdef", record, result).Normalize()
			if err := report.Validate(); err != nil {
				t.Fatalf("the report is one the pipeline refuses: %v", err)
			}
			if len(report.Evidence) != 0 {
				t.Fatalf("the report offers %+v as the output, and that file is short", report.Evidence)
			}
			var said int
			offered := "full output: " + record.path
			for _, f := range report.Findings {
				if strings.Contains(f.Description, offered) {
					t.Fatalf("a finding offers %s as the full output, and that file is short:\n%s",
						record.path, f.Description)
				}
				if f.Action == findings.ActionNote && strings.Contains(f.Description, record.err.Error()) {
					said++
				}
			}
			if said != 1 {
				t.Fatalf("%d findings say the record is short, and the report owes exactly one: %+v",
					said, report.Findings)
			}
		})
	}
}

// A command whose output ends before the grace does is not reported as short,
// and the record holds all of it. Without this the truncation fact could be set
// on every run and every guard above would still pass.
func TestOutputThatReachesItsEndIsReportedWhole(t *testing.T) {
	t.Parallel()
	var whole strings.Builder
	result := runCommand(t.Context(), commandSpec{
		command:    "echo first; echo second",
		dir:        t.TempDir(),
		record:     &whole,
		projection: testProjectionBytes,
		grace:      50 * time.Millisecond,
	})
	if !result.exited || result.code != 0 {
		t.Fatalf("the command exited %d (reported a status: %t): %v", result.code, result.exited, result.err)
	}
	if result.short != nil {
		t.Fatalf("a command whose output ended is reported short: %v", result.short)
	}
	if whole.String() != "first\nsecond\n" {
		t.Fatalf("the record holds %q, and the command wrote %q", whole.String(), "first\nsecond\n")
	}
}
