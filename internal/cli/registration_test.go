package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// The tests in this file drive registration through cli.Run over a real
// checkout and a real gate: init run again, init over an origin that sends a
// push somewhere it does not fetch from, init and doctor with no git to run,
// and the command offer read against the default branch's copy of the
// configuration file. Each path is paired with the control that shows the
// assertion can fail.

// decodeFailure decodes a refusal's structured answer.
func decodeFailure(t *testing.T, got invocation) machine.Failure {
	t.Helper()
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("the refusal does not decode: %v\n%s%s", err, got.stdout, got.stderr)
	}
	return failure
}

// assertNoGateFor fails when the home binds the checkout to a gate or holds a
// repository record for it, which is what a refused init must leave.
func assertNoGateFor(t *testing.T, h *home.Home, subject string) {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if binding, err := records.GateBinding(t.Context(), subject); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the refused init left a gate binding %+v (err %v)", binding, err)
	}
	if record, err := records.RepositoryAt(t.Context(), subject); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the refused init left a repository record %+v (err %v)", record, err)
	}
}

// heldInitToTheRule is heldToTheRule for what init printed: every line but
// the ones init labels as machine text is held to the derived forbidden set,
// and every value the structured answer carried is held absent from all of
// it. A rendering that is nothing but machine text fails rather than passing
// for looking at nothing.
func heldInitToTheRule(t *testing.T, verb, printed string, carried map[string]string) {
	t.Helper()
	held := 0
	for _, line := range strings.Split(strings.TrimRight(printed, "\n"), "\n") {
		if slices.ContainsFunc(cli.InitMachineTextLabels(), func(label string) bool { return strings.HasPrefix(line, label) }) {
			continue
		}
		held++
		vocabtest.Assert(t, line)
	}
	if held == 0 {
		t.Fatalf("%s printed nothing but machine text:\n%s", verb, printed)
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

// remoteNamed reports whether the checkout's own configuration names a remote,
// read from the file rather than through git, so it answers when git cannot
// be run.
func remoteNamed(t *testing.T, subject, name string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(subject, ".git", "config"))
	if err != nil {
		t.Fatalf("reading the checkout's configuration: %v", err)
	}
	return strings.Contains(string(data), `[remote "`+name+`"]`)
}

// An init run again over the checkout it already set up says so rather than
// claiming a new gate, keeps the gate and the record it had, and reports no
// change to the record. The control is the first init, which says it created
// the gate and not that it already had one, so the two rows are told apart
// by what internal/gate answered rather than by which run this is.
func TestInitRunAgainSaysTheGateWasAlreadyThereAndChangesNothing(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	createdRow := machine.InitPhrase(machine.Init{Created: true}).String()
	refreshedRow := machine.InitPhrase(machine.Init{}).String()
	if createdRow == refreshedRow {
		t.Fatal("the created and the already-set-up rows read the same, so this test tells nothing apart")
	}

	first := run(t, h, subject, "init")
	if first.code != machine.ExitOK {
		t.Fatalf("the first init exited %s:\n%s%s", first.code, first.stdout, first.stderr)
	}
	if !strings.Contains(first.stdout, createdRow) || strings.Contains(first.stdout, refreshedRow) {
		t.Fatalf("the first init does not say it created the gate, or says it was already there:\n%s", first.stdout)
	}

	again := run(t, h, subject, "init")
	if again.code != machine.ExitOK {
		t.Fatalf("init run again exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	if !strings.Contains(again.stdout, refreshedRow) || strings.Contains(again.stdout, createdRow) {
		t.Fatalf("init run again does not say the gate was already there, or says it is new:\n%s", again.stdout)
	}

	answer := decodeInit(t, run(t, h, subject, "--json", "init"))
	if answer.Created || answer.Reattached {
		t.Errorf("init run again answered created %v and reattached %v, want neither", answer.Created, answer.Reattached)
	}
	if len(answer.Changed) != 0 {
		t.Errorf("init run again with the same inputs reported changes: %+v", answer.Changed)
	}
	if answer.Upstream == nil || answer.Upstream.Kind != machine.UpstreamKindLocal {
		t.Errorf("init did not report how its local upstream treats proposals: %+v", answer.Upstream)
	} else if !strings.Contains(again.stdout, machine.CheckPhrase(*answer.Upstream).String()) {
		t.Errorf("init does not print the upstream row doctor prints:\n%s", again.stdout)
	}
	records := openStore(t, h)
	binding, err := records.GateBinding(t.Context(), subject)
	_ = records.Close()
	if err != nil || binding.GateID != answer.Gate.ID {
		t.Errorf("the checkout is bound to %+v (err %v), want the gate %s it had", binding, err, answer.Gate.ID)
	}
	heldInitToTheRule(t, "init run again", again.stdout, map[string]string{
		"the gate's identifier": answer.Gate.ID,
		"the gate's path":       answer.Gate.Repository,
		"the working copy":      answer.Repository.WorkingPath,
	})
}

// An init run again with other inputs records them, because init records
// what it was given, and says which fields that replaced: a fork it was not
// given again is dropped, an upstream and a default branch named differently
// are changed, and the branch replaced is printed beside its row. The control
// is the same inputs once more, which reports nothing.
func TestInitRunAgainSaysWhichFieldsOfTheRecordItChanged(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	const otherUpstream = "https://github.com/other/assistant.git"

	first := decodeInit(t, run(t, h, subject, "--json", "init", "--upstream", forkTestUpstream, "--fork-url", forkTestFork))
	if len(first.Changed) != 0 || !first.Created {
		t.Fatalf("a first init reported changes %+v or no creation", first.Changed)
	}

	dropped := run(t, h, subject, "init", "--upstream", forkTestUpstream)
	droppedRow := machine.RecordChangePhrase(machine.RecordChange{Field: machine.RecordFork, Before: forkTestFork}).String()
	if dropped.code != machine.ExitOK || !strings.Contains(dropped.stdout, droppedRow) {
		t.Fatalf("init without --fork-url exited %s and does not say the fork was dropped:\n%s%s", dropped.code, dropped.stdout, dropped.stderr)
	}
	heldInitToTheRule(t, "init dropping a fork", dropped.stdout, map[string]string{"the fork": forkTestFork})

	moved := decodeInit(t, run(t, h, subject, "--json", "init",
		"--upstream", otherUpstream, "--fork-url", forkTestFork, "--default-branch", "trunk"))
	want := []machine.RecordChange{
		{Field: machine.RecordUpstream, Before: forkTestUpstream, After: otherUpstream},
		{Field: machine.RecordFork, Before: "", After: forkTestFork},
		{Field: machine.RecordDefaultBranch, Before: "main", After: "trunk"},
	}
	if !slices.Equal(moved.Changed, want) {
		t.Fatalf("init reported changes %+v, want %+v", moved.Changed, want)
	}

	back := run(t, h, subject, "init", "--upstream", otherUpstream, "--fork-url", forkTestFork, "--default-branch", "main")
	branchRow := machine.RecordChangePhrase(machine.RecordChange{Field: machine.RecordDefaultBranch}).String()
	upstreamRow := machine.RecordChangePhrase(machine.RecordChange{Field: machine.RecordUpstream}).String()
	if back.code != machine.ExitOK || !strings.Contains(back.stdout, branchRow) || !strings.Contains(back.stdout, "Default branch before trunk") {
		t.Fatalf("init does not say the default branch changed from trunk:\n%s%s", back.stdout, back.stderr)
	}
	if strings.Contains(back.stdout, upstreamRow) {
		t.Errorf("init says the upstream changed when it was given the one recorded:\n%s", back.stdout)
	}
	heldInitToTheRule(t, "init changing the default branch", back.stdout, map[string]string{"the upstream": otherUpstream})

	same := decodeInit(t, run(t, h, subject, "--json", "init",
		"--upstream", otherUpstream, "--fork-url", forkTestFork, "--default-branch", "main"))
	if len(same.Changed) != 0 {
		t.Errorf("init given what the record holds reported changes: %+v", same.Changed)
	}
}

// An origin that sends a push to another repository than the one it fetches
// from does not settle where changes go, so init without --upstream refuses
// as incorrect usage, in its row, and sets up nothing: no gate, no record,
// no assistant remote. --upstream settles it and is recorded. An origin
// fetched over HTTPS and pushed over SSH to the same repository on the code
// host is not refused, which is the control that the refusal compares
// repositories and not spellings.
func TestInitRefusesAnOriginThatSendsAPushElsewhere(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	fetch := git(t, subject, "remote", "get-url", "origin")
	git(t, subject, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "elsewhere.git"))

	refused := run(t, h, subject, "--json", "init")
	if refused.code != machine.ExitUsage {
		t.Fatalf("init over an origin with two repositories exited %s, want incorrect usage:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	if got := decodeFailure(t, refused).Error; got != machine.PhraseInitOriginAmbiguous.String() {
		t.Fatalf("init refused with %q, want the ambiguous-origin row", got)
	}
	readOut := run(t, h, subject, "init")
	if readOut.code != machine.ExitUsage || !strings.Contains(readOut.stderr, machine.PhraseInitOriginAmbiguous.String()) {
		t.Fatalf("init exited %s without the ambiguous-origin row:\n%s%s", readOut.code, readOut.stdout, readOut.stderr)
	}
	vocabtest.Assert(t, readOut.stderr)
	assertNoGateFor(t, h, subject)
	if remoteNamed(t, subject, gate.RemoteName) {
		t.Fatal("the refused init left the assistant remote in the checkout")
	}

	settled := decodeInit(t, run(t, h, subject, "--json", "init", "--upstream", fetch))
	if settled.Repository.UpstreamURL != fetch {
		t.Fatalf("init with --upstream recorded %q, want %q", settled.Repository.UpstreamURL, fetch)
	}

	same := newSubject(t)
	git(t, same, "remote", "set-url", "origin", "https://github.com/upstream/assistant.git")
	git(t, same, "remote", "set-url", "--push", "origin", "git@github.com:upstream/assistant.git")
	if got := run(t, h, same, "init"); got.code != machine.ExitOK {
		t.Fatalf("init over an origin fetching and pushing one repository two ways exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}

// With no git to run, init and doctor say that rather than that the directory
// is not a checkout. Init refuses in its row with the operational code and
// sets up nothing; doctor reports a blocking git row and makes none of the
// rows that read the checkout, because what they would say is unknown. The
// control is the same two commands with git on PATH: doctor's git row passes
// and its working copy row is made. It is not parallel, because git is found
// through this process's PATH.
func TestInitAndDoctorSayWhenGitCannotBeRun(t *testing.T) {
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)

	control := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if row, ok := findCheck(control, machine.CheckGit); !ok || !row.OK || !row.Blocking {
		t.Fatalf("doctor with git on PATH reports git as %+v (made %v), want a passing blocking row", row, ok)
	}
	if !namesCheck(control, machine.CheckWorkingCopy) {
		t.Fatal("doctor with git on PATH made no working copy row, so its absence below shows nothing")
	}

	t.Setenv("PATH", t.TempDir())
	refused := run(t, h, subject, "init")
	if refused.code != machine.ExitFailure {
		t.Fatalf("init with no git exited %s, want the operational failure:\n%s%s", refused.code, refused.stdout, refused.stderr)
	}
	if !strings.Contains(refused.stderr, machine.PhraseGitMissing.String()) {
		t.Fatalf("init with no git does not say git could not be run:\n%s", refused.stderr)
	}
	vocabtest.Assert(t, refused.stderr)
	assertNoGateFor(t, h, subject)
	if remoteNamed(t, subject, gate.RemoteName) {
		t.Fatal("the refused init left the assistant remote in the checkout")
	}

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	row, ok := findCheck(report, machine.CheckGit)
	if !ok || row.OK || !row.Blocking {
		t.Fatalf("doctor with no git reports git as %+v (made %v), want a failing blocking row", row, ok)
	}
	if report.CanStartRun {
		t.Fatal("doctor with no git says a run can start")
	}
	for _, name := range []string{machine.CheckWorkingCopy, machine.CheckGate, machine.CheckUpstream} {
		if namesCheck(report, name) {
			t.Errorf("doctor with no git made the %s row, which reads the checkout through git", name)
		}
	}
	diagnosed := run(t, h, subject, "doctor")
	if !strings.Contains(diagnosed.stdout, machine.CheckPhrase(row).String()) {
		t.Fatalf("doctor's rendering does not carry the git row:\n%s", diagnosed.stdout)
	}
	heldToTheRule(t, "doctor with no git", diagnosed.stdout, map[string]string{"what the git check found": row.Detail})
}

// branchDocument is a configuration file naming a command for commands.test.
func branchDocument(command string) string {
	return `{"commands":{"test":` + strings.TrimSpace(mustJSON(command)) + "}}\n"
}

// mustJSON is a string as a JSON value.
func mustJSON(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// onDefaultBranch commits files to main, sends them to origin so the
// checkout's copy of origin's main holds them, and then stands on a branch of
// its own whose working tree init reads.
func onDefaultBranch(t *testing.T, subject string, files map[string]string) {
	t.Helper()
	plant(t, subject, files)
	commitAll(t, subject, "the default branch's configuration")
	git(t, subject, "push", "--quiet", "origin", "main")
	git(t, subject, "checkout", "--quiet", "-b", "work")
}

// The command offer reads the default branch's copy of the configuration
// file beside the checkout's. A key that branch names and the checkout's copy
// does not is not offered, even with consent to write everything, so init
// cannot make the copies disagree; a key both name differently is reported,
// because the checkout's command reaches no run; and a copy on that branch
// that does not parse is reported and compared against nothing, while the
// offer goes on. The control is a checkout whose copy says what the branch's
// does, which prints none of the three rows.
func TestInitReadsTheDefaultBranchsCopyBeforeOffering(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)

	t.Run("named on the branch", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		onDefaultBranch(t, subject, map[string]string{config.RepositoryDocument: branchDocument("go test ./pkg")})
		git(t, subject, "rm", "--quiet", config.RepositoryDocument)
		commitAll(t, subject, "a branch without the file")
		plant(t, subject, goModule)

		answer := decodeInit(t, run(t, h, subject, "--json", "init", "--accept-commands"))
		branch := answer.Commands.Branch
		if branch.Reading != machine.BranchRead || branch.Ref != "refs/remotes/origin/main" {
			t.Fatalf("init read the branch's copy as %+v, want read at origin's main", branch)
		}
		if !slices.Equal(branch.Named, []string{string(config.KeyCommandsTest)}) {
			t.Fatalf("init reported %v as named on the branch, want commands.test", branch.Named)
		}
		got := offered(answer.Commands)
		if _, ok := got[string(config.KeyCommandsTest)]; ok {
			t.Fatalf("init offered commands.test, which the default branch names: %v", got)
		}
		if got[string(config.KeyCommandsLint)] != "go vet ./..." {
			t.Fatalf("init did not offer the lint command the checkout declares: %v", got)
		}
		written, ok := documentAt(t, subject)
		if !ok || strings.Contains(written, `"test"`) {
			t.Fatalf("init wrote a key the default branch names into the checkout:\n%s", written)
		}
	})

	t.Run("named differently", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		onDefaultBranch(t, subject, map[string]string{config.RepositoryDocument: branchDocument("go test ./pkg")})
		plant(t, subject, map[string]string{config.RepositoryDocument: branchDocument("go test ./other")})

		answer := decodeInit(t, run(t, h, subject, "--json", "init"))
		if !slices.Equal(answer.Commands.Branch.Differs, []string{string(config.KeyCommandsTest)}) {
			t.Fatalf("init reported %v as differing, want commands.test", answer.Commands.Branch.Differs)
		}
		readOut := run(t, h, subject, "init")
		if !strings.Contains(readOut.stdout, machine.PhraseCommandsDiffer.String()) ||
			!strings.Contains(readOut.stdout, "Key: commands.test") {
			t.Fatalf("init does not say the two copies differ, naming the key:\n%s", readOut.stdout)
		}
		heldInitToTheRule(t, "init over differing copies", readOut.stdout, map[string]string{"the gate's identifier": answer.Gate.ID})
	})

	t.Run("unreadable on the branch", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		onDefaultBranch(t, subject, map[string]string{config.RepositoryDocument: "{not json\n"})
		git(t, subject, "rm", "--quiet", config.RepositoryDocument)
		commitAll(t, subject, "a branch without the file")
		plant(t, subject, goModule)

		answer := decodeInit(t, run(t, h, subject, "--json", "init"))
		if answer.Commands.Branch.Reading != machine.BranchUnreadable || answer.Commands.Branch.Detail == "" {
			t.Fatalf("init read a copy that does not parse as %+v, want unreadable with what was wrong", answer.Commands.Branch)
		}
		if len(offered(answer.Commands)) == 0 {
			t.Fatal("init offered nothing beside an unreadable branch copy, so the offer stopped on it")
		}
		readOut := run(t, h, subject, "init")
		if !strings.Contains(readOut.stdout, machine.PhraseCommandsBranchUnread.String()) {
			t.Fatalf("init does not say the branch's copy could not be read:\n%s", readOut.stdout)
		}
		heldInitToTheRule(t, "init beside an unreadable branch copy", readOut.stdout, nil)
	})

	t.Run("agreeing copies", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		onDefaultBranch(t, subject, map[string]string{config.RepositoryDocument: branchDocument("go test ./pkg")})

		answer := decodeInit(t, run(t, h, subject, "--json", "init"))
		branch := answer.Commands.Branch
		if branch.Reading != machine.BranchRead || len(branch.Named) != 0 || len(branch.Differs) != 0 {
			t.Fatalf("agreeing copies read as %+v, want read and nothing to report", branch)
		}
		readOut := run(t, h, subject, "init")
		for _, row := range []machine.Phrase{machine.PhraseCommandsOnDefaultBranch, machine.PhraseCommandsDiffer, machine.PhraseCommandsBranchUnread} {
			if strings.Contains(readOut.stdout, row.String()) {
				t.Errorf("init over agreeing copies printed %q", row.Outcome)
			}
		}
	})

	t.Run("no file and no branch", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		if got := decodeInit(t, run(t, h, subject, "--json", "init")).Commands.Branch.Reading; got != machine.BranchAbsent {
			t.Errorf("a default branch with no configuration file read as %q, want absent", got)
		}
		if got := decodeInit(t, run(t, h, subject, "--json", "init", "--default-branch", "nowhere")).Commands.Branch.Reading; got != machine.BranchNotHere {
			t.Errorf("a default branch this checkout does not hold read as %q, want not here", got)
		}
	})
}

