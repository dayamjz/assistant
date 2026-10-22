package procgroup

import (
	"fmt"
	"os/exec"
	"reflect"
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
	return pipeRefusal(closersIn(reflect.ValueOf(cmd).Elem(), "parentIOPipes"))
}

// pipeRefusal refuses a command holding n pipes os/exec opened through its
// StdinPipe, StdoutPipe or StderrPipe, and one for which known is false
// because that count could not be read. Only cmd.Wait closes this process's
// end of such a pipe, and Run does not always call it: the reap it leaves
// behind for ErrAbandoned does not, so the end would stay open. os/exec also
// closes that end in a Wait made before the caller's reads of it are done,
// which Run's own Wait would be. A reader or writer handed to Run as the
// stream is copied by Run instead, with every wait bounded.
func pipeRefusal(n int, known bool) error {
	switch {
	case !known:
		return fmt.Errorf("%w: whether os/exec opened a pipe for it cannot be read, so it is refused as if it had", ErrMisconfigured)
	case n > 0:
		return fmt.Errorf("%w: a stream is a pipe from its StdinPipe, StdoutPipe or StderrPipe, which only cmd.Wait closes; give Run the reader or writer itself", ErrMisconfigured)
	}
	return nil
}

// closersIn returns the length of the slice field name of the struct v, and
// whether v has a slice field of that name. It reads an unexported field of
// exec.Cmd, os/exec's record of the pipe ends it must close in Wait, which
// os/exec does not export; the pinned toolchain's field is what check reads,
// and known is false, and the command refused, if a toolchain renames it.
func closersIn(v reflect.Value, name string) (n int, known bool) {
	f := v.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.Slice {
		return 0, false
	}
	return f.Len(), true
}
