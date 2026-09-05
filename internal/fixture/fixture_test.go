package fixture_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/gate"
)

// build builds a whole fixture for one test. Every assertion below is against
// a fixture built from nothing in this run, which is the property the fixture
// exists to have. A test that changes what it was given builds its own; a test
// that only reads takes the shared one, because building is the slow part and
// twelve builds of the same thing prove nothing twelve times.
func build(t *testing.T) *fixture.Fixture {
	t.Helper()
	requireGit(t)
	built, err := fixture.Build(filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatalf("build the fixture: %v", err)
	}
	return built
}

// shared is the fixture the read-only tests use, built once by TestMain.
var (
	shared    *fixture.Fixture
	sharedErr error
)

// TestMain builds the shared fixture and removes it afterwards. It is built
// here rather than lazily so that a failure to build is one reported failure
// rather than one per test.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err == nil {
		root, err := os.MkdirTemp("", "fixture-shared")
		if err != nil {
			panic(err)
		}
		shared, sharedErr = fixture.Build(filepath.Join(root, "fixture"))
		code := m.Run()
		_ = os.RemoveAll(root)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// readOnly returns the shared fixture, for a test that does not change it.
func readOnly(t *testing.T) *fixture.Fixture {
	t.Helper()
	requireGit(t)
	if sharedErr != nil {
		t.Fatalf("build the fixture: %v", sharedErr)
	}
	if shared == nil {
		t.Skip("the shared fixture was not built")
	}
	return shared
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
}

// run invokes a command in dir and returns its combined output along with
// whether it succeeded. Several assertions here are about a command failing,
// so the failure is a result rather than a fatal.
func run(t *testing.T, dir, name string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func scenario(t *testing.T, f *fixture.Fixture, name fixture.ScenarioName) fixture.Scenario {
	t.Helper()
	s, ok := f.Scenario(name)
	if !ok {
		t.Fatalf("the fixture has no scenario %s", name)
	}
	return s
}

func condition(t *testing.T, f *fixture.Fixture, id fixture.ID) fixture.Condition {
	t.Helper()
	c, ok := f.Condition(id)
	if !ok {
		t.Fatalf("the catalog has no condition %s", id)
	}
	return c
}

// TestEveryConditionNamesAScenarioThatExists holds the catalog against the
// scenarios on disk. A condition recorded against a scenario nobody built is a
// condition a harness would report on and never reach.
func TestEveryConditionNamesAScenarioThatExists(t *testing.T) {
	f := readOnly(t)
	if len(f.Conditions) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, c := range f.Conditions {
		if _, ok := f.Scenario(c.Scenario); !ok {
			t.Errorf("condition %s is recorded against scenario %s, which was not built", c.ID, c.Scenario)
		}
		if c.Expect.Summary == "" {
			t.Errorf("condition %s records no expected outcome", c.ID)
		}
		if c.Mechanism == "" {
			t.Errorf("condition %s names no mechanism to answer it", c.ID)
		}
	}
}

// TestEveryRefusalThatIsADeadEndNamesAnAction holds the part of a refusal that
// is easy to leave out. A refusal an operator cannot act on is what makes
// somebody take the destructive step by hand.
func TestEveryRefusalThatIsADeadEndNamesAnAction(t *testing.T) {
	f := readOnly(t)
	for _, c := range f.Conditions {
		if c.Expect.NamesAction == "" {
			continue
		}
		if c.Expect.ActionSucceeds == "" {
			t.Errorf("condition %s expects a refusal to name an action and does not say why that action "+
				"succeeds from the state the reader is in", c.ID)
		}
	}
}

// TestEveryScenarioHasAWorkingCopyOnTheBranchAndARemoteHoldingIt checks the
// shape every scenario claims: a working copy whose origin is the bare
// repository beside it, with the branch under validation published there.
func TestEveryScenarioHasAWorkingCopyOnTheBranchAndARemoteHoldingIt(t *testing.T) {
	f := readOnly(t)
	for _, s := range f.Scenarios {
		out, ok := run(t, s.WorkingCopy, "git", "rev-parse", "--abbrev-ref", "HEAD")
		if !ok {
			t.Errorf("%s: reading the checked-out branch: %s", s.Name, out)
			continue
		}
		if got := strings.TrimSpace(out); got != f.Branch {
			t.Errorf("%s: the working copy is on %s, want %s", s.Name, got, f.Branch)
		}
		out, ok = run(t, s.WorkingCopy, "git", "ls-remote", "--heads", s.Origin, f.Branch)
		if !ok {
			t.Errorf("%s: reading the remote: %s", s.Name, out)
			continue
		}
		if !strings.Contains(out, "refs/heads/"+f.Branch) {
			t.Errorf("%s: %s does not hold %s: %s", s.Name, s.Origin, f.Branch, out)
		}
		if !strings.Contains(out, "refs/heads/"+f.Branch) {
			t.Errorf("%s: the branch was not published", s.Name)
		}
	}
}

// TestTheDefaultBranchIsGreenAndTheBranchIsNot is the base scenario's stage
// plants held against the commands the trusted configuration names. Without
// this, a stage plant is a claim about code nobody ran.
func TestTheDefaultBranchIsGreenAndTheBranchIsNot(t *testing.T) {
	requireGo(t)
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)

	// The default branch is the control. A stage plant proves nothing if the
	// same command also fails on the branch it was planted against.
	clean := filepath.Join(t.TempDir(), "default-branch")
	if out, ok := run(t, s.Root, "git", "clone", "--quiet", "--branch", f.DefaultBranch, s.Origin, clean); !ok {
		t.Fatalf("clone the default branch: %s", out)
	}
	if out, ok := run(t, clean, "go", "test", "./..."); !ok {
		t.Fatalf("the default branch's tests are expected to pass and did not: %s", out)
	}
	if out, ok := run(t, clean, "go", "vet", "./..."); !ok {
		t.Fatalf("the default branch is expected to be clean and is not: %s", out)
	}

	out, ok := run(t, s.WorkingCopy, "go", "test", "./...")
	if ok {
		t.Fatalf("the branch's tests are expected to fail and passed: %s", out)
	}
	for _, want := range condition(t, f, "stage-failing-test").Expect.MessageContains {
		if !strings.Contains(out, want) {
			t.Errorf("the failing test does not report %q; it reported:\n%s", want, out)
		}
	}
	if strings.Count(out, "--- FAIL:") != 1 {
		t.Errorf("exactly one test is planted to fail, and %d did:\n%s", strings.Count(out, "--- FAIL:"), out)
	}

	out, ok = run(t, s.WorkingCopy, "go", "vet", "./...")
	if ok {
		t.Fatalf("the branch is expected to fail vet and did not: %s", out)
	}
	for _, want := range condition(t, f, "stage-lint-violation").Expect.MessageContains {
		if !strings.Contains(out, want) {
			t.Errorf("the lint violation does not report %q; it reported:\n%s", want, out)
		}
	}
}

