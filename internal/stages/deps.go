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
// internal/pipeline. This build declares two of them, KeyRepository and
// KeyRun, which are what Copy below derives its path from. Lifetime decides
// the owner, which is what P14 asks.
//
// Where the run's target stood when it was observed is not one of them here.
// Only the rebase and push stages need it, and declaring a key is one row in
// internal/pipeline/key.go, so the rebase stage adds that row when it lands
// rather than this seam declaring a key nothing reads.
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
// Nothing on this struct may be, or yield, a route to a fixer session.
// TestStageDepsIsNoRouteToAFixerSession asks that of the type graph rather
// than of a list of assertions, because an assertion answers whether a value
// is a Runner and the question is whether a body can obtain one. Adding a
// field of a type that exposes one fails that test.
//
// # Fields nothing in this build reads yet
//
// Forge and Config have no reader here, because this seam lands before the
// stage bodies that need them and deliberately so: three of them are written
// against it next, and a seam that arrived missing what they need would force
// a second breaking change to the same file, which is the collision landing it
// alone exists to prevent.
//
// That is a considered exception to this repository's rule against exported
// surface whose only caller is work that has not happened, not an oversight of
// it. The rule guards against surface that grows whether or not the work
// arrives; here the work is queued behind this file. If a field below still
// has no reader when those bodies have landed, it is the field that was wrong
// and it should go.
type StageDeps struct {
	// Agent runs one invocation at a time with no session. It is a
	// StageAgent rather than a Runner so that a body cannot open a fixer
	// session on it, which is P4 at this seam.
	Agent agents.StageAgent
	// Home is the root the run's isolated copy lives under. A body reaches
	// that copy through Copy rather than by composing a path, so
	// internal/home stays the one owner of the layout.
	Home *home.Home
	// Config is the run's resolved configuration. It is the operator's global
	// layer and the schema defaults as this build resolves them; PRD section
	// 10's trusted repository layer is not read anywhere yet, which
	// internal/service's documentation states.
	Config config.Config
	// Forge is the code host this run's pull request and checks stages talk
	// to. Nothing in this build constructs one, so it is nil on every run:
	// internal/service passes nil here unconditionally, and a provider arrives
	// with the pull request and checks stages that need it. A body that needs
	// one refuses rather than proceeding without it.
	Forge forge.Provider
	// git are the options every repository this seam opens is opened with, so
	// a body cannot open one without the redactor the service configured. It
	// is unexported because it is a decision the wiring makes and not one a
	// body may vary.
	git []vcs.Option
}

// NewStageDeps returns the dependencies a stage body is given.
//
// git are the options every repository opened through Copy is opened with. The
// caller supplies them because credential redaction is the service's decision:
// internal/store refuses to open without a redactor and internal/vcs takes
// one, so a body opening a repository some other way would be the one path
// that skipped it.
func NewStageDeps(agent agents.StageAgent, h *home.Home, cfg config.Config, provider forge.Provider, git ...vcs.Option) StageDeps {
	return StageDeps{Agent: agent, Home: h, Config: cfg, Forge: provider, git: git}
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
// working somewhere the service does not know to reclaim. Nothing else in this
// build creates one either, so every call here fails today with an error
// naming the path it tried: creation and reclaim is the work queued next, and
// this is the path it has to produce.
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
