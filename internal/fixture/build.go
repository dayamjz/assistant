package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ScenarioName names one scenario. There is more than one because the
// conditions are not all compatible: a run whose trusted configuration cannot
// be parsed aborts before a stage runs, and a run whose push is refused never
// reaches the stages after it, so planting those alongside the stage
// conditions would hide the stage conditions rather than test them.
type ScenarioName string

const (
	// ScenarioBase carries every condition a run can meet while still
	// reaching the end of the pipeline.
	ScenarioBase ScenarioName = "base"
	// ScenarioRemoteAdvanced carries the out-of-band remote advance, whose
	// refusal stops the run at the push stage.
	ScenarioRemoteAdvanced ScenarioName = "remote-advanced"
	// ScenarioEmptyAfterRebase carries a branch whose change is already on the
	// default branch, so a real rebase leaves it with no diff.
	ScenarioEmptyAfterRebase ScenarioName = "empty-after-rebase"
	// ScenarioUnparseableTrustedConfig carries a default branch whose
	// configuration document is not well-formed JSON.
	ScenarioUnparseableTrustedConfig ScenarioName = "unparseable-trusted-config"
	// ScenarioUnreadableTrustedConfig carries a default branch where the
	// configuration path is a directory, so the document cannot be read at
	// all rather than read and refused.
	ScenarioUnreadableTrustedConfig ScenarioName = "unreadable-trusted-config"
	// ScenarioHostileTemplate carries the git template a gate must not be born
	// from, reachable both through init.templateDir and through
	// GIT_TEMPLATE_DIR.
	ScenarioHostileTemplate ScenarioName = "hostile-template"
	// ScenarioCopiedWorkingCopy carries a working copy that is copied once a
	// gate exists, so the copy inherits the original's remote.
	ScenarioCopiedWorkingCopy ScenarioName = "copied-working-copy"
)

// The shape every scenario shares.
const (
	// DefaultBranch is the branch a trusted configuration document is read
	// from, per P7.
	DefaultBranch = "main"
	// BranchUnderValidation is the branch a run validates. It is the untrusted
	// side of every trust-boundary condition here.
	BranchUnderValidation = "fixture/change"
	// ConfigPath is where the repository configuration document is planted,
	// relative to the repository root. PRD section 10 places that document at
	// the repository root and does not name it, and no package in this product
	// owns the name yet, so it is stated here once and carried in the manifest
	// rather than assumed by a reader. See OpenQuestions.
	ConfigPath = "assistant.json"
	// TripwireFile is the scenario-relative path every planted executable
	// appends to when it runs. It must not exist after a run.
	TripwireFile = "tripwires/fired"
)

// Scenario is one built subject repository and everything a harness needs to
// point a run at it.
type Scenario struct {
	// Name identifies the scenario.
	Name ScenarioName `json:"name"`
	// Purpose says in one sentence why this scenario is separate from the
	// others.
	Purpose string `json:"purpose"`
	// Root is the scenario's directory.
	Root string `json:"root"`
	// Origin is the bare repository standing in for the real remote.
	Origin string `json:"origin"`
	// WorkingCopy is the working copy a run is pointed at. Its origin remote
	// names Origin.
	WorkingCopy string `json:"working_copy"`
	// Branch is the branch under validation, empty for a scenario that has
	// none.
	Branch string `json:"branch"`
	// Tripwire is the file every planted executable appends to when it runs.
	// It does not exist in a scenario nothing has executed.
	Tripwire string `json:"tripwire"`
	// Commits maps a name to the commit it resolves to at build time, so a
	// harness can name the state a run started from without parsing history.
	Commits map[string]string `json:"commits,omitempty"`
	// AgentResponses maps a response identifier to the file holding the exact
	// bytes an agent would print. What serves them is the fake agent's
	// question, not this package's.
	AgentResponses map[string]string `json:"agent_responses,omitempty"`
	// ProviderResponses maps a response identifier to the file holding the
	// exact bytes the code host's provider command would print.
	ProviderResponses map[string]string `json:"provider_responses,omitempty"`
	// Paths carries the scenario-specific locations a harness needs, such as
	// the hostile template directory or the second clone an out-of-band
	// advance is pushed from.
	Paths map[string]string `json:"paths,omitempty"`
}

// Fixture is a built fixture: the scenarios on disk, the catalog of what is
// planted in them, and the questions this package declined to answer.
type Fixture struct {
	// Version is the manifest version, so a harness reading an older build
	// fails on the version rather than on a missing field.
	Version int `json:"version"`
	// Root is the directory everything was built under.
	Root string `json:"root"`
	// DefaultBranch, Branch, and ConfigPath restate the shape every scenario
	// shares, so a harness reads them rather than hard-coding them.
	DefaultBranch string `json:"default_branch"`
	Branch        string `json:"branch"`
	ConfigPath    string `json:"config_path"`
	// Scenarios are the built scenarios, ordered by name.
	Scenarios []Scenario `json:"scenarios"`
	// Conditions is the catalog: what is planted and what it has to produce.
	Conditions []Condition `json:"conditions"`
	// OpenQuestions are the decisions this package ran into and did not make.
	OpenQuestions []OpenQuestion `json:"open_questions"`
}

