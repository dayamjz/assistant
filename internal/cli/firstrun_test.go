package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// doctorChange runs doctor both ways and returns the structured change and
// the human rendering, failing the test when the structured answer carries no
// change. Every line of the human rendering that is not labelled machine text
// is held to P13's rule as it is read.
func doctorChange(t *testing.T, h *home.Home, subject string) (machine.Change, string) {
	t.Helper()
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if report.Change == nil {
		t.Fatalf("doctor on a checkout set up for validation reports no change:\n%+v", report)
	}
	plain := run(t, h, subject, "doctor").stdout
	holdPrinted(t, plain)
	return *report.Change, plain
}

// holdPrinted holds every line a person reads to the derived forbidden set,
// leaving out only the lines doctor labels as machine text, which carry the
// person's own branch names and a command line to copy exactly.
func holdPrinted(t *testing.T, printed string) {
	t.Helper()
	held := 0
	for _, line := range strings.Split(strings.TrimRight(printed, "\n"), "\n") {
		if strings.HasPrefix(line, cli.ChangeCommandLabel+" ") || strings.HasPrefix(line, cli.ChangeDefaultBranchLabel+" ") {
			continue
		}
		held++
		vocabtest.Assert(t, line)
	}
	if held == 0 {
		t.Fatalf("doctor printed nothing but machine text:\n%s", printed)
	}
}

// rowsAfterVerdict is what doctor printed after its verdict, which is where
// the change's rows go.
func rowsAfterVerdict(t *testing.T, printed string, report machine.Doctor) string {
	t.Helper()
	verdict := machine.DoctorPhrase(report).String()
	_, after, ok := strings.Cut(printed, verdict+"\n")
	if !ok {
		t.Fatalf("doctor did not print its verdict %q:\n%s", verdict, printed)
	}
	return after
}

