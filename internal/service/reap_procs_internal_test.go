//go:build linux || darwin

package service

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

// matchingCommandLines is what a kill matched on a command line would reach:
// every process of this user whose command line, its arguments joined by
// spaces, contains pattern. It is the positive control's half and nothing in
// the product does it. The command lines are read from the kernel
// (standin.ProcessesMatching) rather than with ps, which is set-user-ID on
// macOS and so cannot start under CI's per-package leftover check.
func matchingCommandLines(t *testing.T, pattern string) map[int]bool {
	t.Helper()
	listed, err := standin.ProcessesMatching(func(args []string) bool {
		return strings.Contains(strings.Join(args, " "), pattern)
	})
	if err != nil {
		t.Fatalf("listing processes for the positive control: %v", err)
	}
	matched := map[int]bool{}
	for _, p := range listed {
		matched[p.PID] = true
	}
	return matched
}
