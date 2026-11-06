package agents

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// The line a new session answers is the pane's process and the server's,
// and nothing else is a creation a handle is returned over: one number,
// which is what the command answered before the host was read, three, a
// word, and a zero are each refused before any identity is read.
func TestTheTerminalAnswerIsTwoProcessIdentifiers(t *testing.T) {
	t.Parallel()
	pane, server, err := parseTerminalAnswer(" 4312 4311\n")
	if err != nil || pane != 4312 || server != 4311 {
		t.Fatalf("parseTerminalAnswer = %d, %d, %v; want the pane 4312 and the server 4311", pane, server, err)
	}
	for _, answer := range []string{"", "4312", "4312\n", "4312 4311 4310", "4312 server", "0 4311", "4312 -1"} {
		if pane, server, err := parseTerminalAnswer(answer); err == nil {
			t.Errorf("parseTerminalAnswer(%q) = %d, %d and no error; want a refusal", answer, pane, server)
		}
	}
}

// TmuxStartCommandSize must count the same argument vector tmuxNewSessionArgs
// builds, so a near-limit prompt cannot pass measurement and fail at start.
func TestTmuxStartCommandSizeMatchesTmuxNewSessionArgs(t *testing.T) {
	t.Parallel()
	// Validate refuses a socket past the platform limit. A fixed short path
	// keeps the measurement under that limit; the directory is never created,
	// because both sides only count the same arguments.
	root := "/tmp/tm"
	term := Terminal{Tmux: "/usr/bin/tmux", Socket: filepath.Join(root, "s")}
	bin := "/bin/echo"
	workerArgs := []string{"--model", "m", "hello"}
	size, err := TmuxStartCommandSize(term, root, bin, workerArgs)
	if err != nil {
		t.Fatal(err)
	}
	args, err := tmuxNewSessionArgs(term, root, bin, workerArgs)
	if err != nil {
		t.Fatal(err)
	}
	if got := commandSize(args); got != size {
		t.Fatalf("TmuxStartCommandSize = %d, commandSize(tmuxNewSessionArgs(...)) = %d", size, got)
	}
}

// A tmux client that runs past its bound is answered with what was ended:
// one procgroup.Run started was ended with the process group it led, and
// one procgroup.RunInCallersGroup started was ended alone, in its caller's
// group, which goes on (#784). Each is still ErrTerminalClientTimedOut.
func TestATimedOutTmuxClientSaysWhatWasEnded(t *testing.T) {
	t.Parallel()
	hang := filepath.Join(t.TempDir(), "hang")
	if err := procgroup.WriteProgram(hang, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		by        tmuxRun
		says, not string
	}{
		{"its own group", inOwnGroup, "with the process group it led", "alone"},
		{"its caller's group", inCallersGroup, "alone, in its caller's group", "process group"},
	} {
		err := runTmuxBy(context.Background(), c.by, 200*time.Millisecond, "the hanging client", procgroup.Command(hang))
		if !errors.Is(err, ErrTerminalClientTimedOut) {
			t.Fatalf("%s: answered %v, want ErrTerminalClientTimedOut", c.name, err)
		}
		if !strings.Contains(err.Error(), c.says) || strings.Contains(err.Error(), c.not) {
			t.Errorf("%s: answered %q, want it to say the client was ended %s and not %q", c.name, err, c.says, c.not)
		}
	}
}
