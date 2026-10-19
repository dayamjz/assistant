package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Stable reason codes a worker selection is recorded under. Route returns
// the first two. The two fallback reasons are the selection of a role's
// configured backup after its primary failed, which Route does not decide
// and FallbackReason names. SelectionReasons lists all four.
const (
	// ReasonOverride is an explicit operator pin: Override named the entry
	// that was selected.
	ReasonOverride = "override"
	// ReasonOrdered is the first runnable entry in the expanded preference
	// list whose capabilities cover Need.
	ReasonOrdered = "ordered"
	// ReasonFallbackUnavailable is a role's configured backup, selected
	// because its primary could not start (FailureUnavailable).
	ReasonFallbackUnavailable = "fallback-" + string(FailureUnavailable)
	// ReasonFallbackQuota is a role's configured backup, selected because
	// its primary reported a recognized quota failure (FailureQuota).
	ReasonFallbackQuota = "fallback-" + string(FailureQuota)
)

// SelectionReasons is every reason a worker selection is recorded under, in
// the order the constants above declare them. It is the one list a record
// or a document naming them is checked against.
func SelectionReasons() []string {
	return []string{ReasonOverride, ReasonOrdered, ReasonFallbackUnavailable, ReasonFallbackQuota}
}

// FallbackReason is the selection reason of a backup chosen after its
// primary failed with f. Only the two failures that reach a backup have
// one; any other failure is an error, so a caller cannot record a fallback
// under a reason the fallback path never admits.
func FallbackReason(f Failure) (string, error) {
	switch f {
	case FailureUnavailable:
		return ReasonFallbackUnavailable, nil
	case FailureQuota:
		return ReasonFallbackQuota, nil
	}
	return "", fmt.Errorf("agents: a %q failure does not select a backup", f)
}

// Stable reason codes Unavailability.Reason carries: why Resolve or Route
// passed over an entry. SkipReasons lists them.
const (
	// SkipNamesNoAgent is an entry with no name in it.
	SkipNamesNoAgent = "names-no-agent"
	// SkipNotInBuild is a name this build has no adapter for.
	SkipNotInBuild = "not-in-build"
	// SkipCannotRun is an adapter whose factory reported it cannot run
	// here, such as a missing executable.
	SkipCannotRun = "cannot-run"
	// SkipMissingCapability is a runnable adapter that does not declare a
	// capability the path needs. Only Route skips for it, and
	// Unavailability.Err is then the *CapabilityError naming which.
	SkipMissingCapability = "missing-capability"
)

// SkipReasons is every reason an entry is passed over under, in the order
// the constants above declare them.
func SkipReasons() []string {
	return []string{SkipNamesNoAgent, SkipNotInBuild, SkipCannotRun, SkipMissingCapability}
}

// Selection is the adapter Route chose for a path that needs capabilities,
// and why. Resolution is what Resolve would have returned for that entry;
// Reason is ReasonOverride or ReasonOrdered.
type Selection struct {
	Resolution Resolution
	Reason     string
}

// NeedForWorker is the capability set a task worker needs: worker sessions
// always, and terminal workers when the session provider is tmux.
func NeedForWorker(tmux bool) Capabilities {
	if tmux {
		return Declare(CapabilityWorkerSessions, CapabilityTerminalWorkers)
	}
	return Declare(CapabilityWorkerSessions)
}

// Covers reports whether c declares every capability in need. An empty need
// is covered by any declaration, including none. An unrecognized capability
// in need is never covered, on Has's terms.
func (c Capabilities) Covers(need Capabilities) bool {
	for _, capability := range need.List() {
		if !c.Has(capability) {
			return false
		}
	}
	return true
}

// Route picks the first runnable agent from entries whose declared
// capabilities cover Need, reporting which one it was and why.
//
// Override, when non-empty after trimming, is the only entry considered: it
// is not expanded as auto, and a runnable adapter that does not cover Need
// is refused with *CapabilityError rather than falling through to another
// entry. That is what an operator pin means: the named adapter, or nothing.
//
// Without an override, entries are expanded as Resolve expands them, tried
// in order, and a runnable adapter that does not cover Need is skipped with
// a *CapabilityError in Skipped rather than ending the search. The first
// runnable adapter that covers Need wins, with ReasonOverride when an
// override selected it and ReasonOrdered otherwise.
//
// Resolving nothing is a *ResolutionError wrapping ErrNoAgent, on Resolve's
// terms. An adapter whose declaration and type disagree ends the whole
// routing with *AdapterError, as Resolve does. Route does not decide grants:
// a selected worker still runs under GrantWorker through the ordinary
// worker start path.
func Route(ctx context.Context, catalog *Catalog, entries []string, need Capabilities, override string) (Selection, error) {
	if catalog == nil {
		catalog = DefaultCatalog()
	}
	reason := ReasonOrdered
	var candidates []string
	if pin := strings.TrimSpace(override); pin != "" {
		reason = ReasonOverride
		candidates = []string{pin}
	} else {
		candidates = expand(entries, catalog)
	}

	var skipped []Unavailability
	for i, entry := range candidates {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			skipped = append(skipped, Unavailability{
				Entry:  entry,
				Reason: SkipNamesNoAgent,
				Err:    errors.New("entry names no agent"),
			})
			continue
		}
		name, args := fields[0], fields[1:]
		factory, ok := catalog.lookup(name)
		if !ok {
			skipped = append(skipped, Unavailability{
				Entry:  entry,
				Name:   name,
				Reason: SkipNotInBuild,
				Err:    errors.New("this build has no adapter named " + name),
			})
			continue
		}
		runner, err := factory.New(ctx, args)
		if err != nil {
			skipped = append(skipped, Unavailability{Entry: entry, Name: name, Reason: SkipCannotRun, Err: err})
			continue
		}
		declared, err := verifyDeclaration(name, runner)
		if err != nil {
			return Selection{}, err
		}
		if missing, found := firstMissing(need, declared); found {
			capErr := &CapabilityError{Agent: name, Capability: missing, Path: "worker routing"}
			if reason == ReasonOverride {
				return Selection{}, capErr
			}
			skipped = append(skipped, Unavailability{Entry: entry, Name: name, Reason: SkipMissingCapability, Err: capErr})
			continue
		}
		return Selection{
			Resolution: Resolution{
				Runner:       runner,
				Name:         name,
				Capabilities: declared,
				Entry:        entry,
				Index:        i,
				Skipped:      skipped,
			},
			Reason: reason,
		}, nil
	}
	return Selection{}, &ResolutionError{Tried: skipped}
}

// firstMissing reports the first capability in need that have does not
// declare, in table order.
func firstMissing(need, have Capabilities) (Capability, bool) {
	for _, capability := range need.List() {
		if !have.Has(capability) {
			return capability, true
		}
	}
	return "", false
}
