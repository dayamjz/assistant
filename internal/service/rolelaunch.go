package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
)

// gateLaunch is one program role resolved for the launches that role owns.
// pinned is false when the role names no agent, which means the service
// agent list, with no model, no effort, and no context window.
type gateLaunch struct {
	pinned   bool
	runner   agents.Runner
	caps     agents.Capabilities
	name     string
	provider string
	typ      string
	version  string
	model    string
	effort   string
	window   string
}

// fields is the attribution a launch of roleName carries. An unpinned role
// names the role and nothing else, which the record keeps as not named.
func (g gateLaunch) fields(roleName string) roleFields {
	return roleFields{
		role: roleName, provider: g.provider, typ: g.typ, version: g.version,
		model: g.model, effort: g.effort, window: g.window,
	}
}

// gateRole resolves one program role from the operator's roles table.
//
// A role that is absent, and a role that names no agent, uses the service
// agent. A role that names an agent is a pin of that one name: an agent that
// cannot run, a model the adapter has not declared, a context window it has
// not declared on that model, and an effort it has not declared are refused
// here, naming the role. An explicit backup is resolved lazily only
// after a recognized availability failure; it cannot cover invalid settings.
// The worker role is not resolved here; launch.go owns that path.
func (s *Service) gateRole(ctx context.Context, roleName string) (gateLaunch, error) {
	role, ok := s.cfg.Roles[roleName]
	if !ok {
		return gateLaunch{}, nil
	}
	return s.gateRoleChoice(ctx, roleName, role)
}

func (s *Service) gateRoleChoice(ctx context.Context, roleName string, role config.Role) (gateLaunch, error) {
	if role.Agent == "" {
		if role.Provider != "" || role.Effort != "" || role.ContextWindow != "" {
			return gateLaunch{}, fmt.Errorf("service: the %s role names a model, an effort, or a context window and no agent", roleName)
		}
		return gateLaunch{}, nil
	}
	var requestedModel string
	if role.Provider != "" || role.ContextWindow != "" {
		var err error
		requestedModel, err = agents.Translate(role.Agent, role.Provider, role.Type, role.Version, role.ContextWindow)
		if err != nil {
			return gateLaunch{}, fmt.Errorf("service: the %s role: %w", roleName, err)
		}
	}
	if err := agents.AcceptsEffort(role.Agent, role.Effort); err != nil {
		return gateLaunch{}, err
	}
	backup := role.Fallback
	role.Fallback = nil
	if backup != nil {
		primary, err := s.gateRoleChoice(ctx, roleName, role)
		openBackup := func(ctx context.Context) (agents.Runner, error) {
			next, err := s.gateRoleChoice(ctx, roleName, *backup)
			if err != nil {
				return nil, err
			}
			return s.readyGateRole(next, roleName)
		}
		if err != nil {
			if !agents.FallbackAllowed(err) {
				return gateLaunch{}, err
			}
			ready, backupErr := s.gateRoleChoice(ctx, roleName, *backup)
			if backupErr != nil {
				return gateLaunch{}, errors.Join(err, backupErr)
			}
			var caps []agents.Capability
			for _, c := range ready.caps.List() {
				if c != agents.CapabilityWorkerSessions && c != agents.CapabilityTerminalWorkers {
					caps = append(caps, c)
				}
			}
			deferred := deferredRoleRunner{name: role.Agent, caps: agents.Declare(caps...), open: func(ctx context.Context) (agents.Runner, error) {
				available, err := s.gateRoleChoice(ctx, roleName, role)
				if err != nil {
					return nil, err
				}
				return s.readyGateRole(available, roleName)
			}}
			var runner agents.Runner = &deferred
			if deferred.caps.Has(agents.CapabilityResumableSessions) {
				runner = &deferredRoleSessions{deferredRoleRunner: deferred}
			}
			primary = gateLaunch{pinned: true, runner: runner, caps: deferred.caps, name: role.Agent, provider: role.Provider, typ: role.Type, version: role.Version, model: requestedModel, effort: role.Effort, window: role.ContextWindow}
		}
		primary.runner = agents.WithFallback(stampRunner(primary.runner, primary.fields(roleName)), openBackup)
		return primary, nil
	}
	selection, err := agents.Route(ctx, s.catalog, nil, agents.Capabilities{}, role.Agent)
	if err != nil {
		return gateLaunch{}, fmt.Errorf("service: the %s role names %s, which cannot start: %w", roleName, role.Agent, err)
	}

	return gateLaunch{
		pinned:   true,
		runner:   selection.Resolution.Runner,
		caps:     selection.Resolution.Capabilities,
		name:     selection.Resolution.Name,
		provider: role.Provider,
		typ:      role.Type,
		version:  role.Version,
		model:    requestedModel,
		effort:   role.Effort,
		window:   role.ContextWindow,
	}, nil
}

// roleCapabilities refuses a pinned review or fix agent that has not declared
// a capability the run would launch it with. Suppression is run-wide, so a
// review agent that cannot suppress fails the run the same way the service
// agent does. The fix agent's suppression is checked only when a fix round
// would be built, which is when a limit is above zero.
func (d *driver) roleCapabilities(cfg config.Config) error {
	if d.reviewPinned {
		if err := requireRoleSuppression(d.reviewName, d.reviewCaps, "review", cfg.SuppressProjectInstructions); err != nil {
			return err
		}
	}
	if d.fixPinned && fixRoundsConfigured(cfg.FixRounds) {
		return requireRoleSuppression(d.fixName, d.fixCaps, "fix", cfg.SuppressProjectInstructions)
	}
	return nil
}
func requireRoleSuppression(name string, caps agents.Capabilities, roleName string, required bool) error {
	if required && !caps.Has(agents.CapabilitySuppressProjectInstructions) {
		return &agents.CapabilityError{Agent: name, Capability: agents.CapabilitySuppressProjectInstructions, Path: roleName + " role"}
	}
	return nil
}
func (s *Service) readyGateRole(next gateLaunch, roleName string) (agents.Runner, error) {
	if err := requireRoleSuppression(next.name, next.caps, roleName, s.cfg.SuppressProjectInstructions); err != nil {
		return nil, err
	}
	return stampRunner(next.runner, next.fields(roleName)), nil
}

