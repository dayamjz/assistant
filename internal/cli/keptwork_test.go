package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
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

// PRD section 9's hold answer given a kept work entry, through the command
// line: --run=RUN names the run whose copy the entry is, --hold its key and
// --answer land. The answer starts a run of the entry's branch and reads out
// as the landed row, whose words are held to the vocabulary; the same
// answer again names the same run. Each malformed answer is incorrect
// usage before anything is asked: no owner, two owners, --run=RUN with no
// kept work key, bare --run beside a kept work key, a flag an answer to
// kept work does not read, and no answer. Carry on is refused by the
// service as a run's copy, which this build does not carry on, and leaves the entry open.
func TestKeptWorkIsAnsweredLandFromTheCommandLine(t *testing.T) {
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
		t.Fatalf("status lists kept work %+v, want one entry:\n%s", status.KeptWork, got.stdout)
	}
	entry := status.KeptWork[0]
	owner := "--run=" + entry.RunID.Or("")

	for _, args := range [][]string{
		{"--answer", "land", "--hold", entry.Key()},
		{"--task", "abcdefghijkl", owner, "--answer", "land", "--hold", entry.Key()},
		{owner, "--answer", "land"},
		{"--run", "--task", "abcdefghijkl", "--answer", "land", "--hold", entry.Key()},
		{owner, "--answer", "land", "--hold", entry.Key(), "--note", "why"},
		{owner, "--hold", entry.Key()},
	} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	if got := run(t, h, subject, owner, "--answer", "carry-on", "--hold", entry.Key()); got.code != machine.ExitFailure ||
		!strings.Contains(got.stdout+got.stderr, "not in this build") {
		t.Fatalf("carry on exited %s, want it refused as a run's copy:\n%s%s", got.code, got.stdout, got.stderr)
	}

	landed := run(t, h, subject, "--json", owner, "--answer", "land", "--hold", entry.Key())
	var answered machine.KeptWorkAnswered
	if landed.code != machine.ExitOK || json.Unmarshal([]byte(landed.stdout), &answered) != nil ||
		answered.Effect != machine.KeptWorkLanded || answered.Run == "" {
		t.Fatalf("landing exited %s with %+v:\n%s%s", landed.code, answered, landed.stdout, landed.stderr)
	}
	again := run(t, h, subject, owner, "--answer", "land", "--hold", entry.Key())
	line := machine.KeptWorkAnsweredPhrase(answered).String()
	if again.code != machine.ExitOK || !strings.Contains(again.stdout, line) {
		t.Fatalf("landing again exited %s, want the landed row:\n%s%s", again.code, again.stdout, again.stderr)
	}
	vocabtest.Assert(t, line)
}

