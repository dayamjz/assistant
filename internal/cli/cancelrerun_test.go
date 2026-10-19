package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// --cancel=RUN ends the run it names from a directory that is not a working
// copy, and ends that run rather than the one the caller would otherwise
// reach: two branches each have a run held, and only the named one ends.
//
// The control for "from anywhere" is the bare --cancel from the same
// directory, which needs a working copy to find this branch's run and so
// fails there; what reads no working copy is the named form and not the flag.
// A run the home has no record of, and the run just ended, are refused as
// incorrect usage and change nothing, in the phrase-table row P13 asks for
// with the identifier beside it. A run written as the word after the flag is
// refused by name rather than read as an argument nobody takes.
func TestARunIsEndedByItsIdentifierFromAnywhere(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "named")
	named := decodeRun(t, run(t, h, subject, "--json", "--intent", "the run that is named").stdout)
	standingOnChange(t, subject, "kept")
	kept := decodeRun(t, run(t, h, subject, "--json", "--intent", "the run that is not").stdout)
	for _, r := range []machine.Run{named, kept} {
		if r.Record.Status == store.RunTerminated || r.Record.ID == "" {
			t.Fatalf("a run did not start and stay unfinished: %+v", r.Record)
		}
	}
	elsewhere := t.TempDir()

	// The control: the bare flag has to find this branch's run, and there is
	// no branch here.
	if bare := run(t, h, elsewhere, "--json", "--cancel"); bare.code == machine.ExitOK {
		t.Fatalf("--cancel alone outside a working copy exited %s, want a failure:\n%s", bare.code, bare.stdout)
	}

	// A run no record carries.
	const unknown = "0123456789abcdef"
	refused := run(t, h, elsewhere, "--json", "--cancel="+unknown)
	failure := cancelRefusal(t, refused)
	for _, want := range []string{machine.CancelRefused{Reason: machine.CancelUnknownRun}.Phrase().String(), unknown} {
		if !strings.Contains(failure.Error, want) {
			t.Errorf("the refusal of an unknown run does not say %q:\n%s", want, failure.Error)
		}
	}
	human := run(t, h, elsewhere, "--cancel="+unknown)
	if want := (machine.CancelRefused{Reason: machine.CancelUnknownRun}).Phrase().String(); human.code != machine.ExitUsage ||
		!strings.Contains(human.stderr, want) {
		t.Fatalf("the person reads, exit %s:\n%s\nwhich does not say %q", human.code, human.stderr, want)
	}

	// The run named, from a directory that is not a working copy.
	ended := run(t, h, elsewhere, "--json", "--cancel="+named.Record.ID)
	if ended.code != machine.ExitOK {
		t.Fatalf("ending run %s by name exited %s:\n%s%s", named.Record.ID, ended.code, ended.stdout, ended.stderr)
	}
	if got := decodeRun(t, ended.stdout); got.Record.ID != named.Record.ID || got.Record.Status != store.RunTerminated {
		t.Fatalf("ending run %s by name answered run %s as %s", named.Record.ID, got.Record.ID, got.Record.Status)
	}
	if got := readRun(t, h, elsewhere, kept.Record.ID); got.Record.Status == store.RunTerminated {
		t.Fatalf("ending run %s by name ended run %s too", named.Record.ID, kept.Record.ID)
	}

	// The same run again: it has ended, so there is nothing to end.
	again := cancelRefusal(t, run(t, h, elsewhere, "--json", "--cancel="+named.Record.ID))
	for _, want := range []string{machine.CancelRefused{Reason: machine.CancelRunEnded}.Phrase().String(),
		named.Record.ID, string(store.RunTerminated)} {
		if !strings.Contains(again.Error, want) {
			t.Errorf("the refusal of an ended run does not say %q:\n%s", want, again.Error)
		}
	}
	if got := readRun(t, h, elsewhere, kept.Record.ID); got.Record.Status == store.RunTerminated {
		t.Fatalf("a refused cancellation ended run %s", kept.Record.ID)
	}

	// The run written as the next word, and a run left empty.
	for _, args := range [][]string{{"--cancel", named.Record.ID}, {"--cancel="}} {
		got := run(t, h, elsewhere, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s", strings.Join(args, " "), got.code, got.stderr)
		}
	}
	if got := run(t, h, elsewhere, "--cancel", named.Record.ID); !strings.Contains(got.stderr, "--cancel="+named.Record.ID) {
		t.Errorf("a run written after --cancel is not refused by name:\n%s", got.stderr)
	}
}

