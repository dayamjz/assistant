package stages

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/vcs"
)

// StageDeps is what a stage body is given that does not vary with the run: the
// adapters it reaches the world through.
//
// # Why this exists at all, and why only half of what a body needs is here
//
// A body used to receive pipeline.Input alone, which is the stage it is
// running as and a reader over declared state. That is enough for a stage that
// is a function of the run's state and enough for nothing else, so the intent
// stage shipped and every stage that touches git, an agent, a code host, or a
// checkout could not be written.
//
// What was missing splits in two by lifetime, and the split is compelled
// rather than tidy. An adapter cannot be serialized into state, so it cannot
// be a run-scoped fact. And a run-scoped fact cannot ride a constructor,
// because All is called once when the service resolves an agent and one
// pipeline and one executor then serve every run: a value captured here would
// be the same value for every run of this service. That second argument is the
// decisive one, since it holds whether or not a process ever restarts.
//
// So the adapters are here, and the run's own facts are declared state keys in
// internal/pipeline. This build declares three of them: KeyRepository and
// KeyRun, which are what Copy below derives its path from, and
// KeyForgeRepository, which is which repository on the code host this run acts
// on. Lifetime decides the owner, which is what P14 asks.
//
// Where the run's target stood when it was observed is not one of them here.
// Only the rebase and push stages need it, and declaring a key is one row in
// internal/pipeline/key.go, so the rebase stage adds that row when it lands
// rather than this seam declaring a key nothing reads.
//
// # The code host is split by that same rule, and it is why Forge is a Host
//
// A forge.Provider addresses exactly one repository, so it is not an adapter
// that a service can settle once: one All builds one pipeline and one executor
// for every run of that service, and a Provider captured here would send every
// run's pull request to the repository the first one happened to be for. The
// same argument that put the isolated copy's path in state rather than on this
// struct applies to it unchanged.
//
// The two halves land on the two sides. forge.Host is the adapter: the
// provider command line, the environment it runs in, the redactor, and the
// output bound are the service's decisions and do not vary with the run, so a
// Host is built once and lives here. Which repository is the run's own fact,
// so it is KeyForgeRepository, filled from the run's record the way
// KeyRepository and KeyRun are, and a body reaches a Provider by opening one
// on that key.
//
// A path is what this must not become. A directory is not an adapter, and a
// forge adapter that resolved its repository from one would address whatever
// the directory pointed at rather than what the run named, which is why
// forge.Host.Open takes the specifier and refuses an empty one.
//
// # The agent is not a Runner, and that is P4
//
// Agent is an agents.StageAgent rather than an agents.Runner, and the
// difference is what closes P4's route through this struct. agents.OpenFixer
// opens a fixer session from any Runner it is handed, so a body given one
// reaches the memory P4 keeps the reviewer out of by calling an exported
// function. A StageAgent carries its runner unexported and offers only Run, so
// there is nothing here to hand to OpenFixer.
//
// That closes the route and not the question. agents.Resolve and
// agents.DefaultCatalog are exported too, and Config below carries the ordered
// agent list Resolve takes, so a body that resolves its own adapter reaches a
// session without this struct handing it anything. agents.StageAgent's
// documentation names that gap; nothing here removes it, and no test can see
// it, because it is not a route out of a value.
//
// Nothing on this struct may be, or yield, a route to a fixer session. That
// is the rule a field added here is held to.
// TestStageDepsIsNoRouteToAFixerSession asks it of the type graph rather than
// of a list of assertions, because an assertion answers whether a value is a
// Runner and the question is whether a body can obtain one.
//
// It enforces the rule over concrete types: a field, or a method result, whose
// type is or yields one fails that test. What an interface-typed field holds
// is outside it, because a dynamic value satisfying agents.Runner is not in
// the static type the walk reads. internal/agents/route states that gap and
// pins it, so read it there rather than here. Forge below is a field of that
// shape today, which makes what is put in one a question review has to ask
// rather than one the test answers.
//
// # Fields nothing in this build reads yet
//
// Forge has no reader here, because this seam lands before the stage bodies
// that need it and deliberately so. The bodies queued behind this file are
// rebase and pull request, each specified work with a task of its own, and a
// seam that arrived missing what they need would force a second breaking
// change to the same file, which is the collision landing it alone exists to
// prevent.
//
// Forge differs from Config in one way worth stating: it is wired rather than
// nil, so what has no reader is the field and not the mechanism behind it. A
// run of this build carries the specifier its record names and a Host that
// would open a provider on it; the pull request stage adds the call.
//
// Forge answers to the pull request and checks bodies. Config was the same
// case until the review body landed and read it, and the test body is its
// other consumer. If Forge still has no reader once the bodies it answers to
// have landed, it is the field that was wrong and it should go.
type StageDeps struct {
	// Agent runs one invocation at a time with no session. It is a
	// StageAgent rather than a Runner so that a body cannot open a fixer
	// session on it, which is P4 at this seam.
	Agent agents.StageAgent
	// Home is the root the run's isolated copy lives under. A body reaches
	// that copy through Copy rather than by composing a path, so
	// internal/home stays the one owner of the layout.
	Home *home.Home
	// Config is the run's resolved configuration, and the review and test
	// bodies are the consumers it is here for: ReviewPathRules is the extra
	// guidance a review is held to, and Commands is what a test run executes.
	// It is the operator's global layer and the schema defaults as this build
	// resolves them; PRD section 10's trusted repository layer is not read
	// anywhere yet, which internal/service's documentation states.
	Config config.Config
	// Forge is the code host the pull request and checks stages open their
	// provider on. It is a forge.Host and not a forge.Provider because a
	// Provider addresses one repository and this seam is built once per
	// service; the repository is pipeline.KeyForgeRepository, and a body
	// passes it to Open.
	//
	// It is nil only where nothing wired it: internal/service builds a
	// forge.GitHubHost for every run of a service. A body given none, or
	// given a run whose KeyForgeRepository is empty, refuses rather than
	// proceeding without a code host.
	Forge forge.Host
	// git are the options every repository opened through Copy is opened
	// with, which is how the redactor the service configured reaches the
	// repository a body works in. It is unexported because it is a decision
	// the wiring makes and not one a body may vary.
	//
	// That bounds the options and not the opening. Package stages imports
	// internal/vcs, so a body may call vcs.OpenWorktree itself and get that
	// package's default redactor rather than the one configured here, and
	// nothing on this struct prevents it.
	git []vcs.Option
}

