package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
)

// quinceneraCheckout is the planning subject inspection is run against. The
// test copies it and never writes the original.
const quinceneraCheckout = "/Users/joelzambrano/Projects/Quincenera"

// plantTripwire writes an executable that records a run by appending one line
// to fired, and returns both paths. Detection is read-only only if fired is
// absent after init. The script path is absolute so a recipe that names it
// fires wherever make is started.
func plantTripwire(t *testing.T, dir string) (script, fired string) {
	t.Helper()
	fired = filepath.Join(dir, "onboard-tripwire-fired")
	script = filepath.Join(dir, "onboard-tripwire")
	quoted := "'" + strings.ReplaceAll(fired, "'", "'\\''") + "'"
	body := "#!/bin/sh\nprintf '%s\\n' fired >> " + quoted + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("planting the tripwire: %v", err)
	}
	return script, fired
}

// proveTripwireFires runs the planted executable and then removes its record,
// so a later absence means init did not run it. A tripwire that cannot fire
// would make that absence prove nothing.
func proveTripwireFires(t *testing.T, script, fired string) {
	t.Helper()
	if err := exec.Command(script).Run(); err != nil {
		t.Fatalf("the tripwire did not run when started: %v", err)
	}
	if _, err := os.Stat(fired); err != nil {
		t.Fatalf("the tripwire produced no record: %v", err)
	}
	if err := os.Remove(fired); err != nil {
		t.Fatalf("clearing the tripwire record: %v", err)
	}
}

// tripwireQuiet fails when the planted executable has run.
func tripwireQuiet(t *testing.T, fired string) {
	t.Helper()
	data, err := os.ReadFile(fired)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("reading the tripwire record: %v", err)
	}
	t.Fatalf("the tripwire ran:\n%s", data)
}

// assertNoGate fails when init left a gate behind.
func assertNoGate(t *testing.T, h *home.Home, subject string) {
	t.Helper()
	if remotes := git(t, subject, "remote"); strings.Contains(remotes, "assistant") {
		t.Errorf("init left an assistant remote behind: %q", remotes)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	if binding, err := records.GateBinding(t.Context(), subject); err == nil {
		t.Errorf("init bound the working copy to gate %s", binding.GateID)
	}
	listed, err := records.Repositories(t.Context())
	if err != nil {
		t.Fatalf("listing repositories: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("init recorded %+v", listed)
	}
}

// commandDocument is an assistant.json whose test command is command.
func commandDocument(t *testing.T, command string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"commands": map[string]string{"test": command},
	})
	if err != nil {
		t.Fatalf("encoding a configuration file: %v", err)
	}
	return string(body) + "\n"
}

// TestDetectedCommandsAreNotRunOrTrustedWithoutConsent is P1 and P7 for
// inspection. A Makefile whose targets run a tripwire is detected and not
// executed. Without consent the command is not written. With consent it is
// written into the checkout and still is not the copy the default branch
// holds, so it is not what a run would trust, and the tripwire has still not
// run. Running init again writes nothing further.
func TestDetectedCommandsAreNotRunOrTrustedWithoutConsent(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	principles.Cite(t, principles.P7)

	h := newHome(t)
	subject := newSubject(t)
	script, fired := plantTripwire(t, subject)
	plant(t, subject, map[string]string{"Makefile": "test:\n\t" + script + "\nlint:\n\t" + script + "\n"})
	proveTripwireFires(t, script, fired)
	make := exec.Command("make", "-C", subject, "test")
	if err := make.Run(); err != nil {
		t.Fatalf("make test did not run the tripwire, so a quiet init would prove nothing: %v", err)
	}
	if err := os.Remove(fired); err != nil {
		t.Fatalf("clearing the tripwire record: %v", err)
	}

	got := run(t, h, subject, "--json", "init")
	if got.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	answer := decodeInit(t, got).Commands
	if answer.Result != machine.CommandsPrinted {
		t.Fatalf("the offer ended %q, want %q", answer.Result, machine.CommandsPrinted)
	}
	if offered(answer)["commands.test"] != "make test" || offered(answer)["commands.lint"] != "make lint" {
		t.Fatalf("init offered %v, want the two make targets", offered(answer))
	}
	tripwireQuiet(t, fired)
	if _, there := documentAt(t, subject); there {
		t.Fatal("init wrote a configuration file with no consent")
	}
	if out, err := tryGit(subject, "show", "main:"+config.RepositoryDocument); err == nil {
		t.Fatalf("the default branch already holds a configuration file:\n%s", out)
	}

	accepted := run(t, h, subject, "--json", "init", "--accept-commands")
	if accepted.code != machine.ExitOK {
		t.Fatalf("init --accept-commands exited %s:\n%s%s", accepted.code, accepted.stdout, accepted.stderr)
	}
	written := decodeInit(t, accepted).Commands
	if written.Result != machine.CommandsWritten {
		t.Fatalf("the accepted offer ended %q, want %q", written.Result, machine.CommandsWritten)
	}
	tripwireQuiet(t, fired)
	if out, err := tryGit(subject, "show", "main:"+config.RepositoryDocument); err == nil {
		t.Fatalf("consent put the command on the default branch, which is where a run would trust it:\n%s", out)
	}
	first, there := documentAt(t, subject)
	if !there {
		t.Fatal("consent wrote no configuration file")
	}

	again := run(t, h, subject, "--json", "init", "--accept-commands")
	if again.code != machine.ExitOK {
		t.Fatalf("init again exited %s:\n%s%s", again.code, again.stdout, again.stderr)
	}
	if decodeInit(t, again).Commands.Result != machine.CommandsNone {
		t.Fatalf("init offered commands the file already names: %+v", decodeInit(t, again).Commands)
	}
	second, _ := documentAt(t, subject)
	if second != first {
		t.Fatalf("running init again rewrote the configuration file:\n%s", second)
	}
	tripwireQuiet(t, fired)
}

