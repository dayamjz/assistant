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
	"github.com/dayamjz/assistant/internal/store"
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
	runsBefore := listedRuns(t, h, subject)

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
	t.Logf("assistant --json rerun, from a checkout one commit past the recorded head:\n%s", refused.stdout)

	human := run(t, h, subject, "rerun")
	if human.code != machine.ExitFailure {
		t.Fatalf("the human rendering exited %s:\n%s", human.code, human.stderr)
	}
	for _, want := range []string{machine.RerunMovedPhrase().String(), local, recorded} {
		if !strings.Contains(human.stderr, want) {
			t.Errorf("the person reads:\n%s\nwhich does not say %q", human.stderr, want)
		}
	}

	// It stayed a read: no run began, and the checkout is as it was.
	if after := listedRuns(t, h, subject); len(after) != len(runsBefore) {
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

// listedRuns is the runs the service has recorded for the working copy's
// repository.
func listedRuns(t *testing.T, h *home.Home, dir string) []store.Run {
	t.Helper()
	got := run(t, h, dir, "--json", "runs")
	if got.code != machine.ExitOK {
		t.Fatalf("listing runs exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var runs machine.Runs
	if err := json.Unmarshal([]byte(got.stdout), &runs); err != nil {
		t.Fatalf("the run listing does not decode: %v\n%s", err, got.stdout)
	}
	return runs.Runs
}