// manifestVersion is the shape of the manifest this package writes.
const manifestVersion = 1

// Option configures a build.
type Option func(*builder)

// WithGitBinary names the git to build with, for a caller that has more than
// one. The default is "git" on PATH.
func WithGitBinary(path string) Option {
	return func(b *builder) { b.gitBinary = path }
}

// builder holds what every plant needs while a fixture is being built.
type builder struct {
	root      string
	gitBinary string
	git       *gitRunner
	// headWrites are the writes a plant asked for that name the branch head.
	// A plant cannot make them when it runs, because the head is not final
	// until the branch is published, and pushBranch is the one place that
	// records it.
	headWrites []headWrite
}

// headWrite is one write waiting for the branch head of the scenario it names.
type headWrite struct {
	scenario ScenarioName
	what     string
	write    func(head string) error
}

// afterBranchHead registers a write to be made once the branch under
// validation is published and its head recorded.
//
// It exists so that a plant naming the branch head and Commits["branch-head"]
// are one fact rather than two that agree. Reading the head at plant time gave
// the same answer only while nothing committed between that plant and the
// push, which is an ordering nothing stated and nothing checked; a plant added
// after it would have left the two describing different commits with the
// catalog still claiming they matched.
func (b *builder) afterBranchHead(s *Scenario, what string, write func(head string) error) {
	b.headWrites = append(b.headWrites, headWrite{scenario: s.Name, what: what, write: write})
}

// requireHeadWritesMade reports the writes that were registered and never
// made, which is a scenario whose branch was never published carrying a plant
// that names its head.
func (b *builder) requireHeadWritesMade() error {
	if len(b.headWrites) == 0 {
		return nil
	}
	first := b.headWrites[0]
	return fmt.Errorf("fixture: %d write(s) naming a branch head were registered and never made, "+
		"starting with %s in scenario %s; a scenario carrying one has to publish its branch",
		len(b.headWrites), first.what, first.scenario)
}

// Build constructs every scenario under root and returns the catalog beside
// them. root must not already exist, or must be empty: this writes a git
// object graph from nothing on every call, and writing it over something else
// would leave a fixture whose contents nobody can account for.
//
// The returned Fixture is the whole deliverable. WriteManifest renders it for
// a harness in another process.
func Build(root string, opts ...Option) (*Fixture, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fixture: resolving the build root %s: %w", root, err)
	}
	if err := requireEmpty(abs); err != nil {
		return nil, err
	}
	b := &builder{root: abs}
	for _, opt := range opts {
		opt(b)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("fixture: creating the build root %s: %w", abs, err)
	}
	home := filepath.Join(abs, ".githome")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("fixture: creating the git home %s: %w", home, err)
	}
	if b.git, err = newGitRunner(b.gitBinary, home); err != nil {
		return nil, err
	}

	f := &Fixture{
		Version:       manifestVersion,
		Root:          abs,
		DefaultBranch: DefaultBranch,
		Branch:        BranchUnderValidation,
		ConfigPath:    ConfigPath,
		OpenQuestions: OpenQuestions(),
	}
	for _, build := range scenarioBuilders {
		scenario, conditions, err := build(b)
		if err != nil {
			return nil, err
		}
		f.Scenarios = append(f.Scenarios, *scenario)
		f.Conditions = append(f.Conditions, conditions...)
	}
	// A planted executable is drained by the commit that stages it, which is
	// where its mode is stated in the index. One left pending is one that was
	// written and then committed by some other plant with the mode git infers,
	// which is the executable bit lost on any platform that does not carry one,
	// and nothing else here would notice.
	if err := b.git.requireDrained(); err != nil {
		return nil, err
	}
	if err := b.requireHeadWritesMade(); err != nil {
		return nil, err
	}
	sort.Slice(f.Scenarios, func(i, j int) bool { return f.Scenarios[i].Name < f.Scenarios[j].Name })
	sort.Slice(f.Conditions, func(i, j int) bool { return f.Conditions[i].ID < f.Conditions[j].ID })
	return f, nil
}

// scenarioBuilders is every scenario, in the order they are built. A scenario
// added here is built by the next call with no other list to update.
var scenarioBuilders = []func(*builder) (*Scenario, []Condition, error){
	buildBase,
	buildRemoteAdvanced,
	buildEmptyAfterRebase,
	buildUnparseableTrustedConfig,
	buildUnreadableTrustedConfig,
	buildHostileTemplate,
	buildCopiedWorkingCopy,
}

