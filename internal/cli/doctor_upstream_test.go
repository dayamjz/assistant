package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
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

// Before init, an origin git cannot read is reported as a failed read, with
// what git said, and not as a checkout with no remote named origin: that
// sentence would send a person to add a remote they already have. The broken
// line names a credential in its section header, which git repeats in its
// error, so the detail is also held to carrying the error without it.
func TestDoctorSaysReadingOriginFailedRatherThanThatThereIsNoRemote(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	const secret = "s3cr3tt0ken"
	// A url.<base>.insteadOf with no value is a line git refuses only when it
	// reads remotes, so the checkout still opens and only the read of origin
	// fails.
	breakConfig(t, subject, "[url \"https://someone:"+secret+"@example.com/\"]\n\tinsteadOf\n")

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckUpstream)
	if !found {
		t.Fatalf("doctor made no upstream check: %+v", report.Checks)
	}
	if row.Kind != machine.UpstreamKindUnreadable {
		t.Fatalf("upstream check kind = %q, want %q; detail: %s", row.Kind, machine.UpstreamKindUnreadable, row.Detail)
	}
	if !strings.Contains(row.Detail, "insteadof") {
		t.Errorf("upstream detail does not carry what git said about the broken line:\n%s", row.Detail)
	}
	if strings.Contains(row.Detail, secret) {
		t.Errorf("upstream detail carries the credential git repeated:\n%s", row.Detail)
	}
	if !strings.Contains(row.Detail, redact.Marker) {
		t.Errorf("upstream detail does not show where the credential was removed:\n%s", row.Detail)
	}

	plain := run(t, h, subject, "doctor").stdout
	if !strings.Contains(plain, machine.CheckPhrase(row).String()) {
		t.Errorf("doctor does not say reading origin failed:\n%s", plain)
	}
	noRemote := machine.CheckPhrase(machine.Check{Name: machine.CheckUpstream, Kind: machine.UpstreamKindUnrecorded}).String()
	if strings.Contains(plain, noRemote) {
		t.Errorf("doctor says there is no remote named origin for an origin it failed to read:\n%s", plain)
	}
}

// Before init, a checkout with no remote named origin is still told so, and
// pointed at assistant init, rather than told its origin could not be read.
func TestDoctorSaysThereIsNoRemoteWhenOriginIsMissing(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "remote", "remove", "origin")

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckUpstream)
	if !found {
		t.Fatalf("doctor made no upstream check: %+v", report.Checks)
	}
	if row.Kind != machine.UpstreamKindUnrecorded {
		t.Fatalf("upstream check kind = %q, want %q; detail: %s", row.Kind, machine.UpstreamKindUnrecorded, row.Detail)
	}

	plain := run(t, h, subject, "doctor").stdout
	noRemote := machine.CheckPhrase(machine.Check{Name: machine.CheckUpstream, Kind: machine.UpstreamKindUnrecorded}).String()
	if !strings.Contains(plain, noRemote) {
		t.Errorf("doctor does not say there is no remote named origin:\n%s", plain)
	}
	unreadable := machine.CheckPhrase(machine.Check{Name: machine.CheckUpstream, Kind: machine.UpstreamKindUnreadable}).String()
	if strings.Contains(plain, unreadable) {
		t.Errorf("doctor says reading origin failed for a checkout with no origin:\n%s", plain)
	}
}

// breakConfig appends text to the subject's own git configuration file.
func breakConfig(t *testing.T, subject, text string) {
	t.Helper()
	path := git(t, subject, "rev-parse", "--git-path", "config")
	if !filepath.IsAbs(path) {
		path = filepath.Join(subject, path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening the subject's git configuration: %v", err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatalf("breaking the subject's git configuration: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing the subject's git configuration: %v", err)
	}
}