// TestTheDocumentationIsStaleAgainstTheBranch holds the stale-documentation
// plant as a fact about the two files rather than as a claim in the catalog.
func TestTheDocumentationIsStaleAgainstTheBranch(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)
	doc := readFile(t, filepath.Join(s.WorkingCopy, "docs", "behavior.md"))
	code := readFile(t, filepath.Join(s.WorkingCopy, "total.go"))
	if !strings.Contains(doc, "`bytes`") {
		t.Errorf("docs/behavior.md no longer documents the old default, so nothing is stale:\n%s", doc)
	}
	if !strings.Contains(code, `unit = "B"`) {
		t.Errorf("total.go no longer changes the default, so the document is not stale:\n%s", code)
	}
}

// TestTheBranchSetsTheKeysAPushedBranchMayNotSet holds the P7 plant against
// both documents, because the condition is that they differ.
func TestTheBranchSetsTheKeysAPushedBranchMayNotSet(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)

	pushed := decodeConfig(t, showFile(t, s.WorkingCopy, f.Branch+":"+f.ConfigPath))
	trusted := decodeConfig(t, showFile(t, s.WorkingCopy, f.DefaultBranch+":"+f.ConfigPath))

	if pushed["agent"] == trusted["agent"] {
		t.Errorf("both documents name the same agent, so which one was read is not observable: %v", pushed["agent"])
	}
	pushedCommands, _ := pushed["commands"].(map[string]any)
	trustedCommands, _ := trusted["commands"].(map[string]any)
	for _, key := range []string{"test", "lint"} {
		if pushedCommands[key] == trustedCommands[key] {
			t.Errorf("both documents name the same commands.%s, so which one was read is not observable", key)
		}
	}
	if _, ok := pushed["ignore_patterns"]; !ok {
		t.Error("the pushed document sets no key a pushed branch may set, so dropping the whole document " +
			"and applying the trust classes are indistinguishable")
	}
	if _, ok := trusted["ignore_patterns"]; ok {
		t.Error("the trusted document also sets ignore_patterns, so the pushed value winning is not observable")
	}
}

