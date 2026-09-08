package stages

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
)

// scratchEvidencePath is where an internal test's record goes: a real
// home.EvidenceLog under a temporary root, so a test exercises the path the
// stage actually writes rather than respelling its leaf here.
func scratchEvidencePath(t *testing.T) string {
	t.Helper()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a home at a temporary root: %v", err)
	}
	return h.EvidenceLog("run-1", "test")
}

// A projection bounded below what a command printed keeps the end of the
// output, says how many bytes of the earlier output it left out, and starts at
// a line boundary rather than partway through one.
//
// The limit here is small so a short, portable command overruns it. It is the
// same parameter the stage passes testProjectionBytes in, so what differs from
// a run is the number and not the mechanism.
func TestAProjectionKeepsTheEndAndSaysWhatItLeftOut(t *testing.T) {
	t.Parallel()
	w := &tailWriter{limit: 12}
	for _, line := range []string{"first line\n", "second line\n", "third\n"} {
		if n, err := w.Write([]byte(line)); n != len(line) || err != nil {
			t.Fatalf("writing %q reported %d, %v, and this writer consumes everything and never fails", line, n, err)
		}
	}
	text, omitted := w.projection()
	if text != "third" {
		t.Fatalf("the projection is %q, and the last thing written that fits a whole line is %q", text, "third")
	}
	whole := len("first line\nsecond line\nthird\n")
	if int(omitted) != whole-len("third\n") {
		t.Fatalf("the projection says it left out %d bytes, and it left out %d of %d written",
			omitted, whole-len("third\n"), whole)
	}
	again, againOmitted := w.projection()
	if again != text || againOmitted != omitted {
		t.Fatalf("asking twice answered %q/%d then %q/%d", text, omitted, again, againOmitted)
	}
}

