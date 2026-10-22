//go:build !darwin && !linux

package journey

import (
	"io"
	"os/exec"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// buildCommand is the product-binary build on a platform where the life
// pipe is not wired. The closer does nothing. The context binaryBuildContext
// returns is still what asks Run to end the group, and a process that exits
// before Run handles cancellation leaves the build running, which
// procgroup's package comment states.
func buildCommand(built string) (*exec.Cmd, io.Closer, error) {
	return procgroup.Command("go", "build", "-o", built, "./cmd/assistant"), nopCloser{}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