// TestTheHarnessInstallationIsCommittedInTheShapeADistributionWrites is the
// heart of the harness-installation condition. It checks the tree, not the
// working directory, and it checks the mode: a guard written against git
// config alone passes while this walks through, and a plant that committed
// these as non-executable text would be a different, weaker condition.
func TestTheHarnessInstallationIsCommittedInTheShapeADistributionWrites(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)

	executables := []string{
		".claude/hooks/session-start.sh",
		".claude/hooks/pre-tool-use.sh",
		".githooks/pre-commit",
		".githooks/pre-push",
		".assistant-harness/bin/fixture-pushed-agent",
		".envrc",
	}
	documents := []string{
		".claude/settings.json",
		".claude/agents/gate-reviewer.md",
		".claude/commands/validate.md",
		".assistant-harness/harness.toml",
		"AGENTS.md",
		"CLAUDE.md",
	}
	for _, path := range executables {
		if mode := treeMode(t, s.WorkingCopy, f.Branch, path); mode != "100755" {
			t.Errorf("%s is committed with mode %q, want 100755: a harness distribution writes an "+
				"executable, and a plant that is not one is a weaker condition", path, mode)
		}
	}
	for _, path := range documents {
		if mode := treeMode(t, s.WorkingCopy, f.Branch, path); mode == "" {
			t.Errorf("%s is not committed on the branch", path)
		}
	}
	// None of it is in git config, which is what makes the condition distinct
	// from the git-config plants. The one thing the installer does put in git
	// config is core.hooksPath, and that is the documented open gap.
	settings := showFile(t, s.WorkingCopy, f.Branch+":.claude/settings.json")
	if !strings.Contains(settings, ".claude/hooks/session-start.sh") {
		t.Errorf("the settings document does not bind the planted hook to anything:\n%s", settings)
	}
	out, ok := run(t, s.WorkingCopy, "git", "config", "--local", "--get", "core.hooksPath")
	if !ok || strings.TrimSpace(out) != ".githooks" {
		t.Errorf("core.hooksPath is %q, want .githooks: without it the .githooks plant is two inert files",
			strings.TrimSpace(out))
	}
}

// TestAPlantedExecutableRecordsHavingRun is the guard on the guard. Every
// harness-installation expectation is "the tripwire file does not exist", and
// a tripwire that could not fire would make that check pass while proving
// nothing.
func TestAPlantedExecutableRecordsHavingRun(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioBase)

	if _, exists, err := s.Fired(); err != nil {
		t.Fatalf("read the tripwire file: %v", err)
	} else if exists {
		t.Fatal("the tripwire file exists in a freshly built scenario, so it cannot report that " +
			"something ran")
	}
	script := filepath.Join(s.WorkingCopy, ".claude", "hooks", "session-start.sh")
	if out, ok := run(t, s.WorkingCopy, "sh", script); !ok {
		t.Fatalf("running the planted hook: %s", out)
	}
	fired, exists, err := s.Fired()
	if err != nil {
		t.Fatalf("read the tripwire file: %v", err)
	}
	if !exists || len(fired) != 1 || fired[0] != "claude-hook-session-start" {
		t.Fatalf("running the planted hook recorded %v (exists=%v), want one entry naming it",
			fired, exists)
	}
}

// TestTheTrustedConfigurationIsUnparseableAndUnreadableWhereItIsPlanted holds
// the two abort conditions as facts about the documents rather than as claims.
func TestTheTrustedConfigurationIsUnparseableAndUnreadableWhereItIsPlanted(t *testing.T) {
	f := readOnly(t)

	malformed := scenario(t, f, fixture.ScenarioUnparseableTrustedConfig)
	body := showFile(t, malformed.WorkingCopy, f.DefaultBranch+":"+f.ConfigPath)
	var into any
	if err := json.Unmarshal([]byte(body), &into); err == nil {
		t.Errorf("the document planted as unparseable parses:\n%s", body)
	}

	unreadable := scenario(t, f, fixture.ScenarioUnreadableTrustedConfig)
	object, ok := run(t, unreadable.WorkingCopy, "git", "rev-parse", "--verify", "--quiet",
		f.DefaultBranch+":"+f.ConfigPath)
	if !ok || strings.TrimSpace(object) == "" {
		t.Fatalf("the configuration path does not resolve, so the read would fail as a missing path "+
			"rather than as an unreadable one: %s", object)
	}
	out, ok := run(t, unreadable.WorkingCopy, "git", "cat-file", "blob", strings.TrimSpace(object))
	if ok {
		t.Fatalf("the configuration path reads as a document, so nothing is unreadable:\n%s", out)
	}
	// Only the part of the expected message that is git's own is checkable
	// here. The rest is internal/vcs's wrapping, which this test does not
	// invoke and must not claim to have seen.
	if !strings.Contains(out, "bad file") {
		t.Errorf("the unreadable path does not report %q; git reported:\n%s", "bad file", out)
	}
}

