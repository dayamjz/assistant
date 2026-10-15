package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// identityRemedies are what the failing commit identity row has to name,
// written out here rather than read from internal/machine, so a row that
// dropped one fails this test rather than agreeing with itself.
var identityRemedies = []string{"git config --global user.name", "git config --global user.email"}

// doctorIdentity runs doctor with --json and returns its commit identity row,
// failing when doctor made no such check.
func doctorIdentity(t *testing.T, subject string, args ...string) machine.Check {
	t.Helper()
	report := decodeDoctor(t, runArgs(t, subject, append(args, "--json", "doctor")...).stdout)
	row, found := findCheck(report, machine.CheckCommitIdentity)
	if !found {
		t.Fatalf("doctor made no %q check on a gated checkout: %+v", machine.CheckCommitIdentity, report.Checks)
	}
	return row
}

// doctor reports whether git names an identity for a commit made in the
// checkout's gate, which is where a run's copy takes its identity from, and
// names the remedy when it does not. The configuration git reads is pointed
// at a file this test writes, first holding an identity, which is the
// control, and then empty. The control comes first so that the failing half
// is answering the one difference between the two: whether the file names
// anyone. Where git still settles an identity with nothing configured, by
// detecting one from the host, the test says so and skips the failing half
// rather than passing on a state it did not build. It is serial, because the
// configuration git reads is process state every command run here inherits.
func TestDoctorReportsAMissingCommitIdentityAndItsRemedy(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Doctor Test\n\temail = doctor@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	home := []string{"--home", h.Root()}

	row := doctorIdentity(t, subject, home...)
	if !row.OK || !strings.Contains(row.Detail, "Doctor Test <doctor@example.invalid>") {
		t.Fatalf("with an identity configured the row is %+v, want it to pass and name the identity", row)
	}
	if row.Blocking {
		t.Fatal("the commit identity row blocks, and a run can start without an identity")
	}

	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("git", "var", "GIT_COMMITTER_IDENT")
	probe.Dir = t.TempDir()
	if out, err := probe.CombinedOutput(); err == nil {
		t.Skipf("git settles an identity on this host with none configured (%s), so the failing half is not reachable here",
			strings.TrimSpace(string(out)))
	}

	row = doctorIdentity(t, subject, home...)
	if row.OK {
		t.Fatalf("with no identity configured the row passes: %+v", row)
	}
	if row.Blocking {
		t.Fatal("the failing commit identity row blocks, and a run can start without an identity")
	}
	for _, remedy := range identityRemedies {
		if !strings.Contains(row.Detail, remedy) {
			t.Errorf("the structured row's detail %q does not name %q", row.Detail, remedy)
		}
	}

	plain := run(t, h, subject, "doctor")
	failing := machine.CheckPhrase(machine.Check{Name: machine.CheckCommitIdentity}).String()
	if !strings.Contains(plain.stdout+plain.stderr, failing) {
		t.Errorf("the human rendering does not carry the failing commit identity row %q:\n%s%s", failing, plain.stdout, plain.stderr)
	}
	for _, remedy := range identityRemedies {
		if !strings.Contains(failing, remedy) {
			t.Errorf("the failing row %q does not name %q", failing, remedy)
		}
	}
}