// TestInitAgainRecordsTheSameRepository is inspection repeated: the second
// init keeps the gate it created and the repository row it wrote, and does
// not add another. Reattached stays false: that flag is a working copy whose
// path no longer hashes to its gate, and this copy has not moved.
func TestInitAgainRecordsTheSameRepository(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)

	first := decodeInit(t, mustInit(t, h, subject))
	second := decodeInit(t, mustInit(t, h, subject))
	if second.Gate.ID != first.Gate.ID || second.Repository.ID != first.Repository.ID {
		t.Fatalf("init again recorded gate %s repository %s, want %s and %s",
			second.Gate.ID, second.Repository.ID, first.Gate.ID, first.Repository.ID)
	}
	if !second.Repository.CreatedAt.Equal(first.Repository.CreatedAt) {
		t.Fatalf("init again moved the repository's created time from %s to %s",
			first.Repository.CreatedAt, second.Repository.CreatedAt)
	}
	if second.Repository.UpstreamURL != first.Repository.UpstreamURL ||
		second.Repository.DefaultBranch != first.Repository.DefaultBranch {
		t.Fatalf("init again recorded %+v, want the same upstream and default branch as %+v",
			second.Repository, first.Repository)
	}
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	listed, err := records.Repositories(t.Context())
	if err != nil {
		t.Fatalf("listing repositories: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.Repository.ID {
		t.Fatalf("the home holds %+v, want the one repository init recorded", listed)
	}
}

