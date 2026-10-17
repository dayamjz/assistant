package cli_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// TestDoctorSaysUpFrontWhetherARunCanOpenAPullRequest is issue #283's owner
// decision at the doctor: a repository whose upstream is on this machine has
// no code host, and doctor says runs there cannot open a pull request without
// stopping a run from starting; an upstream on a host this build cannot
// address is said too, and blocks, because such a run is refused before it
// starts; and the control, an upstream on the code host this build addresses,
// is reported able to. All three are read off the repository record init
// wrote, through the one reading internal/service's begin makes, and each is
// rendered through internal/machine's phrase table.
func TestDoctorSaysUpFrontWhetherARunCanOpenAPullRequest(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	local := pullRequestRow(t, h, subject)
	if local.OK || local.Blocking {
		t.Fatalf("a local upstream reads OK %v, blocking %v; want not OK and not blocking", local.OK, local.Blocking)
	}
	if !strings.Contains(local.Detail, "no code host") {
		t.Fatalf("the row for a local upstream says %q", local.Detail)
	}
	said := run(t, h, subject, "doctor").stdout
	if phrase := machine.CheckPhrase(local).String(); !strings.Contains(said, phrase) ||
		!strings.Contains(phrase, "cannot open a pull request") {
		t.Fatalf("doctor's rendering does not say runs here cannot open a pull request:\n%s", said)
	}
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if strings.Contains(report.Detail, machine.CheckPullRequests) {
		t.Fatalf("a repository with no code host is named among what stops a run: %q", report.Detail)
	}

	if got := run(t, h, subject, "init", "--upstream", "https://gitlab.example.invalid/owner/name.git"); got.code != machine.ExitOK {
		t.Fatalf("init with another host's upstream exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	elsewhere := pullRequestRow(t, h, subject)
	if elsewhere.OK || !elsewhere.Blocking {
		t.Fatalf("an upstream on another host reads OK %v, blocking %v; want not OK and blocking", elsewhere.OK, elsewhere.Blocking)
	}
	report = decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if report.CanStartRun || !strings.Contains(report.Detail, machine.CheckPullRequests) {
		t.Fatalf("doctor says a run can start %v (%q) with an upstream every run is refused over", report.CanStartRun, report.Detail)
	}
	if machine.CheckPhrase(elsewhere) == machine.CheckPhrase(local) {
		t.Fatal("an upstream on another host reads the same as one with no code host")
	}

	if got := run(t, h, subject, "init", "--upstream", "https://github.com/owner/name.git"); got.code != machine.ExitOK {
		t.Fatalf("init with a code host upstream exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if hosted := pullRequestRow(t, h, subject); !hosted.OK || hosted.Blocking || !strings.Contains(hosted.Detail, "owner/name") {
		t.Fatalf("an upstream on the code host reads %+v; want OK naming the repository", hosted)
	}
}

func pullRequestRow(t *testing.T, h *home.Home, subject string) machine.Check {
	t.Helper()
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, found := findCheck(report, machine.CheckPullRequests)
	if !found {
		t.Fatalf("doctor makes no %q check:\n%+v", machine.CheckPullRequests, report.Checks)
	}
	return row
}
