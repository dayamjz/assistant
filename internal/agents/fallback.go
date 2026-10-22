package agents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// WithFallback adds one explicitly configured backup to primary. Each Run
// remains session-free. Only FallbackAllowed failures reach openBackup, and
// each call tries at most two agents. The opener must preserve the same role's
// grants and validate the backup's model and capabilities before returning it.
func WithFallback(primary Runner, openBackup func(context.Context) (Runner, error)) Runner {
	r := fallbackRunner{primary: primary, backup: openBackup}
	if primary.Capabilities().Has(CapabilityResumableSessions) {
		return &fallbackSessions{fallbackRunner: r}
	}
	return &r
}

type fallbackRunner struct {
	primary Runner
	backup  func(context.Context) (Runner, error)
}

// Name identifies the configured primary; invocation records identify the agent actually called.
func (r *fallbackRunner) Name() string { return r.primary.Name() }

// Capabilities preserves role capabilities and exposes no worker mechanism.
func (r *fallbackRunner) Capabilities() Capabilities {
	var caps []Capability
	for _, c := range r.primary.Capabilities().List() {
		if c != CapabilityWorkerSessions && c != CapabilityTerminalWorkers {
			caps = append(caps, c)
		}
	}
	return Declare(caps...)
}
func cleanRoleInvocation(inv Invocation) Invocation {
	inv.Model, inv.Provider, inv.Type, inv.Version, inv.Effort, inv.ContextWindow = "", "", "", "", "", ""
	return inv
}

// Run tries at most two session-free invocations, stopping on non-availability errors.
func (r *fallbackRunner) Run(ctx context.Context, p Purpose, inv Invocation) (Result, error) {
	result, err := r.primary.Run(ctx, p, inv)
	if err == nil || ctx.Err() != nil || !FallbackAllowed(err) {
		return result, err
	}
	backup, openErr := r.backup(ctx)
	if openErr != nil {
		return Result{}, errors.Join(err, openErr)
	}
	result, nextErr := backup.Run(ctx, p, cleanRoleInvocation(inv))
	if nextErr != nil {
		return Result{}, errors.Join(err, nextErr)
	}
	return result, nil
}

type fallbackSessions struct{ fallbackRunner }

const fallbackReferencePrefix = "assistant-fallback-v1:"

type fallbackReference struct {
	Backup  bool   `json:"backup"`
	Agent   string `json:"agent"`
	Session string `json:"session"`
}

func readFallbackReference(s string) (fallbackReference, error) {
	var ref fallbackReference
	bytes, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, fallbackReferencePrefix))
	if err != nil {
		return ref, err
	}
	if err = json.Unmarshal(bytes, &ref); err != nil {
		return ref, err
	}
	if ref.Agent == "" {
		return ref, errors.New("fallback session names no agent")
	}
	return ref, nil
}

// Fixer restores only an agent-qualified reference, or opens a fresh primary session.
func (r *fallbackSessions) Fixer(ctx context.Context, resume string) (Fixer, error) {
	f := &fallbackFixer{runner: r}
	if strings.HasPrefix(resume, fallbackReferencePrefix) {
		ref, err := readFallbackReference(resume)
		if err != nil {
			return nil, fmt.Errorf("agents: invalid fallback session: %w", err)
		}
		f.backup, f.owner, f.resume = ref.Backup, ref.Agent, ref.Session
	} else {
		f.owner, f.resume = r.primary.Name(), resume
	}
	return f, nil
}

// SessionAgent returns the owner only of references created by this fallback
// runner; ordinary references continue to belong to its primary.
func (r *fallbackSessions) SessionAgent(reference string) string {
	if strings.HasPrefix(reference, fallbackReferencePrefix) {
		if ref, err := readFallbackReference(reference); err == nil {
			return ref.Agent
		}
	}
	return r.primary.Name()
}

// SessionAgent reports the adapter owning a runner's stored fixer reference.
// Wrappers forward this fact without exposing a runner or a session handle.
func SessionAgent(r Runner, reference string) string {
	if owner, ok := r.(interface{ SessionAgent(string) string }); ok {
		return owner.SessionAgent(reference)
	}
	return r.Name()
}

type fallbackFixer struct {
	mu            sync.Mutex
	runner        *fallbackSessions
	backup        bool
	owner, resume string
	session       Fixer
}

func (f *fallbackFixer) open(ctx context.Context) error {
	runner := f.runner.primary
	if f.backup {
		var err error
		runner, err = f.runner.backup(ctx)
		if err != nil {
			return err
		}
	}
	if f.owner != "" && f.owner != runner.Name() {
		f.resume = ""
	}
	f.owner = runner.Name()
	session, err := OpenFixer(ctx, runner, f.resume)
	if err == nil {
		f.session = session
	}
	return err
}

// Apply keeps a backup session after handoff and never accepts a review shape.
func (f *fallbackFixer) Apply(ctx context.Context, inv Invocation) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := inv.ValidateForFixer(); err != nil {
		return Result{}, err
	}
	if f.session == nil {
		if err := f.open(ctx); err != nil {
			if f.backup || ctx.Err() != nil || !FallbackAllowed(err) {
				return Result{}, err
			}
			f.backup = true
			f.owner = ""
			f.resume = ""
			if next := f.open(ctx); next != nil {
				return Result{}, errors.Join(err, next)
			}
		}
	}
	if f.backup {
		inv = cleanRoleInvocation(inv)
	}
	result, err := f.session.Apply(ctx, inv)
	if err == nil || f.backup || ctx.Err() != nil || !FallbackAllowed(err) {
		return result, err
	}
	f.backup = true
	f.owner = ""
	f.resume = ""
	f.session = nil
	if next := f.open(ctx); next != nil {
		return Result{}, errors.Join(err, next)
	}
	result, next := f.session.Apply(ctx, cleanRoleInvocation(inv))
	if next != nil {
		return Result{}, errors.Join(err, next)
	}
	return result, nil
}

// Reference binds the chosen agent and its opaque session for a later reopen.
func (f *fallbackFixer) Reference() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	reference := f.resume
	if f.session != nil {
		reference = f.session.Reference()
	}
	if reference == "" {
		return ""
	}
	bytes, _ := json.Marshal(fallbackReference{Backup: f.backup, Agent: f.owner, Session: reference})
	return fallbackReferencePrefix + base64.RawURLEncoding.EncodeToString(bytes)
}