// mustInit runs init and fails when it does not succeed.
func mustInit(t *testing.T, h *home.Home, subject string, args ...string) invocation {
	t.Helper()
	got := run(t, h, subject, append([]string{"--json", "init"}, args...)...)
	if got.code != machine.ExitOK {
		t.Fatalf("init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	return got
}

// TestInitRefusesAnOriginWithTwoAddresses is the ambiguous-identity refusal.
// Origin's fetch address and push address differ, the caller named neither,
// and init sets nothing up. Naming --upstream is the settlement, and then
// init records that address.
func TestInitRefusesAnOriginWithTwoAddresses(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	fetch := git(t, subject, "remote", "get-url", "origin")
	push := filepath.Join(t.TempDir(), "other.git")
	git(t, subject, "remote", "set-url", "--push", "origin", push)

	refused := run(t, h, subject, "--json", "init")
	row := machine.OnboardRefusalPhrase(machine.OnboardIdentityAmbiguous).String()
	if refused.code != machine.ExitUsage {
		t.Fatalf("init exited %s, want %s:\n%s%s", refused.code, machine.ExitUsage, refused.stdout, refused.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
		t.Fatalf("the refusal does not decode: %v\n%s", err, refused.stdout)
	}
	if failure.Error != row {
		t.Fatalf("the refusal is %q, want %q", failure.Error, row)
	}
	assertNoGate(t, h, subject)

	recorded := decodeInit(t, mustInit(t, h, subject, "--upstream", fetch))
	if recorded.Repository.UpstreamURL != fetch {
		t.Fatalf("init --upstream recorded %q, want %q", recorded.Repository.UpstreamURL, fetch)
	}
}

// TestInitRefusesWhenGitCannotBeStarted is the missing-git refusal. It is
// not parallel: the git binary is found through this process's PATH, which
// the test replaces for the one invocation and restores before it asks git
// anything else.
func TestInitRefusesWhenGitCannotBeStarted(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())

	refused := run(t, h, subject, "--json", "init")
	t.Setenv("PATH", path)

	row := machine.OnboardRefusalPhrase(machine.OnboardGitMissing).String()
	if refused.code != machine.ExitFailure {
		t.Fatalf("init exited %s, want %s:\n%s%s", refused.code, machine.ExitFailure, refused.stdout, refused.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
		t.Fatalf("the refusal does not decode: %v\n%s", err, refused.stdout)
	}
	if failure.Error != row {
		t.Fatalf("the refusal is %q, want %q", failure.Error, row)
	}
	assertNoGate(t, h, subject)
}

// TestInitRefusesAConfigurationFileThatDisagreesWithTheDefaultBranch is the
// conflicting-file refusal. The checkout's command differs from the one the
// default branch commit holds, the differing command is a tripwire, and init
// neither runs it nor writes either copy nor sets up a gate. A checkout that
// dropped the file the branch holds is the same refusal. A checkout whose
// file matches the branch is not.
func TestInitRefusesAConfigurationFileThatDisagreesWithTheDefaultBranch(t *testing.T) {
	t.Parallel()
	row := machine.OnboardRefusalPhrase(machine.OnboardConfigConflict).String()

	t.Run("a different command", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		script, fired := plantTripwire(t, subject)
		proveTripwireFires(t, script, fired)
		plant(t, subject, map[string]string{config.RepositoryDocument: commandDocument(t, "true")})
		commitAll(t, subject, "configure a command")
		git(t, subject, "push", "--quiet", "origin", "main")
		dirty := commandDocument(t, script)
		if err := os.WriteFile(filepath.Join(subject, config.RepositoryDocument), []byte(dirty), 0o600); err != nil {
			t.Fatalf("writing the disagreeing file: %v", err)
		}

		refused := run(t, h, subject, "--json", "init", "--accept-commands")
		if refused.code != machine.ExitFailure {
			t.Fatalf("init exited %s, want %s:\n%s%s", refused.code, machine.ExitFailure, refused.stdout, refused.stderr)
		}
		var failure machine.Failure
		if err := json.Unmarshal([]byte(refused.stdout), &failure); err != nil {
			t.Fatalf("the refusal does not decode: %v\n%s", err, refused.stdout)
		}
		if failure.Error != row {
			t.Fatalf("the refusal is %q, want %q", failure.Error, row)
		}
		tripwireQuiet(t, fired)
		got, _ := documentAt(t, subject)
		if got != dirty {
			t.Fatalf("init changed the disagreeing file:\n%s", got)
		}
		assertNoGate(t, h, subject)
	})

	t.Run("the checkout file removed", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		plant(t, subject, map[string]string{config.RepositoryDocument: commandDocument(t, "true")})
		commitAll(t, subject, "configure a command")
		git(t, subject, "push", "--quiet", "origin", "main")
		if err := os.Remove(filepath.Join(subject, config.RepositoryDocument)); err != nil {
			t.Fatalf("removing the checkout's configuration file: %v", err)
		}
		refused := run(t, h, subject, "--json", "init")
		if refused.code != machine.ExitFailure {
			t.Fatalf("init exited %s, want %s:\n%s%s", refused.code, machine.ExitFailure, refused.stdout, refused.stderr)
		}
		if _, there := documentAt(t, subject); there {
			t.Fatal("init recreated the configuration file it was refusing about")
		}
		assertNoGate(t, h, subject)
	})

	t.Run("copies that agree", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		subject := newSubject(t)
		plant(t, subject, map[string]string{config.RepositoryDocument: commandDocument(t, "true")})
		commitAll(t, subject, "configure a command")
		git(t, subject, "push", "--quiet", "origin", "main")
		got := mustInit(t, h, subject)
		answer := decodeInit(t, got).Commands
		if answer.Result != machine.CommandsNone {
			t.Fatalf("init offered %+v where the two copies already agree", answer)
		}
		if len(answer.Configured) == 0 || answer.Configured[0] != string(config.KeyCommandsTest) {
			t.Fatalf("init reports %v already configured, want %s", answer.Configured, config.KeyCommandsTest)
		}
	})
}