// TestTheBranchHasNoDiffOnceItIsRebased runs the rebase rather than asserting
// that it would empty the branch. An empty commit planted directly is a state
// a rebase does not produce, and the short-circuit is about the state a rebase
// does produce.
func TestTheBranchHasNoDiffOnceItIsRebased(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioEmptyAfterRebase)

	before, ok := run(t, s.WorkingCopy, "git", "diff", "--name-only", f.DefaultBranch+"..."+f.Branch)
	if !ok {
		t.Fatalf("diff before the rebase: %s", before)
	}
	if strings.TrimSpace(before) == "" {
		t.Fatal("the branch already has no diff before it is rebased, so the rebase is not what empties it")
	}
	if out, ok := run(t, s.WorkingCopy, "git", "-c", "user.name=T", "-c", "user.email=t@t.invalid",
		"rebase", f.DefaultBranch); !ok {
		t.Fatalf("rebase the branch: %s", out)
	}
	after, ok := run(t, s.WorkingCopy, "git", "diff", "--name-only", f.DefaultBranch+"..."+f.Branch)
	if !ok {
		t.Fatalf("diff after the rebase: %s", after)
	}
	if strings.TrimSpace(after) != "" {
		t.Fatalf("the rebased branch still has a diff, so the short-circuit would not fire:\n%s", after)
	}
}

// TestTheHostileTemplateWouldPutAHookInARepositoryBornFromIt checks that the
// template is one git would actually use, by creating a repository from it.
// A template directory whose hooks git ignores would leave the gate's refusal
// with nothing to refuse.
func TestTheHostileTemplateWouldPutAHookInARepositoryBornFromIt(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioHostileTemplate)
	template := s.Paths["template-mixed"]
	if template == "" {
		t.Fatal("the scenario names no template directory")
	}
	born := filepath.Join(t.TempDir(), "born.git")
	if out, ok := run(t, s.Root, "git", "init", "--bare", "--quiet",
		"--template="+template, born); !ok {
		t.Fatalf("create a repository from the template: %s", out)
	}
	for _, name := range []string{"pre-receive", "post-update", "update"} {
		if _, err := os.Stat(filepath.Join(born, "hooks", name)); err != nil {
			t.Errorf("a repository born from the template does not carry %s: %v", name, err)
		}
	}
	// The configuration file is the channel that is open, so it has to select
	// the template on its own.
	config := s.Paths["gitconfig-template-mixed"]
	if !strings.Contains(readFile(t, config), template) {
		t.Errorf("%s does not point init.templateDir at %s", config, template)
	}
}

// TestTheOutOfBandAdvanceMovesTheBranchOnTheRemote holds the deferred plant. A
// plant that reported success over a push that changed nothing would leave a
// harness reporting that P6 held when P6 was never asked.
func TestTheOutOfBandAdvanceMovesTheBranchOnTheRemote(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioRemoteAdvanced)

	before, ok := run(t, s.WorkingCopy, "git", "ls-remote", s.Origin, "refs/heads/"+f.Branch)
	if !ok {
		t.Fatalf("read the remote before the advance: %s", before)
	}
	landed, err := fixture.AdvanceRemoteOutOfBand(s)
	if err != nil {
		t.Fatalf("advance the remote: %v", err)
	}
	after, ok := run(t, s.WorkingCopy, "git", "ls-remote", s.Origin, "refs/heads/"+f.Branch)
	if !ok {
		t.Fatalf("read the remote after the advance: %s", after)
	}
	if strings.TrimSpace(before) == strings.TrimSpace(after) {
		t.Fatal("the branch on the remote did not move")
	}
	if !strings.Contains(after, landed) {
		t.Fatalf("the branch stands at %s and the advance reported %s", strings.TrimSpace(after), landed)
	}
	// The commit the run would drop has to be one the branch's own history
	// does not contain, or there is nothing to discard.
	if out, ok := run(t, s.WorkingCopy, "git", "merge-base", "--is-ancestor", landed, "HEAD"); ok {
		t.Fatalf("the landed commit is already contained in the run's branch, so nothing would be "+
			"discarded: %s", out)
	}
}

