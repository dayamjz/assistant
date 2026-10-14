package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// The two remedies every refusal to record a default branch names, written
// out here rather than read from internal/machine, so a row that dropped one
// fails this test rather than agreeing with itself.
var defaultBranchRemedies = []string{"--default-branch", "git remote set-head origin --auto"}

// refusedDefaultBranch runs init with args and holds it to the refusal for
// reason: it fails, both renderings carry internal/machine's row for that
// reason and both remedies, and nothing was set up, so the working copy has
// no gate remote and the home binds it to no gate.
func refusedDefaultBranch(t *testing.T, h *home.Home, subject string, reason machine.DefaultBranchRefusal, args ...string) {
	t.Helper()
	row := machine.DefaultBranchRefusalPhrase(reason).String()

	plain := run(t, h, subject, append([]string{"init"}, args...)...)
	if plain.code != machine.ExitFailure {
		t.Fatalf("init exited %s, want a refusal:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stderr, row) {
		t.Errorf("the refusal does not carry the %s row %q:\n%s", reason, row, plain.stderr)
	}
	for _, remedy := range defaultBranchRemedies {
		if !strings.Contains(plain.stderr, remedy) {
			t.Errorf("the %s refusal does not name %q:\n%s", reason, remedy, plain.stderr)
		}
	}

	structured := run(t, h, subject, append([]string{"--json", "init"}, args...)...)
	if structured.code != machine.ExitFailure {
		t.Fatalf("init --json exited %s, want a refusal:\n%s%s", structured.code, structured.stdout, structured.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(structured.stdout), &failure); err != nil {
		t.Fatalf("the refusal does not decode as a failure: %v\n%s", err, structured.stdout)
	}
	if !strings.HasPrefix(failure.Error, row) {
		t.Errorf("the structured refusal is %q, want it to open with the %s row", failure.Error, reason)
	}

	if remotes := git(t, subject, "remote"); strings.Contains(remotes, "assistant") {
		t.Errorf("the refused init left an assistant remote behind: %q", remotes)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if binding, err := records.GateBinding(t.Context(), subject); err == nil {
		t.Errorf("the refused init bound the working copy to gate %s", binding.GateID)
	}
}

// recordedDefaultBranch runs init with --json and args and returns its
// answer, failing unless it succeeded.
func recordedDefaultBranch(t *testing.T, h *home.Home, subject string, args ...string) machine.Init {
	t.Helper()
	got := run(t, h, subject, append([]string{"--json", "init"}, args...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var answer machine.Init
	if err := json.Unmarshal([]byte(got.stdout), &answer); err != nil {
		t.Fatalf("init does not decode: %v\n%s", err, got.stdout)
	}
	return answer
}

// P7 reads trusted configuration from the default branch, so init records one
// only when it is provable. A working copy whose origin records no HEAD, run
// from a feature branch, is refused rather than recording the feature branch,
// which would make the branch under validation its own trusted copy. The
// refusal names both remedies, and the first of them is shown to work: after
// git remote set-head origin --auto the same init records origin's branch
// and says that is where it came from.
func TestInitRefusesWhenOriginRecordsNoDefaultBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "remote", "set-head", "origin", "--delete")
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")

	refusedDefaultBranch(t, h, subject, machine.DefaultBranchUnrecorded)

	git(t, subject, "remote", "set-head", "origin", "--auto")
	answer := recordedDefaultBranch(t, h, subject)
	if answer.Repository.DefaultBranch != "main" || answer.DefaultBranchSource != machine.DefaultBranchFromOrigin {
		t.Fatalf("after the remedy init recorded %q from %q, want main from origin",
			answer.Repository.DefaultBranch, answer.DefaultBranchSource)
	}
}

// A record of origin's HEAD that is an ordinary reference names a commit and
// no branch, so it proves nothing about which branch is the default and is
// refused on the same terms as no record at all.
func TestInitRefusesWhenOriginsHeadIsNotSymbolic(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	head := git(t, subject, "rev-parse", "HEAD")
	git(t, subject, "remote", "set-head", "origin", "--delete")
	git(t, subject, "update-ref", "--no-deref", "refs/remotes/origin/HEAD", head)
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")

	refusedDefaultBranch(t, h, subject, machine.DefaultBranchUnrecorded)
}

// A working copy with no origin has nothing to learn the default branch from.
// Naming the upstream with --upstream does not change that, so init is refused
// rather than taking the branch the checkout stands on, and the remedy that
// applies, --default-branch, is shown to work and to be reported as the
// source.
func TestInitRefusesAWorkingCopyWithNoOriginUntilTheBranchIsNamed(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubjectWithoutOrigin(t)
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")
	upstream := t.TempDir()

	refusedDefaultBranch(t, h, subject, machine.DefaultBranchNoOrigin, "--upstream", upstream)

	answer := recordedDefaultBranch(t, h, subject, "--upstream", upstream, "--default-branch", "main")
	if answer.Repository.DefaultBranch != "main" || answer.DefaultBranchSource != machine.DefaultBranchFromFlag {
		t.Fatalf("with --default-branch init recorded %q from %q, want main from the flag",
			answer.Repository.DefaultBranch, answer.DefaultBranchSource)
	}
}

// A record of origin's HEAD that points outside origin's own branches is not
// origin's answer, and the case that matters is the one this builds: it
// points at the feature branch the checkout stands on, which taking it would
// make its own trusted copy.
func TestInitRefusesAnOriginHeadPointingOutsideOrigin(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")
	git(t, subject, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/heads/a-feature")

	refusedDefaultBranch(t, h, subject, machine.DefaultBranchOutsideOrigin)
}

// --default-branch is the answer when it is given, over whatever origin
// records, and init says that is where the answer came from in both
// renderings. The subject records no origin HEAD, so the flag is the only
// thing that could have supplied the branch.
func TestInitRecordsTheBranchNamedWithDefaultBranch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "remote", "set-head", "origin", "--delete")
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")

	plain := run(t, h, subject, "init", "--default-branch", "main")
	if plain.code != machine.ExitOK {
		t.Fatalf("init --default-branch main exited %s:\n%s%s", plain.code, plain.stdout, plain.stderr)
	}
	if !strings.Contains(plain.stdout, machine.DefaultBranchPhrase(machine.DefaultBranchFromFlag).String()) ||
		!strings.Contains(plain.stdout, "Default branch main") {
		t.Errorf("init does not say it recorded main from the flag:\n%s", plain.stdout)
	}
	answer := recordedDefaultBranch(t, h, subject, "--default-branch", "main")
	if answer.Repository.DefaultBranch != "main" || answer.DefaultBranchSource != machine.DefaultBranchFromFlag {
		t.Fatalf("init recorded %q from %q, want main from the flag", answer.Repository.DefaultBranch, answer.DefaultBranchSource)
	}
}

// A record of origin's HEAD that git itself cannot read is refused as
// unreadable, with what git said following the row, and not as an absent
// record: the two have different remedies, and a refusal that merged them
// would hide a damaged repository. The state is built by writing the loose
// reference file with content git does not accept, and git is asked
// directly, before init runs, that it fails the read rather than reporting
// the reference absent, so the arm is reached through what git puts on the
// wire and not through a value stated here. Where this git keeps references
// somewhere other than loose files, or answers such a file as absent, the
// test says so and skips rather than passing on a state it did not build.
func TestInitRefusesAnOriginHeadGitCannotRead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "checkout", "--quiet", "-b", "a-feature")
	git(t, subject, "remote", "set-head", "origin", "--delete")

	loose := git(t, subject, "rev-parse", "--git-path", "refs/remotes/origin/HEAD")
	if !filepath.IsAbs(loose) {
		loose = filepath.Join(subject, loose)
	}
	if err := os.MkdirAll(filepath.Dir(loose), 0o750); err != nil {
		t.Fatalf("making the loose reference's directory: %v", err)
	}
	if err := os.WriteFile(loose, []byte("not a reference\n"), 0o600); err != nil {
		t.Fatalf("writing the damaged reference: %v", err)
	}
	out, err := gitCommand(t.Context(), subject, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() <= 1 {
		t.Skipf("this git does not fail the read of a damaged loose reference (err %v), so the state is not reachable here:\n%s", err, out)
	}

	refusedDefaultBranch(t, h, subject, machine.DefaultBranchUnreadable)
	if got := run(t, h, subject, "init"); !strings.Contains(got.stderr, "Cause: ") {
		t.Errorf("the unreadable refusal does not give the cause its row points at:\n%s", got.stderr)
	}
}