// TestInitRegistersAPlanningCheckoutForTheCoordinator copies the planning
// subject, whose files declare no command the detectors read, and checks that
// inspection does not run a planted executable, does not rewrite the
// configuration file, records one repository, and that the service's status
// — the read the coordinator uses — returns that same repository. The
// original checkout is not committed to and not pushed.
func TestInitRegistersAPlanningCheckoutForTheCoordinator(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P1)
	if _, err := os.Stat(quinceneraCheckout); err != nil {
		t.Skipf("the planning subject %s is not on this machine", quinceneraCheckout)
	}
	before := git(t, quinceneraCheckout, "rev-parse", "HEAD")
	if dirty := git(t, quinceneraCheckout, "status", "--porcelain"); dirty != "" {
		t.Fatalf("the planning subject is already dirty, so this test would not know what it changed:\n%s", dirty)
	}
	t.Cleanup(func() {
		if git(t, quinceneraCheckout, "rev-parse", "HEAD") != before {
			t.Errorf("the planning subject's HEAD moved")
		}
		if dirty := git(t, quinceneraCheckout, "status", "--porcelain"); dirty != "" {
			t.Errorf("the planning subject was written:\n%s", dirty)
		}
	})

	parent := t.TempDir()
	copyDir := filepath.Join(parent, "copy")
	bare := filepath.Join(parent, "origin.git")
	git(t, parent, "clone", "--no-local", "--quiet", quinceneraCheckout, copyDir)
	git(t, parent, "init", "--quiet", "--bare", "-b", "main", bare)
	git(t, copyDir, "remote", "set-url", "origin", bare)
	if got := git(t, copyDir, "remote", "get-url", "origin"); got != bare {
		t.Fatalf("the copy's origin is %q, want the local bare %q", got, bare)
	}
	resolved, err := filepath.EvalSymlinks(copyDir)
	if err != nil {
		t.Fatalf("resolving the copy: %v", err)
	}

	script, fired := plantTripwire(t, resolved)
	proveTripwireFires(t, script, fired)
	beforeFile, err := os.ReadFile(filepath.Join(resolved, config.RepositoryDocument))
	if err != nil {
		t.Fatalf("reading the planning subject's configuration file: %v", err)
	}

	h := newHome(t)
	first := decodeInit(t, mustInit(t, h, resolved))
	tripwireQuiet(t, fired)
	afterFile, err := os.ReadFile(filepath.Join(resolved, config.RepositoryDocument))
	if err != nil {
		t.Fatalf("reading the configuration file after init: %v", err)
	}
	if !bytes.Equal(beforeFile, afterFile) {
		t.Fatalf("init rewrote the planning subject's configuration file:\n%s", afterFile)
	}
	commands := first.Commands
	if len(commands.Proposed) != 0 {
		t.Fatalf("init detected commands in a planning checkout: %+v", commands.Proposed)
	}
	if commands.Result != machine.CommandsNone {
		t.Fatalf("the offer ended %q, want %q", commands.Result, machine.CommandsNone)
	}
	for _, key := range []config.Key{config.KeyCommandsTest, config.KeyCommandsLint} {
		found := false
		for _, configured := range commands.Configured {
			if configured == string(key) {
				found = true
			}
		}
		if !found {
			t.Errorf("init does not report %s as already configured: %v", key, commands.Configured)
		}
	}

	second := decodeInit(t, mustInit(t, h, resolved))
	if second.Repository.ID != first.Repository.ID || second.Gate.ID != first.Gate.ID {
		t.Fatalf("init again recorded gate %s repository %s, want %s and %s",
			second.Gate.ID, second.Repository.ID, first.Gate.ID, first.Repository.ID)
	}
	tripwireQuiet(t, fired)

	records := openStore(t, h)
	listed, err := records.Repositories(t.Context())
	if err != nil {
		_ = records.Close()
		t.Fatalf("listing repositories: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.Repository.ID {
		_ = records.Close()
		t.Fatalf("the store holds %+v, want the one repository init recorded", listed)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	serve(t, h)
	status := run(t, h, resolved, "--json", "status")
	if status.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s%s", status.code, status.stdout, status.stderr)
	}
	var report machine.Status
	if err := json.Unmarshal([]byte(status.stdout), &report); err != nil {
		t.Fatalf("status does not decode: %v\n%s", err, status.stdout)
	}
	if report.Repository == nil || report.Repository.ID != first.Repository.ID {
		t.Fatalf("status reports %+v, want repository %s", report.Repository, first.Repository.ID)
	}
}
