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

// TestTheWorkerVerbServesItsTableAndNothingElse is the boundary around the
// fourth command this surface answers to beside PRD section 9's table, on
// TestTheCoordinatorVerbServesItsTableAndNothingElse's terms.
//
// Every subcommand the table dispatches answers --help with its own flags,
// in each spelling and as a document under --json. Nothing else under the
// verb is served: no subcommand, a word that is not one, a subcommand
// written after a flag, a message number that is not a number, a report
// with no --file, an unreadable one and an empty one, and an event naming
// no kind are each incorrect usage, reached with no service at all, while a
// well-formed request with none is the operational failure that names how
// to start one. The route the service start gives a worker is this binary,
// --home and the verb, quoted only where a path needs it.
func TestTheWorkerVerbServesItsTableAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	rows := cli.Subcommands()["worker"]
	if len(rows) == 0 {
		t.Fatal("the worker verb has no subcommands to check")
	}
	operands := map[string]string{"acknowledge": "<message> ", "event": "<kind> "}
	for _, row := range rows {
		for _, flag := range []string{"--help", "-h", "-help"} {
			got := run(t, h, subject, "worker", row[0], flag)
			if got.code != machine.ExitOK || !strings.HasPrefix(got.stdout, "Usage: assistant worker "+row[0]+" "+operands[row[0]]) ||
				!strings.Contains(got.stdout, "-json") {
				t.Fatalf("assistant worker %s %s exited %s with:\n%s%s", row[0], flag, got.code, got.stdout, got.stderr)
			}
		}
		structured := run(t, h, subject, "--json", "worker", row[0], "--help")
		var help machine.Help
		if structured.code != machine.ExitOK || json.Unmarshal([]byte(structured.stdout), &help) != nil ||
			!strings.Contains(help.Usage, "assistant worker "+row[0]) {
			t.Fatalf("assistant --json worker %s --help exited %s with:\n%s", row[0], structured.code, structured.stdout)
		}
	}
	if got := run(t, h, subject, "--help"); !strings.Contains(got.stdout, "worker "+rows[0][0]) {
		t.Fatalf("assistant --help does not list the worker's subcommands:\n%s", got.stdout)
	}

	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"worker"},
		{"worker", "start"},
		{"worker", "--json", "acknowledge", "3"},
		{"worker", "acknowledge"},
		{"worker", "acknowledge", "three"},
		{"worker", "acknowledge", "3", "4"},
		{"worker", "report"},
		{"worker", "report", "--file", filepath.Join(t.TempDir(), "missing.json")},
		{"worker", "report", "--file", empty},
		{"worker", "event"},
		{"worker", "event", "working", "again"},
	} {
		if got := run(t, h, subject, args...); got.code != machine.ExitUsage {
			t.Fatalf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	got := run(t, h, subject, "--json", "worker", "event", "working")
	var failure machine.Failure
	if got.code != machine.ExitFailure || json.Unmarshal([]byte(got.stdout), &failure) != nil ||
		!strings.Contains(failure.NextAction, "assistant service start") {
		t.Fatalf("assistant worker event with no service exited %s with:\n%s%s", got.code, got.stdout, got.stderr)
	}

	for _, c := range []struct{ executable, root, want string }{
		{"/usr/local/bin/assistant", "/home/me/.assistant", "/usr/local/bin/assistant --home /home/me/.assistant worker"},
		{"/opt/my tools/assistant", "/home/me/it's", `'/opt/my tools/assistant' --home '/home/me/it'\''s' worker`},
	} {
		if got := cli.WorkerRoute(c.executable, c.root); got != c.want {
			t.Errorf("the route for %q serving %q is %q, want %q", c.executable, c.root, got, c.want)
		}
	}
}
