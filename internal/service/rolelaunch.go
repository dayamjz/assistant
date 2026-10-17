package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
)

// gateLaunch is one program role resolved for the launches that role owns.
// pinned is false when the role names no agent, which means the service
// agent list, with no model and no effort.
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
}

// fields is the attribution a launch of roleName carries. An unpinned role
// names the role and nothing else, which the record keeps as not named.
func (g gateLaunch) fields(roleName string) roleFields {
	return roleFields{
		role: roleName, provider: g.provider, typ: g.typ, version: g.version,
		model: g.model, effort: g.effort,
	}
}

// gateRole resolves one program role from the operator's roles table.
//
// A role that is absent, and a role that names no agent, uses the service
// agent. A role that names an agent is a pin of that one name: an agent that
// cannot run, a model the adapter has not declared, and an effort it has not
// declared are refused here, and no other agent is returned in its place.
// The worker role is not resolved here; launch.go owns that path.
func (s *Service) gateRole(ctx context.Context, roleName string) (gateLaunch, error) {
	role, ok := s.cfg.Roles[roleName]
	if !ok {
		return gateLaunch{}, nil
	}
	if role.Agent == "" {
		if role.Provider != "" || role.Effort != "" {
			return gateLaunch{}, fmt.Errorf("service: the %s role names a model or an effort and no agent", roleName)
		}
		return gateLaunch{}, nil
	}
	selection, err := agents.Route(ctx, s.catalog, nil, agents.Capabilities{}, role.Agent)
	if err != nil {
		return gateLaunch{}, fmt.Errorf("service: the %s role names %s, which cannot start: %w", roleName, role.Agent, err)
	}
	var model string
	if role.Provider != "" {
		model, err = agents.Translate(selection.Resolution.Name, role.Provider, role.Type, role.Version)
		if err != nil {
			return gateLaunch{}, fmt.Errorf("service: the %s role: %w", roleName, err)
		}
	}
	if err := agents.AcceptsEffort(selection.Resolution.Name, role.Effort); err != nil {
		return gateLaunch{}, fmt.Errorf("service: the %s role: %w", roleName, err)
	}
	return gateLaunch{
		pinned:   true,
		runner:   selection.Resolution.Runner,
		caps:     selection.Resolution.Capabilities,
		name:     selection.Resolution.Name,
		provider: role.Provider,
		typ:      role.Type,
		version:  role.Version,
		model:    model,
		effort:   role.Effort,
	}, nil
}

// roleCapabilities refuses a pinned review or fix agent that has not declared
// a capability the run would launch it with. Suppression is run-wide, so a
// review agent that cannot suppress fails the run the same way the service
// agent does. The fix agent's suppression is checked only when a fix round
// would be built, which is when a limit is above zero.
func (d *driver) roleCapabilities(cfg config.Config) error {
	if !cfg.SuppressProjectInstructions {
		return nil
	}
	need := agents.CapabilitySuppressProjectInstructions
	if d.reviewPinned && !d.reviewCaps.Has(need) {
		return &agents.CapabilityError{Agent: d.reviewName, Capability: need, Path: "the review role"}
	}
	if d.fixPinned && fixRoundsConfigured(cfg.FixRounds) && !d.fixCaps.Has(need) {
		return &agents.CapabilityError{Agent: d.fixName, Capability: need, Path: "the fix role"}
	}
	return nil
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
// role name, the triple and effort it named, and the model identifier the
// adapter was asked to start. Empty text is not named.
type roleFields struct {
	role, provider, typ, version, model, effort string
}

func (f roleFields) apply(inv agents.Invocation) agents.Invocation {
	inv.Role = f.role
	inv.Provider = f.provider
	inv.Type = f.typ
	inv.Version = f.version
	inv.Effort = f.effort
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