// PRD section 9's discard through the command line: --answer discard alone
// answers with what would be lost and the digest, in the structured answer,
// and reads out as the shown row; given again with --confirm and that
// digest by a person at a terminal it removes the copy and reads out as the
// discarded row; the rows are held to the vocabulary. --confirm beside any
// other answer, or with no digest, is incorrect usage before anything is
// asked. A confirmation under --json, and one from a process not at a
// terminal, is refused with the row saying so by the surface itself, before
// anything reaches the service, which the refusal's text shows by carrying
// the row alone and no method's name: nothing is recorded and the copy
// stands. A confirmation given at a terminal is forwarded, and what then
// happens is the service's: it admits the confirmation from a peer in a
// terminal's foreground and refuses one that is not, which is this test
// process's own standing and not something the test decides, so the
// forwarded confirmation is held to one of those two answers, each carried
// as the service's; internal/service's own test drives both from processes
// it places. Without the surface's check the confirmation from nowhere is
// forwarded and comes back as the service's answer instead of the row
// alone.
func TestKeptWorkIsDiscardedFromTheCommandLine(t *testing.T) {
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
		t.Fatalf("status lists kept work %+v, want one entry:\n%s", status.KeptWork, got.stdout)
	}
	entry := status.KeptWork[0]
	owner := "--run=" + entry.RunID.Or("")

	for _, args := range [][]string{
		{owner, "--answer", "land", "--hold", entry.Key(), "--confirm", "abc"},
		{owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", ""},
		{"--confirm", "abc"},
	} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	shown := run(t, h, subject, "--json", owner, "--answer", "discard", "--hold", entry.Key())
	var answered machine.KeptWorkAnswered
	if shown.code != machine.ExitOK || json.Unmarshal([]byte(shown.stdout), &answered) != nil ||
		answered.Effect != machine.KeptWorkDiscardShown || answered.Loss == nil || answered.Loss.Digest == "" {
		t.Fatalf("the discard shown exited %s with %+v:\n%s%s", shown.code, answered, shown.stdout, shown.stderr)
	}
	read := run(t, h, subject, owner, "--answer", "discard", "--hold", entry.Key())
	shownRow := machine.KeptWorkAnsweredPhrase(answered).String()
	if read.code != machine.ExitOK || !strings.Contains(read.stdout, shownRow) {
		t.Fatalf("the discard shown reads out as:\n%s%s\nwant the row %q", read.stdout, read.stderr, shownRow)
	}
	vocabtest.Assert(t, shownRow)
	if _, err := os.Stat(entry.CopyPath); err != nil {
		t.Fatalf("showing the discard removed the copy: %v", err)
	}
	needsTerminal := machine.PhraseDiscardNeedsTerminal.String()
	vocabtest.Assert(t, needsTerminal)
	for _, refused := range []invocation{
		run(t, h, subject, "--json", owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", answered.Loss.Digest),
		run(t, h, subject, owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", answered.Loss.Digest),
		atTerminal(t, h, subject, "", "--json", owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", answered.Loss.Digest),
	} {
		said := refused.stdout + refused.stderr
		if refused.code != machine.ExitFailure || !strings.Contains(said, needsTerminal) || strings.Contains(said, string(ipc.MethodKeptWorkAnswer)) {
			t.Fatalf("a confirmation from no terminal, or under --json, exited %s:\n%s\nwant the row %q from the surface itself, naming no method",
				refused.code, said, needsTerminal)
		}
	}
	if _, err := os.Stat(entry.CopyPath); err != nil {
		t.Fatalf("a refused confirmation removed the copy: %v", err)
	}
	if still, err := openStore(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || !still.Open() {
		t.Fatalf("after the refused confirmations the entry reads %+v, %v; want it open", still, err)
	}

	done := atTerminal(t, h, subject, "", owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", answered.Loss.Digest)
	doneRow := machine.KeptWorkAnsweredPhrase(machine.KeptWorkAnswered{Effect: machine.KeptWorkDiscarded}).String()
	vocabtest.Assert(t, doneRow)
	switch said := done.stdout + done.stderr; {
	case done.code == machine.ExitOK && strings.Contains(done.stdout, doneRow):
		// This process is in a terminal's foreground, so the service
		// admitted the confirmation it was forwarded.
		if _, err := os.Lstat(entry.CopyPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the discarded copy still stands: %v", err)
		}
		if gone, err := openStore(t, h).KeptWorkEntry(t.Context(), entry.ID); err != nil || gone.Answer.Or("") != store.KeptDiscard || !gone.DiscardedAt.IsKnown() {
			t.Fatalf("after the discard the entry reads %+v, %v; want it answered discard and discarded", gone, err)
		}
		again := atTerminal(t, h, subject, "", owner, "--answer", "discard", "--hold", entry.Key(), "--confirm", answered.Loss.Digest)
		if again.code != machine.ExitOK || !strings.Contains(again.stdout, doneRow) {
			t.Fatalf("the discard again reads out as:\n%s%s\nwant the row %q", again.stdout, again.stderr, doneRow)
		}
	case done.code == machine.ExitFailure && strings.Contains(said, needsTerminal) && strings.Contains(said, string(ipc.MethodKeptWorkAnswer)):
		// This process is not in a terminal's foreground, so the service
		// refused the confirmation it was forwarded, with its own row.
		if _, err := os.Stat(entry.CopyPath); err != nil {
			t.Fatalf("the service's refusal removed the copy: %v", err)
		}
	default:
		t.Fatalf("the confirmation at a terminal exited %s:\n%s\nwant the service's answer either way, the row %q or the refusal %q from %s",
			done.code, said, doneRow, needsTerminal, ipc.MethodKeptWorkAnswer)
	}
}