// adapterCapabilities is the declaration pipeline.New checks. It is the
// service agent's, plus resumable sessions when the fix role's own agent
// declares them, because the fixer path runs on that agent and the service
// agent's declaration would otherwise refuse a fix path that agent can run.
func (d *driver) adapterCapabilities() agents.Capabilities {
	caps := d.agent.Capabilities
	if d.fixPinned && d.fixCaps.Has(agents.CapabilityResumableSessions) && !caps.Has(agents.CapabilityResumableSessions) {
		caps = agents.Declare(append(caps.List(), agents.CapabilityResumableSessions)...)
	}
	return caps
}

// fixRoundsConfigured reports whether any stage may take a fix round.
func fixRoundsConfigured(rounds config.FixRounds) bool {
	return rounds.Review > 0 || rounds.Rebase > 0 || rounds.Test > 0 || rounds.Lint > 0 || rounds.Checks > 0
}

// purposeRunner sends a review invocation to the review role's runner and
// every other purpose to the service agent. It is not a session runner: the
// fixer is opened on the fix role's own runner, and a review has no route
// to that session from here.
type purposeRunner struct {
	base   agents.Runner
	review agents.Runner
}

func (p purposeRunner) Name() string { return p.base.Name() }

func (p purposeRunner) Capabilities() agents.Capabilities { return p.base.Capabilities() }

func (p purposeRunner) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	if purpose == agents.PurposeReview && p.review != nil {
		return p.review.Run(ctx, purpose, inv)
	}
	return p.base.Run(ctx, purpose, inv)
}

// roleFields is what a review or fix launch records about the role: the
// role name, the triple, effort, and context window it named, and the model
// identifier the adapter was asked to start. Empty text is not named.
type roleFields struct {
	role, provider, typ, version, model, effort, window string
}

func (f roleFields) apply(inv agents.Invocation) agents.Invocation {
	inv.Role = f.role
	inv.Provider = f.provider
	inv.Type = f.typ
	inv.Version = f.version
	inv.Effort = f.effort
	inv.ContextWindow = f.window
	if f.model != "" {
		inv.Model = f.model
	}
	return inv
}

// stampRunner returns next with the role's attribution set on every
// invocation it runs and every fix round it opens. A session runner stays a
// session runner, so the fixer path still opens through agents.OpenFixer.
func stampRunner(next agents.Runner, fields roleFields) agents.Runner {
	stamped := stampedRunner{next: next, fields: fields}
	if sessions, ok := next.(agents.SessionRunner); ok {
		return stampedSession{stampedRunner: stamped, sessions: sessions}
	}
	return stamped
}

type stampedRunner struct {
	next   agents.Runner
	fields roleFields
}

func (s stampedRunner) Name() string { return s.next.Name() }

func (s stampedRunner) Capabilities() agents.Capabilities { return s.next.Capabilities() }

func (s stampedRunner) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	return s.next.Run(ctx, purpose, s.stamp(inv))
}

func (s stampedRunner) stamp(inv agents.Invocation) agents.Invocation {
	return s.fields.apply(inv)
}

type stampedSession struct {
	stampedRunner
	sessions agents.SessionRunner
}

func (s stampedSession) Fixer(ctx context.Context, resume string) (agents.Fixer, error) {
	fixer, err := s.sessions.Fixer(ctx, resume)
	if err != nil {
		return nil, err
	}
	return stampedFixer{inner: fixer, fields: s.fields}, nil
}

type stampedFixer struct {
	inner  agents.Fixer
	fields roleFields
}

func (s stampedFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	return s.inner.Apply(ctx, s.fields.apply(inv))
}

func (s stampedFixer) Reference() string { return s.inner.Reference() }

// SessionAgent forwards session ownership without exposing a runner or session.
func (s stampedRunner) SessionAgent(reference string) string {
	return agents.SessionAgent(s.next, reference)
}

// A missing primary is re-resolved at each fresh review or fixer open. The
// placeholder advertises the available backup path's role capabilities; the
// real primary's declaration is checked before it is ever invoked.
type deferredRoleRunner struct {
	name string
	caps agents.Capabilities
	open func(context.Context) (agents.Runner, error)
}

// Name keeps the configured primary identity while its binary is unavailable.
func (r *deferredRoleRunner) Name() string { return r.name }

// Capabilities describes the verified backup path while resolution is deferred.
func (r *deferredRoleRunner) Capabilities() agents.Capabilities { return r.caps }

// Run resolves the configured primary anew before a fresh invocation.
func (r *deferredRoleRunner) Run(ctx context.Context, p agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	runner, err := r.open(ctx)
	if err != nil {
		return agents.Result{}, err
	}
	return runner.Run(ctx, p, inv)
}

type deferredRoleSessions struct{ deferredRoleRunner }

// Fixer verifies the recovered primary before opening its own session.
func (r *deferredRoleSessions) Fixer(ctx context.Context, resume string) (agents.Fixer, error) {
	runner, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	return agents.OpenFixer(ctx, runner, resume)
}
