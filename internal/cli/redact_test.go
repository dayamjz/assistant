package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// A credential a reviewer writes into a finding reaches neither the run's
// stored position nor any answer the surface gives about the run: not the
// answer that started it, not run.get, and not --json status. The reviewer is
// the scripted stand-in answering over the wire, read by the real adapter and
// the real review body, so the finding takes the path a real reviewer's does.
//
// The control is the finding itself: its non-secret text, its action, and the
// marker internal/redact leaves where each credential stood all arrive in
// every one of those places, so an answer that dropped the finding, or a run
// that never reached the review, cannot pass as one that removed the
// credential.
func TestAFindingsCredentialsReachNeitherTheCheckpointNorAnyAnswer(t *testing.T) {
	requiresIdentifiedPeer(t)
	const (
		urlSecret    = "hunter2-url-password"
		bearerSecret = "s3cretBearerToken0123456789"
		visible      = "the deploy script prints its remote and its request headers"
	)
	planted := findings.Finding{
		Severity: findings.SeverityWarning,
		Action:   findings.ActionAsk,
		// A finding located in what the review read keeps the ask it
		// states; one reaching nothing the reviewer declared reading is
		// demoted to a note by the evidence binding, and would not hold.
		Location: findings.Location{Path: "a-change.txt"},
		Description: visible + ": https://deploy:" + urlSecret + "@example.com/o/r.git and " +
			"Authorization: Bearer " + bearerSecret,
	}
	reviewer := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.MatchReview(),
		Times: standin.Always,
		Reply: standin.Reviewed("one thing needs a decision", planted),
	}}}).Runner()

	h := newHome(t)
	subject := newSubject(t)
	serveCatalog(t, h, stages.All, agentCatalog(reviewer))
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")

	requireRedacted := func(t *testing.T, what, text string) {
		t.Helper()
		for _, secret := range []string{urlSecret, bearerSecret} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s carries the credential %q:\n%s", what, secret, text)
			}
		}
		for _, kept := range []string{visible, "https://" + redact.Marker + "@example.com", "Bearer " + redact.Marker} {
			if !strings.Contains(text, kept) {
				t.Fatalf("%s does not carry %q, so the finding did not arrive as redacted text:\n%s", what, kept, text)
			}
		}
	}

	started := run(t, h, subject, "--json", "--intent", "print less from the deploy script")
	if started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s%s", started.code, started.stdout, started.stderr)
	}
	first := decodeRun(t, started.stdout)
	if first.Decision == nil || first.Decision.Stage != pipeline.StageReview.String() {
		t.Fatalf("the run is not holding at the review stage for the planted finding: %+v", first.Decision)
	}
	var held bool
	for _, f := range first.Decision.Findings {
		held = held || (f.Holds() && strings.HasPrefix(f.Description, visible))
	}
	if !held {
		t.Fatalf("the decision does not relay the planted finding as an ask: %+v", first.Decision.Findings)
	}
	requireRedacted(t, "the answer that started the run", started.stdout)

	got := run(t, h, subject, "--json", "runs", first.Record.ID)
	if got.code != machine.ExitOK {
		t.Fatalf("reading the run exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	requireRedacted(t, "run.get", got.stdout)

	status := run(t, h, subject, "--json", "status")
	if status.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s%s", status.code, status.stdout, status.stderr)
	}
	var reported machine.Status
	if err := json.Unmarshal([]byte(status.stdout), &reported); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, status.stdout)
	}
	if reported.ActiveRun == nil || reported.ActiveRun.Record.ID != first.Record.ID {
		t.Fatalf("status reports %+v, want the run that was started", reported.ActiveRun)
	}
	requireRedacted(t, "--json status", status.stdout)

	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	history, err := records.GraphCheckpointHistory(t.Context(), first.Record.ID)
	if err != nil {
		t.Fatalf("reading the run's checkpoints: %v", err)
	}
	var reached bool
	for _, cp := range history {
		for _, secret := range []string{urlSecret, bearerSecret} {
			if strings.Contains(string(cp.Payload), secret) {
				t.Fatalf("checkpoint %d carries the credential %q", cp.Seq, secret)
			}
		}
		reached = reached || strings.Contains(string(cp.Payload), visible)
	}
	if !reached {
		t.Fatalf("none of the run's %d checkpoints carries the finding, so this read nothing", len(history))
	}
}