// TestCopyingRefusesUntilThereIsARemoteToInherit holds the ordering the copied
// working copy condition rests on. A copy taken before a gate exists inherits
// nothing, and a fixture that produced one anyway would hand the harness a
// scenario missing the fact the ownership question turns on.
func TestCopyingRefusesUntilThereIsARemoteToInherit(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioCopiedWorkingCopy)

	if _, err := fixture.CopyGatedWorkingCopy(s); err == nil {
		t.Fatal("copying succeeded over a working copy with no gate remote to inherit")
	}

	// Stand in for what an initialization writes, using git directly. This
	// test is about the fixture's own helper, so the remote is written here
	// rather than by the package the helper is a fixture for.
	pretendGate := filepath.Join(t.TempDir(), "gate.git")
	if out, ok := run(t, s.Root, "git", "init", "--bare", "--quiet", pretendGate); !ok {
		t.Fatalf("create a repository to stand in for a gate: %s", out)
	}
	if out, ok := run(t, s.WorkingCopy, "git", "remote", "add", gate.RemoteName, pretendGate); !ok {
		t.Fatalf("add the gate remote: %s", out)
	}
	copied, err := fixture.CopyGatedWorkingCopy(s)
	if err != nil {
		t.Fatalf("copy the gated working copy: %v", err)
	}
	out, ok := run(t, copied, "git", "config", "--get", "remote."+gate.RemoteName+".url")
	if !ok || strings.TrimSpace(out) != pretendGate {
		t.Fatalf("the copy's %s remote is %q, want %q", gate.RemoteName, strings.TrimSpace(out), pretendGate)
	}
	if _, err := os.Stat(s.WorkingCopy); err != nil {
		t.Fatalf("the original no longer stands, and a copy is only distinguishable from a move while "+
			"it does: %v", err)
	}
}

// TestTheRemoteNameThisPackagePlantsIsTheOneTheGateUses holds the one fact
// this package restates rather than imports. It is restated so that a fixture
// is not built out of the package it is a fixture for; this is what keeps the
// restatement honest.
func TestTheRemoteNameThisPackagePlantsIsTheOneTheGateUses(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioCopiedWorkingCopy)
	if _, err := fixture.CopyGatedWorkingCopy(s); err == nil {
		t.Fatal("copying succeeded with no gate remote")
	} else if !strings.Contains(err.Error(), gate.RemoteName) {
		t.Fatalf("the fixture looks for a remote other than %s: %v", gate.RemoteName, err)
	}
}

// TestTheManifestRoundTrips holds the form the harness consumes.
func TestTheManifestRoundTrips(t *testing.T) {
	f := readOnly(t)
	path, err := f.WriteManifest()
	if err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	read, err := fixture.ReadManifest(path)
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	if len(read.Conditions) != len(f.Conditions) || len(read.Scenarios) != len(f.Scenarios) {
		t.Fatalf("the manifest holds %d conditions and %d scenarios, want %d and %d",
			len(read.Conditions), len(read.Scenarios), len(f.Conditions), len(f.Scenarios))
	}
	if len(read.OpenQuestions) == 0 {
		t.Error("the manifest carries no open questions, so a harness reading it would rediscover them")
	}
	for _, c := range f.Conditions {
		got, ok := read.Condition(c.ID)
		if !ok {
			t.Errorf("condition %s did not survive the manifest", c.ID)
			continue
		}
		if got.Expect.Summary != c.Expect.Summary {
			t.Errorf("condition %s came back with a different expected outcome", c.ID)
		}
	}
}

// TestBuildingOverSomethingIsRefused holds the refusal that keeps a fixture
// from being partly this build and partly whatever was already there.
func TestBuildingOverSomethingIsRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "something"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write into the root: %v", err)
	}
	if _, err := fixture.Build(root); err == nil {
		t.Fatal("building into a directory that already holds something succeeded")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// showFile reads a path out of a commit, which is what a run reads: a file in
// the working directory is not what was pushed.
func showFile(t *testing.T, dir, spec string) string {
	t.Helper()
	out, ok := run(t, dir, "git", "show", spec)
	if !ok {
		t.Fatalf("git show %s in %s: %s", spec, dir, out)
	}
	return out
}

// treeMode returns the mode a path is committed with, empty when the branch
// does not carry it.
func treeMode(t *testing.T, dir, ref, path string) string {
	t.Helper()
	out, ok := run(t, dir, "git", "ls-tree", ref, "--", path)
	if !ok {
		t.Fatalf("git ls-tree %s -- %s: %s", ref, path, out)
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func decodeConfig(t *testing.T, body string) map[string]any {
	t.Helper()
	var into map[string]any
	if err := json.Unmarshal([]byte(body), &into); err != nil {
		t.Fatalf("decode the configuration document: %v\n%s", err, body)
	}
	return into
}
