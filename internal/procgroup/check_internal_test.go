package procgroup

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
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
