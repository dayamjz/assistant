package procgroup

import (
	"fmt"
	"os/exec"
	"time"
)

// check is Run's refusal of a command it cannot run on its own terms, on
// every platform, so a caller written on one platform is refused on all of
// them alike.
func check(cmd *exec.Cmd, grace time.Duration) error {
	switch {
	case cmd.Process != nil:
		return fmt.Errorf("%w: it was already started", ErrMisconfigured)
	case cmd.Cancel != nil:
		return fmt.Errorf("%w: it was built with a context of its own", ErrMisconfigured)
	case cmd.SysProcAttr != nil:
		return fmt.Errorf("%w: its process attributes are already set", ErrMisconfigured)
	case grace <= 0:
		return fmt.Errorf("%w: a grace of %s bounds nothing", ErrMisconfigured, grace)
	}
	return nil
}
