package procgroup

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// The pipe refusal reads os/exec's record of the pipes it opened, so it is
// asked of a real exec.Cmd: none for a plain command, one for each of
// StdinPipe, StdoutPipe and StderrPipe. That is the positive control: a
// toolchain that renamed the field, or stopped recording a pipe in it, fails
// here rather than letting the refusal look at nothing.
func TestThePipeCountIsReadFromARealCommand(t *testing.T) {
	cmd := exec.Command("true")
	if n, known := closersIn(reflect.ValueOf(cmd).Elem(), "parentIOPipes"); !known || n != 0 {
		t.Fatalf("a plain command holds %d os/exec pipes (known %v), want 0 and known", n, known)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StdoutPipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.StderrPipe(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, f := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
			if c, ok := f.(interface{ Close() error }); ok {
				_ = c.Close()
			}
		}
	})
	if n, known := closersIn(reflect.ValueOf(cmd).Elem(), "parentIOPipes"); !known || n != 3 {
		t.Fatalf("a command with three os/exec pipes holds %d (known %v), want 3 and known", n, known)
	}
}

// The context refusal reads the field CommandContext assigns, so it is asked
// of a real exec.Cmd: unset for a plain command, set for one built with a
// context whose Cancel was then cleared and whose WaitDelay was set above
// zero. That is the positive control: a toolchain that renamed ctx, or
// stopped storing the context there, fails here rather than letting the
// refusal look at nothing.
func TestTheContextIsReadFromARealCommand(t *testing.T) {
	plain := exec.Command("true")
	if set, known := contextOf(reflect.ValueOf(plain).Elem()); !known || set {
		t.Fatalf("a plain command reads as context set %v known %v, want unset and known", set, known)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "true")
	cmd.Cancel = nil
	cmd.WaitDelay = time.Millisecond
	if set, known := contextOf(reflect.ValueOf(cmd).Elem()); !known || !set {
		t.Fatalf("a CommandContext with Cancel cleared reads as context set %v known %v, want set and known", set, known)
	}
}

// A context that cannot be read refuses the command as if it had one, so a
// renamed field fails every Run loudly instead of letting one through; only
// a context known to be absent runs.
func TestAnUnreadableContextRefuses(t *testing.T) {
	if _, known := contextOf(reflect.ValueOf(struct{ other any }{})); known {
		t.Fatal("a struct without ctx reads as known")
	}
	if _, known := contextOf(reflect.ValueOf(struct{ ctx int }{})); known {
		t.Fatal("a ctx that is not an interface reads as known")
	}
	for _, tc := range []struct {
		set   bool
		known bool
		want  error
	}{
		{false, false, ErrMisconfigured},
		{true, true, ErrMisconfigured},
		{false, true, nil},
	} {
		if err := contextRefusal(tc.set, tc.known); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("contextRefusal(%v, %v) = %v; want %v", tc.set, tc.known, err, tc.want)
		}
	}
}

// A count that cannot be read refuses the command as if it held a pipe, so
// a renamed field fails every Run loudly instead of letting one through;
// only a count known to be zero runs.
func TestAnUnreadablePipeCountRefuses(t *testing.T) {
	if _, known := closersIn(reflect.ValueOf(struct{ other []int }{}), "parentIOPipes"); known {
		t.Fatalf("a struct without the field reads as known")
	}
	if _, known := closersIn(reflect.ValueOf(struct{ parentIOPipes int }{}), "parentIOPipes"); known {
		t.Fatalf("a field that is not a slice reads as known")
	}
	for _, tc := range []struct {
		n     int
		known bool
		want  error
	}{
		{0, false, ErrMisconfigured},
		{1, true, ErrMisconfigured},
		{0, true, nil},
	} {
		if err := pipeRefusal(tc.n, tc.known); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("pipeRefusal(%d, %v) = %v; want %v", tc.n, tc.known, err, tc.want)
		}
	}
}
