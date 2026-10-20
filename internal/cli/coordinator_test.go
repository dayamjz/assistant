package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/machine"
)

// TestTheCoordinatorVerbServesItsTableAndNothingElse is the boundary around
// the third command this surface answers to beside PRD section 9's table, on
// TestTheGateVerbServesTheHooksAndNothingElse's terms.
//
// Every subcommand the table dispatches answers --help with its own flags,
// in each spelling and as a document under --json, which is what a
// coordinator reads to learn a subcommand without running it. Nothing else
// under the verb is served: no subcommand, a word that is not one, and a
// subcommand written after a flag are each incorrect usage, because the
// subcommand comes first. And the command line is checked before the
// service is asked anything: each refusal below is reached with no service
// at all, among them a wait whose bound is outside
// machine.WakeWaitRequest.Bounds' range or not whole seconds, while a
// well-formed request with none, a wait at either end of that range
// included, is the operational failure that names how to start one.
func TestTheCoordinatorVerbServesItsTableAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	rows := cli.Subcommands()["coordinator"]
	if len(rows) == 0 {
		t.Fatal("the coordinator verb has no subcommands to check")
	}
	// The operands each subcommand's usage line names, which its flag set
	// cannot say for itself.
	operands := map[string]string{"message": "<task> ", "stop": "<task> ", "relaunch": "<task> ", "promote": "<task> ", "acknowledge": "<wake>... ", "delegate": "<domain> ", "end-delegation": "<domain> ", "tasks": "[<task>] ", "answer": "<task> "}
	for _, row := range rows {
		for _, flag := range []string{"--help", "-h", "-help"} {
			got := run(t, h, subject, "coordinator", row[0], flag)
			if got.code != machine.ExitOK || !strings.HasPrefix(got.stdout, "Usage: assistant coordinator "+row[0]+" "+operands[row[0]]) ||
				!strings.Contains(got.stdout, "-json") {
				t.Fatalf("assistant coordinator %s %s exited %s with:\n%s%s", row[0], flag, got.code, got.stdout, got.stderr)
			}
		}
		structured := run(t, h, subject, "--json", "coordinator", row[0], "--help")
		var help machine.Help
		if structured.code != machine.ExitOK || json.Unmarshal([]byte(structured.stdout), &help) != nil ||
			!strings.Contains(help.Usage, "assistant coordinator "+row[0]) {
			t.Fatalf("assistant --json coordinator %s --help exited %s with:\n%s", row[0], structured.code, structured.stdout)
		}
	}
	// The top-level help names the verb with the rest of the command list.
	if got := run(t, h, subject, "--help"); !strings.Contains(got.stdout, "coordinator "+rows[0][0]) {
		t.Fatalf("assistant --help does not list the coordinator's subcommands:\n%s", got.stdout)
	}

	both := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(both, []byte("Do it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"coordinator"},
		{"coordinator", "start"},
		{"coordinator", "--json", "wakes"},
		{"coordinator", "--text", "stop"},
		{"coordinator", "message"},
		{"coordinator", "message", "abcdefghij23", "abcdefghij24", "--text", "x"},
		{"coordinator", "stop"},
		{"coordinator", "relaunch", "abcdefghij23", "again"},
		{"coordinator", "promote"},
		{"coordinator", "tasks", "abcdefghij23", "abcdefghij24"},
		{"coordinator", "answer"},
		{"coordinator", "answer", "abcdefghij23"},
		{"coordinator", "answer", "abcdefghij23", "--answer", ""},
		{"coordinator", "answer", "abcdefghij23", "abcdefghij24", "--answer", "retry"},
		{"coordinator", "create", "--instructions", "Do it.", "--instructions-file", both},
		{"coordinator", "create", "--instructions-file", filepath.Join(t.TempDir(), "missing.md")},
		{"coordinator", "message", "abcdefghij23", "--text", "x", "--text-file", both},
		{"coordinator", "acknowledge"},
		{"coordinator", "acknowledge", "seven"},
		{"coordinator", "acknowledge", "dseven"},
		{"coordinator", "acknowledge", "x7"},
		{"coordinator", "wakes", "--limit", "-1"},
		{"coordinator", "await", "--timeout", "500ms"},
		{"coordinator", "await", "--timeout", "1500ms"},
		{"coordinator", "await", "--timeout", "-1s"},
		{"coordinator", "await", "--timeout", "61m"},
		{"coordinator", "await", "--timeout", "100000h"},
		{"coordinator", "await", "--limit", "-1"},
		{"coordinator", "await", "now"},
		{"coordinator", "listen", "now"},
		{"coordinator", "supervision", "now"},
	} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	for _, args := range [][]string{{"wakes"}, {"await", "--timeout", "1s"}, {"await", "--timeout", "1h"}, {"listen"}} {
		got := run(t, h, subject, append([]string{"--json", "coordinator"}, args...)...)
		var failure machine.Failure
		if got.code != machine.ExitFailure || json.Unmarshal([]byte(got.stdout), &failure) != nil ||
			!strings.Contains(failure.NextAction, "assistant service start") {
			t.Fatalf("assistant coordinator %s with no service exited %s with:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
}
