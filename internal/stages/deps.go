package stages

import (
	"context"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
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
// because All is called once per resolved configuration and one pipeline and
// one executor then serve every run that resolved the same documents: a value
// captured here would be the same value for all of them. That second argument
// is the decisive one, since it holds whether or not a process ever restarts.
//
// So the adapters are here, and the run's own facts are declared state keys in
// internal/pipeline. KeyRepository and KeyRun are what Copy below derives its
// path from, and KeyForgeRepository is which repository on the code host this
// run acts on. Lifetime decides the owner, which is what P14 asks.
//
// Where the run's target stood when it was observed is not one of them here.
// Only the rebase and push stages need it, and declaring a key is one row in
// internal/pipeline/key.go, so the rebase stage adds that row when it lands
// rather than this seam declaring a key nothing reads.
//
// # The code host is split by that same rule, and it is why Forge is a Host
//
// A forge.Provider addresses exactly one repository, so it is not an adapter a
// service can settle once: one All builds one pipeline and one executor, and
// those serve every run of that service, so a Provider captured here would
// send every run's pull request to the repository the first one happened to be
// for. The same argument that put the isolated copy's path in state rather
// than on this struct applies to it unchanged.
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
// # Fields whose readers land after this seam did
//
// A field here may have no reader yet, because this seam landed before the
// stage bodies that need it and deliberately so. The bodies queued behind this
// file are separate work, each with a task of its own, and a seam that arrived
// missing what they need would force a second breaking change to the same
// file, which is the collision landing it alone exists to prevent.
//
// Each such field names in its own documentation below the bodies it answers
// to, so the claim is checkable against them rather than restated here. A
// field still without a reader once those bodies have landed is the field that
// was wrong, and it should go.
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
	// guidance a review is held to, JudgePaths is what its judge lens reports,
	// and Commands is what a test run executes.
	// It is PRD section 10's full resolution - the operator's layer, the
	// repository document's trusted copy, and its pushed copy - which
	// internal/service resolves per run and builds one of these seams for
	// each distinct resolution of, so one StageDeps still serves every run
	// that resolved the same documents.
	Config config.Config
	// Forge is the code host the pull request and checks stages open their
	// provider on. It is a forge.Host and not a forge.Provider because a
	// Provider addresses one repository and this seam is built once per
	// service; the repository is pipeline.KeyForgeRepository, and a body
	// passes it to Open.
	//
	// It is nil only where nothing wired it: internal/service builds one
	// forge.Hosts and every run of that service opens through it, which hands
	// the specifier to the adapter for the host it names. A run
	// whose KeyForgeRepository is empty has no provider to open, because
	// forge.Host.Open refuses an empty specifier; a body that needs a code
	// host is to report that rather than proceed without one.
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
	// redact removes credentials from text on its way into anything a body
	// here persists or reports: a run's evidence record, the bounded
	// projection of a command's output, and a report's own text. PRD section
	// 8's module table has internal/redact called at every persistence
	// boundary, and a body that runs a configured command and records its
	// output opens one.
	//
	// It is unexported on the same terms as git: which redactor applies is
	// the wiring's decision, not one a body may vary. Its zero value redacts
	// - internal/redact documents that the zero Redactor works - so an
	// unwired StageDeps does not quietly skip redaction.
	//
	// What it removes is whatever shapes internal/redact recognizes; that
	// package's doc owns the list and the residual gap.
	redact redact.Redactor
	// judge is the decision-model client the review body puts its two checks
	// to. It is build-scoped for the reason every adapter here is: the key is
	// the operator's and is read once by the service, and the client holds no
	// per-run state, so one serves every run of the service.
	//
	// It is unexported on the same terms as git and redact: which client, and
	// so whether a key is present and where requests go, is the wiring's
	// decision. Whether a check runs at all is the run's configured mode,
	// config.Config.DecisionModel, which is a fact of the run's resolution
	// and so already on Config. A nil client is a client with no key, so an
	// unwired StageDeps sends nothing.
	judge *judge.Client
	// waits is the durable record the checks body measures PRD section 10's
	// checks_timeout from. It is build-scoped for the reason every adapter
	// here is: one service holds one store and every run's wait is a row in
	// it keyed by the run, so one record serves every run of the service.
	//
	// It is unexported on the same terms as judge: which record a wait is
	// kept in is the wiring's decision. A nil record is a checks body that
	// cannot wait, and it refuses rather than holding on the first unsettled
	// read, because a hold there would be a timeout of zero nobody configured.
	waits ChecksWaits
	// records is the run record the pull request body reads the one fact it
	// states from a record rather than from state: whether the run was put
	// under standing consent. That fact is the run row's, written by
	// internal/service when the per-run flag is given, and it can be given
	// after a run began, so no node wrote it into state; reading the row is
	// what keeps the row its one owner. It is build-scoped for the reason
	// waits is: one service holds one store and every run is a row in it.
	//
	// It is unexported on the same terms as waits. A nil record is a body
	// that says it could not read whether consent was given, rather than one
	// that says it was not.
	records RunRecords
	// clock is how the checks body tells the time and paces its re-reads. Its
	// zero value is the wall clock and a minute between reads; only this
	// package's own tests set anything else.
	clock checksClock
}