// Nothing is reported as omitted when nothing was, so a marker never appears
// over output that is whole.
func TestAProjectionThatFitsLeavesNothingOut(t *testing.T) {
	t.Parallel()
	w := &tailWriter{limit: 64}
	if _, err := w.Write([]byte("all of it\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	text, omitted := w.projection()
	if text != "all of it" || omitted != 0 {
		t.Fatalf("a projection that fits answered %q, leaving out %d bytes", text, omitted)
	}
}

// A single write larger than the limit is bounded the same way, which is the
// case a command printing one long line produces and the one an append-then-
// trim writer gets wrong.
func TestOneWriteLargerThanTheLimitIsBoundedToo(t *testing.T) {
	t.Parallel()
	w := &tailWriter{limit: 4}
	long := strings.Repeat("x", 100) + "\nend"
	if _, err := w.Write([]byte(long)); err != nil {
		t.Fatalf("writing: %v", err)
	}
	text, omitted := w.projection()
	if text != "end" {
		t.Fatalf("the projection is %q, and the end of the write is %q", text, "end")
	}
	if int(omitted) != len(long)-len("end") {
		t.Fatalf("the projection says it left out %d bytes of %d written", omitted, len(long))
	}
}

// A tail holding no whole line keeps its fragment rather than answering with
// nothing. Trimming to the next line boundary is what keeps a projection from
// starting mid-word, and applying it here would leave a reader of a
// single-line command with an empty extract and a byte count.
func TestATailWithNoWholeLineKeepsItsFragment(t *testing.T) {
	t.Parallel()
	w := &tailWriter{limit: 5}
	if _, err := w.Write([]byte("one long line with no break until here\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}
	text, omitted := w.projection()
	if text != "here" {
		t.Fatalf("the projection is %q, and the end of the one line written is %q", text, "here")
	}
	if omitted == 0 {
		t.Fatalf("the projection kept %q and says it left nothing out", text)
	}
}

// The record keeps the whole output whatever the projection keeps, so what
// travels in the report is a bounded view of a record that is not itself
// bounded.
func TestTheRecordHoldsTheWholeOutputTheProjectionBounds(t *testing.T) {
	t.Parallel()
	var whole strings.Builder
	result := runCommand(t.Context(), commandSpec{
		command:    "git --version",
		dir:        t.TempDir(),
		record:     &whole,
		projection: 4,
		grace:      commandGrace,
	})
	if !result.exited || result.code != 0 {
		t.Fatalf("git --version exited %d (reported a status: %t), err %v", result.code, result.exited, result.err)
	}
	if !strings.Contains(whole.String(), "git version") {
		t.Fatalf("the record does not hold what the command printed: %q", whole.String())
	}
	if len(result.tail) > 4 {
		t.Fatalf("the projection is %q, which is longer than the %d bytes asked for", result.tail, 4)
	}
	if int(result.omitted) != len(strings.TrimRight(whole.String(), "\n"))-len(result.tail) {
		t.Fatalf("the projection says it left out %d bytes of the %d recorded",
			result.omitted, len(whole.String()))
	}
}

// A record that starts failing partway through does not cut the command's
// output short and does not change what the command answered. The record and
// the projection are written to through one io.MultiWriter, which stops at the
// first writer that fails, so an evidence file that reported its failures
// would end the command's run with an error and turn a full disk into a
// verdict.
//
// The failure is arranged by handing it a file that is already closed, which
// is the state a write to a file that has gone wrong is in. What is being
// checked is what the rest of the stage does when a write fails, and a closed
// descriptor is a write that fails.
func TestARecordThatFailsPartwayDoesNotDecideTheVerdict(t *testing.T) {
	t.Parallel()
	record := openTestEvidence(scratchEvidencePath(t))
	if !record.recorded() {
		t.Fatalf("opening the record: %v", record.err)
	}
	if err := record.file.Close(); err != nil {
		t.Fatalf("closing the record behind it: %v", err)
	}

	result := runCommand(t.Context(), commandSpec{
		command:    "git --version",
		dir:        t.TempDir(),
		record:     record,
		projection: testProjectionBytes,
		grace:      commandGrace,
	})
	if !result.exited || result.code != 0 {
		t.Fatalf("a record that could not be written changed what the command answered: status %d, "+
			"reported a status: %t, err %v", result.code, result.exited, result.err)
	}
	if !strings.Contains(result.tail, "git version") {
		t.Fatalf("a record that could not be written cut the command's output short: %q", result.tail)
	}
	if record.recorded() {
		t.Fatal("the record reports itself whole after every write to it failed")
	}

	report := testReport("git --version", "0123456789abcdef", record, result).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if len(report.Evidence) != 0 {
		t.Fatalf("the report names evidence at %+v, and nothing was recorded", report.Evidence)
	}
	if !report.AllNotes() || len(report.Findings) != 1 {
		t.Fatalf("a passing check with no record reported %+v, and it owes exactly the one note",
			report.Findings)
	}
}

// What travels in a finding is a bounded view of the output, and PRD section 8
// requires it to say what was omitted and how to read the rest. So a finding
// over a bounded projection carries the count and the path of the record
// holding the whole of it.
//
// Where there is no record, it says that instead of naming a file, because a
// pointer to output nothing wrote sends a reader to an empty hand.
func TestABoundedProjectionSaysWhatItOmittedAndWhereTheRestIs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		recorded bool
	}{
		{"with a record", true},
		{"with none", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			record := openTestEvidence(scratchEvidencePath(t))
			if !c.recorded {
				if err := record.file.Close(); err != nil {
					t.Fatalf("closing the record behind it: %v", err)
				}
			}
			result := runCommand(t.Context(), commandSpec{
				command:    "git --version && exit 3",
				dir:        t.TempDir(),
				record:     record,
				projection: 4,
				grace:      commandGrace,
			})
			record.close()
			if !result.exited || result.code != 3 {
				t.Fatalf("the command exited %d (reported a status: %t), and 3 is what it exits",
					result.code, result.exited)
			}
			if result.omitted == 0 {
				t.Fatalf("nothing was omitted from a projection of 4 bytes over %q, so this checks "+
					"nothing", result.tail)
			}

			report := testReport("git --version && exit 3", "0123456789abcdef", record, result).Normalize()
			if err := report.Validate(); err != nil {
				t.Fatalf("the report is one the pipeline refuses: %v", err)
			}
			said := findings.Fixable(report.Findings)
			if len(said) != 1 {
				t.Fatalf("a failing check reported %+v, and one failure is one fix finding", report.Findings)
			}
			description := said[0].Description
			if !strings.Contains(description, strconv.FormatInt(result.omitted, 10)) {
				t.Fatalf("the finding does not say how much output it left out:\n%s", description)
			}
			if got := strings.Contains(description, record.path); got != c.recorded {
				t.Fatalf("the finding names the record %s: %t, and there is a record: %t\n%s",
					record.path, got, c.recorded, description)
			}
		})
	}
}

// A command that could not be started reports no exit status of its own, which
// is a different thing to report than a status and is what the stage turns
// into a hold rather than a verdict.
//
// It is reached by asking for a directory that is not there, which is a real
// failure to start rather than a result written by hand.
func TestACommandThatCannotStartReportsNoStatus(t *testing.T) {
	t.Parallel()
	var whole strings.Builder
	result := runCommand(t.Context(), commandSpec{
		command:    "git --version",
		dir:        filepath.Join(t.TempDir(), "not-there"),
		record:     &whole,
		projection: testProjectionBytes,
		grace:      commandGrace,
	})
	if result.exited {
		t.Fatalf("a command that could not start reported exit status %d", result.code)
	}
	if result.code != -1 {
		t.Fatalf("a command that reported no status carries code %d, and -1 is what says there was none", result.code)
	}
	if result.err == nil {
		t.Fatal("a command that could not start reported no reason, so nothing can say why")
	}
}

// A command ended before it could report a status is reported the same way,
// and cancelling the context is how a run ends one.
func TestACancelledCommandReportsNoStatus(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var whole strings.Builder
	result := runCommand(ctx, commandSpec{
		command:    "git --version",
		dir:        t.TempDir(),
		record:     &whole,
		projection: testProjectionBytes,
		grace:      commandGrace,
	})
	if result.exited {
		t.Fatalf("a command run under a cancelled context reported exit status %d", result.code)
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("a command run under a cancelled context reported %v, and the run was cancelled", result.err)
	}
}

// A command that reported no status becomes an ask, which holds the stage for
// a person: nothing was established either way, and PRD section 5 calls that a
// stage that could not gather enough evidence. It also claims to have checked
// nothing, because findings.Report.Tested is what the stage actually checked
// and a command that never ran checked nothing.
//
// The result it is given comes from runCommand rather than from this test, so
// the shape it classifies is one the mechanism produces.
func TestACommandWithNoStatusHoldsTheStageForAPerson(t *testing.T) {
	t.Parallel()
	var whole strings.Builder
	result := runCommand(t.Context(), commandSpec{
		command:    "git --version",
		dir:        filepath.Join(t.TempDir(), "not-there"),
		record:     &whole,
		projection: testProjectionBytes,
		grace:      commandGrace,
	})

	record := openTestEvidence(scratchEvidencePath(t))
	record.close()
	report := testReport("git --version", "0123456789abcdef", record, result).Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the report is one the pipeline refuses: %v", err)
	}
	if !report.HasHeld() {
		t.Fatalf("a command that reported no status produced %+v, which does not hold for a person", report)
	}
	if len(report.Tested) != 0 {
		t.Fatalf("the report says it checked %v, and the command never reported a status of its own",
			report.Tested)
	}
	if len(report.Fixable()) != 0 {
		t.Fatalf("a command that reported no status offered a finding to a fixer, and there is nothing to fix")
	}
	held := findings.Held(report.Findings)
	if len(held) != 1 || !strings.Contains(held[0].Description, result.err.Error()) {
		t.Fatalf("the hold does not say what happened instead: %+v", held)
	}
}
