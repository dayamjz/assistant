package agents

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
)

// ErrUndeclaredModel reports that the adapter has not declared a translation
// for the role's model triple, its context window on that triple, or its
// effort. The launch is refused and no other provider, type, version,
// window, or agent is started in its place.
var ErrUndeclaredModel = errors.New("agents: the adapter has not declared a translation for this model")

// claudeLongWindowCells is the anthropic cells the Claude adapter declares at
// config.Context1M, which it starts as the cell's identifier with "[1m]"
// appended. It declares config.ContextStandard on every anthropic cell, as
// the identifier with no suffix. Adding a cell is adding a row, once the
// window is known to run on it; the declaration is what this adapter asks
// for and nothing here checks that the installed CLI grants it.
var claudeLongWindowCells = map[config.ModelCell]bool{
	{Provider: "anthropic", Type: "opus", Version: "5.5"}: true,
}

// Translate returns the model identifier adapter starts for the role's
// provider, type, and version at its context window. A triple outside the
// role table, a window with no triple, and a triple or window the adapter
// has not declared, are ErrUndeclaredModel; a window outside
// config.ContextWindows is one no adapter declares. An empty window is the
// role naming none, and the identifier is the one the adapter starts with no
// window of its own.
//
// Claude translates every anthropic cell to claude-<type>-<version>, with a
// dot in the version written as a hyphen, and that identifier is also its
// standard window; at 1m, on a cell claudeLongWindowCells declares, it
// appends "[1m]". That identifier is what this adapter passes to the CLI.
// Codex translates every openai cell to gpt-<version> and declares no
// window. No translation establishes installed CLI model access, or that the
// started process has the window asked for.
func Translate(adapter, provider, typ, version, window string) (string, error) {
	if window != "" && provider == "" && typ == "" && version == "" {
		return "", fmt.Errorf("%w: context %s names no provider, type, and version", ErrUndeclaredModel, window)
	}
	if !config.KnownModel(provider, typ, version) {
		return "", fmt.Errorf("%w: %s %s %s is not in the model table", ErrUndeclaredModel, provider, typ, version)
	}
	if adapter == ClaudeName && provider == "anthropic" {
		id := "claude-" + typ + "-" + strings.ReplaceAll(version, ".", "-")
		switch {
		case window == "" || window == config.ContextStandard:
			return id, nil
		case window == config.Context1M && claudeLongWindowCells[config.ModelCell{Provider: provider, Type: typ, Version: version}]:
			return id + "[1m]", nil
		}
		return "", fmt.Errorf("%w: %s does not declare %s %s %s at context %s", ErrUndeclaredModel, adapter, provider, typ, version, window)
	}
	if adapter == CodexName && provider == "openai" {
		if window != "" {
			return "", fmt.Errorf("%w: %s does not declare %s %s %s at context %s", ErrUndeclaredModel, adapter, provider, typ, version, window)
		}
		return "gpt-" + version, nil
	}
	if window != "" {
		return "", fmt.Errorf("%w: %s does not translate %s %s %s at context %s", ErrUndeclaredModel, adapter, provider, typ, version, window)
	}
	return "", fmt.Errorf("%w: %s does not translate %s %s %s", ErrUndeclaredModel, adapter, provider, typ, version)
}

// AcceptsEffort reports whether adapter can start effort. An empty effort is
// accepted for every adapter, because the role named none. A named effort is
// accepted for Claude (--effort) and Codex (model_reasoning_effort), and
// refused for every other adapter.
func AcceptsEffort(adapter, effort string) error {
	if effort == "" {
		return nil
	}
	switch effort {
	case "low", "medium", "high":
	default:
		return fmt.Errorf("%w: effort %q is not low, medium, or high", ErrUndeclaredModel, effort)
	}
	if adapter == ClaudeName || adapter == CodexName {
		return nil
	}
	return fmt.Errorf("%w: %s does not translate effort %s", ErrUndeclaredModel, adapter, effort)
}
