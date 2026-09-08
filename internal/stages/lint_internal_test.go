package stages

import (
	"strings"
	"testing"
)

// The status the lint stage reads as "no analysis ran" has to be the status
// this platform's interpreter actually answers with, so it is taken from a
// real interpreter here rather than stated as a number and believed.
//
// That is the whole point of the test. interpreterCouldNotRun is a small table
// of exit statuses, and a table checked only against itself would keep passing
// after the platform it describes stopped agreeing with it. The command below
// is a name nothing provides, run through the same shellCommand the stage runs
// a configured command through.
//
// The two controls are what keep it from passing vacuously. A command that
// ran and objected must not be recognized, or every failing lint run would be
// read as a tool that is not installed and no violation would ever reach a
// fixer; and a command that passed must not be recognized either.
func TestTheInterpretersCouldNotRunStatusIsWhatTheTableRecognizes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		command string
		want    bool
	}{
		{"a name nothing provides", "assistant-no-such-linter-a3f9c2", true},
		{"a command that ran and objected", "exit 3", false},
		{"a command that passed", "exit 0", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var whole strings.Builder
			result := runCommand(t.Context(), commandSpec{
				command:    c.command,
				dir:        t.TempDir(),
				record:     &whole,
				projection: checkProjectionBytes,
			})
			if !result.exited {
				t.Fatalf("%q reported no exit status of its own, so this checks nothing: %v",
					c.command, result.err)
			}
			sense, got := interpreterCouldNotRun(result.code)
			if got != c.want {
				t.Fatalf("%q exited %d, which the table reads as a command the interpreter could "+
					"not run: %t, and this platform's interpreter means that: %t\nit wrote: %q",
					c.command, result.code, got, c.want, whole.String())
			}
			if got && strings.TrimSpace(sense) == "" {
				t.Fatalf("%q exited %d and the table recognizes it but says nothing about what that "+
					"means, which is what the finding is built from", c.command, result.code)
			}
		})
	}
}
