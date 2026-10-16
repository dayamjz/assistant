package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// initGate creates or repairs the gate for this repository, and records the
// repository the gate validates.
//
// internal/gate owns everything about the gate itself: where it lives, its
// hooks, its identity across a move or a copy, and the ownership question it
// asks before adopting or replacing one. Nothing here repeats any of that.
// What this adds is the repository record, because a run needs one and a gate
// is not one.
//
// Everything that record is read from - the upstream and the default branch -
// is established before anything is created, which is internal/gate's own
// invariant applied to the pair: nothing is created unless the whole operation
// can complete, so a working copy this command refuses is one it leaves as it
// was. Ordering it the other way is what left a gate standing beside no
// repository record, a half-state a run cannot start from and no verb here
// repairs.
func initGate(ctx context.Context, in *invocation) (any, error) {
	var upstream, fork, defaultBranch string
	var acceptCommands bool
	if err := in.parseFlags("init", func(set *flag.FlagSet) {
		set.StringVar(&upstream, "upstream", "", "the remote the change is destined for; the default is this working copy's origin")
		set.StringVar(&fork, "fork-url", "", "a fork of the upstream that validated changes are sent to and proposed from; both must be repositories on github.com, and without it changes are sent to the upstream")
		set.StringVar(&defaultBranch, "default-branch", "", "the branch trusted configuration is read from; the default is what this working copy records origin's HEAD as naming, and init refuses when it records none")
		set.BoolVar(&acceptCommands, acceptCommandsFlag, false, "write every command init offers into this checkout's configuration file without asking; without it a command is written only when accepted at a terminal")
	}); err != nil {
		return nil, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	records, err := in.openStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = records.Close() }()

	intended, source, err := intendedRepository(ctx, working, upstream, fork, defaultBranch)
	if err != nil {
		return nil, err
	}
	built, err := gate.Initialize(ctx, gate.Spec{
		Home:        in.home.Root(),
		WorkingPath: working,
		Command:     in.env.Executable,
	}, gate.WithIndex(records))
	if err != nil {
		return nil, err
	}
	intended.ID = built.ID()
	repository, err := records.UpsertRepository(ctx, intended)
	if err != nil {
		return nil, err
	}
	// The offer comes after the gate and the record, because both are what
	// init is for and neither depends on it: a person who declines every
	// command, or whose configuration file cannot take one, still has a
	// gate. propose.go owns what is offered and where it is written.
	commands := proposeCommands(working, acceptCommands, in.env.Interactive && !in.json, in.env.Stderr, in.env.Stdin)
	return machine.Init{
		Commands:   commands,
		Gate:       machine.Gate{Present: true, ID: built.ID(), Repository: built.Repository()},
		Reattached: built.Reattached(),
		Repository: repository,
		// The source travels with the answer so that a wrong default branch
		// is visible when it is written, and not first when a run reads
		// trusted configuration from it.
		DefaultBranchSource: source,
	}, nil
}