// plantTripwire writes an executable that records each time it is run by
// appending a line to fired, and returns the script as one shell word with
// the record's path. The script path is absolute, so a recipe naming it
// fires wherever make is started, and quoted, so it fires whatever the
// temporary directory is called (#415).
func plantTripwire(t *testing.T, dir string) (script, fired string) {
	t.Helper()
	fired = filepath.Join(dir, "registration-tripwire-fired")
	path := filepath.Join(dir, "registration-tripwire")
	script = "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	quoted := "'" + strings.ReplaceAll(fired, "'", `'\''`) + "'"
	body := "#!/bin/sh\nprintf 'fired\\n' >> " + quoted + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("planting the tripwire: %v", err)
	}
	return script, fired
}

// tripwireQuiet fails when the planted executable has run.
func tripwireQuiet(t *testing.T, fired, after string) {
	t.Helper()
	data, err := os.ReadFile(fired)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("reading the tripwire record: %v", err)
	}
	t.Fatalf("the tripwire ran during %s:\n%s", after, data)
}

// Inspection runs nothing it finds. A Makefile whose test and lint targets
// run a tripwire is offered as those two commands, and neither a printed
// offer, a written one, nor an init run again over the written file runs it;
// the run again offers nothing and leaves the file as it was. The control is
// make itself, run on the same Makefile before init, which does fire the
// tripwire, so its silence afterwards is the absence of a run and not a
// tripwire that cannot fire.
func TestInitRunsNothingItFindsAndRunAgainLeavesTheFileAsItWas(t *testing.T) {
	t.Parallel()
	makeBinary, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("this test needs make to show its tripwire can fire: %v", err)
	}
	h := newHome(t)
	subject := newSubject(t)
	script, fired := plantTripwire(t, subject)
	plant(t, subject, map[string]string{"Makefile": "test:\n\t" + script + "\nlint:\n\t" + script + "\n"})
	if out, err := exec.Command(makeBinary, "-C", subject, "test").CombinedOutput(); err != nil {
		t.Fatalf("make test did not run: %v\n%s", err, out)
	}
	if _, err := os.Stat(fired); err != nil {
		t.Fatalf("make test did not fire the tripwire, so a quiet init would show nothing: %v", err)
	}
	if err := os.Remove(fired); err != nil {
		t.Fatalf("clearing the tripwire record: %v", err)
	}

	printed := decodeInit(t, run(t, h, subject, "--json", "init")).Commands
	if printed.Result != machine.CommandsPrinted || offered(printed)["commands.test"] != "make test" ||
		offered(printed)["commands.lint"] != "make lint" {
		t.Fatalf("init offered %v ending %q, want the two make targets printed", offered(printed), printed.Result)
	}
	tripwireQuiet(t, fired, "an offer printed")
	if _, there := documentAt(t, subject); there {
		t.Fatal("init wrote a configuration file with no consent")
	}

	if written := decodeInit(t, run(t, h, subject, "--json", "init", "--accept-commands")).Commands; written.Result != machine.CommandsWritten {
		t.Fatalf("the accepted offer ended %q, want written", written.Result)
	}
	tripwireQuiet(t, fired, "an offer written")
	first, there := documentAt(t, subject)
	if !there {
		t.Fatal("consent wrote no configuration file")
	}

	again := decodeInit(t, run(t, h, subject, "--json", "init", "--accept-commands"))
	if again.Commands.Result != machine.CommandsNone || len(again.Changed) != 0 || again.Created {
		t.Fatalf("init run again offered %q, changed %+v, created %v; want nothing new", again.Commands.Result, again.Changed, again.Created)
	}
	if second, _ := documentAt(t, subject); second != first {
		t.Fatalf("init run again rewrote the configuration file:\n%s\nwas:\n%s", second, first)
	}
	tripwireQuiet(t, fired, "init run again")
}
