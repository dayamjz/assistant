//go:build unix && !linux && !darwin

package agents_test

import "github.com/dayamjz/assistant/internal/agents"

// onParentChain reports false: this platform has no working-directory
// listing, so requiresSweep skips every test that would ask.
func onParentChain(agents.Identity) bool { return false }
