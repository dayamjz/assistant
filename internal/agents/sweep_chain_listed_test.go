//go:build linux || darwin

package agents_test

import (
	"slices"

	"github.com/dayamjz/assistant/internal/agents"
)

// onParentChain reports whether id is on this test process's parent chain,
// as the sweep's first carve-out reads it.
func onParentChain(id agents.Identity) bool { return slices.Contains(agents.Ancestry(), id) }
