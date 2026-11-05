package agents

import (
	"path/filepath"
	"testing"
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

// A Claude Code worker held again removes, when it ends, only a directory
// its start could have made: the server's answer must name a direct child of
// the temporary directory carrying seedClaudeConfig's prefix. Anything else
// the answer names, the temporary directory itself, a path that climbs out
// of it, a nested path, another prefix, a relative path, another variable or
// none, is refused, so nothing a server says can have a directory outside
// that set removed.
func TestAnAdoptedClaudeWorkerRemovesOnlyAConfigurationDirectoryItsStartMakes(t *testing.T) {
	t.Parallel()
	temp := "/var/tmp/x"
	if got := claudeConfigOf("CLAUDE_CONFIG_DIR=/var/tmp/x/assistant-claude-123\n", temp); got != "/var/tmp/x/assistant-claude-123" {
		t.Fatalf("claudeConfigOf of a directory the start makes = %q", got)
	}
	if got := claudeConfigOf("CLAUDE_CONFIG_DIR=/var/tmp/x/assistant-claude-9", temp+"/"); got != "/var/tmp/x/assistant-claude-9" {
		t.Fatalf("claudeConfigOf with a temporary directory ending in a slash = %q", got)
	}
	for _, answer := range []string{
		"", "-CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR=", "CLAUDE_CONFIG_DIR=/var/tmp/x",
		"CLAUDE_CONFIG_DIR=/var/tmp/x/assistant-claude-1/..", "CLAUDE_CONFIG_DIR=/var/tmp/x/../assistant-claude-1",
		"CLAUDE_CONFIG_DIR=/var/tmp/x/a/assistant-claude-1", "CLAUDE_CONFIG_DIR=/var/tmp/x/other-1",
		"CLAUDE_CONFIG_DIR=assistant-claude-1", "HOME=/var/tmp/x/assistant-claude-1",
	} {
		if got := claudeConfigOf(answer, temp); got != "" {
			t.Errorf("claudeConfigOf(%q) = %q, want nothing removed", answer, got)
		}
	}
}
