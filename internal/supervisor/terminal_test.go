package supervisor_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The terminal table is closed over taskstate.States() in both directions,
// on the in-flight table's terms. The oracle is PRD section 6 written down
// here: every terminal is read for a terminal gone except a task that
// resolves Reported, whose stored report outranks its terminal. A word
// outside the set is undecided and answered watched, never unwatched.
func TestEveryStateDecidesWhetherItsTerminalIsRead(t *testing.T) {
	t.Parallel()
	states := taskstate.States()
	unwatched := 0
	for _, s := range states {
		got, decided := supervisor.TerminalWatched(s)
		if !decided {
			t.Errorf("TerminalWatched(%q) is undecided; the table needs a row", s)
			continue
		}
		if want := s != taskstate.Reported; got != want {
			t.Errorf("TerminalWatched(%q) = %t, want %t", s, got, want)
		}
		if !got {
			unwatched++
		}
	}
	if unwatched != 1 {
		t.Fatalf("%d states leave their terminal unread, want exactly Reported", unwatched)
	}
	for _, counterfeit := range []taskstate.State{"", "delivered", "Reported"} {
		if counterfeit.Recognized() {
			t.Fatalf("%q is a State now; place it in the table and the oracle", counterfeit)
		}
		watched, decided := supervisor.TerminalWatched(counterfeit)
		if decided || !watched {
			t.Errorf("TerminalWatched(%q) = (%t, %t), want watched and undecided", counterfeit, watched, decided)
		}
	}
}
