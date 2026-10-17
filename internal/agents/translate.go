package agents

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
)

// ErrUndeclaredModel reports that the adapter has not declared a translation
// for the role's model triple or effort. The launch is refused and no other
// provider, type, version, or agent is started in its place.
var ErrUndeclaredModel = errors.New("agents: the adapter has not declared a translation for this model")

// Translate returns the model identifier adapter starts for the role's
// provider, type, and version. A triple outside the role table, and a triple
// the adapter has not declared, are ErrUndeclaredModel.
//
// Claude translates every anthropic cell to claude-<type>-<version>, with a
// dot in the version written as a hyphen. That identifier is what this
// adapter passes to the CLI. It is not a claim that the installed CLI accepts
// the cell.
func Translate(adapter, provider, typ, version string) (string, error) {
	if !config.KnownModel(provider, typ, version) {
		return "", fmt.Errorf("%w: %s %s %s is not in the model table", ErrUndeclaredModel, provider, typ, version)
	}
	if adapter == ClaudeName && provider == "anthropic" {
		return "claude-" + typ + "-" + strings.ReplaceAll(version, ".", "-"), nil
	}
	return "", fmt.Errorf("%w: %s does not translate %s %s %s", ErrUndeclaredModel, adapter, provider, typ, version)
}

// AcceptsEffort reports whether adapter can start effort. An empty effort is
// accepted for every adapter, because the role named none. A named effort is
// accepted for Claude, which passes it as --effort, and refused for every
// other adapter.
func AcceptsEffort(adapter, effort string) error {
	if effort == "" {
		return nil
	}
	switch effort {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("%w: effort %q is not low, medium, or high", ErrUndeclaredModel, effort)
	}
	if adapter == ClaudeName {
		return nil
	}
	return fmt.Errorf("%w: %s does not translate effort %s", ErrUndeclaredModel, adapter, effort)
}
