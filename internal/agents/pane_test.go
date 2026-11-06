package agents_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// A pane is named by an absolute tmux, an absolute socket and tmux's pane
// shape, and anything else is refused before a client starts: a session or
// window name would let the wake type into whatever tmux resolves it to.
// The same pane with each field right is the positive control.
func TestAPaneIsRefusedUnlessItsNameIsTmuxsPaneShape(t *testing.T) {
	t.Parallel()
	good := agents.Pane{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: "%12"}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-named pane was refused: %v", err)
	}
	for _, bad := range []agents.Pane{
		{Tmux: "tmux", Socket: "/tmp/s", ID: "%1"},
		{Tmux: "/usr/bin/tmux", Socket: "s", ID: "%1"},
		{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: ""},
		{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: "%"},
		{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: "coord"},
		{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: "coord:0.1"},
		{Tmux: "/usr/bin/tmux", Socket: "/tmp/s", ID: "%1x"},
	} {
		if err := bad.Validate(); !errors.Is(err, agents.ErrInvalidInvocation) {
			t.Errorf("pane %+v was not refused as an invalid invocation: %v", bad, err)
		}
	}
}

// Text the wake types has to be one line of printable text that a host
// reads as a message, so a line break, a control character, or a leading
// slash or exclamation mark is refused before any client starts. The
// tmux named here does not exist, so a refusal that reached a client would
// fail some other way.
func TestTypingIntoAPaneRefusesTextAHostWouldNotReadAsAMessage(t *testing.T) {
	t.Parallel()
	pane := agents.Pane{Tmux: "/nonexistent/tmux", Socket: "/tmp/s", ID: "%1"}
	for _, text := range []string{"", "two\nlines", "a\x1b[2Jclear", "/clear", "!rm -rf x", "\xff"} {
		if err := agents.TypeIntoPane(t.Context(), pane, text); !errors.Is(err, agents.ErrInvalidInvocation) {
			t.Errorf("typing %q was not refused as an invalid invocation: %v", text, err)
		}
	}
	if err := agents.TypeIntoPane(t.Context(), pane, "A notice."); err == nil || errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("a message was refused as invalid, or typed into a tmux that does not exist: %v", err)
	}
}
