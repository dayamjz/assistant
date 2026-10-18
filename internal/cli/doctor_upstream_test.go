package cli_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// Before init, doctor reads origin and says a local upstream cannot open a
// pull request rather than naming an unsupported host.
func TestDoctorBeforeInitReportsALocalUpstreamCannotOpenAPullRequest(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckUpstream)
	if !found {
		t.Fatalf("doctor made no upstream check: %+v", report.Checks)
	}
	if row.Kind != machine.UpstreamKindLocal {
		t.Fatalf("upstream check kind = %q, want %q", row.Kind, machine.UpstreamKindLocal)
	}
	phrase := machine.CheckPhrase(row).String()
	if !strings.Contains(phrase, "cannot open a pull request") {
		t.Fatalf("upstream row %q does not say a run cannot open a pull request", phrase)
	}
	if strings.Contains(phrase, "names a host this build does not address") {
		t.Fatalf("upstream row wrongly names an unsupported host:\n%s", phrase)
	}
}

// After init, a local upstream still reads as one where a run cannot open a
// pull request.
func TestDoctorAfterInitReportsALocalUpstreamCannotOpenAPullRequest(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckUpstream)
	if !found {
		t.Fatalf("doctor made no upstream check: %+v", report.Checks)
	}
	if row.Kind != machine.UpstreamKindLocal {
		t.Fatalf("upstream check kind = %q, want %q", row.Kind, machine.UpstreamKindLocal)
	}
	phrase := machine.CheckPhrase(row).String()
	if !strings.Contains(phrase, "cannot open a pull request") {
		t.Fatalf("upstream row %q does not say a run cannot open a pull request", phrase)
	}
}