// requireEmpty refuses a root that already holds something. A build that
// wrote into an existing directory would produce a fixture that is partly this
// build and partly whatever was there, which is the one thing a fixture may
// not be.
func requireEmpty(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fixture: reading the build root %s: %w", root, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("fixture: refusing to build into %s: it is not empty, and a fixture that is "+
			"partly this build and partly what was already there accounts for neither; "+
			"remove it or name a directory that does not exist", root)
	}
	return nil
}

// newScenario creates a scenario's directories and returns it with nothing
// planted yet.
func (b *builder) newScenario(name ScenarioName, purpose string) (*Scenario, error) {
	root := filepath.Join(b.root, string(name))
	for _, dir := range []string{root, filepath.Join(root, "tripwires")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("fixture: creating %s: %w", dir, err)
		}
	}
	return &Scenario{
		Name:              name,
		Purpose:           purpose,
		Root:              root,
		Origin:            filepath.Join(root, "origin.git"),
		WorkingCopy:       filepath.Join(root, "work"),
		Branch:            BranchUnderValidation,
		Tripwire:          filepath.Join(root, filepath.FromSlash(TripwireFile)),
		Commits:           map[string]string{},
		AgentResponses:    map[string]string{},
		ProviderResponses: map[string]string{},
		Paths: map[string]string{
			GitHomeKey:   b.git.home,
			GitConfigKey: b.git.config,
			GitBinaryKey: b.git.binary,
		},
	}, nil
}

// initSubject lays down the default branch of the subject repository, creates
// the bare repository standing in for the real remote, and pushes the default
// branch to it. Every scenario starts from the same trusted document, which is
// what the pushed-branch condition is measured against.
func (b *builder) initSubject(s *Scenario) error {
	work := s.WorkingCopy
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("fixture: creating %s: %w", work, err)
	}
	if _, err := b.git.run(work, "init", "--quiet", "."); err != nil {
		return err
	}
	files := map[string]string{
		"go.mod":           subjectGoMod,
		"total.go":         subjectTotalGo,
		"total_test.go":    subjectTotalTestGo,
		"docs/behavior.md": subjectDocsBehavior,
		ConfigPath:         subjectConfig,
	}
	for rel, content := range files {
		if err := writeFile(work, rel, 0o644, content); err != nil {
			return err
		}
	}
	head, err := b.git.commitAll(work, "the subject as the default branch has it")
	if err != nil {
		return err
	}
	s.Commits["default-branch-initial"] = head

	if _, err := b.git.run(s.Root, "init", "--bare", "--quiet", s.Origin); err != nil {
		return err
	}
	if _, err := b.git.run(work, "remote", "add", "origin", s.Origin); err != nil {
		return err
	}
	if _, err := b.git.run(work, "push", "--quiet", "origin", DefaultBranch); err != nil {
		return err
	}
	return nil
}

// startBranch creates the branch under validation at the current default
// branch tip.
func (b *builder) startBranch(s *Scenario) error {
	_, err := b.git.run(s.WorkingCopy, "checkout", "--quiet", "-b", s.Branch)
	return err
}

// pushBranch publishes the branch under validation, which is how every
// condition carried on it arrives at the remote a run reads.
func (b *builder) pushBranch(s *Scenario) error {
	if _, err := b.git.run(s.WorkingCopy, "push", "--quiet", "origin", s.Branch); err != nil {
		return err
	}
	head, err := b.git.run(s.WorkingCopy, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	s.Commits["branch-head"] = head
	var pending []headWrite
	for _, w := range b.headWrites {
		if w.scenario != s.Name {
			pending = append(pending, w)
			continue
		}
		if err := w.write(head); err != nil {
			return fmt.Errorf("fixture: writing %s for scenario %s: %w", w.what, s.Name, err)
		}
	}
	b.headWrites = pending
	return nil
}

// tripwireScript renders an executable that records having run and then gets
// out of the way. It exits zero on purpose: the evidence wanted from a planted
// executable is that it ran, and a non-zero exit would also break the run for
// a reason that has nothing to do with the condition.
func tripwireScript(id, tripwire, comment string) string {
	return "#!/bin/sh\n" +
		"# " + comment + "\n" +
		"# Planted by internal/fixture. If this line runs, the condition it belongs\n" +
		"# to has failed: nothing in a pushed branch may choose code that executes.\n" +
		"printf '%s\\n' " + shellQuote(id) + " >> " + shellQuote(tripwire) + "\n" +
		"exit 0\n"
}

// shellQuote renders s as a single-quoted shell word.
func shellQuote(s string) string {
	out := []rune{'\''}
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, r)
	}
	return string(append(out, '\''))
}
