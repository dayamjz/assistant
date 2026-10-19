package cli_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// TestInitDoctorAndEjectSpeakInPhrasesThroughTheCommandLine is P13 for the
// three local verbs as a person drives them through cli.Run: init, doctor and
// eject without --json print internal/machine's rows and the person's own
// branch name, and none of the identifiers and paths the same verbs carry
// through --json. The derived forbidden set catches the sample home and
// identifier a fixture plants but not this test's own, so every identifier
// and detail the structured answer carried is held absent by value as well,
// and each is checked to be non-empty first so its absence discriminates.
func TestInitDoctorAndEjectSpeakInPhrasesThroughTheCommandLine(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)

	created := run(t, h, subject, "init")
	if created.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", created.code, created.stdout, created.stderr)
	}
	var initialized machine.Init
	if err := json.Unmarshal([]byte(run(t, h, subject, "--json", "init").stdout), &initialized); err != nil {
		t.Fatalf("assistant --json init does not decode: %v", err)
	}
	carried := map[string]string{
		"the gate's identifier": initialized.Gate.ID,
		"the gate's path":       initialized.Gate.Repository,
		"the working copy":      initialized.Repository.WorkingPath,
		"the home":              h.Root(),
	}
	if !strings.Contains(created.stdout, machine.InitPhrase(machine.Init{}).String()) {
		t.Errorf("init does not say the gate was created:\n%s", created.stdout)
	}
	if !strings.Contains(created.stdout, initialized.Repository.DefaultBranch) {
		t.Errorf("init does not name the default branch %q:\n%s", initialized.Repository.DefaultBranch, created.stdout)
	}
	heldToTheRule(t, "init", created.stdout, carried)

	diagnosed := run(t, h, subject, "doctor")
	var report machine.Doctor
	if err := json.Unmarshal([]byte(run(t, h, subject, "--json", "doctor").stdout), &report); err != nil {
		t.Fatalf("assistant --json doctor does not decode: %v", err)
	}
	if len(report.Checks) == 0 {
		t.Fatal("doctor made no checks, so its rendering is held to nothing")
	}
	details := map[string]string{}
	for _, check := range report.Checks {
		// Every check doctor makes is one the phrase table has rows for,
		// read off what the verb produced rather than off a list here.
		if !slices.Contains(machine.CheckNames(), check.Name) {
			t.Errorf("doctor made the check %q, which internal/machine has no row for", check.Name)
		}
		if !strings.Contains(diagnosed.stdout, machine.CheckPhrase(check).String()) {
			t.Errorf("doctor's rendering does not carry the row for %q:\n%s", check.Name, diagnosed.stdout)
		}
		details["what the "+check.Name+" check found"] = check.Detail
	}
	if !strings.Contains(diagnosed.stdout, machine.DoctorPhrase(report).String()) {
		t.Errorf("doctor's rendering does not carry its verdict:\n%s", diagnosed.stdout)
	}
	heldToTheRule(t, "doctor", diagnosed.stdout, details)

	ejected := run(t, h, subject, "eject", "--confirm")
	if ejected.code != machine.ExitOK {
		t.Fatalf("assistant eject --confirm exited %s:\n%s%s", ejected.code, ejected.stdout, ejected.stderr)
	}
	if got, want := strings.TrimSpace(ejected.stdout), machine.EjectPhrase(machine.Eject{Removed: true}).String(); got != want {
		t.Errorf("eject said %q, want %q", got, want)
	}
	heldToTheRule(t, "eject", ejected.stdout, carried)
}

// TestAPushIsSpokenInPhrasesThroughTheGatesHooks is P13 for the two gate
// renderers as a person sending a change reads them: git relays what the
// hooks internal/gate installed printed, and every relayed line is a row of
// the table beside a branch or reference name, with the run, the gate and the
// commit left to the structured rendering.
func TestAPushIsSpokenInPhrasesThroughTheGatesHooks(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	gatePath := initialize(t, h, subject)

	git(t, subject, "checkout", "--quiet", "-b", "work")
	head := commitOn(t, subject, "work", "work.txt", "a change\n")
	pushed := git(t, subject, "push", gate.RemoteName, "work")
	t.Logf("git push %s work:\n%s", gate.RemoteName, pushed)
	started := onlyRun(t, h, subject)

	var relayed []string
	for _, line := range strings.Split(pushed, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "remote:"); ok && strings.TrimSpace(rest) != "" {
			relayed = append(relayed, strings.TrimSpace(rest))
		}
	}
	said := strings.Join(relayed, "\n")
	for _, want := range []string{machine.PhraseAdmitted.String(), "work: " + machine.PhraseValidationBegun.String()} {
		if !strings.Contains(said, want) {
			t.Errorf("the push did not relay %q:\n%s", want, said)
		}
	}
	heldToTheRule(t, "the push", said, map[string]string{
		"the run's identifier":  started.ID,
		"the gate's path":       gatePath,
		"the gate's identifier": strings.TrimSuffix(filepath.Base(gatePath), ".git"),
		"the pushed commit":     head[:12],
	})
}

// heldToTheRule holds every line a verb printed to the derived forbidden set
// and to the absence of each value the structured answer carried. A value
// that is empty is a fixture that shows nothing, and fails rather than
// passing for looking at nothing.
func heldToTheRule(t *testing.T, verb, printed string, carried map[string]string) {
	t.Helper()
	if strings.TrimSpace(printed) == "" {
		t.Fatalf("%s printed nothing", verb)
	}
	for _, line := range strings.Split(strings.TrimRight(printed, "\n"), "\n") {
		vocabtest.Assert(t, line)
	}
	for what, value := range carried {
		if value == "" {
			t.Fatalf("the structured answer carried no value for %s, so its absence from %s shows nothing", what, verb)
		}
		if strings.Contains(printed, value) {
			t.Errorf("%s printed %s, %q:\n%s", verb, what, value, printed)
		}
	}
}
