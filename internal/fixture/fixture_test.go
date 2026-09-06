package fixture_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
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
// so the failure is a result rather than a fatal. It is for the toolchain;
// git goes through gitIn.
func run(t *testing.T, dir, name string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// gitIn invokes git in dir the way the scenario was built, and returns its
// combined output along with whether it succeeded.
//
// The isolation is the point rather than a convenience. These assertions are
// what hold the plants, and reading them under the ambient environment would
// let a developer's own init.templateDir, core.hooksPath, commit.gpgsign, or
// rebase configuration decide what the assertions see, which is the thing the
// build takes the trouble to shut out.
func gitIn(t *testing.T, s fixture.Scenario, dir string, args ...string) (string, bool) {
	t.Helper()
	binary, env, err := s.GitInvocation()
	if err != nil {
		t.Fatalf("%v", err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// gitInUnder is gitIn with entries of the scenario's environment replaced, for
// the assertions that have to invoke git under a configuration file the
// scenario planted rather than under the one the build wrote.
func gitInUnder(t *testing.T, s fixture.Scenario, dir string, override []string, args ...string) (string, bool) {
	t.Helper()
	binary, env, err := s.GitInvocation()
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, kv := range override {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			t.Fatalf("the override %q is not KEY=VALUE, and applying it would replace whichever entry "+
				"happened to come first rather than the one it names", kv)
		}
		key := kv[:eq+1]
		replaced := false
		for i, existing := range env {
			if strings.HasPrefix(existing, key) {
				env[i], replaced = kv, true
				break
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
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
		out, ok := gitIn(t, s, s.WorkingCopy, "rev-parse", "--abbrev-ref", "HEAD")
		if !ok {
			t.Errorf("%s: reading the checked-out branch: %s", s.Name, out)
			continue
		}
		if got := strings.TrimSpace(out); got != f.Branch {
			t.Errorf("%s: the working copy is on %s, want %s", s.Name, got, f.Branch)
		}
		out, ok = gitIn(t, s, s.WorkingCopy, "ls-remote", "--heads", s.Origin)
		if !ok {
			t.Errorf("%s: reading the remote: %s", s.Name, out)
			continue
		}
		if !strings.Contains(out, "refs/heads/"+f.Branch) {
			t.Errorf("%s: %s does not hold %s: %s", s.Name, s.Origin, f.Branch, out)
		}
		// The default branch has to be there too: it is what the trusted
		// configuration is read from and what the branch is diffed against.
		if !strings.Contains(out, "refs/heads/"+f.DefaultBranch) {
			t.Errorf("%s: %s does not hold %s: %s", s.Name, s.Origin, f.DefaultBranch, out)
		}
		// ls-remote prints one identifier and one name per line, so two heads
		// is four fields. Anything else is a scenario carrying a branch the
		// catalog says nothing about.
		if got := len(strings.Fields(out)) / 2; got != 2 {
			t.Errorf("%s: %s holds %d heads, want exactly %s and %s: %s",
				s.Name, s.Origin, got, f.DefaultBranch, f.Branch, out)
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
	if out, ok := gitIn(t, s, s.Root, "clone", "--quiet", "--branch", f.DefaultBranch, s.Origin, clean); !ok {
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
	// Read out of the committed tree: the condition is about what the branch
	// carries, and this scenario's build touches the working copy after its
	// last commit, so the two are not the same thing.
	doc := showFile(t, s, f.Branch+":docs/behavior.md")
	code := showFile(t, s, f.Branch+":total.go")
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

	pushed := decodeConfig(t, showFile(t, s, f.Branch+":"+f.ConfigPath))
	trusted := decodeConfig(t, showFile(t, s, f.DefaultBranch+":"+f.ConfigPath))

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
		if mode := treeMode(t, s, f.Branch, path); mode != "100755" {
			t.Errorf("%s is committed with mode %q, want 100755: a harness distribution writes an "+
				"executable, and a plant that is not one is a weaker condition", path, mode)
		}
	}
	for _, path := range documents {
		if mode := treeMode(t, s, f.Branch, path); mode == "" {
			t.Errorf("%s is not committed on the branch", path)
		}
	}
	// The settings document is machine-consumed declarative output this
	// package writes, so it is decoded and asked what it binds rather than
	// searched for a path that could sit anywhere in it, including the env
	// block or an entry no harness would run.
	settings := decodeConfig(t, showFile(t, s, f.Branch+":.claude/settings.json"))
	if got := hookCommandsFor(t, settings, "SessionStart"); !contains(got, "sh .claude/hooks/session-start.sh") {
		t.Errorf("SessionStart binds %v, and none of it is a command hook running the planted script", got)
	}
	if got := hookCommandsFor(t, settings, "PreToolUse"); !contains(got, "sh .claude/hooks/pre-tool-use.sh") {
		t.Errorf("PreToolUse binds %v, and none of it is a command hook running the planted script", got)
	}
	// The one thing the installer puts in git config is core.hooksPath, local
	// to this working copy. It is what makes the two committed .githooks
	// scripts the hooks git runs here rather than two inert files.
	out, ok := gitIn(t, s, s.WorkingCopy, "config", "--local", "--get", "core.hooksPath")
	if !ok || strings.TrimSpace(out) != ".githooks" {
		t.Errorf("core.hooksPath is %q, want .githooks: without it the .githooks plant is two inert files",
			strings.TrimSpace(out))
	}
}

// hookCommandsFor returns the commands a settings document binds to an event
// through a hook whose type is "command", which is the only shape that runs
// anything. Anything else in the document is deliberately not reported.
func hookCommandsFor(t *testing.T, settings map[string]any, event string) []string {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	var commands []string
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		inner, _ := group["hooks"].([]any)
		for _, h := range inner {
			hook, _ := h.(map[string]any)
			if kind, _ := hook["type"].(string); kind != "command" {
				continue
			}
			if command, ok := hook["command"].(string); ok {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

func contains(all []string, want string) bool {
	for _, got := range all {
		if got == want {
			return true
		}
	}
	return false
}

// TestTheCheckAnswerNamesTheRecordedBranchHead holds the two together. The
// catalog tells a harness the answer carries Commits["branch-head"] and has to
// have it replaced with the commit the run pushed; an answer naming some other
// commit leaves that substitution with nothing to find, and the run then reads
// a stale check list rather than the planted condition.
//
// The answer is the exact bytes the provider command prints, which is the
// contract this package owns, so it is decoded and asked which head it names.
func TestTheCheckAnswerNamesTheRecordedBranchHead(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)

	path := s.ProviderResponses["checks-empty"]
	if path == "" {
		t.Fatal("the scenario names no empty check-list answer")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the check-list answer: %v", err)
	}
	var answer struct {
		HeadRefOid        string `json:"headRefOid"`
		StatusCheckRollup []any  `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode the check-list answer: %v\n%s", err, body)
	}
	recorded := s.Commits["branch-head"]
	if recorded == "" {
		t.Fatal("the scenario records no branch head for the answer to name")
	}
	if answer.HeadRefOid != recorded {
		t.Errorf("the check-list answer names %q and the scenario records the branch head as %q, so the "+
			"substitution the catalog requires would find nothing to replace",
			answer.HeadRefOid, recorded)
	}
	if len(answer.StatusCheckRollup) != 0 {
		t.Errorf("the rollup carries %d entries, and the condition is that it carries none",
			len(answer.StatusCheckRollup))
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
	body := showFile(t, malformed, f.DefaultBranch+":"+f.ConfigPath)
	var into any
	if err := json.Unmarshal([]byte(body), &into); err == nil {
		t.Errorf("the document planted as unparseable parses:\n%s", body)
	}

	unreadable := scenario(t, f, fixture.ScenarioUnreadableTrustedConfig)
	object, ok := gitIn(t, unreadable, unreadable.WorkingCopy, "rev-parse", "--verify", "--quiet",
		f.DefaultBranch+":"+f.ConfigPath)
	if !ok || strings.TrimSpace(object) == "" {
		t.Fatalf("the configuration path does not resolve, so the read would fail as a missing path "+
			"rather than as an unreadable one: %s", object)
	}
	out, ok := gitIn(t, unreadable, unreadable.WorkingCopy, "cat-file", "blob", strings.TrimSpace(object))
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

// TestOnlyTheDefaultBranchCarriesTheTrustedConfigurationFailure holds what
// makes those two conditions observable. If the branch carried the same broken
// document, a run that read the pushed copy as trusted would abort for the same
// reason and the harness would report a pass either way.
func TestOnlyTheDefaultBranchCarriesTheTrustedConfigurationFailure(t *testing.T) {
	f := readOnly(t)
	for _, name := range []fixture.ScenarioName{
		fixture.ScenarioUnparseableTrustedConfig,
		fixture.ScenarioUnreadableTrustedConfig,
	} {
		s := scenario(t, f, name)
		// The branch's copy is a document git reads and a decoder accepts,
		// which is what the default branch's copy is not.
		object, ok := gitIn(t, s, s.WorkingCopy, "rev-parse", "--verify", "--quiet",
			f.Branch+":"+f.ConfigPath)
		if !ok || strings.TrimSpace(object) == "" {
			t.Errorf("%s: the branch carries no configuration document at %s: %s", name, f.ConfigPath, object)
			continue
		}
		body, ok := gitIn(t, s, s.WorkingCopy, "cat-file", "blob", strings.TrimSpace(object))
		if !ok {
			t.Errorf("%s: the branch's configuration document does not read as one: %s", name, body)
			continue
		}
		branchAgent, _ := decodeConfig(t, body)["agent"].(string)
		trustedAgent, _ := decodeConfig(t, subjectTrustedDocument(t, f))["agent"].(string)
		if branchAgent == "" || branchAgent == trustedAgent {
			t.Errorf("%s: the branch names agent %q and a well-formed trusted document names %q, so a run "+
				"that read the pushed copy as trusted is not distinguishable from one that refused",
				name, branchAgent, trustedAgent)
		}
	}
}

// subjectTrustedDocument returns a well-formed trusted document from a scenario
// whose default-branch copy is not the planted condition, which is where the
// agent the other two are compared against comes from.
func subjectTrustedDocument(t *testing.T, f *fixture.Fixture) string {
	t.Helper()
	s := scenario(t, f, fixture.ScenarioBase)
	return showFile(t, s, f.DefaultBranch+":"+f.ConfigPath)
}

// TestTheBranchHasNoDiffOnceItIsRebased runs the rebase rather than asserting
// that it would empty the branch. An empty commit planted directly is a state
// a rebase does not produce, and the short-circuit is about the state a rebase
// does produce.
func TestTheBranchHasNoDiffOnceItIsRebased(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioEmptyAfterRebase)

	before, ok := gitIn(t, s, s.WorkingCopy, "diff", "--name-only", f.DefaultBranch+"..."+f.Branch)
	if !ok {
		t.Fatalf("diff before the rebase: %s", before)
	}
	if strings.TrimSpace(before) == "" {
		t.Fatal("the branch already has no diff before it is rebased, so the rebase is not what empties it")
	}
	if out, ok := gitIn(t, s, s.WorkingCopy, "rebase", f.DefaultBranch); !ok {
		t.Fatalf("rebase the branch: %s", out)
	}
	after, ok := gitIn(t, s, s.WorkingCopy, "diff", "--name-only", f.DefaultBranch+"..."+f.Branch)
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
	config := s.Paths["gitconfig-template-mixed"]
	if template == "" || config == "" {
		t.Fatal("the scenario names no template directory and configuration file")
	}
	// The configuration file is the channel the condition rests on, so what it
	// selects is read back out of git rather than out of the file's bytes: a
	// value git refuses to parse, or parses into some other path, is the whole
	// failure mode and looks identical to a substring search.
	out, ok := gitIn(t, s, s.Root, "config", "--file", config, "--get", "init.templateDir")
	if !ok {
		t.Fatalf("git cannot read init.templateDir out of %s: %s", config, out)
	}
	if got, want := strings.TrimSpace(out), filepath.ToSlash(template); got != want {
		t.Fatalf("%s points init.templateDir at %q, want %q", config, got, want)
	}
	// And a repository born under that file alone, with no --template flag,
	// has to carry the hooks. That is the channel the gate meets.
	born := filepath.Join(t.TempDir(), "born.git")
	if out, ok := gitInUnder(t, s, s.Root, []string{"GIT_CONFIG_GLOBAL=" + config},
		"init", "--bare", "--quiet", born); !ok {
		t.Fatalf("create a repository under the planted configuration: %s", out)
	}
	for _, name := range []string{"pre-receive", "post-update", "update"} {
		if _, err := os.Stat(filepath.Join(born, "hooks", name)); err != nil {
			t.Errorf("a repository created under %s does not carry %s, so the configuration channel "+
				"selects nothing: %v", config, name, err)
		}
	}
}

// TestTheHooksPathRedirectIsTheOneGateDocumentsAsOpen holds the plant for the
// gap internal/gate/doc.go names. The condition is that a configuration file
// reached through a kept variable moves where git looks for hooks, so it is
// checked by asking git where it looks, not by reading the file back.
func TestTheHooksPathRedirectIsTheOneGateDocumentsAsOpen(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioHostileTemplate)
	hostile := s.Paths["hostile-hooks"]
	config := s.Paths["gitconfig-hostile-hooks"]
	if hostile == "" || config == "" {
		t.Fatal("the scenario names no hostile hooks directory and configuration file")
	}
	hooks := filepath.Join(hostile, "hooks")
	// Both names internal/gate installs are planted, or the redirect would
	// leave one of the gate's own hooks still running.
	for _, name := range []string{"pre-receive", "post-receive"} {
		if _, err := os.Stat(filepath.Join(hooks, name)); err != nil {
			t.Errorf("the hostile hooks directory does not carry %s: %v", name, err)
		}
	}
	repo := filepath.Join(t.TempDir(), "gate.git")
	if out, ok := gitIn(t, s, s.Root, "init", "--bare", "--quiet", repo); !ok {
		t.Fatalf("create a repository to stand in for a gate: %s", out)
	}
	out, ok := gitInUnder(t, s, repo, []string{"GIT_CONFIG_GLOBAL=" + config}, "rev-parse", "--git-path", "hooks")
	if !ok {
		t.Fatalf("ask git where it looks for hooks: %s", out)
	}
	if got, want := filepath.ToSlash(strings.TrimSpace(out)), filepath.ToSlash(hooks); got != want {
		t.Fatalf("under %s git looks for hooks in %q, want %q: the redirect the condition rests on is "+
			"not in force, so a gate's own hooks would still run", config, got, want)
	}
}

// TestTheOutOfBandAdvanceMovesTheBranchOnTheRemote holds the deferred plant. A
// plant that reported success over a push that changed nothing would leave a
// harness reporting that P6 held when P6 was never asked.
func TestTheOutOfBandAdvanceMovesTheBranchOnTheRemote(t *testing.T) {
	f := build(t)
	s := scenario(t, f, fixture.ScenarioRemoteAdvanced)

	before, ok := gitIn(t, s, s.WorkingCopy, "ls-remote", s.Origin, "refs/heads/"+f.Branch)
	if !ok {
		t.Fatalf("read the remote before the advance: %s", before)
	}
	landed, err := fixture.AdvanceRemoteOutOfBand(s)
	if err != nil {
		t.Fatalf("advance the remote: %v", err)
	}
	after, ok := gitIn(t, s, s.WorkingCopy, "ls-remote", s.Origin, "refs/heads/"+f.Branch)
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
	if out, ok := gitIn(t, s, s.WorkingCopy, "merge-base", "--is-ancestor", landed, "HEAD"); ok {
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

	// The remote name is the one fact this package restates rather than
	// imports, so the refusal is asked to name gate.RemoteName. Restating it
	// keeps the fixture from being built out of the package it is a fixture
	// for; this is what keeps the restatement honest.
	if _, err := fixture.CopyGatedWorkingCopy(s); err == nil {
		t.Fatal("copying succeeded over a working copy with no gate remote to inherit")
	} else if !strings.Contains(err.Error(), gate.RemoteName) {
		t.Fatalf("the fixture looks for a remote other than %s: %v", gate.RemoteName, err)
	}

	// Stand in for what an initialization writes, using git directly. This
	// test is about the fixture's own helper, so the remote is written here
	// rather than by the package the helper is a fixture for.
	pretendGate := filepath.Join(t.TempDir(), "gate.git")
	if out, ok := gitIn(t, s, s.Root, "init", "--bare", "--quiet", pretendGate); !ok {
		t.Fatalf("create a repository to stand in for a gate: %s", out)
	}
	if out, ok := gitIn(t, s, s.WorkingCopy, "remote", "add", gate.RemoteName, pretendGate); !ok {
		t.Fatalf("add the gate remote: %s", out)
	}
	copied, err := fixture.CopyGatedWorkingCopy(s)
	if err != nil {
		t.Fatalf("copy the gated working copy: %v", err)
	}
	out, ok := gitIn(t, s, copied, "config", "--get", "remote."+gate.RemoteName+".url")
	if !ok || strings.TrimSpace(out) != pretendGate {
		t.Fatalf("the copy's %s remote is %q, want %q", gate.RemoteName, strings.TrimSpace(out), pretendGate)
	}
	if _, err := os.Stat(s.WorkingCopy); err != nil {
		t.Fatalf("the original no longer stands, and a copy is only distinguishable from a move while "+
			"it does: %v", err)
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

// showFile reads a path out of a commit in the scenario's working copy, which
// is what a run reads: a file in the working directory is not what was pushed.
func showFile(t *testing.T, s fixture.Scenario, spec string) string {
	t.Helper()
	out, ok := gitIn(t, s, s.WorkingCopy, "show", spec)
	if !ok {
		t.Fatalf("git show %s in %s: %s", spec, s.WorkingCopy, out)
	}
	return out
}

// treeMode returns the mode a path is committed with, empty when the branch
// does not carry it.
func treeMode(t *testing.T, s fixture.Scenario, ref, path string) string {
	t.Helper()
	out, ok := gitIn(t, s, s.WorkingCopy, "ls-tree", ref, "--", path)
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

// TestTheP3ResponsesProduceAnAskOnTheEntryPointEachNames drives the planted
// bytes through the real parser each condition names. The catalog records that
// a report carrying no action, an empty one, or an unreadable one parses and
// the finding survives as an ask, and the review-path variants record the same
// outcome through findings.ParseReviewReport, where PRD section 5's evidence
// binding decides first. A recorded expectation nothing ever produced is a
// claim, so it is produced here.
//
// It also drives each variant through the other variant's entry point, because
// the reason there are two is that the entry points differ: the review path
// refuses bytes stating no revision before any finding is reached, which is
// what the findings.ParseReport conditions say about themselves and is exactly
// the gap the review-path variants exist to close.
func TestTheP3ResponsesProduceAnAskOnTheEntryPointEachNames(t *testing.T) {
	f := readOnly(t)
	s := scenario(t, f, fixture.ScenarioBase)

	demand := findings.Demand{
		Revision: s.Commits["branch-head"],
		Touched:  []string{"total.go", "docs/behavior.md"},
	}
	if demand.Revision == "" {
		t.Fatal("the scenario records no branch head for a review demand to name")
	}

	var seen int
	for _, c := range f.Conditions {
		if c.Principle != "P3" || c.Scenario != fixture.ScenarioBase {
			continue
		}
		seen++
		path := s.AgentResponses[string(c.ID)]
		if path == "" {
			t.Errorf("%s: the scenario names no agent response", c.ID)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read the response: %v", c.ID, err)
			continue
		}
		raw := string(body)

		if strings.HasSuffix(string(c.ID), "-review-path") {
			report, binding, err := findings.ParseReviewReport(raw, demand)
			if err != nil {
				t.Errorf("%s: the review path refused the bytes planted for it: %v", c.ID, err)
				continue
			}
			if len(binding.Refused) != 0 || len(binding.Demoted) != 0 {
				t.Errorf("%s: the binding refused %d finding(s) and demoted %d, and the condition is "+
					"that nothing but the action decides the outcome",
					c.ID, len(binding.Refused), len(binding.Demoted))
			}
			requireAskOnTheLoopBound(t, c.ID, report)
			continue
		}

		report, err := findings.ParseReport(raw)
		if err != nil {
			t.Errorf("%s: the ordinary path refused the bytes planted for it: %v", c.ID, err)
			continue
		}
		requireAskOnTheLoopBound(t, c.ID, report)

		if _, _, err := findings.ParseReviewReport(raw, demand); !errors.Is(err, findings.ErrWrongRevision) {
			t.Errorf("%s: driven through the review path these bytes gave %v, and the condition "+
				"states they meet findings.ErrWrongRevision there", c.ID, err)
		}
	}
	if seen != 6 {
		t.Errorf("the catalog carries %d P3 conditions in the base scenario, and there are three "+
			"shapes on each of two entry points", seen)
	}
}

// requireAskOnTheLoopBound asserts the reviewer's own finding survived the
// parse as an ask. It is looked up by the identifier the reviewer wrote, so
// the informational findings the review path adds are not mistaken for it.
func requireAskOnTheLoopBound(t *testing.T, id fixture.ID, report findings.Report) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.ID != "total-loop-bound" {
			continue
		}
		if finding.Action != findings.ActionAsk {
			t.Errorf("%s: the finding's action is %q and P3 fixes it at %q",
				id, finding.Action, findings.ActionAsk)
		}
		return
	}
	t.Errorf("%s: the parsed report carries no finding identified as total-loop-bound, so the "+
		"finding did not survive", id)
}
