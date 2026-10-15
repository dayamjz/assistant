package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// heldAtThePush is every stage reporting and passing except the push, which
// holds on an ask. Standing consent never answers the push's decision, so a
// run under it stops there for a person, which is where a test reads what the
// surfaces say about a run under consent.
func heldAtThePush(stages.StageDeps) pipeline.Stages {
	all := pipeline.ConstantStages("nothing to report")
	all.Push = pipeline.Constant("the push needs a person", findings.Finding{
		ID: "push-decision", Severity: findings.SeverityWarning, Action: findings.ActionAsk,
		Description: "a decision only a person makes",
	})
	return all
}

// commitDocument commits document as the repository document on the branch
// the subject stands on.
func commitDocument(t *testing.T, subject, document string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(subject, config.RepositoryDocument), []byte(document), 0o600); err != nil {
		t.Fatalf("writing the repository document: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "the repository document")
}

// consentSubject is a subject with an assistant gate, whose default branch
// carries trusted as its repository document when it is not empty, standing
// on a change of its own.
func consentSubject(t *testing.T, h *home.Home, trusted, branch string) string {
	t.Helper()
	subject := newSubject(t)
	if trusted != "" {
		commitDocument(t, subject, trusted)
		git(t, subject, "push", "--quiet", "origin", "main")
	}
	mustOK(t, run(t, h, subject, "init"))
	standingOnChange(t, subject, branch)
	return subject
}

// The flag without the trusted key is incorrect usage: it exits 2, as an
// invalid request, names the key, and starts nothing. A branch whose own
// copy sets the key is refused the same way, because the key is trusted-only
// and a pushed branch cannot allow consent for itself.
func TestStandingConsentWithoutTheTrustedKeyIsIncorrectUsage(t *testing.T) {
	principles.Cite(t, principles.P7)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serveStages(t, h, heldAtThePush)
	subject := consentSubject(t, h, "", "no-key")

	refused := run(t, h, subject, "--json", "--standing-consent")
	if refused.code != machine.ExitUsage {
		t.Fatalf("the flag without the key exited %s, want incorrect usage:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not a failure document: %v\n%s", err, refused.stdout)
	}
	if failure.Code != string(ipc.CodeInvalidRequest) || !strings.Contains(failure.Error, string(config.KeyAllowStandingConsent)) {
		t.Fatalf("the refusal is %+v, want an invalid request naming %s", failure, config.KeyAllowStandingConsent)
	}
	if plain := run(t, h, subject, "--standing-consent"); plain.code != machine.ExitUsage {
		t.Fatalf("the flag without the key read out exited %s:\n%s", plain.code, plain.stderr)
	}

	commitDocument(t, subject, `{"allow_standing_consent": true}`)
	if pushed := run(t, h, subject, "--standing-consent"); pushed.code != machine.ExitUsage {
		t.Fatalf("a branch allowing consent in its own copy exited %s, want incorrect usage:\n%s%s",
			pushed.code, pushed.stdout, pushed.stderr)
	}

	var runs machine.Runs
	listed := mustOK(t, run(t, h, subject, "--json", "runs"))
	if err := json.Unmarshal([]byte(listed.stdout), &runs); err != nil {
		t.Fatalf("reading the run listing: %v\n%s", err, listed.stdout)
	}
	if len(runs.Runs) != 0 {
		t.Fatalf("a refused flag left %d runs", len(runs.Runs))
	}

	// The control: the same command without the flag starts a run.
	started := mustOK(t, run(t, h, subject))
	if strings.Contains(started.stdout, machine.PhraseStandingConsent.Outcome) {
		t.Fatalf("a run started without the flag reads as under standing consent:\n%s", started.stdout)
	}
}

// With the trusted key, the flag puts the run under standing consent and
// every surface a person reads says so: the run as the command answers it,
// status, and the run listing, each through the phrase table. The run stops at
// the push's decision, which consent never answers.
func TestEverySurfaceSaysARunRunsUnderStandingConsent(t *testing.T) {
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	serveStages(t, h, heldAtThePush)
	subject := consentSubject(t, h, `{"allow_standing_consent": true}`, "consenting")

	said := machine.PhraseStandingConsent.String()
	started := mustOK(t, run(t, h, subject, "--standing-consent"))
	if !strings.Contains(started.stdout, said) {
		t.Fatalf("the run does not say it runs under standing consent:\n%s", started.stdout)
	}
	if !strings.Contains(mustOK(t, run(t, h, subject, "status")).stdout, said) {
		t.Fatal("status does not say the run runs under standing consent")
	}
	if !strings.Contains(mustOK(t, run(t, h, subject, "runs")).stdout, said) {
		t.Fatal("the run listing does not say the run runs under standing consent")
	}
	var answer machine.Run
	structured := mustOK(t, run(t, h, subject, "--json", "--standing-consent"))
	if err := json.Unmarshal([]byte(structured.stdout), &answer); err != nil {
		t.Fatalf("reading the run: %v\n%s", err, structured.stdout)
	}
	if !answer.Record.StandingConsent.IsKnown() {
		t.Fatalf("the run record does not say the run runs under standing consent: %+v", answer.Record)
	}
	if answer.Decision == nil || answer.Decision.Stage != pipeline.StagePush.String() {
		t.Fatalf("the run stands at %+v, want the push's decision, which consent never answers", answer.Decision)
	}
}
