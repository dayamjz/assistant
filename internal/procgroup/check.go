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
	v := reflect.ValueOf(cmd).Elem()
	switch {
	case cmd.Process != nil:
		return fmt.Errorf("%w: it was already started", ErrMisconfigured)
	case cmd.Cancel != nil:
		return fmt.Errorf("%w: it was built with a context of its own", ErrMisconfigured)
	}
	// CommandContext stores its context in the unexported field ctx, which
	// stays set when the caller sets Cancel back to nil.
	if err := contextRefusal(contextOf(v)); err != nil {
		return err
	}
	switch {
	case cmd.SysProcAttr != nil:
		return fmt.Errorf("%w: its process attributes are already set", ErrMisconfigured)
	case grace <= 0:
		return fmt.Errorf("%w: a grace of %s bounds nothing", ErrMisconfigured, grace)
	}
	return pipeRefusal(closersIn(v, "parentIOPipes"))
}

// contextOf reports whether v, an exec.Cmd, carries the context
// exec.CommandContext stores, and whether that fact could be read.
//
// On toolchain go1.26.9, CommandContext (src/os/exec/exec.go) assigns the
// unexported field ctx and sets Cancel. That constructor's doc says the
// caller may change Cancel and WaitDelay before starting, and setting
// Cancel to nil leaves ctx set. Start then launches watchCtx when Cancel
// is non-nil or WaitDelay is non-zero, and ctx is non-nil and its Done
// channel is non-nil. Wait is what joins that goroutine. known is false
// when ctx is absent or not an interface, which is a toolchain that renamed
// or retyped the field.
func contextOf(v reflect.Value) (set, known bool) {
	f := v.FieldByName("ctx")
	if !f.IsValid() || f.Kind() != reflect.Interface {
		return false, false
	}
	return !f.IsNil(), true
}

// contextRefusal refuses a command whose contextOf result is set, and one
// for which known is false because that field could not be read. On the
// pinned toolchain, Start launches watchCtx when that context is set, its
// Done channel is non-nil, and either Cancel is set or WaitDelay is above
// zero. watchCtx stays for as long as the context does. Once the context
// ends, a nonzero WaitDelay makes it kill the leader and then send on a
// channel only cmd.Wait receives. The reap ErrAbandoned leaves does not
// call cmd.Wait, so that send blocks and the goroutine holds the command.
// Refusing every stored context covers that shape, including Cancel
// cleared with WaitDelay left set.
func contextRefusal(set, known bool) error {
	switch {
	case !known:
		return fmt.Errorf("%w: whether it was built with a context cannot be read, so it is refused as if it had", ErrMisconfigured)
	case set:
		return fmt.Errorf("%w: it was built with a context of its own", ErrMisconfigured)
	}
	return nil
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
