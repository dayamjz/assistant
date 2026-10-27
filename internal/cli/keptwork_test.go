package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// PRD section 6's "your own push is never held by it: a run your push
// starts proceeds as it always does, and its answer names the kept work",
// through the command line and a real push. A cancelled run's copy is kept
// on a commit on no remote, so the project holds one open entry. A person
// then pushes another branch to the gate: the push starts its run, and the
// run's answer, read the way a person reads it, prints the line saying
// work kept in a local copy of the project is waiting on their decision,
// while the structured answer names the entry. That line is held to the
// vocabulary; the run's details beside it are an evidence document, which
// P13 lets name stages. Without the answer's read of the project's kept work
// neither appears.
func TestAPersonsPushRunsWhileKeptWorkWaitsAndItsAnswerNamesIt(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)

	standingOnChange(t, subject, "a-change")
	if started := run(t, h, subject, "--json", "--intent", "a change kept in its copy"); started.code != machine.ExitOK {
		t.Fatalf("starting a run exited %s:\n%s", started.code, started.stdout)
	}
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the run exited %s:\n%s", got.code, got.stdout)
	}
	var status machine.Status
	if got := run(t, h, subject, "--json", "status"); json.Unmarshal([]byte(got.stdout), &status) != nil || len(status.KeptWork) != 1 {
		t.Fatalf("status lists kept work %+v, want the cancelled run's one entry:\n%s", status.KeptWork, got.stdout)
	}
	entry := status.KeptWork[0]

	git(t, subject, "checkout", "--quiet", "main")
	git(t, subject, "branch", "pushed")
	commitOn(t, subject, "pushed", "pushed.txt", "a change a person pushed\n")
	git(t, subject, "push", "--quiet", gate.RemoteName, "pushed")
	var pushed string
	for _, r := range listRuns(t, h, subject) {
		if r.Branch == "pushed" {
			pushed = r.ID
		}
	}
	if pushed == "" {
		t.Fatalf("the push started no run while the project holds kept work; runs: %+v", listRuns(t, h, subject))
	}

	structured := run(t, h, subject, "--json", "runs", pushed)
	var answer machine.Run
	if err := json.Unmarshal([]byte(structured.stdout), &answer); err != nil {
		t.Fatalf("the run's answer does not decode: %v\n%s", err, structured.stdout)
	}
	if len(answer.KeptWork) != 1 || answer.KeptWork[0].ID != entry.ID {
		t.Fatalf("the pushed run's answer names kept work %+v, want entry %d", answer.KeptWork, entry.ID)
	}
	read := run(t, h, subject, "runs", pushed)
	line, _ := machine.RunKeptWorkPhrase(answer)
	if !strings.Contains(read.stdout, line.String()) {
		t.Fatalf("the pushed run as a person reads it does not name the kept work:\n%s", read.stdout)
	}
	vocabtest.Assert(t, line.String())
}
