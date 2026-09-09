package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
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
	var upstream, defaultBranch string
	if err := in.parseFlags("init", func(set *flag.FlagSet) {
		set.StringVar(&upstream, "upstream", "", "the remote the change is destined for; the default is this working copy's origin")
		set.StringVar(&defaultBranch, "default-branch", "", "the branch trusted configuration is read from; the default is what origin's HEAD names, or the branch checked out")
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

	intended, err := intendedRepository(ctx, working, upstream, defaultBranch)
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
	return machine.Init{
		Gate:       machine.Gate{Present: true, ID: built.ID(), Repository: built.Repository()},
		Reattached: built.Reattached(),
		Repository: repository,
	}, nil
}

// intendedRepository is the repository record the gate will validate, filling
// in from the working copy what the caller did not name. It reads and writes
// nothing, so a working copy that cannot answer is refused before the gate it
// would have been recorded against exists.
//
// The record's identifier is the gate's, which is not known until the gate is
// built, so it is the one field the caller fills in afterwards.
func intendedRepository(ctx context.Context, working, upstream, defaultBranch string) (store.Repository, error) {
	repo, err := vcs.OpenWorktree(ctx, working, vcs.WithRedactor(redact.New()))
	if err != nil {
		return store.Repository{}, err
	}
	if upstream == "" {
		if upstream, err = repo.RemoteURL(ctx, "origin"); err != nil {
			return store.Repository{}, fmt.Errorf("this working copy has no origin, so name the upstream with --upstream: %w", err)
		}
	}
	if defaultBranch == "" {
		if defaultBranch, err = defaultBranchOf(ctx, repo); err != nil {
			return store.Repository{}, err
		}
	}
	return store.Repository{
		WorkingPath:   working,
		UpstreamURL:   upstream,
		DefaultBranch: defaultBranch,
	}, nil
}

// defaultBranchOf is the branch PRD principle P7 reads trusted configuration
// from: what origin's own HEAD names, or the branch checked out when the
// working copy records nothing about origin.
//
// It reads what is already in the repository and fetches nothing, so the
// answer is what this working copy last learned. A repository whose default
// branch has moved since it was cloned records the old one until it is fetched
// again, which is why --default-branch exists.
func defaultBranchOf(ctx context.Context, repo *vcs.Repository) (string, error) {
	refs, err := repo.ListRefs(ctx, "refs/remotes/origin/HEAD")
	if err == nil {
		for _, ref := range refs {
			if name := strings.TrimPrefix(ref.Name, "refs/remotes/origin/"); name != "" && name != "HEAD" {
				return name, nil
			}
		}
	}
	branch, err := repo.HeadBranch(ctx)
	if err != nil {
		return "", fmt.Errorf("this working copy names no default branch, so name it with --default-branch: %w", err)
	}
	return branch, nil
}

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
// recovers work it holds.
//
// It refuses, because the module that owns that reconciliation does not exist.
// PRD section 8 gives custody the classify, synchronize and recover operations
// and calls it the only sanctioned mutation path, so a command that moved a
// local branch from here would be a second one. The verb is present and says
// what is missing rather than being absent and leaving a caller to guess
// whether it was ever specified.
func syncBranch(_ context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("sync", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	return machine.Plan{
		Detail: "reconciling a local branch with what the pipeline pushed is the custody module's, which this build does not have. Nothing was changed.",
	}, nil
}

// doctor checks every dependency and decides whether a run can start at all.
//
// The decision is the point of the command, so it is a field rather than
// something a reader infers from a list. A check that is not blocking is
// reported and does not decide: a build short of all nine stage bodies can
// start a run, and what that run does is hold at the stages stages.Holding
// names, which is worth knowing and is not the same as being unable to start.
func doctor(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("doctor", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	report := machine.Doctor{}
	add := func(check machine.Check) { report.Checks = append(report.Checks, check) }

	add(machine.Check{Name: "home", OK: true, Blocking: true, Detail: in.home.Root()})
	records, err := in.openStore(ctx)
	if err != nil {
		add(machine.Check{Name: "database", Blocking: true, Detail: err.Error()})
	} else {
		add(machine.Check{Name: "database", OK: true, Blocking: true, Detail: records.Path()})
		defer func() { _ = records.Close() }()
	}

	working, workErr := in.workingCopy(ctx)
	if workErr != nil {
		add(machine.Check{Name: "working copy", Blocking: true, Detail: workErr.Error()})
	} else {
		add(machine.Check{Name: "working copy", OK: true, Blocking: true, Detail: working})
	}

	if records != nil && workErr == nil {
		add(gateCheck(ctx, records, working))
	}

	service := in.serviceState(ctx)
	add(machine.Check{
		Name:     "service",
		OK:       service.Running,
		Blocking: true,
		Detail:   serviceDetail(service),
	})

	resolution, err := agents.Resolve(ctx, in.resolvedAgents(), agents.DefaultCatalog())
	if err != nil {
		add(machine.Check{Name: "agent", Blocking: true, Detail: err.Error()})
	} else {
		add(machine.Check{Name: "agent", OK: true, Blocking: true, Detail: resolution.Name})
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
		return machine.Check{Name: "gate", Blocking: true, Detail: "this working copy is not bound to a gate; run assistant init"}
	}
	if _, err := records.RepositoryAt(ctx, working); err != nil {
		return machine.Check{
			Name:     "gate",
			Blocking: true,
			Detail: fmt.Sprintf("bound to %s, and this working copy has no repository record, which a run is resolved against; run assistant init",
				binding.GateID),
		}
	}
	return machine.Check{Name: "gate", OK: true, Blocking: true, Detail: "bound to " + binding.GateID}
}

// stageBodies reports how much of the gate this build actually implements. It
// does not block a run, and it is the difference between a run that stops for
// a decision at every stage and one that validates anything.
func stageBodies() machine.Check {
	implemented := stages.Implemented()
	if len(implemented) == len(pipeline.Order()) {
		return machine.Check{Name: "stages", OK: true, Detail: "all nine stages have an implementation"}
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
	return machine.Check{Name: "stages", Detail: detail}
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