// intendedRepository is the repository record the gate will validate, filling
// in from the working copy what the caller did not name, and where its default
// branch came from. It reads and writes nothing, so a working copy that cannot
// answer is refused before the gate it would have been recorded against
// exists.
//
// The record's identifier is the gate's, which is not known until the gate is
// built, so it is the one field the caller fills in afterwards.
//
// A fork is recorded only beside an upstream, and only when forkOf accepts
// the pair; the record then names both, and which one a run pushes to and
// which one its pull request is proposed to are the record's two fields
// rather than one read out of the other. A record without a fork is one
// whose runs push to the upstream, so running init again without --fork-url
// records that.
func intendedRepository(ctx context.Context, working, upstream, fork, defaultBranch string) (store.Repository, machine.DefaultBranchSource, error) {
	repo, err := vcs.OpenWorktree(ctx, working, vcs.WithRedactor(redact.New()))
	if err != nil {
		return store.Repository{}, "", err
	}
	if upstream == "" {
		upstream, err = repo.RemoteURL(ctx, "origin")
		if errors.Is(err, vcs.ErrRemoteNotFound) {
			return store.Repository{}, "", upstreamRefused{cause: err}
		} else if err != nil {
			return store.Repository{}, "", fmt.Errorf("reading this working copy's origin to take the upstream from: %w", err)
		}
	}
	forkURL := store.Unknown[string]()
	if fork != "" {
		if err := forkOf(upstream, fork); err != nil {
			return store.Repository{}, "", err
		}
		forkURL = store.Known(fork)
	}
	source := machine.DefaultBranchFromFlag
	if defaultBranch == "" {
		source = machine.DefaultBranchFromOrigin
		if defaultBranch, err = defaultBranchOf(ctx, repo); err != nil {
			return store.Repository{}, "", err
		}
	}
	return store.Repository{
		WorkingPath:   working,
		UpstreamURL:   upstream,
		ForkURL:       forkURL,
		DefaultBranch: defaultBranch,
	}, source, nil
}

// forkOf refuses a fork a run could not both push to and propose from, with a
// forkRefused saying which: a fork that is not a repository on the code host
// this build proposes changes on, an upstream that is not one, or a fork
// naming the upstream itself. Both are read through forge.GitHubRepository,
// the same reading internal/service makes when a run begins, so init records
// no fork a run would then refuse. It reads the two URLs and asks the code
// host nothing; whether each repository exists, and whether the fork is a
// fork of the upstream, are learned when a run first writes to them.
func forkOf(upstream, fork string) error {
	forkSpec, forkResolved := forge.GitHubRepository(fork)
	if !forkResolved {
		return forkRefused{reason: machine.ForkUnaddressable}
	}
	upstreamSpec, upstreamResolved := forge.GitHubRepository(upstream)
	if !upstreamResolved {
		return forkRefused{reason: machine.ForkUpstreamUnaddressable}
	}
	if strings.EqualFold(forkSpec, upstreamSpec) {
		return forkRefused{reason: machine.ForkIsUpstream}
	}
	return nil
}

// forkRefused is init's refusal to record the fork it was given, read out in
// internal/machine's row for the reason. Every reason's remedy is a flag the
// caller supplies, so failureExit reads it as incorrect usage.
type forkRefused struct {
	reason machine.ForkRefusal
}

// Error is the row a person reads, which says all there is.
func (e forkRefused) Error() string { return machine.ForkRefusalPhrase(e.reason).String() }

// originHead is the reference a clone records origin's own HEAD under.
const originHead = "refs/remotes/origin/HEAD"

// originBranches is the namespace origin's branches are recorded under, and
// the only one a record of origin's HEAD may point into.
const originBranches = "refs/remotes/origin/"

// defaultBranchOf is the branch PRD principle P7 reads trusted configuration
// from: the branch this working copy records origin's own HEAD as naming, and
// nothing else. When that cannot be established it refuses with a
// defaultBranchRefused, whose reason says which of four things stopped it: no
// origin, no record of origin's HEAD, a record pointing outside origin's
// branches, or a read that failed. It never falls back to the branch the
// checkout stands on, because run from a feature branch that answer makes the
// branch under validation its own trusted copy.
//
// It reads what is already in the repository and fetches nothing, so the
// answer is what this working copy last learned. A repository whose default
// branch has moved since then records the old one until the record is
// refreshed, which is why init reports where its answer came from and why
// --default-branch exists. It checks that the record points into origin's
// branches and not that the branch it names still exists there.
func defaultBranchOf(ctx context.Context, repo *vcs.Repository) (string, error) {
	if _, err := repo.RemoteURL(ctx, "origin"); errors.Is(err, vcs.ErrRemoteNotFound) {
		return "", defaultBranchRefused{reason: machine.DefaultBranchNoOrigin, cause: err}
	} else if err != nil {
		return "", defaultBranchRefused{reason: machine.DefaultBranchUnreadable, cause: err}
	}
	target, err := repo.SymbolicTarget(ctx, originHead)
	if errors.Is(err, vcs.ErrRefNotFound) {
		return "", defaultBranchRefused{reason: machine.DefaultBranchUnrecorded, cause: err}
	} else if err != nil {
		return "", defaultBranchRefused{reason: machine.DefaultBranchUnreadable, cause: err}
	}
	name, ok := strings.CutPrefix(target, originBranches)
	if !ok || name == "" || name == "HEAD" {
		return "", defaultBranchRefused{reason: machine.DefaultBranchOutsideOrigin,
			cause: fmt.Errorf("%s points at %s, outside %s", originHead, target, originBranches)}
	}
	return name, nil
}

