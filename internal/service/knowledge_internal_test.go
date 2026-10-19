package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// knowledgeReadReply is what the knowledge stand-in prints once it has read
// the last line of its first turn, which is what a test waits on before it
// reads the record of every line.
const knowledgeReadReply = "the first turn was read"

// knowledgeWorker is a worker whose script answers the last line its first
// turn carries, last, so a test waits on that answer and then reads the
// record once.
func knowledgeWorker(t *testing.T, last string) *standin.Agent {
	t.Helper()
	return standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Greeting:  "worker ready",
		Exchanges: []standin.Exchange{{Contains: last, Reply: knowledgeReadReply}},
	}}, agents.WithTerminationGrace(2*time.Second))
}

// curate commits content as the knowledge document on the primary's main
// and pushes it to the upstream, so the default branch holds it, and
// returns the commit. With push false the commit stays in the primary and
// the copy cut from it, which is the untrusted copy.
func curate(t *testing.T, sub *dispatchSubject, content string, push bool) string {
	t.Helper()
	repository, err := sub.records.Repository(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	primary := repository.WorkingPath
	if err := os.WriteFile(filepath.Join(primary, knowledge.Document), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	heldGit(t, primary, "add", "-A")
	heldGit(t, primary, "commit", "--quiet", "-m", "curate the project knowledge")
	if push {
		heldGit(t, primary, "push", "--quiet", "origin", "main")
	}
	return heldGit(t, primary, "rev-parse", "HEAD")
}

// workerLines is every line the launched worker read, once it has answered
// the line the script waits on.
func workerLines(t *testing.T, sub *launchSubject) string {
	t.Helper()
	provider, launched, ok := sub.service.sessionOf(sub.task)
	if !ok {
		t.Fatal("the service holds no session for the task")
	}
	waitCaptured(t, provider, launched, knowledgeReadReply)
	for _, call := range sub.agent.Calls() {
		if call.Worker {
			return strings.Join(call.Lines, "\n")
		}
	}
	t.Fatal("the stand-in recorded no worker")
	return ""
}

// PRD principle P7 at the launch: the worker's first turn carries the
// curated knowledge document as the default branch holds it, redacted, and
// not the copy the primary and the worker's own clone hold, and the attempt
// records the revision it was told: the default branch's commit and the
// digest of the document's bytes there. The control is the worker's copy,
// which holds the untrusted text, so the launch had it to hand and told the
// trusted one.
func TestAWorkersFirstTurnCarriesTheTrustedKnowledgeAndTheAttemptNamesIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	ctx := t.Context()
	const secret = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	trusted := "TRUSTED-CONVENTION: the mirror is https://deploy:" + secret + "@example.com/r.git\n"
	sub := newLaunchSubject(t, knowledgeWorker(t, "----- end "+knowledge.Document))
	trustedAt := curate(t, sub.dispatchSubject, trusted, true)
	curate(t, sub.dispatchSubject, "PUSHED-CONVENTION: nobody merged this\n", false)

	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if held, err := os.ReadFile(filepath.Join(d.proof.Copy, knowledge.Document)); err != nil ||
		!strings.Contains(string(held), "PUSHED-CONVENTION") {
		t.Fatalf("the worker's copy holds %q (%v), want the untrusted text, or nothing here shows the launch passed it over",
			held, err)
	}
	if _, err := sub.service.launchTask(ctx, d); err != nil {
		t.Fatalf("launching: %v", err)
	}
	lines := workerLines(t, sub)
	if !strings.Contains(lines, "TRUSTED-CONVENTION") || strings.Contains(lines, "PUSHED-CONVENTION") {
		t.Fatalf("the worker read:\n%s\nwant the default branch's knowledge and not the copy's", lines)
	}
	if strings.Contains(lines, secret) || !strings.Contains(lines, redact.Marker) {
		t.Fatalf("the credential in the document reached the worker:\n%s", lines)
	}
	if !strings.Contains(lines, trustedAt) {
		t.Fatalf("the worker was not told which commit its knowledge came from:\n%s", lines)
	}

	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v (%v)", attempts, err)
	}
	sum := sha256.Sum256([]byte(trusted))
	if c, _ := attempts[0].KnowledgeCommit.Get(); c != trustedAt {
		t.Fatalf("the attempt names knowledge commit %v, want the default branch's %s", attempts[0].KnowledgeCommit, trustedAt)
	}
	if got, _ := attempts[0].KnowledgeDigest.Get(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("the attempt names knowledge digest %v, want the committed bytes' %s",
			attempts[0].KnowledgeDigest, hex.EncodeToString(sum[:]))
	}
}

// A default branch with no knowledge document launches the worker told
// none, and the attempt records the commit it found none at, which an
// attempt that never read anything does not.
func TestAMissingKnowledgeDocumentComposesNothingAndRefusesNothing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sub := newLaunchSubject(t, knowledgeWorker(t, "work"))
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if _, err := sub.service.launchTask(ctx, d); err != nil {
		t.Fatalf("launching with no knowledge document: %v", err)
	}
	if lines := workerLines(t, sub); strings.Contains(lines, "Project knowledge") {
		t.Fatalf("the worker was told knowledge nobody curated:\n%s", lines)
	}
	repository, err := sub.records.Repository(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	main := heldGit(t, repository.UpstreamURL, "rev-parse", "main")
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts %+v (%v)", attempts, err)
	}
	if c, _ := attempts[0].KnowledgeCommit.Get(); c != main || attempts[0].KnowledgeDigest.IsKnown() {
		t.Fatalf("the attempt names %v / %v, want the default branch's %s and no digest",
			attempts[0].KnowledgeCommit, attempts[0].KnowledgeDigest, main)
	}
}

// A knowledge document over the bound is refused at the launch rather than
// cut: knowledge-refused is recorded, the task resolves Failed from it, the
// attempt keeps no session, and no worker started.
func TestAKnowledgeDocumentOverTheBoundRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sub := newLaunchSubject(t, knowledgeWorker(t, "work"))
	curate(t, sub.dispatchSubject, strings.Repeat("a", knowledge.MaxBytes+1), true)
	d, err := sub.service.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	_, err = sub.service.launchTask(ctx, d)
	var refused *knowledgeRefused
	if !errors.As(err, &refused) || !errors.Is(err, knowledge.ErrTooLarge) {
		t.Fatalf("launching over an oversized document = %v, want a *knowledgeRefused over ErrTooLarge", err)
	}
	resolved, err := sub.service.tasks().Task(ctx, sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != taskstate.Failed || resolved.Basis.EventKind != knowledgeRefusedKind {
		t.Fatalf("the task resolves %+v, want Failed from %s", resolved, knowledgeRefusedKind)
	}
	attempts, err := sub.records.TaskAttempts(ctx, sub.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() || attempts[0].KnowledgeCommit.IsKnown() {
		t.Fatalf("attempts %+v (%v), want one with no session and no knowledge revision", attempts, err)
	}
	for _, call := range sub.agent.Calls() {
		if call.Worker {
			t.Fatal("a worker started over a refused knowledge document")
		}
	}
}
