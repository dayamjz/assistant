package service

import (
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/config"
)

// errUnhonored is the class of refusalUnhonored's refusals: the operator's
// file sets a key the configuration schema accepts and this build does not
// act on yet.
var errUnhonored = errors.New("service: the configuration sets something this build does not act on yet")

// unhonoredKeys are the global-only keys of PRD section 8's workers on
// another machine that the schema parses and nothing in this build reads:
// how to reach a remote host, the placement limits, and the grace a remote
// bridge applies. placement.arch is here too, for the copy of it the
// operator's own file may carry.
//
// Each is refused when the operator's file sets it, whatever the value,
// because before this build had the rows each was refused as an unrecognized
// key, and accepting one now and acting on none of it would read as a limit,
// a host, or a narrowing the operator does not have. A row leaves this list
// in the change that gives the key its reader.
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
//
// What it does not cover: a repository's trusted copy may set placement.arch
// and nothing refuses or reads it, so a task of such a repository runs on
// this machine whatever this machine's architecture is, as
// config.KeyPlacementArch states.
func refusalUnhonored(global config.Layer, cfg config.Config) error {
	for _, k := range unhonoredKeys {
		if global.IsSet(k) {
			return fmt.Errorf("%w: %s is set, and this build does not place workers on another machine or by limits yet; remove it from the file", errUnhonored, k)
		}
	}
	if cfg.SessionProvider == config.SessionProviderHerdr {
		return fmt.Errorf("%w: %s is %q, and this build has no %s provider yet; set it to %q or %q",
			errUnhonored, config.KeySessionProvider, cfg.SessionProvider, cfg.SessionProvider,
			config.SessionProviderPipe, config.SessionProviderTmux)
	}
	return nil
}
