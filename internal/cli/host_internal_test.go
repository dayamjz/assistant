package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The host adapter maps every answer onto exactly one of the host's two
// statuses: success when the turn may end, the block status when it may not.
// The walk is over internal/supervisor's whole reason set, with and without
// the re-entry signal, and over the uncomputed guard, so a reason added
// there is mapped from the next run.
func TestEveryVerdictMapsToExactlyOneExitCode(t *testing.T) {
	t.Parallel()
	if hostBlockCode == machine.ExitOK {
		t.Fatal("the block status is success, so a blocked turn would end")
	}
	seen := map[machine.Code]bool{}
	for _, reason := range append(supervisor.StopReasons(), "") {
		verdict := supervisor.StopVerdict{Reason: reason}
		for _, reEntered := range []bool{false, true} {
			answer := machine.HostStopOf(verdict, reEntered)
			code := hostExit(answer)
			seen[code] = true
			switch {
			case answer.Allowed && code != machine.ExitOK:
				t.Errorf("%q re-entered=%v is allowed and exits %s", reason, reEntered, code)
			case !answer.Allowed && code != hostBlockCode:
				t.Errorf("%q re-entered=%v is blocked and exits %s, want the host's block status", reason, reEntered, code)
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("the answers mapped onto %d statuses, want the host's two", len(seen))
	}
}

// The hook's command line quotes a path a shell would split or expand, and
// the reader that recognises this program's stop guard splits it back to the
// same words, so a binary or a home under a directory with a space or a quote
// in its name is named whole, and recognised as this program's when it is
// already there.
func TestTheHookCommandQuotesAndSplitsBackToTheSameWords(t *testing.T) {
	t.Parallel()
	for _, words := range [][]string{
		{"/usr/local/bin/assistant", "--home", "/home/someone/.assistant", "host", "stop"},
		{"/Users/some one/Application Support/assistant", "--home", "/Users/some one/it's here", "host", "stop"},
		{"/opt/a$b/assistant", "--home", "/tmp/x;y&z", "host", "stop"},
	} {
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = shellQuote(w)
		}
		line := strings.Join(quoted, " ")
		got, ok := shellWords(line)
		if !ok || !slices.Equal(got, words) {
			t.Fatalf("%q split into %q (%v), want %q", line, got, ok, words)
		}
		if !runsThisStopGuard(line, words[0]) {
			t.Fatalf("%q is not recognised as this program's stop guard", line)
		}
	}
	if runsThisStopGuard("~/.claude/stop-hook-git-check.sh", "/usr/local/bin/assistant") {
		t.Fatal("another program's turn-end hook is read as this program's")
	}
	if _, ok := shellWords("'unterminated"); ok {
		t.Fatal("an unterminated quote split")
	}
}
