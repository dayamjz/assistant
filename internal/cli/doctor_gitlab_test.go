package cli_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// doctorWith runs doctor --json with getenv as the command's environment, for
// the rows whose answer turns on a variable.
func doctorWith(t *testing.T, h *home.Home, subject string, getenv func(string) string) machine.Doctor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving this binary: %v", err)
	}
	var out, errs bytes.Buffer
	cli.Run(t.Context(), cli.Environment{
		Args:   []string{"--home", h.Root(), "--json", "doctor"},
		Stdout: &out, Stderr: &errs, Getenv: getenv,
		WorkingDir: subject, Executable: executable, Version: "assistant (test)",
	})
	return decodeDoctor(t, out.String())
}

// A checkout whose recorded upstream is on gitlab.com reads as one where a run
// opens a merge request, and the code-host row probes what a GitLab run needs,
// the access token, rather than gh: absent, the row is not OK, names the
// variable, and blocks nothing; present, the row is OK and says the service
// reads its own environment.
func TestDoctorProbesTheTokenForAGitLabUpstream(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	if got := run(t, h, subject, "init", "--upstream", "https://gitlab.com/group/assistant.git"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	report := doctorWith(t, h, subject, func(string) string { return "" })
	upstream, found := findCheck(report, machine.CheckUpstream)
	if !found || upstream.Kind != machine.UpstreamKindGitLab || !upstream.OK || !strings.Contains(upstream.Detail, "gitlab.com/group/assistant") {
		t.Fatalf("the upstream row is %+v, want gitlab.com/group/assistant read as GitLab", upstream)
	}
	if phrase := machine.CheckPhrase(upstream).String(); !strings.Contains(phrase, "merge request") {
		t.Fatalf("the upstream row reads %q, want it to say a merge request opens", phrase)
	}
	host, found := findCheck(report, machine.CheckCodeHost)
	if !found || host.OK || host.Blocking || !strings.Contains(host.Detail, forge.GitLabTokenVariable) {
		t.Fatalf("with no token the code-host row is %+v, want not OK, not blocking, naming %s", host, forge.GitLabTokenVariable)
	}

	report = doctorWith(t, h, subject, func(name string) string {
		if name == forge.GitLabTokenVariable {
			return "glpat-doctor"
		}
		return ""
	})
	host, _ = findCheck(report, machine.CheckCodeHost)
	if !host.OK || strings.Contains(host.Detail, "glpat-doctor") || !strings.Contains(host.Detail, "service") {
		t.Fatalf("with a token the code-host row is %+v, want OK, saying the service reads its own environment, and never the token", host)
	}
}
