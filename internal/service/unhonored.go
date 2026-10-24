package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
)

// errUnhonored is the class of refusalUnhonored's and
// refusalUnhonoredRepository's refusals: the operator's file, or a
// repository's trusted copy, sets a key the configuration schema accepts and
// this build does not act on yet.
var errUnhonored = errors.New("service: the configuration sets something this build does not act on yet")

// unhonoredKeys are the keys of PRD section 8's workers on another machine
// that the schema parses and nothing in this build reads: how to reach a
// remote host, the placement limits, and the grace a remote bridge applies,
// which are global-only, and placement.arch, which is trusted-only.
//
// Each is refused wherever a document that may set it is read, whatever the
// value: the operator's file by refusalUnhonored, and a repository's trusted
// copy by refusalUnhonoredRepository. Before this build had the rows each was
// refused as an unrecognized key, and accepting one now and acting on none of
// it would read as a limit, a host, or a narrowing nobody has. A row leaves
// this list in the change that gives the key its reader.
var unhonoredKeys = []config.Key{
	config.KeyRemoteHosts,
	config.KeyPlacementLocal,
	config.KeyPlacementArch,
	config.KeyRemoteDisconnectGrace,
}

// refusalUnhonored refuses the operator's configuration when it asks for
// something this build would not do, naming the key: a key of
// unhonoredKeys that global set, or the herdr session provider. This build
// has no herdr provider, and sessionHost starts every provider but tmux on
// pipes, so a home that named herdr would have its workers started some
// other way than the one the operator chose; the refusal is what stops that,
// and every reader of the operator's file reads it through resolveConfig, so
// a service whose file names herdr does not open.
func refusalUnhonored(global config.Layer, cfg config.Config) error {
	if k, set := unhonoredSet(global); set {
		return fmt.Errorf("%w: %s is set, and this build places every worker on this machine and by none of these settings yet; remove it from the file", errUnhonored, k)
	}
	if cfg.SessionProvider == config.SessionProviderHerdr {
		return fmt.Errorf("%w: %s is %q, and this build has no %s provider yet; set it to %q or %q",
			errUnhonored, config.KeySessionProvider, cfg.SessionProvider, cfg.SessionProvider,
			config.SessionProviderPipe, config.SessionProviderTmux)
	}
	return nil
}

// refusalUnhonoredRepository refuses a repository's trusted copy that sets a
// key of unhonoredKeys, naming the key. Of them only placement.arch can reach
// here, the rest being global-only and refused when the copy was parsed. This
// build places every task on this machine and takes no reading of any
// machine's architecture, so it cannot tell whether this machine is one the
// copy allows, and PRD section 8 makes a machine whose reading cannot be
// taken ineligible: taking the repository's tasks would run them where they
// may have been told not to. The refusal holds whatever architectures the
// copy names, this machine's own included, because which architecture this
// machine reports is the placement's to read, not this build's to assume.
//
// trustedLayerAt calls it on every trusted copy it parses, which is every
// read of one this service takes: a run's, a delivery task's intake and
// promotion, standing consent's, and the one placeable makes before a task's
// first claim. So an investigation, whose intake reads no trusted copy, and a
// delivery task taken before its default branch gained the key are both kept
// queued at dispatch rather than placed here. The change that places by
// architecture removes the row from unhonoredKeys.
func refusalUnhonoredRepository(trusted config.Layer) error {
	if k, set := unhonoredSet(trusted); set {
		return fmt.Errorf("%w: %s is set, and this build places every task on this machine and by no architecture yet; remove it from the default branch's %s",
			errUnhonored, k, config.RepositoryDocument)
	}
	return nil
}

// unhonoredSet reports the first key of unhonoredKeys that layer sets.
func unhonoredSet(layer config.Layer) (config.Key, bool) {
	for _, k := range unhonoredKeys {
		if layer.IsSet(k) {
			return k, true
		}
	}
	return "", false
}

// errTaskUnplaced is the class of placeable's refusals: the task was not
// dispatched, claimed no attempt, and still resolves Dispatched, so it stays
// queued for a later dispatch pass.
var errTaskUnplaced = errors.New("service: the task was not placed on this machine")

// placeable reads the repository's trusted copy before a task's first claim,
// which is the one moment a task is placed, and refuses with errTaskUnplaced
// when the copy does not allow this machine or cannot be read. The read is
// trustedLayerAt's through trustedTaskLayer, so a copy setting a key of
// unhonoredKeys is refused on refusalUnhonoredRepository's terms, and a copy
// that cannot be fetched, read or parsed refuses too (P7): either way the
// task is left queued rather than placed here on a guess. The fetch lands
// under dispatchReadNamespace, in a reference of this read's own, so a
// second dispatcher of the task cannot remove it before it is resolved and
// have this one refused for a read the repository did not fail (#552).
//
// It is what keeps a task from reaching this machine under a placement.arch
// set after its intake, or under one an investigation's intake never read.
// dispatchTask gives the slot back on this refusal, so the next dispatch
// pass, at the next intake, release or open, reads the copy again and
// dispatches the task once the key is gone.
//
// The task is surfaced the way the supervisor loop surfaces any Dispatched
// task with no terminal to read: a terminal-unchanged wake on the loop's
// second pass over it, and again each escalation_window (supervise.go), the
// cadence PRD section 10 asks of a task no eligible machine satisfies. What
// that wake does not carry is why: the coordinator reads it as a worker
// whose screen has not changed, the task reads Dispatched, and only this
// refusal, in the service's log, says the wait is placement. Saying so in
// the wake is the placement's to add (#526).
func (s *Service) placeable(ctx context.Context, repository store.Repository, id string) error {
	forWhat := "the dispatch of task " + id
	if _, err := s.trustedTaskLayer(ctx, repository, dispatchReadNamespace, id, forWhat); err != nil {
		return fmt.Errorf("%w: %s reads its repository's trusted configuration first: %w", errTaskUnplaced, forWhat, err)
	}
	return nil
}