// NewStageDeps returns the dependencies a stage body is given.
//
// git are the options every repository opened through Copy is opened with. The
// caller supplies them because credential redaction is the service's decision:
// internal/store refuses to open without a redactor and internal/vcs takes
// one, so a body opening a repository some other way would be the one path
// that skipped it.
func NewStageDeps(agent agents.StageAgent, h *home.Home, cfg config.Config, host forge.Host, git ...vcs.Option) StageDeps {
	return StageDeps{Agent: agent, Home: h, Config: cfg, Forge: host, git: git}
}

// Copy opens the isolated copy this run works in, which PRD section 8 places
// at worktrees/<repository>/<run>.
//
// The path is derived here rather than carried in state. That is deliberate
// and it is what keeps the run's row the only record of the run: PRD section
// 8's ordering rule has the row written before the directory, so a directory
// with no row is safe to remove, and a second durable copy of the path would
// give that rule a second fact to stay consistent with.
//
// It opens and never creates, because a body that created its own would be
// working somewhere the service does not know to reclaim. Nothing in this
// build's production code creates one either, so a run reaching this today
// finds nothing to open and gets back an error naming the path it tried:
// creation and reclaim is the work queued next, and this is the path it has to
// produce.
func (d StageDeps) Copy(ctx context.Context, repositoryID, runID string) (*vcs.Repository, error) {
	if d.Home == nil {
		return nil, fmt.Errorf("stages: no home, so the isolated copy for run %s cannot be located", runID)
	}
	path := d.Home.Worktree(repositoryID, runID)
	repo, err := vcs.OpenWorktree(ctx, path, d.git...)
	if err != nil {
		return nil, fmt.Errorf("stages: opening the isolated copy for run %s at %s: %w", runID, path, err)
	}
	return repo, nil
}