// defaultBranchRefused is init's refusal to record a default branch it could
// not establish, read out in internal/machine's row for the reason. The
// reason is the typed half a caller in this package branches on; the cause is
// what was read, and it still matches the sentinel it wraps, so errors.Is on
// vcs.ErrRefNotFound or vcs.ErrRemoteNotFound survives the rewording.
type defaultBranchRefused struct {
	reason machine.DefaultBranchRefusal
	cause  error
}

// Error is the row a person reads. For a read that failed, the cause follows
// the row, because that row's decision points at it and nothing else names
// what could not be read; every other reason's row says all there is.
func (e defaultBranchRefused) Error() string {
	row := machine.DefaultBranchRefusalPhrase(e.reason).String()
	if e.reason == machine.DefaultBranchUnreadable && e.cause != nil {
		return row + " Cause: " + e.cause.Error()
	}
	return row
}

// Unwrap resolves to what was read.
func (e defaultBranchRefused) Unwrap() error { return e.cause }

// upstreamRefused is init's refusal when it was given no --upstream and the
// working copy has no remote named origin to take the upstream from, read out
// as internal/machine's PhraseInitNoUpstream. It is a type rather than text so
// that failureExit can decide its exit code from what it is: the remedy is a
// flag the caller supplies, so it is incorrect usage. A read of origin that
// failed for another reason is not this refusal and stays an operational
// failure, because the caller got nothing wrong.
type upstreamRefused struct {
	cause error
}

// Error is the row a person reads, which says all there is.
func (e upstreamRefused) Error() string { return machine.PhraseInitNoUpstream.String() }

// Unwrap resolves to what was read, so errors.Is on vcs.ErrRemoteNotFound
// survives the rewording.
func (e upstreamRefused) Unwrap() error { return e.cause }