// rerun --intent gives the fresh run the intent it names in place of the
// recorded one, and records it as supplied, so the intent stage reports it
// as acceptance criteria the way it reports any supplied intent. The first
// run's intent is offered rather than supplied, so a rerun that inherited its
// standing would be told apart.
//
// The control is the rerun without the flag, which inherits the recorded
// intent and its standing; both reruns validate the same commit, because a
// replaced intent changes nothing else. An intent that is only whitespace is
// refused as incorrect usage and records no run.
func TestARerunCanReplaceTheRecordedIntent(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	const offered = "a hint about what this change is for"
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", offered, "--intent-supplied=false").stdout)
	endBranchRun(t, h, subject)

	// The control: inherited, text and standing both.
	inherited := rerunOnce(t, h, subject)
	if inherited.Record.Intent != offered || inherited.Record.IntentSource != first.Record.IntentSource {
		t.Fatalf("a plain rerun recorded %q from %q, want %q from %q",
			inherited.Record.Intent, inherited.Record.IntentSource, offered, first.Record.IntentSource)
	}
	if id, _ := intentFinding(t, inherited); id != "intent-offered" {
		t.Fatalf("the intent stage of a plain rerun reported %q, want the offered intent", id)
	}
	endBranchRun(t, h, subject)

	// Replaced.
	const replacement = "acceptance criteria restated for the rerun"
	replaced := rerunOnce(t, h, subject, "--intent", replacement)
	if replaced.Record.Intent != replacement {
		t.Fatalf("the rerun recorded the intent %q, want %q", replaced.Record.Intent, replacement)
	}
	if replaced.Record.IntentSource == inherited.Record.IntentSource {
		t.Fatalf("the replaced intent kept the inherited standing %q", replaced.Record.IntentSource)
	}
	if replaced.Record.SubmittedHead != inherited.Record.SubmittedHead {
		t.Fatalf("the rerun with a replaced intent validated %s, and the plain one %s",
			replaced.Record.SubmittedHead, inherited.Record.SubmittedHead)
	}
	id, description := intentFinding(t, replaced)
	if id != "intent-supplied" || !strings.Contains(description, replacement) || strings.Contains(description, offered) {
		t.Fatalf("the intent stage of the rerun reported %q:\n%s\nwant the replacement as supplied intent", id, description)
	}
	endBranchRun(t, h, subject)

	before := listRuns(t, h, subject)
	for _, args := range [][]string{{"rerun", "--intent", "   "}, {"rerun", "--intent="}} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s", strings.Join(args, " "), got.code, got.stderr)
		}
	}
	if after := listRuns(t, h, subject); len(after) != len(before) {
		t.Fatalf("a refused rerun recorded a run: %d before and %d after", len(before), len(after))
	}
}

// cancelRefusal is a structured refusal of a cancellation, which exits as
// incorrect usage and crosses the wire as an invalid request.
func cancelRefusal(t *testing.T, got invocation) machine.Failure {
	t.Helper()
	if got.code != machine.ExitUsage {
		t.Fatalf("the refused cancellation exited %s, want incorrect usage:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(ipc.CodeInvalidRequest) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeInvalidRequest, failure.Error)
	}
	return failure
}

// readRun reads one run in full through the surface.
func readRun(t *testing.T, h *home.Home, dir, id string) machine.Run {
	t.Helper()
	got := run(t, h, dir, "--json", "runs", id)
	if got.code != machine.ExitOK {
		t.Fatalf("reading run %s exited %s:\n%s%s", id, got.code, got.stdout, got.stderr)
	}
	return decodeRun(t, got.stdout)
}

// endBranchRun ends the working copy's branch's run.
func endBranchRun(t *testing.T, h *home.Home, subject string) {
	t.Helper()
	if got := run(t, h, subject, "--json", "--cancel"); got.code != machine.ExitOK {
		t.Fatalf("ending the branch's run exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}

// rerunOnce reruns the working copy's branch with args and returns the fresh
// run.
func rerunOnce(t *testing.T, h *home.Home, subject string, args ...string) machine.Run {
	t.Helper()
	got := run(t, h, subject, append([]string{"--json", "rerun"}, args...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("assistant rerun %s exited %s:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
	}
	return decodeRun(t, got.stdout)
}

// intentFinding is the one finding the intent stage reported for r: its
// identifier and its description.
func intentFinding(t *testing.T, r machine.Run) (string, string) {
	t.Helper()
	for _, stage := range r.Stages {
		if stage.Stage != pipeline.StageIntent.String() {
			continue
		}
		if stage.Report == nil || len(stage.Report.Findings) != 1 {
			t.Fatalf("the intent stage of run %s reported %+v, want one finding", r.Record.ID, stage.Report)
		}
		return stage.Report.Findings[0].ID, stage.Report.Findings[0].Description
	}
	t.Fatalf("run %s reports no intent stage: %+v", r.Record.ID, r.Stages)
	return "", ""
}