// wantRows checks that exactly these rows follow the verdict, in this order,
// followed by the labelled machine text lines given.
func wantRows(t *testing.T, h *home.Home, subject string, change machine.Change, printed string, machineText ...string) {
	t.Helper()
	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	var want []string
	for _, row := range machine.ChangePhrases(change) {
		want = append(want, row.String())
	}
	want = append(want, machineText...)
	got := strings.Split(strings.TrimRight(rowsAfterVerdict(t, printed, report), "\n"), "\n")
	if !slices.Equal(got, want) {
		t.Fatalf("after the verdict doctor printed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Before init the gate row's step is the one a person meets, and doctor reads
// nothing about the change: the structured answer carries none and nothing a
// change row says is printed. The same checkout after init carries one, so the
// absence is the order and not a read that never happens.
func TestDoctorBeforeInitLeavesTheChangeToTheGateRow(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	standingOnChange(t, subject, "feature")

	report := decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout)
	if report.Change != nil {
		t.Fatalf("doctor before init reports a change: %+v", *report.Change)
	}
	if row, ok := findCheck(report, machine.CheckGate); !ok || row.OK {
		t.Fatalf("doctor before init has no failing gate row: %+v", report.Checks)
	}
	plain := run(t, h, subject, "doctor").stdout
	if !strings.Contains(plain, machine.CheckPhrase(machine.Check{Name: machine.CheckGate}).String()) {
		t.Fatalf("doctor before init does not print the gate row's step:\n%s", plain)
	}
	if after := rowsAfterVerdict(t, plain, report); after != "" {
		t.Fatalf("doctor before init printed after its verdict:\n%s", after)
	}

	initialize(t, h, subject)
	if decodeDoctor(t, run(t, h, subject, "--json", "doctor").stdout).Change == nil {
		t.Fatal("doctor after init reports no change either, so the absence above shows nothing")
	}
}

// A first run from uncommitted changes, walked the way doctor's own contract
// says: do what the line says, then run doctor again. Each answer names the
// next step and only that one, in order: a branch of the change's own when
// the checkout stands on the default branch (whatever it holds uncommitted),
// then a commit, naming which kinds of uncommitted work are there, then the
// command that sends the branch to the gate. The structured answer carries the
// facts each row is chosen from as fields, and every line a person reads is
// held to P13's rule.
//
// Doctor reads and writes nothing through any of it: the checkout's
// references, configuration, index and tree, and the gate's references, read
// the same before and after every doctor run, so the only move toward a run
// is the one the person makes. That is P1's consent boundary for this verb,
// and it is paired with the walk making progress at each step, which a doctor
// that read nothing would not show.
func TestDoctorWalksAFirstRunFromUncommittedChangesToTheSend(t *testing.T) {
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	repository := initialize(t, h, subject)

	untouched := func(t *testing.T, step func()) {
		t.Helper()
		snapshot := func() []string {
			return []string{
				git(t, subject, "for-each-ref", "--format=%(refname) %(objectname)"),
				git(t, subject, "config", "--local", "--list"),
				git(t, subject, "status", "--porcelain=v2", "--untracked-files=all"),
				git(t, subject, "rev-parse", "HEAD"),
				strings.Join(gateRefs(t, repository), "\n"),
			}
		}
		before := snapshot()
		step()
		if after := snapshot(); !slices.Equal(before, after) {
			t.Fatalf("doctor changed the checkout or the gate:\nbefore %q\nafter  %q", before, after)
		}
	}
	var change machine.Change
	var printed string
	ask := func() {
		untouched(t, func() { change, printed = doctorChange(t, h, subject) })
	}

	// On the default branch, clean and then with an edit: the branch comes
	// first either way, and the edit is still read into the fields.
	ask()
	if change.Step != machine.ChangeOnDefaultBranch || change.Branch != "main" || change.DefaultBranch != "main" {
		t.Fatalf("on main: %+v, want the default branch step", change)
	}
	wantRows(t, h, subject, change, printed, cli.ChangeDefaultBranchLabel+" main")
	if row := machine.ChangePhrases(change)[0].Decision; !strings.Contains(row, "git switch -c") {
		t.Fatalf("the default branch row %q does not name the command that makes a branch", row)
	}
	if err := os.WriteFile(filepath.Join(subject, "file.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask()
	if change.Step != machine.ChangeOnDefaultBranch || !change.Unstaged {
		t.Fatalf("on main with an edit: %+v, want the default branch step with the edit read", change)
	}

	// On a branch of its own, the edit comes along and is the next step.
	git(t, subject, "switch", "--quiet", "-c", "feature")
	ask()
	if change.Step != machine.ChangeUncommitted || change.Branch != "feature" || !change.Unstaged || change.Untracked || change.Staged {
		t.Fatalf("on feature with an unstaged edit: %+v", change)
	}
	wantRows(t, h, subject, change, printed)

	// Every kind at once: a row per kind, untracked then unstaged then staged.
	if err := os.WriteFile(filepath.Join(subject, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, subject, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(subject, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask()
	if change.Step != machine.ChangeUncommitted || !change.Untracked || !change.Unstaged || !change.Staged {
		t.Fatalf("with every kind of uncommitted work: %+v", change)
	}
	if rows := machine.ChangePhrases(change); len(rows) != 3 {
		t.Fatalf("with every kind of uncommitted work doctor reads %d rows, want three: %v", len(rows), rows)
	}
	wantRows(t, h, subject, change, printed)

	// Only staged work left: one row, naming the commit alone.
	git(t, subject, "add", "-A")
	ask()
	if change.Step != machine.ChangeUncommitted || change.Untracked || change.Unstaged || !change.Staged {
		t.Fatalf("with only staged work: %+v", change)
	}
	wantRows(t, h, subject, change, printed)

	// Committed: the send, with the exact command line beside its row.
	git(t, subject, "commit", "--quiet", "-m", "the change")
	ask()
	want := "git push " + gate.RemoteName + " feature"
	if change.Step != machine.ChangeUnsent || change.Command != want {
		t.Fatalf("committed: %+v, want the send naming %q", change, want)
	}
	wantRows(t, h, subject, change, printed, cli.ChangeCommandLabel+" "+want)
	if row := machine.ChangePhrases(change)[0].Decision; !strings.Contains(row, "assistant watch") {
		t.Fatalf("the send row %q does not say to follow the run with watch", row)
	}
}

// A checkout on no branch is told to make one, and a branch with nothing its
// default branch lacks is told there is nothing to send yet, rather than
// being handed a send that would validate nothing.
func TestDoctorNamesABranchForADetachedCheckoutAndNothingToSendForAnEmptyBranch(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	initialize(t, h, subject)

	git(t, subject, "switch", "--quiet", "--detach")
	change, printed := doctorChange(t, h, subject)
	if change.Step != machine.ChangeDetached || change.Branch != "" {
		t.Fatalf("detached: %+v", change)
	}
	wantRows(t, h, subject, change, printed)

	git(t, subject, "switch", "--quiet", "-c", "empty", "main")
	change, printed = doctorChange(t, h, subject)
	if change.Step != machine.ChangeNothingNew || change.Command != "" {
		t.Fatalf("a branch with nothing of its own: %+v", change)
	}
	wantRows(t, h, subject, change, printed)
}

// A branch name a shell would read differently is quoted in the command, so
// the line a person copies sends the branch it names. The quoting is read
// back with the same rules a POSIX shell applies.
func TestDoctorQuotesABranchNameInTheSendCommand(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	initialize(t, h, subject)
	branch := "it's$work"
	standingOnChange(t, subject, "plain")
	git(t, subject, "switch", "--quiet", "-c", branch)

	change, _ := doctorChange(t, h, subject)
	if change.Step != machine.ChangeUnsent {
		t.Fatalf("on %q: %+v, want the send", branch, change)
	}
	words, ok := cli.ShellWords(change.Command)
	if !ok || !slices.Equal(words, []string{"git", "push", gate.RemoteName, branch}) {
		t.Fatalf("the command %q reads as %q (ok %v), want it to send %q", change.Command, words, ok, branch)
	}
}

// After the send doctor reads what the gate holds for the branch from the gate
// itself: the branch sent says to follow the run, and a branch rewritten since
// it was sent is told that sending replaces what the gate holds, with a
// command that does. Each printed command is run exactly as printed, and
// doctor reads the send it made, so the command is shown to do what its row
// says rather than only to read like it.
func TestDoctorReadsTheGateAfterASendAndItsCommandsDoWhatTheySay(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)
	standingOnChange(t, subject, "feature")

	runPrinted := func(command string) {
		t.Helper()
		words, ok := cli.ShellWords(command)
		if !ok || len(words) < 2 || words[0] != "git" {
			t.Fatalf("the printed command %q is not a git command line", command)
		}
		git(t, subject, words[1:]...)
	}

	change, _ := doctorChange(t, h, subject)
	if change.Step != machine.ChangeUnsent {
		t.Fatalf("before the send: %+v", change)
	}
	runPrinted(change.Command)
	onlyRun(t, h, subject)

	change, printed := doctorChange(t, h, subject)
	if change.Step != machine.ChangeSent || change.Command != "" {
		t.Fatalf("after the send: %+v, want sent", change)
	}
	wantRows(t, h, subject, change, printed)

	git(t, subject, "commit", "--quiet", "--amend", "-m", "the change, reworded")
	change, printed = doctorChange(t, h, subject)
	want := "git push --force-with-lease " + gate.RemoteName + " feature"
	if change.Step != machine.ChangeDiverged || change.Command != want {
		t.Fatalf("after a rewrite: %+v, want the replacing send %q", change, want)
	}
	wantRows(t, h, subject, change, printed, cli.ChangeCommandLabel+" "+want)
	runPrinted(change.Command)
	if change, _ = doctorChange(t, h, subject); change.Step != machine.ChangeSent {
		t.Fatalf("after the replacing send: %+v, want sent", change)
	}
}
