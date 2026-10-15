package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// A rerun validates the head the branch's last run recorded, not the working
// copy's HEAD. When a person commits after a run ends, those are two commits,
// and a rerun that went ahead would re-vet the one they have moved on from.
// So it is refused, naming both commits and the two ways to validate the
// local one: a push to the gate, or a start with an intent.
//
// P1 is the consent boundary, and the half established here is that a rerun
// the person did not get stays a read: the refusal begins no run, and the
// working copy's HEAD, branches, index and files are as they were. P13 is the
// refusal's sentence, which is the phrase-table row with the commits and the
// commands beside it rather than in it.
//
// The positive control is the same branch with no local commit, where the
// rerun proceeds and validates the recorded head, so the refusal is about the
// commit and not about reruns in general.
func TestARerunIsRefusedWhenTheCheckoutMovedPastTheRecordedHead(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	const branch = "a-change"
	standingOnChange(t, subject, branch)
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated").stdout)
	if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
		t.Fatalf("ending the first run exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}
	recorded := first.Record.CurrentHead.Or(first.Record.SubmittedHead)

	// The control: the checkout is where the run left it, so the rerun
	// proceeds, and what it validates is the recorded head.
	if local := git(t, subject, "rev-parse", "HEAD"); local != recorded {
		t.Fatalf("before any local commit the checkout is at %s and the run recorded %s", local, recorded)
	}
	again := run(t, h, subject, "--json", "rerun")
	if again.code != machine.ExitOK {
		t.Fatalf("a rerun from the recorded head exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	rerun := decodeRun(t, again.stdout)
	if rerun.Record.ID == first.Record.ID || rerun.Record.SubmittedHead != recorded {
		t.Fatalf("the rerun is run %s of %s, want a fresh run of %s", rerun.Record.ID, rerun.Record.SubmittedHead, recorded)
	}
	if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
		t.Fatalf("ending the rerun exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}

	// A follow-up committed locally, and something left uncommitted beside
	// it, which a rerun that touched the working copy would disturb.
	if err := os.WriteFile(filepath.Join(subject, "follow-up.txt"), []byte("corrected locally\n"), 0o600); err != nil {
		t.Fatalf("writing the follow-up: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "a follow-up made after the run ended")
	if err := os.WriteFile(filepath.Join(subject, "uncommitted.txt"), []byte("not yet\n"), 0o600); err != nil {
		t.Fatalf("writing an uncommitted file: %v", err)
	}
	local := git(t, subject, "rev-parse", "HEAD")
	before := checkoutState(t, subject)
	runsBefore := listRuns(t, h, subject)

	refused := run(t, h, subject, "--json", "rerun")
	if refused.code != machine.ExitFailure {
		t.Fatalf("a rerun from a moved checkout exited %s, want the operational failure:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, refused.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeRefused, failure.Error)
	}
	for _, want := range []string{local, recorded, "git push " + gate.RemoteName + " " + branch, "assistant --intent"} {
		if !strings.Contains(failure.Error, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, failure.Error)
		}
	}
	if strings.Contains(failure.Error, "assistant sync") {
		t.Errorf("a checkout ahead of the recorded head was sent to sync:\n%s", failure.Error)
	}
	t.Logf("assistant --json rerun, from a checkout one commit past the recorded head:\n%s", refused.stdout)

	human := run(t, h, subject, "rerun")
	if human.code != machine.ExitFailure {
		t.Fatalf("the human rendering exited %s:\n%s", human.code, human.stderr)
	}
	for _, want := range []string{machine.RerunMoved{Reason: machine.RerunMovedElsewhere}.Phrase().String(), local, recorded} {
		if !strings.Contains(human.stderr, want) {
			t.Errorf("the person reads:\n%s\nwhich does not say %q", human.stderr, want)
		}
	}

	// It stayed a read: no run began, and the checkout is as it was.
	if after := listRuns(t, h, subject); len(after) != len(runsBefore) {
		t.Fatalf("the refused rerun recorded a run: %d runs before and %d after", len(runsBefore), len(after))
	}
	if after := checkoutState(t, subject); after != before {
		t.Fatalf("the refused rerun changed the checkout:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// checkoutState is what a refused rerun must leave alone in a working copy:
// where HEAD points and what it resolves to, every reference, and the index
// and files as git status sees them.
func checkoutState(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join([]string{
		git(t, dir, "symbolic-ref", "HEAD"),
		git(t, dir, "rev-parse", "HEAD"),
		git(t, dir, "for-each-ref"),
		git(t, dir, "status", "--porcelain=v1", "--untracked-files=all"),
	}, "\n")
}

// A checkout behind the commit a rerun would validate, where that commit is
// work the last run recorded past the one it was submitted, is the case
// assistant sync reconciles, so the refusal names sync rather than a push or
// a start: validating the older local commit would vet what the run already
// moved on from. The run's work is put where the real path puts it: a commit
// in the gate repository the run's copy is cut from, descended from the
// submitted commit, recorded as the run's head through the store's own
// accessor. Nothing in this build writes that head yet, which is the reading
// this case is about rather than a shape the record cannot hold.
//
// The control beside it is a checkout behind the submitted commit itself,
// which a reset produces: the head a rerun validates is then the submitted
// commit and not the run's work, so the refusal keeps the general remedy.
// Both are refusals that change nothing, on P1's terms.
func TestARerunBehindTheRunsWorkIsRefusedNamingSync(t *testing.T) {
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated").stdout)
	if ended := run(t, h, subject, "--json", "--cancel"); ended.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s%s", ended.code, ended.stdout, ended.stderr)
	}
	submitted := first.Record.SubmittedHead

	// The control: behind the submitted commit, which is what a rerun
	// validates while the run recorded no work of its own.
	git(t, subject, "reset", "--quiet", "--hard", "HEAD~1")
	parent := git(t, subject, "rev-parse", "HEAD")
	reset := refusedRerun(t, h, subject)
	for _, want := range []string{parent, submitted, "assistant --intent"} {
		if !strings.Contains(reset.Error, want) {
			t.Errorf("the refusal from behind the submitted commit does not say %q:\n%s", want, reset.Error)
		}
	}
	if strings.Contains(reset.Error, "assistant sync") {
		t.Errorf("a checkout behind the submitted commit was sent to sync:\n%s", reset.Error)
	}
	git(t, subject, "reset", "--quiet", "--hard", submitted)

	// The run's work: a commit past the submitted one in the gate repository,
	// recorded as the run's head.
	var status machine.Status
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil ||
		status.Gate == nil || status.Gate.ID == "" {
		t.Fatalf("status names no gate:\n%s", got.stdout)
	}
	repository, err := gate.RepositoryFor(h.Root(), status.Gate.ID)
	if err != nil {
		t.Fatalf("locating the gate repository: %v", err)
	}
	work := git(t, repository, "commit-tree", submitted+"^{tree}", "-p", submitted, "-m", "the run's work")
	git(t, repository, "update-ref", "refs/assistant/test/"+first.Record.ID, work)
	records := openStore(t, h)
	if err := records.SetRunHead(t.Context(), first.Record.ID, work); err != nil {
		t.Fatalf("recording the run's head: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	before := checkoutState(t, subject)
	runsBefore := listRuns(t, h, subject)
	behind := refusedRerun(t, h, subject)
	for _, want := range []string{submitted, work, "assistant sync", "assistant sync --confirm"} {
		if !strings.Contains(behind.Error, want) {
			t.Errorf("the refusal from behind the run's work does not say %q:\n%s", want, behind.Error)
		}
	}
	if strings.Contains(behind.Error, "assistant --intent") {
		t.Errorf("a checkout behind the run's work was told to validate its own commit:\n%s", behind.Error)
	}
	t.Logf("assistant --json rerun, from a checkout behind the run's work:\n%s", behind.Error)

	human := run(t, h, subject, "rerun")
	if want := (machine.RerunMoved{Reason: machine.RerunMovedBehind}).Phrase().String(); human.code != machine.ExitFailure ||
		!strings.Contains(human.stderr, want) {
		t.Fatalf("the person reads, exit %s:\n%s\nwhich does not say %q", human.code, human.stderr, want)
	}
	if after := listRuns(t, h, subject); len(after) != len(runsBefore) {
		t.Fatalf("the refused rerun recorded a run: %d runs before and %d after", len(runsBefore), len(after))
	}
	if after := checkoutState(t, subject); after != before {
		t.Fatalf("the refused rerun changed the checkout:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// refusedRerun runs assistant --json rerun, requires the refusal, and returns
// it as it crossed the wire.
func refusedRerun(t *testing.T, h *home.Home, subject string) machine.Failure {
	t.Helper()
	got := run(t, h, subject, "--json", "rerun")
	if got.code != machine.ExitFailure {
		t.Fatalf("the rerun exited %s, want the operational failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(ipc.CodeRefused) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeRefused, failure.Error)
	}
	return failure
}