// eject removes the gate and its records.
//
// It prints what it would remove and does nothing until it is told again with
// --confirm. Removing a gate takes the history of every run recorded against
// it, and a command that destroys work on its first invocation is one that
// destroys work by accident.
//
// Nothing is destroyed until the whole removal is established as possible:
// internal/store refuses to forget a repository whose runs have not finished,
// or one a task still names, and that refusal is asked for before the gate is
// touched rather than after. A refused eject therefore leaves the working
// copy, its assistant remote, and the gate exactly as they were.
//
// The window between asking and acting is not closed. A run started in that
// window is refused by the removal itself, which leaves the records whole and
// the gate gone, and this command reports the refusal. What the ordering buys
// is that the ordinary refusal - a run already in flight when eject was typed
// - destroys nothing; it is not a guarantee that nothing can be removed out
// from under a run that starts in the meantime.
func eject(ctx context.Context, in *invocation) (any, error) {
	var confirm bool
	if err := in.parseFlags("eject", func(set *flag.FlagSet) {
		set.BoolVar(&confirm, "confirm", false, "carry out the removal rather than printing what it would remove")
	}); err != nil {
		return nil, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	records, err := in.openStore(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = records.Close() }()

	binding, err := records.GateBinding(ctx, working)
	if err != nil {
		return nil, fmt.Errorf("%s is not bound to a gate, so there is nothing to remove: %w", working, err)
	}
	if !confirm {
		return nil, ejectPlan(binding.GateID, working)
	}
	if err := records.RepositoryRemovable(ctx, binding.GateID); err != nil {
		return nil, err
	}
	if err := gate.Remove(ctx, gate.Spec{Home: in.home.Root(), WorkingPath: working}, gate.WithIndex(records)); err != nil {
		return nil, err
	}
	removed, err := records.ForgetRepository(ctx, binding.GateID)
	if err != nil {
		return nil, err
	}
	return machine.Eject{Removed: true, Repository: binding.GateID, Runs: removed}, nil
}

// ejectPlan is what a removal would do, reported as a refusal to do it yet.
func ejectPlan(gateID, working string) error {
	return fmt.Errorf("this would remove the gate %s, the assistant remote in %s, and every run recorded against that gate. Run it again with --confirm to do it",
		gateID, working)
}

// syncBranch reconciles the local branch with what the pipeline pushed, or
// recovers work it holds: PRD section 11's custody, which section 9 has print
// the exact plan and require confirmation.
//
// It acts on a run's work, so it calls the service, which holds the home and
// serves internal/custody's classification and steps; nothing here reads a
// run or moves a branch. Without --confirm the answer is the plan and nothing
// is written anywhere; with it, the same classification is made again in the
// service and its steps are carried out, each anchored on the run's own
// observation of the branch rather than on anything read for this command.
// A service that is not running is reported as the failure it is, because
// the branch's custody is the service's and a second path to it here would
// be the second mutation path section 8 forbids.
func syncBranch(ctx context.Context, in *invocation) (any, error) {
	var confirm bool
	if err := in.parseFlags("sync", func(set *flag.FlagSet) {
		set.BoolVar(&confirm, "confirm", false, "carry the plan out rather than printing it")
	}); err != nil {
		return nil, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	var plan machine.Plan
	err = in.callService(ctx, ipc.MethodBranchSync, machine.SyncRequest{
		Working: machine.Working{WorkingPath: working},
		Confirm: confirm,
	}, &plan)
	return plan, err
}

// doctor checks every dependency and decides whether a run can start at all.
//
// The decision is the point of the command, so it is a field rather than
// something a reader infers from a list. A check that is not blocking is
// reported and does not decide: a build short of all nine stage bodies can
// start a run, and what that run does is hold at the stages stages.Holding
// names, which is worth knowing and is not the same as being unable to start.
// The code host row is the same kind: a run can start without a runnable
// provider command line and the stages that talk to the code host refuse
// there, so the row informs before a stage does and stops nothing. The commit
// identity row is the same kind again: commitIdentity says why.
func doctor(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("doctor", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	report := machine.Doctor{}
	add := func(check machine.Check) { report.Checks = append(report.Checks, check) }

	add(machine.Check{Name: machine.CheckHome, OK: true, Blocking: true, Detail: in.home.Root()})
	records, err := in.openStore(ctx)
	if err != nil {
		add(machine.Check{Name: machine.CheckDatabase, Blocking: true, Detail: err.Error()})
	} else {
		add(machine.Check{Name: machine.CheckDatabase, OK: true, Blocking: true, Detail: records.Path()})
		defer func() { _ = records.Close() }()
	}

	working, workErr := in.workingCopy(ctx)
	if workErr != nil {
		add(machine.Check{Name: machine.CheckWorkingCopy, Blocking: true, Detail: workErr.Error()})
	} else {
		add(machine.Check{Name: machine.CheckWorkingCopy, OK: true, Blocking: true, Detail: working})
	}

	if records != nil && workErr == nil {
		gated := gateCheck(ctx, records, working)
		add(gated)
		if gated.OK {
			add(commitIdentity(ctx, in.home.Root(), records, working))
		}
	}

	service := in.serviceState(ctx)
	add(machine.Check{
		Name:     machine.CheckService,
		OK:       service.Running,
		Blocking: true,
		Detail:   serviceDetail(service),
	})

	catalog := agents.DefaultCatalog()
	resolution, err := agents.Resolve(ctx, in.resolvedAgents(), catalog)
	if err != nil {
		add(machine.Check{Name: machine.CheckAgent, Blocking: true, Detail: err.Error()})
	} else {
		add(machine.Check{Name: machine.CheckAgent, OK: true, Blocking: true, Detail: resolution.Name})
	}
	add(namedOnlyAgents(ctx, catalog))
	add(sessionProvider(ctx, in.home.ConfigFile(), catalog))

	add(codeHost())
	if records != nil && workErr == nil {
		add(upstreamDelivery(ctx, records, working))
	}
	add(stageBodies())

	report.CanStartRun = true
	var blocking []string
	for _, check := range report.Checks {
		if check.Blocking && !check.OK {
			report.CanStartRun = false
			blocking = append(blocking, check.Name)
		}
	}
	if !report.CanStartRun {
		report.Detail = "a run cannot start: " + strings.Join(blocking, ", ")
	}
	return report, nil
}

// sessionProvider reports whether the operator's chosen terminal session
// provider can host workers here. The default pipe always can. tmux needs a
// tmux binary on PATH and an agent that declares terminal_workers; without
// either, every launch is refused and the task stays Dispatched, which is
// the stall this row names before an operator waits on one. It reads the
// operator's file the way the service does at start, and resolves the agent
// list that file names (defaults included), which is what launches go
// through. It does not block a run: gate validation does not need a worker
// terminal, and a missing prerequisite is a fleet launch refusal rather than
// a run-start refusal.
func sessionProvider(ctx context.Context, configFile string, catalog *agents.Catalog) machine.Check {
	cfg, err := service.GlobalConfig(configFile)
	if err != nil {
		return machine.Check{
			Name:   machine.CheckSessionProvider,
			Detail: "the home's configuration could not be read, so whether workers can launch under it is unknown: " + err.Error(),
		}
	}
	if cfg.SessionProvider != config.SessionProviderTmux {
		return machine.Check{
			Name: machine.CheckSessionProvider,
			OK:   true,
			Detail: "workers run on pipes (the default); set session_provider: tmux in " +
				configFile + " to host each in an attachable terminal",
		}
	}
	tmux, lookErr := exec.LookPath("tmux")
	if lookErr != nil {
		return machine.Check{
			Name: machine.CheckSessionProvider,
			Detail: "session_provider is tmux and no tmux binary is on PATH, so every launch is refused and the task stays dispatched: " +
				lookErr.Error(),
		}
	}
	resolution, err := agents.Resolve(ctx, cfg.Agent, catalog)
	if err != nil {
		return machine.Check{
			Name: machine.CheckSessionProvider,
			Detail: "session_provider is tmux and the agent that would host workers could not be resolved, so every launch is refused and the task stays dispatched: " +
				err.Error(),
		}
	}
	if !resolution.Runner.Capabilities().Has(agents.CapabilityTerminalWorkers) {
		return machine.Check{
			Name: machine.CheckSessionProvider,
			Detail: fmt.Sprintf("session_provider is tmux and %s has not declared %s, so every launch is refused and the task stays dispatched",
				resolution.Name, agents.CapabilityTerminalWorkers),
		}
	}
	return machine.Check{
		Name: machine.CheckSessionProvider,
		OK:   true,
		Detail: fmt.Sprintf("tmux at %s, and %s declares %s",
			tmux, resolution.Name, agents.CapabilityTerminalWorkers),
	}
}

// namedOnlyAgents reports the adapters that resolve on this machine but that
// "auto" never chooses, separately from the "agent" check so that the agent a
// run would use is never confused with one that merely has a binary on PATH.
// Which adapters auto may choose is the catalog's declaration, not a list
// here. It never blocks and never changes what "auto" resolves; naming one of
// these in the agent key still selects it.
func namedOnlyAgents(ctx context.Context, catalog *agents.Catalog) machine.Check {
	var found []string
	for _, standing := range catalog.Survey(ctx) {
		if !standing.AutoEligible && standing.Runnable {
			found = append(found, standing.Name)
		}
	}
	if len(found) == 0 {
		return machine.Check{Name: machine.CheckNamedOnlyAgents, OK: true,
			Detail: "no adapter that auto never chooses resolves here"}
	}
	return machine.Check{
		Name: machine.CheckNamedOnlyAgents,
		Detail: strings.Join(found, ", ") + " resolve on this machine, but PRD section 12 defers them, so " +
			"auto never chooses them and a run uses one only when the agent key names it; nothing here " +
			"has checked them against their real CLIs",
	}
}

// gateCheck establishes what a run needs of this working copy: the gate it is
// validated through, and the repository record the service resolves the run
// against. Both, because either alone is a half-state a run cannot start from,
// and this is the check whose whole contract is deciding whether one can.
//
// The record is read through internal/store's own lookup, which is what the
// service asks, so this reports the answer starting a run would give rather
// than a second question shaped like it.
func gateCheck(ctx context.Context, records *store.Store, working string) machine.Check {
	binding, err := records.GateBinding(ctx, working)
	if err != nil {
		return machine.Check{Name: machine.CheckGate, Blocking: true, Detail: "this working copy is not bound to a gate; run assistant init"}
	}
	if _, err := records.RepositoryAt(ctx, working); err != nil {
		return machine.Check{
			Name:     machine.CheckGate,
			Blocking: true,
			Detail: fmt.Sprintf("bound to %s, and this working copy has no repository record, which a run is resolved against; run assistant init",
				binding.GateID),
		}
	}
	return machine.Check{Name: machine.CheckGate, OK: true, Blocking: true, Detail: "bound to " + binding.GateID}
}

// commitIdentity reports whether git names an author and a committer for a
// commit made in this working copy's gate repository, which is the repository
// a run's isolated copy is a linked worktree of and so the configuration a
// commit made in that copy is resolved against: the working copy's own
// repository configuration is not part of it, which is why this does not ask
// the working copy. The gate is located the way internal/service locates it
// before cutting a copy, and the question is vcs.Repository.CommitIdentity,
// whose contract says what its answer does and does not establish. It is made
// only when the gate check passed, because without a gate there is no
// repository a run's copy would be cut from to ask.
//
// It does not block. A run can start without an identity, and a step that has
// to record a change, a fix round or a rebase onto a moved upstream, fails
// there with git's own refusal; nothing supplies an identity in its place.
// What the row buys is that the operator learns here and is told the remedy.
//
// It answers for this process's environment. The commit is made by the
// service, which may have been started with another one, so an identity set
// only in a variable such as GIT_AUTHOR_NAME, or a GIT_CONFIG_GLOBAL that
// differs between the two, can make this answer differ from what a run meets.
func commitIdentity(ctx context.Context, home string, records *store.Store, working string) machine.Check {
	failed := func(err error) machine.Check {
		return machine.Check{
			Name: machine.CheckCommitIdentity,
			Detail: "git could not name who a commit made in this checkout's gate is recorded under; set one with " +
				"git config --global user.name and git config --global user.email: " + err.Error(),
		}
	}
	binding, err := records.GateBinding(ctx, working)
	if err != nil {
		return failed(err)
	}
	path, err := gate.RepositoryFor(home, binding.GateID)
	if err != nil {
		return failed(err)
	}
	bare, err := vcs.OpenBare(ctx, path, vcs.WithRedactor(redact.New()))
	if err != nil {
		return failed(err)
	}
	identity, err := bare.CommitIdentity(ctx)
	if err != nil {
		return failed(err)
	}
	detail := "author " + identity.Author
	if identity.Committer != identity.Author {
		detail += ", committer " + identity.Committer
	}
	return machine.Check{Name: machine.CheckCommitIdentity, OK: true, Detail: detail}
}

// codeHost reports whether the code-host provider command line resolves on
// this machine. It probes the host built the way internal/service builds the
// one its stage bodies get, so what it answers for is the command a run would
// invoke rather than a second spelling of it, and what the probe does and
// does not establish is forge.GitHubHost.Probe's contract.
//
// It does not block, because a run can start without a code host: the stages
// that talk to one refuse there, honestly, when they are reached. What the
// row buys is that the operator learns here rather than from a stage partway
// through a run.
func codeHost() machine.Check {
	resolved, err := forge.NewGitHubHost(redact.New()).Probe()
	if err != nil {
		return machine.Check{
			Name:   machine.CheckCodeHost,
			Detail: err.Error() + "; a run can start without it, and a stage that needs the code host reports the refusal itself",
		}
	}
	return machine.Check{Name: machine.CheckCodeHost, OK: true, Detail: resolved}
}

// upstreamDelivery reports how this checkout's recorded upstream treats the
// pull request and checks stages, from forge.ClassifyRemote over the
// repository row. A local or file:// upstream is OK and says runs cannot open
// a pull request here (they end pushed after push). An unsupported network
// host blocks a run from starting, matching internal/service's begin refusal.
func upstreamDelivery(ctx context.Context, records *store.Store, working string) machine.Check {
	repository, err := records.RepositoryAt(ctx, working)
	if err != nil {
		return machine.Check{
			Name:   machine.CheckUpstream,
			Detail: "this working copy has no repository record yet; run assistant init",
		}
	}
	switch forge.ClassifyRemote(repository.UpstreamURL) {
	case forge.RemoteGitHub:
		spec, _ := forge.GitHubRepository(repository.UpstreamURL)
		return machine.Check{
			Name:   machine.CheckUpstream,
			OK:     true,
			Detail: "pull requests open on " + spec,
		}
	case forge.RemoteLocal:
		return machine.Check{
			Name: machine.CheckUpstream,
			OK:   true,
			Detail: "no code host: runs here cannot open a pull request; after push they skip " +
				"the pull request and checks stages and end pushed",
		}
	default:
		return machine.Check{
			Name:     machine.CheckUpstream,
			Blocking: true,
			Detail: fmt.Sprintf("upstream %q names a host this build does not address; "+
				"a run is refused at start", repository.UpstreamURL),
		}
	}
}

// stageBodies reports how much of the gate this build actually implements. It
// does not block a run, and it is the difference between a run that stops for
// a decision at every stage and one that validates anything.
func stageBodies() machine.Check {
	implemented := stages.Implemented()
	if len(implemented) == len(pipeline.Order()) {
		return machine.Check{Name: machine.CheckStages, OK: true, Detail: "all nine stages have an implementation"}
	}
	names := make([]string, 0, len(implemented))
	for _, stage := range implemented {
		names = append(names, stage.String())
	}
	detail := "no stage has an implementation in this build, so every stage holds for a decision and nothing is validated"
	if len(names) > 0 {
		detail = fmt.Sprintf("%d of %d stages have an implementation (%s); the rest hold for a decision",
			len(names), len(pipeline.Order()), strings.Join(names, ", "))
	}
	return machine.Check{Name: machine.CheckStages, Detail: detail}
}

// resolvedAgents is the agent list this machine would resolve a run against.
// The service reads its own configuration, so what doctor answers here is
// about the agents that are runnable rather than about a resolved
// configuration this process does not hold.
func (in *invocation) resolvedAgents() []string { return []string{agents.AutoEntry} }

// serviceDetail says what the service check found.
func serviceDetail(state machine.Service) string {
	if state.Running {
		return state.Socket + ", build " + state.Build.String()
	}
	if state.Detail != "" {
		return state.Detail
	}
	return "not running; start it with assistant service start"
}