// ChecksWaits is the durable record of when a run's checks stage began
// waiting, and the one route the checks body has to it. internal/store's
// ArmChecksWait and EndChecksWait are the implementation, and own the
// re-arming rule: a wait read against the base it was armed against is kept,
// and one read against another base is re-armed at that instant.
//
// It is an interface because a stage body opens no database, per the rule
// that internal/store is the only package that does; internal/service hands
// over the store it already opened.
type ChecksWaits interface {
	// ArmChecksWait returns the run's wait after observing the base branch
	// at base at the instant at, beginning or re-arming it as store states.
	ArmChecksWait(ctx context.Context, run, base string, at time.Time) (store.ChecksWait, error)
	// EndChecksWait removes the run's wait, so the next one begins afresh.
	EndChecksWait(ctx context.Context, run string) error
}

// WithChecksWaits returns these dependencies with the record the checks body
// measures checks_timeout from. internal/service is what calls it, with the
// store it opened for the home.
func (d StageDeps) WithChecksWaits(w ChecksWaits) StageDeps {
	d.waits = w
	return d
}

// RunRecords is the run record, and the one route a stage body has to it.
// internal/store's Run is the implementation; the pull request body is the one
// consumer, and it reads the standing consent the record carries and nothing
// else, so no body comes to decide anything from a record a run keeps.
//
// It is an interface because a stage body opens no database, per the rule
// that internal/store is the only package that does; internal/service hands
// over the store it already opened.
type RunRecords interface {
	// Run returns the run's record.
	Run(ctx context.Context, id string) (store.Run, error)
}

// WithRunRecords returns these dependencies with the run record the pull
// request body reads standing consent from. internal/service is what calls
// it, with the store it opened for the home.
func (d StageDeps) WithRunRecords(r RunRecords) StageDeps {
	d.records = r
	return d
}

// WithJudge returns these dependencies with the decision-model client the
// review body's checks are put to. internal/service is what calls it, with
// the one client it built from the operator's environment.
func (d StageDeps) WithJudge(c *judge.Client) StageDeps {
	d.judge = c
	return d
}

// NewStageDeps returns the dependencies a stage body is given.
//
// git are the options every repository opened through Copy is opened with, and
// redactor is what the bodies that persist or report a command's text apply at
// those boundaries. The caller supplies both because credential redaction is
// the service's decision: internal/store refuses to open without a redactor,
// internal/vcs takes one, and the command-running path here takes this one, so
// a body that opened a repository or persisted output some other way would be
// a path that skipped it.
func NewStageDeps(agent agents.StageAgent, h *home.Home, cfg config.Config, host forge.Host,
	redactor redact.Redactor, git ...vcs.Option) StageDeps {
	return StageDeps{Agent: agent, Home: h, Config: cfg, Forge: host, redact: redactor, git: git}
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
// working somewhere the service does not know to reclaim. internal/service
// creates it, before the run is recorded as started and so before any body
// runs, and reclaims it when the run reaches a terminal status; that package's
// copy.go owns what it is cut from and why.
//
// So an error from here is a run whose copy is missing rather than one nothing
// ever built, and it names the path it tried.
func (d StageDeps) Copy(ctx context.Context, repositoryID, runID string) (*vcs.Repository, error) {
	return openRunCopy(ctx, d.Home, d.git, repositoryID, runID)
}

// openRunCopy is the one place a run's isolated copy is located and opened.
//
// Both seams go through it - a stage body's and a fix round's - because the
// path is PRD section 8's layout and a second composition of it would be a
// second owner of where a run works. A fix round that opened a different
// directory than the stage it is fixing would be editing something nobody
// validates.
func openRunCopy(ctx context.Context, h *home.Home, git []vcs.Option, repositoryID, runID string) (
	*vcs.Repository, error) {
	if h == nil {
		return nil, fmt.Errorf("stages: no home, so the isolated copy for run %s cannot be located", runID)
	}
	path := h.Worktree(repositoryID, runID)
	repo, err := vcs.OpenWorktree(ctx, path, git...)
	if err != nil {
		return nil, fmt.Errorf("stages: opening the isolated copy for run %s at %s: %w", runID, path, err)
	}
	return repo, nil
}
